package delivery

import (
	"encoding/json"
	"events-stocks/models"
	"fmt"
	"sort"
	"strings"
)

// Phase is historical metadata, not part of a retry's identity: the workflow
// may have advanced after the first response was lost. Never alter the record.
func sameDeliveryMessage(existing, requested models.DeliveryMessage) bool {
	return existing.WorkItemID == requested.WorkItemID &&
		existing.AuthorID == requested.AuthorID &&
		existing.AuthorType == requested.AuthorType &&
		existing.Body == requested.Body &&
		existing.ResumeRequested == requested.ResumeRequested &&
		canonicalDeliveryMessageAttachments(existing.AttachmentsJSON) == canonicalDeliveryMessageAttachments(requested.AttachmentsJSON)
}

func canonicalDeliveryMessageAttachments(raw string) string {
	var attachments []models.DeliveryMessageAttachment
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &attachments); err != nil {
			return "[]"
		}
	}
	if attachments == nil {
		attachments = []models.DeliveryMessageAttachment{}
	}
	// The resolver emits a stable order. Sorting here also makes identity safe
	// for legacy records or future callers that construct the model directly.
	sort.SliceStable(attachments, func(i, j int) bool {
		left, right := attachments[i].Kind+":"+attachments[i].ID, attachments[j].Kind+":"+attachments[j].ID
		return left < right
	})
	encoded, err := json.Marshal(attachments)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

// Conversation can request another attempt, never approve a gate, publish a
// branch or restart a provider outcome whose side effects are unknown.
func messageContinuationPhase(item models.DeliveryWorkItem, epoch, active int64) (string, error) {
	if epoch != item.AutomationEpoch {
		return "", fmt.Errorf("the workflow changed; refresh before continuing")
	}
	if active > 0 || item.AgentProgress == "queued" {
		return "", fmt.Errorf("work is already active or stopping; the note can be saved without continuing")
	}
	switch item.State {
	case "planning":
		return "plan", nil
	case "implementation":
		return "implementation", nil
	case "qa_running":
		return "qa", nil
	case "release_review":
		return "summary", nil
	}
	return "", fmt.Errorf("this state requires a human decision or a verified preview, not a conversation retry")
}

func ambiguousAgentOutcome(reason string) bool {
	reason = strings.ToLower(reason)
	for _, marker := range []string{"uncertain", "durable answer", "private recovery", "response storage unavailable"} {
		if strings.Contains(reason, marker) {
			return true
		}
	}
	return false
}
