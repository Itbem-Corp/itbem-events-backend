package delivery

import (
	"strings"
	"testing"

	"events-stocks/models"
	"github.com/stretchr/testify/require"
)

func TestDelegatedCodeWaitsForEveryCurrentRepositoryPublication(t *testing.T) {
	f := newCoordinatorFixture(t)
	plan := `{"repository_impact":[{"reference":"workspace://api","impact":"changes"},{"reference":"workspace://dashboard","impact":"changes"}]}`
	require.False(t, delegatedCodePublicationsReady(plan, f.changes))
	second := append([]models.DeliveryChangeSet(nil), f.changes...)
	for i := range second {
		second[i].RepositoryRef = "workspace://dashboard"
		second[i].MetadataJSON = strings.ReplaceAll(second[i].MetadataJSON, "workspace://api", "workspace://dashboard")
	}
	changes := append(append([]models.DeliveryChangeSet(nil), f.changes...), second...)
	require.True(t, delegatedCodePublicationsReady(plan, changes))
	newLocal := second[1]
	newLocal.MetadataJSON = strings.ReplaceAll(newLocal.MetadataJSON, strings.Repeat("d", 64), strings.Repeat("a", 64))
	require.False(t, delegatedCodePublicationsReady(plan, append([]models.DeliveryChangeSet{newLocal}, changes...)))
}
