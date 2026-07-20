# Product boundaries

The API is one deployable Go service with product-owned definitions. It shares
connections, deployment, PostgreSQL and Valkey, but never treats a tenant name
as cosmetic presentation.

## Structure

```text
internal/products/
  core/          Product code and definition types
  eventiapp/     Event operations definition
  itbem/         Platform-control definition
  cafettonhouse/ Explicit-membership definition
  registry.go    Only resolver consumed by core services
```

Controllers, authorization and services must resolve a product through this
registry. Do not add `tenant == "..."` branches outside a product definition.

## Cache contract

Valkey is shared for efficiency, with explicit namespaces:

| Data class | Key shape | Scope |
| --- | --- | --- |
| Shared catalog | existing `catalog:*` keys | Global, non-personal data only |
| Product/user | `v1:tenant:<product>:user:<id>:<resource>` | Product + Cognito identity |
| Product/org | `v1:tenant:<product>:org:<id>:<resource>` | Product + organization |
| Event public wall | event UUID key | EventiApp only; never contains dashboard session data |

All personalized cache keys must use `services/cacheutil.TenantKey`. Mutations
must invalidate the same versioned namespace. Use bounded `SCAN` + `UNLINK` for
intentional broad invalidation; do not use `KEYS`, `FLUSHALL`, or an unscoped
pattern in a request path.

## Adding a product

1. Add a definition under `internal/products/<product>/` and register it.
2. Define its Cognito audience and API host in infrastructure; configure the
   backend tenant client/host/bucket maps.
3. Seed an `applications` row and explicit organization/member entitlements.
4. Add its media bucket and CORS origin; no bucket is shared with another app.
5. Add product-scoped cache keys for every personalized read model.
6. Add a worker product module and an SNS topic binding when it owns jobs.
7. Add dashboard catalog/manifest, smoke coverage and domain tests.

The API authentication chain is immutable: hostname -> Cognito audience ->
product -> application entitlement -> organization capability -> resource.

## Dashboard request context

Authenticated dashboards send `X-Application-Code`, `X-Workspace-Mode`, and,
for organization workspaces, `X-Organization-ID` plus its short-lived signed
`X-Organization-Context` credential. CORS allows these headers on
the configured dashboard origins. `applicationaccess.Require` compares them
with the tenant and application session resolved from the signed Cognito token:

- an application header cannot contradict the authenticated tenant;
- `platform` mode requires an application that allows platform administration
  and a root user;
- platform mode cannot carry a retained organization;
- a non-root organization request may only select an organization included in
  its application session.

The headers improve scoping and observability but never grant authorization.
Resource-level checks remain authoritative in controllers and services.
