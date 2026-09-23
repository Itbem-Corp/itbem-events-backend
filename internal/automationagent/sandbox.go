package automationagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/uuid"
)

type sandboxTaskIDContextKey struct{}

func withSandboxTaskID(ctx context.Context, taskID string) context.Context {
	return context.WithValue(ctx, sandboxTaskIDContextKey{}, strings.TrimSpace(taskID))
}

func sandboxTaskID(ctx context.Context) string {
	if value, ok := ctx.Value(sandboxTaskIDContextKey{}).(string); ok {
		return value
	}
	return ""
}

func newSandboxLease(ctx context.Context, workspace Workspace, directory string) map[string]any {
	root, _ := filepath.Abs(directory)
	digest := sandboxWorktreeDigest(root)
	runtime := strings.ToLower(strings.TrimSpace(workspace.Config.SandboxRuntime))
	if runtime == "" {
		runtime = WorkspaceSandboxProcess
	}
	isolation := "host_process"
	switch runtime {
	case WorkspaceSandboxDocker:
		isolation = "docker_container"
	case WorkspaceSandboxFirecracker:
		isolation = "firecracker_microvm"
	}
	lease := map[string]any{
		"lease_id":        uuid.Must(uuid.NewV4()).String(),
		"task_id":         sandboxTaskID(ctx),
		"workspace":       "workspace://" + workspace.ID,
		"runtime":         runtime,
		"isolation_mode":  isolation,
		"worktree_digest": "sha256:" + hex.EncodeToString(digest[:]),
		"started_at":      time.Now().UTC().Format(time.RFC3339Nano),
		"status":          "running",
	}
	if runtime != WorkspaceSandboxFirecracker {
		if evidence := sandboxAttestation(os.Getenv("ITBEM_AI_SANDBOX_ATTESTATION_JSON")); evidence != nil {
			lease["sandbox_attestation"] = map[string]any{
				"runtime": evidence.Runtime, "runtime_version": evidence.RuntimeVersion,
				"transport": evidence.Transport, "evidence_scope": evidence.EvidenceScope,
				"guest_command_verified": evidence.GuestCommandVerified,
			}
		}
	}
	return lease
}

func sandboxWorktreeDigest(root string) [32]byte {
	return sha256.Sum256([]byte(strings.TrimSpace(root)))
}

func finishSandboxLease(lease map[string]any, runErr error) {
	if lease == nil {
		return
	}
	status := "completed"
	if runErr != nil {
		status = "failed"
	}
	lease["status"] = status
	lease["finished_at"] = time.Now().UTC().Format(time.RFC3339Nano)
}

// runWorkspaceCommand is the only entry point used for operator-registered
// validation and QA commands. Git plumbing intentionally stays on the host;
// this wrapper covers the untrusted repository-owned toolchain where the
// isolation boundary matters. Process mode remains explicit compatibility for
// existing workspaces, while Docker mode provides a real filesystem/process,
// network, CPU, memory, PID and temporary-disk boundary.
func runWorkspaceCommand(parent context.Context, workspace Workspace, directory string, timeout time.Duration, input string, environment map[string]string, command string, arguments ...string) (commandResult, error) {
	lease := newSandboxLease(parent, workspace, directory)
	runtime := strings.ToLower(strings.TrimSpace(workspace.Config.SandboxRuntime))
	if workspace.Config.RequireSandbox && runtime != WorkspaceSandboxDocker && runtime != WorkspaceSandboxFirecracker {
		err := fmt.Errorf("workspace requires Docker sandbox execution or Firecracker sandbox execution")
		finishSandboxLease(lease, err)
		return commandResult{SandboxLease: lease}, err
	}
	var result commandResult
	var runErr error
	if runtime == WorkspaceSandboxFirecracker {
		leaseID, _ := lease["lease_id"].(string)
		result, runErr = runFirecrackerSandboxCommand(parent, workspace, directory, leaseID, timeout, input, environment, command, arguments...)
	} else if runtime != WorkspaceSandboxDocker {
		result, runErr = runLocalWithEnv(parent, directory, timeout, input, environment, command, arguments...)
	} else {
		result, runErr = runDockerSandboxCommand(parent, workspace, directory, timeout, input, environment, command, arguments...)
	}
	if result.SandboxAttestation != nil {
		evidence := result.SandboxAttestation
		lease["sandbox_attestation"] = map[string]any{
			"runtime": evidence.Runtime, "runtime_version": evidence.RuntimeVersion,
			"transport": evidence.Transport, "evidence_scope": evidence.EvidenceScope,
			"guest_command_verified": evidence.GuestCommandVerified,
		}
	}
	if result.SandboxLifecycle != nil {
		lease["sandbox_lifecycle"] = result.SandboxLifecycle
	}
	finishSandboxLease(lease, runErr)
	result.SandboxLease = lease
	return result, runErr
}

