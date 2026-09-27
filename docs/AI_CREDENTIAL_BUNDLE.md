# AI provider credential bundle

Production stores provider API keys in one AWS Secrets Manager `SecretString`
per environment. The production identifier is supplied through
`AI_PROVIDER_CREDENTIALS_SECRET_ID`, for example
`eventiapp/prod/ai-provider-credentials`. The backend uses the workload IAM
role and the AWS-managed `aws/secretsmanager` key; no customer-managed KMS key
is required for this phase.

Local development is deliberately a separate boundary. Set `ENV=local` and
`AI_PROVIDER_CREDENTIALS_LOCAL_FILE=.local/ai-provider-credentials/credentials.json`.
That ignored file has the same JSON shape but holds only a disposable test key.
Local startup rejects `AI_PROVIDER_CREDENTIALS_SECRET_ID` before creating an
AWS Secrets Manager client; the local worker still calls
`ITBEM_AI_GATEWAY_URL` and never receives the key. Never put a production key
in that file. Conversely, a deployed environment rejects the local-file
setting and uses only Secrets Manager.

Create the initial secret before enabling the gateway. New bundles use schema
version 2 and keep project credentials in separate UUID-keyed sections:

```json
{"schema_version":2,"credentials":{},"projects":{}}
```

Project keys are then added through the project-scoped Settings endpoint. A
project owner, delivery manager, explicit `ai:credentials:manage` member, or
platform administrator may replace them; ordinary project members can read
only the `stored` / `not_configured` status. The endpoint response never
returns the key:

```text
PUT /api/automation/ai/projects/{project_id}/providers/{provider}/credential
{"api_key":"..."}
GET /api/automation/ai/projects/{project_id}/providers/{provider}/credential
DELETE /api/automation/ai/projects/{project_id}/providers/{provider}/credential
```

The bundle parser rejects duplicate JSON object member names (including
casing variants) at every depth.
This avoids `encoding/json`'s normal last-value-wins behavior making the
effective project/provider scope ambiguous. Replacing an inference key retains
that provider's separate optional usage-export credential; deleting a
project's final provider credential removes its now-empty project section.

The API checks project membership/role on the server. Project inference scope is
derived from the persisted task → work item → project relationship; a worker
cannot select a different project by adding an ID to its request. Project
execution never falls back to a key in the legacy top-level `credentials`
section. That section remains only for platform-scoped work and legacy
administrative settings. Schema version 1 is read for compatibility; the first
write upgrades the bundle while preserving existing top-level entries. Existing
global keys must be explicitly added to each project that should use them.

The API returns only credential status. Audit records are body-free; clients,
workers, task inputs, SQS messages, execution receipts and logs must never
contain this JSON or an API key.

Account balance and subscription-quota views are project-scoped too:

```text
GET  /api/automation/ai/projects/{project_id}/provider-usage
POST /api/automation/ai/projects/{project_id}/provider-usage/refresh
```

Reading requires project access; refresh requires project-management permission
and calls supported vendor read endpoints with only that project's own
inference credential. GET returns the latest sanitized capture or an empty
`accounts` array when none exists. Each refresh appends a normalized snapshot;
provider responses, keys, and raw error messages are never stored. DeepSeek
balances and MiniMax subscription windows retain vendor-reported units and
values; the dashboard must not infer missing usage or reset times. OpenRouter
credit queries remain disabled until a distinct management credential is
implemented, rather than reusing its inference key.

The only internal caller allowed to resolve the bundle is the cloud inference
gateway (`POST /api/internal/automation/inference`). A local worker supplies
the callback identity plus its current task/run lease, provider/model and
messages. The gateway verifies that the task is currently running and its run
lease and operation match the durable task record before reading the selected
key and invoking the provider. The worker receives only the normalized model
completion.

