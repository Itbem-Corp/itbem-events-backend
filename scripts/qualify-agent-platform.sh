#!/bin/sh
set -eu

# Deterministic, network-free qualification of the multi-agent control and
# execution planes. Live GitHub/staging/production evidence remains a separate
# operator-owned gate; this script never merges or deploys.

run() {
  printf '\n==> %s\n' "$1"
  shift
  "$@"
}

run "generic onboarding, monorepo discovery and prompt-injection boundary" \
  go test ./internal/projectvault -run 'Test(BuildCreatesDeterministicEvidenceBasedProposal|BuildEnvironmentTemplatesAreNameOnlyEvidence|BuildProposesCommandsPerMonorepoModule|BuildTreatsRepositoryTextAsData|ApplyCapabilityProbesRequiresExactSHAAndSealedSandboxEvidence|ReconcilePreservesChangedRemovedAndUnchangedVaultHistory|ReconcileRejectsCrossRepositoryOrMutableHistory)$' -count=1

run "single-repository worktree and exact reviewed diff" \
  go test ./internal/automationagent -run 'TestRunImplementationUsesIsolatedWorktree$' -count=1

run "heterogeneous discovery and coordinated multi-repository worktrees" \
  go test ./internal/automationagent -run 'Test(DescribeWorkspaceBuildsAnEvidenceBasedArchitectureMap|RunImplementationCreatesIndependentWorktreesForEveryChangedRepository|TopologicalRepositoryOrderRunsDependenciesBeforeConsumers)$' -count=1

run "configured non-main default branch" \
  go test ./internal/automationagent -run 'TestSyncManagedWorkspaceSupportsNonMainBranchAndRejectsDirtyCheckout$' -count=1

run "review-only and production release policies" \
  go test ./internal/deliverypolicy -run 'Test(ReviewOnlyRequiresAnExplicitEmptyTestPolicyAndNeverGrantsMerge|ReleasePolicyRequiresWorkflowEnvironmentReferencesHealthAndRecovery)$' -count=1

run "single/multi-repository deterministic Gatekeeper" \
  go test ./internal/releasegate -run 'Test(EvaluateAllowsCompleteSingleRepositoryMerge|EvaluateAllowsResolvedReviewOnlyPolicyWithoutInventedTests|EvaluateRequiresCurrentHumanApprovalForRelease|EvaluateSupportsAnExactMultiRepositoryMatrix)$' -count=1

run "authoritative QA, security, dependency, environment and recovery evidence" \
  go test ./internal/releasegatecontrol -run 'Test(ResolveStoredEvidenceReplacesCandidateClaimsForMultiRepoMatrix|EnvironmentMatrixEvidenceRequiresCurrentExactPolicy|CompositeRecoveryClassificationUsesMostConstrainedRepositoryPolicy)$' -count=1

run "safe restart/redelivery and durable queue leases" \
  go test ./internal/automationagent -run 'Test(WorkerRecoveryReusesOriginalInferenceRunWithoutCreatingNewCostIdentity|WorkerReusesPersistedResultInsteadOfReexecutingProvider|LongRunningQueueMessageRenewsItsVisibilityLease|ProcessQueueMessageRetainsRetryableWork)$' -count=1

run "Linux role isolation and non-consuming doctor" \
  go test ./cmd/itbem-ai-agent -run 'Test(SystemdUnitFailsClosedAndRunsUnprivileged|SystemdDoctorIsReadOnlyAndCannotConsumeQueueWork|SystemdRoleFilesBindExactLaneAndSeparateReleaseSecrets|SystemdInstallerStagesButNeverActivatesServices|DoctorReleaseReadinessRequiresGitHubAppConfiguration)$' -count=1

run "complete backend regression suite" go test ./... -count=1
run "static analysis" go vet ./...
run "security configuration" go run ./cmd/security-preflight

printf '\nLocal platform qualification passed. Live gates are still required; see docs/agent-platform/QUALIFICATION.md.\n'
