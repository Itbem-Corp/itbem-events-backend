package automationagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"events-stocks/models"
)

type capturedPatchObject struct {
	body        []byte
	contentType string
}

type patchArtifactWorkerStore struct {
	objects map[string]capturedPatchObject
	putErr  error
}

func (*patchArtifactWorkerStore) Get(context.Context, string, string) ([]byte, error) {
	return nil, os.ErrNotExist
}

func (*patchArtifactWorkerStore) PutEncryptedJSON(context.Context, string, string, []byte) error {
	return nil
}

func (store *patchArtifactWorkerStore) PutEncryptedObject(_ context.Context, bucket, key string, body []byte, contentType string) error {
	if store.putErr != nil {
		return store.putErr
	}
	if store.objects == nil {
		store.objects = make(map[string]capturedPatchObject)
	}
	store.objects[bucket+"/"+key] = capturedPatchObject{body: append([]byte(nil), body...), contentType: contentType}
	return nil
}

func TestCaptureWorktreePatchMatchesExactBinaryDiffAndIncludesIntentToAdd(t *testing.T) {
	root := testRepository(t)
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-m", "base")
	base := testGit(t, root, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	newBinary := []byte{0, 1, 2, 0, 255, 16, 32, 0, 42}
	if err := os.WriteFile(filepath.Join(root, "new.bin"), newBinary, 0600); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", "--intent-to-add", "--all")

	expectedCommand := exec.Command("git", "diff", "--binary", "--full-index", "--no-ext-diff", base)
	expectedCommand.Dir = root
	expectedCommand.Env = repositoryCommandEnvironment(os.Environ(), nil)
	expected, err := expectedCommand.Output()
	if err != nil {
		t.Fatalf("capture reference diff: %v", err)
	}
	digest, body, err := captureWorktreePatch(context.Background(), root, base, maxStepPatchArtifactBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, expected) {
		t.Fatalf("captured bytes differ from the reviewed Git diff: got %d want %d", len(body), len(expected))
	}
	if !bytes.Contains(body, []byte("new.bin")) || !bytes.Contains(body, []byte("GIT binary patch")) {
		t.Fatalf("binary or intent-to-add content was omitted from captured patch: %q", body)
	}
	actual := sha256.Sum256(body)
	if digest != hex.EncodeToString(actual[:]) || digest == "" {
		t.Fatalf("patch digest does not hash the exact captured bytes: %s", digest)
	}
}

func TestCaptureWorktreePatchEnforcesBoundWithoutUnboundedBuffer(t *testing.T) {
	root := testRepository(t)
	testGit(t, root, "commit", "--allow-empty", "-m", "base")
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(strings.Repeat("x", 4096)), 0600); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", "--intent-to-add", "--all")
	base := testGit(t, root, "rev-parse", "HEAD")
	if _, _, err := captureWorktreePatch(context.Background(), root, base, 128); err == nil || !strings.Contains(err.Error(), "size") {
		t.Fatalf("oversized patch was not rejected: %v", err)
	}
}

func TestStepPatchPayloadIsStrippedBeforeSerializationAndUploadUsesBoundedSSEReference(t *testing.T) {
	body := []byte("diff --git a/a.txt b/a.txt\nindex 1234567890abcdef1234567890abcdef12345678..abcdef1234567890abcdef1234567890abcdef12 100644\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-before\n+after\n")
	digest := sha256.Sum256(body)
	payload := newStepPatchArtifactPayload("workspace://repo", strings.Repeat("a", 40), hex.EncodeToString(digest[:]), body)
	result := map[string]any{"workspace": "workspace://repo", privateStepPatchArtifactKey: payload}
	intermediate, err := json.Marshal(result)
	if err != nil || bytes.Contains(intermediate, body) || bytes.Contains(intermediate, []byte(strings.Repeat("a", 40))) || bytes.Contains(intermediate, []byte(hex.EncodeToString(digest[:]))) {
		t.Fatalf("private patch fields were serializable: %s err=%v", intermediate, err)
	}
	artifacts, err := takeStepPatchArtifacts(result)
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("could not extract private patch payload: %#v %v", artifacts, err)
	}
	encodedResult, err := json.Marshal(result)
	if err != nil || bytes.Contains(encodedResult, []byte("patch")) || bytes.Contains(encodedResult, body) || bytes.Contains(encodedResult, []byte("base_sha")) {
		t.Fatalf("stripped result still exposes patch data: %s err=%v", encodedResult, err)
	}

	store := &patchArtifactWorkerStore{}
	worker := &Worker{config: WorkerConfig{OutputBucket: "itbem-ai-outputs-test"}, store: store}
	stepID := "11111111-1111-4111-8111-111111111111"
	references, err := worker.uploadStepPatchArtifacts(context.Background(), "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333", stepID, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	if len(references) != 1 || references[0].RepositoryRef != "workspace://repo" || references[0].BaseSHA != strings.Repeat("a", 40) || references[0].SHA256 != hex.EncodeToString(digest[:]) || references[0].SizeBytes != int64(len(body)) {
		t.Fatalf("artifact reference metadata mismatch: %#v", references)
	}
	key := "itbem-ai-outputs-test/automation/22222222-2222-4222-8222-222222222222/runs/33333333-3333-4333-8333-333333333333/steps/" + stepID + "/patches/" + hex.EncodeToString(digest[:]) + ".patch"
	stored, ok := store.objects[key]
	if !ok || stored.contentType != stepPatchArtifactMediaType || !bytes.Equal(stored.body, body) {
		t.Fatalf("private patch object key, bytes, or MIME metadata mismatch: key=%s object=%#v", key, stored)
	}
	for _, value := range artifacts[0].Body {
		if value != 0 {
			t.Fatal("uploaded patch body was not cleared from the worker buffer")
		}
	}
}

