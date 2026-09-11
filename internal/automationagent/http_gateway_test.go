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
			if sample.wantRetry && gatewayErr.operation != "lease" {
				t.Fatalf("gateway operation = %q, want lease", gatewayErr.operation)
			}
		})
	}
}

func TestGatewayOperationAllowsOnlyStableDiagnosticLabels(t *testing.T) {
	for path, want := range map[string]string{
		"/api/internal/automation/gateway/probe":                      "probe",
		"/api/internal/automation/gateway/leases":                     "lease",
		"/api/internal/automation/gateway/leases/visibility":          "lease visibility",
		"/api/internal/automation/gateway/objects/read":               "object read",
		"/api/internal/automation/gateway/objects/write":              "object write",
		"/api/internal/automation/gateway/objects/read/untrusted-key": "request",
	} {
		if got := gatewayOperation(path); got != want {
			t.Errorf("gatewayOperation(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestHTTPGatewayAllowsOnlyStorageDiagnosticAllowList(t *testing.T) {
	for _, sample := range []struct {
		header string
		want   string
	}{
		{header: "authorization", want: "storage=authorization"},
		{header: "region", want: "storage=region"},
		{header: "untrusted value containing a private object ref", want: ""},
	} {
		t.Run(sample.header, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("X-ITBEM-Gateway-Storage-Failure", sample.header)
				writer.WriteHeader(http.StatusServiceUnavailable)
			}))
			defer server.Close()
			gateway, err := NewHTTPGateway(server.URL, "test-token", agentwork.RoleReviewer, agentwork.LaneReview, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			_, err = gateway.Receive(context.Background(), 1)
			var gatewayErr *gatewayRequestError
			if !errors.As(err, &gatewayErr) || gatewayErr.diagnostic != sample.want {
				t.Fatalf("gateway diagnostic = %q, want %q (error: %v)", gatewayErr.diagnostic, sample.want, err)
			}
		})
	}
}

func TestHTTPGatewayMapsOnlyMissingOptionalObjectToNotFound(t *testing.T) {
	for _, sample := range []struct {
		name    string
		status  int
		missing bool
	}{
		{name: "absent checkpoint", status: http.StatusNotFound, missing: true},
		{name: "forbidden checkpoint", status: http.StatusForbidden},
	} {
		t.Run(sample.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/api/internal/automation/gateway/objects/read" {
					t.Fatalf("unexpected gateway path %q", request.URL.Path)
				}
				writer.WriteHeader(sample.status)
			}))
			defer server.Close()
			gateway, err := NewHTTPGateway(server.URL, "test-token", agentwork.RoleReviewer, agentwork.LaneReview, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.WithValue(context.Background(), gatewayLeaseContextKey{}, "sealed-test-lease")
			_, err = gateway.Get(ctx, "itbem-ai-outputs-local", "automation/task/code-review-progress.json")
			if errors.Is(err, ErrObjectNotFound) != sample.missing {
				t.Fatalf("object missing classification = %t, want %t (error: %v)", errors.Is(err, ErrObjectNotFound), sample.missing, err)
			}
		})
	}
}

func TestHTTPGatewayPreservesForbiddenStatusWithoutMakingItRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	gateway, err := NewHTTPGateway(server.URL, "test-token", agentwork.RoleReviewer, agentwork.LaneReview, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), gatewayLeaseContextKey{}, "sealed-test-lease")
	_, err = gateway.Get(ctx, "itbem-ai-outputs-local", "automation/task/code-review-progress.json")
	if err == nil {
		t.Fatal("expected forbidden gateway read")
	}
	var retryable *gatewayRequestError
	if errors.As(err, &retryable) {
		t.Fatalf("forbidden gateway read became retryable: %v", err)
	}
	if status, ok := gatewayResponseStatus(err); !ok || status != http.StatusForbidden {
		t.Fatalf("forbidden status = (%d, %t), want (403, true)", status, ok)
	}
}
