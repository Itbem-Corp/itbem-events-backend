package automation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"events-stocks/configuration"
	"events-stocks/internal/authz"
	"events-stocks/internal/automationagent"
	"events-stocks/models"
	"events-stocks/utils"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	defaultAIActionPolicyRevisionPageSize = 25
	maxAIActionPolicyRevisionPageSize     = 100
	maxAIActionPolicyRevisionCursorBytes  = 2048
)

type aiActionPolicyRequest struct {
	// Routes is an ordered primary/fallback/final-fallback chain. The legacy
	// fields remain accepted for existing dashboard clients as a one-route
	// policy during rollout.
	Routes           []models.AutomationAIActionRoute `json:"routes"`
	Provider         automationagent.Provider         `json:"provider"`
	Model            string                           `json:"model"`
	ReasoningEnabled bool                             `json:"reasoning_enabled"`
	ReasoningEffort  string                           `json:"reasoning_effort"`
}

type aiActionPolicyResponse struct {
	Operation        string                           `json:"operation"`
	Routes           []models.AutomationAIActionRoute `json:"routes,omitempty"`
	Provider         string                           `json:"provider"`
	Model            string                           `json:"model"`
	ReasoningEnabled bool                             `json:"reasoning_enabled"`
	ReasoningEffort  string                           `json:"reasoning_effort"`
	Revision         int64                            `json:"revision"`
	Configured       bool                             `json:"configured"`
}

type aiActionPolicyRevisionDTO struct {
	ID         uuid.UUID                        `json:"id"`
	Operation  string                           `json:"operation"`
	Revision   int64                            `json:"revision"`
	Routes     []models.AutomationAIActionRoute `json:"routes"`
	RoutesHash string                           `json:"routes_hash"`
	ChangedBy  string                           `json:"changed_by"`
	CreatedAt  time.Time                        `json:"created_at"`
}

type aiActionPolicyRevisionPage struct {
	Operation  string                      `json:"operation"`
	Items      []aiActionPolicyRevisionDTO `json:"items"`
	Limit      int                         `json:"limit"`
	NextCursor string                      `json:"next_cursor"`
}

type aiActionPolicyRevisionCursor struct {
	Version   int    `json:"v"`
	Operation string `json:"o"`
	Revision  int64  `json:"r"`
	ID        string `json:"i"`
}

func isConfigurableAutomationOperation(operation string) bool {
	return automationagent.IsInferenceOperation(operation)
}

func validReasoningEffort(effort string) bool {
	return automationagent.IsAllowedReasoningEffort(effort)
}

// ListAIActionPolicies exposes only routing metadata. Provider credentials never leave the server.
func ListAIActionPolicies(c echo.Context) error {
	if _, err := authz.RequirePrimaryRoot(c); err != nil {
		return authz.Respond(c, err)
	}

	var rows []models.AutomationAIActionPolicy
	if err := configuration.DB.Find(&rows).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "No se pudieron cargar las políticas de IA", "")
	}

	byOperation := make(map[string]models.AutomationAIActionPolicy, len(rows))
	for _, row := range rows {
		byOperation[row.Operation] = row
	}

	response := make([]aiActionPolicyResponse, 0, len(automationagent.InferenceOperations))
	for _, operation := range automationagent.InferenceOperations {
		row, configured := byOperation[operation]
		routes, routeErr := row.Routes()
		if routeErr != nil {
			configured = false
			routes = nil
		}
		response = append(response, aiActionPolicyResponse{
			Operation:        operation,
			Routes:           routes,
			Provider:         row.Provider,
			Model:            row.Model,
			ReasoningEnabled: row.ReasoningEnabled,
			ReasoningEffort:  row.ReasoningEffort,
			Revision:         row.Revision,
			Configured:       configured,
		})
	}

	return utils.Success(c, http.StatusOK, "Políticas de IA obtenidas", response)
}

