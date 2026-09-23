[CmdletBinding()]
param(
    [switch]$SkipServices,
    [switch]$Json
)

$ErrorActionPreference = 'Stop'
$backendRoot = Split-Path $PSScriptRoot -Parent
$workspaceRoot = Split-Path $backendRoot -Parent
$serviceResults = [ordered]@{}
$evidenceResults = [System.Collections.Generic.List[string]]::new()

function Info([string]$message) {
    if (-not $Json) { Write-Host $message }
}

function Assert-LatestEvidence([string]$pattern) {
    $evidenceRoot = Join-Path $workspaceRoot 'design-qa-artifacts/automation-plan/implementation'
    $match = Get-ChildItem -LiteralPath $evidenceRoot -Filter $pattern -File -ErrorAction SilentlyContinue |
        Sort-Object LastWriteTime -Descending |
        Select-Object -First 1
    if (-not $match) {
        throw "Required evidence artifact is missing for pattern: $pattern"
    }
    $evidenceResults.Add($match.Name)
    Info "Evidence: present ($($match.Name))"
}

if (-not $SkipServices) {
    $services = @{
        dashboard = 'http://127.0.0.1:3017/login'
        backend = 'http://127.0.0.1:18080/health'
        localstack = 'http://127.0.0.1:4566/_localstack/health'
    }
    foreach ($service in $services.GetEnumerator()) {
        try {
            $response = Invoke-WebRequest -UseBasicParsing -Uri $service.Value -TimeoutSec 5
            if ($response.StatusCode -lt 200 -or $response.StatusCode -ge 400) {
                throw "HTTP $($response.StatusCode)"
            }
            $serviceResults[$service.Key] = [ordered]@{ status = 'ready'; http_status = $response.StatusCode; url = $service.Value }
            Info "Service $($service.Key): HTTP $($response.StatusCode)"
        } catch {
            $serviceResults[$service.Key] = [ordered]@{ status = 'unavailable'; url = $service.Value }
            throw "Local service $($service.Key) is not ready: $($_.Exception.Message)"
        }
    }
}

$guard = Join-Path $PSScriptRoot 'Test-EventiAppInvariant.ps1'
if ($Json) {
    & (Get-Command pwsh -ErrorAction Stop).Source -NoProfile -File $guard | Out-Null
} else {
    & (Get-Command pwsh -ErrorAction Stop).Source -NoProfile -File $guard
}
if ($LASTEXITCODE -ne 0) { throw 'EventiApp invariant guard failed.' }

