package automationagent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

var deliveryPlanListFields = []string{
	"context_reviewed", "context_gaps", "assumptions", "human_decisions",
	"implementation_steps", "risks", "qa_plan", "evidence_plan",
	"acceptance_criteria", "files_impacted", "rollback_plan", "questions",
}

var repositoryImpactStates = map[string]struct{}{"changes": {}, "consulted": {}, "untouched": {}}

var deliverySummaryExecutiveFields = []string{"what_changed", "why", "how_to_test"}
var deliverySummaryTechnicalFields = []string{"decisions", "evidence"}

var deliveryQAVerdicts = map[string]struct{}{"passed": {}, "failed": {}, "blocked": {}}
var deliveryQACheckStates = map[string]struct{}{"passed": {}, "failed": {}, "skipped": {}}

// ParseDeliveryPlan keeps the planning gate deterministic. A prose-only plan
// is a failed task rather than an ambiguous artifact a human might approve.
func ParseDeliveryPlan(content string) (map[string]any, error) {
	plan, ok := decodeJSONObject(content)
	if !ok {
		return nil, fmt.Errorf("delivery plan must be a JSON object")
	}
	summary, ok := plan["summary"].(string)
	if !ok || strings.TrimSpace(summary) == "" {
		return nil, fmt.Errorf("delivery plan requires a non-empty summary")
	}
	for _, name := range deliveryPlanListFields {
		// Some reasoning models serialize a single sentence as text despite the
		// requested list. Normalize only bounded, semantically unambiguous list
		// fields. A missing risks field is represented as an explicit harness
		// warning rather than silently treated as "no risk"; all other missing
		// fields remain strict and fail closed.
		if name == "risks" {
			if _, present := plan[name]; !present {
				plan[name] = []any{"Provider omitted the risks field; human review is required before approval."}
				recordDeliveryPlanRepair(plan, "risks missing: inserted an explicit human-review warning")
			}
		}
		if name == "rollback_plan" || name == "risks" {
			if value, ok := plan[name].(string); ok && strings.TrimSpace(value) != "" {
				trimmed := strings.TrimSpace(value)
				if len(trimmed) > 240 {
					return nil, fmt.Errorf("delivery plan field %s must be a list of non-empty strings", name)
				}
				plan[name] = []any{trimmed}
				recordDeliveryPlanRepair(plan, fmt.Sprintf("%s single string normalized to a one-item list", name))
			}
		}
		values, ok := plan[name].([]any)
		if !ok {
			return nil, fmt.Errorf("delivery plan field %s must be a list of non-empty strings", name)
		}
		for _, value := range values {
			item, ok := value.(string)
			if !ok || strings.TrimSpace(item) == "" {
				return nil, fmt.Errorf("delivery plan field %s must be a list of non-empty strings", name)
			}
		}
	}
	if err := normalizeRepositoryImpact(plan); err != nil {
		return nil, err
	}
	if err := normalizeQAExecutionMatrix(plan); err != nil {
		return nil, err
	}
	if err := normalizeBrowserQAProposalForHumanReview(plan); err != nil {
		return nil, err
	}
	if err := ValidateDeliveryPlanExecutionContract(plan); err != nil {
		return nil, err
	}
	if estimate, ok := plan["estimate"].(string); !ok || strings.TrimSpace(estimate) == "" {
		return nil, fmt.Errorf("delivery plan requires a non-empty estimate")
	}
	if intent, ok := plan["goal_interpretation"].(string); !ok || strings.TrimSpace(intent) == "" {
		return nil, fmt.Errorf("delivery plan requires a non-empty goal_interpretation")
	}
	if autonomy, ok := plan["autonomy_boundary"].(string); !ok || strings.TrimSpace(autonomy) == "" {
		return nil, fmt.Errorf("delivery plan requires a non-empty autonomy_boundary")
	}
	confidence, ok := plan["confidence"].(float64)
	if !ok || confidence < 0 || confidence > 1 {
		return nil, fmt.Errorf("delivery plan confidence must be a number from 0 to 1")
	}
	return plan, nil
}

// recordDeliveryPlanRepair makes compatibility repairs observable in the
// structured result. It never grants a missing capability or invents a
// repository; it only records a bounded shape repair before the normal
// validators run.
func recordDeliveryPlanRepair(plan map[string]any, repair string) {
	if strings.TrimSpace(repair) == "" {
		return
	}
	entries, _ := plan["_harness_repairs"].([]any)
	plan["_harness_repairs"] = append(entries, repair)
}

