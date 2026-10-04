package delivery

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"events-stocks/internal/automationagent"
	"events-stocks/models"
	"github.com/gofrs/uuid"
)

func TestCatalogContextFollowsOnlySelectedProjectAndFrozenRevision(t *testing.T) {
	project := uuid.Must(uuid.NewV4())
	source := models.DeliveryContextSource{ID: uuid.Must(uuid.NewV4()), ProjectID: project, Kind: "repository", Reference: "github://itbem-corp/api", Revision: strings.Repeat("a", 40)}
	metadata, _ := json.Marshal(map[string]any{"catalog_managed": true, "linked_source_id": source.ID.String()})
	workspace := models.DeliveryContextSource{ID: uuid.Must(uuid.NewV4()), ProjectID: project, Kind: "repository", Reference: "workspace://" + automationagent.CatalogWorkspaceID(source.ID.String()), Revision: source.Revision, MetadataJSON: string(metadata)}
	for _, scenario := range []string{"eligible", "other-project", "stale-checkpoint", "unselected"} {
		t.Run(scenario, func(t *testing.T) {
			candidate := workspace
			selected := []models.DeliveryContextSource{source}
			switch scenario {
			case "other-project":
				candidate.ProjectID = uuid.Must(uuid.NewV4())
			case "stale-checkpoint":
				candidate.Revision = strings.Repeat("b", 40)
			case "unselected":
				selected = nil
			}
			got := appendMandatoryProjectContext(selected, []models.DeliveryContextSource{candidate})
			if scenario == "eligible" {
				if len(got) != 2 {
					t.Fatalf("prepared workspace not selected: %#v", got)
				}
				snapshots, err := taskContextSnapshots(uuid.Must(uuid.NewV4()), got, source.ID.String(), time.Now())
				if err != nil {
					t.Fatal(err)
				}
				var metadata map[string]any
				_ = json.Unmarshal([]byte(snapshots[1].MetadataJSON), &metadata)
				if metadata["repository_role"] != "primary" {
					t.Fatal("logical repository primary did not resolve to its prepared workspace")
				}
			} else if len(got) != len(selected) {
				t.Fatalf("catalog widened selected task scope: %#v", got)
			}
		})
	}
}
