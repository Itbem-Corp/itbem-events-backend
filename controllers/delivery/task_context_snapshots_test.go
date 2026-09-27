package delivery

import (
	"encoding/json"
	"errors"
	"events-stocks/models"
	"testing"
	"time"

	"github.com/gofrs/uuid"
)

func TestTaskContextSnapshotsSelectsLocalPrimaryWithoutMutatingProjectSources(t *testing.T) {
	backendID := uuid.Must(uuid.NewV4())
	frontendID := uuid.Must(uuid.NewV4())
	remoteID := uuid.Must(uuid.NewV4())
	sources := []models.DeliveryContextSource{
		{ID: backendID, Kind: "repository", Reference: "workspace://backend", MetadataJSON: `{"repository_role":"primary","repository_kind":"backend_api"}`},
		{ID: frontendID, Kind: "repository", Reference: "workspace://frontend", MetadataJSON: `{"repository_role":"supporting","repository_kind":"frontend"}`},
		{ID: remoteID, Kind: "repository", Reference: "github://org/frontend", MetadataJSON: `{"repository_role":"supporting"}`},
	}
	snapshots, err := taskContextSnapshots(uuid.Must(uuid.NewV4()), sources, frontendID.String(), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	for index, want := range []string{"supporting", "primary", "supporting"} {
		var metadata map[string]any
		if err := json.Unmarshal([]byte(snapshots[index].MetadataJSON), &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata["repository_role"] != want {
			t.Fatalf("snapshot %d role = %v, want %s", index, metadata["repository_role"], want)
		}
	}
	if sources[0].MetadataJSON != `{"repository_role":"primary","repository_kind":"backend_api"}` || sources[1].MetadataJSON != `{"repository_role":"supporting","repository_kind":"frontend"}` {
		t.Fatal("project repository metadata was mutated")
	}
}

func TestTaskContextSnapshotsRejectsUnselectedOrRemotePrimary(t *testing.T) {
	selectedID := uuid.Must(uuid.NewV4())
	otherID := uuid.Must(uuid.NewV4())
	local := []models.DeliveryContextSource{{ID: selectedID, Kind: "repository", Reference: "workspace://backend"}}
	if _, err := taskContextSnapshots(uuid.Must(uuid.NewV4()), local, otherID.String(), time.Now().UTC()); !errors.Is(err, errInvalidTaskPrimaryRepository) {
		t.Fatalf("unselected repository error = %v", err)
	}
	remote := []models.DeliveryContextSource{{ID: selectedID, Kind: "repository", Reference: "github://org/backend"}}
	if _, err := taskContextSnapshots(uuid.Must(uuid.NewV4()), remote, selectedID.String(), time.Now().UTC()); !errors.Is(err, errInvalidTaskPrimaryRepository) {
		t.Fatalf("remote-only repository error = %v", err)
	}
}

func TestTaskContextSnapshotsPreservesLegacyRoleWhenNoTaskPrimarySelected(t *testing.T) {
	source := models.DeliveryContextSource{ID: uuid.Must(uuid.NewV4()), Kind: "repository", Reference: "workspace://backend", MetadataJSON: `{"repository_role":"primary"}`}
	snapshots, err := taskContextSnapshots(uuid.Must(uuid.NewV4()), []models.DeliveryContextSource{source}, "", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if snapshots[0].MetadataJSON != source.MetadataJSON {
		t.Fatalf("legacy role changed: %s", snapshots[0].MetadataJSON)
	}
}
