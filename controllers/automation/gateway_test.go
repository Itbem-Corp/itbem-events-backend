package automation

import (
	"strings"
	"testing"
	"time"

	"events-stocks/internal/agentwork"
)

func TestGatewayTokensAreLaneBoundAndDoNotExposeRoot(t *testing.T) {
	root := strings.Repeat("r", 48)
	review := deriveGatewayToken(root, gatewayIdentity{Role: agentwork.RoleReviewer, Lane: agentwork.LaneReview})
	release := deriveGatewayToken(root, gatewayIdentity{Role: agentwork.RoleReleaseManager, Lane: agentwork.LaneRelease})
	if review == release || strings.Contains(review, root) || len(review) < 40 {
		t.Fatalf("gateway token is not safely lane-bound")
	}
}

func TestGatewayLeaseIsConfidentialTamperEvidentAndIdentityBound(t *testing.T) {
	t.Setenv("AUTOMATION_CALLBACK_SECRET", strings.Repeat("s", 48))
	identity := gatewayIdentity{Role: agentwork.RoleReviewer, Lane: agentwork.LaneReview}
	original := gatewayLease{Version: 1, Role: string(identity.Role), Lane: string(identity.Lane), TaskID: "11111111-1111-4111-8111-111111111111", InputRef: "s3://private/automation/inputs/task/input.json", ReceiptHandle: "aws-receipt-must-stay-private", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	token, err := sealGatewayLease(original)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(token, original.ReceiptHandle) || strings.Contains(token, original.InputRef) {
		t.Fatal("sealed lease exposed private transport state")
	}
	opened, err := openGatewayLease(token, identity)
	if err != nil || opened.ReceiptHandle != original.ReceiptHandle {
		t.Fatalf("sealed lease did not round-trip: %#v, %v", opened, err)
	}
	if _, err := openGatewayLease(token, gatewayIdentity{Role: agentwork.RoleQA, Lane: agentwork.LaneQA}); err == nil {
		t.Fatal("lease crossed its role/lane boundary")
	}
	tampered := token[:len(token)-1] + "A"
	if _, err := openGatewayLease(tampered, identity); err == nil {
		t.Fatal("tampered lease was accepted")
	}
}
