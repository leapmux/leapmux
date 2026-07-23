package userid

import (
	"log/slog"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew_Empty(t *testing.T) {
	u, ok := New("")
	assert.False(t, ok)
	assert.True(t, u.IsZero())
	assert.Equal(t, "", u.String())
}

func TestNew_NonEmpty(t *testing.T) {
	u, ok := New("user-1")
	require.True(t, ok)
	assert.False(t, u.IsZero())
	assert.Equal(t, "user-1", u.String())
}

func TestMustNew_PanicsOnEmpty(t *testing.T) {
	assert.Panics(t, func() { MustNew("") })
}

func TestMustNew_NonEmpty(t *testing.T) {
	assert.Equal(t, "u", MustNew("u").String())
}

func TestMatches_FailClosed(t *testing.T) {
	u := MustNew("user-1")
	assert.True(t, u.Matches("user-1"))
	assert.False(t, u.Matches("other"))
	assert.False(t, u.Matches(""), "blank stored id must deny")
	assert.False(t, UserID{}.Matches("user-1"), "zero UserID must deny")
	assert.False(t, UserID{}.Matches(""), "empty-vs-empty must deny")
}

func TestMatchesUser_FailClosed(t *testing.T) {
	a := MustNew("user-1")
	b := MustNew("user-1")
	c := MustNew("other")
	assert.True(t, a.MatchesUser(b))
	assert.False(t, a.MatchesUser(c))
	assert.False(t, a.MatchesUser(UserID{}), "zero other must deny")
	assert.False(t, UserID{}.MatchesUser(a), "zero self must deny")
	assert.False(t, UserID{}.MatchesUser(UserID{}), "zero-vs-zero must deny")
}

func TestLogValue_RoundTrip(t *testing.T) {
	u := MustNew("user-1")
	assert.Equal(t, slog.StringValue("user-1"), u.LogValue())
	assert.Equal(t, slog.StringValue(""), UserID{}.LogValue())
}

// TestUserIDIsNotComparable pins the zero-size func field that makes `a == b`
// and `map[UserID]T` compile errors.
//
// It is not decoration. Matches and MatchesUser both refuse an empty side, but
// `==` cannot: two zero values compare EQUAL, so an unminted caller would match
// another unminted one -- fail-open, in precisely the way this type exists to
// prevent, and reachable by an expression that reads perfectly natural. The
// only way to rule that out is to stop it compiling, and the only way to stop a
// future edit from quietly restoring it is to assert the property here.
func TestUserIDIsNotComparable(t *testing.T) {
	assert.False(t, reflect.TypeOf(UserID{}).Comparable(),
		"UserID must not be comparable: `==` is a third comparison route that matches empty against empty")
}

// TestUserIDIsOneWordWide guards the placement of that field. A zero-size field
// in LAST position makes Go pad the struct, so putting it at the end would
// silently double the size of every UserID in every struct and slice that holds
// one.
func TestUserIDIsOneWordWide(t *testing.T) {
	assert.Equal(t, reflect.TypeOf("").Size(), reflect.TypeOf(UserID{}).Size(),
		"UserID must be exactly as wide as the string it wraps")
}
