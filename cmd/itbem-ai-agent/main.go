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
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"events-stocks/internal/agentcallbackauth"
	"events-stocks/internal/agentprotocol"
	"events-stocks/internal/agentwork"
	"events-stocks/internal/automationagent"
	"github.com/gofrs/uuid"
)

func main() {
	smoke := flag.Bool("provider-smoke", false, "make one explicit non-sensitive provider request")
	authProbe := flag.Bool("provider-auth-probe", false, "validate central inference gateway configuration without calling a provider")
	runtimeAuthProbe := flag.Bool("runtime-auth-probe", false, "verify the configured runtime transport without consuming work")
	githubAuthProbe := flag.Bool("github-auth-probe", false, "verify the role-specific GitHub App installation with bounded read-only access")
	doctor := flag.Bool("doctor", false, "validate the local workspace registry without calling a provider")
	syncWorkspaces := flag.Bool("sync-workspaces", false, "clone or fast-forward operator-managed workspace base checkouts")
	provisionWorkspaces := flag.Bool("provision-workspaces", false, "provision missing authorized checkouts without modifying existing bases")
	showMachineIdentity := flag.Bool("show-machine-identity", false, "display the local machine ID and public key for administrator registration")
	ensureRegistered := flag.Bool("ensure-registered", false, "automatically register this machine using its role/lane gateway token")
	flag.Parse()
	if *showMachineIdentity {
		report, err := machineIdentityReport(os.Getenv)
		if err != nil {
			fail(err)
		}
		_ = json.NewEncoder(os.Stdout).Encode(report)
		return
	}
	if *ensureRegistered {
		instanceID, err := automationagent.EnsureGatewayAgentInstance(context.Background(), os.Getenv)
		if err != nil {
			fail(err)
		}
		status := "registered"
		if instanceID == "" {
			status = "not_required_for_aws_transport"
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"ready": true, "status": status, "instance_id": instanceID})
		return
	}
	if *provisionWorkspaces {
		if err := automationagent.SyncCentralWorkspaceCatalog(context.Background(), os.Getenv); err != nil {
			// A failed dependency job is not retried by Restart=on-failure on the
			// main unit. Keep the dependency successful and let its doctor fail
			// closed/retry when there is no still-fresh authorized cache.
			if !json.Valid([]byte(automationagent.ConfiguredWorkspaceRegistry(os.Getenv))) {
				_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"ready": false, "status": "workspace_catalog_pending", "provider_billable": false})
				return
			}
			slog.Warn("Workspace catalog reconciliation deferred; retaining fresh authorized cache")
		}
		if err := automationagent.ProvisionRegisteredWorkspaces(context.Background(), os.Getenv); err != nil {
			fail(err)
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"ready": true, "status": "registered_workspaces_provisioned", "provider_billable": false})
		return
	}
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
	if *authProbe {
		if config, runtimeErr := automationagent.LoadRuntimeConfig(os.Getenv); runtimeErr == nil && providerNotRequired(config) {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"ready": true, "status": "not_required", "network_checks_made": false, "provider_billable": false})
			return
		}
		config, err := automationagent.LoadGatewayProviderConfig(os.Getenv)
		if err != nil {
			fail(err)
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"ready": true, "status": "configured_unverified", "provider": config.Provider, "model": config.Model, "network_checks_made": false, "provider_billable": false})
		return
	}
	if *runtimeAuthProbe {
		config, err := automationagent.LoadRuntimeConfig(os.Getenv)
		if err != nil {
			fail(err)
		}
		if config.Transport == "gateway" {
			gateway, gatewayErr := automationagent.NewHTTPGateway(config.APIBaseURL, config.GatewayToken, config.Role, config.Lane, nil)
			if gatewayErr != nil {
				fail(gatewayErr)
			}
			if gatewayErr = gateway.Probe(context.Background()); gatewayErr != nil {
				fail(gatewayErr)
			}
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"ready": true, "transport": "gateway", "aws_credentials_required": false, "network_checks_made": true})
		} else {
			runtime, runtimeErr := automationagent.NewAWSRuntime(context.Background(), config)
			if runtimeErr != nil {
				fail(fmt.Errorf("AWS runtime authentication probe could not initialize"))
			}
			report, probeErr := automationagent.ProbeRuntimeAuth(context.Background(), config, runtime.SQS)
			if probeErr != nil {
				fail(probeErr)
			}
			_ = json.NewEncoder(os.Stdout).Encode(report)
			if !report.Ready {
				os.Exit(1)
			}
		}
		return
	}
	if *githubAuthProbe {
		runtimeConfig, err := automationagent.LoadRuntimeConfig(os.Getenv)
		if err != nil {
			fail(err)
		}
		report, err := githubAuthProbeReport(context.Background(), runtimeConfig, os.Getenv, func(ctx context.Context, config automationagent.GitHubAppConfig) error {
			return automationagent.VerifyGitHubAppInstallation(ctx, config, nil, time.Now().UTC())
		})
		if err != nil {
			fail(err)
		}
		_ = json.NewEncoder(os.Stdout).Encode(report)
		return
	}
	if !*smoke {
		run()
		return
	}
	provider, err := loadExecutionProvider(os.Getenv)
	if err != nil {
		fail(err)
	}
	if os.Getenv("ITBEM_AI_ALLOW_PROVIDER_SMOKE") != "1" {
		fail(fmt.Errorf("set ITBEM_AI_ALLOW_PROVIDER_SMOKE=1 before a billable provider smoke request"))
	}
	completion, err := provider.client.Complete(context.Background(), []automationagent.Message{{Role: "system", Content: "You are a provider connectivity check. Reply with exactly ITBEM_PROVIDER_OK."}, {Role: "user", Content: "Connectivity check."}}, 256)
	if err != nil {
		fail(err)
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"provider": completion.Provider, "configured_model": provider.model, "reported_model": completion.Model, "response_id": completion.ResponseID, "usage": completion.Usage})
}

