package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/password"
)

// The two facts the rename separated. `first_credential_exempt` is a claim the
// creating flow makes; `HasUsablePassword` reads the stored hash. They disagree
// on exactly one account -- the solo bootstrap -- and that account is the reason
// a single name for both was a hazard.
func TestUserHasUsablePasswordIsNotTheExemptClaim(t *testing.T) {
	hashed, err := password.Hash("correct-horse-battery-staple")
	require.NoError(t, err)

	solo := User{FirstCredentialExempt: true, PasswordHash: ""}
	assert.False(t, solo.HasUsablePassword(),
		"the solo bootstrap claims the exemption with no hash behind it")
	assert.True(t, solo.FirstCredentialExempt,
		"and the claim is what routes it past the first-credential rule")

	withPassword := User{FirstCredentialExempt: true, PasswordHash: hashed}
	assert.True(t, withPassword.HasUsablePassword())

	passkeyOnly := User{FirstCredentialExempt: false, PasswordHash: password.PlaceholderHash}
	assert.False(t, passkeyOnly.HasUsablePassword(),
		"the placeholder hash can never verify")
}
