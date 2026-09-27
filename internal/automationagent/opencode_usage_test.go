package automationagent

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestFetchOpenCodeUsageUsesConsoleCostRowsAndIgnoresOtherProviders(t *testing.T) {
	now := time.Date(2026, time.September, 23, 18, 0, 0, 0, time.UTC)
	client := &http.Client{Transport: providerModelsRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != openCodeUsageExportURL+"?range=24h&scope=organization" && request.URL.String() != openCodeUsageExportURL+"?range=7d&scope=organization" && request.URL.String() != openCodeUsageExportURL+"?range=30d&scope=organization" {
			t.Fatalf("unexpected usage request %s", request.URL)
		}
		csv := "provider,cost_micro_cents,created_at\nopencode,1200000000,2026-09-23T17:00:00Z\nopenai,9999999999,2026-09-23T17:00:00Z\n"
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(csv))}, nil
	})}
	usage, err := FetchOpenCodeUsage(context.Background(), "usage-key", client, now)
	if err != nil || len(usage.Windows) != 3 {
		t.Fatalf("usage=%#v err=%v", usage, err)
	}
	if usage.Windows[0].UsedMicrousd != 12_000_000 || usage.Windows[0].RemainingMicrousd != 0 {
		t.Fatalf("unexpected 5-hour OpenCode quota: %#v", usage.Windows[0])
	}
}