type githubInstallationVerifier func(context.Context, automationagent.GitHubAppConfig) error

func githubAuthProbeReport(ctx context.Context, runtimeConfig automationagent.RuntimeConfig, lookup func(string) string, verify githubInstallationVerifier) (map[string]any, error) {
	sourceRequired, err := automationagent.GitHubSourceAccessRequired(lookup)
	if err != nil {
		return nil, fmt.Errorf("GitHub source workspace registry is invalid")
	}
	publicationRequired := githubPublicationRequired(runtimeConfig)
	if !publicationRequired && !sourceRequired {
		return map[string]any{"ready": true, "status": "not_required", "network_checks_made": false, "provider": "github_app"}, nil
	}
	if verify == nil {
		return nil, fmt.Errorf("GitHub App installation verifier is unavailable")
	}
	verifiedInstallations := 0
	var sourceConfig automationagent.GitHubAppConfig
	if sourceRequired {
		config, sourceErr := automationagent.LoadGitHubSourceAppConfig(lookup)
		if sourceErr != nil {
			return nil, fmt.Errorf("GitHub Source App is not configured")
		}
		count, verifyErr := verifyGitHubAppInstallations(ctx, config, verify)
		if verifyErr != nil {
			return nil, verifyErr
		}
		verifiedInstallations += count
		sourceConfig = config
	}
	if publicationRequired {
		config, publicationErr := automationagent.LoadGitHubAppConfig(lookup)
		if publicationErr != nil {
			return nil, fmt.Errorf("GitHub publication App is not configured")
		}
		if sourceRequired && strings.TrimSpace(config.AppID) == strings.TrimSpace(sourceConfig.AppID) {
			return nil, fmt.Errorf("GitHub Source and publication Apps must use distinct identities")
		}
		count, verifyErr := verifyGitHubAppInstallations(ctx, config, verify)
		if verifyErr != nil {
			return nil, verifyErr
		}
		verifiedInstallations += count
	}
	return map[string]any{"ready": true, "status": "authenticated", "network_checks_made": true, "provider": "github_app", "installation_count": verifiedInstallations, "source_required": sourceRequired}, nil
}

