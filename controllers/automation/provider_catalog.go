package automation

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/authz"
	"events-stocks/internal/automationagent"
	"events-stocks/models"
	"events-stocks/utils"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

type providerCatalogEntry struct {
	Profile    automationagent.ProviderCatalogProfile `json:"profile"`
	Models     []automationagent.ProviderModel        `json:"models"`
	CapturedAt time.Time                              `json:"captured_at,omitempty"`
	Status     string                                 `json:"status"`
}

type providerCatalogRouteReview struct {
	Operation  string `json:"operation"`
	RouteIndex int    `json:"route_index"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Reason     string `json:"reason"`
}

type providerCatalogResponse struct {
	Providers             []providerCatalogEntry       `json:"providers"`
	RoutesRequiringReview []providerCatalogRouteReview `json:"routes_requiring_review"`
	Changes               []providerCatalogChange      `json:"changes,omitempty"`
}

// providerCatalogChange is a compact, credential-free difference between the
// two most recent audit snapshots. It makes weekly provider changes visible
// without exposing either raw provider response.
type providerCatalogChange struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Kind     string `json:"kind"`
}

// ListProviderCatalog returns the most recently audited, credential-free
// snapshots. It never refreshes remote providers on a browser request; the
// background synchronizer and explicit per-provider refresh own that work.
func ListProviderCatalog(c echo.Context) error {
	if _, err := authz.RequirePrimaryRoot(c); err != nil {
		return authz.Respond(c, err)
	}
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "El catálogo de IA no está disponible", "")
	}
	entries := make([]providerCatalogEntry, 0, len(cataloguedProviders))
	changes := make([]providerCatalogChange, 0)
	byProvider := make(map[string][]automationagent.ProviderModel, len(cataloguedProviders))
	for _, provider := range cataloguedProviders {
		entry := providerCatalogEntry{Profile: automationagent.ProviderCatalogProfileFor(provider), Status: "pending_sync"}
		var snapshot models.AutomationProviderModelSnapshot
		err := configuration.DB.Where("provider = ?", string(provider)).Order("captured_at DESC").First(&snapshot).Error
		if err == nil {
			var catalogue []automationagent.ProviderModel
			if json.Unmarshal([]byte(snapshot.CatalogJSON), &catalogue) == nil {
				entry.Models = catalogue
				entry.CapturedAt = snapshot.CapturedAt
				entry.Status = "ready"
				byProvider[string(provider)] = catalogue
				var previous models.AutomationProviderModelSnapshot
				if configuration.DB.Where("provider = ?", string(provider)).Order("captured_at DESC").Offset(1).First(&previous).Error == nil {
					var priorCatalogue []automationagent.ProviderModel
					if json.Unmarshal([]byte(previous.CatalogJSON), &priorCatalogue) == nil {
						changes = append(changes, diffProviderCatalogues(string(provider), priorCatalogue, catalogue)...)
					}
				}
			} else {
				entry.Status = "invalid_snapshot"
			}
		} else if err != nil && err != gorm.ErrRecordNotFound {
			return utils.Error(c, http.StatusInternalServerError, "No se pudo cargar el catálogo de IA", "")
		}
		entries = append(entries, entry)
	}
	return utils.Success(c, http.StatusOK, "Catálogo de IA obtenido", providerCatalogResponse{
		Providers:             entries,
		RoutesRequiringReview: providerCatalogRoutesRequiringReview(byProvider),
		Changes:               changes,
	})
}

func diffProviderCatalogues(provider string, previous, current []automationagent.ProviderModel) []providerCatalogChange {
	prior := make(map[string]automationagent.ProviderModel, len(previous))
	for _, model := range previous {
		prior[model.ID] = model
	}
	next := make(map[string]automationagent.ProviderModel, len(current))
	for _, model := range current {
		next[model.ID] = model
	}
	changes := make([]providerCatalogChange, 0)
	for id, model := range next {
		before, existed := prior[id]
		if !existed {
			changes = append(changes, providerCatalogChange{Provider: provider, Model: model.ID, Kind: "added"})
			continue
		}
		if before.InputMicrosPerMillion != model.InputMicrosPerMillion || before.OutputMicrosPerMillion != model.OutputMicrosPerMillion || before.CachedMicrosPerMillion != model.CachedMicrosPerMillion || before.CacheWriteMicrosPerMillion != model.CacheWriteMicrosPerMillion || before.PricingKnown != model.PricingKnown {
			changes = append(changes, providerCatalogChange{Provider: provider, Model: model.ID, Kind: "pricing_changed"})
		}
		if before.ContextWindowTokens != model.ContextWindowTokens || before.MaxOutputTokens != model.MaxOutputTokens || before.SupportsReasoning != model.SupportsReasoning || before.SupportsTools != model.SupportsTools || before.SupportsStructuredOutput != model.SupportsStructuredOutput || strings.Join(before.InputModalities, ",") != strings.Join(model.InputModalities, ",") || strings.Join(before.OutputModalities, ",") != strings.Join(model.OutputModalities, ",") {
			changes = append(changes, providerCatalogChange{Provider: provider, Model: model.ID, Kind: "capabilities_changed"})
		}
		if before.Availability != model.Availability || before.Supported != model.Supported {
			changes = append(changes, providerCatalogChange{Provider: provider, Model: model.ID, Kind: "availability_changed"})
		}
	}
	for id, model := range prior {
		if _, exists := next[id]; !exists {
			changes = append(changes, providerCatalogChange{Provider: provider, Model: model.ID, Kind: "removed"})
		}
	}
	if len(changes) > 80 {
		changes = changes[:80]
	}
	return changes
}

func providerCatalogRoutesRequiringReview(byProvider map[string][]automationagent.ProviderModel) []providerCatalogRouteReview {
	if configuration.DB == nil {
		return nil
	}
	var policies []models.AutomationAIActionPolicy
	if configuration.DB.Find(&policies).Error != nil {
		return nil
	}
	reviews := make([]providerCatalogRouteReview, 0)
	for _, policy := range policies {
		routes, err := policy.Routes()
		if err != nil {
			continue
		}
		for index, route := range routes {
			provider := automationagent.Provider(strings.ToLower(strings.TrimSpace(route.Provider)))
			catalogue, synchronized := byProvider[string(provider)]
			review := providerCatalogRouteReview{Operation: policy.Operation, RouteIndex: index, Provider: route.Provider, Model: route.Model}
			if !synchronized {
				review.Reason = "catalog_pending_sync"
				reviews = append(reviews, review)
				continue
			}
			model, variant := providerModelSelector(catalogue, provider, route.Model)
			if model == nil || !model.Supported {
				review.Reason = "model_unavailable_or_incompatible"
				reviews = append(reviews, review)
				continue
			}
			if variant != "" && !providerModelHasVariant(*model, variant) {
				review.Reason = "variant_unavailable"
				reviews = append(reviews, review)
			}
		}
	}
	return reviews
}