// ValidateDeliveryPlanExecutionContract prevents a plan that intends to
// change code from crossing the review boundary without the minimum contract
// needed to execute and verify it. Exploratory or metadata-only plans may keep
// these lists empty while they are still waiting for context.
func ValidateDeliveryPlanExecutionContract(plan map[string]any) error {
	files, _ := plan["files_impacted"].([]any)
	impacts, _ := plan["repository_impact"].([]any)
	hasChanges := len(files) > 0
	for _, raw := range impacts {
		entry, ok := raw.(map[string]any)
		if ok && strings.EqualFold(strings.TrimSpace(stringAny(entry["impact"])), "changes") {
			hasChanges = true
			break
		}
	}
	if !hasChanges {
		return nil
	}
	for _, field := range []string{"implementation_steps", "qa_plan", "evidence_plan", "acceptance_criteria", "rollback_plan"} {
		values, ok := plan[field].([]any)
		if !ok || len(values) == 0 {
			return fmt.Errorf("delivery plan with code changes requires a non-empty %s", field)
		}
	}
	return nil
}

// normalizeBrowserQAProposalForHumanReview keeps an optional browser proposal
// from turning an otherwise reviewable delivery plan into a failed billable
// run. Browser cases are never executable until a human has approved the
// plan, so an invalid model proposal is removed rather than repaired or
// trusted. The private provider response retains the original proposal for
// inspection; the persisted plan explicitly tells the reviewer to define the
// browser checks before approving QA.
func normalizeBrowserQAProposalForHumanReview(plan map[string]any) error {
	_, hasMode := plan["browser_qa_mode"]
	_, hasCases := plan["browser_qa_cases"]
	if !hasMode && !hasCases {
		return nil
	}
	if err := ValidateApprovedBrowserQAPlan(plan); err == nil {
		return nil
	}

	delete(plan, "browser_qa_mode")
	delete(plan, "browser_qa_cases")
	plan["browser_qa_review"] = map[string]any{
		"status": "requires_human_revision",
		"reason": "La propuesta de navegador del agente no era ejecutable y se retiró antes de cualquier aprobación.",
	}
	appendDeliveryPlanReviewNote(plan, "human_decisions", "Definir o corregir los casos de navegador antes de aprobar el plan de QA.")
	return nil
}

func appendDeliveryPlanReviewNote(plan map[string]any, field, note string) {
	values, ok := plan[field].([]any)
	if !ok {
		return
	}
	for _, raw := range values {
		if strings.EqualFold(strings.TrimSpace(stringAny(raw)), note) {
			return
		}
	}
	plan[field] = append(values, note)
}

// normalizeQAExecutionMatrix turns the planner's execution proposal into a
// reviewable per-repository contract. It is optional for older/manual plans;
// agent-generated plans are instructed to include one exact row per frozen
// repository and topology validation below binds those rows to the task.
func normalizeQAExecutionMatrix(plan map[string]any) error {
	raw, present := plan["qa_execution_matrix"]
	if !present || raw == nil {
		return nil
	}
	entries, ok := raw.([]any)
	if !ok || len(entries) > 16 {
		return fmt.Errorf("delivery plan qa_execution_matrix must be a bounded list")
	}
	seen := map[string]struct{}{}
	normalized := make([]any, 0, len(entries))
	for _, rawEntry := range entries {
		entry, ok := rawEntry.(map[string]any)
		if !ok {
			return fmt.Errorf("delivery plan qa_execution_matrix entries must be objects")
		}
		reference := strings.TrimSpace(stringAny(entry["repository_ref"]))
		if reference == "" {
			return fmt.Errorf("delivery plan qa_execution_matrix repository_ref is required")
		}
		if _, duplicate := seen[reference]; duplicate {
			return fmt.Errorf("delivery plan qa_execution_matrix must not repeat a repository")
		}
		seen[reference] = struct{}{}
		row := map[string]any{"repository_ref": reference}
		for _, key := range []string{"run_validation", "run_qa", "run_stagehand", "collect_evidence"} {
			value, ok := entry[key].(bool)
			if !ok {
				return fmt.Errorf("delivery plan qa_execution_matrix %s must be boolean", key)
			}
			row[key] = value
		}
		if row["run_stagehand"] == true && row["collect_evidence"] != true {
			return fmt.Errorf("delivery plan qa_execution_matrix requires collect_evidence when run_stagehand is true")
		}
		normalized = append(normalized, row)
	}
	plan["qa_execution_matrix"] = normalized
	return nil
}

