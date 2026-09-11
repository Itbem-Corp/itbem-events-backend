package automation

import (
	"errors"
	"strings"
	"testing"
	"time"

	"events-stocks/internal/agentwork"
	"events-stocks/models"

	"github.com/aws/smithy-go"
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
	replacement := "A"
	if strings.HasSuffix(token, replacement) {
		replacement = "B"
	}
	tampered := token[:len(token)-1] + replacement
	if _, err := openGatewayLease(tampered, identity); err == nil {
		t.Fatal("tampered lease was accepted")
	}
}

func TestGatewayObjectMissingDoesNotTreatStorageFailuresAsAbsence(t *testing.T) {
	if !gatewayObjectMissing(&smithy.GenericAPIError{Code: "NoSuchKey"}) {
		t.Fatal("confirmed missing object was not classified as absent")
	}
	if gatewayObjectMissing(&smithy.GenericAPIError{Code: "AccessDenied"}) || gatewayObjectMissing(errors.New("network unavailable")) {
		t.Fatal("storage authorization or network failures must not look like an absent checkpoint")
	}
}

func TestValidateGatewayObjectBindsReadsAndWritesToTheExactTask(t *testing.T) {
	const taskID = "11111111-1111-4111-8111-111111111111"
	inputBucket := "itbem-ai-inputs-test"
	outputBucket := "itbem-ai-outputs-test"
	lease := gatewayLease{
		TaskID:   taskID,
		InputRef: "s3://" + inputBucket + "/automation/inputs/" + taskID + "/input.json",
	}
	cfg := &models.Config{AutomationInputBucket: inputBucket, AutomationOutputBucket: outputBucket}
	checkpoint := "s3://" + outputBucket + "/automation/" + taskID + "/code-review-progress.json"
	otherTask := "s3://" + outputBucket + "/automation/22222222-2222-4222-8222-222222222222/code-review-progress.json"

	for _, test := range []struct {
		name      string
		reference string
		write     bool
		valid     bool
	}{
		{name: "reads exact input", reference: lease.InputRef, valid: true},
		{name: "does not write input", reference: lease.InputRef, write: true, valid: false},
		{name: "reads own checkpoint", reference: checkpoint, valid: true},
		{name: "writes own checkpoint", reference: checkpoint, write: true, valid: true},
		{name: "does not read another task output", reference: otherTask, valid: false},
		{name: "does not write another task output", reference: otherTask, write: true, valid: false},
		{name: "does not read foreign bucket", reference: "s3://foreign/automation/" + taskID + "/result.json", valid: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, valid := validateGatewayObject(lease, cfg, test.reference, test.write)
			if valid != test.valid {
				t.Fatalf("valid = %t, want %t", valid, test.valid)
			}
		})
	}
}
