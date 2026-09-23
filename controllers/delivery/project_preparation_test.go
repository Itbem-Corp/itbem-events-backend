package delivery

import (
	"events-stocks/models"
	"testing"
)

func TestProjectPreparationDoesNotConfuseRemoteContextWithEditableCode(t *testing.T) {
	project := models.DeliveryProject{Summary: "Build", Context: []models.DeliveryContextSource{{Kind: "repository", Reference: "github://org/repo", Status: "ready", Revision: "abc"}}}
	result := describeProjectPreparation(project)
	if result.Checks[1].State != "missing" {
		t.Fatal("remote reference must not imply an editable checkout")
	}
	for _, key := range []string{"runtime", "acceptance", "budget", "sandbox"} {
		if check := preparationCheckByKey(result, key); check.State != "unknown" {
			t.Fatalf("unverified %s: %#v", key, check)
		}
	}
}

func TestProjectPreparationRejectsMissingDependencyAndDirtyCheckout(t *testing.T) {
	for _, metadata := range []string{`{"depends_on_repositories":["workspace://missing"]}`, `{"local_workspace_dirty":true}`} {
		project := models.DeliveryProject{Summary: "Build", Context: []models.DeliveryContextSource{{Kind: "repository", Reference: "workspace://api", Status: "ready", Revision: "abc", MetadataJSON: metadata}}}
		if describeProjectPreparation(project).Checks[1].State != "missing" {
			t.Fatalf("unsafe preparation: %s", metadata)
		}
	}
}

func TestProjectPreparationSeparatesRemoteSyncAndSandboxReadiness(t *testing.T) {
	pending := models.DeliveryProject{Summary: "Build", Context: []models.DeliveryContextSource{{Kind: "repository", Reference: "github://org/repo", Status: "pending_sync"}}}
	result := describeProjectPreparation(pending)
	if check := preparationCheckByKey(result, "remote_sync"); check.State != "missing" {
		t.Fatalf("pending remote context must remain actionable: %#v", check)
	}

	readyMetadata := `{"workspace_harness":{"sandbox_mode":"docker","sandbox_resource_policy":"cpu=2,memory=2g,pids=256"},"repository_role":"primary"}`
	ready := models.DeliveryProject{Summary: "Build", Context: []models.DeliveryContextSource{{Kind: "repository", Reference: "workspace://api", Revision: "abc", Status: "ready", MetadataJSON: readyMetadata}}}
	result = describeProjectPreparation(ready)
	if check := preparationCheckByKey(result, "sandbox"); check.State != "ready" {
		t.Fatalf("declared Docker harness should be visible as ready: %#v", check)
	}
}

func TestWorkerSupportsOperationTreatsEmptyProfileAsGeneralist(t *testing.T) {
	if !workerSupportsOperation(nil, "delivery.plan") {
		t.Fatal("an empty capability profile must represent an explicit generalist worker")
	}
	if !workerSupportsOperation([]string{"delivery.plan"}, "delivery.plan") {
		t.Fatal("declared operation capability was not recognized")
	}
	if workerSupportsOperation([]string{"delivery.plan"}, "delivery.implementation") {
		t.Fatal("specialist readiness must not invent unsupported implementation capability")
	}
}

func TestImplementationReadinessNeverTreatsHostProcessAsSandbox(t *testing.T) {
	if implementationWorkspaceSandboxReady(true, true, "host_process") {
		t.Fatal("host process must not satisfy implementation sandbox readiness")
	}
	if implementationWorkspaceSandboxReady(true, false, "docker_container") {
		t.Fatal("unready Docker workspace must not satisfy implementation readiness")
	}
	if !implementationWorkspaceSandboxReady(true, true, "docker_container") {
		t.Fatal("ready Docker workspace should satisfy implementation readiness")
	}
}

func preparationCheckByKey(preparation projectPreparation, key string) preparationCheck {
	for _, check := range preparation.Checks {
		if check.Key == key {
			return check
		}
	}
	return preparationCheck{Key: key, State: "missing", Detail: "not found"}
}
