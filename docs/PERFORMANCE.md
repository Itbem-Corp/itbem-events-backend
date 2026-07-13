# Performance Guide

> For code quality standards, see `docs/CODING_STANDARDS.md`.
> This file focuses on runtime: DB queries, caching, and code efficiency.

## Database Query Optimization

### N+1 Problem

```go
// BAD: N+1 queries
events, _ := GetAllEvents()
for _, e := range events {
    client, _ := GetClient(e.ClientID)  // N additional queries!
}

// GOOD: Eager loading
db.Preload("Client").Preload("Guests").Find(&events)
```

### Missing Indexes

```go
// Add index tag to model
ClientID uuid.UUID `gorm:"type:uuid;not null;index" json:"client_id"`

// Or manual migration
db.Exec("CREATE INDEX idx_events_client_id ON events(client_id)")
```

### Public moments wall index

The public wall filters by event, approval, processing state, and soft-delete
state, then orders manual positions before the chronological fallback. A plain
`event_id` index still has to visit every moment for the event and sort every
visible row. `configuration.EnsurePerformanceIndexes` installs the versioned
partial index `idx_moments_public_wall_v1` with the complete filter and sort
shape:

```sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_moments_public_wall_v1
ON moments (
    event_id,
    (CASE WHEN "order" > 0 THEN 0 ELSE 1 END) ASC,
    "order" ASC,
    created_at DESC,
    id DESC
)
WHERE deleted_at IS NULL
  AND is_approved = true
  AND processing_status IN ('', 'done');
```

Reproducible local benchmark (PostgreSQL 16.12, deterministic isolated
database, 600,000 moments across 37 events, 7,778 visible rows in the measured
event, 7 warm-cache samples after one discarded warm-up, parallel workers
disabled). The guarded fixture and all four before/after plans live in
`docs/benchmarks/public_moments_wall.sql`; it refuses to run unless the scratch
database name starts with `eventi_wall_bench_`:

| Query | `event_id` index median | Partial wall index median | Change |
|---|---:|---:|---:|
| Exact visible count | 21.757 ms | 0.768 ms | -96.5% |
| First page, 21 rows | 24.815 ms | 0.116 ms | -99.5% |
| Cursor after row 100, 101 rows | 24.085 ms | 0.567 ms | -97.6% |
| Offset 5,000, 21 rows | 36.572 ms | 24.195 ms | -33.8% |

The baseline count/page plans used a bitmap heap scan over 16,231 shared
buffers and the page added a top-N sort. With the partial index, the count used
an index-only scan over 77 buffers (`Heap Fetches: 0` after `VACUUM`) and the
first/cursor pages used ordered index scans without a sort. The synthetic index
contained 283,680 entries, occupied 21 MB, and built concurrently in about
400 ms on the local machine.

The repository intentionally binds only request-scoped values (`event_id`,
cursor fields, and limits). Approval and ready-state values are compile-time
SQL literals. This matters with pgx/GORM prepared statements: PostgreSQL cannot
prove that a generic plan with bound `is_approved` and processing statuses
implies a partial-index predicate. In the same benchmark, the exact prepared
count with literal invariants switched naturally to a generic plan after five
custom plans and still used the 77-buffer index-only scan. With
`plan_cache_mode = force_generic_plan`, both count and page still used
`idx_moments_public_wall_v1` (77 and 25 buffers respectively). The former
all-bound predicate fell back to the plain `event_id` bitmap scan under that
forced generic plan.

Cursor continuation uses the same complete sort tuple and compares the final
UUID directly (`id < ?::uuid`) so the indexed UUID key remains usable. A
non-empty cursor must include its `order` boundary; legacy cursors without that
field are rejected with `Invalid cursor`, requiring the client to restart from
the first page rather than silently skipping or duplicating manually ordered
moments.

