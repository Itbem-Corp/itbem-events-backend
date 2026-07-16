package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMediaVariantsValueAndScanRoundTrip(t *testing.T) {
	original := MediaVariants{{ObjectKey: "moments/event/photos/moment-480.webp", Width: 480, Format: "webp", Bytes: 1234}}
	value, err := original.Value()
	require.NoError(t, err)

	var decoded MediaVariants
	require.NoError(t, decoded.Scan(value))
	require.Equal(t, original, decoded)
}

func TestMediaVariantsScanNullProducesEmptyCollection(t *testing.T) {
	var decoded MediaVariants
	require.NoError(t, decoded.Scan(nil))
	require.NotNil(t, decoded)
	require.Empty(t, decoded)
}
