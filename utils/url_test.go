package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsAbsoluteURLLike(t *testing.T) {
	assert.True(t, IsAbsoluteURLLike("https://cdn.example.com/photo.webp"))
	assert.True(t, IsAbsoluteURLLike("http://cdn.example.com/photo.webp"))
	assert.True(t, IsAbsoluteURLLike("//cdn.example.com/photo.webp"))
	assert.True(t, IsAbsoluteURLLike("blob:https://app.example/id"))
	assert.True(t, IsAbsoluteURLLike("data:image/svg+xml;base64,PHN2Zy8+"))

	assert.False(t, IsAbsoluteURLLike(""))
	assert.False(t, IsAbsoluteURLLike("events/demo/photo.webp"))
	assert.False(t, IsAbsoluteURLLike("/storage/events/demo/photo.webp"))
	assert.False(t, IsAbsoluteURLLike("storage/events/demo/photo.webp"))
}
