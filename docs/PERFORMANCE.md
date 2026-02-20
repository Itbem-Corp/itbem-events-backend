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
redisrepository.Invalidate("events", "all")
```

### Better: Granular Keys
```go
// Invalidate only affected entries
redisrepository.Invalidate("events", id.String())
redisrepository.Invalidate("events", fmt.Sprintf("client_%s", clientID))
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
