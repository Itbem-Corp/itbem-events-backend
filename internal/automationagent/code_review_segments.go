package automationagent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	codeReviewSegmentMaxPatchBytes = 48 << 10
	codeReviewSegmentMaxFiles      = 6
	codeReviewSegmentMaxContext    = 64 << 10
	codeReviewSegmentMaxExcerpts   = 24
	maxCodeReviewSegments          = 16
)

// SegmentCodeReviewInput partitions a large immutable PR by complete file
// diffs. It never cuts a hunk or changes source authority: every segment is
// parsed again, receives only exact-revision context for its files, and has no
// remote target. Only an aggregate validated against the original boundary may
// later be published.
func SegmentCodeReviewInput(input CodeReviewInput) ([]CodeReviewInput, error) {
	blocks, err := splitCodeReviewPatchFiles(input.Patch)
	if err != nil {
		return nil, err
	}
	canReorder := true
	for _, block := range blocks {
		canReorder = canReorder && strings.HasSuffix(block, "\n")
	}
	if canReorder {
		sort.SliceStable(blocks, func(left, right int) bool {
			return codeReviewPatchBlockRisk(blocks[left]) > codeReviewPatchBlockRisk(blocks[right])
		})
	}
	groups := make([]string, 0, len(blocks))
	current := ""
	files := 0
	flush := func() {
		if current != "" {
			groups = append(groups, current)
			current, files = "", 0
		}
	}
	for _, block := range blocks {
		if len(block) > codeReviewSegmentMaxPatchBytes {
			return nil, fmt.Errorf("code review file diff exceeds the segment safety limit")
		}
		if current != "" && (files >= codeReviewSegmentMaxFiles || len(current)+len(block) > codeReviewSegmentMaxPatchBytes) {
			flush()
		}
		current += block
		files++
	}
	flush()
	if len(groups) == 0 || len(groups) > maxCodeReviewSegments {
		return nil, fmt.Errorf("code review requires a bounded segment count")
	}
	segments := make([]CodeReviewInput, 0, len(groups))
	for _, patch := range groups {
		segment, err := NewCodeReviewInput(input.RepositoryRef, input.BaseSHA, input.HeadSHA, patch)
		if err != nil {
			return nil, fmt.Errorf("code review segment is invalid: %w", err)
		}
		context := boundedCodeReviewSegmentContext(segment.ChangedFiles, input.Context)
		if len(context) > 0 {
			segment, err = BindCodeReviewContext(segment, context)
			if err != nil {
				return nil, fmt.Errorf("code review segment context is invalid: %w", err)
			}
		}
		segments = append(segments, segment)
	}
	if !sameCodeReviewPatchBlocks(input.Patch, groups) {
		return nil, fmt.Errorf("code review segmentation changed the frozen file diffs")
	}
	if err := validateCodeReviewSegmentCoverage(input, segments); err != nil {
		return nil, err
	}
	return segments, nil
}

func codeReviewPatchBlockRisk(block string) int {
	files, err := patchChangedFiles(block)
	if err != nil || len(files) != 1 {
		return 0
	}
	file := strings.ToLower(files[0])
	score := 10
	for _, keyword := range []string{"security", "auth", "permission", "policy", "credential", "secret", "token", "crypto"} {
		if strings.Contains(file, keyword) {
			score += 100
			break
		}
	}
	for _, keyword := range []string{"migration", "schema", "database", "deploy", "workflow", "infrastructure", "terraform", "recovery", "rollback"} {
		if strings.Contains(file, keyword) {
			score += 70
			break
		}
	}
	for _, keyword := range []string{"controller", "handler", "route", "api", "worker", "queue"} {
		if strings.Contains(file, keyword) {
			score += 40
			break
		}
	}
	if strings.Contains(file, "test") || strings.Contains(file, "spec") {
		score += 15
	}
	if strings.HasSuffix(file, ".md") || strings.Contains(file, "docs/") {
		score -= 5
	}
	return score
}

func sameCodeReviewPatchBlocks(original string, groups []string) bool {
	expected, err := splitCodeReviewPatchFiles(original)
	if err != nil {
		return false
	}
	counts := make(map[string]int, len(expected))
	for _, block := range expected {
		counts[block]++
	}
	actualCount := 0
	for _, group := range groups {
		blocks, err := splitCodeReviewPatchFiles(group)
		if err != nil {
			return false
		}
		for _, block := range blocks {
			counts[block]--
			if counts[block] < 0 {
				return false
			}
			actualCount++
		}
	}
	if actualCount != len(expected) {
		return false
	}
	for _, count := range counts {
		if count != 0 {
			return false
		}
	}
	return true
}

