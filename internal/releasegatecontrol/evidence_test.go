package releasegatecontrol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"events-stocks/internal/deliveryledger"
	"events-stocks/internal/deliverypolicy"
	"events-stocks/internal/projectvault"
	"events-stocks/internal/qaevidence"
	"events-stocks/internal/releasegate"
	"events-stocks/internal/securityevidence"
	"events-stocks/models"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var controlNow = time.Date(2026, time.August, 30, 18, 0, 0, 0, time.UTC)

func TestResolveStoredQAEvidenceLoadsOnlyExactMatrixAndCompletedTask(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	workItemID, taskID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	revisions := []releasegate.Revision{{Repository: "example/api", Branch: "main", SHA: strings.Repeat("a", 40)}}
	matrixDigest, _ := releasegate.RevisionMatrixDigest(revisions)
	branch := "itbem-agent/11111111-1111-4111-8111-111111111111"
	observation := qaevidence.Observation{
		SchemaVersion: qaevidence.SchemaVersion, TaskID: taskID.String(), MatrixDigest: matrixDigest, PreviewPassed: true,
		RepositoryExecutionOrder: []string{"workspace://api"},
		Repositories: []qaevidence.Repository{{Reference: "workspace://api", Branch: branch, Commands: []qaevidence.Command{
			{Index: 0, Phase: "validation", Kind: "unit", Passed: true},
			{Index: 1, Phase: "qa", Kind: compatibilityTestKind, Passed: true},
			{Index: 2, Phase: "qa", Kind: migrationsTestKind, Passed: true},
		}}},
	}
	event := controlQAEvent(t, workItemID, 7, observation, controlNow)
	mock.ExpectQuery(`SELECT \* FROM "delivery_events" WHERE work_item_id = \$1 AND event_type = \$2 AND subject_digest = \$3.*LIMIT \$4`).
		WithArgs(workItemID, deliveryledger.EventTypeQAObserved, matrixDigest, maxAssuranceObservations+1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "work_item_id", "sequence", "event_type", "dedupe_key", "subject_digest", "payload_json", "payload_digest", "actor_type", "actor_id", "occurred_at", "created_at"}).
			AddRow(event.ID, event.WorkItemID, event.Sequence, event.EventType, event.DedupeKey, event.SubjectDigest, event.PayloadJSON, event.PayloadDigest, event.ActorType, event.ActorID, event.OccurredAt, event.CreatedAt))
	mock.ExpectQuery(`SELECT .* FROM "automation_tasks" WHERE "automation_tasks"\."id" = \$1.*LIMIT \$2`).
		WithArgs(taskID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "delivery_work_item_id", "operation", "evidence_subject_digest", "status", "completed_at"}).
			AddRow(taskID, workItemID, "delivery.qa", matrixDigest, "completed", controlNow))
	mock.ExpectQuery(`SELECT .* FROM "delivery_context_snapshots" WHERE work_item_id = \$1 AND kind = \$2.*LIMIT \$3`).
		WithArgs(workItemID, "repository", maxDependencyRows+1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "work_item_id", "kind", "reference", "metadata_json"}).
			AddRow(uuid.Must(uuid.NewV4()), workItemID, "repository", "workspace://api", `{}`))
	mock.ExpectQuery(`SELECT dependency\..*FROM delivery_work_item_dependencies AS dependency.*WHERE dependency\.work_item_id = \$1.*LIMIT \$2`).
		WithArgs(workItemID, maxDependencyRows+1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "work_item_id", "depends_on_work_item_id", "state"}))
	input := releasegate.Input{Revisions: revisions, Policy: releasegate.Policy{RequiredTestKinds: []string{"unit"}}, Tests: []releasegate.TestEvidence{}}
	subject := publishedReleaseSubject{Revisions: revisions, RepositoryByReference: map[string]string{"workspace://api": "example/api"}, WorktreeBranchByReference: map[string]string{"workspace://api": branch}}
	resolved, err := resolveStoredQAEvidence(db, workItemID, input, subject, map[string][]string{"example/api": {"unit"}})
	if err != nil || len(resolved.Tests) != 1 || resolved.Tests[0].Kind != "unit" || resolved.Tests[0].Status != releasegate.StatusPassed || resolved.Tests[0].MatrixDigest != matrixDigest ||
		resolved.Compatibility.Status != releasegate.StatusPassed || resolved.Compatibility.MatrixDigest != matrixDigest || resolved.Migrations.Status != releasegate.StatusPassed || resolved.Migrations.MatrixDigest != matrixDigest ||
		resolved.Dependencies.Status != releasegate.StatusPassed || resolved.Dependencies.MatrixDigest != matrixDigest {
		t.Fatalf("exact persisted QA evidence was not resolved: %#v / %v", resolved.Tests, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func controlQAEvent(t *testing.T, workItemID uuid.UUID, sequence int64, observation qaevidence.Observation, occurredAt time.Time) models.DeliveryEvent {
	t.Helper()
	canonical, err := qaevidence.Canonical(observation)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"schema_version": qaevidence.SchemaVersion, "observation": canonical})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	return models.DeliveryEvent{
		ID: uuid.Must(uuid.NewV4()), WorkItemID: workItemID, Sequence: sequence, EventType: deliveryledger.EventTypeQAObserved,
		DedupeKey: workItemID.String() + ":qa-observation-v2:" + canonical.TaskID, SubjectDigest: canonical.MatrixDigest,
		PayloadJSON: string(payload), PayloadDigest: hex.EncodeToString(digest[:]), ActorType: "system", ActorID: "qa-runner/v2", OccurredAt: occurredAt, CreatedAt: occurredAt,
	}
}