This benchmark proves the access-path improvement, not production cardinality:
validate the deployed plan with `EXPLAIN (ANALYZE, BUFFERS)` and real event
sizes. The exact count still scales with visible entries, index-only behavior
depends on vacuum visibility, and deep offset pagination still walks the
offset; new clients should continue to prefer the cursor endpoint. The partial
index adds write amplification only while a row is public-wall-visible and
uses additional disk proportional to visible rows. A single synthetic bulk
insert check (200,000 rows) took 1,601 ms with the baseline indexes and 1,895 ms
with the wall index (+18.4%); this is a write-amplification signal, not an OLTP
latency forecast, and should be monitored with production ingest rates.

The migration does not rely on an AutoMigrate index tag. It reserves one SQL
connection, takes a stable session advisory lock so replicas cannot race,
builds with `CONCURRENTLY` outside a transaction, uses a 5-second lock timeout
and 25-minute statement timeout, repairs an interrupted invalid index on the
next attempt, and verifies `indisvalid`, `indisready`, the resolved `moments`
table, all ordered keys, and the partial predicate before reporting success.
A valid homonymous index with the wrong definition is dropped concurrently and
rebuilt. Cleanup unlocks before resetting session timeouts; if any cleanup step
fails, the physical session is discarded instead of returning possibly leaked
advisory locks or timeout settings to the request pool. Critical table/column
migration remains synchronous, but this
performance-only migration runs in a contained background goroutine: lock,
timeout, driver, and recovered panic failures are logged with the explicit
`API remains available` message and retried at the next process start. This
keeps readiness independent of a non-critical index while preserving an
observable retry path. If the definition changes, add a new versioned index
name instead of silently reusing `_v1`. To roll back, deploy code without the
migration first, then run
`DROP INDEX CONCURRENTLY IF EXISTS idx_moments_public_wall_v1`.

### Filter at DB Level

```go
// BAD: Load all, filter in memory
events, _ := GetAllEvents()
filtered := filterByClient(events, clientID)

// GOOD: Filter in query
db.Where("client_id = ?", clientID).Find(&events)
```

### Query Checklist

- Use `Preload()` for all relationships
- Index foreign keys and frequently filtered columns
- Use `Select()` to load only needed columns
- Use `Limit()` + `Offset()` for pagination — never load all rows without limit
- Filter with `Where()`, not in memory

---

## Cache Optimization

### Current Pattern (Invalidate All)
```go
// After any mutation — simple but low hit rate (~40%)
s.cache.Invalidate("events", "all")
```

### Better: Granular Keys
```go
// Invalidate only affected entries
s.cache.Invalidate("events", id.String())
s.cache.Invalidate("events", fmt.Sprintf("client_%s", clientID))
```

### Cache-Aside Pattern
```go
func GetEvent(id uuid.UUID) (*models.Event, error) {
    cacheKey := fmt.Sprintf("events:%s", id)
    if cached, err := redisrepository.Get(cacheKey); err == nil {
        return cached.(*models.Event), nil
    }
    event, err := repository.GetEventByID(id)
    if err != nil {
        return nil, err
    }
    redisrepository.Set(cacheKey, event, 5*time.Minute)
    return event, nil
}
```

### Concurrent Miss Coalescing

`services/cacheutil.GetOrLoadJSON` coalesces simultaneous misses for the
same cache instance, key, and result type. The flight leader double-checks
Redis, runs the loader once, and writes once; waiters receive private
JSON-decoded copies so mutable slices, maps, and pointers are not shared.
Loader errors are returned to every waiter but are never cached, so the next
request can retry normally.

### Cache Performance Targets

| Strategy | Hit Rate | Latency |
|----------|----------|---------|
| Invalidate all | 30–50% | 20–100ms |
| Granular keys | 70–90% | 5–20ms |
| Cache-aside + warming | 80–95% | 2–10ms |

---

## Code-Level Optimization

### Preallocate Slices
```go
// BAD
var results []models.Event
for _, item := range items { results = append(results, ...) }

// GOOD
results := make([]models.Event, 0, len(items))
for _, item := range items { results = append(results, ...) }
```

