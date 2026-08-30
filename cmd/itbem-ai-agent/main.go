// itbem-ai-agent is the isolated local Go worker. Transport support is added
// in internal/automationagent; this entry point deliberately never starts the
// dashboard server or connects to its database.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"events-stocks/internal/automationagent"
	"github.com/gofrs/uuid"
)

func main() {
	smoke := flag.Bool("provider-smoke", false, "make one explicit non-sensitive provider request")
	doctor := flag.Bool("doctor", false, "validate the local workspace registry without calling a provider")
	syncWorkspaces := flag.Bool("sync-workspaces", false, "clone or fast-forward operator-managed workspace base checkouts")
	flag.Parse()
	if *syncWorkspaces {
		report, err := syncWorkspaceReport(context.Background(), os.Getenv)
		if err != nil {
			fail(err)
		}
		_ = json.NewEncoder(os.Stdout).Encode(report)
		return
	}
	if *doctor {
		report, ready, err := doctorReport(os.Getenv)
		if err != nil {
			fail(err)
		}
		_ = json.NewEncoder(os.Stdout).Encode(report)
		if !ready {
			os.Exit(1)
		}
		return
	}
	if !*smoke {
		run()
		return
	}
	config, err := automationagent.LoadProviderConfig(os.Getenv)
	if err != nil {
		fail(err)
	}
	if os.Getenv("ITBEM_AI_ALLOW_PROVIDER_SMOKE") != "1" {
		fail(fmt.Errorf("set ITBEM_AI_ALLOW_PROVIDER_SMOKE=1 before a billable provider smoke request"))
	}
	completion, err := automationagent.NewProviderClient(config, nil).Complete(context.Background(), []automationagent.Message{{Role: "system", Content: "You are a provider connectivity check. Reply with exactly ITBEM_PROVIDER_OK."}, {Role: "user", Content: "Connectivity check."}}, 256)
	if err != nil {
		fail(err)
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"provider": completion.Provider, "configured_model": config.Model, "reported_model": completion.Model, "response_id": completion.ResponseID, "usage": completion.Usage})
}

// syncWorkspaceReport is a local, explicit maintenance command. It deliberately
// runs before the queue worker starts, so no task can change a project checkout
// or silently invalidate a frozen Delivery context.
func syncWorkspaceReport(ctx context.Context, lookup func(string) string) (map[string]any, error) {
	workspaces, err := automationagent.LoadWorkspaceRegistry(lookup("ITBEM_AI_WORKSPACES_JSON"))
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(workspaces))
	for id := range workspaces {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	results := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		state, syncErr := automationagent.SyncManagedWorkspace(ctx, workspaces[id])
		if syncErr != nil {
			return nil, fmt.Errorf("workspace %s: %w", id, syncErr)
		}
		results = append(results, map[string]any{"id": id, "ready": state.Available && !state.HasLocalChanges, "head_sha": state.HeadSHA, "branch": state.Branch})
	}
	return map[string]any{"ready": true, "network_checks_made": true, "workspaces": results}, nil
}