func boundedCodeReviewSegmentContext(files []string, excerpts []CodeReviewContextExcerpt) []CodeReviewContextExcerpt {
	byFile := make(map[string][]CodeReviewContextExcerpt, len(files))
	for _, excerpt := range excerpts {
		byFile[excerpt.File] = append(byFile[excerpt.File], excerpt)
	}
	positions := make(map[string]int, len(files))
	result := make([]CodeReviewContextExcerpt, 0, min(len(excerpts), codeReviewSegmentMaxExcerpts))
	total := 0
	for len(result) < codeReviewSegmentMaxExcerpts && total < codeReviewSegmentMaxContext {
		added := false
		for _, file := range files {
			position := positions[file]
			if position >= len(byFile[file]) {
				continue
			}
			excerpt := byFile[file][position]
			positions[file] = position + 1
			remaining := codeReviewSegmentMaxContext - total
			if len(excerpt.Content) > remaining {
				excerpt.Content = excerpt.Content[:remaining]
				excerpt.End = min(excerpt.End, excerpt.Start+strings.Count(excerpt.Content, "\n"))
			}
			if strings.TrimSpace(excerpt.Content) == "" {
				continue
			}
			result = append(result, excerpt)
			total += len(excerpt.Content)
			added = true
			if len(result) >= codeReviewSegmentMaxExcerpts || total >= codeReviewSegmentMaxContext {
				break
			}
		}
		if !added {
			break
		}
	}
	return result
}

func splitCodeReviewPatchFiles(patch string) ([]string, error) {
	if !strings.HasPrefix(patch, "diff --git ") {
		return nil, fmt.Errorf("code review patch is not a unified Git diff")
	}
	parts := strings.Split(patch, "\ndiff --git ")
	blocks := make([]string, 0, len(parts))
	for index, part := range parts {
		if index > 0 {
			part = "diff --git " + part
		}
		if index < len(parts)-1 {
			part += "\n"
		}
		if strings.TrimSpace(part) == "" {
			return nil, fmt.Errorf("code review patch contains an empty file diff")
		}
		blocks = append(blocks, part)
	}
	return blocks, nil
}

func validateCodeReviewSegmentCoverage(input CodeReviewInput, segments []CodeReviewInput) error {
	files := make(map[string]int, len(input.ChangedFiles))
	ranges := make([]CodeReviewChangedLineRange, 0, len(input.ChangedLines))
	for _, segment := range segments {
		if segment.Remote != nil || segment.RepositoryRef != input.RepositoryRef || segment.BaseSHA != input.BaseSHA || segment.HeadSHA != input.HeadSHA {
			return fmt.Errorf("code review segment escaped the immutable subject")
		}
		for _, file := range segment.ChangedFiles {
			files[file]++
		}
		ranges = append(ranges, segment.ChangedLines...)
	}
	for _, file := range input.ChangedFiles {
		if files[file] != 1 {
			return fmt.Errorf("code review segmentation did not cover each changed file exactly once")
		}
		delete(files, file)
	}
	if len(files) != 0 || !sameReviewLineRangeSet(input.ChangedLines, ranges) {
		return fmt.Errorf("code review segmentation changed the frozen line authority")
	}
	return nil
}

func sameReviewLineRangeSet(left, right []CodeReviewChangedLineRange) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[CodeReviewChangedLineRange]int, len(left))
	for _, item := range left {
		counts[item]++
	}
	for _, item := range right {
		counts[item]--
		if counts[item] < 0 {
			return false
		}
	}
	for _, count := range counts {
		if count != 0 {
			return false
		}
	}
	return true
}

