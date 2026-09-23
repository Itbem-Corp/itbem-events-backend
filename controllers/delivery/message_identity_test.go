package delivery

import (
	"events-stocks/models"
	"testing"

	"github.com/gofrs/uuid"
)

func TestDeliveryMessageRetryIdentity(t *testing.T) {
	original := models.DeliveryMessage{WorkItemID: uuid.Must(uuid.NewV4()), AuthorID: "actor", AuthorType: "human", Body: "Preserve order", Phase: "implementation", ResumeRequested: true}
	advanced := original
	advanced.Phase = "code_review"
	if !sameDeliveryMessage(original, advanced) {
		t.Fatal("a lost response retry must resolve to the existing message after a phase change")
	}
	for name, change := range map[string]func(*models.DeliveryMessage){
		"work":      func(m *models.DeliveryMessage) { m.WorkItemID = uuid.Must(uuid.NewV4()) },
		"actor":     func(m *models.DeliveryMessage) { m.AuthorID = "other" },
		"authority": func(m *models.DeliveryMessage) { m.AuthorType = "agent" },
		"body":      func(m *models.DeliveryMessage) { m.Body = "different" },
		"intent":    func(m *models.DeliveryMessage) { m.ResumeRequested = false },
	} {
		t.Run(name, func(t *testing.T) {
			modified := original
			change(&modified)
			if sameDeliveryMessage(original, modified) {
				t.Fatal("conflicting retry accepted")
			}
		})
	}
}

func TestDeliveryMessageRetryIdentityIncludesAuthorizedAttachments(t *testing.T) {
	workItemID := uuid.Must(uuid.NewV4())
	original := models.DeliveryMessage{
		WorkItemID: workItemID, AuthorID: "actor", AuthorType: "human", Body: "Review the captured failure",
		Phase: "implementation", ResumeRequested: false,
		AttachmentsJSON: `[{"kind":"evidence","id":"00000000-0000-0000-0000-000000000001","name":"failure","reference":"evidence://failure"}]`,
	}
	reordered := original
	reordered.AttachmentsJSON = `[{"reference":"evidence://failure","name":"failure","id":"00000000-0000-0000-0000-000000000001","kind":"evidence"}]`
	if !sameDeliveryMessage(original, reordered) {
		t.Fatal("equivalent attachment references must preserve idempotent identity")
	}
	changed := original
	changed.AttachmentsJSON = `[{"kind":"evidence","id":"00000000-0000-0000-0000-000000000002","name":"other","reference":"evidence://other"}]`
	if sameDeliveryMessage(original, changed) {
		t.Fatal("a retry with a different attachment must not be accepted")
	}
}