func TestStepPatchCredentialScannerFailsClosedAndNeverUploads(t *testing.T) {
	secretPatch := []byte("diff --git a/.env b/.env\nnew file mode 100644\n+++ b/.env\n+MINIMAX_API_KEY=must-not-upload-this-secret\n")
	if err := scanStepPatchForCredentials(secretPatch); err == nil {
		t.Fatal("credential-bearing patch was accepted")
	}
	digest := sha256.Sum256(secretPatch)
	payload := newStepPatchArtifactPayload("workspace://repo", strings.Repeat("a", 40), hex.EncodeToString(digest[:]), secretPatch)
	store := &patchArtifactWorkerStore{}
	worker := &Worker{config: WorkerConfig{OutputBucket: "itbem-ai-outputs-test"}, store: store}
	if _, err := worker.uploadStepPatchArtifacts(context.Background(), "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333", "11111111-1111-4111-8111-111111111111", []stepPatchArtifactPayload{payload}); err == nil {
		t.Fatal("credential-bearing patch upload did not fail closed")
	}
	if len(store.objects) != 0 {
		t.Fatalf("credential-bearing patch was persisted: %#v", store.objects)
	}
}

func TestStepPatchCredentialScannerFindsHighConfidenceCredentialForms(t *testing.T) {
	for _, patch := range [][]byte{
		[]byte("+OPENAI_API_KEY=sk-proj-abcdefghijklmnopqrstuvwxyz123456\n"),
		[]byte("+-----BEGIN PRIVATE KEY-----\n"),
		[]byte("+token=ghp_abcdefghijklmnopqrstuvwxyz1234567890\n"),
	} {
		if err := scanStepPatchForCredentials(patch); err == nil {
			t.Fatalf("high-confidence credential form passed the scanner: %q", patch)
		}
	}
}

func TestStepPatchArtifactManifestDigestIsStableForOneOrManyRepositories(t *testing.T) {
	first := models.DeliveryPlanStepPatchArtifactReference{RepositoryRef: "workspace://api", BaseSHA: strings.Repeat("a", 40), SHA256: strings.Repeat("1", 64), SizeBytes: 42}
	second := models.DeliveryPlanStepPatchArtifactReference{RepositoryRef: "workspace://web", BaseSHA: strings.Repeat("b", 40), SHA256: strings.Repeat("2", 64), SizeBytes: 1234}
	for _, references := range [][]models.DeliveryPlanStepPatchArtifactReference{{first}, {second, first}, {first, second}} {
		got, err := models.DeliveryPlanStepPatchArtifactManifestSHA256(references)
		if err != nil {
			t.Fatal(err)
		}
		manifest := "workspace://api\n" + first.BaseSHA + "\n" + first.SHA256 + "\n42\n"
		if len(references) > 1 {
			manifest += "workspace://web\n" + second.BaseSHA + "\n" + second.SHA256 + "\n1234\n"
		}
		digest := sha256.Sum256([]byte(manifest))
		if got != hex.EncodeToString(digest[:]) {
			t.Fatalf("manifest digest mismatch for %d refs: %s", len(references), got)
		}
	}
}
