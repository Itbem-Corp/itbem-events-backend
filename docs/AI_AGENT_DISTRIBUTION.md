# Go AI agent distribution

The supported distributable worker is the Go command at
`cmd/itbem-ai-agent`, backed by `internal/automationagent`. The
`Build AI Agent` GitHub Actions workflow tests those packages and builds
Windows amd64 and Linux amd64 binaries. It does not build the archived Python
agent, publish a GitHub Release, or change a running worker.

## Download and verify on Windows

The workflow runs on a manual dispatch or a push that changes the worker,
one of its Go dependencies, this workflow, or this document. Artifacts follow
the repository's Actions access controls and expire after seven days. Do not
upload or include secrets in an artifact.

1. Select a successful `Build AI Agent` run for the trusted branch/commit you
   intend to use. The run page shows the source commit and its two artifacts.
2. Download `itbem-ai-agent-windows-amd64` from that run. With GitHub CLI, use:

   ```powershell
   gh run download <RUN_ID> --repo <OWNER/REPOSITORY> --name itbem-ai-agent-windows-amd64 --dir .\itbem-ai-agent-windows-amd64
   ```

   Replace the angle-bracket placeholders with the numeric run ID and the
   repository's actual `OWNER/REPOSITORY` before running the command.

3. Compare the downloaded executable with the checksum included in the same
   artifact:

   ```powershell
   $artifactDir = '.\itbem-ai-agent-windows-amd64'
   $expected = ((Get-Content "$artifactDir\SHA256SUMS.txt" -Raw).Trim() -split '\s+')[0].ToLowerInvariant()
   $actual = (Get-FileHash "$artifactDir\itbem-ai-agent.exe" -Algorithm SHA256).Hash.ToLowerInvariant()
   if ($actual -ne $expected) { throw 'AI agent checksum mismatch' }
   ```

   Keep the run ID and source commit with your deployment record. The checksum
   detects transfer or storage corruption; because it is delivered beside the
   executable, it does not independently authenticate the publisher.

For Linux amd64, download `itbem-ai-agent-linux-amd64` and verify with
`sha256sum --check SHA256SUMS.txt` from the extracted artifact directory.

## Configure and run

The binary does not read `.env.ai.local` or contain configuration. Configure
the worker through the deployment's approved environment mechanism. For the
recommended `gateway` transport, configure `ITBEM_API_BASE_URL`,
`ITBEM_AI_GATEWAY_URL`, `ITBEM_AI_GATEWAY_TOKEN`, the exact
`ITBEM_AI_ROLE`/`ITBEM_AI_QUEUE_LANE`, `ITBEM_AI_INPUT_BUCKET`, and
`ITBEM_AI_OUTPUT_BUCKET`. The gateway token is scoped to that lane; do not put
the callback root secret, provider keys, or AWS credentials on the worker.

Gateway workers no longer need a manually copied `ITBEM_AGENT_INSTANCE_ID`.
The first `--ensure-registered` prestart creates the machine's protected local
identity, proves key possession to the backend with the lane token, and saves
the server-issued ID in `ITBEM_AI_STATE_DIR`. Preserve that private state
directory across restarts. The systemd unit sets it to its lane-specific
`StateDirectory`; direct installations can set it explicitly. The separate
legacy AWS-direct transport still requires its approved least-privilege AWS
identity, queue URL, region, and primary-root-enrolled instance ID.

Use the environment reference in `docs/ENVIRONMENT.md` and the local setup
notes in `internal/automationagent/README.md` for the surrounding control-plane,
queue, bucket, gateway, and registration requirements. Do not put provider API
keys in the worker environment: the worker calls the configured inference
gateway, which owns provider credentials.

After downloading and verifying in PowerShell, set the lane-bound gateway
configuration through the operator-approved mechanism, then run:

```powershell
& .\itbem-ai-agent-windows-amd64\itbem-ai-agent.exe --ensure-registered
& .\itbem-ai-agent-windows-amd64\itbem-ai-agent.exe -doctor
```

Review the readiness result locally. When the registered instance and runtime
are ready, start the continuous worker with:

```powershell
& .\itbem-ai-agent-windows-amd64\itbem-ai-agent.exe
```

The Linux executable is `itbem-ai-agent`; make it executable if needed, run
`./itbem-ai-agent --ensure-registered`, then `./itbem-ai-agent -doctor` before
`./itbem-ai-agent`. Enrollment is limited to a valid lane token and active
profile; revoked machines require explicit primary-root re-enrollment. The
worker processes queue work and can make billable provider calls through the
gateway, so start it only after its non-mutating doctor and auth probes pass.

## Security and limits

For a narrowly approved retry or isolated evaluation, set `ITBEM_AI_TASK_IDS`
to a comma-separated list of up to 60 unique canonical task UUIDs in the
operator-managed service environment. The worker defers every other queue
message before claiming it or accessing private input/provider state. This
selection only narrows processing; normal role, server claim, lease, signed
policy, quotas and budgets still apply. Omit it for ordinary continuous work.
Use a temporary service override and remove that override after the scoped
work ends. Deferring messages does not cancel them or acknowledge them.

- The workflow grants only `contents: read`; checkout does not persist its
  token. It has no provider, cloud, signing, or publication credentials and
  does not print runtime configuration.
- Artifacts are temporary Actions artifacts, not releases or a managed update
  channel. Downloads follow repository visibility/access controls and expire
  after seven days. Rebuild from a trusted source commit when an artifact expires.
- The workflow includes SHA-256 files, but artifacts are **not signed** and no
  build provenance attestation is generated. Do not treat a matching adjacent
  checksum as proof of publisher identity. Verify the workflow run, source
  commit, repository access controls, and branch review before using a binary.
- There is no installer, auto-update behavior, rollback policy, code signing,
  or compatibility handshake in this distribution path. Operators must
  coordinate binary upgrades with the backend/runtime contract and manage
  installation, configuration, and rollback themselves.
