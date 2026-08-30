package delivery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"events-stocks/configuration"
	"events-stocks/models"
	"events-stocks/services/deliverydecomposition"
	"events-stocks/services/deliveryworkflow"
	"events-stocks/utils"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type decompositionRequest struct {
	Structured map[string]any `json:"structured"`
}

type applyDecompositionRequest struct {
	Comment string `json:"comment"`
}

// ListRequestDecompositions exposes the immutable proposals before a reviewer
// turns one into operational work items.
func ListRequestDecompositions(c echo.Context) error {
	projectID, requestID, err := decompositionRouteIDs(c)
	if err != nil {
		return err
	}
	if _, err := projectActor(c, projectID, deliveryView); err != nil {
		return err
	}
	if err := requestInProject(requestID, projectID); err != nil {
		return lookup(c, "Delivery request", err)
	}
	var proposals []models.DeliveryDecomposition
	if err := configuration.DB.Where("request_id = ?", requestID).Order("version DESC").Find(&proposals).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Delivery decomposition unavailable", "Could not load decomposition proposals")
	}
	return utils.Success(c, http.StatusOK, "Delivery decompositions", proposals)
}

// CreateRequestDecomposition stores a reviewable task graph. It does not
// create work items, schedule inference or grant execution authority.
func CreateRequestDecomposition(c echo.Context) error {
	projectID, requestID, err := decompositionRouteIDs(c)
	if err != nil {
		return err
	}
	actor, err := projectActor(c, projectID, deliveryManage)
	if err != nil {
		return err
	}
	if err := requestInProject(requestID, projectID); err != nil {
		return lookup(c, "Delivery request", err)
	}
	var input decompositionRequest
	if err := c.Bind(&input); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid delivery decomposition", err.Error())
	}
	raw, err := json.Marshal(input.Structured)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid delivery decomposition", "structured proposal is invalid")
	}
	proposal, err := deliverydecomposition.Parse(raw)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid delivery decomposition", err.Error())
	}

	var created models.DeliveryDecomposition
	if err := configuration.DB.Transaction(func(tx *gorm.DB) error {
		digest, sources, err := decompositionContext(tx, projectID)
		if err != nil {
			return err
		}
		if err := verifyDecompositionReferences(proposal, sources); err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&models.DeliveryDecomposition{}).Where("request_id = ?", requestID).Count(&count).Error; err != nil {
			return err
		}
		created = models.DeliveryDecomposition{RequestID: requestID, Version: int(count) + 1, Status: "proposed", Summary: proposal.Summary, StructuredJSON: string(raw), ContextDigest: digest, ProposedBy: actor.CognitoSub}
		return tx.Create(&created).Error
	}); err != nil {
		return utils.Error(c, http.StatusConflict, "Delivery decomposition rejected", err.Error())
	}
	return utils.Success(c, http.StatusCreated, "Delivery decomposition proposed", created)
}

