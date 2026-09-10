package testutil

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/password"
)

func BenchmarkFixturePasswordHash(b *testing.B) {
	b.Run("uncached", func(b *testing.B) {
		for b.Loop() {
			if _, err := password.Hash("fixture-password"); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("cached", func(b *testing.B) {
		FixturePasswordHash(b, "fixture-password")
		for b.Loop() {
			FixturePasswordHash(b, "fixture-password")
		}
	})
}

func TestFixturePasswordHashSeparatesPasswords(t *testing.T) {
	first := FixturePasswordHash(t, "fixture-first")
	second := FixturePasswordHash(t, "fixture-second")
	for _, tc := range []struct {
		hash, plain string
		valid       bool
	}{
		{first, "fixture-first", true},
		{second, "fixture-second", true},
		{first, "fixture-second", false},
		{second, "fixture-first", false},
	} {
		valid, err := password.Verify(tc.hash, tc.plain)
		require.NoError(t, err)
		require.Equal(t, tc.valid, valid)
	}
}

func TestFixturePasswordHashReusesConcurrentRequests(t *testing.T) {
	const count = 16
	var hashes [count]string
	t.Run("requests", func(t *testing.T) {
		for i := range count {
			t.Run(strconv.Itoa(i), func(t *testing.T) {
				t.Parallel()
				hashes[i] = FixturePasswordHash(t, "fixture-concurrent")
			})
		}
	})
	for _, hash := range hashes {
		require.Equal(t, hashes[0], hash, "each caller must receive the same salted hash")
	}
	valid, err := password.Verify(hashes[0], "fixture-concurrent")
	require.NoError(t, err)
	require.True(t, valid)
}
