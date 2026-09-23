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

func TestShort_Length(t *testing.T) {
	assert.Len(t, Short(), 13)
}

func TestShort_ValidCharacters(t *testing.T) {
	valid := regexp.MustCompile(`^[a-z0-9]+$`)
	for range 100 {
		s := Short()
		assert.True(t, valid.MatchString(s), "short id contains invalid characters: %q", s)
	}
}

func TestShort_Unique(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for range 1000 {
		s := Short()
		assert.False(t, seen[s], "Short repeated %q", s)
		seen[s] = true
	}
}