Each attempt recipe is also protected by a server-only HMAC key named
`AUTOMATION_ATTEMPT_POLICY_SIGNING_KEY`. It is independent from the callback
secret and provider bundle: a local agent cannot forge a different model route
after database tampering. The key is a normal backend environment secret (not
another AWS Secrets Manager object or customer-managed KMS key), is never
passed to the worker launcher, and adds no per-secret cloud service charge.
The local control-plane launcher creates and reuses a random key under the
ignored `.local` directory when one is not explicitly supplied. For production
rotation, deploy a new active key and place the old value in
`AUTOMATION_ATTEMPT_POLICY_SIGNING_KEY_PREVIOUS` until no running attempt uses
it; then remove the previous key. The identifier is derived from the key and
only that non-secret identifier plus an HMAC signature is stored with a recipe.

`ITBEM_AI_GATEWAY_URL` opts a local worker into this mode, normally
`https://api.eventiapp.com.mx/api/internal/automation/inference`. In that
mode, do not configure `MINIMAX_API_KEY`, `OPENAI_API_KEY`,
`DEEPSEEK_API_KEY`, `OPENROUTER_API_KEY`, or `ANTHROPIC_API_KEY` on the
worker. The current direct-provider mode remains only for an
empty/local/development/test `ENV`. Any deployed environment fails closed
unless `ITBEM_AI_GATEWAY_URL` is set, so production workers cannot fall back
to a locally stored provider key.

The gateway currently understands `minimax`, `deepseek`, `openrouter`,
`openai`, `anthropic`, and `opencode-go`; DeepSeek and OpenRouter use their
OpenAI-compatible Chat Completions adapters. Storing a key only makes a
provider eligible. Before it can be called, the deployment must select the
exact provider/model in the automation policy. On the first inference call,
the gateway atomically snapshots that action's ordered routes, policy revision,
and route hash onto the durable automation task. Every later call in that task
uses the same snapshot, even if Settings change while it is running. The worker
cannot select or switch the effective provider/model through its request. The
project credential scope is derived from the task's persisted Delivery
work-item relationship; a task without a project uses only platform-scoped
credentials, and a project task never falls back to a platform key. The ledger uses a server-owned
price catalog, never a price supplied by the browser or worker. Built-in rates
cover the curated MiniMax, DeepSeek and current OpenAI models; a model not
covered there (especially a dynamic OpenRouter model) needs a matching
`AUTOMATION_PRICING_JSON` entry before an enforced budget can admit it. This
keeps a new key from silently enabling spend.

The cloud provider transport rejects every HTTP redirect. It clones any
injected HTTP client rather than changing shared client state, and it never
forwards a provider credential to a redirect target—even one on a provider
subdomain. A redirect is a failed provider attempt and must be reconciled or
retried only under the normal task policy.

## OpenCode Go

Use the provider key `opencode-go` in the credential bundle and select it with
`ITBEM_AI_PROVIDER=opencode-go` plus `OPENCODE_GO_MODEL=<model-id>`. The
gateway, not the local worker, sends the key to OpenCode Go. The models route
is available to a primary platform administrator at:

```text
GET /api/automation/ai/providers/opencode-go/models
```

It reads OpenCode Go's live `/zen/go/v1/models` catalogue and returns a safe
projection. `api_family` identifies whether the backend will use Chat
Completions, Responses, or Messages. `supported:false` means OpenCode
announced a model family whose wire contract has not been added yet; policies
cannot select it, rather than risking a billable call against the wrong API.

Start with one explicitly selected model, rather than rotating the whole
catalogue. `glm-5.3-flash` is the default because it uses the bounded Chat
Completions adapter. Other supported current families include `kimi-*`,
`deepseek-*`, `mimo-*`, `minimax-*`, `qwen*`, `gpt-*`, `grok-*`, and `muse-*`.
The live route is authoritative for availability; a configured policy stays
subject to the provider's current limits.

OpenCode Go's five-hour allowance is a value budget, not a fixed request
counter: it is 20% of each model's monthly allowance. The exact number of
requests changes with model and tokens. The OpenCode Console is the authority
for remaining Go allowance. Its usage export and member-budget APIs require a
separate Console service-account key, so the normal inference key is never
used to guess a remaining quota.

