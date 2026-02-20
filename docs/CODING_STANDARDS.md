# Coding Standards - Production-Grade Go

> **CRITICAL**: These standards are MANDATORY for all code. Non-compliant code will not be merged.

## 🎯 Core Principles

1. **Performance First** - Every line counts in production
2. **Cost-Effective** - Optimize for cloud costs (DB queries, memory, CPU)
3. **Readable** - Code is read 10x more than written
4. **Secure** - Security is not optional
5. **Testable** - If you can't test it, don't write it

---

## 🚀 Performance Standards

### 1. Database Queries - CRITICAL

#### ❌ NEVER DO THIS:

```go
// BAD: N+1 query problem - Kills performance!
events, _ := repository.GetAllEvents()  // 1 query
for _, event := range events {
    client, _ := repository.GetClient(event.ClientID)  // N queries!!
    // This can cause 1000+ queries for 1000 events
}
```

#### ✅ ALWAYS DO THIS:

```go
// GOOD: Single query with eager loading
var events []models.Event
db.Preload("Client").              // Load relationships in ONE query
   Preload("Guests").
   Where("active = ?", true).
   Find(&events)
```

**Rule**: Use `Preload()` for ALL relationships. One query is ALWAYS better than N queries.

---

#### ❌ NEVER DO THIS:

```go
// BAD: Loading everything into memory
func GetAllUsers() ([]models.User, error) {
    var users []models.User
    db.Find(&users)  // Could load millions of records!
    return users, nil
}
```

#### ✅ ALWAYS DO THIS:

```go
// GOOD: Pagination with limits
func GetUsers(page, pageSize int) ([]models.User, int64, error) {
    var users []models.User
    var total int64

    // Get total count
    db.Model(&models.User{}).Count(&total)

    // Get page with limit
    offset := (page - 1) * pageSize
    err := db.Limit(pageSize).
        Offset(offset).
        Order("created_at DESC").
        Find(&users).Error

    return users, total, err
}
```

**Rule**: NEVER load all records without pagination. Always use `Limit()` + `Offset()`.

---

#### ❌ NEVER DO THIS:

```go
// BAD: Selecting all columns when you only need a few
var events []models.Event
db.Find(&events)  // Loads all columns, all relationships
```

#### ✅ ALWAYS DO THIS:

```go
// GOOD: Select only needed columns
var events []struct {
    ID   uuid.UUID
    Name string
}
db.Model(&models.Event{}).
   Select("id", "name").
   Find(&events)
```

**Rule**: Use `Select()` to load only needed columns. Save bandwidth and memory.

---

### 2. Memory Management

#### ❌ NEVER DO THIS:

```go
// BAD: Reallocates memory multiple times
var results []models.Event
for _, item := range items {
    results = append(results, process(item))  // Reallocates when capacity exceeded
}
```

#### ✅ ALWAYS DO THIS:

```go
// GOOD: Preallocate with known capacity
results := make([]models.Event, 0, len(items))  // Preallocate
for _, item := range items {
    results = append(results, process(item))  // No reallocation
}
```

**Rule**: Preallocate slices when you know the capacity.

---

#### ❌ NEVER DO THIS:

```go
// BAD: Passing large structs by value (copies entire struct)
func ProcessEvent(event models.Event) error {  // COPIES everything!
    // ...
}
```

#### ✅ ALWAYS DO THIS:

```go
// GOOD: Pass pointers for structs > 64 bytes
func ProcessEvent(event *models.Event) error {  // Passes reference
    // ...
}
```

**Rule**: Pass pointers for structs. Value receivers for primitives/small structs only.

---

### 3. Goroutines & Concurrency

#### ❌ NEVER DO THIS:

```go
// BAD: Goroutine leak - no wait, no cleanup
for _, user := range users {
    go sendEmail(user.Email)  // Spawns goroutines and forgets them
}
// Function exits, goroutines orphaned!
```

#### ✅ ALWAYS DO THIS:

```go
// GOOD: Wait for goroutines, handle errors
var wg sync.WaitGroup
errChan := make(chan error, len(users))

for _, user := range users {
    wg.Add(1)
    go func(u models.User) {
        defer wg.Done()
        if err := sendEmail(u.Email); err != nil {
            errChan <- err
        }
    }(user)  // Pass by value to avoid race conditions
}

wg.Wait()
close(errChan)

// Check for errors
for err := range errChan {
    log.Printf("Error sending email: %v", err)
}
```

