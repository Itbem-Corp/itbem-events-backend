package automationagent

import (
	"context"
	"strings"
	"testing"
)

func TestQASourceFetchKeepsCredentialOutOfArgumentsAndRejectsUnboundCoordinates(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "99")
	t.Setenv("GIT_SSH_COMMAND", "/untrusted-parent-command")
	t.Setenv("GIT_TRACE", "/untrusted-trace-output")
	cmd := qaSourceFetchCommand(context.Background(), t.TempDir(), "example/service", strings.Repeat("a", 40), "synthetic-private-token")
	arguments := strings.Join(cmd.Args, " ")
	if strings.Contains(arguments, "synthetic-private-token") || !strings.Contains(arguments, "http.followRedirects=false") || !strings.Contains(arguments, "--depth=1") || !strings.Contains(arguments, "--no-recurse-submodules") {
		t.Fatal("source fetch weakened its transport or exposed a credential in arguments")
	}
	environment := strings.Join(cmd.Env, "\n")
	if strings.Contains(environment, "/untrusted-") || strings.Contains(environment, "GIT_CONFIG_COUNT=99") || !strings.Contains(environment, "GIT_ALLOW_PROTOCOL=https") || !strings.Contains(environment, "http.https://github.com/.extraheader") {
		t.Fatal("source fetch inherited caller credentials, tracing, or transport configuration")
	}
	for _, repository := range []string{"https://outside.invalid/service", "example/service/extra", "example/service\nother"} {
		if _, _, err := FetchGitHubQASourcePack(context.Background(), repository, strings.Repeat("a", 40), GitHubAppConfig{}, nil); err == nil {
			t.Fatal("arbitrary source coordinates accepted")
		}
	}
}