// ParseDeliverySummary turns the model's end-of-delivery proposal into a
// reviewable release draft. It never releases work by itself: a human must
// still prepare and approve the final delivery gate.
func ParseDeliverySummary(content string) (map[string]any, error) {
	summary, ok := decodeJSONObject(content)
	if !ok {
		return nil, fmt.Errorf("delivery summary must be a JSON object")
	}
	executive, ok := summary["executive"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("delivery summary requires an executive object")
	}
	for _, field := range deliverySummaryExecutiveFields {
		value, ok := executive[field].(string)
		if !ok || strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("delivery executive field %s must be a non-empty string", field)
		}
	}
	if err := validateDeliverySummaryList(executive, "risks"); err != nil {
		return nil, err
	}
	technical, ok := summary["technical"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("delivery summary requires a technical object")
	}
	normalizeDeliverySummaryEvidence(technical)
	for _, field := range deliverySummaryTechnicalFields {
		// Absence is valid when the supplied history contains no decisions.
		// The context-aware evidence gate rejects omission of recorded gates.
		if field == "decisions" {
			if decisions, ok := technical[field].([]any); ok && len(decisions) == 0 {
				continue
			}
		}
		if err := validateDeliverySummaryList(technical, field); err != nil {
			return nil, err
		}
	}
	return map[string]any{"executive": executive, "technical": technical}, nil
}

// normalizeDeliverySummaryEvidence is a bounded compatibility repair for
// providers that follow the evidence shape semantically but serialize each
// citation as {id,title,summary} instead of the declared string list. It never
// grants evidence: ValidateDeliverySummaryEvidence still checks UUID
// membership and the exact recorded title against the control-plane input.
// Invalid or mixed entries remain invalid and fail closed in the normal list
// validator. The repair marker is retained in the structured result so the
// operator can distinguish a normalized provider response from a native one.
func normalizeDeliverySummaryEvidence(technical map[string]any) {
	entries, ok := technical["evidence"].([]any)
	if !ok || len(entries) == 0 {
		return
	}
	normalized := make([]any, 0, len(entries))
	repaired := false
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			normalized = append(normalized, raw)
			continue
		}
		id := strings.TrimSpace(stringAny(entry["id"]))
		title := strings.TrimSpace(stringAny(entry["title"]))
		if id == "" || title == "" {
			normalized = append(normalized, raw)
			continue
		}
		citation := id + " — " + title
		if summary := strings.TrimSpace(stringAny(entry["summary"])); summary != "" {
			citation += ": " + summary
		}
		normalized = append(normalized, citation)
		repaired = true
	}
	if repaired {
		technical["evidence"] = normalized
		technical["_harness_repairs"] = []any{"technical.evidence object citations normalized to grounded strings"}
	}
}

// ParseDeliveryChat accepts only a bounded, informational answer. A chat
// response is never a plan, gate decision or tool instruction; keeping this
// contract separate prevents a conversational provider response from being
// promoted into workflow state by accident.
func ParseDeliveryChat(content string) (map[string]any, error) {
	answer, ok := decodeJSONObject(content)
	if !ok {
		return nil, fmt.Errorf("delivery chat must be a JSON object")
	}
	text, ok := answer["answer"].(string)
	text = strings.TrimSpace(text)
	if !ok || text == "" || len(text) > 6000 {
		return nil, fmt.Errorf("delivery chat requires a bounded non-empty answer")
	}
	repairs := make([]any, 0, 2)
	for _, field := range []string{"next_steps", "questions"} {
		raw, present := answer[field]
		if !present {
			answer[field] = []any{}
			continue
		}
		items, ok := raw.([]any)
		valid := ok && len(items) <= 4
		if valid {
			for _, item := range items {
				value, stringValue := item.(string)
				if !stringValue || strings.TrimSpace(value) == "" || len(value) > 240 {
					valid = false
					break
				}
			}
		}
		if !valid {
			// Suggestions are optional presentation fields. Preserve the valid,
			// bounded answer while explicitly dropping an invalid optional list;
			// never coerce model objects into operator instructions.
			answer[field] = []any{}
			repairs = append(repairs, field+" omitted because it did not contain at most four bounded strings")
		}
	}
	if len(repairs) > 0 {
		answer["_harness_repairs"] = repairs
	}
	answer["answer"] = text
	return answer, nil
}

