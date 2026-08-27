package usernames_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/hub/usernames"
)

func TestIsReservedSystem(t *testing.T) {
	for _, in := range []string{"solo", "SOLO", "  solo  ", "Solo"} {
		assert.True(t, usernames.IsReservedSystem(in), "expected reserved: %q", in)
	}
	for _, in := range []string{"admin", "owner", "alice", ""} {
		assert.False(t, usernames.IsReservedSystem(in), "expected allowed: %q", in)
	}
}

func TestIsReservedForSignup_PublicMode(t *testing.T) {
	for _, in := range []string{"solo", "SOLO", "admin", "ADMIN", "  admin  ", "Solo"} {
		assert.True(t, usernames.IsReservedForSignup(in, false), "expected reserved: %q", in)
	}
	for _, in := range []string{"owner", "alice", ""} {
		assert.False(t, usernames.IsReservedForSignup(in, false), "expected allowed: %q", in)
	}
}

// Setup mode exempts Admin and nothing else. The first administrator claims
// the conventional name; Solo stays refused because the hazard it carries is
// a property of the database, not of the flow that wrote the row.
func TestIsReservedForSignup_SetupMode(t *testing.T) {
	for _, in := range []string{"admin", "ADMIN", "  admin  ", "Admin", "owner", "alice", ""} {
		assert.False(t, usernames.IsReservedForSignup(in, true), "expected allowed: %q", in)
	}
	for _, in := range []string{"solo", "SOLO", "  solo  ", "Solo"} {
		assert.True(t, usernames.IsReservedForSignup(in, true), "expected reserved: %q", in)
	}
}
