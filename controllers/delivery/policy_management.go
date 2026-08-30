package delivery

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/deliverypolicy"
	"events-stocks/internal/deliverypolicystore"
	"events-stocks/internal/projectvault"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	policyManagementBodyLimit     = 64 << 10
	policyManagementRevisionLimit = 200
)

type projectPolicyRevisionRequest struct {
	Level       string          `json:"level"`
	Repository  string          `json:"repository,omitempty"`
	ChangeSetID string          `json:"change_set_id,omitempty"`
	Patch       json.RawMessage `json:"patch"`
	Reason      string          `json:"reason,omitempty"`
	ExpiresAt   *time.Time      `json:"expires_at,omitempty"`
}

type projectPolicyDecisionRequest struct {
	Action         string `json:"action"`
	ExpectedDigest string `json:"expected_digest"`
	Reason         string `json:"reason,omitempty"`
}

type projectPolicyDecisionProjection struct {
	ID         uuid.UUID `json:"id"`
	Action     string    `json:"action"`
	Reason     string    `json:"reason,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
}

type projectPolicyRevisionProjection struct {
	ID                  uuid.UUID                        `json:"id"`
	SchemaVersion       int                              `json:"schema_version"`
	Level               string                           `json:"level"`
	ProjectID           uuid.UUID                        `json:"project_id"`
	RepositoryReference string                           `json:"repository,omitempty"`
	ChangeSetID         string                           `json:"change_set_id,omitempty"`
	Patch               json.RawMessage                  `json:"patch"`
	Reason              string                           `json:"reason,omitempty"`
	ExpiresAt           *time.Time                       `json:"expires_at,omitempty"`
	ContentSHA256       string                           `json:"content_sha256"`
	CreatedAt           time.Time                        `json:"created_at"`
	Status              string                           `json:"status"`
	LatestDecision      *projectPolicyDecisionProjection `json:"latest_decision,omitempty"`
}

// ListProjectPolicyRevisions returns a bounded safe projection. Authentication
// subjects are intentionally absent; the endpoint is configuration evidence,
// not a source of merge or release authority.
func ListProjectPolicyRevisions(c echo.Context) error {
	projectID, err := id(c, "project")
	if err != nil {
		return err
	}
	if _, err := projectActor(c, projectID, deliveryView); err != nil {
		return err
	}
	project, err := policyProject(projectID)
	if err != nil {
		return lookup(c, "Delivery project", err)
	}
	var revisions []models.DeliveryPolicyRevision
	if err := configuration.DB.Where("organization_id = ? AND project_id = ? AND level IN ?", project.ClientID.String(), projectID, []string{"project", "repository", "override"}).
		Order("created_at DESC, id DESC").Limit(policyManagementRevisionLimit + 1).Find(&revisions).Error; err != nil {
		return utilsError(c, err)
	}
	if len(revisions) > policyManagementRevisionLimit {
		return conflict(c, "Delivery policy history is too large", "Use an archived read model before listing more policy revisions")
	}
	decisions, err := latestPolicyDecisions(revisions)
	if err != nil {
		return utilsError(c, err)
	}
	values := make([]projectPolicyRevisionProjection, 0, len(revisions))
	now := time.Now().UTC()
	for _, revision := range revisions {
		decision, present := decisions[revision.ID]
		if err := deliverypolicystore.ValidateRevision(revision, revision.CreatedAt.UTC()); err != nil {
			return conflict(c, "Delivery policy evidence failed integrity checks", "Policy history cannot be projected safely")
		}
		if present {
			if err := deliverypolicystore.ValidateDecision(revision, decision, now); err != nil {
				return conflict(c, "Delivery policy evidence failed integrity checks", "Policy history cannot be projected safely")
			}
		}
		values = append(values, projectPolicyRevisionView(revision, decision, present))
	}
	return success(c, "Delivery policy revisions", values)
}

// CreateProjectPolicyRevision appends a proposal at project, repository, or
// exact override scope. It never creates an approval or performs an action.
func CreateProjectPolicyRevision(c echo.Context) error {
	projectID, err := id(c, "project")
	if err != nil {
		return err
	}
	actor, err := projectActor(c, projectID, deliveryManage)
	if err != nil {
		return err
	}
	project, err := policyProject(projectID)
	if err != nil {
		return lookup(c, "Delivery project", err)
	}
	var request projectPolicyRevisionRequest
	if err := decodePolicyManagementRequest(c, &request); err != nil {
		return badRequest(c, "Invalid delivery policy proposal", err.Error())
	}
	revisionID, err := uuid.NewV4()
	if err != nil {
		return utilsError(c, err)
	}
	now := time.Now().UTC()
	revision, err := deliverypolicystore.BuildProjectRevision(project.ClientID.String(), projectID, actor.CognitoSub, deliverypolicystore.ProjectRevisionInput{
		Level: deliverypolicy.Level(strings.TrimSpace(request.Level)), Repository: request.Repository,
		ChangeSetID: request.ChangeSetID, Patch: request.Patch, Reason: request.Reason, ExpiresAt: request.ExpiresAt,
	}, revisionID, now)
	if err != nil {
		return badRequest(c, "Invalid delivery policy proposal", err.Error())
	}
	err = configuration.DB.Transaction(func(tx *gorm.DB) error {
		var lockedProject models.DeliveryProject
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("id = ? AND client_id = ?", projectID, project.ClientID).First(&lockedProject).Error; err != nil {
			return err
		}
		if err := requireApprovedVaultScope(tx, projectID, revision.RepositoryReference); err != nil {
			return policyProposalVaultMissing{}
		}
		return tx.Create(&revision).Error
	})
	if _, blocked := err.(policyProposalVaultMissing); blocked {
		return conflict(c, "Delivery policy proposal blocked", "Complete and approve repository onboarding before configuring its policy")
	}
	if err != nil {
		return utilsError(c, err)
	}
	return created(c, "Delivery policy revision proposed", projectPolicyRevisionView(revision, models.DeliveryPolicyDecision{}, false))
}

// DecideProjectPolicyRevision appends an independent human approval or a
// revocation for one exact revision digest. Repeated same-state requests are
// idempotent under a row lock and never duplicate authority records.
func DecideProjectPolicyRevision(c echo.Context) error {
	projectID, err := id(c, "project")
	if err != nil {
		return err
	}
	actor, err := projectActor(c, projectID, deliveryReview)
	if err != nil {
		return err
	}
	revisionID, err := uuid.FromString(strings.TrimSpace(c.Param("revisionID")))
	if err != nil || revisionID == uuid.Nil {
		return badRequest(c, "Invalid delivery policy decision", "revision ID must be a UUID")
	}
	var request projectPolicyDecisionRequest
	if err := decodePolicyManagementRequest(c, &request); err != nil {
		return badRequest(c, "Invalid delivery policy decision", err.Error())
	}
	action := strings.ToLower(strings.TrimSpace(request.Action))
	expectedDigest := strings.ToLower(strings.TrimSpace(request.ExpectedDigest))
	reason := strings.TrimSpace(request.Reason)
	if (action != "approved" && action != "revoked") || !deliveryArtifactDigestPattern.MatchString(expectedDigest) {
		return badRequest(c, "Invalid delivery policy decision", "action and expected_digest are required")
	}
	if action == "revoked" && reason == "" {
		return badRequest(c, "Invalid delivery policy decision", "a revocation reason is required")
	}

	var revision models.DeliveryPolicyRevision
	var decision models.DeliveryPolicyDecision
	createdDecision := false
	now := time.Now().UTC()
	err = configuration.DB.Transaction(func(tx *gorm.DB) error {
		var project models.DeliveryProject
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "client_id").First(&project, projectID).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND project_id = ? AND organization_id = ?", revisionID, projectID, project.ClientID.String()).First(&revision).Error; err != nil {
			return err
		}
		if revision.ContentSHA256 != expectedDigest {
			return errPolicyDecisionRejected("expected digest does not match the immutable proposal")
		}
		if action == "approved" && strings.TrimSpace(revision.ProposedBy) == strings.TrimSpace(actor.CognitoSub) {
			return errPolicyDecisionRejected("the proposer cannot approve the same policy revision")
		}
		if err := requireApprovedVaultScope(tx, projectID, revision.RepositoryReference); err != nil {
			return errPolicyDecisionRejected("the policy scope is not present in the approved Vault")
		}
		if err := deliverypolicystore.ValidateRevision(revision, now); err != nil {
			return errPolicyDecisionRejected("the immutable policy revision failed integrity checks")
		}

		var latest models.DeliveryPolicyDecision
		latestErr := tx.Where("policy_revision_id = ?", revision.ID).Order("occurred_at DESC, id DESC").First(&latest).Error
		if latestErr == nil {
			if err := deliverypolicystore.ValidateDecision(revision, latest, now); err != nil {
				return errPolicyDecisionRejected("the latest policy decision failed integrity checks")
			}
			if latest.PolicyDigest == expectedDigest && latest.Action == action {
				decision = latest
				return nil
			}
		}
		if latestErr != nil && latestErr != gorm.ErrRecordNotFound {
			return latestErr
		}
		decisionID, err := uuid.NewV4()
		if err != nil {
			return err
		}
		decision = models.DeliveryPolicyDecision{
			ID: decisionID, PolicyRevisionID: revision.ID, PolicyDigest: revision.ContentSHA256,
			Action: action, Reason: reason, ActorCognitoSub: actor.CognitoSub, OccurredAt: now,
		}
		if err := deliverypolicystore.ValidateDecision(revision, decision, now); err != nil {
			return errPolicyDecisionRejected(err.Error())
		}
		if err := tx.Create(&decision).Error; err != nil {
			return err
		}
		createdDecision = true
		return nil
	})
	if rejected, ok := err.(policyDecisionRejection); ok {
		return conflict(c, "Delivery policy decision rejected", rejected.Error())
	}
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return lookup(c, "Delivery policy revision", err)
		}
		return utilsError(c, err)
	}
	message := "Delivery policy decision already recorded"
	if createdDecision {
		message = "Delivery policy decision recorded"
	}
	return success(c, message, projectPolicyRevisionView(revision, decision, true))
}

type policyDecisionRejection string

type policyProposalVaultMissing struct{}

func (policyProposalVaultMissing) Error() string { return "approved Vault scope is missing" }

func (err policyDecisionRejection) Error() string   { return string(err) }
func errPolicyDecisionRejected(reason string) error { return policyDecisionRejection(reason) }

func policyProject(projectID uuid.UUID) (models.DeliveryProject, error) {
	return policyProjectWithDB(configuration.DB, projectID)
}

func policyProjectWithDB(db *gorm.DB, projectID uuid.UUID) (models.DeliveryProject, error) {
	var project models.DeliveryProject
	err := db.Select("id", "client_id").First(&project, projectID).Error
	return project, err
}

func requireApprovedVaultScope(db *gorm.DB, projectID uuid.UUID, repository string) error {
	query := db.Select("id").Where("project_id = ?", projectID)
	if repository != "" {
		canonical, err := projectvault.CanonicalGitHubReference(repository)
		if err != nil {
			return err
		}
		query = query.Where("repository_reference = ?", canonical)
	}
	var vault models.DeliveryProjectVaultRevision
	return query.First(&vault).Error
}

func latestPolicyDecisions(revisions []models.DeliveryPolicyRevision) (map[uuid.UUID]models.DeliveryPolicyDecision, error) {
	result := make(map[uuid.UUID]models.DeliveryPolicyDecision)
	if len(revisions) == 0 {
		return result, nil
	}
	ids := make([]uuid.UUID, 0, len(revisions))
	for _, revision := range revisions {
		ids = append(ids, revision.ID)
	}
	var decisions []models.DeliveryPolicyDecision
	if err := configuration.DB.Select("DISTINCT ON (policy_revision_id) delivery_policy_decisions.*").Where("policy_revision_id IN ?", ids).
		Order("policy_revision_id, occurred_at DESC, id DESC").Find(&decisions).Error; err != nil {
		return nil, err
	}
	for _, decision := range decisions {
		result[decision.PolicyRevisionID] = decision
	}
	return result, nil
}

func projectPolicyRevisionView(revision models.DeliveryPolicyRevision, decision models.DeliveryPolicyDecision, hasDecision bool) projectPolicyRevisionProjection {
	patch := json.RawMessage(revision.PatchJSON)
	trimmedPatch := bytes.TrimSpace(patch)
	if !json.Valid(patch) || len(trimmedPatch) == 0 || trimmedPatch[0] != '{' {
		patch = json.RawMessage(`{}`)
	}
	projectID := uuid.Nil
	if revision.ProjectID != nil {
		projectID = *revision.ProjectID
	}
	view := projectPolicyRevisionProjection{
		ID: revision.ID, SchemaVersion: revision.SchemaVersion, Level: revision.Level, ProjectID: projectID,
		RepositoryReference: revision.RepositoryReference, ChangeSetID: revision.ChangeSetID, Patch: patch,
		Reason: revision.Reason, ExpiresAt: revision.ExpiresAt, ContentSHA256: revision.ContentSHA256,
		CreatedAt: revision.CreatedAt.UTC(), Status: "pending",
	}
	if hasDecision {
		view.Status = decision.Action
		view.LatestDecision = &projectPolicyDecisionProjection{ID: decision.ID, Action: decision.Action, Reason: decision.Reason, OccurredAt: decision.OccurredAt.UTC()}
	}
	return view
}

func decodePolicyManagementRequest(c echo.Context, target any) error {
	raw, err := io.ReadAll(io.LimitReader(c.Request().Body, policyManagementBodyLimit+1))
	if err != nil || len(raw) == 0 {
		return fmt.Errorf("request body is required")
	}
	if len(raw) > policyManagementBodyLimit {
		return fmt.Errorf("request body exceeds 64 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("request body must match the policy schema")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("request body must contain one JSON object")
	}
	return nil
}
