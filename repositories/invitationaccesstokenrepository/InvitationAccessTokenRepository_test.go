package invitationaccesstokenrepository

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateRSVPCodeUsesExpectedHumanSafeAlphabet(t *testing.T) {
	code, err := GenerateRSVPCode(64)
	require.NoError(t, err)
	assert.Len(t, code, 64)
	for _, character := range code {
		assert.Contains(t, rsvpCharset, string(character))
	}
	for _, ambiguous := range []string{"0", "O", "I", "1"} {
		assert.False(t, strings.Contains(code, ambiguous))
	}
}

func TestGenerateRSVPCodeRejectsInvalidLength(t *testing.T) {
	_, err := GenerateRSVPCode(0)
	require.Error(t, err)
}
