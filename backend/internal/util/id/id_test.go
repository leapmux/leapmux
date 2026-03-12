package id

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGenerate_Length(t *testing.T) {
	id := Generate()
	assert.Len(t, id, 48)
}

func TestGenerate_ValidCharacters(t *testing.T) {
	valid := regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	id := Generate()
	assert.True(t, valid.MatchString(id), "id contains invalid characters: %q", id)
}

func TestGenerate_Unique(t *testing.T) {
	a := Generate()
	b := Generate()
	assert.NotEqual(t, a, b, "two consecutive calls produced the same ID")
}