OpenCode Go is billed as a subscription allowance rather than a universal
USD-per-token API rate. Its executions retain provider usage but are marked
unpriced unless the organization deliberately supplies an internal allocation
rate. If a hard project budget must constrain OpenCode Go, set a conservative
allocation entry in `AUTOMATION_PRICING_JSON` for the selected
`opencode-go:<model-id>` and set
`AUTOMATION_BUDGET_PROVIDER=opencode-go` /
`AUTOMATION_BUDGET_MODEL=<model-id>`. Do not use a wildcard price for the full
OpenCode catalogue: model allowances differ materially. When the selected
model changes, update the allocation entry first; otherwise the control plane
fails closed before queuing a budgeted task.

Each automation action can select one primary route plus up to two ordered
fallbacks. Every route can use a different provider and model, but references
the corresponding existing credential rather than carrying a key. The gateway
advances only after an explicit provider 408, 429, or 5xx response; it does
not fail over after an ambiguous network failure, invalid credential, policy
rejection, or malformed completion. Cost admission holds the upper bound of
every selected route, so fallbacks cannot evade a project budget. Configure a
concrete price entry for each route before enabling it.

The bundle is one trust boundary: someone with `GetSecretValue` receives the
entire JSON, not one provider or project property. Do not grant it to a
browser, dashboard user, local worker, queue consumer, or generic support role.
The project sections are logical application scopes, not field-level AWS IAM
policies; authorization is enforced by the backend before it resolves a
project key.

The resolver caches the parsed bundle in the backend for five minutes and
invalidates its cache after an update. A bundle read racing with an in-process
rotation is retried rather than allowed to repopulate the cache with its older
snapshot. Replacing a key creates a new Secrets Manager version for the whole
JSON. All Settings writes take the same
PostgreSQL transaction advisory lock before reading and replacing the bundle,
so concurrent API replicas cannot overwrite another project's update. Direct
out-of-band writes to Secrets Manager must still be serialized with the API.

## Token price catalog and cache accounting

The dashboard model picker returns four visible rates per million tokens when
the provider publishes them: ordinary input, output, cache read and cache
write. OpenRouter supplies those four values in its live model catalogue;
MiniMax, DeepSeek and supported OpenAI models use a curated official rate
catalogue. A live provider catalogue is informational only: it is never used
as a mutable invoice source during execution.

For each completed request the ledger derives cost from provider-reported
usage, not from the request estimate:

```text
ordinary_input = input_tokens - cache_read_tokens - cache_write_tokens
cost = ordinary_input × input_rate
     + output_tokens × output_rate
     + cache_read_tokens × cache_read_rate
     + cache_write_tokens × cache_write_rate
```

Every component is rounded and stored in micro-USD, together with an immutable
rate snapshot. Cache reads and writes are input dimensions, so they are never
added a second time to `total_tokens`. Admission holds use the most expensive
input mode plus maximum completion tokens, and therefore remain conservative
even before the provider reports the actual cache split.

`AUTOMATION_PRICING_JSON` overrides or extends the built-in catalog per exact
`provider:model` key (a provider wildcard is allowed only when every selected
model genuinely has the same rate):

```json
{
  "version": "2026-09-23-review",
  "basis": "official_api_price",
  "models": {
    "openrouter:vendor/model": {
      "input_microusd_per_million": 250000,
      "output_microusd_per_million": 1500000,
      "cached_microusd_per_million": 25000,
      "cache_write_microusd_per_million": 312500
    }
  }
}
```

DeepSeek has peak/off-peak rates. The built-in entry intentionally uses the
published peak rate for safe reservations; use an explicit reviewed catalog if
your finance policy needs a different allocation basis for off-peak execution.

## Production parity

The deployed backend obtains provider keys from the environment-scoped Secrets
Manager bundle selected by `AI_PROVIDER_CREDENTIALS_SECRET_ID`. Its model lists
are fetched live by the gateway exactly as in local mode; no provider key or
model list is bundled into the dashboard. The deployment workflow passes the
following protected-environment settings through its reviewed env-file renderer:

- GitHub Secret: `AI_PROVIDER_CREDENTIALS_SECRET_ID`
- GitHub Secret: `AUTOMATION_ATTEMPT_POLICY_SIGNING_KEY` (required for the
  automation queue); optionally configure
  `AUTOMATION_ATTEMPT_POLICY_SIGNING_KEY_PREVIOUS` during rotation.
- GitHub Variables: `AUTOMATION_PRICING_JSON`,
  `AUTOMATION_BUDGET_PROVIDER`, and `AUTOMATION_BUDGET_MODEL`

Use the same one-line catalog JSON locally and in the protected production
variable. Keep a price entry exact to each chosen `provider:model`; dynamic
OpenRouter models and subscription-backed OpenCode Go models must not inherit a
wildcard rate unless that allocation is intentionally identical for every model.

## Model capabilities and reasoning effort

The model selector receives a credential-free capability projection from the
provider catalog. `reasoning_efforts` is an allow-list for the exact selected
provider/model; an empty list means the dashboard must not offer a reasoning
control and the gateway will not send one.

- DeepSeek V4 Flash and V4 Pro: `low`, `high`, `max`.
- OpenRouter: only models whose live `supported_parameters` advertises
  `reasoning` or `reasoning_effort` expose its documented values (`none`,
  `minimal`, `low`, `medium`, `high`, `xhigh`, `max`).
- MiniMax, Anthropic, OpenAI direct, and OpenCode Go: no generic effort is
  exposed until the specific adapter can prove an equivalent per-model request
  contract. Provider-managed thinking and token-budget controls are not
  interchangeable with an effort enum.

Saving a route re-fetches the current provider catalog and rejects a removed,
incompatible model or an effort outside that model's advertised allow-list.
This prevents stale browser state from persisting an unsupported configuration.

The backend also refreshes each authenticated provider catalogue in the
background at startup and every `AUTOMATION_PROVIDER_CATALOG_SYNC_HOURS`.
The default is 168 hours (weekly). Each differing, credential-free projection
is stored as an audit snapshot, so model removals, capability changes and price
changes can be reviewed without retaining a provider response or API key. A
failed refresh is best-effort: it logs the provider name only and preserves the
last known snapshot.

Each synchronization also makes one credential-free request to the public
Models.dev directory—the directory used by OpenCode for model metadata. It
enriches matching live IDs with descriptions, model family, release/update and
knowledge dates, modalities, context/output limits, tool/structured-output/
temperature flags, open-weight status, reasoning controls, and published base
and long-context token-price tiers. Provider `/models` remains authoritative
for whether the authenticated account can actually use a model. If a provider
has no configured credential, the public cards are still shown but marked
non-selectable; saving a route still requires a live account check.

## Audited model catalogue

`GET /api/automation/ai/catalog` is Root-1-only and returns the latest safe
snapshot for every connector plus any saved route that now needs review. It is
read-only: opening Settings never sends provider keys to the browser or calls a
provider API. The per-model card includes:

- provider/model description, gateway API family and compatibility status;
- input/output modalities, context/output limits and provider-published
  parameters/capability tags (text, vision, tools, agent, structured output,
  reasoning when declared);
- USD per million input, output, cache-read and cache-write tokens, together
  with any published long-context tiers and provenance (`provider_api`,
  `official_catalog`, `models_dev_catalog`, peak official rate, or subscription
  quota);
- family/publisher identity, dates, open-weight status, attachment/tool/
  structured-output/temperature capabilities, and visual provider/model-family
  marks where a licensed local mark is available.

`Compatible` means the current gateway owns a request/response adapter for the
model's API family. It does **not** turn every provider capability into an
automation feature: for example a provider may publish tool calling while the
current worker is intentionally text-chat-only. Unknown prices and capabilities
remain visibly unquoted rather than being inferred.