// ParseDeliveryQAReport turns the agent's narration of an already-executed QA
// run into a reviewable structure. It never decides the QA gate: the raw,
// independently collected commands, preview check and artifacts remain the
// source of truth for a human reviewer.
func ParseDeliveryQAReport(content string) (map[string]any, error) {
	report, ok := decodeJSONObject(content)
	if !ok {
		return nil, fmt.Errorf("delivery QA report must be a JSON object")
	}
	if summary, ok := report["summary"].(string); !ok || strings.TrimSpace(summary) == "" {
		return nil, fmt.Errorf("delivery QA report requires a non-empty summary")
	}
	verdict := strings.ToLower(strings.TrimSpace(stringAny(report["verdict"])))
	if _, allowed := deliveryQAVerdicts[verdict]; !allowed {
		return nil, fmt.Errorf("delivery QA report verdict must be passed, failed or blocked")
	}
	checks, ok := report["checks"].([]any)
	if !ok || len(checks) == 0 {
		return nil, fmt.Errorf("delivery QA report requires at least one check")
	}
	normalizedChecks := make([]any, 0, len(checks))
	for _, value := range checks {
		check, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("delivery QA checks must be structured")
		}
		name, detail := strings.TrimSpace(stringAny(check["name"])), strings.TrimSpace(stringAny(check["detail"]))
		status := strings.ToLower(strings.TrimSpace(stringAny(check["status"])))
		if name == "" || detail == "" {
			return nil, fmt.Errorf("delivery QA checks require name and detail")
		}
		if _, allowed := deliveryQACheckStates[status]; !allowed {
			return nil, fmt.Errorf("delivery QA check status must be passed, failed or skipped")
		}
		normalizedChecks = append(normalizedChecks, map[string]any{"name": name, "status": status, "detail": detail})
	}
	for _, field := range []string{"defects", "coverage_gaps", "recommended_actions"} {
		items, ok := report[field].([]any)
		if !ok {
			return nil, fmt.Errorf("delivery QA report field %s must be a list of strings", field)
		}
		for _, item := range items {
			if text, ok := item.(string); !ok || strings.TrimSpace(text) == "" {
				return nil, fmt.Errorf("delivery QA report field %s must be a list of strings", field)
			}
		}
	}
	report["verdict"] = verdict
	report["checks"] = normalizedChecks
	return report, nil
}

// ValidateDeliveryQAReport prevents a model summary from describing a passing
// run when independently observed QA already contains a failing preview,
// command or screenshot. A bad narration is simply not promoted to structured
// UI; the real evidence and private response remain available for review.
func ValidateDeliveryQAReport(report, execution map[string]any) error {
	if strings.EqualFold(stringAny(report["verdict"]), "passed") {
		if err := validateQASuccessEvidence(report, execution); err != nil {
			return err
		}
	}
	if strings.EqualFold(stringAny(report["verdict"]), "passed") && deliveryQAExecutionFailed(execution) {
		return fmt.Errorf("delivery QA report cannot pass when observed QA contains a failed check")
	}
	return nil
}

func deliveryQAExecutionFailed(execution map[string]any) bool {
	if preview, ok := execution["preview"].(map[string]any); ok {
		if passed, known := preview["passed"].(bool); known && !passed {
			return true
		}
	}
	if semantic, ok := execution["semantic"].(map[string]any); ok {
		if passed, known := semantic["passed"].(bool); known && !passed {
			return true
		}
	}
	if runs, ok := execution["repository_runs"].([]any); ok {
		for _, run := range runs {
			entry, ok := run.(map[string]any)
			if !ok {
				continue
			}
			commands, ok := entry["commands"].([]any)
			if !ok {
				continue
			}
			for _, command := range commands {
				value, ok := command.(map[string]any)
				if !ok {
					continue
				}
				if passed, known := value["passed"].(bool); known && !passed {
					return true
				}
			}
		}
	}
	for _, captureKey := range []string{"screenshot", "screenshots"} {
		if capture, ok := execution[captureKey].(map[string]any); ok {
			if passed, known := capture["passed"].(bool); known && !passed {
				return true
			}
		}
		if captures, ok := execution[captureKey].([]any); ok {
			for _, item := range captures {
				capture, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if passed, known := capture["passed"].(bool); known && !passed {
					return true
				}
			}
		}
	}
	return false
}

func validateDeliverySummaryList(value map[string]any, field string) error {
	items, ok := value[field].([]any)
	if !ok || len(items) == 0 {
		return fmt.Errorf("delivery summary field %s must be a non-empty list of strings", field)
	}
	for _, item := range items {
		entry, ok := item.(string)
		if !ok || strings.TrimSpace(entry) == "" {
			return fmt.Errorf("delivery summary field %s must be a non-empty list of strings", field)
		}
	}
	return nil
}

