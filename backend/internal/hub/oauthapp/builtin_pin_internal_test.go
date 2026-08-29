package oauthapp

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/authscope"
)

// The built-in ceilings hand-spell the grantable vocabulary, and nothing else
// links them to it: a scope added to scope.proto would silently never reach
// the control CLI's or the service account's registration. This pin makes
// that day a test failure instead, so widening a built-in ceiling stays a
// conscious decision.
func TestBuiltInCeilingsMatchTheGrantableVocabulary(t *testing.T) {
	// Order-insensitive: the constant stays in enum order (its own doc calls
	// it a constant of the build), while SortedTokens is alphabetical. What
	// must not drift is the SET of tokens.
	want := strings.Join(authscope.EveryGrantableScope().SortedTokens(), " ")
	got := ControlCLIScopes
	assert.ElementsMatch(t, strings.Fields(want), strings.Fields(got),
		"the control CLI's ceiling must list every grantable token; update it beside scope.proto")
	assert.Equal(t, ControlCLIScopes, ServiceAccountScopes,
		"the service account's ceiling documents itself as the whole grantable vocabulary")
	require.NotEmpty(t, want)
}