**Rule**: ALWAYS wait for goroutines. Use `sync.WaitGroup` or channels.

---

#### ❌ NEVER DO THIS:

```go
// BAD: Unbounded goroutine creation
for i := 0; i < 10000; i++ {
    go processItem(items[i])  // Could spawn 10,000 goroutines!
}
```

#### ✅ ALWAYS DO THIS:

```go
// GOOD: Worker pool pattern with semaphore
const maxWorkers = 10
sem := make(chan struct{}, maxWorkers)

for _, item := range items {
    sem <- struct{}{}  // Acquire
    go func(item Item) {
        defer func() { <-sem }()  // Release
        processItem(item)
    }(item)
}

// Wait for all workers
for i := 0; i < cap(sem); i++ {
    sem <- struct{}{}
}
```

**Rule**: Limit concurrent goroutines. Use worker pools or semaphores.

---

### 4. Cache Usage

#### ❌ NEVER DO THIS:

```go
// BAD: No caching, hits DB every time
func GetEvent(id uuid.UUID) (*models.Event, error) {
    var event models.Event
    db.First(&event, "id = ?", id)  // DB query EVERY time
    return &event, nil
}
```

#### ✅ ALWAYS DO THIS:

```go
// GOOD: Cache-aside pattern
func GetEvent(id uuid.UUID) (*models.Event, error) {
    cacheKey := fmt.Sprintf("events:%s", id)

    // Try cache first
    if cached, err := redisrepository.Get(cacheKey); err == nil {
        return cached.(*models.Event), nil
    }

    // Cache miss - get from DB
    var event models.Event
    if err := db.First(&event, "id = ?", id).Error; err != nil {
        return nil, err
    }

    // Store in cache with TTL
    redisrepository.Set(cacheKey, &event, 5*time.Minute)

    return &event, nil
}
```

**Rule**: Cache frequently accessed data. Use cache-aside pattern.

---

## 💰 Cost-Effective Cloud Standards

### 1. Database Connection Pooling

#### ❌ NEVER DO THIS:

```go
// BAD: Creating new connection every time
func QueryDB() {
    db, _ := gorm.Open(postgres.Open(dsn))  // New connection!
    db.Find(&users)
    db.Close()  // Expensive!
}
```

#### ✅ ALWAYS DO THIS:

```go
// GOOD: Reuse connection pool (already in configuration/gorm.go)
func QueryDB() {
    db := configuration.GetDB()  // Reuse existing pool
    db.Find(&users)
    // No close - pool manages connections
}

// Configure pool in gorm.go:
sqlDB.SetMaxOpenConns(25)        // Max 25 connections
sqlDB.SetMaxIdleConns(10)        // Keep 10 idle
sqlDB.SetConnMaxLifetime(5 * time.Minute)
```

**Rule**: NEVER create new DB connections. Use pool. Configure limits.

**Cost Impact**: New connections cost ~50ms + DB resources. Pool = instant.

---

### 2. HTTP Client Reuse

#### ❌ NEVER DO THIS:

```go
// BAD: Creating new HTTP client every request
func CallAPI() {
    client := &http.Client{}  // New client!
    resp, _ := client.Get(url)
    // ...
}
```

#### ✅ ALWAYS DO THIS:

```go
// GOOD: Reuse HTTP client with connection pooling
var httpClient = &http.Client{
    Timeout: 10 * time.Second,
    Transport: &http.Transport{
        MaxIdleConns:        100,
        MaxIdleConnsPerHost: 10,
        IdleConnTimeout:     90 * time.Second,
    },
}

func CallAPI() {
    resp, _ := httpClient.Get(url)  // Reuses connections
    // ...
}
```

**Rule**: Create HTTP client ONCE, reuse forever. Connection pooling is free performance.

**Cost Impact**: Reused connections = 10x faster + lower CPU usage.

---

### 3. Context Timeouts

#### ❌ NEVER DO THIS:

```go
// BAD: No timeout - can hang forever and waste resources
func ProcessRequest(c echo.Context) error {
    data := callExternalAPI()  // Could hang forever!
    return c.JSON(200, data)
}
```

#### ✅ ALWAYS DO THIS:

```go
// GOOD: Always use context with timeout
func ProcessRequest(c echo.Context) error {
    ctx, cancel := context.WithTimeout(c.Request().Context(), 5*time.Second)
    defer cancel()

    data, err := callExternalAPIWithContext(ctx)
    if err != nil {
        if ctx.Err() == context.DeadlineExceeded {
            return utils.Error(c, http.StatusGatewayTimeout, "Request timeout", "")
        }
        return utils.Error(c, http.StatusInternalServerError, "Error", err.Error())
    }

    return c.JSON(200, data)
}
```

**Rule**: ALWAYS use context with timeout for external calls. Prevent resource leaks.

**Cost Impact**: Hanging requests = wasted CPU/memory/money.

---

### 4. Graceful Shutdown

#### ✅ MUST HAVE:

```go
// In server.go
func main() {
    e := echo.New()

    // Setup routes...

    // Start server in goroutine
    go func() {
        if err := e.Start(":8080"); err != nil && err != http.ErrServerClosed {
            log.Fatal(err)
        }
    }()

    // Wait for interrupt signal
    quit := make(chan os.Signal, 1)
    signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
    <-quit

    // Graceful shutdown with timeout
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    if err := e.Shutdown(ctx); err != nil {
        log.Fatal(err)
    }

    log.Println("Server gracefully stopped")
}
```

**Rule**: ALWAYS implement graceful shutdown. Let in-flight requests complete.

**Cost Impact**: Prevents data corruption, better user experience.

---

## 📖 Readability Standards

### 1. Naming Conventions

#### ❌ BAD:

```go
func gd(i uuid.UUID) (*models.Event, error) {}  // What is 'gd'? What is 'i'?
var e models.Event  // Too short in long scope
var userEmailAddressString string  // Redundant
```

#### ✅ GOOD:

```go
func GetEventByID(id uuid.UUID) (*models.Event, error) {}
var event models.Event  // Clear
var email string  // Type is obvious
```

**Rules**:
- Functions: `VerbNoun` (GetUser, CreateEvent, DeleteGuest)
- Variables: Descriptive but concise (event, not e; user, not u)
- Constants: `ALL_CAPS` or `PascalCase` (MaxRetries, DefaultTimeout)
- Private: `lowerCamelCase` (parseRequest, validateInput)
- Public: `PascalCase` (CreateEvent, GetUserByID)

---

### 2. Function Length

#### ❌ BAD:

```go
// BAD: 200-line function doing everything
func CreateEvent(c echo.Context) error {
    // 200 lines of validation, DB calls, business logic...
    // IMPOSSIBLE to understand or test
}
```

#### ✅ GOOD:

```go
// GOOD: Small, focused functions
func CreateEvent(c echo.Context) error {
    event, err := parseEventRequest(c)
    if err != nil {
        return err
    }

    if err := validateEvent(event); err != nil {
        return err
    }

    if err := eventService.CreateEvent(event); err != nil {
        return err
    }

    return utils.Success(c, http.StatusCreated, "Event created", event)
}

func parseEventRequest(c echo.Context) (*models.Event, error) { /* ... */ }
func validateEvent(event *models.Event) error { /* ... */ }
```

**Rules**:
- Functions: < 50 lines (ideally < 30)
- One function = one responsibility
- Extract helpers for complex logic

---

### 3. Error Handling

#### ❌ NEVER DO THIS:

```go
// BAD: Ignoring errors
user, _ := GetUser(id)  // What if error?!

// BAD: Generic error messages
return errors.New("error")  // USELESS!

// BAD: Panic in library code
if err != nil {
    panic(err)  // DON'T CRASH THE SERVER!
}
```

#### ✅ ALWAYS DO THIS:

```go
// GOOD: Check all errors, provide context
user, err := GetUser(id)
if err != nil {
    return fmt.Errorf("failed to get user %s: %w", id, err)  // Wrap error
}

// GOOD: Custom error types for business logic
type ValidationError struct {
    Field   string
    Message string
}

func (e *ValidationError) Error() string {
    return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// GOOD: Return errors, don't panic
if event.Date.Before(time.Now()) {
    return &ValidationError{
        Field:   "date",
        Message: "event date cannot be in the past",
    }
}
```

