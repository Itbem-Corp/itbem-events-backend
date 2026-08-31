package automationagent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
)

const (
	maxCodeReviewChangedFiles = 300
	maxCodeReviewPatchBytes   = 512 << 10
)

// CodeReviewInput is an immutable review boundary. The untrusted prompt may
// contain the bounded diff, but the repository and commit identity must be
// explicit before the worker spends provider capacity on a PR review.
type CodeReviewInput struct {
	RepositoryRef string                       `json:"repository_ref"`
	BaseSHA       string                       `json:"base_sha"`
	HeadSHA       string                       `json:"head_sha"`
	Remote        *CodeReviewRemoteTarget      `json:"remote,omitempty"`
	ChangedFiles  []string                     `json:"changed_files"`
	ChangedLines  []CodeReviewChangedLineRange `json:"changed_line_ranges"`
	Patch         string                       `json:"patch"`
	PatchSHA256   string                       `json:"patch_sha256"`
}

// CodeReviewRemoteTarget is admitted only by the signed GitHub webhook. It
// contains no credential: the Review lane must independently resolve the
// allow-listed installation and mint its own repository-scoped token.
type CodeReviewRemoteTarget struct {
	PullRequestNumber int   `json:"pull_request_number"`
	InstallationID    int64 `json:"installation_id"`
}