### Pass Pointers for Large Structs
```go
func ProcessEvent(event *models.Event) error { ... }  // not models.Event
```

### Context Timeouts for External Calls
```go
ctx, cancel := context.WithTimeout(c.Request().Context(), 5*time.Second)
defer cancel()
data, err := callExternalAPI(ctx)
```

### Bounded Parallel Reads

Independent reads on a hot request path may overlap when their fan-out is
small and fixed. `PageSpecService` loads the resource, public-attendee, and
public-moment content versions concurrently (at most three database reads),
then joins them before calculating `contentVersion`. Keep conditional reads
conditional, preserve best-effort behavior, and add a deterministic test that
proves the loaders actually overlap; never create one goroutine per row.

Public moment-wall pagination follows the same rule. Both offset and cursor
paths need an exact visible-item count plus one bounded page of rows. Those two
queries use identical visibility filters but are otherwise independent, so
`momentrepository.loadPublicWallPage` runs them concurrently with a fixed
fan-out of two. The database critical path changes from `count + page` to
approximately `max(count, page)` while query count and API response semantics
remain unchanged. Both queries share a cancellation context: either failure
cancels its sibling, and both workers are joined before the repository returns
or re-panics, so failed requests cannot leave orphaned database work. The
configured pool allows up to 50 open connections; do not replace this bounded
pair with per-moment goroutines.

Protected event detail has another bounded pair before authorization.
`authz.RequireEventAccess` needs both the synchronized dashboard identity and
the event row before it can evaluate client membership, but neither initial
read depends on the other. It therefore loads them concurrently and joins
before authorization. The common non-root path changes from
`identity sync + event lookup + membership check` to approximately
`max(identity sync, event lookup) + membership check`; query count, root
behavior, error priority, and the response contract stay unchanged. Missing
or malformed identity context is still rejected before starting either read.

### Production SQL Logging

GORM uses `info` query logging during local development, but defaults to
`warn` whenever `ENV` is set. This removes per-query formatting and output
from the production hot path while retaining slow-query and database-error
diagnostics. Set `DB_LOG_LEVEL` to `silent`, `error`, `warn`, or `info` for an
explicit override.

### Goroutine Worker Pool
```go
const maxWorkers = 10
sem := make(chan struct{}, maxWorkers)
for _, item := range items {
    sem <- struct{}{}
    go func(item Item) {
        defer func() { <-sem }()
        processItem(item)
    }(item)
}
for i := 0; i < cap(sem); i++ { sem <- struct{}{} }
```

---

## Performance Targets

| Endpoint Type | Target | Max |
|--------------|--------|-----|
| Cached GET | < 10ms | 50ms |
| Simple DB GET | < 50ms | 100ms |
| POST/PUT/DELETE | < 100ms | 200ms |
| Complex query | < 200ms | 500ms |

| DB Query | Target | Action if Exceeded |
|----------|--------|-------------------|
| PK lookup | < 5ms | — |
| FK lookup (indexed) | < 10ms | Add index |
| Full table scan | Avoid | Add where clause |

---

## Monitoring

```bash
# Redis: watch commands in real time
redis-cli MONITOR

# PostgreSQL: analyze query plan
EXPLAIN ANALYZE SELECT * FROM events WHERE client_id = 'some-id';
```

### GORM Query Logging (dev only)
```go
// configuration/gorm.go
db, _ := gorm.Open(postgres.Open(dsn), &gorm.Config{
    Logger: logger.Default.LogMode(logger.Info),
})
```

### Slow Request Detection
```go
func TimingMiddleware() echo.MiddlewareFunc {
    return func(next echo.HandlerFunc) echo.HandlerFunc {
        return func(c echo.Context) error {
            start := time.Now()
            err := next(c)
            if d := time.Since(start); d > 100*time.Millisecond {
                log.Printf("SLOW: %s %s — %v", c.Request().Method, c.Path(), d)
            }
            return err
        }
    }
}
```
