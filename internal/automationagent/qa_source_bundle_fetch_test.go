package automationagent

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

type qaBundleRejectTransport struct{ calls int }

func (r *qaBundleRejectTransport) RoundTrip(*http.Request) (*http.Response, error) {
	r.calls++
	panic("invalid source policy reached authentication")
}

func TestQASourceBundleRejectsInvalidApprovalBeforeAuthentication(t *testing.T) {
	for _, policy := range []map[string]string{
		{"../contract": "example/contract"},
		{".git/contract": "example/contract"},
		{"contract\x00hidden": "example/contract"},
		{"contract\xff": "example/contract"},
		{"contract": "https://github.com/example/contract"},
		{"contract": "example/contract?token=synthetic"},
	} {
		transport := &qaBundleRejectTransport{}
		bundle, err := FetchGitHubQASourceBundle(context.Background(), "example/service", strings.Repeat("a", 40), policy, GitHubAppConfig{}, &http.Client{Transport: transport})
		if err == nil || transport.calls != 0 || bundle.Pack != nil || bundle.Dependencies != nil {
			t.Fatal("invalid approval was not rejected before acquisition")
		}
	}
}
