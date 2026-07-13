package dtos

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewMediaProcessMessagePopulatesLegacyAndNeutralKeys(t *testing.T) {
	msg := NewMediaProcessMessage("moment-1", "event-1", "moments/event/raw/file.jpg", "bucket", "image/jpeg", false)

	assert.Equal(t, "moments/event/raw/file.jpg", msg.ObjectKey)
	assert.Equal(t, "moments/event/raw/file.jpg", msg.RawS3Key)
	assert.Equal(t, msg.ObjectKey, msg.StorageObjectKey())
}

func TestMediaProcessMessageStorageObjectKeyFallsBackToRawS3Key(t *testing.T) {
	var msg MediaProcessMessage
	err := json.Unmarshal([]byte(`{"moment_id":"m1","event_id":"e1","raw_s3_key":"moments/e1/raw/m1.jpg"}`), &msg)
	require.NoError(t, err)

	assert.Empty(t, msg.ObjectKey)
	assert.Equal(t, "moments/e1/raw/m1.jpg", msg.StorageObjectKey())
}

func TestMediaProcessMessageStorageObjectKeyPrefersObjectKey(t *testing.T) {
	var msg MediaProcessMessage
	err := json.Unmarshal([]byte(`{"moment_id":"m1","event_id":"e1","object_key":"moments/e1/raw/new.jpg","raw_s3_key":"moments/e1/raw/old.jpg"}`), &msg)
	require.NoError(t, err)

	assert.Equal(t, "moments/e1/raw/new.jpg", msg.StorageObjectKey())
}

func TestMediaProcessMessageJobIdentityRoundTrip(t *testing.T) {
	msg := NewMediaProcessMessage("m1", "e1", "moments/e1/raw/file.jpg", "bucket", "image/jpeg", false)
	msg.JobID = "job-1"
	msg.Generation = 4

	encoded, err := json.Marshal(msg)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"job_id":"job-1"`)
	assert.Contains(t, string(encoded), `"generation":4`)

	var decoded MediaProcessMessage
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.Equal(t, msg.JobID, decoded.JobID)
	assert.Equal(t, msg.Generation, decoded.Generation)
}
