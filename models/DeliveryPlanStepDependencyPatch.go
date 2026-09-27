package models

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/uuid"
)

const (
	MaxDeliveryPlanStepDependencyPatchReferences = 64
	MaxDeliveryPlanStepDependencyPatchBytes      = 32 * 1024 * 1024
)

// DeliveryPlanStepDependencyPatchReference is safe manifest metadata for one
// patch produced by a direct, completed dependency. It deliberately contains
// no object bucket/key or patch bytes.
type DeliveryPlanStepDependencyPatchReference struct {
	DependencyStepID string `json:"dependency_step_id"`
	RepositoryRef    string `json:"repository_ref"`
	BaseSHA          string `json:"base_sha"`
	SHA256           string `json:"sha256"`
	SizeBytes        int64  `json:"size_bytes"`
}

// DeliveryPlanStepDependencyPatchManifestSHA256 computes the canonical
// newline-delimited digest for a direct-dependency patch set. An empty set is
// represented by SHA-256 of the empty byte string.
func DeliveryPlanStepDependencyPatchManifestSHA256(references []DeliveryPlanStepDependencyPatchReference) (string, error) {
	if len(references) > MaxDeliveryPlanStepDependencyPatchReferences {
		return "", fmt.Errorf("dependency patch manifest exceeds the reference limit")
	}
	ordered := append([]DeliveryPlanStepDependencyPatchReference(nil), references...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].DependencyStepID != ordered[j].DependencyStepID {
			return ordered[i].DependencyStepID < ordered[j].DependencyStepID
		}
		if ordered[i].RepositoryRef != ordered[j].RepositoryRef {
			return ordered[i].RepositoryRef < ordered[j].RepositoryRef
		}
		return ordered[i].SHA256 < ordered[j].SHA256
	})

	var manifest strings.Builder
	var totalBytes int64
	seen := make(map[string]struct{}, len(ordered))
	for _, reference := range ordered {
		dependencyStepID, err := uuid.FromString(reference.DependencyStepID)
		if err != nil || dependencyStepID == uuid.Nil || dependencyStepID.String() != reference.DependencyStepID ||
			!validDeliveryPlanStepPatchRepositoryReference(reference.RepositoryRef) || !validDeliveryPlanStepCommitSHA(reference.BaseSHA) ||
			!validDeliveryPlanStepSHA256(reference.SHA256) || reference.SizeBytes < 1 || reference.SizeBytes > MaxDeliveryPlanStepPatchArtifactBytes {
			return "", fmt.Errorf("dependency patch manifest contains an invalid reference")
		}
		uniqueKey := reference.DependencyStepID + "\x00" + reference.RepositoryRef
		if _, duplicate := seen[uniqueKey]; duplicate {
			return "", fmt.Errorf("dependency patch manifest contains a duplicate repository")
		}
		seen[uniqueKey] = struct{}{}
		if totalBytes > MaxDeliveryPlanStepDependencyPatchBytes-reference.SizeBytes {
			return "", fmt.Errorf("dependency patch manifest exceeds the total size limit")
		}
		totalBytes += reference.SizeBytes
		manifest.WriteString(reference.DependencyStepID)
		manifest.WriteByte('\n')
		manifest.WriteString(reference.RepositoryRef)
		manifest.WriteByte('\n')
		manifest.WriteString(reference.BaseSHA)
		manifest.WriteByte('\n')
		manifest.WriteString(reference.SHA256)
		manifest.WriteByte('\n')
		manifest.WriteString(strconv.FormatInt(reference.SizeBytes, 10))
		manifest.WriteByte('\n')
	}
	digest := sha256.Sum256([]byte(manifest.String()))
	return hex.EncodeToString(digest[:]), nil
}

// DeliveryPlanStepDependencyPatch is an append-only receipt that a verified
// direct-dependency patch was handed to the current consumer lease. It stores
// source artifact identity and safe metadata only, never bytes or S3 locations.
type DeliveryPlanStepDependencyPatch struct {
	ID                     uuid.UUID  `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"-"`
	PlanID                 uuid.UUID  `gorm:"type:uuid;not null;index" json:"-"`
	AutomationTaskID       uuid.UUID  `gorm:"type:uuid;not null;index" json:"-"`
	RunID                  string     `gorm:"type:uuid;not null;index:idx_delivery_step_dependency_patch_consumer,priority:1;uniqueIndex:idx_delivery_step_dependency_patch_idempotency,priority:1" json:"-"`
	StepID                 uuid.UUID  `gorm:"type:uuid;not null;index:idx_delivery_step_dependency_patch_consumer,priority:2;uniqueIndex:idx_delivery_step_dependency_patch_idempotency,priority:2" json:"-"`
	FencingToken           int64      `gorm:"not null;index:idx_delivery_step_dependency_patch_consumer,priority:3;uniqueIndex:idx_delivery_step_dependency_patch_idempotency,priority:3" json:"-"`
	DependencyStepID       uuid.UUID  `gorm:"type:uuid;not null;index;uniqueIndex:idx_delivery_step_dependency_patch_idempotency,priority:4" json:"-"`
	SourcePatchArtifactID  uuid.UUID  `gorm:"type:uuid;not null;index" json:"-"`
	SourceAutomationTaskID uuid.UUID  `gorm:"type:uuid;not null;index" json:"-"`
	SourceRunID            string     `gorm:"type:uuid;not null;index" json:"-"`
	WorkerID               string     `gorm:"type:uuid;not null;index" json:"-"`
	AgentKey               string     `gorm:"type:varchar(64);not null;index" json:"-"`
	MachineID              string     `gorm:"type:varchar(64);not null" json:"-"`
	AgentInstanceID        *uuid.UUID `gorm:"type:uuid;index" json:"-"`
	ManifestSHA256         string     `gorm:"type:varchar(64);not null;check:delivery_step_dependency_patch_manifest_sha,manifest_sha256 ~ '^[0-9a-f]{64}$'" json:"-"`
	RepositoryRef          string     `gorm:"type:varchar(256);not null;uniqueIndex:idx_delivery_step_dependency_patch_idempotency,priority:5" json:"-"`
	BaseSHA                string     `gorm:"type:varchar(64);not null;check:delivery_step_dependency_patch_base_sha,base_sha ~ '^(?:[0-9a-f]{40}|[0-9a-f]{64})$'" json:"-"`
	SHA256                 string     `gorm:"type:varchar(64);not null;check:delivery_step_dependency_patch_sha,sha256 ~ '^[0-9a-f]{64}$'" json:"-"`
	SizeBytes              int64      `gorm:"not null;check:delivery_step_dependency_patch_size,size_bytes BETWEEN 1 AND 4194304" json:"-"`
	CreatedAt              time.Time  `gorm:"not null;index" json:"-"`
}
