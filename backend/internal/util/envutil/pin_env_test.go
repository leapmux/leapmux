package envutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPinEnv_ReplacesInheritedValue(t *testing.T) {
	got := PinEnv([]string{"PATH=/usr/bin", "TERM=dumb"}, "TERM=xterm-256color")
	assert.Equal(t, []string{"PATH=/usr/bin", "TERM=xterm-256color"}, got,
		"the inherited entry must be gone, not merely outranked")
}

// Windows env names are case-insensitive, and a worker can inherit `Path` or
// `Term` from a non-Go parent. Matching must fold, or the pin layers.
func TestPinEnv_CaseInsensitiveLikeFilterEnv(t *testing.T) {
	got := PinEnv([]string{"term=dumb", "TeRm=also-dumb"}, "TERM=xterm-256color")
	assert.Equal(t, []string{"TERM=xterm-256color"}, got)
}

func TestPinEnv_AppendsWhenNothingToDisplace(t *testing.T) {
	got := PinEnv([]string{"PATH=/usr/bin"}, "TERM=xterm-256color")
	assert.Equal(t, []string{"PATH=/usr/bin", "TERM=xterm-256color"}, got)
}

func TestPinEnv_MultipleAssignments(t *testing.T) {
	got := PinEnv(
		[]string{"A=old", "KEEP=yes", "B=old"},
		"A=new", "B=new", "C=new",
	)
	assert.Equal(t, []string{"KEEP=yes", "A=new", "B=new", "C=new"}, got)
}

// An entry with no '=' names no key to displace. It must not be silently
// dropped, and it must not filter everything by matching the empty name.
func TestPinEnv_AssignmentWithoutEquals(t *testing.T) {
	got := PinEnv([]string{"PATH=/usr/bin", "TERM=dumb"}, "BAREWORD")
	assert.Equal(t, []string{"PATH=/usr/bin", "TERM=dumb", "BAREWORD"}, got)
}

func TestPinEnv_EmptyInputs(t *testing.T) {
	assert.Empty(t, PinEnv(nil))
	assert.Equal(t, []string{"A=1"}, PinEnv(nil, "A=1"))

	// No assignments -> identity copy, not a view onto the input.
	in := []string{"A=1"}
	got := PinEnv(in)
	require.Equal(t, in, got)
	got[0] = "MUTATED=yes"
	assert.Equal(t, "A=1", in[0], "PinEnv must not alias its input")
}

// PinEnv appends onto FilterEnv's result. If that slice were ever backed by the
// caller's array with spare capacity, the append would scribble on it.
func TestPinEnv_DoesNotAliasCallerSlice(t *testing.T) {
	in := make([]string, 2, 8)
	in[0], in[1] = "A=1", "B=2"

	got := PinEnv(in, "C=3")

	assert.Equal(t, []string{"A=1", "B=2", "C=3"}, got)
	assert.Equal(t, []string{"A=1", "B=2"}, in, "the caller's slice must be untouched")
}

func TestValuesFor_ReturnsEveryValueInOrder(t *testing.T) {
	got := ValuesFor([]string{"A=1", "B=x", "a=2", "A=3"}, "A")
	assert.Equal(t, []string{"1", "2", "3"}, got,
		"every match in order, folding case -- this is what distinguishes a pin from a layered pin")
}

func TestValuesFor_NoMatch(t *testing.T) {
	assert.Empty(t, ValuesFor([]string{"A=1"}, "ZZZ"))
	assert.Empty(t, ValuesFor(nil, "A"))
}

// An entry without '=' has the whole entry as its name and an empty value --
// the same reading FilterEnv and HasKey use.
func TestValuesFor_MalformedEntryMatchesWithEmptyValue(t *testing.T) {
	assert.Equal(t, []string{""}, ValuesFor([]string{"NOEQUALS"}, "NOEQUALS"))
}

// HasKey is now implemented on top of ValuesFor, so asserting it AGREES with
// ValuesFor would be true by construction and could never fail. These are the
// literal answers instead -- they catch a regression in the fold semantics or
// in how a malformed entry is read, whichever helper introduces it.
func TestHasKey_MatchesNameCaseInsensitively(t *testing.T) {
	env := []string{"A=1", "b=2", "NOEQUALS"}

	assert.True(t, HasKey(env, "A"))
	assert.True(t, HasKey(env, "a"), "matching folds case")
	assert.True(t, HasKey(env, "B"), "matching folds case in the other direction too")
	assert.True(t, HasKey(env, "NOEQUALS"), "an entry without '=' is all name")
	assert.False(t, HasKey(env, "missing"))
	assert.False(t, HasKey(env, ""), "the empty name must not match every entry")
}