// AggregateCodeReviewSegments combines only independently parsed and
// boundary-validated segment verdicts. No model gets authority to override a
// sibling result or the full immutable boundary.
func AggregateCodeReviewSegments(boundary CodeReviewInput, segments []CodeReviewInput, reviews []map[string]any) (map[string]any, error) {
	if len(segments) == 0 || len(segments) != len(reviews) {
		return nil, fmt.Errorf("code review segment results are incomplete")
	}
	if err := validateCodeReviewSegmentCoverage(boundary, segments); err != nil {
		return nil, err
	}
	findings := make([]any, 0)
	scopes, tests, gaps := []string{}, []string{}, []string{}
	blocked, commented := false, false
	for index, review := range reviews {
		if err := ValidateCodeReviewBoundary(review, segments[index]); err != nil {
			return nil, fmt.Errorf("code review segment %d is invalid: %w", index+1, err)
		}
		verdict := strings.TrimSpace(stringAny(review["verdict"]))
		blocked = blocked || verdict == "blocked"
		commented = commented || verdict == "comment"
		for _, raw := range review["findings"].([]any) {
			finding := cloneCodeReviewFinding(raw.(map[string]any))
			prefix := fmt.Sprintf("s%02d-", index+1)
			id := boundedReviewID(stringAny(finding["id"]), 80-len(prefix))
			finding["id"] = prefix + id
			findings = append(findings, finding)
		}
		scopes = appendUniqueReviewStrings(scopes, review["review_scope"], 12)
		tests = appendUniqueReviewStrings(tests, review["test_plan"], 12)
		gaps = appendUniqueReviewStrings(gaps, review["coverage_gaps"], 12)
	}
	if len(findings) > 24 {
		return nil, fmt.Errorf("code review aggregate has too many findings")
	}
	if blocked && len(findings) > 0 {
		return nil, fmt.Errorf("code review cannot safely aggregate blocked and finding-bearing segments")
	}
	verdict := "approve"
	if blocked {
		verdict = "blocked"
	} else if hasBlockingCodeReviewFinding(findings) {
		verdict = "request_changes"
	} else if len(findings) > 0 || len(gaps) > 0 || commented {
		verdict = "comment"
	}
	if len(scopes) == 0 {
		scopes = []string{"all frozen review segments"}
	}
	if verdict != "blocked" && len(tests) == 0 {
		tests = []string{"Run the repository checks configured for the exact head SHA."}
	}
	if verdict == "blocked" && len(gaps) == 0 {
		gaps = []string{"At least one review segment lacked sufficient evidence; inspect its private result before retrying."}
	}
	aggregate := map[string]any{
		"summary":       fmt.Sprintf("Reviewed %d exact-SHA segments covering %d changed files.", len(segments), len(boundary.ChangedFiles)),
		"verdict":       verdict,
		"review_scope":  stringSliceAny(scopes),
		"findings":      findings,
		"test_plan":     stringSliceAny(tests),
		"coverage_gaps": stringSliceAny(gaps),
	}
	encoded, err := json.Marshal(aggregate)
	if err != nil {
		return nil, fmt.Errorf("code review aggregate could not be encoded")
	}
	parsed, err := ParseCodeReview(string(encoded))
	if err != nil {
		return nil, fmt.Errorf("code review aggregate is invalid: %w", err)
	}
	NormalizeCodeReviewCoverage(parsed, boundary)
	if err := ValidateCodeReviewBoundary(parsed, boundary); err != nil {
		return nil, fmt.Errorf("code review aggregate escaped the full boundary: %w", err)
	}
	return parsed, nil
}

func boundedReviewID(value string, limit int) string {
	value = strings.ToValidUTF8(value, "-")
	for len(value) > limit {
		_, size := utf8.DecodeLastRuneInString(value)
		if size < 1 {
			return "finding"
		}
		value = value[:len(value)-size]
	}
	return value
}

func appendUniqueReviewStrings(target []string, value any, limit int) []string {
	seen := make(map[string]struct{}, len(target))
	for _, item := range target {
		seen[item] = struct{}{}
	}
	items, _ := value.([]any)
	for _, raw := range items {
		item := strings.TrimSpace(stringAny(raw))
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		if len(target) >= limit {
			break
		}
		seen[item] = struct{}{}
		target = append(target, item)
	}
	return target
}

func cloneCodeReviewFinding(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func hasBlockingCodeReviewFinding(findings []any) bool {
	for _, raw := range findings {
		severity := strings.TrimSpace(stringAny(raw.(map[string]any)["severity"]))
		if severity == "critical" || severity == "high" || severity == "medium" {
			return true
		}
	}
	return false
}

func stringSliceAny(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}
