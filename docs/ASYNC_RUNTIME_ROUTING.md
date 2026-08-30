# Runtime routing and execution ownership

ITBEM and EventiApp use several runtimes because their operational boundaries
are different. A queue message is never allowed to choose its own runtime: the
API resolves a typed event through `internal/runtimeroute` and the outbox
persists that route before any delivery attempt.

## Ownership matrix

| Runtime | Owns | Does not own | Examples |
| --- | --- | --- | --- |
| Go API | Synchronous request handling, authorization, tenancy, canonical state, gates and durable task records | Long-running or CPU-heavy work | Delivery gates, issuing a reviewed agent task, event mutations |
| Go local AI agent | A bounded, task-scoped delivery run close to its registered worktrees and browser harness | Domain authorization, autonomous releases, arbitrary shell execution | Context assembly, plan/implementation proposals, Stagehand QA, reviewed branch publication |
| Rust workers | High-throughput, durable, idempotent background business/data jobs with retry, lease and queue heartbeat semantics | Media transforms and delivery agent decisions | Analytics and performance rollups, notification rendering |
| Media Lambdas | Isolated, bursty, bounded media transforms triggered by S3/SQS | General business jobs, Git operations and long workflows | Image and video derivatives |

The runtime choice is based on execution shape, not on language preference:

- Use the API when the caller needs an immediate authorized decision.
- Use the Go agent only for an approved, audited delivery task that needs a
  local checkout, a controlled browser, or a model-provider call.
- Use Rust when a job is durable, repeatable, queue-driven and benefits from a
  continuously running worker with explicit leases and throughput controls.
- Use a Lambda for isolated, event-driven work with a short bounded lifetime,
  such as a media derivative.

## Existing admission routes

| Event type | Runtime | Tenant boundary |
| --- | --- | --- |
| `analytics.rollup` | Rust worker | EventiApp workload lane |
| `notification.slack` | Rust worker | EventiApp notification lane |
| `media.process` | Media Lambda | EventiApp media lane |
| `automation.ai.local.process` | Go local AI agent | ITBEM only |

`automation.ai.local.process` is deliberately ITBEM-only. EventiApp event,
invitation, RSVP and Studio data cannot be routed to the delivery agent merely
because both products share infrastructure.

## Adding a new asynchronous capability

1. Define a versioned event type and bounded payload contract.
2. Select the execution owner using the matrix above; do not reuse an existing
   queue just because it is available.
3. Add the route to `internal/runtimeroute` with its runtime, queue namespace
   and tenant restriction. Route validation fails closed.
4. Make the producer write an outbox event in the same transaction as the
   state change. The consumer must be idempotent and own retry/DLQ behavior.
5. Add runtime-specific limits, observability, a failure runbook and tests for
   route ownership plus tenant isolation.
6. For a delivery task, define the human gate, evidence artifact and cost
   ledger behavior before enabling the runtime.

## Delivery QA exception, by design

Stagehand executes inside the Go delivery agent rather than the API, Rust
worker or a Lambda. It needs the task's approved browser plan, local project
configuration and task-scoped evidence path. The agent may only run it after
the preview is available; it records the tool call separately in the immutable
ledger and sends screenshots/report artifacts to private storage. Passing QA
never opens a release gate automatically.
