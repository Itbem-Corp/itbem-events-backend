# Faster CI and database recovery

Ordinary production deployment does not create, wait for, or delete manual RDS
snapshots. AWS manages automated backups and point-in-time recovery independently
of delivery. Before candidate startup, a read-only check requires the exact DB to
be available, automated retention of at least seven days (including pending
changes), and a latest restorable point at most 30 minutes
behind. Missing permissions or coverage blocks promotion; the workflow never
changes database settings to make this check pass.

## Rollout prerequisite

Apply the updated `infra/github-oidc-backend-role.yml` stack **before merging the
workflow change**. The production OIDC role needs `rds:DescribeDBInstances` on the
configured database. Its former snapshot create/delete/tag permissions are removed.
Check `BackupRetentionPeriod`, `LatestRestorableTime`
and `PendingModifiedValues` with an authorized operator. Live RDS verification confirmed seven-day retention and a recent restorable point
after the operator added the local read permission. `DescribeDBInstances` does not
return the earliest restorable time; inspect that separately in Automated backups.
If automated backups are disabled, enabling them is a separate database operation
that may interrupt service and must be scheduled accordingly.

For destructive migrations or explicit maintenance, an operator can take a manual
snapshot separately, check it is available, and record its identifier in the
maintenance plan. Deploys must use backward-compatible expand/contract migrations;
rolling the container back does not undo database writes. PITR restores to another
DB instance and requires a separately reviewed cutover. Keep a restore drill and
recovery runbook. Existing manual snapshots are left intact for operator review.

AWS reference: https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/USER_WorkingWithAutomatedBackups.html

## CI changes and measurement

- Agent build runs on PRs and main pushes; feature-branch pushes no longer duplicate
  the same PR build. Manual builds remain available.
- Lint restores Go module/build caches instead of starting cold.
- PostgreSQL automation tests share one Go invocation, as do delivery tests. Every
  previous test remains selected; signed workspace catalog registration is added.
  Race detection, vulnerability checks, transport qualification, security scans,
  immutable image verification, health checks and rollback remain enabled.
- Production deployment concurrency still queues; it never cancels a promotion.

Baseline run `37177999885`: validation 2m41s, image build 1m36s. It failed before
snapshot completion, so that run cannot establish normal snapshot wait duration.
The new pipeline removes that wait and quota dependency. Measure CI after merge;
no measured percentage speedup is claimed. Container build cache and splitting
independent test suites into jobs can be evaluated next using those measurements.