// doctorReport performs only local, credential-free configuration checks. It
// never calls a model, AWS or GitHub. This prevents an operator from starting
// a worker that merely has a readable checkout but lacks the model key, queue
// runtime or callback contract required to process a Delivery task.
func doctorReport(lookup func(string) string) (map[string]any, bool, error) {
	diagnostics, err := automationagent.DiagnoseWorkspaces(lookup)
	if err != nil {
		return nil, false, err
	}
	workspacesReady := len(diagnostics) > 0
	for _, diagnostic := range diagnostics {
		workspacesReady = workspacesReady && diagnostic.Ready
	}
	provider := map[string]any{
		"ready": false, "status": "not_configured",
		"message": "Provider execution is disabled until a valid local provider configuration is present.",
	}
	runtime := map[string]any{
		"ready": false, "status": "not_configured",
		"message": "Runtime execution is disabled until queue, storage and callback configuration is present.",
	}
	runtimeReady := false
	var runtimeConfig automationagent.RuntimeConfig
	if config, runtimeErr := automationagent.LoadRuntimeConfig(lookup); runtimeErr == nil {
		runtimeReady = true
		runtimeConfig = config
		runtime = map[string]any{"ready": true, "status": "configured", "concurrency": config.Concurrency, "role": config.Role, "lane": config.Lane}
	}
	providerReady := false
	if runtimeReady && providerNotRequired(runtimeConfig) {
		providerReady = true
		provider = map[string]any{"ready": true, "status": "not_required", "message": "This deterministic release worker has no model-provider credential."}
	} else if config, providerErr := automationagent.LoadProviderConfig(lookup); providerErr == nil {
		providerReady = true
		provider = map[string]any{"ready": true, "status": "configured", "provider": config.Provider, "model": config.Model}
	}
	publication := map[string]any{"ready": true, "status": "configured"}
	githubAppReady := true
	if _, githubErr := automationagent.LoadGitHubAppConfig(lookup); githubErr != nil {
		githubAppReady = false
		status := "invalid"
		if errors.Is(githubErr, automationagent.ErrGitHubAppNotConfigured) {
			status = "not_configured"
		}
		// Never serialize the configuration error: its wording can differ by
		// runtime and must not become a route for leaking credential details.
		publication = map[string]any{
			"ready": false, "status": status,
			"message": "Remote publication remains disabled. Plan, implementation and QA stay available behind their human gates.",
		}
	}
	reviewIngress := doctorReviewIngress(lookup, githubAppReady, runtimeReady)
	publicationRequired := runtimeReady && providerNotRequired(runtimeConfig)
	ready := doctorExecutionReady(workspacesReady, providerReady, runtimeReady, githubAppReady, runtimeConfig)
	report := map[string]any{
		"ready":                ready,
		"workspaces_ready":     workspacesReady,
		"publication_required": publicationRequired,
		"provider":             provider,
		"runtime":              runtime,
		"publication":          publication,
		"review_ingress":       reviewIngress,
		"workspaces":           diagnostics,
		"provider_billable":    false,
		"network_checks_made":  false,
	}
	return report, ready, nil
}

func doctorExecutionReady(workspacesReady, providerReady, runtimeReady, githubAppReady bool, runtimeConfig automationagent.RuntimeConfig) bool {
	publicationRequired := runtimeReady && providerNotRequired(runtimeConfig)
	return workspacesReady && providerReady && runtimeReady && (!publicationRequired || githubAppReady)
}

// doctorReviewIngress is deliberately configuration-only. It never receives
// a GitHub delivery, mints an installation token, or reports any secret/app/
// repository identity. It tells an operator why automatic PR review is off
// before they start a long-lived worker.
func doctorReviewIngress(lookup func(string) string, githubAppReady, runtimeReady bool) map[string]any {
	secretConfigured := strings.TrimSpace(lookup("GITHUB_REVIEW_WEBHOOK_SECRET")) != ""
	repositories := 0
	for _, raw := range strings.Split(lookup("GITHUB_REVIEW_REPOSITORIES"), ",") {
		parts := strings.Split(strings.ToLower(strings.TrimSpace(raw)), "/")
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			repositories++
		}
	}
	if !secretConfigured && repositories == 0 {
		return map[string]any{"enabled": false, "ready": false, "status": "disabled", "allowed_repository_count": 0, "message": "Automatic pull-request review is disabled until a dedicated webhook secret and repository allow-list are configured."}
	}
	if !secretConfigured || repositories == 0 || !githubAppReady || !runtimeReady {
		return map[string]any{"enabled": true, "ready": false, "status": "incomplete", "allowed_repository_count": repositories, "message": "Automatic pull-request review is configured incompletely; require a webhook secret, allow-list, GitHub App and queue runtime."}
	}
	return map[string]any{"enabled": true, "ready": true, "status": "configured", "allowed_repository_count": repositories}
}

