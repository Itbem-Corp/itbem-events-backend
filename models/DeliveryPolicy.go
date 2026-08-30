package models

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

// DeliveryPolicyRevision is immutable policy content proposed for one exact
// hierarchy scope. It has no authority until an append-only decision activates
// its exact content digest. Repository/Vault text is data inside PatchJSON and
// never an instruction or capability by itself.
type DeliveryPolicyRevision struct {
	ID                  uuid.UUID                `gorm:"type:uuid;default:uuid_generate_v4();primaryKey;uniqueIndex:idx_delivery_policy_revision_digest" json:"id"`
	SchemaVersion       int                      `gorm:"not null;check:chk_delivery_policy_schema,schema_version > 0" json:"schema_version"`
	Level               string                   `gorm:"type:varchar(24);not null;index;check:chk_delivery_policy_level,level IN ('platform','organization','project','repository','override')" json:"level"`
	OrganizationID      string                   `gorm:"type:varchar(128);not null;default:'';index;check:chk_delivery_policy_scope,(level = 'platform' AND organization_id = '' AND project_id IS NULL AND repository_reference = '' AND change_set_id = '' AND expires_at IS NULL) OR (level = 'organization' AND organization_id <> '' AND project_id IS NULL AND repository_reference = '' AND change_set_id = '' AND expires_at IS NULL) OR (level = 'project' AND organization_id <> '' AND project_id IS NOT NULL AND repository_reference = '' AND change_set_id = '' AND expires_at IS NULL) OR (level = 'repository' AND organization_id <> '' AND project_id IS NOT NULL AND repository_reference <> '' AND change_set_id = '' AND expires_at IS NULL) OR (level = 'override' AND organization_id <> '' AND project_id IS NOT NULL AND change_set_id <> '' AND reason <> '' AND expires_at IS NOT NULL)" json:"organization_id,omitempty"`
	ProjectID           *uuid.UUID               `gorm:"type:uuid;index" json:"project_id,omitempty"`
	RepositoryReference string                   `gorm:"type:varchar(255);not null;default:'';index" json:"repository_reference,omitempty"`
	ChangeSetID         string                   `gorm:"type:varchar(128);not null;default:'';index" json:"change_set_id,omitempty"`
	PatchJSON           string                   `gorm:"type:jsonb;not null;check:chk_delivery_policy_patch,jsonb_typeof(patch_json) = 'object'" json:"-"`
	Reason              string                   `gorm:"type:text;not null;default:''" json:"reason,omitempty"`
	ExpiresAt           *time.Time               `gorm:"index" json:"expires_at,omitempty"`
	ContentSHA256       string                   `gorm:"type:varchar(64);not null;uniqueIndex;uniqueIndex:idx_delivery_policy_revision_digest;check:chk_delivery_policy_revision_digest,content_sha256 ~ '^[a-f0-9]{64}$'" json:"content_sha256"`
	ProposedBy          string                   `gorm:"type:varchar(128);not null;index;check:chk_delivery_policy_proposer,char_length(proposed_by) > 0" json:"-"`
	Decisions           []DeliveryPolicyDecision `gorm:"foreignKey:PolicyRevisionID,PolicyDigest;references:ID,ContentSHA256" json:"decisions,omitempty" validate:"-"`
	Project             *DeliveryProject         `gorm:"foreignKey:ProjectID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT" json:"-" validate:"-"`
	CreatedAt           time.Time                `gorm:"not null;index" json:"created_at"`
}

func (revision DeliveryPolicyRevision) MarshalJSON() ([]byte, error) {
	type alias DeliveryPolicyRevision
	return json.Marshal(struct {
		alias
		Patch json.RawMessage `json:"patch"`
	}{alias: alias(revision), Patch: jsonObjectOrEmpty(revision.PatchJSON)})
}

// DeliveryPolicyDecision is a human activation/revocation event for the exact
// immutable revision and digest. Effective-policy selection is based on these
// events, never on mutable status columns.
type DeliveryPolicyDecision struct {
	ID               uuid.UUID              `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	PolicyRevisionID uuid.UUID              `gorm:"type:uuid;not null;index" json:"policy_revision_id"`
	PolicyDigest     string                 `gorm:"type:varchar(64);not null;index;check:chk_delivery_policy_decision_digest,policy_digest ~ '^[a-f0-9]{64}$'" json:"policy_digest"`
	Action           string                 `gorm:"type:varchar(16);not null;index;check:chk_delivery_policy_decision_action,action IN ('approved','revoked')" json:"action"`
	Reason           string                 `gorm:"type:text;not null;default:''" json:"reason,omitempty"`
	ActorCognitoSub  string                 `gorm:"type:varchar(128);not null;index;check:chk_delivery_policy_actor,char_length(actor_cognito_sub) > 0" json:"-"`
	OccurredAt       time.Time              `gorm:"not null;index" json:"occurred_at"`
	PolicyRevision   DeliveryPolicyRevision `gorm:"foreignKey:PolicyRevisionID,PolicyDigest;references:ID,ContentSHA256;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT" json:"-" validate:"-"`
}

var ErrImmutableDeliveryPolicy = errors.New("delivery policy ledgers are append-only")

func (*DeliveryPolicyRevision) BeforeUpdate(*gorm.DB) error { return ErrImmutableDeliveryPolicy }
func (*DeliveryPolicyRevision) BeforeDelete(*gorm.DB) error { return ErrImmutableDeliveryPolicy }
func (*DeliveryPolicyDecision) BeforeUpdate(*gorm.DB) error { return ErrImmutableDeliveryPolicy }
func (*DeliveryPolicyDecision) BeforeDelete(*gorm.DB) error { return ErrImmutableDeliveryPolicy }
