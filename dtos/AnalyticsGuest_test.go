package dtos

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHydrateAnalyticsGuestTables(t *testing.T) {
	guests := []AnalyticsGuest{{FirstName: "Ana", TableName: "Mesa VIP"}, {FirstName: "Luis"}}

	HydrateAnalyticsGuestTables(guests)

	require.NotNil(t, guests[0].Table)
	require.Equal(t, "Mesa VIP", guests[0].Table.Name)
	require.Nil(t, guests[1].Table)
}