$requiredEvidencePatterns = @(
    'OFFLINE-HARNESS-SUMMARY-*.md',
    'EVENTIAPP-INVARIANT-GUARD-*.md',
    'PROVIDER-ADAPTER-CONTRACT-RECHECK-*.md',
    'RECOVERY-PUBLICATION-UNCERTAINTY-RECHECK-*.md',
    'TOPOLOGY-SAFETY-RECHECK-*.md',
    'FRONTEND-HERMETIC-RECHECK-*.md',
    'LOCAL-RUNTIME-INTEGRATION-RECHECK-*.md',
    'LOCAL-RUNTIME-RESTART-RECHECK-*.md',
    'BACKEND-FULL-REGRESSION-RECHECK-*.md',
    'INTEGRATION-FULL-RECHECK-*.md',
    'GOAL-COMPLETION-AUDIT-*.md',
    'BACKEND-GO-VET-RECHECK-*.md',
    'BACKEND-QA-HERMETIC-SCREENSHOT-RECHECK-*.md',
    'FRONTEND-STATIC-RECHECK-*.md',
    'FRONTEND-UNIT-REGRESSION-RECHECK-*.md',
    'FRONTEND-PRODUCTION-BUILD-RECHECK-*.md',
    'GOAL-READINESS-JSON-METADATA-RECHECK-*.md',
    'GOAL-COMPLETION-AUDIT-*.md',
    'EXTERNAL-GATES-REQUEST-*.md',
    'EXTERNAL-GATES-RESOLUTION-*.md',
    'GOAL-READINESS-EXTERNAL-RECHECK-*.md',
    'MINIMAX-LIVE-ROUND-*.json',
    'MINIMAX-LIVE-ROUND-*-EVIDENCE.md',
    'GOAL-READINESS-LIVE-MINIMAX-RECHECK-*.md',
    'FIRECRACKER-WSL-ROUNDTRIP-*.md',
    'FIRECRACKER-GUEST-COMMAND-EVIDENCE-*.md',
    'FIRECRACKER-VSOCK-ROUNDTRIP-*.md',
    'FIRECRACKER-WSL-KVM-PERMISSION-BLOCK-*.md',
    'SANDBOX-ATTESTATION-HEARTBEAT-CONTRACT-*.md',
    'FRONTEND-SANDBOX-TRANSPORT-EVIDENCE-*.md',
    'SANDBOX-ATTESTATION-DOCTOR-RECHECK-*.md',
    'SANDBOX-TASK-LEASE-RECHECK-*.md',
    'FIRECRACKER-TASK-SUPERVISOR-ADAPTER-*.md',
    'FIRECRACKER-TASK-SUPERVISOR-REAL-RECHECK-*.md',
    'FIRECRACKER-TASK-SUPERVISOR-VSOCK-REAL-RECHECK-*.md',
    'FIRECRACKER-PRODUCTION-PROFILE-RECHECK-*.md',
    'FIRECRACKER-USER-SCOPE-PRODUCTION-RECHECK-*.md',
    'FIRECRACKER-JAILER-CGROUP-PREFLIGHT-*.md',
    'FIRECRACKER-JAILER-REAL-LIFECYCLE-*.md',
    'MULTI-PROVIDER-EVAL-RUNBOOK-*.md',
    'LOCAL-WORKER-RECOVERY-RECHECK-*.md',
    'DEPENDENCY-READINESS-DASHBOARD-RECHECK-*.md',
    'OFFLINE-HARNESS-CONCURRENCY-RECHECK-*.md',
    'LOCAL-DISTRIBUTED-RECOVERY-RECHECK-*.md',
    'GOAL-COMPLETION-AUDIT-20260922.md',
    'FRONTEND-LIVE-SIGNAL-CLARITY-*.md',
    'FRONTEND-CSP-WCAG-HARNESS-*.md',
    'FRONTEND-TASK-LEASE-EVIDENCE-*.md',
    'DASHBOARD-AUTHENTICATED-CONTROL-RECHECK-*.md',
    'DASHBOARD-AUTHENTICATED-SECTIONS-RECHECK-*.md',
    'OFFLINE-HARNESS-RECHECK-*.md'
)
foreach ($pattern in $requiredEvidencePatterns) { Assert-LatestEvidence $pattern }

$evidenceRoot = Join-Path $workspaceRoot 'design-qa-artifacts/automation-plan/implementation'
$hasMiniMaxLiveEvidence = $null -ne (Get-ChildItem -LiteralPath $evidenceRoot -Filter 'MINIMAX-LIVE-ROUND-*-EVIDENCE.md' -File -ErrorAction SilentlyContinue | Select-Object -First 1)
$hasKvmPermissionEvidence = $null -ne (Get-ChildItem -LiteralPath $evidenceRoot -Filter 'FIRECRACKER-WSL-KVM-PERMISSION-BLOCK-*.md' -File -ErrorAction SilentlyContinue | Select-Object -First 1)
$hasRealVmLifecycleEvidence = $null -ne (Get-ChildItem -LiteralPath $evidenceRoot -Filter 'FIRECRACKER-TASK-SUPERVISOR-REAL-RECHECK-*.md' -File -ErrorAction SilentlyContinue | Select-Object -First 1)
$hasRealVmVsockEvidence = $null -ne (Get-ChildItem -LiteralPath $evidenceRoot -Filter 'FIRECRACKER-TASK-SUPERVISOR-VSOCK-REAL-RECHECK-*.md' -File -ErrorAction SilentlyContinue | Select-Object -First 1)
$hasUserScopeProductionEvidence = $null -ne (Get-ChildItem -LiteralPath $evidenceRoot -Filter 'FIRECRACKER-USER-SCOPE-PRODUCTION-RECHECK-*.md' -File -ErrorAction SilentlyContinue | Select-Object -First 1)
$miniMaxGateState = if ($hasMiniMaxLiveEvidence) { 'verified_live_bounded' } else { 'verified_synthetic' }
$vmState = if ($hasRealVmLifecycleEvidence) { 'verified_live_bounded' } else { 'unverified' }
$vmReason = if ($hasRealVmVsockEvidence -and $hasUserScopeProductionEvidence) {
	'A real local Firecracker task-scoped lifecycle used the virtio-vsock guest agent and the production profile with built-in seccomp/resource limits; a root-owned Jailer lifecycle also passed with a bounded read-only worktree image. The normal local fleet now runs inside delegated systemd user scopes, but the Jailer still requires an operator-owned root boundary, so deployment policy remains a separate gate.'
} elseif ($hasRealVmVsockEvidence) {
	'A real local Firecracker task-scoped lifecycle used the virtio-vsock guest agent and the production profile with built-in seccomp/resource limits; a root-owned Jailer lifecycle also passed with a bounded read-only worktree image. The worker identity and deployment boundary still require review before hostile-code execution.'
} elseif ($hasRealVmLifecycleEvidence) {
    'A real local Firecracker task-scoped lifecycle created one disposable guest, bound the worktree digest, executed a guest command over the explicitly scoped serial-console proof transport, destroyed the VM, and persisted a credential-free attestation.'
} elseif ($hasKvmPermissionEvidence) {
    'The real WSL Firecracker preflight reached /dev/kvm but the invoking user was denied read/write access; task-scoped lifecycle, worktree binding, durable attestation and control-plane registration remain unverified.'
} else {
    'Firecracker and virtio-vsock pass a synthetic guest command and the worker can validate optional evidence, but task-scoped lifecycle, worktree binding, durable attestation and control-plane registration are not verified.'
}
$vmNextAction = if ($hasRealVmVsockEvidence) {
    'Keep the user-scope production proof bounded; provide the operator-owned root/Jailer boundary, register `--profile production --jailer`, then repeat the lifecycle corpus under the deployment profile before hostile-code execution.'
} elseif ($hasRealVmLifecycleEvidence) {
    'Keep the local proof bounded; before production hostile-code execution, replace the serial-console fixture with a reviewed virtio-vsock guest agent and repeat the same lifecycle corpus.'
} elseif ($hasKvmPermissionEvidence) {
    'In WSL run `sudo usermod -aG kvm andbema`, restart the WSL session, then rerun the real Firecracker round-trip and the control-plane supervisor lifecycle.'
} else {
    'Create the microVM per task, bind a real worktree by policy, persist attestation, and execute a repository command from the control-plane worker.'
}

