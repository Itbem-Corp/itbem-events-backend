# Code Templates

> Copy-paste and replace `{{placeholders}}`. Use these for consistent, fast scaffolding.
> **Module name**: `events-stocks` (not `itbem-events-backend`)

## Model Template

```go
package models

import (
    "time"
    "github.com/gofrs/uuid"
)

type {{ModelName}} struct {
    ID          uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
    Name        string     `gorm:"type:varchar(255);not null" json:"name"`
    Description string     `gorm:"type:text" json:"description"`

    // Foreign Keys
    ClientID    uuid.UUID  `gorm:"type:uuid;not null" json:"client_id"`

    // Relationships
    Client      *Client    `gorm:"foreignKey:ClientID" json:"client,omitempty"`

    // Timestamps
    CreatedAt   time.Time  `json:"created_at"`
    UpdatedAt   time.Time  `json:"updated_at"`
    // DeletedAt gorm.DeletedAt `gorm:"index" json:"deleted_at,omitempty"` // soft delete
}
```

**Replace**: `{{ModelName}}`. Add/remove fields and relationships as needed.

---

## Repository Template

```go
package {{domain}}repository

import (
    "github.com/gofrs/uuid"
    "events-stocks/configuration"
    "events-stocks/models"
    "events-stocks/repositories/gormrepository"
)

func Create{{ModelName}}(obj *models.{{ModelName}}) error {
    return gormrepository.Insert(obj)
}

func Get{{ModelName}}ByID(id uuid.UUID) (*models.{{ModelName}}, error) {
    var obj models.{{ModelName}}
    err := gormrepository.FindByID(id, &obj)
    return &obj, err
}

func Update{{ModelName}}(id uuid.UUID, obj *models.{{ModelName}}) error {
    return gormrepository.Update(id, obj)
}

func Delete{{ModelName}}(id uuid.UUID) error {
    return gormrepository.Delete(id, &models.{{ModelName}}{})
}

func GetAll{{ModelName}}s() ([]models.{{ModelName}}, error) {
    var items []models.{{ModelName}}
    err := gormrepository.FindAll(&items)
    return items, err
}

// Custom query example
func Get{{ModelName}}sByClientID(clientID uuid.UUID) ([]models.{{ModelName}}, error) {
    var items []models.{{ModelName}}
    db := configuration.GetDB()
    err := db.Where("client_id = ?", clientID).
        Order("created_at DESC").
        Find(&items).Error
    return items, err
}
```

**Replace**: `{{domain}}`, `{{ModelName}}`.

---

## Service Template

```go
package {{domain}}Service

import (
    "errors"
    "github.com/gofrs/uuid"
    "events-stocks/models"
    "events-stocks/repositories/{{domain}}repository"
    "events-stocks/repositories/redisrepository"
)

func Create{{ModelName}}(obj *models.{{ModelName}}) error {
    if obj.Name == "" {
        return errors.New("name is required")
    }
    if err := {{domain}}repository.Create{{ModelName}}(obj); err != nil {
        return err
    }
    return redisrepository.Invalidate("{{cacheKey}}", "all")
}

func Get{{ModelName}}(id uuid.UUID) (*models.{{ModelName}}, error) {
    return {{domain}}repository.Get{{ModelName}}ByID(id)
}

func Update{{ModelName}}(id uuid.UUID, obj *models.{{ModelName}}) error {
    if err := {{domain}}repository.Update{{ModelName}}(id, obj); err != nil {
        return err
    }
    return redisrepository.Invalidate("{{cacheKey}}", "all")
}

func Delete{{ModelName}}(id uuid.UUID) error {
    if err := {{domain}}repository.Delete{{ModelName}}(id); err != nil {
        return err
    }
    return redisrepository.Invalidate("{{cacheKey}}", "all")
}

func GetAll{{ModelName}}s() ([]models.{{ModelName}}, error) {
    return {{domain}}repository.GetAll{{ModelName}}s()
}
```

**Replace**: `{{domain}}`, `{{ModelName}}`, `{{cacheKey}}`.

---

## Controller Template