// normalizeRepositoryImpact keeps repository impact as a typed matrix rather
// than collapsing it into prose. This is essential once a delivery plan spans
// a primary repository and supporting repositories: reviewers need to see the
// exact reference, role and action for each one.
func normalizeRepositoryImpact(plan map[string]any) error {
	values, ok := plan["repository_impact"].([]any)
	if !ok {
		return fmt.Errorf("delivery plan field repository_impact must be a list")
	}
	seen := map[string]struct{}{}
	normalized := make([]any, 0, len(values))
	for _, value := range values {
		entry, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("delivery plan field repository_impact must contain structured entries")
		}
		name, reference := strings.TrimSpace(stringAny(entry["name"])), strings.TrimSpace(stringAny(entry["reference"]))
		revision, role := strings.TrimSpace(stringAny(entry["revision"])), strings.ToLower(strings.TrimSpace(stringAny(entry["role"])))
		impact, notes := strings.ToLower(strings.TrimSpace(stringAny(entry["impact"]))), strings.TrimSpace(stringAny(entry["notes"]))
		if name == "" || reference == "" || revision == "" || notes == "" || (role != "primary" && role != "supporting") {
			return fmt.Errorf("delivery plan field repository_impact entries require name, reference, revision, role and notes")
		}
		if _, allowed := repositoryImpactStates[impact]; !allowed {
			return fmt.Errorf("delivery plan field repository_impact impact must be changes, consulted or untouched")
		}
		if _, duplicate := seen[reference]; duplicate {
			return fmt.Errorf("delivery plan field repository_impact must not repeat a repository reference")
		}
		seen[reference] = struct{}{}
		normalizedEntry := map[string]any{"name": name, "reference": reference, "revision": revision, "role": role, "impact": impact, "notes": notes}
		if rawScopes, present := entry["allowed_paths"]; present {
			scopes, err := NormalizeRepositoryAllowedPaths(rawScopes)
			if err != nil {
				return err
			}
			if len(scopes) > 0 {
				normalizedEntry["allowed_paths"] = scopes
			}
		}
		normalized = append(normalized, normalizedEntry)
	}
	plan["repository_impact"] = normalized
	return nil
}

// normalizeRepositoryAllowedPaths is the monorepo boundary. A plan may bind
// one repository to one or more explicit relative component roots, but it may
// never use an absolute path, traversal, .git, or secret-bearing environment
// file as a scope. The scope is advisory until the implementation patch is
// checked against it below; it is never inferred from model prose.
func NormalizeRepositoryAllowedPaths(raw any) ([]string, error) {
	var values []any
	switch typed := raw.(type) {
	case []any:
		values = typed
	case []string:
		values = make([]any, len(typed))
		for index, value := range typed {
			values[index] = value
		}
	default:
		return nil, fmt.Errorf("repository_impact allowed_paths must be a list")
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		path, ok := value.(string)
		path = strings.ReplaceAll(path, "\\", "/")
		if !ok || strings.HasPrefix(path, "/") {
			return nil, fmt.Errorf("repository_impact allowed_paths contains an unsafe path")
		}
		path = strings.Trim(path, " /")
		if path == "" || strings.Contains(path, "../") || path == ".." || path == "." || strings.HasPrefix(path, ".git/") || path == ".git" || strings.HasPrefix(path, ".env") {
			return nil, fmt.Errorf("repository_impact allowed_paths contains an unsafe path")
		}
		if _, duplicate := seen[path]; duplicate {
			return nil, fmt.Errorf("repository_impact allowed_paths must not repeat a path")
		}
		seen[path] = struct{}{}
		result = append(result, path)
	}
	sort.Strings(result)
	return result, nil
}