func TestResolveStoredSecurityEvidenceLoadsOnlyExactMatrixAndCompletedQATask(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	workItemID, taskID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	revisions := []releasegate.Revision{{Repository: "example/api", Branch: "main", SHA: strings.Repeat("a", 40)}}
	matrixDigest, _ := releasegate.RevisionMatrixDigest(revisions)
	branch := "itbem-agent/11111111-1111-4111-8111-111111111111"
	observation := securityevidence.Observation{
		SchemaVersion: securityevidence.SchemaVersion, TaskID: taskID.String(), MatrixDigest: matrixDigest,
		Repositories: []securityevidence.Repository{{Reference: "workspace://api", Branch: branch, SecretScanPassed: true}},
	}
	event := controlSecurityEvent(t, workItemID, 8, observation, controlNow)
	mock.ExpectQuery(`SELECT \* FROM "delivery_events" WHERE work_item_id = \$1 AND event_type = \$2 AND subject_digest = \$3.*LIMIT \$4`).
		WithArgs(workItemID, deliveryledger.EventTypeSecurityObserved, matrixDigest, maxAssuranceObservations+1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "work_item_id", "sequence", "event_type", "dedupe_key", "subject_digest", "payload_json", "payload_digest", "actor_type", "actor_id", "occurred_at", "created_at"}).
			AddRow(event.ID, event.WorkItemID, event.Sequence, event.EventType, event.DedupeKey, event.SubjectDigest, event.PayloadJSON, event.PayloadDigest, event.ActorType, event.ActorID, event.OccurredAt, event.CreatedAt))
	mock.ExpectQuery(`SELECT .* FROM "automation_tasks" WHERE "automation_tasks"\."id" = \$1.*LIMIT \$2`).
		WithArgs(taskID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "delivery_work_item_id", "operation", "evidence_subject_digest", "status", "completed_at"}).
			AddRow(taskID, workItemID, "delivery.qa", matrixDigest, "completed", controlNow))
	input := releasegate.Input{
		Revisions: revisions,
		Security:  []releasegate.SecurityEvidence{{Repository: "attacker/repo", HeadSHA: strings.Repeat("f", 40), SecretScanPassed: true}},
	}
	subject := publishedReleaseSubject{
		Revisions: revisions, RepositoryByReference: map[string]string{"workspace://api": "example/api"},
		WorktreeBranchByReference: map[string]string{"workspace://api": branch},
	}
	resolved, err := resolveStoredSecurityEvidence(db, workItemID, input, subject)
	if err != nil || len(resolved.Security) != 1 || resolved.Security[0].Repository != "example/api" || resolved.Security[0].HeadSHA != revisions[0].SHA || !resolved.Security[0].SecretScanPassed {
		t.Fatalf("exact persisted security evidence was not resolved: %#v / %v", resolved.Security, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func controlSecurityEvent(t *testing.T, workItemID uuid.UUID, sequence int64, observation securityevidence.Observation, occurredAt time.Time) models.DeliveryEvent {
	t.Helper()
	canonical, err := securityevidence.Canonical(observation)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"schema_version": securityevidence.SchemaVersion, "observation": canonical})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	return models.DeliveryEvent{
		ID: uuid.Must(uuid.NewV4()), WorkItemID: workItemID, Sequence: sequence, EventType: deliveryledger.EventTypeSecurityObserved,
		DedupeKey: workItemID.String() + ":security-observation-v1:" + canonical.TaskID, SubjectDigest: canonical.MatrixDigest,
		PayloadJSON: string(payload), PayloadDigest: hex.EncodeToString(digest[:]), ActorType: "system", ActorID: "qa-runner/local-security/v1", OccurredAt: occurredAt, CreatedAt: occurredAt,
	}
}

func TestQAObservationSatisfiesOnlyTheRepositoryPolicyThatRanIt(t *testing.T) {
	matrixDigest := strings.Repeat("d", 64)
	subject := publishedReleaseSubject{
		Revisions:                 []releasegate.Revision{{Repository: "example/api", Branch: "main", SHA: strings.Repeat("a", 40)}, {Repository: "example/web", Branch: "trunk", SHA: strings.Repeat("b", 40)}},
		RepositoryByReference:     map[string]string{"workspace://api": "example/api", "workspace://web": "example/web"},
		WorktreeBranchByReference: map[string]string{"workspace://api": "itbem-agent/11111111-1111-4111-8111-111111111111", "workspace://web": "itbem-agent/22222222-2222-4222-8222-222222222222"},
	}
	observation := qaevidence.Observation{
		SchemaVersion: qaevidence.SchemaVersion, TaskID: "33333333-3333-4333-8333-333333333333", MatrixDigest: matrixDigest, PreviewPassed: true,
		RepositoryExecutionOrder: []string{"workspace://api", "workspace://web"},
		Repositories: []qaevidence.Repository{
			{Reference: "workspace://api", Branch: subject.WorktreeBranchByReference["workspace://api"], Commands: []qaevidence.Command{{Index: 0, Phase: "validation", Kind: "unit", Passed: true}, {Index: 1, Phase: "qa", Kind: "contract", Passed: true}}},
			{Reference: "workspace://web", Branch: subject.WorktreeBranchByReference["workspace://web"], Commands: []qaevidence.Command{{Index: 0, Phase: "validation", Kind: "unit", Passed: true}}},
		},
	}
	required := map[string][]string{"example/api": {"contract", "unit"}, "example/web": {"unit"}}
	tests, err := testsFromQAObservation([]string{"contract", "unit"}, required, subject, observation)
	if err != nil || len(tests) != 2 || tests[0].Kind != "contract" || tests[0].Status != releasegate.StatusPassed || tests[1].Status != releasegate.StatusPassed {
		t.Fatalf("exact per-repository evidence was not resolved: %#v / %v", tests, err)
	}

	// Running contract on web cannot replace the API contract requirement.
	wrongRepository := observation
	wrongRepository.Repositories = append([]qaevidence.Repository(nil), observation.Repositories...)
	wrongRepository.Repositories[0].Commands = []qaevidence.Command{{Index: 0, Phase: "validation", Kind: "unit", Passed: true}}
	wrongRepository.Repositories[1].Commands = append(wrongRepository.Repositories[1].Commands, qaevidence.Command{Index: 1, Phase: "qa", Kind: "contract", Passed: true})
	tests, err = testsFromQAObservation([]string{"contract", "unit"}, required, subject, wrongRepository)
	if err != nil || len(tests) != 1 || tests[0].Kind != "unit" {
		t.Fatalf("a test from the wrong repository satisfied policy: %#v / %v", tests, err)
	}

	failed := observation
	failed.Repositories = append([]qaevidence.Repository(nil), observation.Repositories...)
	failed.Repositories[0].Commands = append([]qaevidence.Command(nil), observation.Repositories[0].Commands...)
	failed.Repositories[0].Commands[1].Passed = false
	tests, err = testsFromQAObservation([]string{"contract", "unit"}, required, subject, failed)
	if err != nil || tests[0].Status != releasegate.StatusFailed || tests[1].Status != releasegate.StatusPassed {
		t.Fatalf("failed repository command did not fail its named gate: %#v / %v", tests, err)
	}
}

