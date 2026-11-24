package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLookupByIDPrefix(t *testing.T) {
	ids := []string{"abc123", "abc456", "abc", "xyz789"}
	identity := func(id string) string { return id }

	got, matches, ok := lookupByIDPrefix(ids, "abc", identity)
	assert.True(t, ok)
	assert.Equal(t, "abc", got, "an exact id wins over a prefix that other ids share")
	assert.Nil(t, matches)

	got, _, ok = lookupByIDPrefix(ids, "xyz", identity)
	assert.True(t, ok)
	assert.Equal(t, "xyz789", got, "a unique prefix selects its item")

	got, matches, ok = lookupByIDPrefix(ids, "abc4", identity)
	assert.True(t, ok)
	assert.Equal(t, "abc456", got)
	assert.Nil(t, matches)

	got, matches, ok = lookupByIDPrefix([]string{"abc123", "abc456"}, "abc", identity)
	assert.False(t, ok)
	assert.Empty(t, got)
	assert.Equal(t, []string{"abc123", "abc456"}, matches, "an ambiguous prefix reports every match")

	_, matches, ok = lookupByIDPrefix(ids, "nope", identity)
	assert.False(t, ok)
	assert.Empty(t, matches, "no match reports no candidate")

	_, matches, ok = lookupByIDPrefix(nil, "abc", identity)
	assert.False(t, ok)
	assert.Empty(t, matches)
}