func verifyGitHubAppInstallations(ctx context.Context, config automationagent.GitHubAppConfig, verify githubInstallationVerifier) (int, error) {
	verifiedInstallations := 0
	for _, installationID := range config.InstallationIDs {
		candidate := config
		candidate.InstallationID = installationID
		if err := verify(ctx, candidate); err != nil {
			return 0, fmt.Errorf("GitHub App installation authentication failed")
		}
		verifiedInstallations++
	}
	return verifiedInstallations, nil
}

type executionProvider struct {
	client   automationagent.ProviderClient
	provider automationagent.Provider
	model    string
}

func loadExecutionProvider(lookup func(string) string) (executionProvider, error) {
	config, err := automationagent.LoadGatewayProviderConfig(lookup)
	if err != nil {
		return executionProvider{}, fmt.Errorf("ITBEM_AI_GATEWAY_URL and valid gateway provider configuration are required; local workers never call provider APIs directly: %w", err)
	}
	return executionProvider{client: automationagent.NewGatewayProviderClient(config, nil), provider: config.Provider, model: config.Model}, nil
}

func machineIdentityReport(lookup func(string) string) (map[string]string, error) {
	identity, err := automationagent.LoadLocalMachineIdentity(lookup("ITBEM_AI_MACHINE_ID"), lookup("ITBEM_AI_STATE_DIR"))
	if err != nil {
		return nil, err
	}
	publicKey, err := agentcallbackauth.EncodePublicKey(identity.PublicKey())
	if err != nil {
		return nil, fmt.Errorf("could not encode local machine public key")
	}
	return map[string]string{"machine_id": identity.MachineID(), "public_key": publicKey}, nil
}

