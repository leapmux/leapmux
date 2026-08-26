package auth

import "time"

// ElevationWindow is how long one proven step-up factor admits sensitive
// actions ("sudo mode"). Every sensitive action slides it forward, so an
// uninterrupted work session asks for one factor, not one per action.
//
// Two hours matches what comparable products settle on. It is deliberately
// long enough that a user who adds a passkey, renames it and then changes
// their password answers one prompt, and short enough that a browser left
// open over lunch is no longer elevated.
const ElevationWindow = 2 * time.Hour

// ElevationMaxTotal caps the total life of ONE elevation, measured from the
// instant the factor was proven rather than from the last slide.
//
// The cap is what stops a sliding window from becoming a permanent
// privilege: without it a user (or a stolen cookie acting for them) who
// performs one sensitive action every two hours stays elevated for ever.
// Eight hours is a working day, so the ceiling is reached by a genuine
// all-day session and not by anything shorter.
const ElevationMaxTotal = 8 * time.Hour

// Elevation is a session's step-up state: the sliding deadline, and nothing
// else. The zero value means "never elevated", which is why there is no
// separate boolean -- a caller cannot forget to check it, because the only
// question the type answers already accounts for it.
//
// The deadline is carried RAW, and the predicate takes now at the point of
// use. A precomputed boolean would be decided when the UserInfo was CACHED,
// so a cache entry minted just before the deadline would keep granting for
// the rest of its life. Taking now here means a stale entry can only prompt
// slightly early -- it can never grant falsely.
//
// The anchor instant is not a FIELD here. elevation_proven_at is what the
// absolute cap is measured from, and that cap is applied in SQL by the slide
// statement, against the stored column, so nothing in Go compares it. It is
// still READ, at construction, for the both-or-neither guard below.
type Elevation struct {
	ExpiresAt time.Time
}

// NewElevation builds an elevation from the stored PAIR. A missing half means
// the session is not elevated, so the value is zero.
//
// Both columns, and the guard is here rather than only in the schema. The
// schema states it as
// CHECK ((elevation_proven_at IS NULL) = (elevation_expires_at IS NULL)) on
// user_sessions, and that is the right home for the WRITE side -- half a pair
// is unwritable rather than tolerated at every read. But this is the ADMISSION
// side, and a CHECK is not a guarantee it can stand on: the mysql dialect's
// own migration header records that TiDB parses and IGNORES a CHECK unless
// tidb_enable_check_constraint is ON, and the hub's attempt to turn that
// variable on discards its error, so a connection user without
// SYSTEM_VARIABLES_ADMIN leaves enforcement off in silence.
//
// A lone elevation_expires_at would then admit a sensitive action on a row
// whose factor was never proven, and no Go path could see that it was reading
// half a pair. The slide statement's own "elevation_proven_at IS NOT NULL"
// already refuses to EXTEND such a row; this is the same rule on the read that
// grants.
//
// No Go write path can produce a half pair -- Elevate sets both and
// DropElevation nulls both -- so the shape this refuses comes from outside the
// hub: a manual repair, a restore, or a future query somebody adds. Refusing
// it costs nothing and fails in the safe direction, which is the one property
// a step-up predicate must have.
func NewElevation(provenAt, expiresAt *time.Time) Elevation {
	if provenAt == nil || expiresAt == nil {
		return Elevation{}
	}
	return Elevation{ExpiresAt: expiresAt.UTC()}
}

// IsZero reports whether the session was never elevated.
func (e Elevation) IsZero() bool { return e.ExpiresAt.IsZero() }

// IsCurrent reports whether the elevation still admits a sensitive action at
// now. The upper bound is exclusive, matching IsExpired: a credential is
// invalid AT the recorded instant, not one clock tick afterward.
func (e Elevation) IsCurrent(now time.Time) bool {
	return !e.IsZero() && now.Before(e.ExpiresAt)
}

// Deadline returns the deadline to report to the client, and whether the
// elevation is current at now. A lapsed or absent elevation reports the zero
// time, so a client never renders a deadline that already passed.
func (e Elevation) Deadline(now time.Time) (time.Time, bool) {
	if !e.IsCurrent(now) {
		return time.Time{}, false
	}
	return e.ExpiresAt, true
}

// ElevationDeadline reports the deadline a client should show and whether
// the request may perform a sensitive action. Nil-safe: an unauthenticated
// request is never elevated.
//
// The credential check is structural, not defensive. Two kinds have a row to
// stamp and a person who can be prompted -- a session cookie, and a
// command-line credential, which proves its factor through the browser
// step-up leg. Every other kind carries the zero value and would be refused
// anyway, but stating the requirement here keeps the rule readable at the one
// place it is enforced.
//
// A DELEGATION bearer is the kind this excludes, and excluding it is the
// whole reason the test is on the credential rather than on the Elevation
// value: a worker mints it for an agent that reads untrusted input, and there
// is nobody present to re-authenticate it.
//
// The predicate and the reported deadline are ONE computation, so the value
// a client renders can never disagree with the value the hub enforces.
func (u *UserInfo) ElevationDeadline(now time.Time) (time.Time, bool) {
	if u == nil {
		return time.Time{}, false
	}
	if _, _, ok := u.Credential.ElevatableRow(); !ok {
		return time.Time{}, false
	}
	return u.Elevation.Deadline(now)
}

// Elevated reports whether this request may perform a sensitive action. It
// is ElevationDeadline without the instant, for the callers that only gate.
func (u *UserInfo) Elevated(now time.Time) bool {
	_, ok := u.ElevationDeadline(now)
	return ok
}