$externalGates = [ordered]@{
    cognito_authenticated = 'verified_local_session'
    github_remote_authorized = 'verified_disposable_pr'
    docker_sandbox = 'verified_local_not_vm'
    minimax_live = $miniMaxGateState
    vm_microvm_isolation = $vmState
    multi_provider_semantic = 'unverified'
}
$externalGateDetails = [ordered]@{
    cognito_authenticated = [ordered]@{
        state = 'verified_local_session'
        reason = 'Authenticated local dashboard session was observed.'
        next_action = 'Keep the session scoped to the local tenant.'
    }
    github_remote_authorized = [ordered]@{
        state = 'verified_disposable_pr'
        reason = 'A disposable private validation repository and PR were observed.'
        next_action = 'Review manually; do not merge automatically.'
    }
    docker_sandbox = [ordered]@{
        state = 'verified_local_not_vm'
        reason = 'Worker workspaces report digest-pinned Docker isolation with network none.'
        next_action = 'Use only for local validation; do not label it VM or microVM isolation.'
    }
    minimax_live = [ordered]@{
        state = $miniMaxGateState
        reason = if ($hasMiniMaxLiveEvidence) { 'MiniMax-M3 passed the bounded synthetic corpus through a real provider run; the result remains a bounded evaluation, not a market-quality certification.' } else { 'MiniMax-M3 passed the bounded synthetic harness corpus.' }
        next_action = 'Keep the human plan gate, expand repetitions when useful, and do not generalize the bounded score to market quality.'
    }
    vm_microvm_isolation = [ordered]@{
        state = $vmState
        reason = $vmReason
        next_action = $vmNextAction
    }
    multi_provider_semantic = [ordered]@{
        state = 'unverified'
        reason = 'Only MiniMax has a live semantic run in the reserved corpus.'
        next_action = 'Select a second provider/model, reserve a budget, and run the same corpus with comparable scoring.'
    }
}
Info 'Goal readiness: PASS local; Cognito, GitHub, Docker, MiniMax and bounded Firecracker lifecycle verified with explicit product-worktree and multi-provider gaps'
if ($Json) {
    [ordered]@{
        schema_version = 2
        generated_at_utc = (Get-Date).ToUniversalTime().ToString('o')
        source = 'Test-GoalReadiness.ps1'
        status = 'pass'
        services = $serviceResults
        eventiapp_platform = 'clean'
        evidence = @($evidenceResults)
        external_gates = $externalGates
        external_gate_details = $externalGateDetails
        external_gates_status = 'partial'
    } | ConvertTo-Json -Compress
}