// syncWorkspaceReport is a local, explicit maintenance command. It deliberately
// runs before the queue worker starts, so no task can change a project checkout
// or silently invalidate a frozen Delivery context.
func syncWorkspaceReport(ctx context.Context, lookup func(string) string) (map[string]any, error) {
	workspaces, err := automationagent.LoadWorkspaceRegistry(automationagent.ConfiguredWorkspaceRegistry(lookup))
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
		state, syncErr := automationagent.SyncAuthorizedManagedWorkspace(ctx, workspaces[id], lookup)
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
	if strings.EqualFold(lookup("ITBEM_AI_WORKSPACE_CATALOG_ENABLED"), "true") && json.Valid([]byte(automationagent.ConfiguredWorkspaceRegistry(lookup))) {
		workspacesReady = true
	}
	for _, diagnostic := range diagnostics {
		workspacesReady = workspacesReady && diagnostic.Ready
	}
	provider := map[string]any{
		"ready": false, "status": "not_configured",
		"message": "Provider execution is disabled until the central inference gateway is configured.",
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
	} else if config, providerErr := automationagent.LoadGatewayProviderConfig(lookup); providerErr == nil {
		providerReady = true
		provider = map[string]any{"ready": true, "status": "configured_unverified", "provider": config.Provider, "model": config.Model, "message": "Gateway configuration is valid; use --provider-auth-probe for local validation or --provider-smoke for an explicitly billable gateway request."}
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
	sourceAccess, sourceReady := doctorSourceAccess(lookup)
	reviewIngress := doctorReviewIngress(lookup, githubAppReady, runtimeReady)
	publicationRequired := runtimeReady && githubPublicationRequired(runtimeConfig)
	ready := doctorExecutionReady(workspacesReady, providerReady, runtimeReady, githubAppReady, runtimeConfig) && sourceReady
	report := map[string]any{
		"ready":                ready,
		"workspaces_ready":     workspacesReady,
		"publication_required": publicationRequired,
		"provider":             provider,
		"runtime":              runtime,
		"publication":          publication,
		"source_access":        sourceAccess,
		"review_ingress":       reviewIngress,
		"workspaces":           diagnostics,
		"provider_billable":    false,
		"network_checks_made":  false,
	}
	if attestation := automationagent.SandboxAttestationSnapshot(lookup); attestation != nil {
		report["sandbox_attestation"] = map[string]any{
			"ready": true, "status": "verified_evidence", "runtime": attestation.Runtime,
			"runtime_version": attestation.RuntimeVersion, "transport": attestation.Transport,
			"evidence_scope": attestation.EvidenceScope, "guest_command_verified": attestation.GuestCommandVerified,
			"evidence_digest": attestation.EvidenceDigest,
			"message":         "This is observational only; lifecycle ownership, worktree binding and task-level isolation are still verified separately.",
		}
	}
	return report, ready, nil
}

// doctorSourceAccess is strictly local configuration validation. It prevents a
// lane with a registered GitHub workspace from starting only to discover that
// it would need a developer SSH key or personal token. It never contacts
// GitHub and never serializes an App identity, installation, path or secret.
func doctorSourceAccess(lookup func(string) string) (map[string]any, bool) {
	required, err := automationagent.GitHubSourceAccessRequired(lookup)
	if err != nil {
		return map[string]any{"ready": false, "status": "invalid", "required": true, "message": "GitHub workspace registration is invalid."}, false
	}
	if !required {
		return map[string]any{"ready": true, "status": "not_required", "required": false}, true
	}
	if _, err := automationagent.LoadGitHubSourceAppConfig(lookup); err != nil {
		return map[string]any{"ready": false, "status": "not_configured", "required": true, "message": "GitHub source synchronization remains disabled until the dedicated read-only Source App is configured."}, false
	}
	return map[string]any{"ready": true, "status": "configured_unverified", "required": true, "message": "Authentication is verified by --github-auth-probe before worker activation."}, true
}

func doctorExecutionReady(workspacesReady, providerReady, runtimeReady, githubAppReady bool, runtimeConfig automationagent.RuntimeConfig) bool {
	if !agentwork.IsKnownRoleLane(runtimeConfig.Role, runtimeConfig.Lane) {
		return false
	}
	publicationRequired := runtimeReady && githubPublicationRequired(runtimeConfig)
	return workspacesReady && providerReady && runtimeReady && (!publicationRequired || githubAppReady)
}

func githubPublicationRequired(config automationagent.RuntimeConfig) bool {
	// An explicitly narrowed gateway observer never publishes and obtains all
	// GitHub evidence from the server. General release workers may still publish.
	if config.Role == "release_manager" && config.Lane == "release" && config.Transport == "gateway" && len(config.AllowedOperations) == 1 && config.AllowedOperations[0] == "delivery.release_gate" {
		return false
	}
	return (config.Role == "release_manager" && config.Lane == "release") ||
		(config.Role == "reviewer" && config.Lane == "review")
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

func run() {
	if _, err := automationagent.EnsureGatewayAgentInstance(context.Background(), os.Getenv); err != nil {
		fail(err)
	}
	runtimeConfig, err := automationagent.LoadRuntimeConfig(os.Getenv)
	if err != nil {
		fail(err)
	}
	providerConfig := executionProvider{}
	if providerNotRequired(runtimeConfig) {
		providerConfig = executionProvider{client: deterministicOnlyProvider{}}
	} else {
		providerConfig, err = loadExecutionProvider(os.Getenv)
		if err != nil {
			fail(err)
		}
	}
	callback, err := automationagent.NewHTTPCallback(runtimeConfig.APIBaseURL, runtimeConfig.CallbackIdentity, runtimeConfig.AgentInstanceID, nil)
	if err != nil {
		fail(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	workerID := uuid.Must(uuid.NewV4()).String()
	runtimeConfig.WorkerID = workerID
	var store automationagent.ObjectStore
	var queue automationagent.Queue
	if runtimeConfig.Transport == "gateway" {
		gateway, gatewayErr := automationagent.NewHTTPGateway(runtimeConfig.APIBaseURL, runtimeConfig.GatewayToken, runtimeConfig.Role, runtimeConfig.Lane, nil)
		if gatewayErr != nil {
			fail(gatewayErr)
		}
		store, queue = gateway, gateway
	} else {
		runtime, runtimeErr := automationagent.NewAWSRuntime(ctx, runtimeConfig)
		if runtimeErr != nil {
			fail(runtimeErr)
		}
		store = automationagent.NewAWSObjectStore(runtime.S3)
		queue, err = automationagent.NewAWSQueue(runtime.SQS, runtimeConfig.QueueURL)
		if err != nil {
			fail(err)
		}
	}
	worker, err := automationagent.NewWorker(runtimeConfig.WorkerConfig, store, callback, providerConfig.client)
	if err != nil {
		fail(err)
	}
	startedAt := time.Now().UTC()
	heartbeat := automationagent.AgentHeartbeat{Role: string(runtimeConfig.Role), Lane: string(runtimeConfig.Lane), WorkerID: workerID, AgentKey: runtimeConfig.AgentKey, MachineID: runtimeConfig.MachineID, Provider: string(providerConfig.provider), Model: providerConfig.model, Concurrency: runtimeConfig.Concurrency, Capabilities: runtimeConfig.AllowedOperations, Protocols: supportedRuntimeProtocols(), StartedAt: startedAt.Format(time.RFC3339)}
	if err := runQueueAfterEnrollment(ctx, callback, heartbeat, os.Getenv, func() error {
		slog.Info(
			"ITBEM Go AI agent started",
			"provider", providerConfig.provider,
			"model", providerConfig.model,
			"concurrency", runtimeConfig.Concurrency,
			"queue_url", runtimeConfig.QueueURL,
			"sqs_endpoint", runtimeConfig.SQSEndpoint,
		)
		var draining atomic.Bool
		drain := make(chan struct{})
		beginDrain := func() bool {
			if !draining.CompareAndSwap(false, true) {
				return false
			}
			close(drain)
			return true
		}
		startDrainTimeout := func() {
			timer := time.NewTimer(workerDrainTimeout)
			go func() {
				defer timer.Stop()
				select {
				case <-ctx.Done():
				case <-timer.C:
					slog.Warn("ITBEM agent drain deadline reached; cancelling remaining work")
					stop()
				}
			}()
		}
		go reportHeartbeats(ctx, callback, heartbeat, func() bool { return draining.Load() }, os.Getenv, func() {
			if beginDrain() {
				slog.Warn("ITBEM agent identity rejected; stopping new queue polls and draining active work")
				startDrainTimeout()
			}
		})
		go func() {
			select {
			case <-signals:
				if beginDrain() {
					slog.Info("ITBEM Go AI agent draining", "grace_seconds", int(workerDrainTimeout/time.Second))
					if err := sendHeartbeat(context.Background(), callback, heartbeat, true, os.Getenv); err != nil {
						slog.Warn("ITBEM draining heartbeat failed", "error", automationagent.RedactPublicError(err.Error()))
					}
					startDrainTimeout()
				}
			case <-ctx.Done():
			}
		}()
		return automationagent.RunQueueWithDrain(ctx, worker, queue, runtimeConfig.Concurrency, slog.Default(), drain)
	}); err != nil {
		stop()
		fail(err)
	}
}

func credentialDeliveryMode(lookup func(string) string) string {
	if automationagent.GatewayProviderEnabled(lookup) {
		return "cloud_gateway"
	}
	return "legacy_local_environment"
}

const workerDrainTimeout = 45 * time.Second
const maxEnrollmentHeartbeatRetries = 4

func supportedRuntimeProtocols() []string {
	return []string{agentprotocol.ProtocolDeliveryPlanStepsV1}
}

var enrollmentHeartbeatRetryDelays = [...]time.Duration{
	time.Second,
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
}

// runQueueAfterEnrollment performs the signed enrollment/profile check before
// invoking the queue loop. An unknown, revoked or mismatched instance therefore
// cannot claim work that would only fail when its first callback is submitted.
func runQueueAfterEnrollment(ctx context.Context, callback *automationagent.HTTPCallback, heartbeat automationagent.AgentHeartbeat, lookup func(string) string, runQueue func() error) error {
	return runQueueAfterEnrollmentWithWait(ctx, callback, heartbeat, lookup, runQueue, waitForEnrollmentRetry)
}

func runQueueAfterEnrollmentWithWait(ctx context.Context, callback *automationagent.HTTPCallback, heartbeat automationagent.AgentHeartbeat, lookup func(string) string, runQueue func() error, wait func(context.Context, time.Duration) error) error {
	if runQueue == nil {
		return fmt.Errorf("queue runner is unavailable")
	}
	if wait == nil {
		wait = waitForEnrollmentRetry
	}
	for attempt := 0; ; attempt++ {
		err := sendHeartbeat(ctx, callback, heartbeat, false, lookup)
		if err == nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return runQueue()
		}
		if isTerminalHeartbeatRejection(err) {
			return fmt.Errorf("registered machine identity or agent profile was rejected; refusing to poll the queue: %w", err)
		}
		if attempt >= maxEnrollmentHeartbeatRetries {
			return fmt.Errorf("could not verify the registered machine after temporary heartbeat failures; refusing to poll the queue: %w", err)
		}
		if waitErr := wait(ctx, enrollmentHeartbeatRetryDelays[attempt]); waitErr != nil {
			return fmt.Errorf("enrollment heartbeat retry was interrupted; refusing to poll the queue: %w", waitErr)
		}
	}
}

func waitForEnrollmentRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isRetryableHeartbeatRejection(err error) bool {
	var rejection *automationagent.HeartbeatRejectionError
	if !errors.As(err, &rejection) {
		// Network and timeout failures have no response status and are retried
		// with a bounded backoff before startup is abandoned.
		return true
	}
	switch rejection.StatusCode {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests:
		return true
	default:
		return rejection.StatusCode >= http.StatusInternalServerError && rejection.StatusCode <= 599
	}
}

func isTerminalHeartbeatRejection(err error) bool {
	var rejection *automationagent.HeartbeatRejectionError
	return errors.As(err, &rejection) && !isRetryableHeartbeatRejection(err)
}

func sendHeartbeat(ctx context.Context, callback *automationagent.HTTPCallback, heartbeat automationagent.AgentHeartbeat, draining bool, lookup func(string) string) error {
	current := heartbeat
	current.Draining = draining
	readiness, readinessErr := automationagent.WorkspaceReadinessSnapshot(lookup)
	if readinessErr != nil {
		// A heartbeat must remain a liveness signal even if a developer moves
		// or reconfigures a local workspace. Do not serialize the raw error: it
		// may include a local path. The empty readiness snapshot instead makes
		// the dashboard surface an explicit unknown/preflight-required state.
		slog.Warn("ITBEM agent workspace readiness check failed; diagnostics withheld")
	} else {
		current.WorkspaceReadiness = readiness
	}
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return callback.Heartbeat(requestCtx, current)
}

func reportHeartbeats(ctx context.Context, callback *automationagent.HTTPCallback, heartbeat automationagent.AgentHeartbeat, draining func() bool, lookup func(string) string, onTerminalRejection func()) {
	reportHeartbeatsAtInterval(ctx, callback, heartbeat, draining, lookup, 30*time.Second, onTerminalRejection)
}

func reportHeartbeatsAtInterval(ctx context.Context, callback *automationagent.HTTPCallback, heartbeat automationagent.AgentHeartbeat, draining func() bool, lookup func(string) string, interval time.Duration, onTerminalRejection func()) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := sendHeartbeat(ctx, callback, heartbeat, draining != nil && draining(), lookup)
			if err == nil || ctx.Err() != nil {
				continue
			}
			if isTerminalHeartbeatRejection(err) {
				slog.Error("ITBEM agent heartbeat rejected; stopping new queue polls and draining")
				if onTerminalRejection != nil {
					onTerminalRejection()
				}
				return
			}
			// Transport errors, 429s and 5xx responses are transient. Keep the
			// worker alive and retry on the next heartbeat without exposing any
			// response body or secret.
			slog.Warn("ITBEM agent heartbeat temporarily unavailable; will retry", "error", automationagent.RedactPublicError(err.Error()))
		}
	}
}

func fail(err error) {
	message := "startup failed; inspect private worker diagnostics"
	if err != nil {
		if safe := automationagent.RedactPublicError(err.Error()); safe != "" {
			message = safe
		}
	}
	fmt.Fprintln(os.Stderr, "itbem-ai-agent:", message)
	os.Exit(1)
}
