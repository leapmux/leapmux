package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// The wire's `password_set` says whether a password SIGNS IN, which is what
// every client renders from it. The users.first_credential_exempt column says
// only that some flow claimed one, and the solo bootstrap claims one with an
// empty hash -- so a client that read the column offered a solo owner "Change
// Password" for the first password on the hub, and reported "Password
// changed." for a password nobody had ever set.
//
// The column keeps its own meaning for the ADMISSION rules, which is why this
// conversion is where the two part company. The two now have two NAMES, so a
// reader picks by type rather than by remembering which fact a shared name
// carried. See assertFirstCredentialAuthIsFresh.
func TestUserToProtoPasswordSetReadsTheHashNotTheClaim(t *testing.T) {
	hashed, err := password.Hash("correct-horse-battery-staple")
	require.NoError(t, err)

	cases := []struct {
		name  string
		user  store.User
		wants bool
	}{
		{
			name:  "the solo bootstrap: the claim is true and no password can verify",
			user:  store.User{FirstCredentialExempt: true, PasswordHash: ""},
			wants: false,
		},
		{
			name:  "a stored password verifies",
			user:  store.User{FirstCredentialExempt: true, PasswordHash: hashed},
			wants: true,
		},
		{
			name:  "a passkey-only or OAuth-only account holds the placeholder",
			user:  store.User{FirstCredentialExempt: false, PasswordHash: password.PlaceholderHash},
			wants: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wants, userToProto(&tc.user, 0).GetPasswordSet())
			// The administration surface answers the same question, so it
			// answers it the same way: an operator listing the solo account
			// must not read a password on an account nothing signs in to.
			assert.Equal(t, tc.wants, adminUserToProto(&tc.user).GetPasswordSet())
		})
	}
}
