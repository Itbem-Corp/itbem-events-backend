# GitHub OIDC bootstrap and rollback

EventiApp deployments use short-lived GitHub OIDC sessions. No repository or
environment may contain `AWS_ACCESS_KEY_ID` or `AWS_SECRET_ACCESS_KEY`.

The trust boundary is split in two:

1. `infra/github-oidc-provider.yml` creates the account-wide GitHub token
   provider once.
2. Each repository creates an environment-specific GitHub deploy role. Media
   and worker deploy roles may update only their named CloudFormation stacks
   and may pass only a separate CloudFormation execution role. GitHub cannot
   directly administer the runtime IAM roles.

All commands in this document are bootstrap operations. Run them from an
administrator/SSO session, never from one of the deploy roles.

## Live bootstrap status (2026-07-13)

- `eventiapp-github-oidc-provider` is `CREATE_COMPLETE` in `us-east-2` and
  exposes the account-wide `token.actions.githubusercontent.com` provider with
  the single audience `sts.amazonaws.com`.
- `eventiapp-github-backend-production-role` is `CREATE_COMPLETE`. Its trust is
  exactly `repo:Itbem-Corp/itbem-events-backend:environment:production`, its
  session duration is capped at one hour, and its resource parameters are the
  production `eventiapp-private-database` RDS instance and `i-07a11e90284b8d69f` EC2
  instance with OS user `ec2-user`.
- The Media `staging` and `prod` role stacks are also `CREATE_COMPLETE`, with
  exact environment trusts, isolated artifact buckets, and environment-bound
  runtime boundaries. The production runtime boundary has been adopted through
  a boundary-only change set and verified; staging adoption remains blocked by
  legacy cross-environment stack parameters and its consumers remain disabled.
- Both backend observability role stacks are `CREATE_COMPLETE`; each GitHub
  trust is scoped to its exact environment and can pass only its matching
  CloudFormation role. The observability resources themselves have not been
  created because the monitored inbox is not yet confirmed.
- The TLS-monitor role is `CREATE_COMPLETE`, trusts only the backend `main`
  branch, and can read/publish only the fixed production public-health topic.
- Repository variables, GitHub Environment protection, alarm/topic stacks, and
  Worker roles are still pending. Do not remove the administrator bootstrap
  path or declare OIDC migration complete until a protected workflow has
  assumed each role successfully.

## Required GitHub protection

Create `production` in `Itbem-Corp/itbem-events-backend`, `staging` and `prod`
in `Itbem-Corp/itbem-media-processor`, and the same two environments in the
eventual worker repository. For every environment:

- allow deployments only from the protected `main` branch;
- require a reviewer for `production` and `prod`;
- prevent administrators from bypassing production protection where the
  organization policy permits it;
- store ARNs, resource IDs, bucket names, regions, and host names as GitHub
  Environment **variables**, not secrets;
- keep only application credentials and the pinned SSH host key in GitHub
  Environment **secrets**.

An environment changes the GitHub OIDC `sub` claim to
`repo:<owner>/<repo>:environment:<environment>`. AWS cannot also test the Git
branch in that subject, so the GitHub deployment-branch rule and the workflow's
explicit `refs/heads/main` gate are both required.

## 1. Create the shared provider once

First verify that another stack or platform team does not already own the
provider:

```bash
aws iam list-open-id-connect-providers
```

If `token.actions.githubusercontent.com` is absent, preview and deploy:

```bash
aws cloudformation deploy \
  --stack-name eventiapp-github-oidc-provider \
  --template-file infra/github-oidc-provider.yml \
  --region us-east-2 \
  --no-execute-changeset

# Inspect the generated change set, then rerun without --no-execute-changeset.
aws cloudformation deploy \
  --stack-name eventiapp-github-oidc-provider \
  --template-file infra/github-oidc-provider.yml \
  --region us-east-2
```

Read the provider ARN for the repository-role stacks:

```bash
PROVIDER_ARN="$(aws cloudformation describe-stacks \
  --stack-name eventiapp-github-oidc-provider \
  --region us-east-2 \
  --query "Stacks[0].Outputs[?OutputKey=='GitHubOidcProviderArn'].OutputValue" \
  --output text)"
test -n "$PROVIDER_ARN"
```

Do not create a second provider with the same URL. If one already exists and is
managed elsewhere, pass its ARN to the role stacks and leave ownership there.

## 2. Create the backend production role

The role can mutate only the selected RDS snapshot namespace and the 60-second
EC2 Instance Connect key for the selected instance/OS user. It also has
read-only access to the fixed production observability stack, alert topic, and
alarm prefix so deployment can fail closed when alert delivery is not ready.

```bash
aws cloudformation deploy \
  --stack-name eventiapp-github-backend-production-role \
  --template-file infra/github-oidc-backend-role.yml \
  --region us-east-2 \
  --capabilities CAPABILITY_NAMED_IAM \
  --no-execute-changeset \
  --parameter-overrides \
    GitHubOidcProviderArn="$PROVIDER_ARN" \
    BackendDbInstanceId="<rds-instance-id>" \
    BackendEc2InstanceId="<i-instance-id>" \

# Inspect the change set, then repeat without --no-execute-changeset.
```