// ValidateDeliveryPlanTopology binds an otherwise well-formed plan to the
// immutable repository topology that was sent with the task. The model may
// describe impact, but it cannot introduce another repository, rewrite a
// revision, or omit a supporting dependency from review.
func ValidateDeliveryPlanTopology(plan map[string]any, delivery json.RawMessage) error {
	var input struct {
		RepositoryTopology []struct {
			Name                string   `json:"name"`
			Reference           string   `json:"reference"`
			Revision            string   `json:"revision"`
			Role                string   `json:"role"`
			Kind                string   `json:"kind"`
			AllowedPaths        []string `json:"allowed_paths"`
			StagehandConfigured bool     `json:"stagehand_configured"`
		} `json:"repository_topology"`
	}
	if err := json.Unmarshal(delivery, &input); err != nil {
		return fmt.Errorf("delivery input must be a JSON object")
	}
	entries, ok := plan["repository_impact"].([]any)
	if !ok {
		return fmt.Errorf("delivery plan field repository_impact must be normalized before topology validation")
	}
	if len(entries) != len(input.RepositoryTopology) {
		return fmt.Errorf("delivery plan repository_impact must declare every frozen repository")
	}
	expected := make(map[string]struct {
		name, revision, role string
		kind                 string
		allowedPaths         []string
		stagehandConfigured  bool
		metadataOnly         bool
	}, len(input.RepositoryTopology))
	for _, repository := range input.RepositoryTopology {
		reference := strings.TrimSpace(repository.Reference)
		if reference == "" {
			return fmt.Errorf("delivery repository topology is invalid")
		}
		if _, duplicate := expected[reference]; duplicate {
			return fmt.Errorf("delivery repository topology repeats a repository reference")
		}
		expected[reference] = struct {
			name, revision, role string
			kind                 string
			allowedPaths         []string
			stagehandConfigured  bool
			metadataOnly         bool
		}{
			strings.TrimSpace(repository.Name), strings.TrimSpace(repository.Revision), strings.ToLower(strings.TrimSpace(repository.Role)),
			strings.ToLower(strings.TrimSpace(repository.Kind)), append([]string(nil), repository.AllowedPaths...), repository.StagehandConfigured,
			strings.HasPrefix(strings.ToLower(reference), "github://"),
		}
	}
	for _, value := range entries {
		entry, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("delivery plan field repository_impact must contain structured entries")
		}
		reference := strings.TrimSpace(stringAny(entry["reference"]))
		repository, exists := expected[reference]
		if !exists || strings.TrimSpace(stringAny(entry["name"])) != repository.name || strings.TrimSpace(stringAny(entry["revision"])) != repository.revision || strings.ToLower(strings.TrimSpace(stringAny(entry["role"]))) != repository.role {
			return fmt.Errorf("delivery plan repository_impact does not match frozen repository topology")
		}
		if err := validateAllowedPathsWithin(entry["allowed_paths"], repository.allowedPaths); err != nil {
			return fmt.Errorf("delivery plan repository_impact %s: %w", reference, err)
		}
		if repository.metadataOnly && strings.EqualFold(strings.TrimSpace(stringAny(entry["impact"])), "changes") {
			return fmt.Errorf("delivery plan cannot mark a github-only repository as changed; register a local workspace checkpoint before implementation")
		}
		delete(expected, reference)
	}
	if len(expected) != 0 {
		return fmt.Errorf("delivery plan repository_impact omits a frozen repository")
	}
	stagehandRequiredRepositories := make(map[string]struct{})
	for _, repository := range input.RepositoryTopology {
		if strings.EqualFold(strings.TrimSpace(repository.Kind), "frontend") && repository.StagehandConfigured {
			stagehandRequiredRepositories[strings.TrimSpace(repository.Reference)] = struct{}{}
		}
	}
	if raw, present := plan["qa_execution_matrix"]; present {
		matrix, ok := raw.([]any)
		if !ok || len(matrix) != len(input.RepositoryTopology) {
			return fmt.Errorf("delivery plan qa_execution_matrix must declare every frozen repository")
		}
		// The impact pass above intentionally consumes expected while binding
		// the impact rows. Build an independent topology index for QA instead
		// of accidentally accepting or rejecting based on that consumed map.
		matrixExpected := make(map[string]struct {
			kind                string
			stagehandConfigured bool
		}, len(input.RepositoryTopology))
		for _, repository := range input.RepositoryTopology {
			matrixExpected[strings.TrimSpace(repository.Reference)] = struct {
				kind                string
				stagehandConfigured bool
			}{kind: strings.ToLower(strings.TrimSpace(repository.Kind)), stagehandConfigured: repository.StagehandConfigured}
		}
		seen := map[string]struct{}{}
		stagehandPlanned := make(map[string]struct{}, len(stagehandRequiredRepositories))
		for _, value := range matrix {
			entry, ok := value.(map[string]any)
			reference := strings.TrimSpace(stringAny(entry["repository_ref"]))
			if !ok || reference == "" {
				return fmt.Errorf("delivery plan qa_execution_matrix is invalid")
			}
			if _, duplicate := seen[reference]; duplicate {
				return fmt.Errorf("delivery plan qa_execution_matrix is invalid")
			}
			repository, exists := matrixExpected[reference]
			if !exists {
				return fmt.Errorf("delivery plan qa_execution_matrix does not match frozen repository topology")
			}
			if repository.kind == "frontend" && repository.stagehandConfigured {
				if entry["run_stagehand"] == true && entry["collect_evidence"] == true {
					stagehandPlanned[reference] = struct{}{}
				}
			}
			seen[reference] = struct{}{}
		}
		if len(seen) != len(matrixExpected) {
			return fmt.Errorf("delivery plan qa_execution_matrix omits a frozen repository")
		}
		for reference := range stagehandRequiredRepositories {
			if _, planned := stagehandPlanned[reference]; !planned {
				return fmt.Errorf("delivery plan must include Stagehand visual QA with evidence for every configured frontend")
			}
		}
	} else if len(stagehandRequiredRepositories) > 0 {
		return fmt.Errorf("delivery plan must include a QA execution matrix for every configured frontend")
	}
	if err := ValidateStagehandBrowserQAContract(plan, len(stagehandRequiredRepositories) > 0); err != nil {
		return err
	}
	return nil
}

// validateAllowedPathsWithin prevents a plan from widening a component scope
// that an operator already attached to the frozen repository context. A plan
// may narrow that maximum, but an omitted scope means whole-repository access
// and is therefore rejected when a maximum exists.
func validateAllowedPathsWithin(raw any, maximum []string) error {
	if len(maximum) == 0 {
		return nil
	}
	planned, err := NormalizeRepositoryAllowedPaths(raw)
	if err != nil {
		return err
	}
	if len(planned) == 0 {
		return fmt.Errorf("allowed_paths must remain within the operator-approved component scope")
	}
	for _, path := range planned {
		within := false
		for _, root := range maximum {
			if workspacePathWithin(root, path) {
				within = true
				break
			}
		}
		if !within {
			return fmt.Errorf("allowed_paths contains %q outside the operator-approved component scope", path)
		}
	}
	return nil
}