func UpsertAIActionPolicy(c echo.Context) error {
	rootUser, err := authz.RequirePrimaryRoot(c)
	if err != nil {
		return authz.Respond(c, err)
	}

	operation := strings.TrimSpace(c.Param("operation"))
	if !isConfigurableAutomationOperation(operation) {
		return utils.Error(c, http.StatusBadRequest, "La acción de automatización no es configurable", "")
	}

	var request aiActionPolicyRequest
	raw, err := io.ReadAll(io.LimitReader(c.Request().Body, (16<<10)+1))
	if err != nil || len(raw) == 0 || len(raw) > 16<<10 {
		return utils.Error(c, http.StatusBadRequest, "Configuración de IA inválida", "")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return utils.Error(c, http.StatusBadRequest, "Configuración de IA inválida", "")
	}
	if inferenceCredentials == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "El almacén de credenciales no está disponible", "")
	}
	routes, routeErr := validatedActionRoutes(c, request)
	if routeErr != nil {
		return utils.Error(c, http.StatusBadRequest, routeErr.Error(), "")
	}

	row, err := persistAIActionPolicyRevision(configuration.DB, operation, routes, rootUser.ID.String())
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "No se pudo guardar la política de IA", "")
	}

	return utils.Success(c, http.StatusOK, "Política de IA guardada", aiActionPolicyResponse{
		Operation: operation, Routes: routes, Provider: row.Provider, Model: row.Model,
		ReasoningEnabled: row.ReasoningEnabled, ReasoningEffort: row.ReasoningEffort, Revision: row.Revision, Configured: true,
	})
}

