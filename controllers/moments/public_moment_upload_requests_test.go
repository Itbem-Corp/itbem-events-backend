package moments

import (
	"encoding/json"
	"testing"

	"events-stocks/dtos"

	"github.com/stretchr/testify/require"
)

func TestPublicMomentUploadRequestsNormalizeLegacyJSONFields(t *testing.T) {
	t.Parallel()

	var personal publicMomentUploadURLRequest
	require.NoError(t, json.Unmarshal([]byte(`{
		"invitationToken":"invite-token",
		"fileName":"photo.jpg",
		"contentType":"image/jpeg",
		"fileSize":42
	}`), &personal))
	require.Equal(t, "invite-token", personal.invitationToken())
	require.Equal(t, "photo.jpg", personal.filename())
	require.Equal(t, "image/jpeg", personal.contentType())
	require.Equal(t, int64(42), personal.fileSize())

	var shared sharedMomentConfirmRequest
	require.NoError(t, json.Unmarshal([]byte(`{
		"s3Key":"moments/demo/raw/photo.jpg",
		"ContentType":"image/jpeg",
		"FileSize":42
	}`), &shared))
	require.Equal(t, "moments/demo/raw/photo.jpg", shared.objectKey())
	require.Equal(t, "image/jpeg", shared.contentType())
	require.Equal(t, int64(42), shared.fileSize())
}

func TestSharedMultipartCompleteRequestPrefersCanonicalParts(t *testing.T) {
	t.Parallel()

	request := sharedMultipartCompleteRequest{
		Parts:          []dtos.CompletedUploadPart{{PartNumber: 1, ETag: "canonical"}},
		CompletedParts: []dtos.CompletedUploadPart{{PartNumber: 2, ETag: "legacy"}},
	}

	parts := request.completedParts()
	require.Len(t, parts, 1)
	require.Equal(t, "canonical", parts[0].ETag)
}