// CodeReviewChangedLineRange is derived from the PR patch by the trusted
// producer. It lets the worker reject a plausible-looking line number that
// was never touched by the frozen head revision.
type CodeReviewChangedLineRange struct {
	File  string `json:"file"`
	Side  string `json:"side"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

// NewCodeReviewInput derives the review manifest exclusively from a frozen
// unified patch. Webhook callers never choose files or line ranges themselves.
func NewCodeReviewInput(repositoryRef, baseSHA, headSHA, patch string) (CodeReviewInput, error) {
	ranges, err := patchReviewLineRanges(patch)
	if err != nil {
		return CodeReviewInput{}, err
	}
	files := make([]string, 0, len(ranges))
	seen := make(map[string]struct{}, len(ranges))
	for _, item := range ranges {
		if _, present := seen[item.File]; present {
			continue
		}
		seen[item.File] = struct{}{}
		files = append(files, item.File)
	}
	sort.Strings(files)
	digest := sha256.Sum256([]byte(patch))
	input := CodeReviewInput{RepositoryRef: repositoryRef, BaseSHA: strings.ToLower(strings.TrimSpace(baseSHA)), HeadSHA: strings.ToLower(strings.TrimSpace(headSHA)), ChangedFiles: files, ChangedLines: ranges, Patch: patch, PatchSHA256: hex.EncodeToString(digest[:])}
	encoded, err := json.Marshal(input)
	if err != nil {
		return CodeReviewInput{}, err
	}
	return ParseCodeReviewInput(encoded)
}

// BindCodeReviewRemoteTarget turns an immutable advisory input into a remote
// review candidate without letting a webhook supply files, line ranges or a
// patch. Parsing the complete object again keeps one validation boundary.
func BindCodeReviewRemoteTarget(input CodeReviewInput, pullRequestNumber int, installationID int64) (CodeReviewInput, error) {
	input.Remote = &CodeReviewRemoteTarget{PullRequestNumber: pullRequestNumber, InstallationID: installationID}
	encoded, err := json.Marshal(input)
	if err != nil {
		return CodeReviewInput{}, err
	}
	return ParseCodeReviewInput(encoded)
}

func ParseCodeReviewInput(raw json.RawMessage) (CodeReviewInput, error) {
	var review CodeReviewInput
	if len(raw) == 0 || json.Unmarshal(raw, &review) != nil {
		return CodeReviewInput{}, fmt.Errorf("code review input is required")
	}
	review.RepositoryRef = strings.TrimSpace(review.RepositoryRef)
	review.BaseSHA = strings.ToLower(strings.TrimSpace(review.BaseSHA))
	review.HeadSHA = strings.ToLower(strings.TrimSpace(review.HeadSHA))
	review.PatchSHA256 = strings.ToLower(strings.TrimSpace(review.PatchSHA256))
	if !validReviewRepositoryRef(review.RepositoryRef) || !validReviewSHA(review.BaseSHA) || !validReviewSHA(review.HeadSHA) || review.BaseSHA == review.HeadSHA {
		return CodeReviewInput{}, fmt.Errorf("code review input requires an immutable repository and distinct base/head revisions")
	}
	if review.Remote != nil && (review.Remote.PullRequestNumber < 1 || review.Remote.InstallationID < 1) {
		return CodeReviewInput{}, fmt.Errorf("remote code review target is invalid")
	}
	if len(review.ChangedFiles) == 0 || len(review.ChangedFiles) > maxCodeReviewChangedFiles {
		return CodeReviewInput{}, fmt.Errorf("code review input requires a bounded changed_files list")
	}
	if len(review.Patch) == 0 || len(review.Patch) > maxCodeReviewPatchBytes || !strings.HasPrefix(review.Patch, "diff --git ") || !validReviewDigest(review.PatchSHA256) {
		return CodeReviewInput{}, fmt.Errorf("code review input requires a bounded unified patch and SHA-256")
	}
	digest := sha256.Sum256([]byte(review.Patch))
	if !strings.EqualFold(hex.EncodeToString(digest[:]), review.PatchSHA256) {
		return CodeReviewInput{}, fmt.Errorf("code review patch does not match its SHA-256")
	}
	seen := make(map[string]struct{}, len(review.ChangedFiles))
	files := make([]string, 0, len(review.ChangedFiles))
	for _, file := range review.ChangedFiles {
		file = strings.Trim(strings.TrimSpace(file), "/")
		if file == "" || len(file) > 500 || strings.Contains(file, "\\") || strings.HasPrefix(file, ".env") || strings.Contains(file, "../") || path.Clean(file) != file {
			return CodeReviewInput{}, fmt.Errorf("code review input has an invalid changed file")
		}
		if _, duplicate := seen[file]; duplicate {
			return CodeReviewInput{}, fmt.Errorf("code review input repeats a changed file")
		}
		seen[file] = struct{}{}
		files = append(files, file)
	}
	review.ChangedFiles = files
	if len(review.ChangedLines) == 0 || len(review.ChangedLines) > maxCodeReviewChangedFiles*8 {
		return CodeReviewInput{}, fmt.Errorf("code review input requires bounded changed_line_ranges")
	}
	ranges := make([]CodeReviewChangedLineRange, 0, len(review.ChangedLines))
	for _, lineRange := range review.ChangedLines {
		lineRange.File = strings.Trim(strings.TrimSpace(lineRange.File), "/")
		lineRange.Side = strings.ToLower(strings.TrimSpace(lineRange.Side))
		if lineRange.Side == "" {
			lineRange.Side = "head"
		}
		if _, changed := seen[lineRange.File]; !changed || (lineRange.Side != "head" && lineRange.Side != "base") || lineRange.Start < 1 || lineRange.End < lineRange.Start || lineRange.End > 10_000_000 {
			return CodeReviewInput{}, fmt.Errorf("code review input has an invalid changed line range")
		}
		ranges = append(ranges, lineRange)
	}
	patchRanges, err := patchReviewLineRanges(review.Patch)
	if err != nil {
		return CodeReviewInput{}, err
	}
	patchFiles, err := patchChangedFiles(review.Patch)
	if err != nil {
		return CodeReviewInput{}, err
	}
	if !sameReviewFiles(files, patchFiles) {
		return CodeReviewInput{}, fmt.Errorf("code review changed files do not match the frozen patch")
	}
	if !sameReviewLineRanges(ranges, patchRanges) {
		return CodeReviewInput{}, fmt.Errorf("code review changed line ranges do not match the frozen patch")
	}
	review.ChangedLines = patchRanges
	return review, nil
}

// CodeReviewPublicationSubjectSHA256 binds the external review side effect to
// the repository, PR, exact head and frozen patch. A model verdict is not part
// of the subject; the published payload digest separately binds the verdict.
func CodeReviewPublicationSubjectSHA256(review CodeReviewInput) (string, error) {
	if review.Remote == nil {
		return "", fmt.Errorf("remote code review target is required")
	}
	encoded, err := json.Marshal(struct {
		RepositoryRef  string `json:"repository_ref"`
		PullRequest    int    `json:"pull_request"`
		InstallationID int64  `json:"installation_id"`
		HeadSHA        string `json:"head_sha"`
		PatchSHA256    string `json:"patch_sha256"`
	}{review.RepositoryRef, review.Remote.PullRequestNumber, review.Remote.InstallationID, review.HeadSHA, review.PatchSHA256})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func patchChangedFiles(patch string) ([]string, error) {
	seen := map[string]struct{}{}
	files := make([]string, 0, 8)
	oldFile := ""
	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			oldFile = ""
			continue
		}
		if strings.HasPrefix(line, "--- ") {
			value := strings.TrimSpace(strings.TrimPrefix(line, "--- "))
			if value == "/dev/null" {
				oldFile = ""
				continue
			}
			if !strings.HasPrefix(value, "a/") {
				return nil, fmt.Errorf("code review patch has an unsupported old-file path")
			}
			oldFile = strings.TrimPrefix(value, "a/")
			continue
		}
		if !strings.HasPrefix(line, "+++ ") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
		file := ""
		if strings.HasPrefix(value, "b/") {
			file = strings.TrimPrefix(value, "b/")
		} else if value == "/dev/null" && oldFile != "" {
			// A complete file deletion has no head-side path. The immutable
			// manifest must retain its base path so deletion regressions remain
			// reviewable instead of being silently discarded.
			file = oldFile
		} else {
			return nil, fmt.Errorf("code review patch has an unsupported new-file path")
		}
		if _, exists := seen[file]; !exists {
			seen[file] = struct{}{}
			files = append(files, file)
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("code review patch has no changed files")
	}
	return files, nil
}

func sameReviewFiles(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make(map[string]struct{}, len(left))
	for _, value := range left {
		seen[value] = struct{}{}
	}
	for _, value := range right {
		if _, exists := seen[value]; !exists {
			return false
		}
	}
	return true
}

// patchReviewLineRanges is a small, fail-closed unified-diff parser. It
// records both added head and removed base lines so a reviewer can explain a
// risky deletion without pretending it exists in the new revision.
func patchReviewLineRanges(patch string) ([]CodeReviewChangedLineRange, error) {
	var currentFile, oldFile string
	var inHunk bool
	oldLine, newLine, remainingOld, remainingNew := 0, 0, 0, 0
	result := make([]CodeReviewChangedLineRange, 0, 16)
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case line == "":
			// strings.Split retains the terminal empty fragment after a newline.
			// It carries no patch content and must not invalidate the final hunk.
			if inHunk && (remainingOld != 0 || remainingNew != 0) {
				return nil, fmt.Errorf("code review patch hunk is truncated")
			}
			inHunk = false
		case strings.HasPrefix(line, "+++ "):
			if inHunk && (remainingOld != 0 || remainingNew != 0) {
				return nil, fmt.Errorf("code review patch hunk is truncated")
			}
			value := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			if strings.HasPrefix(value, "b/") {
				currentFile = strings.TrimPrefix(value, "b/")
			} else if value == "/dev/null" && oldFile != "" {
				currentFile = oldFile
			} else {
				return nil, fmt.Errorf("code review patch has an unsupported new-file path")
			}
			inHunk = false
		case strings.HasPrefix(line, "--- "):
			if inHunk && (remainingOld != 0 || remainingNew != 0) {
				return nil, fmt.Errorf("code review patch hunk is truncated")
			}
			value := strings.TrimSpace(strings.TrimPrefix(line, "--- "))
			if value == "/dev/null" {
				oldFile = ""
			} else if strings.HasPrefix(value, "a/") {
				oldFile = strings.TrimPrefix(value, "a/")
			} else {
				return nil, fmt.Errorf("code review patch has an unsupported old-file path")
			}
			inHunk = false
		case strings.HasPrefix(line, "@@ "):
			if inHunk && (remainingOld != 0 || remainingNew != 0) {
				return nil, fmt.Errorf("code review patch hunk is truncated")
			}
			if currentFile == "" {
				return nil, fmt.Errorf("code review patch hunk has no file")
			}
			oldStart, start, oldCount, newCount, err := unifiedPatchHunkCounts(line)
			if err != nil {
				return nil, err
			}
			oldLine, newLine, remainingOld, remainingNew, inHunk = oldStart, start, oldCount, newCount, true
		case inHunk && strings.HasPrefix(line, "+"):
			if remainingNew < 1 {
				return nil, fmt.Errorf("code review patch hunk exceeds declared line count")
			}
			appendPatchLine(&result, currentFile, newLine)
			newLine++
			remainingNew--
		case inHunk && strings.HasPrefix(line, "-"):
			// Removed base lines do not advance the head revision line number.
			if remainingOld < 1 {
				return nil, fmt.Errorf("code review patch hunk exceeds declared line count")
			}
			appendPatchReviewLine(&result, currentFile, "base", oldLine)
			oldLine++
			remainingOld--
		case inHunk && strings.HasPrefix(line, " "):
			if remainingOld < 1 || remainingNew < 1 {
				return nil, fmt.Errorf("code review patch hunk exceeds declared line count")
			}
			newLine++
			oldLine++
			remainingOld--
			remainingNew--
		case inHunk && strings.HasPrefix(line, "\\ No newline at end of file"):
			// Annotation for the preceding line; it has no line-number effect.
		case strings.HasPrefix(line, "diff --git "):
			if inHunk && (remainingOld != 0 || remainingNew != 0) {
				return nil, fmt.Errorf("code review patch hunk is truncated")
			}
			inHunk = false
			currentFile, oldFile = "", ""
		case inHunk:
			return nil, fmt.Errorf("code review patch hunk is malformed")
		}
	}
	if inHunk && (remainingOld != 0 || remainingNew != 0) {
		return nil, fmt.Errorf("code review patch hunk is truncated")
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("code review patch has no added head-revision lines")
	}
	return result, nil
}

func unifiedPatchHunkCounts(header string) (oldStart, newStart, oldCount, newCount int, err error) {
	fields := strings.Fields(header)
	if len(fields) < 3 || !strings.HasPrefix(fields[1], "-") || !strings.HasPrefix(fields[2], "+") {
		return 0, 0, 0, 0, fmt.Errorf("code review patch hunk header is invalid")
	}
	oldStart, oldCount, parseErr := unifiedPatchRange(strings.TrimPrefix(fields[1], "-"))
	newStart, newCount, newErr := unifiedPatchRange(strings.TrimPrefix(fields[2], "+"))
	if parseErr != nil || newErr != nil || oldStart < 0 || newStart < 0 {
		return 0, 0, 0, 0, fmt.Errorf("code review patch hunk header is invalid")
	}
	return oldStart, newStart, oldCount, newCount, nil
}

func unifiedPatchRange(value string) (start, count int, err error) {
	parts := strings.Split(value, ",")
	if len(parts) > 2 || len(parts) == 0 {
		return 0, 0, fmt.Errorf("invalid patch range")
	}
	start, err = strconv.Atoi(parts[0])
	if err != nil || start < 0 {
		return 0, 0, fmt.Errorf("invalid patch range")
	}
	count = 1
	if len(parts) == 2 {
		count, err = strconv.Atoi(parts[1])
		if err != nil || count < 0 {
			return 0, 0, fmt.Errorf("invalid patch range")
		}
	}
	return start, count, nil
}

func appendPatchLine(ranges *[]CodeReviewChangedLineRange, file string, line int) {
	appendPatchReviewLine(ranges, file, "head", line)
}

func appendPatchReviewLine(ranges *[]CodeReviewChangedLineRange, file, side string, line int) {
	if line < 1 {
		return
	}
	if len(*ranges) > 0 {
		last := &(*ranges)[len(*ranges)-1]
		if last.File == file && last.Side == side && last.End+1 == line {
			last.End = line
			return
		}
	}
	*ranges = append(*ranges, CodeReviewChangedLineRange{File: file, Side: side, Start: line, End: line})
}

func sameReviewLineRanges(left, right []CodeReviewChangedLineRange) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// SanitizedPatch is the exact frozen patch after conservative value redaction.
// The caller validates the digest against the original evidence first, then
// hands this model-safe representation to the provider. Redaction can change
// text but never expands the changed-file/line authority used for validation.
func (input CodeReviewInput) SanitizedPatch() string {
	patch, _ := RedactSourceExcerpt(input.Patch)
	return patch
}

// NormalizeCodeReviewCoverage keeps an approval from overstating what the
// frozen review evidence proves. A changed production file with no changed
// test is not necessarily defective, but this reviewer has not inspected the
// repository's existing test suite or CI. It therefore becomes an advisory
// comment with a precise coverage gap instead of an unqualified approval.
// The operation is deterministic and only derives from the immutable manifest.
func NormalizeCodeReviewCoverage(review map[string]any, boundary CodeReviewInput) {
	if !reviewNeedsCoverageGap(boundary) {
		return
	}
	gaps, _ := review["coverage_gaps"].([]any)
	if len(gaps) == 0 {
		review["coverage_gaps"] = []any{"No test change is included in this PR; existing coverage was not inspected. Add or identify a targeted regression test."}
	}
	if strings.EqualFold(strings.TrimSpace(stringAny(review["verdict"])), "approve") {
		review["verdict"] = "comment"
	}
}

func reviewNeedsCoverageGap(boundary CodeReviewInput) bool {
	hasProductionChange, hasTestChange := false, false
	for _, file := range boundary.ChangedFiles {
		if reviewTestFile(file) {
			hasTestChange = true
		}
		if reviewProductionSourceFile(file) {
			hasProductionChange = true
		}
	}
	return hasProductionChange && !hasTestChange
}

func reviewTestFile(file string) bool {
	file = strings.ToLower(strings.TrimSpace(file))
	base := path.Base(file)
	if strings.Contains(file, "/__tests__/") || strings.Contains(file, "/testdata/") || strings.Contains(file, "/tests/") || strings.Contains(file, "/test/") {
		return true
	}
	if strings.HasSuffix(base, "_test.go") || strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_spec.rb") || strings.HasSuffix(base, "test.php") || strings.HasSuffix(base, "tests.cs") || strings.HasSuffix(base, "test.java") || strings.HasSuffix(base, "tests.java") {
		return true
	}
	return strings.Contains(base, ".test.") || strings.Contains(base, ".spec.")
}

func reviewProductionSourceFile(file string) bool {
	file = strings.ToLower(strings.TrimSpace(file))
	if reviewTestFile(file) || strings.Contains(file, "/vendor/") || strings.Contains(file, "/node_modules/") || strings.HasSuffix(file, ".d.ts") {
		return false
	}
	switch path.Ext(file) {
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".rb", ".java", ".kt", ".cs", ".rs", ".php", ".swift", ".scala":
		return true
	default:
		return false
	}
}

func validReviewRepositoryRef(value string) bool {
	if !strings.HasPrefix(value, "github://") || len(value) > 240 {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(value, "github://"), "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] != "" && !strings.ContainsAny(value, " \\?#")
}

func validReviewSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, character := range value {
		if !isLowercaseHex(character) {
			return false
		}
	}
	return true
}

func validReviewDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !isLowercaseHex(character) {
			return false
		}
	}
	return true
}

func isLowercaseHex(character rune) bool {
	return (character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')
}

// ParseCodeReview turns a model response into a bounded, actionable review
// record. A review is advisory only: it never approves, merges, publishes, or
// changes a pull request. Its structure makes an eventual GitHub/PR surface
// deterministic instead of relying on free-form prose.
func ParseCodeReview(content string) (map[string]any, error) {
	review, ok := decodeJSONObject(content)
	if !ok {
		return nil, fmt.Errorf("code review must be a JSON object")
	}
	if !boundedCodeReviewText(review["summary"], 1200) {
		return nil, fmt.Errorf("code review requires a bounded summary")
	}
	verdict := strings.ToLower(strings.TrimSpace(stringAny(review["verdict"])))
	if _, ok := map[string]struct{}{"approve": {}, "comment": {}, "request_changes": {}, "blocked": {}}[verdict]; !ok {
		return nil, fmt.Errorf("code review verdict must be approve, comment, request_changes or blocked")
	}
	if err := validateCodeReviewStrings(review, "review_scope", 12); err != nil {
		return nil, err
	}
	if len(review["review_scope"].([]any)) == 0 {
		return nil, fmt.Errorf("code review requires a non-empty review_scope")
	}
	if err := validateCodeReviewStrings(review, "test_plan", 12); err != nil {
		return nil, err
	}
	testPlan := review["test_plan"].([]any)
	if err := validateCodeReviewStrings(review, "coverage_gaps", 12); err != nil {
		return nil, err
	}
	coverageGaps := review["coverage_gaps"].([]any)
	findings, ok := review["findings"].([]any)
	if !ok || len(findings) > 24 {
		return nil, fmt.Errorf("code review findings must be a bounded list")
	}
	if verdict == "approve" && len(findings) != 0 {
		return nil, fmt.Errorf("an approving code review cannot include findings")
	}
	if verdict == "request_changes" && len(findings) == 0 {
		return nil, fmt.Errorf("a change-requesting code review requires a finding")
	}
	if verdict == "blocked" && len(findings) != 0 {
		return nil, fmt.Errorf("a blocked code review must report its evidence gap without speculative findings")
	}
	if verdict == "blocked" && len(coverageGaps) == 0 {
		return nil, fmt.Errorf("a blocked code review requires an actionable coverage gap")
	}
	if verdict != "blocked" && len(testPlan) == 0 {
		return nil, fmt.Errorf("a conclusive code review requires an actionable test plan")
	}
	if verdict == "approve" && len(coverageGaps) != 0 {
		return nil, fmt.Errorf("an approving code review cannot hide known coverage gaps")
	}
	seen := make(map[string]struct{}, len(findings))
	seenLocations := make(map[string]struct{}, len(findings))
	normalized := make([]any, 0, len(findings))
	hasBlockingFinding := false
	for _, raw := range findings {
		finding, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("code review findings must be objects")
		}
		id := strings.ToLower(strings.TrimSpace(stringAny(finding["id"])))
		if id == "" || len(id) > 80 {
			return nil, fmt.Errorf("code review finding id is invalid")
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, fmt.Errorf("code review finding ids must be unique")
		}
		seen[id] = struct{}{}
		severity := strings.ToLower(strings.TrimSpace(stringAny(finding["severity"])))
		if _, ok := map[string]struct{}{"critical": {}, "high": {}, "medium": {}, "low": {}}[severity]; !ok {
			return nil, fmt.Errorf("code review finding severity is invalid")
		}
		if severity == "critical" || severity == "high" || severity == "medium" {
			hasBlockingFinding = true
		}
		category := strings.ToLower(strings.TrimSpace(stringAny(finding["category"])))
		if _, ok := map[string]struct{}{"correctness": {}, "security": {}, "reliability": {}, "performance": {}, "maintainability": {}, "test_coverage": {}}[category]; !ok {
			return nil, fmt.Errorf("code review finding category is invalid")
		}
		for _, field := range []string{"title", "evidence", "evidence_quote", "recommendation"} {
			if !boundedCodeReviewText(finding[field], 1000) {
				return nil, fmt.Errorf("code review finding %s is required", field)
			}
		}
		confidence, ok := finding["confidence"].(float64)
		if !ok || confidence < 0 || confidence > 1 {
			return nil, fmt.Errorf("code review finding confidence must be between 0 and 1")
		}
		if (severity == "critical" && confidence < 0.9) || (severity == "high" && confidence < 0.8) || (severity == "medium" && confidence < 0.65) {
			return nil, fmt.Errorf("code review finding confidence is too low for its blocking severity")
		}
		evidence := strings.TrimSpace(stringAny(finding["evidence"]))
		recommendation := strings.TrimSpace(stringAny(finding["recommendation"]))
		if strings.EqualFold(evidence, recommendation) {
			return nil, fmt.Errorf("code review evidence and recommendation must be distinct")
		}
		file := strings.TrimSpace(stringAny(finding["file"]))
		side := strings.ToLower(strings.TrimSpace(stringAny(finding["side"])))
		if side == "" {
			side = "head"
		}
		if side != "head" && side != "base" {
			return nil, fmt.Errorf("code review finding side is invalid")
		}
		if file == "" || len(file) > 500 || strings.Contains(file, "..") || strings.HasPrefix(file, "/") || strings.HasPrefix(file, "\\") {
			return nil, fmt.Errorf("code review finding file is invalid")
		}
		lineStart, startOK := integralReviewLine(finding["line_start"])
		lineEnd, endOK := integralReviewLine(finding["line_end"])
		if !startOK || !endOK || lineEnd < lineStart {
			return nil, fmt.Errorf("code review finding line range is invalid")
		}
		location := file + ":" + side + ":" + strconv.Itoa(lineStart) + ":" + strconv.Itoa(lineEnd)
		if _, duplicate := seenLocations[location]; duplicate {
			return nil, fmt.Errorf("code review findings must not repeat a source location")
		}
		seenLocations[location] = struct{}{}
		normalized = append(normalized, map[string]any{
			"id": id, "severity": severity, "category": category, "title": strings.TrimSpace(stringAny(finding["title"])),
			"file": file, "side": side, "line_start": lineStart, "line_end": lineEnd, "evidence": evidence,
			"evidence_quote": strings.TrimSpace(stringAny(finding["evidence_quote"])), "recommendation": recommendation, "confidence": confidence,
		})
	}
	if verdict == "request_changes" && !hasBlockingFinding {
		return nil, fmt.Errorf("a change-requesting code review requires a medium or higher finding")
	}
	if verdict != "request_changes" && hasBlockingFinding {
		return nil, fmt.Errorf("a code review with medium or higher findings must request changes")
	}
	review["verdict"] = verdict
	review["findings"] = normalized
	return review, nil
}

// ValidateCodeReviewBoundary prevents a model from attaching a finding to a
// file outside the frozen change set or downgrading a high-impact defect into
// a non-blocking verdict. It runs after parsing because the immutable input is
// known only to the worker, never trusted from the response.
func ValidateCodeReviewBoundary(review map[string]any, boundary CodeReviewInput) error {
	changed := make(map[string]struct{}, len(boundary.ChangedFiles))
	for _, file := range boundary.ChangedFiles {
		changed[file] = struct{}{}
	}
	findings, ok := review["findings"].([]any)
	if !ok {
		return fmt.Errorf("code review findings must be normalized")
	}
	verdict := strings.ToLower(strings.TrimSpace(stringAny(review["verdict"])))
	for _, raw := range findings {
		finding, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("code review findings must be normalized")
		}
		file := strings.TrimSpace(stringAny(finding["file"]))
		if _, present := changed[file]; !present {
			return fmt.Errorf("code review finding must reference a changed file")
		}
		side := strings.ToLower(strings.TrimSpace(stringAny(finding["side"])))
		if side == "" {
			side = "head"
		}
		if side != "head" && side != "base" {
			return fmt.Errorf("code review finding side is invalid")
		}
		start, startOK := integralReviewLine(finding["line_start"])
		end, endOK := integralReviewLine(finding["line_end"])
		if !startOK || !endOK || !reviewLineRangeTouched(boundary.ChangedLines, file, side, start, end) {
			return fmt.Errorf("code review finding must reference changed lines")
		}
		quote := strings.TrimSpace(stringAny(finding["evidence_quote"]))
		if len(quote) < 3 || len(quote) > 1000 || !patchContainsChangedQuote(boundary.Patch, file, side, start, end, quote) {
			return fmt.Errorf("code review finding evidence quote must appear in changed patch lines")
		}
		severity := strings.ToLower(strings.TrimSpace(stringAny(finding["severity"])))
		if (severity == "critical" || severity == "high" || severity == "medium") && verdict != "request_changes" {
			return fmt.Errorf("code review with blocking findings must request changes")
		}
	}
	return nil
}

func patchContainsChangedQuote(patch, file, side string, start, end int, quote string) bool {
	currentFile, oldFile := "", ""
	inHunk := false
	oldLine, newLine := 0, 0
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "--- "):
			value := strings.TrimSpace(strings.TrimPrefix(line, "--- "))
			if strings.HasPrefix(value, "a/") {
				oldFile = strings.TrimPrefix(value, "a/")
			} else if value == "/dev/null" {
				oldFile = ""
			} else {
				return false
			}
			inHunk = false
		case strings.HasPrefix(line, "+++ "):
			value := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			if strings.HasPrefix(value, "b/") {
				currentFile = strings.TrimPrefix(value, "b/")
			} else if value == "/dev/null" && oldFile != "" {
				currentFile = oldFile
			} else {
				return false
			}
			inHunk = false
		case strings.HasPrefix(line, "@@ "):
			inHunk = currentFile == file
			oldStart, newStart, _, _, err := unifiedPatchHunkCounts(line)
			if err != nil {
				return false
			}
			oldLine, newLine = oldStart, newStart
		case inHunk && strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			if side == "head" && newLine >= start && newLine <= end && strings.Contains(strings.TrimSpace(strings.TrimPrefix(line, "+")), quote) {
				return true
			}
			newLine++
		case inHunk && strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			if side == "base" && oldLine >= start && oldLine <= end && strings.Contains(strings.TrimSpace(strings.TrimPrefix(line, "-")), quote) {
				return true
			}
			oldLine++
		case inHunk && strings.HasPrefix(line, " "):
			oldLine++
			newLine++
		case strings.HasPrefix(line, "diff --git "):
			inHunk = false
			currentFile, oldFile = "", ""
		}
	}
	return false
}

func reviewLineRangeTouched(ranges []CodeReviewChangedLineRange, file, side string, start, end int) bool {
	for _, lineRange := range ranges {
		if lineRange.File == file && lineRange.Side == side && start >= lineRange.Start && end <= lineRange.End {
			return true
		}
	}
	return false
}

func validateCodeReviewStrings(review map[string]any, field string, max int) error {
	items, ok := review[field].([]any)
	if !ok || len(items) > max {
		return fmt.Errorf("code review %s must be a bounded list of strings", field)
	}
	for _, item := range items {
		if !boundedCodeReviewText(item, 1000) {
			return fmt.Errorf("code review %s must be a bounded list of strings", field)
		}
	}
	return nil
}

func boundedCodeReviewText(value any, max int) bool {
	text, ok := value.(string)
	return ok && strings.TrimSpace(text) != "" && len(text) <= max
}

func integralReviewLine(value any) (int, bool) {
	if number, ok := value.(int); ok {
		return number, number >= 1 && number <= 10_000_000
	}
	number, ok := value.(float64)
	if !ok || number < 1 || number > 10_000_000 || number != float64(int(number)) {
		return 0, false
	}
	return int(number), true
}
