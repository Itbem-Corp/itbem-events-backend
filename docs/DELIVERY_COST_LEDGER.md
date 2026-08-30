# Delivery cost ledger

Every billable inference is immutable execution evidence. Cost is never
inferred from a task description or overwritten by a retry.

## Ledger grain

| Level | Record | Purpose |
| --- | --- | --- |
| Agent call | `automation_executions` | Primary MiniMax/agent request for a Delivery phase. |
| Tool call | `automation_tool_executions` | A separately billable tool call, including Stagehand semantic QA. |
| Step | `step_key` + execution kind/tool | Groups plan, implementation, QA, summary and tool usage. |
| Task | `delivery_work_item_id` | Aggregates every execution attached to one gated work item. |
| Project | Delivery project query | Aggregates task costs without losing the underlying calls. |
| Global | cost endpoint | Cross-project operational allocation view. |

## Required immutable fields

Each execution stores provider, model, completion status, request and response
references, timestamps and the exact usage dimensions reported by the provider:

- input tokens;
- output tokens;
- cached input tokens;
- cache-write tokens;
- reasoning tokens (visible independently, never double-counted);
- total tokens;
- cost components and total cost in micro-USD.

The request/response bodies live as private artifacts. Aggregate endpoints and
standard traces expose only metadata and totals; an authorized reviewer opens
the exact inspector for a specific call. Failed provider calls remain in the
ledger when usage exists, so retries cannot hide cost.

## Stagehand

Stagehand emits a separate call ledger from its report. Browser metrics and
the MiniMax semantic assessment are combined without counting reasoning twice.
The report, request summary and screenshots are private task evidence, while
the task/project/global cost surfaces retain their independent token totals.

## Pricing and review

Pricing is snapshotted per call using the configured provider/model rate. A
monthly project budget is an admission guardrail; it does not rewrite history.
Human gates remain independent of spend: a completed call may propose a plan
or QA report, but only a recorded reviewer decision advances Delivery.