func TestQAObservationRejectsIncompleteOrAmbiguousPublicationEvidence(t *testing.T) {
	subject := publishedReleaseSubject{
		RepositoryByReference:     map[string]string{"workspace://api": "example/api"},
		WorktreeBranchByReference: map[string]string{"workspace://api": "itbem-agent/11111111-1111-4111-8111-111111111111"},
	}
	observation := qaevidence.Observation{
		SchemaVersion: qaevidence.SchemaVersion, TaskID: "33333333-3333-4333-8333-333333333333", MatrixDigest: strings.Repeat("d", 64),
		RepositoryExecutionOrder: []string{"workspace://other"},
		Repositories:             []qaevidence.Repository{{Reference: "workspace://other", Branch: "itbem-agent/11111111-1111-4111-8111-111111111111", Commands: []qaevidence.Command{{Index: 0, Phase: "qa", Kind: "unit", Passed: true}}}},
	}
	if _, err := testsFromQAObservation([]string{"unit"}, map[string][]string{"example/api": {"unit"}}, subject, observation); err == nil {
		t.Fatal("QA evidence for a different workspace repository was accepted")
	}
	observation.RepositoryExecutionOrder[0], observation.Repositories[0].Reference = "workspace://api", "workspace://api"
	observation.Repositories[0].Branch = "itbem-agent/99999999-9999-4999-8999-999999999999"
	if _, err := testsFromQAObservation([]string{"unit"}, map[string][]string{"example/api": {"unit"}}, subject, observation); err == nil {
		t.Fatal("QA evidence for a different reviewed worktree branch was accepted")
	}
}

func TestAssuranceMatrixEvidenceRequiresNamedCommandInEveryRepository(t *testing.T) {
	revisions := []releasegate.Revision{{Repository: "example/api", Branch: "main", SHA: strings.Repeat("a", 40)}, {Repository: "example/web", Branch: "trunk", SHA: strings.Repeat("b", 40)}}
	matrixDigest, _ := releasegate.RevisionMatrixDigest(revisions)
	subject := publishedReleaseSubject{
		Revisions:                 revisions,
		RepositoryByReference:     map[string]string{"workspace://api": "example/api", "workspace://web": "example/web"},
		WorktreeBranchByReference: map[string]string{"workspace://api": "itbem-agent/11111111-1111-4111-8111-111111111111", "workspace://web": "itbem-agent/22222222-2222-4222-8222-222222222222"},
	}
	observation := qaevidence.Observation{
		SchemaVersion: qaevidence.SchemaVersion, TaskID: "33333333-3333-4333-8333-333333333333", MatrixDigest: matrixDigest,
		RepositoryExecutionOrder: []string{"workspace://api", "workspace://web"},
		Repositories: []qaevidence.Repository{
			{Reference: "workspace://api", Branch: subject.WorktreeBranchByReference["workspace://api"], Commands: []qaevidence.Command{{Index: 0, Phase: "qa", Kind: compatibilityTestKind, Passed: true}}},
			{Reference: "workspace://web", Branch: subject.WorktreeBranchByReference["workspace://web"], Commands: []qaevidence.Command{{Index: 0, Phase: "qa", Kind: compatibilityTestKind, Passed: true}}},
		},
	}
	evidence, err := namedMatrixEvidenceFromQA(compatibilityTestKind, subject, observation)
	if err != nil || evidence.Status != releasegate.StatusPassed || evidence.MatrixDigest != matrixDigest {
		t.Fatalf("complete passing compatibility evidence was rejected: %#v / %v", evidence, err)
	}
	failed := observation
	failed.Repositories = append([]qaevidence.Repository(nil), observation.Repositories...)
	failed.Repositories[1].Commands = append([]qaevidence.Command(nil), observation.Repositories[1].Commands...)
	failed.Repositories[1].Commands[0].Passed = false
	evidence, err = namedMatrixEvidenceFromQA(compatibilityTestKind, subject, failed)
	if err != nil || evidence.Status != releasegate.StatusFailed {
		t.Fatalf("one failed repository did not fail matrix evidence: %#v / %v", evidence, err)
	}
	missing := observation
	missing.Repositories = append([]qaevidence.Repository(nil), observation.Repositories...)
	missing.Repositories[1].Commands = nil
	evidence, err = namedMatrixEvidenceFromQA(compatibilityTestKind, subject, missing)
	if err != nil || evidence.Status != "" || evidence.MatrixDigest != "" {
		t.Fatalf("missing repository command became assurance evidence: %#v / %v", evidence, err)
	}
	wrongBranch := observation
	wrongBranch.Repositories = append([]qaevidence.Repository(nil), observation.Repositories...)
	wrongBranch.Repositories[0].Branch = subject.WorktreeBranchByReference["workspace://web"]
	if _, err := namedMatrixEvidenceFromQA(compatibilityTestKind, subject, wrongBranch); err == nil {
		t.Fatal("assurance evidence from a different reviewed branch was accepted")
	}
	staleMatrix := observation
	staleMatrix.MatrixDigest = strings.Repeat("f", 64)
	if _, err := namedMatrixEvidenceFromQA(compatibilityTestKind, subject, staleMatrix); err == nil {
		t.Fatal("assurance evidence for a different revision matrix was accepted")
	}
}

