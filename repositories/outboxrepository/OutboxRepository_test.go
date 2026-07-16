package outboxrepository

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRetryDelayBacksOffAndCaps(t *testing.T) {
	assert.Equal(t, 2*time.Second, retryDelay(1))
	assert.Equal(t, 10*time.Second, retryDelay(2))
	assert.Equal(t, 30*time.Second, retryDelay(3))
	assert.Equal(t, 2*time.Minute, retryDelay(4))
	assert.Equal(t, 2*time.Minute, retryDelay(100))
}
