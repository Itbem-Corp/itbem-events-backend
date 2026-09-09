package automationagent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"events-stocks/internal/agentwork"
)

func TestHTTPGatewayClassifiesOnlyTransientResponsesForQueueRetry(t *testing.T) {
	for _, sample := range []struct {
		name       string
		status     int
		retryAfter string
		wantRetry  bool
		wantDelay  time.Duration
	}{
		{name: "bad gateway", status: http.StatusBadGateway, wantRetry: true, wantDelay: gatewayRetryDefaultDelay},
		{name: "rate limit hint", status: http.StatusTooManyRequests, retryAfter: "7", wantRetry: true, wantDelay: 7 * time.Second},
		{name: "unauthorized", status: http.StatusUnauthorized},
		{name: "invalid request", status: http.StatusBadRequest},
	} {
		t.Run(sample.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Header.Get("X-Agent-Gateway-Token") != "test-token" {
					t.Fatal("gateway token header was not sent")
				}
				if sample.retryAfter != "" {
					writer.Header().Set("Retry-After", sample.retryAfter)
				}
				writer.WriteHeader(sample.status)
			}))
			defer server.Close()

			gateway, err := NewHTTPGateway(server.URL, "test-token", agentwork.RoleQA, agentwork.LaneQA, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			_, err = gateway.Receive(context.Background(), 1)
			if err == nil {
				t.Fatal("expected gateway receive error")
			}
			var gatewayErr *gatewayRequestError
			if transient := errors.As(err, &gatewayErr); transient != sample.wantRetry {
				t.Fatalf("transient classification = %t, want %t (error: %v)", transient, sample.wantRetry, err)
			}
			if sample.wantRetry && gatewayErr.RetryDelay() != sample.wantDelay {
				t.Fatalf("retry delay = %s, want %s", gatewayErr.RetryDelay(), sample.wantDelay)
			}
		})
	}
}