// ApplyRequestDecomposition is the request-level human gate. The transaction
// verifies the frozen context, creates every child and its snapshots, then
// installs dependency links together so a partial graph cannot escape.
func ApplyRequestDecomposition(c echo.Context) error {
	projectID, requestID, err := decompositionRouteIDs(c)
	if err != nil {
		return err
	}
	decompositionID, err := uuid.FromString(strings.TrimSpace(c.Param("decompositionID")))
	if err != nil || decompositionID == uuid.Nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid delivery decomposition", "decomposition id must be a UUID")
	}
	actor, err := projectActor(c, projectID, deliveryReview)
	if err != nil {
		return err
	}
	var input applyDecompositionRequest
	if err := c.Bind(&input); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid decomposition approval", err.Error())
	}
	comment := strings.TrimSpace(input.Comment)
	if len(comment) < 8 || len(comment) > 2000 {
		return utils.Error(c, http.StatusBadRequest, "Invalid decomposition approval", "an auditable approval comment of 8 to 2,000 characters is required")
	}

	var children []models.DeliveryWorkItem
	if err := configuration.DB.Transaction(func(tx *gorm.DB) error {
		var request models.DeliveryRequest
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND project_id = ?", requestID, projectID).First(&request).Error; err != nil {
			return err
		}
		var decomposition models.DeliveryDecomposition
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND request_id = ?", decompositionID, requestID).First(&decomposition).Error; err != nil {
			return err
		}
		if decomposition.Status != "proposed" {
			return fmt.Errorf("this decomposition has already been %s", decomposition.Status)
		}
		proposal, err := deliverydecomposition.Parse([]byte(decomposition.StructuredJSON))
		if err != nil {
			return fmt.Errorf("stored decomposition is no longer valid: %w", err)
		}
		digest, sources, err := decompositionContext(tx, projectID)
		if err != nil {
			return err
		}
		if digest != decomposition.ContextDigest {
			return fmt.Errorf("project context changed after this proposal; create a new decomposition")
		}
		if err := verifyDecompositionReferences(proposal, sources); err != nil {
			return err
		}
		keyToID := make(map[string]uuid.UUID, len(proposal.Tasks))
		now := time.Now().UTC()
		clientContext, err := snapshotClientContext(tx, projectID)
		if err != nil {
			return err
		}
		for _, task := range proposal.Tasks {
			included, _ := json.Marshal(task.IncludedScope)
			excluded, _ := json.Marshal(task.ExcludedScope)
			acceptance, _ := json.Marshal(task.AcceptanceCriteria)
			child := models.DeliveryWorkItem{ProjectID: projectID, RequestID: &requestID, RequestedBy: actor.CognitoSub, Title: task.Title, Description: task.Description, ExpectedOutcome: task.ExpectedOutcome, IncludedScopeJSON: string(included), ExcludedScopeJSON: string(excluded), AcceptanceJSON: string(acceptance), BudgetMicros: task.BudgetMicros, BudgetAlertPercent: defaultTaskBudgetAlertPercent, State: deliveryworkflow.StatePlanning}
			child.ClientContextJSON = clientContext
			if err := tx.Create(&child).Error; err != nil {
				return err
			}
			keyToID[task.Key] = child.ID
			for _, reference := range task.ContextReferences {
				source := sources[reference]
				snapshot := models.DeliveryContextSnapshot{WorkItemID: child.ID, SourceID: source.ID, Kind: source.Kind, Name: source.Name, Reference: source.Reference, Revision: source.Revision, MetadataJSON: source.MetadataJSON, CapturedAt: now}
				if err := tx.Create(&snapshot).Error; err != nil {
					return err
				}
			}
			children = append(children, child)
		}
		for _, task := range proposal.Tasks {
			for _, dependencyKey := range task.DependsOn {
				link := models.DeliveryWorkItemDependency{WorkItemID: keyToID[task.Key], DependsOnWorkItemID: keyToID[dependencyKey]}
				if err := tx.Create(&link).Error; err != nil {
					return err
				}
			}
		}
		if err := tx.Model(&decomposition).Updates(map[string]any{"status": "applied", "approved_by": actor.CognitoSub, "approval_comment": comment, "approved_at": now, "applied_at": now}).Error; err != nil {
			return err
		}
		return tx.Model(&request).Update("status", "planned").Error
	}); err != nil {
		return utils.Error(c, http.StatusConflict, "Delivery decomposition not applied", err.Error())
	}
	return utils.Success(c, http.StatusCreated, "Delivery work items created from approved decomposition", children)
}

func decompositionRouteIDs(c echo.Context) (uuid.UUID, uuid.UUID, error) {
	projectID, err := id(c, "project")
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	requestID, err := uuid.FromString(strings.TrimSpace(c.Param("requestID")))
	if err != nil || requestID == uuid.Nil {
		return uuid.Nil, uuid.Nil, utils.Error(c, http.StatusBadRequest, "Invalid delivery request", "request id must be a UUID")
	}
	return projectID, requestID, nil
}

func requestInProject(requestID, projectID uuid.UUID) error {
	return configuration.DB.Where("id = ? AND project_id = ?", requestID, projectID).First(&models.DeliveryRequest{}).Error
}

func decompositionContext(tx *gorm.DB, projectID uuid.UUID) (string, map[string]models.DeliveryContextSource, error) {
	var values []models.DeliveryContextSource
	if err := tx.Where("project_id = ? AND status = ?", projectID, "ready").Find(&values).Error; err != nil {
		return "", nil, err
	}
	if len(values) == 0 {
		return "", nil, fmt.Errorf("the project has no ready context sources")
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Reference < values[j].Reference })
	digestable := make([]string, 0, len(values))
	sources := make(map[string]models.DeliveryContextSource, len(values))
	for _, source := range values {
		reference := canonicalDeliveryRepositoryReference(source.Reference)
		if reference == "" {
			continue
		}
		if _, exists := sources[reference]; exists {
			return "", nil, fmt.Errorf("project context contains duplicate reference %s", reference)
		}
		source.Reference = reference
		sources[reference] = source
		digestable = append(digestable, source.ID.String()+"|"+source.Reference+"|"+source.Revision+"|"+source.MetadataJSON)
	}
	encoded, _ := json.Marshal(digestable)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), sources, nil
}

func verifyDecompositionReferences(proposal deliverydecomposition.Proposal, sources map[string]models.DeliveryContextSource) error {
	for _, task := range proposal.Tasks {
		for _, reference := range task.ContextReferences {
			reference = canonicalDeliveryRepositoryReference(reference)
			if _, exists := sources[reference]; !exists {
				return fmt.Errorf("task %s references unavailable project context %s", task.Key, reference)
			}
		}
	}
	return nil
}
