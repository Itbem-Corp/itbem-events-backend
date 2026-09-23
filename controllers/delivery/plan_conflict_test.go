package delivery

import (
	"testing"

	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/require"
)

func TestDecompositionPlanConflictsOnlyBlockOverlappingComparableRoots(t *testing.T) {
	requestID := uuid.Must(uuid.NewV4())
	firstID := uuid.Must(uuid.NewV4())
	secondID := uuid.Must(uuid.NewV4())
	thirdID := uuid.Must(uuid.NewV4())
	otherRequestID := uuid.Must(uuid.NewV4())
	makeItem := func(id uuid.UUID, request uuid.UUID, file, repository string) models.DeliveryWorkItem {
		return models.DeliveryWorkItem{
			ID: id, RequestID: &request, PlanJSON: `{"files_impacted":["` + file + `"],"repository_impact":[{"reference":"` + repository + `","impact":"changes"}]}`,
		}
	}
	items := []models.DeliveryWorkItem{
		makeItem(firstID, requestID, "src/shared.ts", "workspace://dashboard"),
		makeItem(secondID, requestID, "src/shared.ts", "workspace://dashboard"),
		makeItem(thirdID, requestID, "src/other.ts", "workspace://dashboard"),
		makeItem(uuid.Must(uuid.NewV4()), otherRequestID, "src/shared.ts", "workspace://dashboard"),
	}

	conflicts := decompositionPlanConflicts(items)
	require.Len(t, conflicts, 2)
	require.Contains(t, conflicts[firstID], "src/shared.ts")
	require.Contains(t, conflicts[secondID], "workspace://dashboard")
	require.NotContains(t, conflicts, thirdID)

	// A plan that declares multiple changed repositories is intentionally not
	// guessed into a file-level conflict; repository-specific integration must
	// provide a stronger mapping before blocking it.
	multiRepo := makeItem(uuid.Must(uuid.NewV4()), requestID, "src/shared.ts", "workspace://dashboard")
	multiRepo.PlanJSON = `{"files_impacted":["src/shared.ts"],"repository_impact":[{"reference":"workspace://dashboard","impact":"changes"},{"reference":"workspace://api","impact":"changes"}]}`
	require.Empty(t, planChangedFiles(multiRepo))
}