```go
package {{domain}}

import (
    "net/http"
    "github.com/gofrs/uuid"
    "github.com/labstack/echo/v4"
    "events-stocks/models"
    {{domain}}Service "events-stocks/services/{{domain}}"
    "events-stocks/utils"
)

func Create{{ModelName}}(c echo.Context) error {
    var obj models.{{ModelName}}
    if err := c.Bind(&obj); err != nil {
        return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
    }
    // cognitoSub := c.Get("cognito_sub").(string)
    if err := {{domain}}Service.Create{{ModelName}}(&obj); err != nil {
        return utils.Error(c, http.StatusInternalServerError, "Failed to create", err.Error())
    }
    return utils.Success(c, http.StatusCreated, "{{ModelName}} created", obj)
}

func Get{{ModelName}}(c echo.Context) error {
    id, err := uuid.FromString(c.Param("id"))
    if err != nil {
        return utils.Error(c, http.StatusBadRequest, "Invalid ID", err.Error())
    }
    obj, err := {{domain}}Service.Get{{ModelName}}(id)
    if err != nil {
        return utils.Error(c, http.StatusNotFound, "Not found", err.Error())
    }
    return utils.Success(c, http.StatusOK, "{{ModelName}} retrieved", obj)
}

func GetAll{{ModelName}}s(c echo.Context) error {
    items, err := {{domain}}Service.GetAll{{ModelName}}s()
    if err != nil {
        return utils.Error(c, http.StatusInternalServerError, "Failed to retrieve", err.Error())
    }
    return utils.Success(c, http.StatusOK, "{{ModelName}}s retrieved", items)
}

func Update{{ModelName}}(c echo.Context) error {
    id, err := uuid.FromString(c.Param("id"))
    if err != nil {
        return utils.Error(c, http.StatusBadRequest, "Invalid ID", err.Error())
    }
    var obj models.{{ModelName}}
    if err := c.Bind(&obj); err != nil {
        return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
    }
    if err := {{domain}}Service.Update{{ModelName}}(id, &obj); err != nil {
        return utils.Error(c, http.StatusInternalServerError, "Failed to update", err.Error())
    }
    return utils.Success(c, http.StatusOK, "{{ModelName}} updated", obj)
}

func Delete{{ModelName}}(c echo.Context) error {
    id, err := uuid.FromString(c.Param("id"))
    if err != nil {
        return utils.Error(c, http.StatusBadRequest, "Invalid ID", err.Error())
    }
    if err := {{domain}}Service.Delete{{ModelName}}(id); err != nil {
        return utils.Error(c, http.StatusInternalServerError, "Failed to delete", err.Error())
    }
    return utils.Success(c, http.StatusOK, "{{ModelName}} deleted", nil)
}
```

**Replace**: `{{domain}}`, `{{ModelName}}`.

---

## Routes Registration Template

```go
// In routes/routes.go — inside ConfigurarRutas
import "events-stocks/controllers/{{domain}}"

protected.POST("/{{plural}}", {{domain}}.Create{{ModelName}})
protected.GET("/{{plural}}", {{domain}}.GetAll{{ModelName}}s)
protected.GET("/{{plural}}/:id", {{domain}}.Get{{ModelName}})
protected.PUT("/{{plural}}/:id", {{domain}}.Update{{ModelName}})
protected.DELETE("/{{plural}}/:id", {{domain}}.Delete{{ModelName}})
```

---

## Middleware Template

```go
package {{name}}

import "github.com/labstack/echo/v4"

func {{Name}}Middleware() echo.MiddlewareFunc {
    return func(next echo.HandlerFunc) echo.HandlerFunc {
        return func(c echo.Context) error {
            // pre-processing
            err := next(c)
            // post-processing
            return err
        }
    }
}
```

---

## Test Template

```go
package {{domain}}_test

import (
    "testing"
    "github.com/stretchr/testify/assert"
    "events-stocks/models"
    {{domain}}Service "events-stocks/services/{{domain}}"
)

func TestCreate{{ModelName}}(t *testing.T) {
    tests := []struct {
        name    string
        input   *models.{{ModelName}}
        wantErr bool
    }{
        {"valid", &models.{{ModelName}}{Name: "Test"}, false},
        {"empty name", &models.{{ModelName}}{Name: ""}, true},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            err := {{domain}}Service.Create{{ModelName}}(tt.input)
            if tt.wantErr {
                assert.Error(t, err)
            } else {
                assert.NoError(t, err)
            }
        })
    }
}
```

---

## Quick Copy Snippets

```go
// UUID primary key
ID uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`

// Required string
Name string `gorm:"type:varchar(255);not null" json:"name"`

// Optional text
Description string `gorm:"type:text" json:"description"`

// Foreign key
ClientID uuid.UUID `gorm:"type:uuid;not null" json:"client_id"`

// BelongsTo relationship
Client *Client `gorm:"foreignKey:ClientID" json:"client,omitempty"`

// Timestamps
CreatedAt time.Time `json:"created_at"`
UpdatedAt time.Time `json:"updated_at"`

// Soft delete
DeletedAt gorm.DeletedAt `gorm:"index" json:"deleted_at,omitempty"`

// Success response
return utils.Success(c, http.StatusOK, "message", data)

// Error response
return utils.Error(c, http.StatusBadRequest, "message", err.Error())

// Cache invalidation
return redisrepository.Invalidate("resourceType", "all")
```
