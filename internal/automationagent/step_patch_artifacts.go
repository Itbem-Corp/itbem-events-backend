package automationagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"events-stocks/models"
)

const (
	maxStepPatchArtifacts      = models.MaxDeliveryPlanStepPatchArtifacts
	maxStepPatchArtifactBytes  = models.MaxDeliveryPlanStepPatchArtifactBytes
	stepPatchArtifactMediaType = "application/vnd.itbem.git-patch"
)

// Every field is excluded from JSON. The value exists only briefly in the
// in-memory RunImplementation result, then is extracted before any result,
// checkpoint, feedback, or callback can serialize that map.
type stepPatchArtifactPayload struct {
	RepositoryRef string `json:"-"`
	BaseSHA       string `json:"-"`
	SHA256        string `json:"-"`
	SizeBytes     int64  `json:"-"`
	Body          []byte `json:"-"`
}

const privateStepPatchArtifactKey = "\x00itbem_private_step_patch_artifact"

var (
	privateKeyPEMPattern    = regexp.MustCompile(`(?i)-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----`)
	knownCredentialPatterns = []*regexp.Regexp{
		regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`),
		regexp.MustCompile(`\bsk-proj-[A-Za-z0-9_-]{20,}`),
		regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`),
		regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`),
		regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{20,}`),
		regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
		regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{30,}`),
	}
	secretAssignmentPattern = regexp.MustCompile(`(?i)^\+[^+\r\n]*(?:api[_-]?key|access[_-]?token|client[_-]?secret|secret[_-]?key|password|authorization)\s*["']?\s*[:=]\s*["']?([^"'\s,;}]+)`)
)

// captureWorktreePatch uses the exact diff representation used by the review
// digest. The writer drains git's output but retains no more than maxBytes, so
// oversized patches cannot cause unbounded memory growth or hang the child.
func captureWorktreePatch(ctx context.Context, worktree, baseSHA string, maxBytes int) (string, []byte, error) {
	if maxBytes <= 0 {
		return "", nil, fmt.Errorf("reviewed patch size limit is invalid")
	}
	commandCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	command := exec.CommandContext(commandCtx, "git", "diff", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", strings.ToLower(strings.TrimSpace(baseSHA)))
	command.Dir = worktree
	command.Env = repositoryCommandEnvironment(os.Environ(), nil)
	bounded := &boundedPatchBuffer{limit: maxBytes}
	command.Stdout = bounded
	if err := command.Run(); err != nil {
		return "", nil, fmt.Errorf("could not capture reviewed worktree patch")
	}
	if bounded.overflow {
		return "", nil, fmt.Errorf("reviewed patch exceeds the supported artifact size")
	}
	if len(bounded.buffer.Bytes()) == 0 {
		return "", nil, fmt.Errorf("reviewed worktree has no publishable diff")
	}
	body := append([]byte(nil), bounded.buffer.Bytes()...)
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), body, nil
}

type boundedPatchBuffer struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (w *boundedPatchBuffer) Write(value []byte) (int, error) {
	if len(value) > w.limit-w.buffer.Len() {
		w.overflow = true
		return len(value), nil
	}
	return w.buffer.Write(value)
}

func newStepPatchArtifactPayload(repositoryRef, baseSHA, digest string, body []byte) stepPatchArtifactPayload {
	return stepPatchArtifactPayload{
		RepositoryRef: strings.TrimSpace(repositoryRef),
		BaseSHA:       strings.ToLower(strings.TrimSpace(baseSHA)),
		SHA256:        strings.ToLower(strings.TrimSpace(digest)),
		SizeBytes:     int64(len(body)),
		Body:          append([]byte(nil), body...),
	}
}

// takeStepPatchArtifacts removes all private payload markers from result and
// returns their bounded in-memory values. The rest of result is safe for normal
// acceptance verification and JSON checkpointing.
func takeStepPatchArtifacts(result map[string]any) ([]stepPatchArtifactPayload, error) {
	if result == nil {
		return nil, nil
	}
	changes := []map[string]any{result}
	switch raw := result["change_sets"].(type) {
	case []any:
		changes = changes[:0]
		for _, item := range raw {
			change, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("implementation patch evidence is invalid")
			}
			changes = append(changes, change)
		}
	case []map[string]any:
		changes = raw
	}

	values := make([]any, 0, len(changes))
	for _, change := range changes {
		value, exists := change[privateStepPatchArtifactKey]
		delete(change, privateStepPatchArtifactKey)
		if !exists {
			continue
		}
		values = append(values, value)
	}
	artifacts := make([]stepPatchArtifactPayload, 0, len(values))
	for _, value := range values {
		payload, ok := value.(stepPatchArtifactPayload)
		if !ok {
			wipeStepPatchArtifacts(artifacts)
			return nil, fmt.Errorf("implementation patch evidence is invalid")
		}
		artifacts = append(artifacts, payload)
		if len(artifacts) > maxStepPatchArtifacts {
			wipeStepPatchArtifacts(artifacts)
			return nil, fmt.Errorf("implementation patch evidence exceeds the supported repository count")
		}
	}
	if len(artifacts) == 0 {
		return nil, nil
	}
	seenRepositories := make(map[string]struct{}, len(artifacts))
	for _, artifact := range artifacts {
		if !validStepPatchArtifactPayload(artifact) {
			wipeStepPatchArtifacts(artifacts)
			return nil, fmt.Errorf("implementation patch evidence is invalid")
		}
		if _, duplicate := seenRepositories[artifact.RepositoryRef]; duplicate {
			wipeStepPatchArtifacts(artifacts)
			return nil, fmt.Errorf("implementation patch evidence contains a duplicate repository")
		}
		seenRepositories[artifact.RepositoryRef] = struct{}{}
	}
	return artifacts, nil
}

func validStepPatchArtifactPayload(artifact stepPatchArtifactPayload) bool {
	if strings.TrimSpace(artifact.RepositoryRef) == "" || len(artifact.RepositoryRef) > 256 ||
		len(artifact.Body) == 0 || len(artifact.Body) > maxStepPatchArtifactBytes || artifact.SizeBytes != int64(len(artifact.Body)) ||
		!gitCommitPattern.MatchString(artifact.BaseSHA) || !validAgentSHA256(artifact.SHA256) {
		return false
	}
	digest := sha256.Sum256(artifact.Body)
	return hex.EncodeToString(digest[:]) == artifact.SHA256
}

func wipeStepPatchArtifacts(artifacts []stepPatchArtifactPayload) {
	for index := range artifacts {
		for offset := range artifacts[index].Body {
			artifacts[index].Body[offset] = 0
		}
		artifacts[index].Body = nil
	}
}

// scanStepPatchForCredentials is intentionally fail closed: it only scans
// added diff lines for assignment-shaped secrets, plus unambiguous provider,
// GitHub, AWS and private-key token forms anywhere in the binary patch.
func scanStepPatchForCredentials(body []byte) error {
	if privateKeyPEMPattern.Match(body) {
		return fmt.Errorf("patch contains private-key material")
	}
	for _, pattern := range knownCredentialPatterns {
		if pattern.Match(body) {
			return fmt.Errorf("patch contains credential material")
		}
	}
	for _, line := range bytes.Split(body, []byte{'\n'}) {
		match := secretAssignmentPattern.FindSubmatch(line)
		if len(match) < 2 {
			continue
		}
		value := strings.TrimSpace(string(match[1]))
		if value == "" || value == "${...}" || value == "${REDACTED}" || value == "REDACTED" || value == "<redacted>" || value == "changeme" || value == "change-me" || value == "your_api_key" {
			continue
		}
		return fmt.Errorf("patch contains credential-like assignment")
	}
	return nil
}

// scanPlanStepEvidenceBytes applies the credential guard to ordinary report
// text as well as patch-formatted content. Adding the patch scanner's line
// marker lets the shared assignment parser detect JSON and plain-text key/value
// fields without retaining or reporting their matched value.
func scanPlanStepEvidenceBytes(body []byte) error {
	if privateKeyPEMPattern.Match(body) {
		return fmt.Errorf("evidence contains private-key material")
	}
	for _, pattern := range knownCredentialPatterns {
		if pattern.Match(body) {
			return fmt.Errorf("evidence contains credential material")
		}
	}
	for _, line := range bytes.Split(body, []byte{'\n'}) {
		marked := make([]byte, 1, len(line)+1)
		marked[0] = '+'
		marked = append(marked, line...)
		match := secretAssignmentPattern.FindSubmatch(marked)
		if len(match) < 2 {
			continue
		}
		value := strings.TrimSpace(string(match[1]))
		if value == "" || value == "${...}" || value == "${REDACTED}" || value == "REDACTED" || value == "<redacted>" || value == "changeme" || value == "change-me" || value == "your_api_key" {
			continue
		}
		return fmt.Errorf("evidence contains credential-like assignment")
	}
	return nil
}

func (w *Worker) uploadStepPatchArtifacts(ctx context.Context, taskID, runID, stepID string, artifacts []stepPatchArtifactPayload) ([]models.DeliveryPlanStepPatchArtifactReference, error) {
	if len(artifacts) == 0 {
		return nil, nil
	}
	if len(artifacts) > maxStepPatchArtifacts || !taskIDPattern.MatchString(strings.ToLower(strings.TrimSpace(taskID))) ||
		!validStepCallbackUUID(runID) || !validStepCallbackUUID(stepID) {
		wipeStepPatchArtifacts(artifacts)
		return nil, fmt.Errorf("plan-step patch artifact identity is invalid")
	}
	store, ok := w.store.(ArtifactStore)
	if !ok {
		wipeStepPatchArtifacts(artifacts)
		return nil, fmt.Errorf("configured private storage cannot upload plan-step patches")
	}
	references := make([]models.DeliveryPlanStepPatchArtifactReference, 0, len(artifacts))
	defer wipeStepPatchArtifacts(artifacts)
	for _, artifact := range artifacts {
		if !validStepPatchArtifactPayload(artifact) {
			return nil, fmt.Errorf("plan-step patch artifact is invalid")
		}
		if err := scanStepPatchForCredentials(artifact.Body); err != nil {
			return nil, fmt.Errorf("plan-step patch artifact was rejected by the credential scanner")
		}
		key := fmt.Sprintf("automation/%s/runs/%s/steps/%s/patches/%s.patch", taskID, runID, stepID, artifact.SHA256)
		if err := store.PutEncryptedObject(ctx, w.config.OutputBucket, key, artifact.Body, stepPatchArtifactMediaType); err != nil {
			return nil, fmt.Errorf("plan-step patch artifact could not be stored")
		}
		references = append(references, models.DeliveryPlanStepPatchArtifactReference{
			RepositoryRef: artifact.RepositoryRef,
			BaseSHA:       artifact.BaseSHA,
			SHA256:        artifact.SHA256,
			SizeBytes:     artifact.SizeBytes,
		})
	}
	return references, nil
}
