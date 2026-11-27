package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli"
)

func TestNormalizeIDHandle(t *testing.T) {
	got, err := normalizeIDHandle("  abc123  ", "request handle")
	require.NoError(t, err)
	assert.Equal(t, "abc123", got)

	got, err = normalizeIDHandle("id:abc123", "request handle")
	require.NoError(t, err)
	assert.Equal(t, "abc123", got)

	got, err = normalizeIDHandle(" id:  tok-1111 ", "request handle")
	require.NoError(t, err)
	assert.Equal(t, "tok-1111", got)

	for _, raw := range []string{"", "   "} {
		_, err = normalizeIDHandle(raw, "request handle")
		require.Error(t, err)
		e := cli.FromError(err)
		assert.Equal(t, cli.CodeUsage, e.Code)
		assert.Contains(t, e.Message, "the request handle is empty")
	}

	for _, raw := range []string{"id:", "id:   ", " id: "} {
		_, err = normalizeIDHandle(raw, "input id")
		require.Error(t, err)
		e := cli.FromError(err)
		assert.Equal(t, cli.CodeUsage, e.Code)
		assert.Contains(t, e.Message, `missing input id after "id:"`)
	}

	// Prefix matching stays enabled after stripping "id:".
	ids := []string{"abc123", "abc456", "abc", "xyz789"}
	identity := func(id string) string { return id }

	h, err := normalizeIDHandle("id:xyz", "request handle")
	require.NoError(t, err)
	matched, _, ok := lookupByIDPrefix(ids, h, identity)
	assert.True(t, ok)
	assert.Equal(t, "xyz789", matched)

	h, err = normalizeIDHandle("id:abc", "request handle")
	require.NoError(t, err)
	matched, _, ok = lookupByIDPrefix(ids, h, identity)
	assert.True(t, ok)
	assert.Equal(t, "abc", matched)

	h, err = normalizeIDHandle("id:xy", "request handle")
	require.NoError(t, err)
	_, _, ok = lookupByIDPrefix(ids, h, identity)
	assert.False(t, ok, "prefix shorter than minHandlePrefixLength does not match after stripping id:")
}

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

	shortIDs := []string{"ab123", "ab", "cd456"}
	got, matches, ok = lookupByIDPrefix(shortIDs, "ab", identity)
	assert.True(t, ok, "an exact id shorter than minHandlePrefixLength still matches")
	assert.Equal(t, "ab", got)
	assert.Nil(t, matches)

	got, matches, ok = lookupByIDPrefix(shortIDs, "cd", identity)
	assert.False(t, ok, "a non-exact prefix shorter than minHandlePrefixLength never matches")
	assert.Empty(t, got)
	assert.Empty(t, matches)
}
