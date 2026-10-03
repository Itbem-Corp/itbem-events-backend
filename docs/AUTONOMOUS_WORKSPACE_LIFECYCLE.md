# Autonomous project workspace lifecycle

The target is one authorized project policy followed by unattended execution,
not one manually provisioned checkout per task. Persistent role workers provide
execution slots; task workspaces are separate resources. Multiple tasks and
repositories must never share a writable task tree.

## Implemented foundation

- Systemd provisions missing checkouts from the existing trusted registry before
  doctor runs. Existing local changes and task branches are preserved.
- Delivery prepares selected registered repositories before inference, using the
  dedicated contents-read Source App and frozen full commit SHAs.
- Preparation can create missing managed bases without requiring unrelated,
  unselected checkouts to exist.
- Kernel locks serialize clone/fetch/base-switch/worktree creation per checkout.
  The lock is released on process exit. Independent task worktrees can execute
  concurrently; a multi-repository task uses its task ID in each repository.
- Task input cannot choose filesystem paths, credential sources or executable
  commands. Existing approved-plan repository-impact and validation checks remain.

This does not yet replace the local registry with a fleet project catalog, add
autoscaling, impose disk quotas, or garbage-collect completed task evidence.

## Next implementation contracts

1. **Project catalog and reconciliation.** The control plane supplies the
   enrolled machine with the projects/repositories its role is authorized to
   serve. Bind catalog entries to organization, project, repository, profile
   revision and policy epoch. Derive local paths under the private lane root;
   tasks cannot supply paths or extend repository access. Reconcile additions
   and removals without restart; removal drains current owners and prevents new
   claims. Record explicit readiness and unavailable dependencies per project.
2. **Reusable execution profiles.** Approve Go/Node/toolchain profiles once,
   including pinned sandbox image, resource limits, allowed validation commands,
   dependency preparation and Source/publication App boundaries. A new project
   within that policy can be provisioned automatically. Stack discovery proposes
   a profile; it never turns repository scripts into trusted host commands.
3. **Admission and lifecycle.** Persist leases per task/run/attempt and reserve
   CPU, RAM, disk and concurrency before inference. Allocate one checkout per
   repository at its frozen SHA, resume the same owned attempt after redelivery,
   and fence expired owners. Excess work remains queued. Release idle resources
   automatically; retain publication/QA evidence until receipt and retention
   requirements are satisfied. Recovery must not delete another attempt's files.
4. **Multi-repository scheduling.** Validate all repository authority and
   checkpoints before allocation, reserve the complete resource set in stable
   order, then execute the approved dependency graph. Preserve partial results
   and per-repository PR/commit/review/QA receipts. Do not report project completion
   until all required repositories pass. A failure in one repository must not
   republish successful repositories on retry.
5. **Fleet capacity.** Reuse current role workers up to host resource limits.
   Scale execution processes only through an approved supervisor. Report queue
   pressure, capacity, provisioning latency, lease recovery, cost and cleanup.
   Do not promise unlimited simultaneous tasks on one host.

## Acceptance evidence required

An enrolled project with no local checkout becomes runnable without shell work.
Two projects and several tasks run concurrently without source-tree contamination.
A multi-repository task resumes after one repository fails without duplicating
publication. Killing a worker releases preparation locks and preserves task
evidence. Full capacity queues new work without paid inference; completed work
returns capacity. Removed repository authorization blocks subsequent allocation.
Run the single/multi-repository qualification within the already authorized
US$2 project budget after checking current consumption in the control plane.