func TestDependencyEvidenceRequiresReleasedTasksAndQAOrderThatRespectsFrozenDAG(t *testing.T) {
	workItemID := uuid.Must(uuid.NewV4())
	revisions := []releasegate.Revision{{Repository: "example/api", Branch: "main", SHA: strings.Repeat("a", 40)}, {Repository: "example/web", Branch: "trunk", SHA: strings.Repeat("b", 40)}}
	matrixDigest, _ := releasegate.RevisionMatrixDigest(revisions)
	subject := publishedReleaseSubject{
		Revisions:                 revisions,
		RepositoryByReference:     map[string]string{"workspace://api": "example/api", "workspace://web": "example/web"},
		WorktreeBranchByReference: map[string]string{"workspace://api": "itbem-agent/11111111-1111-4111-8111-111111111111", "workspace://web": "itbem-agent/22222222-2222-4222-8222-222222222222"},
	}
	observation := qaevidence.Observation{
		SchemaVersion: qaevidence.SchemaVersion, TaskID: "33333333-3333-4333-8333-333333333333", MatrixDigest: matrixDigest,
		RepositoryExecutionOrder: []string{"workspace://api", "workspace://web"},
		Repositories: []qaevidence.Repository{
			{Reference: "workspace://api", Branch: subject.WorktreeBranchByReference["workspace://api"], Commands: []qaevidence.Command{}},
			{Reference: "workspace://web", Branch: subject.WorktreeBranchByReference["workspace://web"], Commands: []qaevidence.Command{}},
		},
	}
	snapshots := []models.DeliveryContextSnapshot{
		{ID: uuid.Must(uuid.NewV4()), WorkItemID: workItemID, Kind: "repository", Reference: "workspace://api", MetadataJSON: `{}`},
		{ID: uuid.Must(uuid.NewV4()), WorkItemID: workItemID, Kind: "repository", Reference: "workspace://web", MetadataJSON: `{"depends_on_repositories":["workspace://api"]}`},
	}
	releasedDependency := workItemDependencyState{ID: uuid.Must(uuid.NewV4()), WorkItemID: workItemID, DependsOnWorkItemID: uuid.Must(uuid.NewV4()), State: "released"}
	evidence, err := dependencyEvidenceFromControlPlane(workItemID, subject, observation, snapshots, []workItemDependencyState{releasedDependency})
	if err != nil || evidence.Status != releasegate.StatusPassed || evidence.MatrixDigest != matrixDigest {
		t.Fatalf("valid dependency evidence was rejected: %#v / %v", evidence, err)
	}
	reversed := observation
	reversed.RepositoryExecutionOrder = []string{"workspace://web", "workspace://api"}
	evidence, err = dependencyEvidenceFromControlPlane(workItemID, subject, reversed, snapshots, []workItemDependencyState{releasedDependency})
	if err != nil || evidence.Status != releasegate.StatusFailed {
		t.Fatalf("QA order that violated the repository DAG did not fail: %#v / %v", evidence, err)
	}
	pendingDependency := releasedDependency
	pendingDependency.State = "release_review"
	evidence, err = dependencyEvidenceFromControlPlane(workItemID, subject, observation, snapshots, []workItemDependencyState{pendingDependency})
	if err != nil || evidence.Status != releasegate.StatusFailed {
		t.Fatalf("unreleased work-item dependency did not fail: %#v / %v", evidence, err)
	}
	cyclic := append([]models.DeliveryContextSnapshot(nil), snapshots...)
	cyclic[0].MetadataJSON = `{"depends_on_repositories":["workspace://web"]}`
	if _, err := dependencyEvidenceFromControlPlane(workItemID, subject, observation, cyclic, nil); err == nil {
		t.Fatal("cyclic frozen repository dependencies were accepted")
	}
	missingWorkItem := releasedDependency
	missingWorkItem.State = ""
	if _, err := dependencyEvidenceFromControlPlane(workItemID, subject, observation, snapshots, []workItemDependencyState{missingWorkItem}); err == nil {
		t.Fatal("dependency row whose prerequisite work item is missing was accepted")
	}
}

func TestQATaskProvenanceRequiresCompletedExactTaskAndMatrix(t *testing.T) {
	workItemID, taskID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	completedAt := controlNow
	task := models.AutomationTask{ID: taskID, DeliveryWorkItemID: &workItemID, Operation: "delivery.qa", Status: "completed", EvidenceSubjectDigest: strings.Repeat("a", 64), CompletedAt: &completedAt}
	if err := validateQATaskProvenance(task, workItemID, taskID, strings.Repeat("a", 64), completedAt); err != nil {
		t.Fatalf("matching QA task provenance was rejected: %v", err)
	}
	mutations := []func(*models.AutomationTask){
		func(value *models.AutomationTask) { value.ID = uuid.Must(uuid.NewV4()) },
		func(value *models.AutomationTask) { value.Status = "running" },
		func(value *models.AutomationTask) { value.EvidenceSubjectDigest = strings.Repeat("b", 64) },
		func(value *models.AutomationTask) { later := completedAt.Add(time.Second); value.CompletedAt = &later },
	}
	for _, mutate := range mutations {
		forged := task
		mutate(&forged)
		if err := validateQATaskProvenance(forged, workItemID, taskID, strings.Repeat("a", 64), completedAt); err == nil {
			t.Fatalf("forged QA task provenance was accepted: %#v", forged)
		}
	}
}