func workspacePathWithin(root, candidate string) bool {
	root = strings.Trim(strings.TrimSpace(filepath.ToSlash(root)), "/")
	candidate = strings.Trim(strings.TrimSpace(filepath.ToSlash(candidate)), "/")
	return root != "" && (candidate == root || strings.HasPrefix(candidate, root+"/"))
}

// ValidateStagehandBrowserQAContract ensures a configured browser harness is
// backed by at least one concrete, human-reviewable E2E case. A matrix flag
// alone is not evidence of a user journey; this keeps a Stagehand-capable
// frontend from degrading to a screenshot-only smoke by omission.
func ValidateStagehandBrowserQAContract(plan map[string]any, required bool) error {
	if err := ValidateApprovedBrowserQAPlan(plan); err != nil {
		return err
	}
	if !required {
		return nil
	}
	rawCases, present := plan["browser_qa_cases"]
	if !present {
		return fmt.Errorf("delivery plan must define at least one approved browser E2E case for a configured frontend")
	}
	cases, ok := rawCases.([]any)
	if !ok || len(cases) == 0 {
		return fmt.Errorf("delivery plan must define at least one approved browser E2E case for a configured frontend")
	}
	return nil
}

// ValidateDeliveryPlanContextCoverage makes the planner account for every
// frozen source, not only code repositories. Context can include decisions,
// client conversations, briefs and environments; accepting a plan that omits
// one would make its claimed context review non-auditable. Each entry in
// context_reviewed is the exact immutable source reference, never free prose.
func ValidateDeliveryPlanContextCoverage(plan map[string]any, delivery json.RawMessage) error {
	var input struct {
		ContextSources []struct {
			Reference  string `json:"reference"`
			Revision   string `json:"revision"`
			SnapshotAt string `json:"snapshot_at"`
		} `json:"context_sources"`
	}
	if err := json.Unmarshal(delivery, &input); err != nil {
		return fmt.Errorf("delivery input must be a JSON object")
	}
	reviewed, ok := plan["context_reviewed"].([]any)
	if !ok {
		return fmt.Errorf("delivery plan context_reviewed must be normalized before context validation")
	}
	if len(reviewed) != len(input.ContextSources) {
		return fmt.Errorf("delivery plan context_reviewed must declare every frozen context source")
	}
	expected := make(map[string]struct {
		revision   string
		snapshotAt string
	}, len(input.ContextSources))
	for _, source := range input.ContextSources {
		reference := strings.TrimSpace(source.Reference)
		if reference == "" {
			return fmt.Errorf("frozen delivery context source reference is invalid")
		}
		if _, duplicate := expected[reference]; duplicate {
			return fmt.Errorf("frozen delivery context repeats a source reference")
		}
		expected[reference] = struct {
			revision   string
			snapshotAt string
		}{revision: strings.TrimSpace(source.Revision), snapshotAt: strings.TrimSpace(source.SnapshotAt)}
	}
	for _, value := range reviewed {
		reference, ok := value.(string)
		reference = strings.TrimSpace(reference)
		if !ok || reference == "" {
			return fmt.Errorf("delivery plan context_reviewed must contain context references")
		}
		canonical, present := normalizeFrozenContextReference(reference, expected)
		if !present {
			return fmt.Errorf("delivery plan context_reviewed does not match frozen context")
		}
		delete(expected, canonical)
	}
	if len(expected) != 0 {
		return fmt.Errorf("delivery plan context_reviewed omits frozen context")
	}
	// Persist only canonical immutable references. Decorated references are
	// accepted solely when their revision and optional snapshot match exactly;
	// this lets a model show useful provenance without broadening its context.
	normalized := make([]any, 0, len(reviewed))
	for _, value := range reviewed {
		canonical, _ := normalizeFrozenContextReference(strings.TrimSpace(stringAny(value)), contextSourceMap(input.ContextSources))
		normalized = append(normalized, canonical)
	}
	plan["context_reviewed"] = normalized
	return nil
}

func contextSourceMap(sources []struct {
	Reference  string `json:"reference"`
	Revision   string `json:"revision"`
	SnapshotAt string `json:"snapshot_at"`
}) map[string]struct {
	revision   string
	snapshotAt string
} {
	result := make(map[string]struct {
		revision   string
		snapshotAt string
	}, len(sources))
	for _, source := range sources {
		reference := strings.TrimSpace(source.Reference)
		result[reference] = struct {
			revision   string
			snapshotAt string
		}{revision: strings.TrimSpace(source.Revision), snapshotAt: strings.TrimSpace(source.SnapshotAt)}
	}
	return result
}

