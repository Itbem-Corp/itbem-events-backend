package services

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"events-stocks/repositories/awsrepository"
	validations "events-stocks/services/validations"
	"github.com/stretchr/testify/assert"
)

func TestUploadErrorResponse(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantDetail string
	}{
		{"size", validations.ValidationError{Msg: "file size exceeds 10 MB"}, http.StatusRequestEntityTooLarge, "file size exceeds 10 MB"},
		{"type", validations.ValidationError{Msg: "unsupported file type: text/html"}, http.StatusBadRequest, "unsupported file type: text/html"},
		{"validation", validations.ValidationError{Msg: "invalid upload key"}, http.StatusBadRequest, "invalid upload key"},
		{"temporary storage", fmt.Errorf("cover: %w", &awsrepository.StorageError{Operation: "upload", Kind: "unavailable", Err: errors.New("SlowDown")}), http.StatusServiceUnavailable, "Media storage is temporarily unavailable. Please retry."},
		{"region", &awsrepository.StorageError{Operation: "upload", Kind: "region", Err: errors.New("PermanentRedirect")}, http.StatusInternalServerError, "Media storage is using the wrong bucket region. The server configuration must be corrected."},
		{"unknown", errors.New("secret internal error"), http.StatusInternalServerError, "The upload could not be completed. Please retry."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, detail := UploadErrorResponse(tt.err)
			assert.Equal(t, tt.wantStatus, status)
			assert.Equal(t, tt.wantDetail, detail)
		})
	}
}
