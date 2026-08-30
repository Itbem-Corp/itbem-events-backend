package models

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

// deliveryMetadataObject makes JSONB metadata a stable API object. Storage
// intentionally remains a string so GORM can read and write jsonb without a
// custom database type, but clients must never need to parse JSON embedded in
// a JSON response. Malformed or non-object legacy data fails closed to an
// empty object rather than being exposed as an ambiguous string.
func deliveryMetadataObject(raw string) json.RawMessage {
	candidate := strings.TrimSpace(raw)
	if candidate == "" {
		return json.RawMessage(`{}`)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(candidate), &object); err != nil || object == nil {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(candidate)
}

// DeliveryClientProfile holds the ITBEM operational view of an existing
// organization: current delivery health, named contacts, working rules and a
// concise recent-conversation handoff. It is deliberately separate from the
// shared Client model so EventiApp data remains isolated from Delivery/AI.
type DeliveryClientProfile struct {
	ID                  uuid.UUID  `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	ClientID            uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex" json:"client_id"`
	Health              string     `gorm:"type:varchar(24);not null;default:'healthy';index" json:"health"`
	ContactsJSON        string     `gorm:"type:jsonb;not null;default:'[]'" json:"contacts,omitempty"`
	RulesJSON           string     `gorm:"type:jsonb;not null;default:'[]'" json:"rules,omitempty"`
	ConversationSummary string     `gorm:"type:text;not null;default:''" json:"conversation_summary,omitempty"`
	LastConversationAt  *time.Time `gorm:"index" json:"last_conversation_at,omitempty"`
	UpdatedBy           string     `gorm:"type:varchar(128);not null;default:''" json:"updated_by,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// DeliveryProject is ITBEM's private workspace for work performed for a
// client. It deliberately stores references to private sources rather than
// duplicating repository files, client conversations, or credentials.
type DeliveryProject struct {
	ID       uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	ClientID uuid.UUID `gorm:"type:uuid;not null;index" json:"client_id"`
	Name     string    `gorm:"type:varchar(180);not null" json:"name"`
	Slug     string    `gorm:"type:varchar(180);not null;uniqueIndex" json:"slug"`
	Summary  string    `gorm:"type:text;not null;default:''" json:"summary"`
	Status   string    `gorm:"type:varchar(24);not null;default:'active';index" json:"status"`
	// A zero monthly budget means the project is intentionally unmetered. A
	// positive value is enforced before a new non-deterministic agent run.
	MonthlyBudgetMicros int64                   `gorm:"not null;default:0" json:"monthly_budget_microusd"`
	BudgetAlertPercent  int                     `gorm:"not null;default:80" json:"budget_alert_percent"`
	CreatedBy           string                  `gorm:"type:varchar(128);not null;index" json:"created_by"`
	Client              Client                  `gorm:"foreignKey:ClientID" json:"client,omitempty" validate:"-"`
	Members             []DeliveryProjectMember `gorm:"foreignKey:ProjectID" json:"members,omitempty" validate:"-"`
	Context             []DeliveryContextSource `gorm:"foreignKey:ProjectID" json:"context,omitempty" validate:"-"`
	Requests            []DeliveryRequest       `gorm:"foreignKey:ProjectID" json:"requests,omitempty" validate:"-"`
	WorkItems           []DeliveryWorkItem      `gorm:"foreignKey:ProjectID" json:"work_items,omitempty" validate:"-"`
	Releases            []DeliveryRelease       `gorm:"foreignKey:ProjectID" json:"releases,omitempty" validate:"-"`
	CreatedAt           time.Time               `json:"created_at"`
	UpdatedAt           time.Time               `json:"updated_at"`
	DeletedAt           gorm.DeletedAt          `gorm:"index" json:"-"`
}

// DeliveryProjectMember grants a deliberate project role in addition to the
// platform-wide ITBEM policy. Roles are evaluated before a human may request,
// review, approve QA, or authorize a release for this project.
type DeliveryProjectMember struct {
	ID          uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	ProjectID   uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_delivery_project_member" json:"project_id"`
	CognitoSub  string    `gorm:"type:varchar(128);not null;uniqueIndex:idx_delivery_project_member" json:"cognito_sub"`
	Role        string    `gorm:"type:varchar(32);not null;index" json:"role"`
	Permissions string    `gorm:"type:jsonb;not null;default:'[]'" json:"permissions,omitempty"`
	CreatedBy   string    `gorm:"type:varchar(128);not null" json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// DeliveryContextSource describes a source the agent may use. Reference and
// revision are immutable once snapshotted onto a work item, so a later source
// sync cannot silently alter the context that a human approved.
type DeliveryContextSource struct {
	ID           uuid.UUID  `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	ProjectID    uuid.UUID  `gorm:"type:uuid;not null;index" json:"project_id"`
	Kind         string     `gorm:"type:varchar(40);not null;index" json:"kind"`
	Name         string     `gorm:"type:varchar(180);not null" json:"name"`
	Reference    string     `gorm:"type:text;not null" json:"reference"`
	Revision     string     `gorm:"type:varchar(255);not null;default:''" json:"revision"`
	Status       string     `gorm:"type:varchar(24);not null;default:'ready';index" json:"status"`
	MetadataJSON string     `gorm:"type:jsonb;not null;default:'{}'" json:"metadata,omitempty"`
	SyncedAt     *time.Time `gorm:"index" json:"synced_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// MarshalJSON preserves the database field while exposing metadata as a JSON
// object to every API client. This keeps the Delivery contract consistent for
// web, mobile, integrations and persisted evidence readers.
func (source DeliveryContextSource) MarshalJSON() ([]byte, error) {
	type alias DeliveryContextSource
	return json.Marshal(struct {
		alias
		Metadata json.RawMessage `json:"metadata"`
	}{
		alias:    alias(source),
		Metadata: deliveryMetadataObject(source.MetadataJSON),
	})
}

// DeliveryWorkItem is a bounded unit of work. The lifecycle is enforced by
// services/deliveryworkflow: agents cannot move an item through human gates.
type DeliveryWorkItem struct {
	ID                uuid.UUID  `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	ProjectID         uuid.UUID  `gorm:"type:uuid;not null;index" json:"project_id"`
	RequestID         *uuid.UUID `gorm:"type:uuid;index" json:"request_id,omitempty"`
	RequestedBy       string     `gorm:"type:varchar(128);not null;index" json:"requested_by"`
	AssignedAgent     string     `gorm:"type:varchar(128);not null;default:''" json:"assigned_agent,omitempty"`
	Title             string     `gorm:"type:varchar(240);not null" json:"title"`
	Description       string     `gorm:"type:text;not null;default:''" json:"description"`
	ExpectedOutcome   string     `gorm:"type:text;not null;default:''" json:"expected_outcome"`
	IncludedScopeJSON string     `gorm:"type:jsonb;not null;default:'[]'" json:"included_scope,omitempty"`
	ExcludedScopeJSON string     `gorm:"type:jsonb;not null;default:'[]'" json:"excluded_scope,omitempty"`
	AcceptanceJSON    string     `gorm:"type:jsonb;not null;default:'[]'" json:"acceptance_criteria,omitempty"`
	ClientContextJSON string     `gorm:"type:jsonb;not null;default:'{}'" json:"client_context,omitempty"`
	PlanJSON          string     `gorm:"type:jsonb;not null;default:'{}'" json:"plan,omitempty"`
	PullRequestURL    string     `gorm:"type:text;not null;default:''" json:"pull_request_url,omitempty"`
	PreviewURL        string     `gorm:"type:text;not null;default:''" json:"preview_url,omitempty"`
	// BudgetMicros is an optional hard ceiling for this bounded delivery task.
	// It complements the project monthly budget so one unexpectedly large plan
	// cannot consume the entire project allocation before a person intervenes.
	BudgetMicros       int64                        `gorm:"not null;default:0" json:"budget_microusd"`
	BudgetAlertPercent int                          `gorm:"not null;default:80" json:"budget_alert_percent"`
	State              string                       `gorm:"type:varchar(32);not null;default:'planning';index" json:"state"`
	BlockedReason      string                       `gorm:"type:text;not null;default:''" json:"blocked_reason,omitempty"`
	Project            DeliveryProject              `gorm:"foreignKey:ProjectID" json:"project,omitempty" validate:"-"`
	Request            *DeliveryRequest             `gorm:"foreignKey:RequestID" json:"request,omitempty" validate:"-"`
	ContextSnapshots   []DeliveryContextSnapshot    `gorm:"foreignKey:WorkItemID" json:"context_snapshots,omitempty" validate:"-"`
	Plans              []DeliveryPlan               `gorm:"foreignKey:WorkItemID" json:"plans,omitempty" validate:"-"`
	ChangeSets         []DeliveryChangeSet          `gorm:"foreignKey:WorkItemID" json:"change_sets,omitempty" validate:"-"`
	PublicationGrants  []DeliveryPublicationGrant   `gorm:"foreignKey:WorkItemID" json:"publication_grants,omitempty" validate:"-"`
	Dependencies       []DeliveryWorkItemDependency `gorm:"foreignKey:WorkItemID" json:"dependencies,omitempty" validate:"-"`
	Gates              []DeliveryGate               `gorm:"foreignKey:WorkItemID" json:"gates,omitempty" validate:"-"`
	Evidence           []DeliveryEvidence           `gorm:"foreignKey:WorkItemID" json:"evidence,omitempty" validate:"-"`
	Events             []DeliveryEvent              `gorm:"foreignKey:WorkItemID" json:"-" validate:"-"`
	Messages           []DeliveryMessage            `gorm:"foreignKey:WorkItemID" json:"messages,omitempty" validate:"-"`
	AutomationTasks    []AutomationTask             `gorm:"foreignKey:DeliveryWorkItemID" json:"automation_tasks,omitempty" validate:"-"`
	CreatedAt          time.Time                    `json:"created_at"`
	UpdatedAt          time.Time                    `json:"updated_at"`
	DeletedAt          gorm.DeletedAt               `gorm:"index" json:"-"`
}

// DeliveryWorkItemDependency records an explicit ordering constraint between
// two tasks in the same project. It is a relation rather than free text so
// the control plane can prevent a downstream task from entering review before
// the work it depends on has been released.
type DeliveryWorkItemDependency struct {
	ID                  uuid.UUID        `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	WorkItemID          uuid.UUID        `gorm:"type:uuid;not null;uniqueIndex:idx_delivery_work_item_dependency" json:"work_item_id"`
	DependsOnWorkItemID uuid.UUID        `gorm:"type:uuid;not null;uniqueIndex:idx_delivery_work_item_dependency;index" json:"depends_on_work_item_id"`
	DependsOn           DeliveryWorkItem `gorm:"foreignKey:DependsOnWorkItemID" json:"depends_on,omitempty" validate:"-"`
	CreatedAt           time.Time        `json:"created_at"`
}

// DeliveryRequest records what a human asked for before an agent proposes a
// plan. It is intentionally separate from a work item: one request may be
// refined, split into tasks, or cancelled without rewriting its history.
type DeliveryRequest struct {
	ID              uuid.UUID               `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	ProjectID       uuid.UUID               `gorm:"type:uuid;not null;index" json:"project_id"`
	RequestedBy     string                  `gorm:"type:varchar(128);not null;index" json:"requested_by"`
	Title           string                  `gorm:"type:varchar(240);not null" json:"title"`
	Body            string                  `gorm:"type:text;not null;default:''" json:"body"`
	Priority        string                  `gorm:"type:varchar(24);not null;default:'normal';index" json:"priority"`
	ConstraintsJSON string                  `gorm:"type:jsonb;not null;default:'[]'" json:"constraints,omitempty"`
	ExpectedOutcome string                  `gorm:"type:text;not null;default:''" json:"expected_outcome"`
	Status          string                  `gorm:"type:varchar(32);not null;default:'open';index" json:"status"`
	Project         DeliveryProject         `gorm:"foreignKey:ProjectID" json:"project,omitempty" validate:"-"`
	WorkItems       []DeliveryWorkItem      `gorm:"foreignKey:RequestID" json:"work_items,omitempty" validate:"-"`
	Decompositions  []DeliveryDecomposition `gorm:"foreignKey:RequestID" json:"decompositions,omitempty" validate:"-"`
	CreatedAt       time.Time               `json:"created_at"`
	UpdatedAt       time.Time               `json:"updated_at"`
	DeletedAt       gorm.DeletedAt          `gorm:"index" json:"-"`
}

// DeliveryDecomposition is an append-only proposal for turning one human
// request into several bounded work items. It is intentionally separate from
// DeliveryPlan: approving a decomposition authorizes only the creation of
// planning tasks, never implementation or a later delivery gate.
type DeliveryDecomposition struct {
	ID              uuid.UUID       `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	RequestID       uuid.UUID       `gorm:"type:uuid;not null;index;uniqueIndex:idx_delivery_decomposition_version" json:"request_id"`
	Version         int             `gorm:"not null;uniqueIndex:idx_delivery_decomposition_version" json:"version"`
	Status          string          `gorm:"type:varchar(24);not null;default:'proposed';index" json:"status"`
	Summary         string          `gorm:"type:text;not null;default:''" json:"summary"`
	StructuredJSON  string          `gorm:"type:jsonb;not null;default:'{}'" json:"structured_result,omitempty"`
	ContextDigest   string          `gorm:"type:varchar(128);not null;default:''" json:"context_digest,omitempty"`
	ProposedBy      string          `gorm:"type:varchar(128);not null;default:''" json:"proposed_by,omitempty"`
	ApprovedBy      string          `gorm:"type:varchar(128);not null;default:''" json:"approved_by,omitempty"`
	ApprovalComment string          `gorm:"type:text;not null;default:''" json:"approval_comment,omitempty"`
	ApprovedAt      *time.Time      `json:"approved_at,omitempty"`
	AppliedAt       *time.Time      `json:"applied_at,omitempty"`
	Request         DeliveryRequest `gorm:"foreignKey:RequestID" json:"-" validate:"-"`
	CreatedAt       time.Time       `json:"created_at"`
}

// DeliveryPlan is append-only and versioned. The agent proposes structured
// content; a human gate authorizes which revision may enter implementation.
type DeliveryPlan struct {
	ID             uuid.UUID  `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	WorkItemID     uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_delivery_plan_version" json:"work_item_id"`
	Version        int        `gorm:"not null;uniqueIndex:idx_delivery_plan_version" json:"version"`
	Status         string     `gorm:"type:varchar(32);not null;default:'proposed';index" json:"status"`
	Summary        string     `gorm:"type:text;not null;default:''" json:"summary"`
	StructuredJSON string     `gorm:"type:jsonb;not null;default:'{}'" json:"structured_result,omitempty"`
	ContextDigest  string     `gorm:"type:varchar(128);not null;default:''" json:"context_digest,omitempty"`
	ProposedBy     string     `gorm:"type:varchar(128);not null;default:''" json:"proposed_by,omitempty"`
	ApprovedGateID *uuid.UUID `gorm:"type:uuid;index" json:"approved_gate_id,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

// DeliveryChangeSet makes the repository change, review reference, CI and
// preview a traceable record instead of unrelated text fields on a task. A
// local worktree is a first-class review target during local-only delivery;
// it is never represented as a fictional pull request.
type DeliveryChangeSet struct {
	ID             uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	WorkItemID     uuid.UUID `gorm:"type:uuid;not null;index" json:"work_item_id"`
	RepositoryRef  string    `gorm:"type:text;not null;default:''" json:"repository_ref"`
	Branch         string    `gorm:"type:varchar(255);not null;default:''" json:"branch"`
	CommitSHA      string    `gorm:"type:varchar(128);not null;default:''" json:"commit_sha"`
	ReviewType     string    `gorm:"type:varchar(32);not null;default:'pull_request'" json:"review_type"`
	PullRequestURL string    `gorm:"type:text;not null;default:''" json:"pull_request_url"`
	CIStatus       string    `gorm:"type:varchar(32);not null;default:'pending';index" json:"ci_status"`
	CIURL          string    `gorm:"type:text;not null;default:''" json:"ci_url"`
	PreviewURL     string    `gorm:"type:text;not null;default:''" json:"preview_url"`
	Environment    string    `gorm:"type:varchar(32);not null;default:'preview'" json:"environment"`
	MetadataJSON   string    `gorm:"type:jsonb;not null;default:'{}'" json:"metadata,omitempty"`
	CreatedBy      string    `gorm:"type:varchar(128);not null;default:''" json:"created_by,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (changeSet DeliveryChangeSet) MarshalJSON() ([]byte, error) {
	type alias DeliveryChangeSet
	return json.Marshal(struct {
		alias
		Metadata json.RawMessage `json:"metadata"`
	}{
		alias:    alias(changeSet),
		Metadata: deliveryMetadataObject(changeSet.MetadataJSON),
	})
}

// DeliveryPublicationGrant is a short-lived, human-issued authorization to
// publish one reviewed worktree branch. It deliberately stores no credential:
// a future GitHub App integration must mint its own installation token at the
// moment of use and still satisfy this immutable scope.
type DeliveryPublicationGrant struct {
	ID               uuid.UUID  `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	WorkItemID       uuid.UUID  `gorm:"type:uuid;not null;index" json:"work_item_id"`
	RepositoryRef    string     `gorm:"type:text;not null" json:"repository_ref"`
	BaseSHA          string     `gorm:"type:varchar(128);not null" json:"base_sha"`
	GitHubRepository string     `gorm:"type:varchar(255);not null;default:''" json:"github_repository"`
	ReviewDiffSHA256 string     `gorm:"type:varchar(64);not null;default:''" json:"review_diff_sha256"`
	Branch           string     `gorm:"type:varchar(255);not null" json:"branch"`
	CapabilitiesJSON string     `gorm:"type:jsonb;not null;default:'[]'" json:"capabilities,omitempty"`
	Reason           string     `gorm:"type:text;not null;default:''" json:"reason"`
	GrantedBy        string     `gorm:"type:varchar(128);not null;index" json:"granted_by"`
	GrantedAt        time.Time  `gorm:"not null;index" json:"granted_at"`
	ExpiresAt        time.Time  `gorm:"not null;index" json:"expires_at"`
	RevokedBy        string     `gorm:"type:varchar(128);not null;default:''" json:"revoked_by,omitempty"`
	RevokedAt        *time.Time `gorm:"index" json:"revoked_at,omitempty"`
	RevocationReason string     `gorm:"type:text;not null;default:''" json:"revocation_reason,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// DeliveryRelease is the final human-facing delivery record. It references
// private reports/evidence while keeping its readable executive summary here.
type DeliveryRelease struct {
	ID            uuid.UUID  `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	ProjectID     uuid.UUID  `gorm:"type:uuid;not null;index" json:"project_id"`
	WorkItemID    uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex" json:"work_item_id"`
	Status        string     `gorm:"type:varchar(32);not null;default:'draft';index" json:"status"`
	ExecutiveJSON string     `gorm:"type:jsonb;not null;default:'{}'" json:"executive_summary,omitempty"`
	TechnicalJSON string     `gorm:"type:jsonb;not null;default:'{}'" json:"technical_summary,omitempty"`
	ReportRef     string     `gorm:"type:text;not null;default:''" json:"report_ref,omitempty"`
	ReleasedBy    string     `gorm:"type:varchar(128);not null;default:''" json:"released_by,omitempty"`
	ReleasedAt    *time.Time `gorm:"index" json:"released_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// DeliveryContextSnapshot freezes the exact source revision used to plan or
// execute a work item. The original source may change after this row exists.
type DeliveryContextSnapshot struct {
	ID           uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	WorkItemID   uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_delivery_snapshot_source" json:"work_item_id"`
	SourceID     uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_delivery_snapshot_source" json:"source_id"`
	Kind         string    `gorm:"type:varchar(40);not null;default:''" json:"kind"`
	Name         string    `gorm:"type:varchar(180);not null;default:''" json:"name"`
	Reference    string    `gorm:"type:text;not null" json:"reference"`
	Revision     string    `gorm:"type:varchar(255);not null;default:''" json:"revision"`
	MetadataJSON string    `gorm:"type:jsonb;not null;default:'{}'" json:"metadata,omitempty"`
	CapturedAt   time.Time `gorm:"not null;index" json:"captured_at"`
	CreatedAt    time.Time `json:"created_at"`
}

func (snapshot DeliveryContextSnapshot) MarshalJSON() ([]byte, error) {
	type alias DeliveryContextSnapshot
	return json.Marshal(struct {
		alias
		Metadata json.RawMessage `json:"metadata"`
	}{
		alias:    alias(snapshot),
		Metadata: deliveryMetadataObject(snapshot.MetadataJSON),
	})
}

// DeliveryGate records the human decision that authorizes a sensitive
// transition. It is append-only in the domain: a requested change is a new
// decision, never an overwritten approval.
type DeliveryGate struct {
	ID                uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	WorkItemID        uuid.UUID `gorm:"type:uuid;not null;index" json:"work_item_id"`
	Kind              string    `gorm:"type:varchar(32);not null;index" json:"kind"`
	Decision          string    `gorm:"type:varchar(32);not null;index" json:"decision"`
	DecidedBy         string    `gorm:"type:varchar(128);not null;index" json:"decided_by"`
	Comment           string    `gorm:"type:text;not null;default:''" json:"comment,omitempty"`
	EvidenceChecklist string    `gorm:"type:jsonb;not null;default:'[]'" json:"evidence_checklist,omitempty"`
	DecidedAt         time.Time `gorm:"not null;index" json:"decided_at"`
	CreatedAt         time.Time `json:"created_at"`
}

// DeliveryEvidence is an immutable reference to a screenshot, report, test
// result, diff or other artifact. Large data stays in private object storage.
type DeliveryEvidence struct {
	ID           uuid.UUID  `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	WorkItemID   uuid.UUID  `gorm:"type:uuid;not null;index" json:"work_item_id"`
	Kind         string     `gorm:"type:varchar(32);not null;index" json:"kind"`
	Phase        string     `gorm:"type:varchar(32);not null;index" json:"phase"`
	Title        string     `gorm:"type:varchar(240);not null" json:"title"`
	Reference    string     `gorm:"type:text;not null" json:"reference"`
	MetadataJSON string     `gorm:"type:jsonb;not null;default:'{}'" json:"metadata,omitempty"`
	CapturedBy   string     `gorm:"type:varchar(128);not null;default:''" json:"captured_by,omitempty"`
	CapturedAt   *time.Time `gorm:"index" json:"captured_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// DeliveryEvent is the append-only, sequence-bearing event ledger for one
// work item. Payload is private control-plane evidence; browser read models
// must expose a deliberately smaller projection instead of this row.
type DeliveryEvent struct {
	ID            uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"-"`
	WorkItemID    uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_delivery_event_sequence;index" json:"-"`
	Sequence      int64     `gorm:"not null;uniqueIndex:idx_delivery_event_sequence" json:"sequence"`
	EventType     string    `gorm:"type:varchar(96);not null;index" json:"event_type"`
	DedupeKey     string    `gorm:"type:varchar(256);not null;uniqueIndex" json:"-"`
	SubjectDigest string    `gorm:"type:varchar(64);not null;default:'';index" json:"subject_digest,omitempty"`
	PayloadJSON   string    `gorm:"type:jsonb;not null" json:"-"`
	PayloadDigest string    `gorm:"type:varchar(64);not null;index" json:"payload_digest"`
	ActorType     string    `gorm:"type:varchar(24);not null;index" json:"actor_type"`
	ActorID       string    `gorm:"type:varchar(128);not null;index" json:"-"`
	OccurredAt    time.Time `gorm:"not null;index" json:"occurred_at"`
	CreatedAt     time.Time `gorm:"not null;index" json:"created_at"`
}

func (evidence DeliveryEvidence) MarshalJSON() ([]byte, error) {
	type alias DeliveryEvidence
	return json.Marshal(struct {
		alias
		Metadata json.RawMessage `json:"metadata"`
	}{
		alias:    alias(evidence),
		Metadata: deliveryMetadataObject(evidence.MetadataJSON),
	})
}

// DeliveryMessage keeps the human-agent conversation attached to a specific
// work item and phase instead of losing change requests in a generic chat.
type DeliveryMessage struct {
	ID         uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	WorkItemID uuid.UUID `gorm:"type:uuid;not null;index" json:"work_item_id"`
	Phase      string    `gorm:"type:varchar(32);not null;index" json:"phase"`
	AuthorType string    `gorm:"type:varchar(16);not null" json:"author_type"`
	AuthorID   string    `gorm:"type:varchar(128);not null;default:''" json:"author_id,omitempty"`
	Body       string    `gorm:"type:text;not null" json:"body"`
	CreatedAt  time.Time `json:"created_at"`
}
