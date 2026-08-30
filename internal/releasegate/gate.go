// Package releasegate evaluates merge and release evidence without invoking a
// model or performing an external side effect. Callers may explain its output,
// but only this structured decision is authoritative.
package releasegate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

const SchemaVersion = 1
const maxInputBytes = 1 << 20

type Action string

const (
	ActionMerge   Action = "merge"
	ActionRelease Action = "release"
)

type EvidenceStatus string

const (
	StatusPassed EvidenceStatus = "passed"
	StatusFailed EvidenceStatus = "failed"
)

type RecoveryClassification string

const (
	RecoveryRollback       RecoveryClassification = "rollback"
	RecoveryRollForward    RecoveryClassification = "roll_forward"
	RecoveryExpandContract RecoveryClassification = "expand_contract"
	RecoveryIrreversible   RecoveryClassification = "irreversible"
)

type Revision struct {
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	SHA        string `json:"sha"`
}

type Policy struct {
	Resolved          bool     `json:"resolved"`
	Digest            string   `json:"digest"`
	RequiredTestKinds []string `json:"required_test_kinds"`
}

type BranchEvidence struct {
	Repository          string   `json:"repository"`
	HeadSHA             string   `json:"head_sha"`
	Mergeable           bool     `json:"mergeable"`
	ConflictFree        bool     `json:"conflict_free"`
	ProtectionEvaluated bool     `json:"protection_evaluated"`
	RequiredChecks      []string `json:"required_checks"`
}

type CheckEvidence struct {
	Repository string         `json:"repository"`
	Name       string         `json:"name"`
	HeadSHA    string         `json:"head_sha"`
	Status     EvidenceStatus `json:"status"`
}

type ReviewEvidence struct {
	Repository             string `json:"repository"`
	HeadSHA                string `json:"head_sha"`
	AuthorActor            string `json:"author_actor"`
	ReviewerActor          string `json:"reviewer_actor"`
	Approved               bool   `json:"approved"`
	BlockingChangeRequests int    `json:"blocking_change_requests"`
}

type VaultEvidence struct {
	Repository string `json:"repository"`
	HeadSHA    string `json:"head_sha"`
	RevisionID string `json:"revision_id"`
	Reconciled bool   `json:"reconciled"`
}

type TestEvidence struct {
	Kind         string         `json:"kind"`
	MatrixDigest string         `json:"matrix_digest"`
	Status       EvidenceStatus `json:"status"`
}

type SecurityEvidence struct {
	Repository       string `json:"repository"`
	HeadSHA          string `json:"head_sha"`
	SecretScanPassed bool   `json:"secret_scan_passed"`
	HighFindings     int    `json:"high_findings"`
	CriticalFindings int    `json:"critical_findings"`
}

type MatrixEvidence struct {
	MatrixDigest string         `json:"matrix_digest"`
	Status       EvidenceStatus `json:"status"`
}

type RecoveryEvidence struct {
	MatrixDigest   string                 `json:"matrix_digest"`
	Classification RecoveryClassification `json:"classification"`
	Evaluated      bool                   `json:"evaluated"`
	HumanApproved  bool                   `json:"human_approved"`
}

type HumanApproval struct {
	Actor         string `json:"actor"`
	ActorType     string `json:"actor_type"`
	SubjectDigest string `json:"subject_digest"`
	Approved      bool   `json:"approved"`
}

type Input struct {
	SchemaVersion int                `json:"schema_version"`
	Action        Action             `json:"action"`
	ChangeSetID   string             `json:"change_set_id"`
	Revisions     []Revision         `json:"revisions"`
	Policy        Policy             `json:"policy"`
	Branches      []BranchEvidence   `json:"branches"`
	Checks        []CheckEvidence    `json:"checks"`
	Reviews       []ReviewEvidence   `json:"reviews"`
	Vault         []VaultEvidence    `json:"vault"`
	Tests         []TestEvidence     `json:"tests"`
	Security      []SecurityEvidence `json:"security"`
	Compatibility MatrixEvidence     `json:"compatibility"`
	Migrations    MatrixEvidence     `json:"migrations"`
	Dependencies  MatrixEvidence     `json:"dependencies"`
	Environment   MatrixEvidence     `json:"environment"`
	Recovery      RecoveryEvidence   `json:"recovery"`
	HumanApproval *HumanApproval     `json:"human_approval,omitempty"`
}