Set these non-secret variables on the `production` GitHub Environment:

| Variable | Source |
|---|---|
| `AWS_DEPLOY_ROLE_ARN` | stack output `BackendDeployRoleArn` |
| `BACKEND_DB_INSTANCE_ID` | exact value passed to the role stack |
| `EC2_INSTANCE_ID` | exact value passed to the role stack |
| `EC2_USER` | exact value passed to the role stack |
| `EC2_HOST` | DNS name or IP whose pinned host key is in `EC2_HOST_KEY` |
| `BACKEND_PORT` | production container/host port (for example `8080`) |
| `BACKEND_CANDIDATE_PORT` | optional; defaults to `18080` |

The workflow deliberately fails before requesting an AWS token if the role ARN
or target identifiers are absent or malformed.

## 3. Create backend observability roles

Observability uses different GitHub and CloudFormation execution roles so the
backend application deploy role never gains Route53, SNS, CloudWatch, IAM
`PassRole`, or CloudFormation permissions.

```bash
for ENVIRONMENT in staging prod; do
  aws cloudformation deploy \
    --stack-name "eventiapp-github-backend-obs-${ENVIRONMENT}-role" \
    --template-file infra/github-oidc-observability-role.yml \
    --region us-east-2 \
    --capabilities CAPABILITY_NAMED_IAM \
    --no-execute-changeset \
    --parameter-overrides \
      GitHubOidcProviderArn="$PROVIDER_ARN" \
      DeploymentEnvironment="$ENVIRONMENT"
  # Inspect the change set, then repeat without --no-execute-changeset.
done
```

On the matching `staging` and `prod` GitHub Environments, configure
`AWS_OBSERVABILITY_DEPLOY_ROLE_ARN` from `GitHubDeployRoleArn` and
`AWS_OBSERVABILITY_CLOUDFORMATION_ROLE_ARN` from
`CloudFormationExecutionRoleArn`. Keep `BACKEND_ALERT_EMAIL` as an Environment
secret and confirm the resulting SNS subscription before treating the alarm
route as operational.

For an existing observability stack, the workflow verifies that the configured
email is already confirmed on both the us-east-2 host topic and the us-east-1
public-health topic before either stack is updated. Only CloudFormation's exact
`ValidationError ... does not exist` response permits first-time bootstrap;
access denial, throttling, network failure, a different recipient, or
`PendingConfirmation` aborts without mutation. Rotate a monitored inbox through
a separately reviewed route cutover, not by replacing `BACKEND_ALERT_EMAIL` in
place.

## 4. Create the isolated daily TLS-monitor role

Route53 HTTPS health checks do not replace certificate-chain and hostname
validation. The scheduled `tls-certificate-monitor.yml` workflow performs an
external TLS handshake against `api.eventiapp.com.mx`, alerts at 30 days, and
publishes the JSON result when validation fails or the certificate enters the
renewal window.

Bootstrap its separate, read-and-publish-only role from an administrator/SSO
session after the production public-health topic exists:

```bash
aws cloudformation deploy \
  --stack-name eventiapp-github-backend-tls-monitor-role \
  --template-file infra/github-oidc-tls-monitor-role.yml \
  --region us-east-2 \
  --capabilities CAPABILITY_NAMED_IAM \
  --no-execute-changeset \
  --parameter-overrides GitHubOidcProviderArn="$PROVIDER_ARN"

# Inspect the change set, then repeat without --no-execute-changeset.
```

Configure repository variable `AWS_TLS_MONITOR_ROLE_ARN` from
`TLSMonitorRoleArn`, repository variable `TLS_MONITOR_TOPIC_ARN` from
`TLSAlertTopicArn`, and repository secret `TLS_MONITOR_ALERT_EMAIL` with the
already-confirmed production public-health recipient. This job deliberately
does not use a GitHub Environment: its OIDC trust is exactly
`repo:Itbem-Corp/itbem-events-backend:ref:refs/heads/main`. The role has no
CloudFormation, Route53, EC2, RDS, IAM, or deployment permissions; it can only
read the subscription and publish to the one production public-health topic.

## Rollback and emergency revoke

- CloudFormation automatically rolls back a failed role-stack update. For a
  bad successful update, redeploy the previous committed template and
  parameters through a reviewed change set.
- To stop new sessions immediately, disable the GitHub environment and update
  the affected role trust policy to an impossible subject (or delete that
  repository-role stack) using the administrator session. Removing a GitHub
  variable alone is not an IAM revocation.
- Existing OIDC sessions last at most one hour. Inspect CloudTrail for
  `AssumeRoleWithWebIdentity` and the deployment actions during an incident.
- Never delete `eventiapp-github-oidc-provider` while any repository role still
  trusts it. Remove repository-role stacks first, then the provider stack.
- Media SAM artifact buckets use `DeletionPolicy: Retain`; a deliberate final
  teardown requires a separate reviewed empty-and-delete operation.
