package storetest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
)

func (s *Suite) testTableHoldsText(t *testing.T) {
	st := s.NewStore(t)
	user := SeedUser(t, st, "textneedle")
	SeedOAuthProvider(t, st, "blobneedle")
	require.NoError(t, st.PasskeyCredentials().Create(ctx, store.CreatePasskeyCredentialParams{
		ID:           id.Generate(),
		UserID:       user.ID,
		CredentialID: []byte("cred-blobneedle"),
		PublicKey:    []byte("pk-blobneedle"),
		Transports:   "[]",
		FriendlyName: "key",
		CreatedAt:    time.Now().UTC(),
	}))
	for _, tc := range []struct {
		table, text string
		want        bool
	}{
		{"users", "textneedle", true},
		{"passkey_credentials", "cred-blobneedle", true},
		{"users", "hash-textneedle", false},
		{"oauth_providers", "secret-blobneedle", false},
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