**Rules**:
- ALWAYS check errors
- Provide context when wrapping: `fmt.Errorf("context: %w", err)`
- NEVER panic in library code
- Use custom error types for business logic

---

### 4. Comments & Documentation

#### ❌ BAD:

```go
// This function gets the user
func GetUser(id uuid.UUID) (*models.User, error) {}  // Useless comment

// i is the id
var i uuid.UUID  // Restates obvious

func doStuff() {}  // No comment for complex function
```

#### ✅ GOOD:

```go
// GetUser retrieves a user by ID with client relationships preloaded.
// Returns ErrNotFound if user doesn't exist.
func GetUser(id uuid.UUID) (*models.User, error) {}

// No comment needed - name is clear
var userID uuid.UUID

// processEventNotifications sends notifications to all event guests
// and logs any failures for later retry. It uses a worker pool to
// limit concurrent email sends to avoid rate limiting.
func processEventNotifications(eventID uuid.UUID) error {
    // Implementation...
}
```

**Rules**:
- Public functions: ALWAYS document (GoDoc format)
- Complex logic: Explain WHY, not WHAT
- Don't comment obvious code
- Explain business rules and edge cases

---

## 🔒 Security Standards

### 1. Input Validation - CRITICAL

#### ❌ NEVER DO THIS:

```go
// BAD: No validation - SQL injection risk!
func GetUser(c echo.Context) error {
    id := c.Param("id")
    db.Raw("SELECT * FROM users WHERE id = " + id).Scan(&user)  // DANGER!
}

// BAD: Trusting user input
email := c.FormValue("email")
sendEmail(email)  // Could be malicious!
```

#### ✅ ALWAYS DO THIS:

```go
// GOOD: Validate and sanitize ALL input
func GetUser(c echo.Context) error {
    // Validate UUID format
    id, err := uuid.FromString(c.Param("id"))
    if err != nil {
        return utils.Error(c, http.StatusBadRequest, "Invalid ID format", "")
    }

    // Use parameterized queries (GORM does this automatically)
    var user models.User
    if err := db.First(&user, "id = ?", id).Error; err != nil {
        return utils.Error(c, http.StatusNotFound, "User not found", "")
    }

    return utils.Success(c, http.StatusOK, "Success", user)
}

// GOOD: Validate email before using
func SendEmail(c echo.Context) error {
    email := c.FormValue("email")

    // Validate email format
    if !isValidEmail(email) {
        return utils.Error(c, http.StatusBadRequest, "Invalid email", "")
    }

    // Validate domain (optional but recommended)
    if !isAllowedDomain(email) {
        return utils.Error(c, http.StatusBadRequest, "Email domain not allowed", "")
    }

    sendEmail(email)
    return utils.Success(c, http.StatusOK, "Email sent", nil)
}
```

**Rules**:
- ALWAYS validate input types (UUID, email, phone, etc.)
- Use GORM parameterized queries (NEVER string concat)
- Sanitize input before using in external systems
- Validate business rules (max length, allowed values, etc.)

---

### 2. Authentication & Authorization

#### ❌ NEVER DO THIS:

```go
// BAD: No authentication check
func DeleteEvent(c echo.Context) error {
    id := c.Param("id")
    eventService.DeleteEvent(id)  // Anyone can delete!
    return utils.Success(c, http.StatusOK, "Deleted", nil)
}
```

#### ✅ ALWAYS DO THIS:

```go
// GOOD: Always check authentication AND authorization
func DeleteEvent(c echo.Context) error {
    // 1. Get authenticated user (from JWT middleware)
    cognitoSub := c.Get("cognito_sub").(string)
    if cognitoSub == "" {
        return utils.Error(c, http.StatusUnauthorized, "Not authenticated", "")
    }

    // 2. Validate ID
    id, err := uuid.FromString(c.Param("id"))
    if err != nil {
        return utils.Error(c, http.StatusBadRequest, "Invalid ID", "")
    }

    // 3. Get resource
    event, err := eventService.GetEvent(id)
    if err != nil {
        return utils.Error(c, http.StatusNotFound, "Event not found", "")
    }

    // 4. Check authorization - user must own the resource
    user, err := userService.GetByCognitoSub(cognitoSub)
    if err != nil {
        return utils.Error(c, http.StatusUnauthorized, "User not found", "")
    }

    if !userOwnsEvent(user, event) {
        return utils.Error(c, http.StatusForbidden, "Access denied", "")
    }

    // 5. Now safe to delete
    if err := eventService.DeleteEvent(id); err != nil {
        return utils.Error(c, http.StatusInternalServerError, "Delete failed", "")
    }

    return utils.Success(c, http.StatusOK, "Event deleted", nil)
}
```

**Rules**:
- ALWAYS verify authentication (JWT middleware)
- ALWAYS verify authorization (user owns resource)
- Use role-based access control where appropriate
- Never trust client-side authorization

---

### 3. Secrets Management

#### ❌ NEVER DO THIS:

```go
// BAD: Hardcoded secrets
const dbPassword = "mypassword123"  // NEVER!
apiKey := "sk_live_abc123"  // NEVER!

// BAD: Logging secrets
log.Printf("User password: %s", user.Password)  // NEVER LOG SECRETS!
```

#### ✅ ALWAYS DO THIS:

```go
// GOOD: Use environment variables
cfg := configuration.GetConfig()
dbPassword := cfg.DBPassword  // From .env or system env

// GOOD: Never log sensitive data
log.Printf("User authenticated: %s", user.Email)  // OK
// NEVER log: password, tokens, API keys, credit cards

// GOOD: Hash passwords before storing
hashedPassword, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
user.Password = string(hashedPassword)
```

**Rules**:
- NEVER hardcode secrets
- Use environment variables
- NEVER log secrets
- Hash passwords (bcrypt, argon2)
- Use AWS Secrets Manager for production

---

## ✅ Code Review Checklist

Before submitting code, verify:

### Performance
- [ ] No N+1 queries (use Preload)
- [ ] Pagination for list endpoints
- [ ] Indexes on foreign keys
- [ ] Pointers for structs > 64 bytes
- [ ] Preallocated slices
- [ ] Cache frequently accessed data
- [ ] Context timeouts for external calls

### Cost-Effectiveness
- [ ] Reusing DB connection pool
- [ ] Reusing HTTP client
- [ ] Graceful shutdown implemented
- [ ] No goroutine leaks
- [ ] Resource limits configured

### Readability
- [ ] Functions < 50 lines
- [ ] Clear naming (no abbreviations)
- [ ] Public functions documented
- [ ] Complex logic explained
- [ ] Errors have context

### Security
- [ ] All input validated
- [ ] Authentication checked
- [ ] Authorization enforced
- [ ] No SQL injection risk
- [ ] No secrets in code/logs

### Testing
- [ ] Unit tests written
- [ ] Edge cases covered
- [ ] Error paths tested
- [ ] Integration tests for APIs

---

## 🎯 Performance Targets

| Metric | Target | Maximum | Action if Exceeded |
|--------|--------|---------|-------------------|
| API Response (simple GET) | < 50ms | 100ms | Add caching |
| API Response (complex) | < 200ms | 500ms | Optimize query |
| Database Query | < 50ms | 100ms | Add index |
| Memory per Request | < 1MB | 5MB | Review allocations |
| Goroutines | < 100 | 1000 | Use worker pool |

---

## 🚫 Forbidden Patterns

These patterns are **BANNED** in this codebase:

1. ❌ `SELECT *` without Preload (N+1 queries)
2. ❌ Loading all records without pagination
3. ❌ Creating DB connections in functions
4. ❌ Ignoring errors (`_, err := ...` without checking)
5. ❌ `panic()` in library code
6. ❌ Hardcoded secrets
7. ❌ No input validation
8. ❌ String concatenation for SQL
9. ❌ Goroutines without WaitGroup
10. ❌ Functions > 100 lines

**Violation = Code rejected in review**

---

## When to Update This File

- ✅ Found new performance pattern → Add example
- ✅ Discovered security vulnerability → Add to security section
- ✅ Found common mistake → Add to forbidden patterns
- ✅ Measured performance regression → Update targets