// persistAIActionPolicyRevision serializes writers on the active policy row and
// commits the current route and its immutable snapshot together. ON CONFLICT
// on the operation key makes initial creation race-safe before the row lock is
// acquired; the unique operation/revision index is the durable final guard.
func persistAIActionPolicyRevision(db *gorm.DB, operation string, routes []models.AutomationAIActionRoute, actor string) (models.AutomationAIActionPolicy, error) {
	if db == nil || !isConfigurableAutomationOperation(operation) || len(routes) == 0 || len(routes) > models.MaxAutomationAIActionRoutes || strings.TrimSpace(actor) == "" {
		return models.AutomationAIActionPolicy{}, errors.New("invalid action policy revision input")
	}
	routesJSON, err := json.Marshal(routes)
	if err != nil {
		return models.AutomationAIActionPolicy{}, err
	}
	routesHash := fmt.Sprintf("%x", sha256.Sum256(routesJSON))
	primary := routes[0]
	now := time.Now().UTC()
	var saved models.AutomationAIActionPolicy
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO automation_ai_action_policies
			(operation, provider, model, reasoning_enabled, reasoning_effort, routes_json, revision, updated_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, CAST(? AS jsonb), 0, ?, ?, ?)
			ON CONFLICT (operation) DO NOTHING`,
			operation, primary.Provider, primary.Model, primary.ReasoningEnabled, primary.ReasoningEffort, string(routesJSON), actor, now, now).Error; err != nil {
			return err
		}
		var current models.AutomationAIActionPolicy
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("operation = ?", operation).First(&current).Error; err != nil {
			return err
		}
		if current.Revision < 0 || current.Revision == math.MaxInt64 {
			return errors.New("action policy revision counter is exhausted")
		}
		nextRevision := current.Revision + 1
		updates := map[string]any{
			"provider": primary.Provider, "model": primary.Model,
			"reasoning_enabled": primary.ReasoningEnabled, "reasoning_effort": primary.ReasoningEffort,
			"routes_json": string(routesJSON), "revision": nextRevision, "updated_by": actor, "updated_at": now,
		}
		result := tx.Model(&models.AutomationAIActionPolicy{}).
			Where("id = ? AND revision = ?", current.ID, current.Revision).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("action policy changed during revision")
		}
		if err := tx.Exec(`INSERT INTO automation_ai_action_policy_revisions
			(policy_id, operation, revision, routes_json, routes_hash, changed_by, created_at)
			VALUES (?, ?, ?, CAST(? AS jsonb), ?, ?, ?)`,
			current.ID, operation, nextRevision, string(routesJSON), routesHash, actor, now).Error; err != nil {
			return err
		}
		saved = current
		saved.Provider = primary.Provider
		saved.Model = primary.Model
		saved.ReasoningEnabled = primary.ReasoningEnabled
		saved.ReasoningEffort = primary.ReasoningEffort
		saved.RoutesJSON = string(routesJSON)
		saved.Revision = nextRevision
		saved.UpdatedBy = actor
		saved.UpdatedAt = now
		return nil
	})
	return saved, err
}

// ListAIActionPolicyRevisions returns a bounded, credential-free history page
// for one inference operation. Revision/id ordering and an operation-scoped
// keyset cursor keep pagination deterministic and non-transferable.
func ListAIActionPolicyRevisions(c echo.Context) error {
	if _, err := authz.RequirePrimaryRoot(c); err != nil {
		return authz.Respond(c, err)
	}
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "No se pudo cargar el historial de políticas de IA", "")
	}
	operation := strings.TrimSpace(c.Param("operation"))
	if !isConfigurableAutomationOperation(operation) {
		return utils.Error(c, http.StatusBadRequest, "La acción de automatización no es configurable", "")
	}
	limit, err := aiActionPolicyRevisionPageSize(c.QueryParam("limit"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Límite de historial inválido", "")
	}
	cursor, err := decodeAIActionPolicyRevisionCursor(c.QueryParam("cursor"), operation)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Cursor de historial inválido", "")
	}
	query := configuration.DB.Model(&models.AutomationAIActionPolicyRevision{}).
		Select("id", "operation", "revision", "routes_json", "routes_hash", "changed_by", "created_at").
		Where("operation = ?", operation)
	if cursor != nil {
		query = query.Where("(revision < ? OR (revision = ? AND id < ?))", cursor.Revision, cursor.Revision, uuid.Must(uuid.FromString(cursor.ID)))
	}
	var rows []models.AutomationAIActionPolicyRevision
	if err := query.Order("revision DESC, id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "No se pudo cargar el historial de políticas de IA", "")
	}
	page := aiActionPolicyRevisionPage{Operation: operation, Items: make([]aiActionPolicyRevisionDTO, 0, limit), Limit: limit}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		page.NextCursor = encodeAIActionPolicyRevisionCursor(aiActionPolicyRevisionCursor{Version: 1, Operation: operation, Revision: last.Revision, ID: last.ID.String()})
	}
	for _, row := range rows {
		var routes []models.AutomationAIActionRoute
		if err := json.Unmarshal([]byte(row.RoutesJSON), &routes); err != nil || len(routes) == 0 || len(routes) > models.MaxAutomationAIActionRoutes {
			return utils.Error(c, http.StatusInternalServerError, "No se pudo cargar el historial de políticas de IA", "")
		}
		page.Items = append(page.Items, aiActionPolicyRevisionDTO{
			ID: row.ID, Operation: row.Operation, Revision: row.Revision, Routes: routes,
			RoutesHash: row.RoutesHash, ChangedBy: row.ChangedBy, CreatedAt: row.CreatedAt,
		})
	}
	return utils.Success(c, http.StatusOK, "Historial de políticas de IA obtenido", page)
}

func aiActionPolicyRevisionPageSize(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultAIActionPolicyRevisionPageSize, nil
	}
	limit, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || limit < 1 || limit > maxAIActionPolicyRevisionPageSize {
		return 0, errors.New("limit must be between 1 and 100")
	}
	return limit, nil
}

func encodeAIActionPolicyRevisionCursor(cursor aiActionPolicyRevisionCursor) string {
	payload, err := json.Marshal(cursor)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeAIActionPolicyRevisionCursor(raw, operation string) (*aiActionPolicyRevisionCursor, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	if len(raw) > maxAIActionPolicyRevisionCursorBytes {
		return nil, errors.New("cursor too large")
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, errors.New("cursor encoding is invalid")
	}
	var cursor aiActionPolicyRevisionCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || cursor.Version != 1 || cursor.Operation != operation || cursor.Revision < 1 {
		return nil, errors.New("cursor is invalid or belongs to another operation")
	}
	id, err := uuid.FromString(cursor.ID)
	if err != nil || id == uuid.Nil {
		return nil, errors.New("cursor id must be a non-zero UUID")
	}
	cursor.ID = id.String()
	return &cursor, nil
}

func validatedActionRoutes(c echo.Context, request aiActionPolicyRequest) ([]models.AutomationAIActionRoute, error) {
	routes := request.Routes
	if len(routes) == 0 {
		routes = []models.AutomationAIActionRoute{{Provider: string(request.Provider), Model: request.Model, ReasoningEnabled: request.ReasoningEnabled, ReasoningEffort: request.ReasoningEffort}}
	}
	if len(routes) == 0 || len(routes) > models.MaxAutomationAIActionRoutes {
		return nil, errors.New("selecciona entre una y tres rutas de IA")
	}
	seen := make(map[string]struct{}, len(routes))
	for index := range routes {
		route := &routes[index]
		route.Provider = strings.ToLower(strings.TrimSpace(route.Provider))
		route.Model = strings.TrimSpace(route.Model)
		route.ReasoningEffort = strings.ToLower(strings.TrimSpace(route.ReasoningEffort))
		provider := automationagent.Provider(route.Provider)
		// Migrate the previously exposed DeepSeek value when an administrator
		// next saves a route. OpenRouter still accepts "medium" natively.
		if provider == automationagent.ProviderDeepSeek && route.ReasoningEffort == "medium" {
			route.ReasoningEffort = "high"
		}
		if route.Provider == "" || len(route.Model) == 0 || len(route.Model) > 200 || !validReasoningEffort(route.ReasoningEffort) {
			return nil, errors.New("proveedor, modelo o nivel de razonamiento inválido")
		}
		if _, configured := automationagent.DefaultProviderEndpoint(provider); !configured {
			return nil, errors.New("proveedor de IA no permitido")
		}
		if provider == automationagent.ProviderOpenCodeGo {
			if _, supported := automationagent.OpenCodeGoModelAPI(route.Model); !supported {
				return nil, errors.New("el modelo de OpenCode Go no usa una familia de API compatible")
			}
		}
		if provider == automationagent.ProviderOpenAI {
			if _, supported := automationagent.OpenAIModelAPI(route.Model); !supported {
				return nil, errors.New("el modelo de OpenAI no usa una familia de API compatible")
			}
		}
		if !route.ReasoningEnabled {
			route.ReasoningEffort = ""
		}
		identity := route.Provider + "\x00" + route.Model
		if _, duplicate := seen[identity]; duplicate {
			return nil, errors.New("una ruta de fallback no puede repetir proveedor y modelo")
		}
		seen[identity] = struct{}{}
		apiKey, err := inferenceCredentials.APIKey(c.Request().Context(), route.Provider)
		if err != nil {
			return nil, errors.New("primero autentica cada proveedor en la sección de credenciales")
		}
		// Re-read the provider catalogue at save time. Browser state can be
		// stale and the catalog is the authoritative per-model capability source.
		catalogue, err := automationagent.ListProviderModels(c.Request().Context(), provider, apiKey, nil)
		if err != nil {
			return nil, errors.New("no se pudo validar el catálogo actual del proveedor")
		}
		selected, variant := providerModelSelector(catalogue, provider, route.Model)
		if selected == nil || !selected.Supported {
			return nil, errors.New("el modelo seleccionado ya no es compatible; recarga el catálogo")
		}
		if variant != "" && !providerModelHasVariant(*selected, variant) {
			return nil, errors.New("la variante del modelo ya no está disponible; recarga el catálogo")
		}
		if route.ReasoningEnabled && !modelSupportsReasoningSelection(provider, *selected, route.ReasoningEffort) {
			return nil, errors.New("ese modelo no admite el nivel de razonamiento seleccionado")
		}
	}
	return routes, nil
}

// MiniMax M3 has a binary thinking control, not named effort levels. Keep the
// exception scoped to the adapter that actually honors ReasoningEnabled;
// reasoning capability alone does not imply a configurable wire parameter.
func modelSupportsReasoningSelection(provider automationagent.Provider, model automationagent.ProviderModel, effort string) bool {
	if !model.SupportsReasoning {
		return false
	}
	if len(model.ReasoningEfforts) == 0 {
		return provider == automationagent.ProviderMiniMax && strings.EqualFold(model.ID, "MiniMax-M3") && effort == ""
	}
	return containsReasoningEffort(model.ReasoningEfforts, effort)
}

func providerModelSelector(models []automationagent.ProviderModel, provider automationagent.Provider, selector string) (*automationagent.ProviderModel, string) {
	variant := ""
	if provider == automationagent.ProviderOpenCodeGo {
		var hasVariant bool
		selector, variant, hasVariant = strings.Cut(selector, "#")
		if !hasVariant {
			variant = ""
		}
	}
	for index := range models {
		if models[index].ID == selector {
			return &models[index], variant
		}
	}
	return nil, ""
}

func providerModelHasVariant(model automationagent.ProviderModel, requested string) bool {
	for _, variant := range model.Variants {
		if variant.ID == requested {
			return true
		}
	}
	return false
}

func containsReasoningEffort(efforts []string, requested string) bool {
	for _, effort := range efforts {
		if effort == requested {
			return true
		}
	}
	return false
}

// ListProviderModels loads non-secret model metadata using the stored provider credential.
func ListProviderModels(c echo.Context) error {
	if _, err := authz.RequirePrimaryRoot(c); err != nil {
		return authz.Respond(c, err)
	}
	provider := automationagent.Provider(strings.TrimSpace(c.Param("provider")))
	if _, configured := automationagent.DefaultProviderEndpoint(provider); !configured {
		return utils.Error(c, http.StatusBadRequest, "Proveedor de IA no permitido", "")
	}
	if inferenceCredentials == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "El almacén de credenciales no está disponible", "")
	}
	metadataContext, cancelMetadata := context.WithTimeout(c.Request().Context(), 30*time.Second)
	metadata, metadataErr := automationagent.FetchProviderModelMetadata(metadataContext, nil)
	cancelMetadata()
	apiKey, credentialErr := inferenceCredentials.APIKey(c.Request().Context(), string(provider))
	if credentialErr != nil {
		if metadataErr != nil {
			return utils.Error(c, http.StatusBadRequest, "Primero autentica este proveedor en la sección de credenciales", "")
		}
		models := automationagent.PublicProviderModels(provider, metadata)
		if len(models) == 0 {
			return utils.Error(c, http.StatusBadGateway, "No se pudo obtener el catálogo público de modelos", "")
		}
		snapshot := recordProviderCatalogSnapshot(provider, models)
		return utils.Success(c, http.StatusOK, "Catálogo público obtenido; autentica el proveedor para confirmar disponibilidad", providerModelsResponse{Models: models, Snapshot: snapshot})
	}
	models, err := automationagent.ListProviderModelsWithMetadata(c.Request().Context(), provider, apiKey, nil, metadata)
	if err != nil {
		return utils.Error(c, http.StatusBadGateway, "No se pudo obtener el catálogo de modelos del proveedor", "")
	}
	// Provider catalogues are upstream-controlled. Keep the key out of both the
	// settings response and the audit snapshot even if a provider accidentally
	// echoes it in a model label or capability string.
	models, snapshot := recordCredentialSafeProviderCatalogSnapshot(provider, apiKey, models)
	return utils.Success(c, http.StatusOK, "Modelos obtenidos", providerModelsResponse{Models: models, Snapshot: snapshot})
}

func redactProviderCatalogCredential(catalogue []automationagent.ProviderModel, secret string) []automationagent.ProviderModel {
	secret = strings.TrimSpace(secret)
	if secret == "" || len(catalogue) == 0 {
		return catalogue
	}
	redact := func(value string) string { return strings.ReplaceAll(value, secret, "[REDACTED]") }
	redactList := func(values []string) []string {
		if values == nil {
			return nil
		}
		result := make([]string, len(values))
		for index, value := range values {
			result[index] = redact(value)
		}
		return result
	}
	result := append([]automationagent.ProviderModel(nil), catalogue...)
	for index := range result {
		model := &result[index]
		model.ID = redact(model.ID)
		model.Name = redact(model.Name)
		model.Description = redact(model.Description)
		model.Publisher = redact(model.Publisher)
		model.Family = redact(model.Family)
		model.ReleaseDate = redact(model.ReleaseDate)
		model.LastUpdated = redact(model.LastUpdated)
		model.KnowledgeCutoff = redact(model.KnowledgeCutoff)
		model.PricingSource = redact(model.PricingSource)
		model.GatewayAPI = redact(model.GatewayAPI)
		model.Availability = redact(model.Availability)
		model.Source = redact(model.Source)
		model.InputModalities = redactList(model.InputModalities)
		model.OutputModalities = redactList(model.OutputModalities)
		model.SupportedParameters = redactList(model.SupportedParameters)
		model.CapabilityTags = redactList(model.CapabilityTags)
		model.ReasoningEfforts = redactList(model.ReasoningEfforts)
		if model.Variants != nil {
			variants := append([]automationagent.ProviderModelVariant(nil), model.Variants...)
			for variantIndex := range variants {
				variants[variantIndex].ID = redact(variants[variantIndex].ID)
				variants[variantIndex].Name = redact(variants[variantIndex].Name)
			}
			model.Variants = variants
		}
		if model.PricingTiers != nil {
			tiers := append([]automationagent.ProviderModelPricingTier(nil), model.PricingTiers...)
			for tierIndex := range tiers {
				tiers[tierIndex].Kind = redact(tiers[tierIndex].Kind)
			}
			model.PricingTiers = tiers
		}
	}
	return result
}

// recordCredentialSafeProviderCatalogSnapshot is shared by the interactive
// model route and background synchronizer so untrusted upstream catalogue text
// is redacted before either persistence or API exposure.
func recordCredentialSafeProviderCatalogSnapshot(provider automationagent.Provider, secret string, catalogue []automationagent.ProviderModel) ([]automationagent.ProviderModel, providerCatalogSnapshotResponse) {
	safeCatalogue := redactProviderCatalogCredential(catalogue, secret)
	return safeCatalogue, recordProviderCatalogSnapshot(provider, safeCatalogue)
}

type providerModelsResponse struct {
	Models   []automationagent.ProviderModel `json:"models"`
	Snapshot providerCatalogSnapshotResponse `json:"snapshot"`
}

type providerCatalogSnapshotResponse struct {
	CatalogHash         string    `json:"catalog_hash,omitempty"`
	PreviousCatalogHash string    `json:"previous_catalog_hash,omitempty"`
	CapturedAt          time.Time `json:"captured_at,omitempty"`
	ModelCount          int       `json:"model_count"`
	Changed             bool      `json:"changed"`
}

func recordProviderCatalogSnapshot(provider automationagent.Provider, catalogue []automationagent.ProviderModel) providerCatalogSnapshotResponse {
	payload, err := json.Marshal(catalogue)
	if err != nil || len(payload) > 2<<20 {
		return providerCatalogSnapshotResponse{ModelCount: len(catalogue)}
	}
	// Include provider in the digest. The database uniqueness constraint is
	// global, and two providers can legitimately publish an identical catalogue.
	// Provider-scoped hashing preserves a separate audit record for both.
	hashInput := append(append([]byte(string(provider)), 0), payload...)
	hash := fmt.Sprintf("%x", sha256.Sum256(hashInput))
	result := providerCatalogSnapshotResponse{CatalogHash: hash, ModelCount: len(catalogue)}
	if configuration.DB == nil {
		return result
	}
	var previous models.AutomationProviderModelSnapshot
	previousFound := configuration.DB.Where("provider = ?", string(provider)).Order("captured_at DESC").First(&previous).Error == nil
	if previousFound {
		result.PreviousCatalogHash = previous.CatalogHash
		result.CapturedAt = previous.CapturedAt
		if previous.CatalogHash == hash {
			return result
		}
		result.Changed = true
	}
	now := time.Now().UTC()
	record := models.AutomationProviderModelSnapshot{Provider: string(provider), CatalogHash: hash, ModelCount: len(catalogue), CatalogJSON: string(payload), CapturedAt: now}
	if err := configuration.DB.Create(&record).Error; err == nil {
		result.CapturedAt = now
	}
	return result
}

func actionPolicyForOperation(db *gorm.DB, operation string) (models.AutomationAIActionPolicy, bool) {
	if db == nil || !isConfigurableAutomationOperation(operation) {
		return models.AutomationAIActionPolicy{}, false
	}
	var policy models.AutomationAIActionPolicy
	if err := db.Where("operation = ?", operation).First(&policy).Error; err != nil {
		return models.AutomationAIActionPolicy{}, false
	}
	return policy, true
}
