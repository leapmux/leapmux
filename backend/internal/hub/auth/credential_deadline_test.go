package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestCredentialDeadline_States(t *testing.T) {
	now := time.Now()
	at := DeadlineAt(now.Add(time.Hour))

	// Unset is the zero value.
	var zero CredentialDeadline
	assert.True(t, zero.IsUnset())
	assert.Equal(t, UnsetDeadline(), zero)
	assert.False(t, zero.IsNever())
	_, ok := zero.At()
	assert.False(t, ok)

	assert.True(t, NeverExpires().IsNever())
	assert.False(t, NeverExpires().IsUnset())
	_, ok = NeverExpires().At()
	assert.False(t, ok)

	gotAt, ok := at.At()
	assert.True(t, ok)
	assert.Equal(t, now.Add(time.Hour), gotAt)
	assert.False(t, at.IsUnset())
	assert.False(t, at.IsNever())

	// A zero instant maps to NeverExpires, centralizing "zero time == never".
	assert.True(t, DeadlineAt(time.Time{}).IsNever())
}

func TestCredentialDeadline_IsCurrent(t *testing.T) {
	now := time.Now()

	// No finite deadline (Unset or NeverExpires) never expires -- the zero-value
	// UserInfo.CredentialExpiresAt must read as never-expires, exactly as the old
	// zero time.Time did.
	assert.True(t, UnsetDeadline().IsCurrent(now), "Unset (zero value) must be current")
	assert.True(t, NeverExpires().IsCurrent(now))

	// At(t) is an exclusive upper bound: current strictly before t.
	assert.True(t, DeadlineAt(now.Add(time.Minute)).IsCurrent(now))
	assert.False(t, DeadlineAt(now).IsCurrent(now), "now == deadline is expired (exclusive)")
	assert.False(t, DeadlineAt(now.Add(-time.Minute)).IsCurrent(now))
}

func TestCredentialDeadline_Later(t *testing.T) {
	now := time.Now()
	early := DeadlineAt(now.Add(time.Minute))
	late := DeadlineAt(now.Add(time.Hour))

	// Two At deadlines: the later instant wins (order-independent).
	assert.Equal(t, late, early.Later(late))
	assert.Equal(t, late, late.Later(early))

	// A deadline with no finite instant (Never or the zero-value Unset) is most
	// permissive, so it wins against any finite At.
	assert.Equal(t, NeverExpires(), NeverExpires().Later(late))
	assert.Equal(t, NeverExpires(), late.Later(NeverExpires()))
	assert.Equal(t, NeverExpires(), UnsetDeadline().Later(late))
	assert.Equal(t, NeverExpires(), late.Later(UnsetDeadline()))
}

func TestCredentialDeadline_AdoptReschedule(t *testing.T) {
	now := time.Now()
	early := DeadlineAt(now.Add(time.Minute))
	late := DeadlineAt(now.Add(time.Hour))

	// Two At deadlines: never regress a later finite deadline to an earlier one
	// (the out-of-order concurrent-extension case). Adopt a later one.
	assert.Equal(t, late, late.AdoptReschedule(early), "must not regress a later deadline to an earlier one")
	assert.Equal(t, late, early.AdoptReschedule(late), "a genuine extension is adopted")

	// A clear (NeverExpires) is always adopted -- it is the most permissive.
	assert.Equal(t, NeverExpires(), late.AdoptReschedule(NeverExpires()))
	// A never -> finite transition is adopted verbatim (current has no finite
	// instant to regress from).
	assert.Equal(t, late, NeverExpires().AdoptReschedule(late))
	// The first reschedule off the zero-value Unset adopts the incoming verbatim
	// (Unset has no finite instant to protect), including a clear.
	assert.Equal(t, late, UnsetDeadline().AdoptReschedule(late))
	assert.Equal(t, NeverExpires(), UnsetDeadline().AdoptReschedule(NeverExpires()))
	assert.Equal(t, UnsetDeadline(), UnsetDeadline().AdoptReschedule(UnsetDeadline()))

	// Equal instants: no regression, result carries the same instant.
	same := DeadlineAt(now.Add(time.Minute))
	assert.Equal(t, early, early.AdoptReschedule(same))
}

func TestCredentialDeadline_Equal(t *testing.T) {
	now := time.Now()
	assert.True(t, UnsetDeadline().Equal(UnsetDeadline()))
	assert.True(t, NeverExpires().Equal(NeverExpires()))
	assert.False(t, UnsetDeadline().Equal(NeverExpires()), "Unset and NeverExpires are distinct kinds")
	assert.True(t, DeadlineAt(now).Equal(DeadlineAt(now)))
	assert.False(t, DeadlineAt(now).Equal(DeadlineAt(now.Add(time.Second))))
	assert.False(t, DeadlineAt(now).Equal(NeverExpires()))
}
