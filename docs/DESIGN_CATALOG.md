# Published design catalog

`SeedBaseData` runs `SeedDesignCatalog` after schema migration on every backend start. Publication is
transactional and idempotent: the three product-owned templates use stable UUIDs, while entries with
other IDs are left untouched.

| Identifier | Category | Palette | Access |
| --- | --- | --- | --- |
| `editorial-romance` | romantic | Editorial Romance | included |
| `contemporary-night` | modern | Noche Contemporánea | included |
| `warm-celebration` | celebration | Celebración Cálida | included |

All starter templates are included until a real entitlement service exists. `is_premium` must not be
used as decorative marketing metadata: marking a seed premium without enforcing or explaining the
entitlement would produce a misleading catalog.

## Usability contract

1. `GET /api/catalogs/design-workspace` returns active templates with their complete palette.
2. The dashboard can select a template and persist `design_template_id` on `EventConfig`.
3. `PageSpecService` resolves the selected template into `meta.theme`, with explicit palette/font
   overrides taking precedence.
4. The public renderer maps theme tokens to effective CSS variables. Starter identifiers also have
   system-font fallbacks, so they remain renderable without an uploaded font resource.
5. A missing `preview_url` is supported: the dashboard generates a representative preview from the
   actual palette. An uploaded preview remains an optional enhancement, not a publication blocker.

## Verification

The regular unit tests validate stable definitions and PageSpec/theme contracts. The opt-in Postgres
test runs publication twice inside a rollback-only transaction and verifies uniqueness plus custom
entry preservation:

```powershell
$env:DESIGN_CATALOG_TEST_DSN='host=127.0.0.1 user=postgres password=postgres dbname=events_db port=15432 sslmode=disable TimeZone=UTC'
go test ./seeds -run TestSeedDesignCatalogPostgresIsIdempotentAndPreservesCustomEntries -count=1
```