type Reason struct {
	Code       string `json:"code"`
	Repository string `json:"repository,omitempty"`
	Evidence   string `json:"evidence,omitempty"`
}

type Decision struct {
	SchemaVersion int      `json:"schema_version"`
	Action        Action   `json:"action"`
	ChangeSetID   string   `json:"change_set_id"`
	MatrixDigest  string   `json:"matrix_digest,omitempty"`
	SubjectDigest string   `json:"subject_digest,omitempty"`
	State         string   `json:"state"`
	Reasons       []Reason `json:"reasons"`
}

var changeSetPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
var repositoryPartPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
var evidenceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _./:+-]{0,127}$`)

// DecodeInput is intentionally strict. Repository text cannot smuggle an
// override, model instruction, or future field into a gate evaluation.
func DecodeInput(payload []byte) (Input, error) {
	if len(payload) == 0 || len(payload) > maxInputBytes {
		return Input{}, fmt.Errorf("release gate input size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var input Input
	if err := decoder.Decode(&input); err != nil {
		return Input{}, fmt.Errorf("decode release gate input: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Input{}, fmt.Errorf("release gate input must contain one JSON document")
	}
	return input, nil
}

// RevisionMatrixDigest returns the stable identity of the exact coordinated
// repository/branch/SHA matrix. Input order never changes the digest.
func RevisionMatrixDigest(revisions []Revision) (string, error) {
	normalized, err := normalizeRevisions(revisions)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("encode revision matrix: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// Evaluate always fails closed. Missing, malformed, stale, contradictory, or
// failed evidence yields a blocked decision with stable machine reason codes.
func Evaluate(input Input) Decision {
	decision := Decision{SchemaVersion: SchemaVersion, Action: input.Action, ChangeSetID: input.ChangeSetID, State: "blocked", Reasons: []Reason{}}
	add := func(code, repository, evidence string) {
		decision.Reasons = append(decision.Reasons, Reason{Code: code, Repository: repository, Evidence: evidence})
	}
	if input.SchemaVersion != SchemaVersion || (input.Action != ActionMerge && input.Action != ActionRelease) || !changeSetPattern.MatchString(input.ChangeSetID) {
		add("invalid_input", "", "schema, action, or change-set identity")
	}

	normalized, matrixErr := normalizeRevisions(input.Revisions)
	if matrixErr != nil {
		add("revision_matrix_invalid", "", "revisions")
		sortReasons(decision.Reasons)
		return decision
	}
	decision.MatrixDigest, _ = RevisionMatrixDigest(normalized)
	decision.SubjectDigest = subjectDigest(input.Action, decision.MatrixDigest, input.Policy, input.Recovery.Classification)

	if !input.Policy.Resolved || !validDigest(input.Policy.Digest) || !validUniqueNames(input.Policy.RequiredTestKinds) {
		add("policy_unresolved", "", "required test policy")
	}

	branches := uniqueBranches(input.Branches, add)
	checks := uniqueChecks(input.Checks, add)
	reviews := groupReviews(input.Reviews)
	vault := uniqueVault(input.Vault, add)
	security := uniqueSecurity(input.Security, add)

	for _, revision := range normalized {
		repositoryKey := strings.ToLower(revision.Repository)
		branch, ok := branches[repositoryKey]
		if !ok {
			add("branch_evidence_missing", revision.Repository, "branch")
		} else {
			if !equalSHA(branch.HeadSHA, revision.SHA) {
				add("branch_evidence_stale", revision.Repository, "head_sha")
			}
			if !branch.ProtectionEvaluated {
				add("branch_protection_unknown", revision.Repository, "protection")
			}
			if !branch.Mergeable || !branch.ConflictFree {
				add("branch_not_mergeable", revision.Repository, "mergeability")
			}
			if !validUniqueNames(branch.RequiredChecks) {
				add("required_checks_invalid", revision.Repository, "required_checks")
			} else {
				for _, required := range branch.RequiredChecks {
					check, exists := checks[repositoryKey+"\x00"+strings.ToLower(required)]
					if !exists {
						add("required_check_missing", revision.Repository, required)
					} else if !equalSHA(check.HeadSHA, revision.SHA) {
						add("required_check_stale", revision.Repository, required)
					} else if check.Status != StatusPassed {
						add("required_check_failed", revision.Repository, required)
					}
				}
			}
		}

		approved := false
		exactReviewSeen := false
		for _, review := range reviews[repositoryKey] {
			if !equalSHA(review.HeadSHA, revision.SHA) {
				continue
			}
			exactReviewSeen = true
			if review.BlockingChangeRequests > 0 {
				add("review_changes_requested", revision.Repository, "review")
				continue
			}
			if !review.Approved {
				continue
			}
			if !independentActors(review.AuthorActor, review.ReviewerActor) {
				add("review_not_independent", revision.Repository, "review")
				continue
			}
			approved = true
		}
		if !approved {
			if len(reviews[repositoryKey]) == 0 {
				add("review_missing", revision.Repository, "review")
			} else if !exactReviewSeen {
				add("review_evidence_stale", revision.Repository, "review")
			} else {
				add("review_not_approved_for_head", revision.Repository, "review")
			}
		}

		vaultEvidence, ok := vault[repositoryKey]
		if !ok {
			add("vault_evidence_missing", revision.Repository, "vault")
		} else if !equalSHA(vaultEvidence.HeadSHA, revision.SHA) {
			add("vault_evidence_stale", revision.Repository, "vault")
		} else if !vaultEvidence.Reconciled || strings.TrimSpace(vaultEvidence.RevisionID) == "" {
			add("vault_not_reconciled", revision.Repository, "vault")
		}

		securityEvidence, ok := security[repositoryKey]
		if !ok {
			add("security_evidence_missing", revision.Repository, "security")
		} else if !equalSHA(securityEvidence.HeadSHA, revision.SHA) {
			add("security_evidence_stale", revision.Repository, "security")
		} else {
			if !securityEvidence.SecretScanPassed {
				add("secret_scan_failed", revision.Repository, "secret_scan")
			}
			if securityEvidence.HighFindings < 0 || securityEvidence.CriticalFindings < 0 || securityEvidence.HighFindings > 0 || securityEvidence.CriticalFindings > 0 {
				add("high_or_critical_security_findings", revision.Repository, "security")
			}
		}
	}

	tests := uniqueTests(input.Tests, add)
	if input.Policy.Resolved && validDigest(input.Policy.Digest) && validUniqueNames(input.Policy.RequiredTestKinds) {
		for _, required := range input.Policy.RequiredTestKinds {
			test, ok := tests[strings.ToLower(required)]
			if !ok {
				add("required_test_missing", "", required)
			} else if !equalDigest(test.MatrixDigest, decision.MatrixDigest) {
				add("required_test_stale", "", required)
			} else if test.Status != StatusPassed {
				add("required_test_failed", "", required)
			}
		}
	}

	evaluateMatrixEvidence("compatibility", input.Compatibility, decision.MatrixDigest, add)
	evaluateMatrixEvidence("migrations", input.Migrations, decision.MatrixDigest, add)
	evaluateMatrixEvidence("dependencies", input.Dependencies, decision.MatrixDigest, add)
	evaluateMatrixEvidence("environment", input.Environment, decision.MatrixDigest, add)
	evaluateRecovery(input.Recovery, decision.MatrixDigest, add)

	approval := input.HumanApproval
	if approval == nil || !approval.Approved || !strings.EqualFold(strings.TrimSpace(approval.ActorType), "human") || strings.TrimSpace(approval.Actor) == "" {
		add("human_approval_missing", "", "human_approval")
	} else if !equalDigest(approval.SubjectDigest, decision.SubjectDigest) {
		add("human_approval_stale", "", "human_approval")
	}

	sortReasons(decision.Reasons)
	if len(decision.Reasons) == 0 {
		decision.State = "allowed"
	}
	return decision
}

func normalizeRevisions(revisions []Revision) ([]Revision, error) {
	if len(revisions) == 0 || len(revisions) > 64 {
		return nil, fmt.Errorf("revision matrix size is invalid")
	}
	normalized := make([]Revision, 0, len(revisions))
	seen := map[string]struct{}{}
	for _, revision := range revisions {
		repository := strings.TrimSpace(revision.Repository)
		branch := strings.TrimSpace(revision.Branch)
		sha := strings.ToLower(strings.TrimSpace(revision.SHA))
		if !validRepository(repository) || !validBranch(branch) || !validSHA(sha) {
			return nil, fmt.Errorf("revision is invalid")
		}
		key := strings.ToLower(repository)
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("repository is duplicated")
		}
		seen[key] = struct{}{}
		normalized = append(normalized, Revision{Repository: repository, Branch: branch, SHA: sha})
	}
	sort.Slice(normalized, func(i, j int) bool {
		return strings.ToLower(normalized[i].Repository) < strings.ToLower(normalized[j].Repository)
	})
	return normalized, nil
}

func validRepository(value string) bool {
	parts := strings.Split(value, "/")
	return len(parts) == 2 && repositoryPartPattern.MatchString(parts[0]) && repositoryPartPattern.MatchString(parts[1])
}

func validBranch(value string) bool {
	return value != "" && len(value) <= 255 && !strings.HasPrefix(value, "/") && !strings.HasSuffix(value, "/") &&
		!strings.HasSuffix(value, ".") && !strings.HasSuffix(value, ".lock") && !strings.ContainsAny(value, " ~^:?*[\\") &&
		!strings.Contains(value, "..") && !strings.Contains(value, "@{") && !strings.Contains(value, "//") &&
		!strings.Contains(value, "/.") && strings.IndexFunc(value, func(character rune) bool { return character < 0x20 || character == 0x7f }) == -1
}

func validSHA(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validUniqueNames(values []string) bool {
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := strings.ToLower(value)
		if !evidenceNamePattern.MatchString(value) {
			return false
		}
		if _, duplicate := seen[key]; duplicate {
			return false
		}
		seen[key] = struct{}{}
	}
	return true
}

func uniqueBranches(values []BranchEvidence, add func(string, string, string)) map[string]BranchEvidence {
	result := map[string]BranchEvidence{}
	for _, value := range values {
		key := strings.ToLower(strings.TrimSpace(value.Repository))
		if !validRepository(value.Repository) || key == "" {
			add("branch_evidence_invalid", "", "branch")
			continue
		}
		if _, duplicate := result[key]; duplicate {
			add("branch_evidence_duplicate", value.Repository, "branch")
			continue
		}
		result[key] = value
	}
	return result
}

func uniqueChecks(values []CheckEvidence, add func(string, string, string)) map[string]CheckEvidence {
	result := map[string]CheckEvidence{}
	for _, value := range values {
		name := strings.TrimSpace(value.Name)
		key := strings.ToLower(strings.TrimSpace(value.Repository)) + "\x00" + strings.ToLower(name)
		if !validRepository(value.Repository) || !evidenceNamePattern.MatchString(name) {
			add("check_evidence_invalid", "", "check")
			continue
		}
		if _, duplicate := result[key]; duplicate {
			add("check_evidence_duplicate", value.Repository, name)
			continue
		}
		result[key] = value
	}
	return result
}

func groupReviews(values []ReviewEvidence) map[string][]ReviewEvidence {
	result := map[string][]ReviewEvidence{}
	for _, value := range values {
		key := strings.ToLower(strings.TrimSpace(value.Repository))
		if validRepository(value.Repository) && value.BlockingChangeRequests >= 0 {
			result[key] = append(result[key], value)
		}
	}
	return result
}

func uniqueVault(values []VaultEvidence, add func(string, string, string)) map[string]VaultEvidence {
	result := map[string]VaultEvidence{}
	for _, value := range values {
		key := strings.ToLower(strings.TrimSpace(value.Repository))
		if !validRepository(value.Repository) {
			add("vault_evidence_invalid", "", "vault")
			continue
		}
		if _, duplicate := result[key]; duplicate {
			add("vault_evidence_duplicate", value.Repository, "vault")
			continue
		}
		result[key] = value
	}
	return result
}

func uniqueSecurity(values []SecurityEvidence, add func(string, string, string)) map[string]SecurityEvidence {
	result := map[string]SecurityEvidence{}
	for _, value := range values {
		key := strings.ToLower(strings.TrimSpace(value.Repository))
		if !validRepository(value.Repository) {
			add("security_evidence_invalid", "", "security")
			continue
		}
		if _, duplicate := result[key]; duplicate {
			add("security_evidence_duplicate", value.Repository, "security")
			continue
		}
		result[key] = value
	}
	return result
}

func uniqueTests(values []TestEvidence, add func(string, string, string)) map[string]TestEvidence {
	result := map[string]TestEvidence{}
	for _, value := range values {
		kind := strings.TrimSpace(value.Kind)
		key := strings.ToLower(kind)
		if !evidenceNamePattern.MatchString(kind) {
			add("test_evidence_invalid", "", "test")
			continue
		}
		if _, duplicate := result[key]; duplicate {
			add("test_evidence_duplicate", "", kind)
			continue
		}
		result[key] = value
	}
	return result
}

func evaluateMatrixEvidence(name string, evidence MatrixEvidence, matrixDigest string, add func(string, string, string)) {
	if !equalDigest(evidence.MatrixDigest, matrixDigest) {
		add(name+"_evidence_stale", "", name)
	} else if evidence.Status != StatusPassed {
		add(name+"_not_ready", "", name)
	}
}

func evaluateRecovery(evidence RecoveryEvidence, matrixDigest string, add func(string, string, string)) {
	if !equalDigest(evidence.MatrixDigest, matrixDigest) {
		add("recovery_evidence_stale", "", "recovery")
		return
	}
	valid := evidence.Classification == RecoveryRollback || evidence.Classification == RecoveryRollForward || evidence.Classification == RecoveryExpandContract || evidence.Classification == RecoveryIrreversible
	if !evidence.Evaluated || !valid {
		add("recovery_not_evaluated", "", "recovery")
	}
	if evidence.Classification == RecoveryIrreversible && !evidence.HumanApproved {
		add("irreversible_recovery_approval_missing", "", "recovery")
	}
}

func subjectDigest(action Action, matrixDigest string, policy Policy, recovery RecoveryClassification) string {
	kinds := append([]string(nil), policy.RequiredTestKinds...)
	for index := range kinds {
		kinds[index] = strings.ToLower(strings.TrimSpace(kinds[index]))
	}
	sort.Strings(kinds)
	encoded, _ := json.Marshal(struct {
		Action       Action                 `json:"action"`
		MatrixDigest string                 `json:"matrix_digest"`
		PolicyDigest string                 `json:"policy_digest"`
		Recovery     RecoveryClassification `json:"recovery"`
		Tests        []string               `json:"required_test_kinds"`
	}{Action: action, MatrixDigest: matrixDigest, PolicyDigest: strings.ToLower(strings.TrimSpace(policy.Digest)), Recovery: recovery, Tests: kinds})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func independentActors(author, reviewer string) bool {
	author = strings.TrimSpace(author)
	reviewer = strings.TrimSpace(reviewer)
	return author != "" && reviewer != "" && !strings.EqualFold(author, reviewer)
}

func equalSHA(left, right string) bool {
	return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(right)) && validSHA(strings.ToLower(strings.TrimSpace(left)))
}

func equalDigest(left, right string) bool {
	left = strings.ToLower(strings.TrimSpace(left))
	right = strings.ToLower(strings.TrimSpace(right))
	return left == right && validDigest(left)
}

func validDigest(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func sortReasons(reasons []Reason) {
	sort.SliceStable(reasons, func(i, j int) bool {
		left := reasons[i].Code + "\x00" + strings.ToLower(reasons[i].Repository) + "\x00" + strings.ToLower(reasons[i].Evidence)
		right := reasons[j].Code + "\x00" + strings.ToLower(reasons[j].Repository) + "\x00" + strings.ToLower(reasons[j].Evidence)
		return left < right
	})
}
