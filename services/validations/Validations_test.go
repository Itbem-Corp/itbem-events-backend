package services

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsValidationErrorSupportsWrappedErrors(t *testing.T) {
	err := fmt.Errorf("logo upload failed: %w", ValidationError{Msg: "unsupported image type: video/mp4"})

	assert.True(t, IsValidationError(err))
}

func TestIsValidationErrorRejectsRegularErrors(t *testing.T) {
	err := fmt.Errorf("storage unavailable")

	assert.False(t, IsValidationError(err))
}
