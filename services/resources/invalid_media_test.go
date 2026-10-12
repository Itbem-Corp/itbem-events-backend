package services

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// STU-43: a text file renamed .png is a 400 invalid_media_file, not a 500.
func TestDetectUploadContentTypeRejectsTextRenamedAsImage(t *testing.T) {
	content := []byte("esto no es una imagen, solo texto plano\n")
	file := &memoryMultipartFile{Reader: bytes.NewReader(content)}

	_, err := detectUploadContentType(file, newTestFileHeader("foto.png", "image/png", int64(len(content))))

	require.Error(t, err)
	assert.True(t, IsInvalidMediaFile(err))
	status, detail := UploadErrorResponse(err)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, InvalidMediaFileCode, detail)
	assert.Equal(t, InvalidMediaFileMessage, UploadErrorMessage(err, "Failed"))
}

func TestDetectUploadContentTypeKeepsRealImagesAndGenericProbes(t *testing.T) {
	jpegBytes := validTestJPEG(t)
	got, err := detectUploadContentType(&memoryMultipartFile{Reader: bytes.NewReader(jpegBytes)}, newTestFileHeader("foto.png", "image/png", int64(len(jpegBytes))))
	require.NoError(t, err)
	assert.Equal(t, "image/jpeg", got)

	// HEIC is not sniffable: the declared allowlisted type is kept.
	heic := []byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'h', 'e', 'i', 'c'}
	got, err = detectUploadContentType(&memoryMultipartFile{Reader: bytes.NewReader(heic)}, newTestFileHeader("foto.heic", "image/heic", int64(len(heic))))
	require.NoError(t, err)
	assert.Equal(t, "image/heic", got)
}

func TestUploadErrorMessageFallsBackForOtherErrors(t *testing.T) {
	assert.Equal(t, "Failed", UploadErrorMessage(errors.New("boom"), "Failed"))
	assert.False(t, IsInvalidMediaFile(errors.New("boom")))
}

// Review S5: only a decode failure is the file's fault; other optimizer
// failures are server errors and keep the generic upload message.
func TestOptimizerUploadErrorSeparatesDecodeFromServerFailures(t *testing.T) {
	decode := optimizerUploadError("image/png", fmt.Errorf("%w: VipsForeignLoad: not a PNG file", ErrImageDecode))
	if !IsInvalidMediaFile(decode) {
		t.Fatalf("decode failure must be invalid_media_file, got %v", decode)
	}
	for _, cause := range []error{
		errors.New("failed to read image: unexpected EOF"),
		errors.New("image format not supported on this system: image/heic"),
		errors.New("image optimization failed: vips: out of memory"),
	} {
		err := optimizerUploadError("image/png", cause)
		if IsInvalidMediaFile(err) {
			t.Fatalf("%v must not be invalid_media_file", cause)
		}
		if got := UploadErrorMessage(err, "fallback"); got != "fallback" {
			t.Fatalf("server failure must keep the generic message, got %q", got)
		}
	}
}