type firecrackerSupervisorRequest struct {
	ProtocolVersion int               `json:"protocol_version"`
	Operation       string            `json:"operation"`
	LeaseID         string            `json:"lease_id"`
	TaskID          string            `json:"task_id"`
	WorkspaceID     string            `json:"workspace_id"`
	WorkspacePath   string            `json:"workspace_path"`
	WorktreeDigest  string            `json:"worktree_digest"`
	Command         string            `json:"command"`
	Args            []string          `json:"args,omitempty"`
	Input           string            `json:"input,omitempty"`
	TimeoutMS       int64             `json:"timeout_ms"`
	Environment     map[string]string `json:"environment,omitempty"`
}

type firecrackerSupervisorResponse struct {
	ProtocolVersion int                            `json:"protocol_version"`
	Operation       string                         `json:"operation"`
	LeaseID         string                         `json:"lease_id"`
	OK              bool                           `json:"ok"`
	ExitCode        int                            `json:"exit_code"`
	Stdout          string                         `json:"stdout,omitempty"`
	Stderr          string                         `json:"stderr,omitempty"`
	Error           string                         `json:"error,omitempty"`
	TaskID          string                         `json:"task_id"`
	WorkspaceID     string                         `json:"workspace_id"`
	WorktreeDigest  string                         `json:"worktree_digest"`
	Attestation     SandboxAttestation             `json:"attestation"`
	Lifecycle       firecrackerSupervisorLifecycle `json:"lifecycle"`
}

type firecrackerSupervisorLifecycle struct {
	Created              bool `json:"created"`
	WorktreeBound        bool `json:"worktree_bound"`
	GuestCommandExecuted bool `json:"guest_command_executed"`
	Destroyed            bool `json:"destroyed"`
	AttestationPersisted bool `json:"attestation_persisted"`
}