type deterministicOnlyProvider struct{}

func (deterministicOnlyProvider) Complete(context.Context, []automationagent.Message, int) (automationagent.Completion, error) {
	return automationagent.Completion{}, fmt.Errorf("model execution is disabled for the deterministic release worker")
}

func providerNotRequired(config automationagent.RuntimeConfig) bool {
	return config.Role == "release_manager" && config.Lane == "release"
}

func providerForRuntime(config automationagent.RuntimeConfig, lookup func(string) string) (automationagent.ProviderClient, string, string, error) {
	if providerNotRequired(config) {
		return deterministicOnlyProvider{}, "", "", nil
	}
	providerConfig, err := automationagent.LoadProviderConfig(lookup)
	if err != nil {
		return nil, "", "", err
	}
	return automationagent.NewProviderClient(providerConfig, nil), string(providerConfig.Provider), providerConfig.Model, nil
}

func run() {
	runtimeConfig, err := automationagent.LoadRuntimeConfig(os.Getenv)
	if err != nil {
		fail(err)
	}
	provider, providerName, modelName, err := providerForRuntime(runtimeConfig, os.Getenv)
	if err != nil {
		fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runtime, err := automationagent.NewAWSRuntime(ctx, runtimeConfig)
	if err != nil {
		fail(err)
	}
	callback, err := automationagent.NewHTTPCallback(runtimeConfig.APIBaseURL, runtimeConfig.CallbackSecret, nil)
	if err != nil {
		fail(err)
	}
	worker, err := automationagent.NewWorker(runtimeConfig.WorkerConfig, automationagent.NewAWSObjectStore(runtime.S3), callback, provider)
	if err != nil {
		fail(err)
	}
	queue, err := automationagent.NewAWSQueue(runtime.SQS, runtimeConfig.QueueURL)
	if err != nil {
		fail(err)
	}
	slog.Info(
		"ITBEM Go AI agent started",
		"provider", providerName,
		"model", modelName,
		"concurrency", runtimeConfig.Concurrency,
		"role", runtimeConfig.Role,
		"lane", runtimeConfig.Lane,
		"queue_url", runtimeConfig.QueueURL,
		"sqs_endpoint", runtimeConfig.SQSEndpoint,
	)
	workerID := uuid.Must(uuid.NewV4()).String()
	startedAt := time.Now().UTC()
	go reportHeartbeats(ctx, callback, automationagent.AgentHeartbeat{WorkerID: workerID, Provider: providerName, Model: modelName, Role: string(runtimeConfig.Role), Lane: string(runtimeConfig.Lane), Concurrency: runtimeConfig.Concurrency, StartedAt: startedAt.Format(time.RFC3339)}, os.Getenv)
	if err := automationagent.RunQueue(ctx, worker, queue, runtimeConfig.Concurrency, slog.Default()); err != nil {
		fail(err)
	}
}

func reportHeartbeats(ctx context.Context, callback *automationagent.HTTPCallback, heartbeat automationagent.AgentHeartbeat, lookup func(string) string) {
	report := func() {
		current := heartbeat
		readiness, readinessErr := automationagent.WorkspaceReadinessSnapshot(lookup)
		if readinessErr != nil {
			// A heartbeat must remain a liveness signal even if a developer moves
			// or reconfigures a local workspace. Do not serialize the raw error: it
			// may include a local path. The empty readiness snapshot instead makes
			// the dashboard surface an explicit unknown/preflight-required state.
			slog.Warn("ITBEM agent workspace readiness check failed", "error", readinessErr)
		} else {
			current.WorkspaceReadiness = readiness
		}
		requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := callback.Heartbeat(requestCtx, current); err != nil && ctx.Err() == nil {
			slog.Warn("ITBEM agent heartbeat failed", "error", err)
		}
	}
	report()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			report()
		}
	}
}

func fail(err error) { fmt.Fprintln(os.Stderr, "itbem-ai-agent:", err); os.Exit(1) }