func TestSecurityObservationResolvesEveryExactRepositorySHA(t *testing.T) {
	revisions := []releasegate.Revision{{Repository: "example/api", Branch: "main", SHA: strings.Repeat("a", 40)}, {Repository: "example/web", Branch: "trunk", SHA: strings.Repeat("b", 40)}}
	apiBranch := "itbem-agent/11111111-1111-4111-8111-111111111111"
	webBranch := "itbem-agent/22222222-2222-4222-8222-222222222222"
	subject := publishedReleaseSubject{
		Revisions: revisions,
		RepositoryByReference: map[string]string{
			"workspace://api": "example/api",
			"workspace://web": "example/web",
		},
		WorktreeBranchByReference: map[string]string{
			"workspace://api": apiBranch,
			"workspace://web": webBranch,
		},
	}
	observation := securityevidence.Observation{
		SchemaVersion: securityevidence.SchemaVersion, TaskID: "33333333-3333-4333-8333-333333333333", MatrixDigest: strings.Repeat("d", 64),
		Repositories: []securityevidence.Repository{
			{Reference: "workspace://web", Branch: webBranch, SecretScanPassed: true},
			{Reference: "workspace://api", Branch: apiBranch, SecretScanPassed: false, HighFindings: 2, CriticalFindings: 1},
		},
	}
	security, err := securityFromObservation(revisions, subject, observation)
	if err != nil || len(security) != 2 || security[0].Repository != "example/api" || security[0].HighFindings != 2 || security[0].CriticalFindings != 1 || security[0].SecretScanPassed {
		t.Fatalf("exact security evidence was not resolved: %#v / %v", security, err)
	}
	stale := observation
	stale.Repositories = append([]securityevidence.Repository(nil), observation.Repositories...)
	stale.Repositories[0].Branch = apiBranch
	if _, err := securityFromObservation(revisions, subject, stale); err == nil {
		t.Fatal("security evidence for a different reviewed branch was accepted")
	}
	if _, err := securityFromObservation(revisions, subject, observationWithSecuritySubset(observation)); err == nil {
		t.Fatal("incomplete security repository set was accepted")
	}
}

func observationWithSecuritySubset(observation securityevidence.Observation) securityevidence.Observation {
	observation.Repositories = observation.Repositories[:1]
	return observation
}

func TestSecurityTaskProvenanceRequiresCompletedQATask(t *testing.T) {
	workItemID, taskID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	completedAt := controlNow
	task := models.AutomationTask{ID: taskID, DeliveryWorkItemID: &workItemID, Operation: "delivery.qa", Status: "completed", EvidenceSubjectDigest: strings.Repeat("a", 64), CompletedAt: &completedAt}
	if err := validateSecurityTaskProvenance(task, workItemID, taskID, strings.Repeat("a", 64), completedAt); err != nil {
		t.Fatalf("matching security task provenance was rejected: %v", err)
	}
	task.Operation = "delivery.release_gate"
	if err := validateSecurityTaskProvenance(task, workItemID, taskID, strings.Repeat("a", 64), completedAt); err == nil {
		t.Fatal("security evidence from a non-QA task was accepted")
	}
}

