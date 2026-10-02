package automationagent

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestQASourceFetchTransfersExactCommitOverVerifiedTLSAndRefusesRedirect(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	pathCommand := exec.Command(gitPath, "--exec-path")
	pathOutput, err := pathCommand.Output()
	if err != nil {
		t.Fatal(err)
	}
	backend := filepath.Join(strings.TrimSpace(string(pathOutput)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Fatal("required real Git HTTP backend missing:", err)
	}
	source := setupImplementationRepository(t)
	head := exec.Command(gitPath, "-C", source, "rev-parse", "HEAD")
	headOutput, err := head.Output()
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.TrimSpace(string(headOutput))
	serverRoot := t.TempDir()
	clone := exec.Command(gitPath, "clone", "--bare", source, filepath.Join(serverRoot, "service.git"))
	if err := clone.Run(); err != nil {
		t.Fatal(err)
	}
	gitHandler := &cgi.Handler{Path: backend, Dir: serverRoot, Env: []string{"GIT_PROJECT_ROOT=" + serverRoot, "GIT_HTTP_EXPORT_ALL=1"}}
	var requests atomic.Int32
	var redirected atomic.Bool
	stalled := make(chan struct{}, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path == "/stall.git/info/refs" {
			stalled <- struct{}{}
			<-r.Context().Done()
			return
		}
		if r.URL.Path == "/redirect.git/info/refs" {
			w.Header().Set("Location", "/must-not-follow")
			w.WriteHeader(http.StatusFound)
			return
		}
		if r.URL.Path == "/must-not-follow" {
			redirected.Store(true)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		gitHandler.ServeHTTP(w, r)
	}))
	defer server.Close()
	ca := filepath.Join(t.TempDir(), "synthetic-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	command := func(ctx context.Context, root, remote string) *exec.Cmd {
		cmd := qaSourceFetchCommand(ctx, root, "example/service", commit, "synthetic-token")
		// Only the isolated fixture redirects the fixed GitHub transport to a
		// verified local TLS Git backend. Production has no configurable URL.
		cmd.Args[len(cmd.Args)-2] = server.URL + remote
		for i, value := range cmd.Env {
			if strings.HasPrefix(value, "GIT_CONFIG_KEY_0=") {
				cmd.Env[i] = "GIT_CONFIG_KEY_0=http." + server.URL + "/.extraheader"
			}
		}
		cmd.Env = append(cmd.Env, "GIT_SSL_CAINFO="+ca)
		return cmd
	}
	root := t.TempDir()
	if err := qaSourceGit(context.Background(), root, nil, "init", "--template=", "--initial-branch=source"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if output, err := command(ctx, root, "/service.git").CombinedOutput(); err != nil {
		t.Fatalf("verified TLS source fetch failed: %v %s", err, output)
	}
	pack, digest, err := buildQASourcePack(ctx, root, commit)
	if err != nil {
		t.Fatal(err)
	}
	qa := Workspace{ID: "qa", Root: t.TempDir(), Config: WorkspaceConfig{RepositoryURL: "https://github.com/example/service.git", BaseBranch: "main"}}
	target, err := materializePublishedQASourcePack(ctx, qa, "itbem-agent/11111111-1111-4111-8111-111111111111", commit, digest, pack)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyQASourceRevision(ctx, target, "itbem-agent/11111111-1111-4111-8111-111111111111", commit); err != nil {
		t.Fatal(err)
	}
	if requests.Load() < 2 {
		t.Fatal("fixture did not exercise real Git negotiation and upload-pack")
	}
	if err := command(ctx, root, "/redirect.git").Run(); err == nil || redirected.Load() {
		t.Fatal("source transport followed or accepted a redirect")
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if err := command(cancelled, root, "/service.git").Run(); err == nil {
		t.Fatal("cancelled acquisition succeeded")
	}
	inflight, revoke := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- command(inflight, root, "/stall.git").Run() }()
	select {
	case <-stalled:
	case <-time.After(5 * time.Second):
		revoke()
		t.Fatal("source fetch never reached the pending HTTPS request")
	}
	revoke()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("revoked in-flight acquisition succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("source fetch survived cancellation")
	}
}
