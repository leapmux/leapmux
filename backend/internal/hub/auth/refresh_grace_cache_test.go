package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRefreshGraceCache_PutGet(t *testing.T) {
	cache, err := NewRefreshGraceCache(60 * time.Second)
	require.NoError(t, err)

	require.NoError(t, cache.Put("tok-1", "lmx_tok-1_access", "lmx_tok-1_refresh"))

	got1, got2, err := cache.Get("tok-1")
	require.NoError(t, err)
	assert.Equal(t, "lmx_tok-1_access", got1)
	assert.Equal(t, "lmx_tok-1_refresh", got2)
}

func TestRefreshGraceCache_TokenIDMixupIsAuthFailure(t *testing.T) {
	// AAD binds the token id; reading a different id must miss rather
	// than returning the wrong pair.
	cache, err := NewRefreshGraceCache(60 * time.Second)
	require.NoError(t, err)
	require.NoError(t, cache.Put("tok-A", "accessA", "refreshA"))

	_, _, err = cache.Get("tok-B")
	assert.ErrorIs(t, err, ErrGraceCacheMiss)
}

func TestRefreshGraceCache_Expired(t *testing.T) {
	cache, err := NewRefreshGraceCache(10 * time.Millisecond)
	require.NoError(t, err)
	require.NoError(t, cache.Put("tok-1", "a", "b"))
	time.Sleep(20 * time.Millisecond)
	_, _, err = cache.Get("tok-1")
	assert.ErrorIs(t, err, ErrGraceCacheMiss)
}

func TestRefreshGraceCache_Evict(t *testing.T) {
	cache, err := NewRefreshGraceCache(60 * time.Second)
	require.NoError(t, err)
	require.NoError(t, cache.Put("tok-1", "a", "b"))
	cache.Evict("tok-1")
	_, _, err = cache.Get("tok-1")
	assert.ErrorIs(t, err, ErrGraceCacheMiss)
}

func TestRefreshGraceCache_Overwrite(t *testing.T) {
	cache, err := NewRefreshGraceCache(60 * time.Second)
	require.NoError(t, err)
	require.NoError(t, cache.Put("tok-1", "a1", "b1"))
	require.NoError(t, cache.Put("tok-1", "a2", "b2"))

	g1, g2, err := cache.Get("tok-1")
	require.NoError(t, err)
	assert.Equal(t, "a2", g1)
	assert.Equal(t, "b2", g2)
}