func runFirecrackerSandboxCommand(parent context.Context, workspace Workspace, directory, leaseID string, timeout time.Duration, input string, environment map[string]string, command string, arguments ...string) (commandResult, error) {
	taskID := sandboxTaskID(parent)
	if taskID == "" || strings.TrimSpace(leaseID) == "" {
		return commandResult{}, fmt.Errorf("firecracker sandbox requires a task-scoped context")
	}
	if len(workspace.Config.SandboxSupervisorCommand) == 0 {
		return commandResult{}, fmt.Errorf("firecracker sandbox supervisor is not configured")
	}
	if len(input) > maxCommandOutput {
		return commandResult{}, fmt.Errorf("firecracker supervisor input exceeds the bounded command limit")
	}
	root, err := filepath.Abs(directory)
	if err != nil || root == "" {
		return commandResult{}, fmt.Errorf("firecracker workspace directory is invalid")
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return commandResult{}, fmt.Errorf("firecracker workspace directory is unavailable")
	}
	digest := sandboxWorktreeDigest(root)
	request := firecrackerSupervisorRequest{
		ProtocolVersion: 1,
		Operation:       "execute",
		LeaseID:         leaseID,
		TaskID:          taskID,
		WorkspaceID:     workspace.ID,
		WorkspacePath:   root,
		WorktreeDigest:  "sha256:" + hex.EncodeToString(digest[:]),
		Command:         command,
		Args:            append([]string(nil), arguments...),
		Input:           input,
		TimeoutMS:       timeout.Milliseconds(),
		Environment:     environment,
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return commandResult{}, fmt.Errorf("encode firecracker supervisor request: %w", err)
	}
	supervisor := workspace.Config.SandboxSupervisorCommand
	// This marker is deliberately non-secret and only distinguishes the
	// supervisor's own process environment from the guest environment. Host
	// credentials are still removed by runLocalWithEnv.
	observed, runErr := runLocalWithEnv(parent, root, timeout, string(encoded)+"\n", map[string]string{"FIRECRACKER_SUPERVISOR_PROTOCOL": "1"}, supervisor[0], supervisor[1:]...)
	if runErr != nil {
		return commandResult{}, fmt.Errorf("firecracker supervisor failed: %w", runErr)
	}
	if observed.ExitCode != 0 {
		return commandResult{}, fmt.Errorf("firecracker supervisor exited with code %d: %s", observed.ExitCode, observed.Output)
	}
	var response firecrackerSupervisorResponse
	decoder := json.NewDecoder(strings.NewReader(observed.Output))
	if err := decoder.Decode(&response); err != nil {
		return commandResult{}, fmt.Errorf("firecracker supervisor returned invalid JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return commandResult{}, fmt.Errorf("firecracker supervisor returned more than one JSON response")
	}
	if response.ProtocolVersion != 1 || response.Operation != request.Operation || response.LeaseID != leaseID || response.TaskID != taskID || response.WorkspaceID != workspace.ID || response.WorktreeDigest != request.WorktreeDigest {
		return commandResult{}, fmt.Errorf("firecracker supervisor response is not bound to this task/workspace")
	}
	if !response.Lifecycle.Created || !response.Lifecycle.WorktreeBound || !response.Lifecycle.GuestCommandExecuted || !response.Lifecycle.Destroyed || !response.Lifecycle.AttestationPersisted {
		return commandResult{}, fmt.Errorf("firecracker supervisor did not prove task lifecycle, worktree binding, teardown, and durable attestation")
	}
	attestationJSON, _ := json.Marshal(response.Attestation)
	attestation := sandboxAttestation(string(attestationJSON))
	if attestation == nil {
		return commandResult{}, fmt.Errorf("firecracker supervisor did not provide verified guest attestation")
	}
	if response.ExitCode < 0 || response.ExitCode > 255 {
		return commandResult{}, fmt.Errorf("firecracker supervisor returned an invalid exit code")
	}
	output := strings.TrimSpace(response.Stdout + response.Stderr)
	if response.Error != "" {
		output = strings.TrimSpace(output + "\n" + response.Error)
	}
	if len(output) > maxCommandOutput {
		output = output[:maxCommandOutput]
	}
	if response.OK && response.ExitCode != 0 {
		return commandResult{}, fmt.Errorf("firecracker supervisor reported success with a non-zero exit code")
	}
	if !response.OK && response.ExitCode == 0 {
		response.ExitCode = 1
	}
	lifecycle := map[string]any{
		"created":                response.Lifecycle.Created,
		"worktree_bound":         response.Lifecycle.WorktreeBound,
		"guest_command_executed": response.Lifecycle.GuestCommandExecuted,
		"destroyed":              response.Lifecycle.Destroyed,
		"attestation_persisted":  response.Lifecycle.AttestationPersisted,
	}
	return commandResult{ExitCode: response.ExitCode, Output: output, SandboxAttestation: attestation, SandboxLifecycle: lifecycle}, nil
}

func runDockerSandboxCommand(parent context.Context, workspace Workspace, directory string, timeout time.Duration, input string, environment map[string]string, command string, arguments ...string) (commandResult, error) {
	if strings.TrimSpace(workspace.Config.SandboxImage) == "" {
		return commandResult{}, fmt.Errorf("docker sandbox is configured without an image")
	}
	root, err := filepath.Abs(directory)
	if err != nil || root == "" {
		return commandResult{}, fmt.Errorf("sandbox workspace directory is invalid")
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return commandResult{}, fmt.Errorf("sandbox workspace directory is unavailable")
	}
	rootLink, err := os.Lstat(root)
	if err != nil || rootLink.Mode()&os.ModeSymlink != 0 {
		return commandResult{}, fmt.Errorf("sandbox workspace directory must not be a symlink")
	}
	containerName := "itbem-agent-sandbox-" + uuid.Must(uuid.NewV4()).String()
	args := dockerSandboxArguments(workspace, root, containerName, environment, command, arguments...)
	result, runErr := runLocalWithEnv(parent, root, timeout, input, environment, "docker", args...)
	if runErr != nil {
		// --rm handles normal completion. On timeout the Docker CLI can be
		// killed before it receives the container exit event, so explicitly fence
		// the named container to avoid a leaked process tree or writable mount.
		_, _ = runLocal(context.Background(), root, 20*time.Second, "", "docker", "rm", "-f", containerName)
		return commandResult{}, fmt.Errorf("sandbox command failed: %w", runErr)
	}
	return result, nil
}

func dockerSandboxArguments(workspace Workspace, directory, containerName string, environment map[string]string, command string, arguments ...string) []string {
	network := strings.ToLower(strings.TrimSpace(workspace.Config.SandboxNetwork))
	if network == "" {
		network = "none"
	}
	cpus := strings.TrimSpace(workspace.Config.SandboxCPUs)
	if cpus == "" {
		cpus = "2"
	}
	memory := strings.TrimSpace(workspace.Config.SandboxMemory)
	if memory == "" {
		memory = "2g"
	}
	pids := workspace.Config.SandboxPIDsLimit
	if pids == 0 {
		pids = 256
	}
	result := []string{
		"run", "--rm", "--init", "--name", containerName,
		"--cap-drop=ALL", "--security-opt=no-new-privileges",
		// Repository-owned commands never need to run as root. A numeric UID is
		// portable across the pinned images and avoids relying on an image's
		// passwd database. The mounted worktree is the only writable project
		// surface and the temporary caches are explicitly redirected below.
		"--user", "65532:65532",
		"--read-only", "--network", network,
		"--cpus", cpus, "--memory", memory, "--pids-limit", fmt.Sprint(pids),
		"--ulimit", "nofile=1024:1024",
		"--tmpfs", "/tmp:rw,noexec,nosuid,size=512m",
		"--tmpfs", "/root:rw,nosuid,size=512m",
		"--tmpfs", "/home:rw,noexec,nosuid,size=256m",
		"--volume", filepath.Clean(directory) + ":/workspace:rw",
		"--workdir", "/workspace",
		"--env", "HOME=/tmp",
		"--env", "GOCACHE=/tmp/go-cache",
		"--env", "GOMODCACHE=/tmp/go-mod-cache",
	}
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	// Only explicit, short-lived overrides cross into the container. The host
	// worker environment is never copied wholesale into a repository command.
	for _, key := range keys {
		if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "=\x00\r\n") {
			continue
		}
		result = append(result, "--env", key+"="+environment[key])
	}
	// The image is the Docker option boundary; command arguments follow it. A
	// required sandbox always carries an operator-pinned digest, so a mutable
	// registry tag cannot silently change the execution environment.
	image := workspace.Config.SandboxImage
	if digest := strings.TrimSpace(workspace.Config.SandboxImageDigest); digest != "" && !strings.Contains(image, "@") {
		image += "@" + digest
	}
	result = append(result, image, command)
	return append(result, arguments...)
}
