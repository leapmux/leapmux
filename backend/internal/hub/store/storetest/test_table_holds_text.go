package storetest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testTableHoldsText(t *testing.T) {
	st := s.NewStore(t)
	SeedUser(t, st, "textneedle")
	SeedOAuthProvider(t, st, "blobneedle")
	for _, tc := range []struct {
		table, text string
		want        bool
	}{
		{"users", "hash-textneedle", true},
		{"oauth_providers", "secret-blobneedle", true},
		{"users", "a value that is absent", false},
	} {
		t.Run(tc.text, func(t *testing.T) {
			found, err := st.TestHelper().TableHoldsText(ctx, tc.table, tc.text)
			require.NoError(t, err)
			assert.Equal(t, tc.want, found)
		})
	}
	_, err := st.TestHelper().TableHoldsText(ctx, "users; DELETE FROM users", "needle")
	require.ErrorContains(t, err, "unknown application table")
}