// normalizeFrozenContextReference recognizes only an exact decorated form
// emitted by the planner: reference@revision optionally followed by the exact
// frozen snapshot timestamp or the fixed "(repository)" source-kind marker.
// It never accepts arbitrary prose or a revision prefix, so it cannot make
// unfrozen context look reviewed.
func normalizeFrozenContextReference(value string, expected map[string]struct {
	revision   string
	snapshotAt string
}) (string, bool) {
	if _, ok := expected[value]; ok {
		return value, true
	}
	for reference, source := range expected {
		if source.revision == "" || !strings.HasPrefix(value, reference+"@") {
			continue
		}
		suffix := strings.TrimSpace(strings.TrimPrefix(value, reference+"@"))
		if !strings.HasPrefix(suffix, source.revision) {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(suffix, source.revision))
		if rest == "" || (rest == "(repository)" && strings.HasPrefix(reference, "github://")) || (source.snapshotAt != "" && rest == "(snapshot_at "+source.snapshotAt+")") {
			return reference, true
		}
	}
	return "", false
}

func stringAny(value any) string {
	text, _ := value.(string)
	return text
}

// decodeJSONObject accepts a single JSON object even when a provider wraps it
// in a Markdown fence or a short introductory sentence. Providers frequently
// do that despite an explicit JSON-only instruction. We still parse and
// validate the object strictly below; prose alone, arrays and malformed JSON
// remain rejected so no ambiguous plan can be presented for human review.
func decodeJSONObject(content string) (map[string]any, bool) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return nil, false
	}
	// Some OpenAI-compatible providers serialize the entire JSON response as a
	// JSON string (for example: "{\\\"summary\\\":...}"). Unwrap exactly one
	// valid string layer before scanning. This is still strict: malformed text,
	// arrays and prose are rejected below.
	var wrapped string
	if json.Unmarshal([]byte(trimmed), &wrapped) == nil && strings.TrimSpace(wrapped) != "" {
		trimmed = strings.TrimSpace(wrapped)
	}
	for offset := 0; offset < len(trimmed); {
		next := strings.IndexByte(trimmed[offset:], '{')
		if next < 0 {
			return nil, false
		}
		start := offset + next
		end, found := jsonObjectEnd(trimmed[start:])
		if !found {
			return nil, false
		}
		var value map[string]any
		if err := json.Unmarshal([]byte(trimmed[start:start+end]), &value); err == nil && value != nil {
			return value, true
		}
		// A few OpenAI-compatible reasoning responses close the outer object
		// while omitting exactly one final array bracket.  This recovery adds no
		// semantic content: it is accepted only when the sole possible repair is
		// inserting `]` immediately before the terminal root `}` and the repaired
		// document passes the normal JSON decoder.  Any other malformed output is
		// still rejected rather than mining a nested object as if it were a plan.
		if strings.TrimSpace(trimmed[start+end:]) == "" {
			if repaired, ok := repairSingleTrailingArrayClosure(trimmed[start : start+end]); ok {
				if err := json.Unmarshal([]byte(repaired), &value); err == nil && value != nil {
					return value, true
				}
			}
		}
		if start == 0 {
			return nil, false
		}
		offset = start + 1
	}
	return nil, false
}

func repairSingleTrailingArrayClosure(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if len(value) < 3 || value[0] != '{' || value[len(value)-1] != '}' {
		return "", false
	}
	stack := make([]byte, 0, 4)
	inString, escaped := false, false
	for index := 0; index < len(value)-1; index++ {
		character := value[index]
		if inString {
			if escaped {
				escaped = false
			} else if character == '\\' {
				escaped = true
			} else if character == '"' {
				inString = false
			}
			continue
		}
		switch character {
		case '"':
			inString = true
		case '{', '[':
			stack = append(stack, character)
		case '}', ']':
			if len(stack) == 0 || (character == '}' && stack[len(stack)-1] != '{') || (character == ']' && stack[len(stack)-1] != '[') {
				return "", false
			}
			stack = stack[:len(stack)-1]
		}
	}
	if inString || escaped || len(stack) != 2 || stack[0] != '{' || stack[1] != '[' {
		return "", false
	}
	return value[:len(value)-1] + "]}", true
}

func jsonObjectEnd(value string) (int, bool) {
	depth := 0
	inString := false
	escaped := false
	for index := 0; index < len(value); index++ {
		character := value[index]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch character {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch character {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return index + 1, true
			}
			if depth < 0 {
				return 0, false
			}
		}
	}
	return 0, false
}
