package models

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

// DeliveryRepositoryOnboarding is the reviewable, mutable workflow record for
// a static repository inspection. Repository content never becomes authority:
// the proposal remains pinned to one immutable Git SHA and needs a human
// approval before it can publish Delivery context or a Vault revision.
type DeliveryRepositoryOnboarding struct {
	ID                  uuid.UUID  `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	ProjectID           uuid.UUID  `gorm:"type:uuid;not null;index;uniqueIndex:idx_delivery_onboarding_checkpoint" json:"project_id"`
	RepositoryReference string     `gorm:"type:varchar(255);not null;index;uniqueIndex:idx_delivery_onboarding_checkpoint" json:"repository_reference"`
	DefaultBranch       string     `gorm:"type:varchar(255);not null" json:"default_branch"`
	Revision            string     `gorm:"type:varchar(40);not null;index;uniqueIndex:idx_delivery_onboarding_checkpoint" json:"revision"`
	Status              string     `gorm:"type:varchar(24);not null;default:'proposed';index" json:"status"`
	Readiness           string     `gorm:"type:varchar(24);not null;index" json:"readiness"`
	ProposalJSON        string     `gorm:"type:jsonb;not null" json:"-"`
	ProposalSHA256      string     `gorm:"type:varchar(64);not null" json:"proposal_sha256"`
	VaultSHA256         string     `gorm:"type:varchar(64);not null" json:"vault_sha256"`
	ProposedBy          string     `gorm:"type:varchar(128);not null;index" json:"proposed_by"`
	ApprovedBy          string     `gorm:"type:varchar(128);not null;default:'';index" json:"approved_by,omitempty"`
	ApprovedAt          *time.Time `gorm:"index" json:"approved_at,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

func (onboarding DeliveryRepositoryOnboarding) MarshalJSON() ([]byte, error) {
	type alias DeliveryRepositoryOnboarding
	proposal := jsonObjectOrEmpty(onboarding.ProposalJSON)
	capabilities := json.RawMessage(`[]`)
	var projection struct {
		Capabilities json.RawMessage `json:"capabilities"`
	}
	if json.Unmarshal(proposal, &projection) == nil && len(projection.Capabilities) > 0 {
		capabilities = jsonArrayOrEmpty(string(projection.Capabilities))
	}
	return json.Marshal(struct {
		alias
		Proposal         json.RawMessage `json:"proposal"`
		CapabilityMatrix json.RawMessage `json:"capability_matrix"`
	}{
		alias:            alias(onboarding),
		Proposal:         proposal,
		CapabilityMatrix: capabilities,
	})
}

// DeliveryProjectVaultRevision is an immutable, versioned repository-scoped
// Vault snapshot. The current project Vault is the latest revision for each
// configured repository plus future project-scoped revisions. Large generated
// indexes belong in object storage and may only be referenced by the manifest.
type DeliveryProjectVaultRevision struct {
	ID                  uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	ProjectID           uuid.UUID `gorm:"type:uuid;not null;index;uniqueIndex:idx_delivery_vault_version" json:"project_id"`
	RepositoryReference string    `gorm:"type:varchar(255);not null;index;uniqueIndex:idx_delivery_vault_version" json:"repository_reference"`
	Version             int64     `gorm:"not null;uniqueIndex:idx_delivery_vault_version" json:"version"`
	Revision            string    `gorm:"type:varchar(40);not null;index" json:"revision"`
	SchemaVersion       int       `gorm:"not null" json:"schema_version"`
	ManifestJSON        string    `gorm:"type:jsonb;not null" json:"-"`
	ContentSHA256       string    `gorm:"type:varchar(64);not null;index" json:"content_sha256"`
	SourceOnboardingID  uuid.UUID `gorm:"type:uuid;not null;uniqueIndex" json:"source_onboarding_id"`
	PublishedBy         string    `gorm:"type:varchar(128);not null;index" json:"published_by"`
	PublishedAt         time.Time `gorm:"not null;index" json:"published_at"`
	CreatedAt           time.Time `json:"created_at"`
}

func (revision DeliveryProjectVaultRevision) MarshalJSON() ([]byte, error) {
	type alias DeliveryProjectVaultRevision
	return json.Marshal(struct {
		alias
		Manifest json.RawMessage `json:"manifest"`
	}{alias: alias(revision), Manifest: jsonObjectOrEmpty(revision.ManifestJSON)})
}

var ErrImmutableVaultRevision = errors.New("project Vault revisions are append-only")

func (*DeliveryProjectVaultRevision) BeforeUpdate(*gorm.DB) error { return ErrImmutableVaultRevision }
func (*DeliveryProjectVaultRevision) BeforeDelete(*gorm.DB) error { return ErrImmutableVaultRevision }

func jsonObjectOrEmpty(raw string) json.RawMessage {
	var value map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &value) != nil || value == nil {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(raw)
}

func jsonArrayOrEmpty(raw string) json.RawMessage {
	var value []json.RawMessage
	if json.Unmarshal([]byte(raw), &value) != nil || value == nil {
		return json.RawMessage(`[]`)
	}
	return json.RawMessage(raw)
}
