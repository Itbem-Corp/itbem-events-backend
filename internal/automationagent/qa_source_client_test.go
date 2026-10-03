package automationagent

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"events-stocks/internal/agentwork"
	"events-stocks/internal/releasegate"
)

func TestQASourceSignedClientImportsOnlyExactFrozenPack(t *testing.T) {
	commit, pack := qaSourcePackFixture(t)
	const task = "11111111-1111-4111-8111-111111111111"
	const run = "22222222-2222-4222-8222-222222222222"
	branch := "itbem-agent/" + task
	candidate := releasegate.Input{SchemaVersion: releasegate.SchemaVersion, Action: releasegate.ActionRelease, ChangeSetID: task, Revisions: []releasegate.Revision{{Repository: "example/service", Branch: "main", SHA: commit}}, Policy: releasegate.Policy{RequiredTestKinds: []string{}}}
	matrix, err := releasegate.RevisionMatrixDigest(candidate.Revisions)
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := json.Marshal(map[string]any{"gatekeeper": candidate, "change_sets": []any{map[string]any{"repository_ref": "workspace://repo", "branch": branch, "commit_sha": commit, "review_type": "pull_request", "ci_status": "passed", "metadata": map[string]string{"remote_repository": "example/service", "target_branch": "main"}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"valid", "valid-bundle", "bundle-digest", "bundle-unapproved", "bundle-type", "task", "run", "matrix", "repository", "sha", "digest", "unknown"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			registry, err := json.Marshal(map[string]WorkspaceConfig{"repo": {Path: root, RepositoryURL: "https://github.com/example/service.git", BaseBranch: "main"}})
			if err != nil {
				t.Fatal(err)
			}
			lookup := func(key string) string {
				if key == "ITBEM_AI_WORKSPACES_JSON" {
					return string(registry)
				}
				return ""
			}
			identity, instance := newTestMachineIdentity(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
				assertSignedCallbackRequest(t, r, body, identity, instance)
				var request map[string]string
				if json.Unmarshal(body, &request) != nil || len(request) != 3 || request["lease_token"] != "sealed-lease" || request["run_id"] != run || request["repository_ref"] != "workspace://repo" || r.URL.Path != "/api/internal/automation/gateway/qa-source" {
					t.Error("source request carried arbitrary coordinates")
				}
				metadata := map[string]any{"schema_version": 1, "task_id": task, "run_id": run, "matrix_digest": matrix, "repository_ref": "workspace://repo", "repository": "example/service", "branch": branch, "commit_sha": commit, "pack_sha256": fmt.Sprintf("%x", sha256.Sum256(pack))}
				payload := pack
				contentType := "application/x-git-packed-objects"
				if strings.Contains(scenario, "bundle") {
					bundle := QASourceBundle{Pack: pack, PackSHA256: fmt.Sprintf("%x", sha256.Sum256(pack))}
					if scenario == "bundle-unapproved" {
						bundle.Dependencies = []QASourceDependencyPack{{Path: "contract", Repository: "outside/contract", CommitSHA: commit, PackSHA256: bundle.PackSHA256, Pack: pack}}
					}
					var hash string
					payload, hash, err = EncodeQASourceBundle(bundle)
					if err != nil {
						t.Error(err)
						w.WriteHeader(500)
						return
					}
					metadata["schema_version"] = 2
					metadata["bundle_sha256"] = hash
					contentType = "application/vnd.itbem.qa-source-bundle"
					if scenario == "bundle-digest" {
						metadata["bundle_sha256"] = strings.Repeat("a", 64)
					}
					if scenario == "bundle-type" {
						contentType = "application/x-git-packed-objects"
					}
				}
				switch scenario {
				case "task":
					metadata["task_id"] = run
				case "run":
					metadata["run_id"] = task
				case "matrix":
					metadata["matrix_digest"] = strings.Repeat("b", 64)
				case "repository":
					metadata["repository"] = "outside/service"
				case "sha":
					metadata["commit_sha"] = strings.Repeat("a", 40)
				case "digest":
					metadata["pack_sha256"] = strings.Repeat("b", 64)
				case "unknown":
					metadata["credential"] = "untrusted-field"
				}
				raw, _ := json.Marshal(metadata)
				w.Header().Set("X-ITBEM-QA-Source", base64.RawURLEncoding.EncodeToString(raw))
				w.Header().Set("Content-Type", contentType)
				_, _ = w.Write(payload)
			}))
			defer server.Close()
			callback := newTestCallbackWithIdentity(t, server, identity, instance)
			gateway, err := NewHTTPGateway(server.URL, "synthetic-lane-token", agentwork.RoleQA, agentwork.LaneQA, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.WithValue(context.Background(), gatewayLeaseContextKey{}, "sealed-lease")
			target, err := callback.AcquireQASource(ctx, gateway, task, run, "workspace://repo", delivery, lookup)
			if scenario == "valid" || scenario == "valid-bundle" {
				if err != nil {
					t.Fatal(err)
				}
				if err := verifyQASourceRevision(ctx, target, branch, commit); err != nil {
					t.Fatal(err)
				}
			} else {
				if err == nil {
					t.Fatal("substituted source was imported")
				}
				if _, err := os.Stat(filepath.Join(root, ".itbem-agent-worktrees", task)); !os.IsNotExist(err) {
					t.Fatal("rejected response published a checkout")
				}
			}
		})
	}
}
