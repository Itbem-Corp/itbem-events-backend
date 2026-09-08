#!/bin/sh
set -eu

# Deterministic qualification of the multi-agent control and execution planes.
# A first run may fetch only the repository's pinned Git submodules; subsequent
# checks are network-free after dependencies are present. Live
# GitHub/staging/production evidence remains a separate operator-owned gate;
# this script never merges or deploys.

run() {
  printf '\n==> %s\n' "$1"
  shift
  "$@"
}

# A clean `git worktree` retains only each submodule's gitlink. Initialize the
# exact pinned revisions before the complete regression suite so a local
# qualification exercises the same source graph as CI. `protocol.file` stays
# disabled: a repository cannot turn this bootstrap into a local-file read.
prepare_pinned_submodules() {
  test -f .gitmodules || return 0
  git -c protocol.file.allow=never submodule update --init --recursive
}

run "pinned Git submodule checkout" prepare_pinned_submodules

run "generic onboarding, monorepo discovery and prompt-injection boundary" \
  go test ./internal/projectvault -run 'Test(BuildCreatesDeterministicEvidenceBasedProposal|BuildEnvironmentTemplatesAreNameOnlyEvidence|BuildProposesCommandsPerMonorepoModule|BuildTreatsRepositoryTextAsData|ApplyCapabilityProbesRequiresExactSHAAndSealedSandboxEvidence|ReconcilePreservesChangedRemovedAndUnchangedVaultHistory|ReconcileRejectsCrossRepositoryOrMutableHistory)$' -count=1

run "single-repository worktree and exact reviewed diff" \
  go test ./internal/automationagent -run 'TestRunImplementationUsesIsolatedWorktree$' -count=1

run "heterogeneous discovery and coordinated multi-repository worktrees" \
  go test ./internal/automationagent -run 'Test(DescribeWorkspaceBuildsAnEvidenceBasedArchitectureMap|RunImplementationCreatesIndependentWorktreesForEveryChangedRepository|TopologicalRepositoryOrderRunsDependenciesBeforeConsumers)$' -count=1

run "configured non-main default branch" \
  go test ./internal/automationagent -run 'TestSyncManagedWorkspaceSupportsNonMainBranchAndRejectsDirtyCheckout$' -count=1

run "GitHub source synchronization with a dedicated read-only App" \
  go test ./cmd/itbem-ai-agent ./internal/automationagent -run 'Test(LoadGitHubSourceAppConfigRequiresItsDedicatedNamespace|FetchAuthorizedWorkspaceRemoteRequiresDedicatedSourceApp|GitHubInstallationWorkspaceCommandsDisableCredentialHelpers|RunOnboardingCapabilityProbesUsesExactSHAOperatorCommandsAndCleansUp|GitHubAuthProbeRequiresPublicationOrRegisteredGitHubSourceAndRedactsFailures)$' -count=1

run "review-only and production release policies" \
  go test ./internal/deliverypolicy -run 'Test(ReviewOnlyRequiresAnExplicitEmptyTestPolicyAndNeverGrantsMerge|ReleasePolicyRequiresWorkflowEnvironmentReferencesHealthAndRecovery)$' -count=1

run "single/multi-repository deterministic Gatekeeper" \
  go test ./internal/releasegate -run 'Test(EvaluateAllowsCompleteSingleRepositoryMerge|EvaluateAllowsResolvedReviewOnlyPolicyWithoutInventedTests|EvaluateRequiresCurrentHumanApprovalForRelease|EvaluateSupportsAnExactMultiRepositoryMatrix)$' -count=1

run "authoritative QA, security, dependency, environment and recovery evidence" \
  go test ./internal/releasegatecontrol -run 'Test(ResolveStoredEvidenceReplacesCandidateClaimsForMultiRepoMatrix|EnvironmentMatrixEvidenceRequiresCurrentExactPolicy|CompositeRecoveryClassificationUsesMostConstrainedRepositoryPolicy)$' -count=1

run "safe restart/redelivery and durable queue leases" \
  go test ./internal/automationagent -run 'Test(WorkerRecoveryReusesOriginalInferenceRunWithoutCreatingNewCostIdentity|WorkerReusesPersistedResultInsteadOfReexecutingProvider|LongRunningQueueMessageRenewsItsVisibilityLease|ProcessQueueMessageRetainsRetryableWork)$' -count=1

run "Linux role isolation, non-consuming doctor and outbound gateway preflight" \
  go test ./cmd/itbem-ai-agent ./internal/automationagent ./controllers/automation -run 'Test(SystemdUnitFailsClosedAndRunsUnprivileged|SystemdDoctorIsReadOnlyAndCannotConsumeQueueWork|SystemdRoleFilesBindExactLaneAndSeparatePublicationSecrets|SystemdInstallerStagesButNeverActivatesServices|DoctorPublicationReadinessRequiresRoleSpecificGitHubAppConfiguration|LoadRuntimeConfigSelectsHTTPSGatewayWithoutAWSIdentity|GatewayTokensAreLaneBoundAndDoNotExposeRoot|GatewayLeaseIsConfidentialTamperEvidentAndIdentityBound)$' -count=1

run "exact-SHA idempotent GitHub review relay and independent identity" \
  go test ./internal/automationagent ./controllers/automation -run 'Test(PublishGitHubCodeReviewIsExactSHAAndRetrySafe|PublishGitHubCodeReviewNeverSelfApproves|CodeReviewPublicationForTaskRequiresExactIndependentGitHubEvidence)$' -count=1

run "complete backend regression suite" go test ./... -count=1
run "static analysis" go vet ./...
run "security configuration" go run ./cmd/security-preflight

printf '\nLocal platform qualification passed. Live gates are still required; see docs/agent-platform/QUALIFICATION.md.\n'