func TestControlPlaneQueriesUseBoundedCaseInsensitiveRepositoryScopes(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	project := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: uuid.Must(uuid.NewV4())}
	mock.ExpectQuery(`SELECT DISTINCT ON \(LOWER\(repository_reference\)\).*FROM "delivery_project_vault_revisions"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	if vaults, err := loadLatestVaults(db, project.ID, []string{"github://Example/API"}); err != nil || len(vaults) != 0 {
		t.Fatalf("bounded Vault query failed: %#v / %v", vaults, err)
	}
	mock.ExpectQuery(`SELECT \* FROM "delivery_policy_revisions".*LOWER\(repository_reference\).*LIMIT`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	revisions, decisions, err := loadPolicyLedger(db, project, "change-set:query", []string{"github://Example/API"})
	if err != nil || len(revisions) != 0 || len(decisions) != 0 {
		t.Fatalf("bounded policy query failed: %#v / %#v / %v", revisions, decisions, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadPublishedRevisionMatrixUsesPlanAndConsumedPublicationGrants(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	workItemID := uuid.Must(uuid.NewV4())
	apiChangeID, webChangeID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	apiGrantID, webGrantID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	item := models.DeliveryWorkItem{
		ID:       workItemID,
		PlanJSON: `{"repository_impact":[{"reference":"workspace://api","impact":"changes"},{"reference":"workspace://web","impact":"changes"},{"reference":"workspace://docs","impact":"consulted"}]}`,
	}
	changeRows := sqlmock.NewRows([]string{"id", "work_item_id", "repository_ref", "branch", "commit_sha", "review_type", "pull_request_url", "metadata_json", "created_by", "created_at"}).
		AddRow(webChangeID, workItemID, "workspace://web", "itbem-agent/22222222-2222-4222-8222-222222222222", strings.Repeat("b", 40), "pull_request", "https://github.com/Example/Web/pull/8", publicationMetadata(webGrantID, strings.Repeat("2", 40), "Example/Web", "trunk"), "itbem-github-app", controlNow).
		AddRow(apiChangeID, workItemID, "workspace://api", "itbem-agent/11111111-1111-4111-8111-111111111111", strings.Repeat("a", 40), "pull_request", "https://github.com/Example/API/pull/7", publicationMetadata(apiGrantID, strings.Repeat("1", 40), "Example/API", "main"), "itbem-github-app", controlNow)
	mock.ExpectQuery(`SELECT \* FROM "delivery_change_sets".*ORDER BY created_at DESC, id DESC LIMIT`).WillReturnRows(changeRows)
	grantRows := sqlmock.NewRows([]string{"id", "work_item_id", "repository_ref", "base_sha", "git_hub_repository", "review_diff_sha256", "branch", "revoked_by", "revoked_at"}).
		AddRow(apiGrantID, workItemID, "workspace://api", strings.Repeat("1", 40), "example/api", strings.Repeat("c", 64), "itbem-agent/11111111-1111-4111-8111-111111111111", "itbem-github-app", controlNow).
		AddRow(webGrantID, workItemID, "workspace://web", strings.Repeat("2", 40), "example/web", strings.Repeat("d", 64), "itbem-agent/22222222-2222-4222-8222-222222222222", "itbem-github-app", controlNow)
	mock.ExpectQuery(`SELECT \* FROM "delivery_publication_grants" WHERE id IN`).WillReturnRows(grantRows)

	revisions, err := loadPublishedRevisionMatrix(db, item)
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 2 || revisions[0].Repository != "example/api" || revisions[0].Branch != "main" || revisions[0].SHA != strings.Repeat("a", 40) || revisions[1].Repository != "example/web" || revisions[1].Branch != "trunk" {
		t.Fatalf("unexpected authoritative publication matrix: %#v", revisions)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDiscardUntrustedAssuranceRemovesReleaseWorkerClaims(t *testing.T) {
	input := releasegate.Input{
		Tests:         []releasegate.TestEvidence{{Kind: "unit", MatrixDigest: strings.Repeat("a", 64), Status: releasegate.StatusPassed}},
		Security:      []releasegate.SecurityEvidence{{Repository: "example/api", HeadSHA: strings.Repeat("a", 40), SecretScanPassed: true}},
		Compatibility: releasegate.MatrixEvidence{MatrixDigest: strings.Repeat("a", 64), Status: releasegate.StatusPassed},
		Migrations:    releasegate.MatrixEvidence{MatrixDigest: strings.Repeat("a", 64), Status: releasegate.StatusPassed},
		Dependencies:  releasegate.MatrixEvidence{MatrixDigest: strings.Repeat("a", 64), Status: releasegate.StatusPassed},
		Environment:   releasegate.MatrixEvidence{MatrixDigest: strings.Repeat("a", 64), Status: releasegate.StatusPassed},
		Recovery:      releasegate.RecoveryEvidence{MatrixDigest: strings.Repeat("a", 64), Classification: releasegate.RecoveryRollback, Evaluated: true},
	}
	discardUntrustedAssurance(&input)
	if len(input.Tests) != 0 || len(input.Security) != 0 || input.Compatibility.Status != "" || input.Migrations.Status != "" || input.Dependencies.Status != "" || input.Environment.Status != "" || input.Recovery.Evaluated {
		t.Fatalf("release worker assurance claim survived control-plane reset: %#v", input)
	}
}

func TestSameRevisionMatrixRejectsChangedRepositoryBranchOrSHA(t *testing.T) {
	want := []releasegate.Revision{
		{Repository: "example/api", Branch: "main", SHA: strings.Repeat("a", 40)},
		{Repository: "example/web", Branch: "trunk", SHA: strings.Repeat("b", 40)},
	}
	reordered := []releasegate.Revision{want[1], want[0]}
	if !sameRevisionMatrix(want, reordered) {
		t.Fatal("canonical matrix order changed the release identity")
	}
	for _, forged := range [][]releasegate.Revision{
		{{Repository: "attacker/api", Branch: "main", SHA: strings.Repeat("a", 40)}, want[1]},
		{{Repository: "example/api", Branch: "production", SHA: strings.Repeat("a", 40)}, want[1]},
		{{Repository: "example/api", Branch: "main", SHA: strings.Repeat("c", 40)}, want[1]},
		{want[0]},
	} {
		if sameRevisionMatrix(want, forged) {
			t.Fatalf("changed release matrix was accepted: %#v", forged)
		}
	}
}

func publicationMetadata(grantID uuid.UUID, baseSHA, repository, targetBranch string) string {
	encoded, _ := json.Marshal(map[string]any{
		"publication_grant_id": grantID.String(), "base_sha": baseSHA, "remote_repository": repository,
		"target_branch": targetBranch, "branch_published": true, "verification_source": "itbem-github-app",
	})
	return string(encoded)
}

func TestResolveStoredEvidenceReplacesCandidateClaimsForMultiRepoMatrix(t *testing.T) {
	project := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: uuid.Must(uuid.NewV4())}
	revisions := []releasegate.Revision{
		{Repository: "example/api", Branch: "main", SHA: strings.Repeat("a", 40)},
		{Repository: "example/web", Branch: "main", SHA: strings.Repeat("b", 40)},
	}
	input := releasegate.Input{
		SchemaVersion: releasegate.SchemaVersion, Action: releasegate.ActionRelease, ChangeSetID: "change-set:control",
		Revisions: revisions,
		Policy:    releasegate.Policy{Resolved: true, Digest: strings.Repeat("f", 64), RequiredTestKinds: []string{"candidate"}, Repositories: []releasegate.RepositoryPolicyEvidence{{Repository: "attacker/repo", Digest: strings.Repeat("f", 64), Resolved: true, ActionAllowed: true, BranchAllowed: true}}},
		Vault:     []releasegate.VaultEvidence{{Repository: "attacker/repo", HeadSHA: strings.Repeat("f", 40), RevisionID: "candidate", Reconciled: true}},
	}
	policyRevision, decision := controlPolicy(t, project, []string{"main"})
	vaults := []models.DeliveryProjectVaultRevision{
		controlVault(t, project.ID, "github://Example/Api", revisions[0].SHA),
		controlVault(t, project.ID, "github://EXAMPLE/Web", strings.Repeat("c", 40)),
	}
	repositories, _, err := repositoryReferences(revisions)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveStoredEvidence(input, project, repositories, vaults, []models.DeliveryPolicyRevision{policyRevision}, []models.DeliveryPolicyDecision{decision}, controlNow)
	if err != nil {
		t.Fatal(err)
	}
	matrixDigest, _ := releasegate.RevisionMatrixDigest(revisions)
	if !resolved.Policy.Resolved || len(resolved.Policy.Repositories) != 2 || strings.Join(resolved.Policy.RequiredTestKinds, ",") != "contract,unit" || len(resolved.Vault) != 2 ||
		!resolved.Recovery.Evaluated || resolved.Recovery.Classification != releasegate.RecoveryRollback || resolved.Recovery.MatrixDigest != matrixDigest || resolved.Recovery.HumanApproved {
		t.Fatalf("authoritative multi-repo evidence was not resolved: %#v", resolved)
	}
	if resolved.Policy.Digest == input.Policy.Digest || resolved.Policy.Repositories[0].Repository == "attacker/repo" || resolved.Vault[0].Repository == "attacker/repo" {
		t.Fatalf("candidate policy or Vault claim survived control-plane resolution: %#v", resolved)
	}
	if !resolved.Vault[0].Reconciled || resolved.Vault[1].Reconciled {
		t.Fatalf("Vault reconciliation was not bound to each exact SHA: %#v", resolved.Vault)
	}
	decisionResult := releasegate.Evaluate(resolved)
	if !hasControlReason(decisionResult, "vault_evidence_stale") {
		t.Fatalf("stale repository Vault was not visible to Gatekeeper: %#v", decisionResult)
	}
}

func TestCompositeRecoveryClassificationUsesMostConstrainedRepositoryPolicy(t *testing.T) {
	values := map[string]releasegate.RecoveryClassification{
		"example/api": releasegate.RecoveryRollback,
		"example/web": releasegate.RecoveryRollForward,
	}
	classification, err := compositeRecoveryClassification(values)
	if err != nil || classification != releasegate.RecoveryRollForward {
		t.Fatalf("composite recovery did not retain the most constrained strategy: %s / %v", classification, err)
	}
	values["example/data"] = releasegate.RecoveryExpandContract
	classification, err = compositeRecoveryClassification(values)
	if err != nil || classification != releasegate.RecoveryExpandContract {
		t.Fatalf("expand/contract did not dominate a simple recovery: %s / %v", classification, err)
	}
	values["example/ledger"] = releasegate.RecoveryIrreversible
	classification, err = compositeRecoveryClassification(values)
	if err != nil || classification != releasegate.RecoveryIrreversible {
		t.Fatalf("irreversible component did not classify the composite as irreversible: %s / %v", classification, err)
	}
	values["example/broken"] = "pretend_rollback"
	if _, err := compositeRecoveryClassification(values); err == nil {
		t.Fatal("unknown recovery strategy was accepted")
	}
}

func TestResolveStoredEvidenceKeepsMissingPolicyAndVaultBlocked(t *testing.T) {
	project := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: uuid.Must(uuid.NewV4())}
	revision := releasegate.Revision{Repository: "example/api", Branch: "main", SHA: strings.Repeat("a", 40)}
	input := releasegate.Input{SchemaVersion: releasegate.SchemaVersion, Action: releasegate.ActionRelease, ChangeSetID: "change-set:missing", Revisions: []releasegate.Revision{revision}}
	repositories, _, _ := repositoryReferences(input.Revisions)
	resolved, err := resolveStoredEvidence(input, project, repositories, nil, nil, nil, controlNow)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Policy.Resolved || len(resolved.Vault) != 0 || len(resolved.Policy.Repositories) != 1 {
		t.Fatalf("missing authoritative evidence was invented: %#v", resolved)
	}
	decision := releasegate.Evaluate(resolved)
	for _, code := range []string{"repository_policy_unresolved", "policy_action_not_allowed", "target_branch_not_allowed", "vault_evidence_missing"} {
		if !hasControlReason(decision, code) {
			t.Fatalf("missing control-plane evidence reason %s: %#v", code, decision)
		}
	}
}

func TestResolveStoredEvidenceBlocksBranchOutsidePolicy(t *testing.T) {
	project := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: uuid.Must(uuid.NewV4())}
	revision := releasegate.Revision{Repository: "example/api", Branch: "release/v2", SHA: strings.Repeat("a", 40)}
	input := releasegate.Input{SchemaVersion: releasegate.SchemaVersion, Action: releasegate.ActionRelease, ChangeSetID: "change-set:branch", Revisions: []releasegate.Revision{revision}}
	policyRevision, decision := controlPolicy(t, project, []string{"main"})
	repositories, _, _ := repositoryReferences(input.Revisions)
	resolved, err := resolveStoredEvidence(input, project, repositories, []models.DeliveryProjectVaultRevision{controlVault(t, project.ID, "github://example/api", revision.SHA)}, []models.DeliveryPolicyRevision{policyRevision}, []models.DeliveryPolicyDecision{decision}, controlNow)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Policy.Resolved || resolved.Policy.Repositories[0].BranchAllowed {
		t.Fatalf("branch outside policy was authorized: %#v", resolved.Policy)
	}
	if decision := releasegate.Evaluate(resolved); !hasControlReason(decision, "target_branch_not_allowed") {
		t.Fatalf("branch policy block was not explained: %#v", decision)
	}
}

func TestResolveStoredEvidenceRejectsTamperedVaultManifest(t *testing.T) {
	project := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: uuid.Must(uuid.NewV4())}
	revision := releasegate.Revision{Repository: "example/api", Branch: "main", SHA: strings.Repeat("a", 40)}
	input := releasegate.Input{SchemaVersion: releasegate.SchemaVersion, Action: releasegate.ActionRelease, ChangeSetID: "change-set:tampered", Revisions: []releasegate.Revision{revision}}
	vault := controlVault(t, project.ID, "github://example/api", revision.SHA)
	vault.ManifestJSON = `{"schema_version":1,"scope":"repository"}`
	repositories, _, _ := repositoryReferences(input.Revisions)
	if _, err := resolveStoredEvidence(input, project, repositories, []models.DeliveryProjectVaultRevision{vault}, nil, nil, controlNow); err == nil {
		t.Fatal("tampered Vault content was accepted by its stored digest")
	}
}

func TestValidateVaultProvenanceRowsBindsApprovedOnboarding(t *testing.T) {
	projectID := uuid.Must(uuid.NewV4())
	vault := controlVault(t, projectID, "github://example/api", strings.Repeat("a", 40))
	approvedAt := vault.PublishedAt
	onboarding := models.DeliveryRepositoryOnboarding{
		ID: vault.SourceOnboardingID, ProjectID: projectID, RepositoryReference: vault.RepositoryReference,
		Revision: vault.Revision, Status: "approved", VaultSHA256: vault.ContentSHA256,
		ApprovedBy: vault.PublishedBy, ApprovedAt: &approvedAt,
	}
	if err := validateVaultProvenanceRows([]models.DeliveryProjectVaultRevision{vault}, []models.DeliveryRepositoryOnboarding{onboarding}); err != nil {
		t.Fatalf("matching approved Vault provenance was rejected: %v", err)
	}
	onboarding.Revision = strings.Repeat("b", 40)
	if err := validateVaultProvenanceRows([]models.DeliveryProjectVaultRevision{vault}, []models.DeliveryRepositoryOnboarding{onboarding}); err == nil {
		t.Fatal("Vault provenance for another repository revision was accepted")
	}
}

func controlPolicy(t *testing.T, project models.DeliveryProject, branches []string) (models.DeliveryPolicyRevision, models.DeliveryPolicyDecision) {
	t.Helper()
	mode, method := deliverypolicy.ModeRelease, "squash"
	tests, health := []string{"unit", "contract"}, []string{"health"}
	workflow, environment, recovery := ".github/workflows/deploy.yml", "production", string(releasegate.RecoveryRollback)
	secretReferences, variableReferences := []string{}, []string{}
	patch := deliverypolicy.Patch{
		Mode: &mode, MergeMethod: &method, RequiredTestKinds: &tests, AllowedTargetBranches: &branches,
		DeploymentWorkflow: &workflow, DeploymentEnvironment: &environment,
		RequiredSecretReferences: &secretReferences, RequiredVariableReferences: &variableReferences,
		RequiredHealthChecks: &health, RecoveryDefault: &recovery,
	}
	id := uuid.Must(uuid.NewV4())
	layer := deliverypolicy.Layer{
		SchemaVersion: deliverypolicy.SchemaVersion, RevisionID: id.String(), Level: deliverypolicy.LevelProject,
		OrganizationID: project.ClientID.String(), ProjectID: project.ID.String(), Patch: patch,
	}
	digest, err := deliverypolicy.LayerDigest(layer)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(patch)
	revision := models.DeliveryPolicyRevision{
		ID: id, SchemaVersion: deliverypolicy.SchemaVersion, Level: string(deliverypolicy.LevelProject),
		OrganizationID: project.ClientID.String(), ProjectID: &project.ID, PatchJSON: string(encoded), ContentSHA256: digest,
		ProposedBy: "policy-author", CreatedAt: controlNow.Add(-2 * time.Hour),
	}
	decision := models.DeliveryPolicyDecision{
		ID: uuid.Must(uuid.NewV4()), PolicyRevisionID: id, PolicyDigest: digest, Action: "approved",
		ActorCognitoSub: "independent-policy-reviewer", OccurredAt: controlNow.Add(-time.Hour),
	}
	return revision, decision
}

func controlVault(t *testing.T, projectID uuid.UUID, reference, revision string) models.DeliveryProjectVaultRevision {
	t.Helper()
	manifest := projectvault.Manifest{
		SchemaVersion: projectvault.SchemaVersion, Scope: "repository",
		Repository: projectvault.Repository{Reference: reference, DefaultBranch: "main", Revision: revision}, Entries: []projectvault.VaultEntry{},
	}
	digest, err := projectvault.ManifestSHA256(manifest)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(manifest)
	return models.DeliveryProjectVaultRevision{
		ID: uuid.Must(uuid.NewV4()), ProjectID: projectID, RepositoryReference: reference, Version: 1,
		Revision: revision, SchemaVersion: projectvault.SchemaVersion, ManifestJSON: string(encoded), ContentSHA256: digest,
		SourceOnboardingID: uuid.Must(uuid.NewV4()), PublishedBy: "vault-reviewer", PublishedAt: controlNow.Add(-time.Hour),
	}
}

func hasControlReason(decision releasegate.Decision, code string) bool {
	for _, reason := range decision.Reasons {
		if reason.Code == code {
			return true
		}
	}
	return false
}
