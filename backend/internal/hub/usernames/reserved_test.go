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

func TestIsReservedPublic(t *testing.T) {
	for _, in := range []string{"admin", "ADMIN", "  admin  ", "Admin"} {
		assert.True(t, usernames.IsReservedPublic(in), "expected reserved: %q", in)
	}
	for _, in := range []string{"solo", "owner", "alice", ""} {
		assert.False(t, usernames.IsReservedPublic(in), "expected allowed: %q", in)
	}
}

func TestIsReservedForPublicSignup(t *testing.T) {
	for _, in := range []string{"solo", "SOLO", "admin", "ADMIN", "  admin  ", "Solo"} {
		assert.True(t, usernames.IsReservedForPublicSignup(in), "expected reserved: %q", in)
	}
	for _, in := range []string{"owner", "alice", ""} {
		assert.False(t, usernames.IsReservedForPublicSignup(in), "expected allowed: %q", in)
	}
}
