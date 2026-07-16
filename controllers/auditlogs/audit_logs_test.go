package auditlogs

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPositiveIntBoundsPagination(t *testing.T) {
	assert.Equal(t, 50, positiveInt("", 50, 100))
	assert.Equal(t, 50, positiveInt("-2", 50, 100))
	assert.Equal(t, 25, positiveInt("25", 50, 100))
	assert.Equal(t, 100, positiveInt("999", 50, 100))
}
