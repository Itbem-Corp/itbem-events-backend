# Common Development Tasks

> Update this file when you discover new patterns or common tasks.
> **For full code templates, see `docs/TEMPLATES.md`.**

## Quick Reference

- [Adding a New Entity (Full CRUD)](#adding-a-new-entity-full-crud)
- [Adding a Protected Route](#adding-a-protected-route)
- [Adding a Public Route](#adding-a-public-route)
- [Working with Cache](#working-with-cache)
- [Adding Environment Variable](#adding-environment-variable)
- [Custom Database Queries](#custom-database-queries)
- [File Upload Handling](#file-upload-handling)
- [Adding Middleware](#adding-middleware)

---

## Adding a New Entity (Full CRUD)

> **Use `docs/TEMPLATES.md` for all code.** This section describes the steps only.

### Step 1: Create the Model
Create `models/<Entity>.go` using the **Model Template** from `docs/TEMPLATES.md`.

**Required**: add `json:"snake_case_name"` tags to every field. Use `json:"-"` for sensitive fields (tokens, internal flags). Use `json:"...,omitempty"` on relation fields.

Then update: **`docs/MODELS.md`**

### Step 2: Register for Migration
Edit `configuration/gorm.go` — add `&models.Entity{}` to `modelsWithoutSeed` (or `modelSeedList` if it needs seed data).

### Step 3: Create Repository
Create `repositories/entityrepository/EntityRepository.go` using the **Repository Template** from `docs/TEMPLATES.md`.

Then update: **`docs/REPOSITORIES.md`**

### Step 4: Create Service
Create `services/entity/EntityService.go` using the **Service Template** from `docs/TEMPLATES.md`.

Then update: **`docs/SERVICES.md`**

### Step 5: Create Controller
Create `controllers/entity/entity.go` using the **Controller Template** from `docs/TEMPLATES.md`.

### Step 6: Register Routes
Edit `routes/routes.go`:

```go
import "events-stocks/controllers/entity"

// In ConfigurarRutas, add to protected group:
protected.POST("/entities", entity.CreateEntity)
protected.GET("/entities", entity.GetAllEntities)
protected.GET("/entities/:id", entity.GetEntity)
protected.PUT("/entities/:id", entity.UpdateEntity)
protected.DELETE("/entities/:id", entity.DeleteEntity)
```

Then update: **`docs/ROUTES.md`** and **`docs/CODE_INDEX.md`**

### Step 7: Test
```bash
go run ./cmd/api
```

---

## Adding a Protected Route

```go
// routes/routes.go — inside ConfigurarRutas
protected.POST("/your-endpoint", yourController.YourHandler)
```

Access authenticated user in handler:
```go
cognitoSub := c.Get("cognito_sub").(string)
userEmail  := c.Get("user_email").(string)
cfg        := c.Get("config").(*models.Config)
```

Then update: **`docs/ROUTES.md`**

---

## Adding a Public Route

```go
// routes/routes.go — inside ConfigurarRutas
public.GET("/public-endpoint", yourController.PublicHandler)
```

Then update: **`docs/ROUTES.md`**

---

## Working with Cache

### Invalidate After Mutations (in service layer)
```go
return s.cache.Invalidate("resourceType", "all")
```

### Custom Cache Keys
See `utils/redisKeys.go` and `services/cacheutil/` for custom patterns.

---

## Adding Environment Variable

### 1. Add to Config struct
Edit `models/environmentVariables.go`:
```go
NewVariable string `env:"NEW_VARIABLE"`
```

### 2. Add to `.env` (local dev)
```bash
NEW_VARIABLE=value
```

### 3. Add to GitHub Secrets (production)
Add secret in GitHub repository settings.

### 4. Update deployment workflow
Edit `.github/workflows/deploy-backend.yml`:
```yaml
-e NEW_VARIABLE=${{ secrets.NEW_VARIABLE }} \
```

Then update: **`docs/ENVIRONMENT.md`**

---

## Custom Database Queries

Add to repository file:
```go
func GetEntitiesByClientID(clientID uuid.UUID) ([]models.Entity, error) {
    var entities []models.Entity
    db := configuration.GetDB()
    err := db.Where("client_id = ?", clientID).
        Preload("Client").
        Order("created_at DESC").
        Find(&entities).Error
    return entities, err
}
```

Then update: **`docs/REPOSITORIES.md`**

---

## File Upload Handling

```go
import "events-stocks/repositories/awsrepository"

func UploadFileHandler(c echo.Context) error {
    file, err := c.FormFile("file")
    if err != nil {
        return utils.Error(c, http.StatusBadRequest, "File required", err.Error())
    }
    src, err := file.Open()
    if err != nil {
        return utils.Error(c, http.StatusInternalServerError, "Error opening file", err.Error())
    }
    defer src.Close()

    s3URL, err := awsrepository.UploadFile(src, file.Filename)
    if err != nil {
        return utils.Error(c, http.StatusInternalServerError, "Error uploading", err.Error())
    }
    // Save to Resource model and return
}
```

For image optimization, call `services/resources/ImageOptimizer.go` before upload.

---

## Writing Admin-Only Handlers

Use the boolean `requireRoot` guard pattern to prevent the nil-error trap:

```go
func MyAdminHandler(c echo.Context) error {
    if !requireRoot(c) {
        return nil  // response already written (401 or 403)
    }
    // ... admin logic
}
```

See `controllers/users/users.go` for the full `requireRoot` implementation.

> **Do NOT** use `if err := requireRoot(c); err != nil { return err }` — `utils.Error` returns `nil` on success (c.JSON succeeds), so the guard would always fall through.

---

## Using DB Transactions in Services

Services that must keep two tables in sync use `ports.Transactor`:

```go
type MyService struct {
    tx ports.Transactor
    // ...
}

// In mutation method — nil guard enables unit testing without a real DB:
if s.tx != nil {
    if err := s.tx.Transaction(func(tx *gorm.DB) error {
        if err := tx.Create(&record1).Error; err != nil { return err }
        return tx.Create(&record2).Error
    }); err != nil {
        return fmt.Errorf("transaction failed: %w", err)
    }
} else {
    // Non-transactional fallback for unit tests (tx is nil)
    if err := s.repo.CreateRecord1(&record1); err != nil { return err }
    if err := s.repo.CreateRecord2(&record2); err != nil { return err }
}
```

Wire the transactor in `internal/app/app.go`:
```go
myService := myService.NewMyService(repo, cache, transactor)
```

Pass `nil` in unit tests:
```go
svc := myService.NewMyService(mockRepo, nil, nil)
```

---

## Adding validate:"-" to Nested Struct Fields

When a model contains a **non-pointer** relationship field (e.g. `Event Event` vs `Event *Event`), `go-playground/validator` will recursively validate its fields. This breaks `c.Validate()` if the nested struct has `validate:"required"` tags on its own fields.

**Always add `validate:"-"` to non-pointer relationship fields in models:**

```go
// models/MyEntity.go — CORRECT
type MyEntity struct {
    EventID uuid.UUID `json:"event_id"`
    Event   Event     `gorm:"foreignKey:EventID" json:"-" validate:"-"`  // ← required
}
```

Without `validate:"-"`, any handler that calls `c.Validate(&entity)` will fail with a validation error on `Event.Name` (or any required field inside `Event`) even when the request body is valid.

**Affected models** (already tagged): `Guest.Event`, `Guest.GuestStatus`, `Moment.Invitation`, `Moment.MomentType`, `Invitation.Event`, `Event.EventType`, `Event.EventConfig`.

---

## Adding Middleware

### 1. Create middleware
Create `middleware/custom/custom_middleware.go` using the **Middleware Template** from `docs/TEMPLATES.md`.

### 2. Apply in routes
```go
// Global
e.Use(custom.CustomMiddleware())

// Route group
protected.Use(custom.CustomMiddleware())
```
