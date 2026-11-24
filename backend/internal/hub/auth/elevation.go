package auth

import (
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect/v2"

	"github.com/leapmux/leapmux/internal/hub/store"
)

// ElevationWindow is how long one proven step-up factor admits sensitive
// actions ("sudo mode"). Every sensitive action slides it forward, so an
// uninterrupted work session asks for one factor, not one per action.
//
// Two hours matches what comparable products settle on. It is deliberately
// long enough that a user who adds a passkey, renames it and then changes
// their password answers one prompt, and short enough that a browser left
// open over lunch is no longer elevated.
const ElevationWindow = 2 * time.Hour

// FreshAuthenticationWindow is how long a sign-in that proved a factor
// counts as fresh authentication. A sensitive action inside this window
// passes without a step-up, and the first such action slides the ordinary
// ElevationWindow forward from the sign-in instant.
const FreshAuthenticationWindow = 5 * time.Minute

// SignInProof states whether a sign-in proved a factor. CreateSession
// refuses the zero value so a forgotten write fails instead of silently
// granting no window.
type SignInProof int

const (
	// SignInProvedNoFactor is a sign-in that proved no factor, such as an
	// OAuth handoff or a provider sign-in without re-authentication.
	SignInProvedNoFactor SignInProof = 1
	// SignInProvedFactor is a sign-in that proved a factor: a password, a
	// passkey, or a provider sign-in for a consent route that asked for
	// fresh authentication. The provider sign-in counts only for an account
	// that elevates only through that provider, one holding neither a
	// password nor a passkey. A consent-route sign-in for any other account
	// proves nothing here: its factor is the password or the passkey, and
	// the provider sign-in did not present it.
	SignInProvedFactor SignInProof = 2
	// SignInProvedFactorWithFullWindow is the solo first-password setup. The
	// local operator already controls the Hub, and the existing contract grants
	// the ordinary two-hour window in the session's insert transaction.
	SignInProvedFactorWithFullWindow SignInProof = 3
)

// ElevationMaxTotal caps the total life of ONE elevation, measured from the
// instant the factor was proven rather than from the last slide.
//
// The cap is what stops a sliding window from becoming a permanent
// privilege: without it a user (or a stolen cookie acting for them) who
// performs one sensitive action every two hours stays elevated for ever.
// Eight hours is a working day, so a genuine all-day session reaches the
// ceiling and nothing shorter does.
//
// An ALIAS of the store's constant, never a second copy. The store is what
// enforces the cap -- the slide statement clamps against the stored anchor in
// SQL, and the grant clamps before it writes -- and it cannot import this
// package, because this package imports it. Two independent 8-hour literals
// would let the value Go reports and the value SQL enforces drift apart.
const ElevationMaxTotal = store.ElevationMaxTotal

// Elevation is a session's step-up state: the sliding deadline, and nothing
// else. The zero value means "never elevated", which is why there is no
// separate boolean -- a caller cannot forget to check it, because the only
// question the type answers already accounts for it.
//
// The deadline is carried RAW, and the predicate takes now at the point of
// use. A precomputed boolean would take its value when the cache stored the
// UserInfo, so a cache entry minted just before the deadline would keep granting for
// the rest of its life. Taking now here means a stale entry can only prompt
// slightly early -- it can never grant falsely.
//
// The anchor instant is not a FIELD here. elevation_proven_at is what the
// absolute cap is measured from, and the slide statement applies that cap in
// SQL, against the stored column, so nothing in Go compares it. NewElevation
// still READS it, at construction, for the both-or-neither guard below.
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
// tidb_enable_check_constraint is ON. The Hub logs a failed attempt to turn
// that variable on and checks its value, but permits startup when enforcement
// stays disabled. A connection user without SYSTEM_VARIABLES_ADMIN can leave
// enforcement disabled despite those logs.
//
// A lone elevation_expires_at would then admit a sensitive action on a row
// whose factor was never proven, and no Go path could see that it read
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
// now. The upper limit is exclusive, matching IsExpired: a credential is
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
	if u.Credential.ElevatableRef().IsZero() {
		return time.Time{}, false
	}
	return u.Elevation.Deadline(now)
}

// Elevated reports whether this request may perform a sensitive action. It
// is ElevationDeadline without the instant, for the callers that only need
// the yes-or-no answer.
func (u *UserInfo) Elevated(now time.Time) bool {
	_, ok := u.ElevationDeadline(now)
	return ok
}

// ErrElevationRequired is the SENTINEL every elevation refusal wraps, so
// errors.Is answers for all of them however each one is worded. There is no
// rate-limit budget keyed on it, because being un-elevated is not a failed
// attempt.
var ErrElevationRequired = errors.New("this action needs a recent sign-in")

// elevationRequiredMessage is the remedy every caller but one states.
const elevationRequiredMessage = "verify your identity and try again"

// ElevationRequiredError refuses a sensitive action on a session that is not
// elevated.
//
// FailedPrecondition, deliberately, NOT Unauthenticated. The frontend's
// global interceptor treats Unauthenticated as "your session ended" and
// signs the user out, so the one refusal whose remedy is "prove a factor and
// try again" would instead discard the session the user is about to prove
// a factor for.
//
// The wording is elevationRequiredMessage, the default every caller but one
// takes. See ElevationRequiredErrorSaying for the exception.
func ElevationRequiredError() error {
	return ElevationRequiredErrorSaying(elevationRequiredMessage)
}

// ElevationRequiredErrorSaying is the same refusal with different wording,
// for the one caller whose remedy is WIDER than "prove a factor": the
// first-credential rule, where verifying at an identity provider AND signing
// in again both admit, and only that rule can say so.
//
// The message is a required parameter rather than a variadic. A variadic
// accepts two strings and silently ignores all but the first, which is a call
// a reader cannot get right from the signature.
//
// It still WRAPS ErrElevationRequired, so errors.Is keeps answering for every
// refusal of this kind. Replacing the cause left the sentinel true of five
// refusals and false of the sixth, with nothing but the header to distinguish
// them.
//
// The refusal WRAPS ErrElevationRequired, which is also what decides the
// ElevationRequiredHeader marker: see the elevation response interceptor.
// The marker is not set here, because connect v2 carries no metadata on an
// error and this function reaches no response header. Keying the marker on
// the sentinel instead of on construction is a stronger contract than the
// v1 marker-on-the-error was: every refusal of this kind carries it, and a
// caller cannot forget to add it.
func ElevationRequiredErrorSaying(message string) error {
	// The message is what the caller reads; the wrapped sentinel travels as
	// the cause, so errors.Is still answers and the interceptor can key the
	// marker on it. connect.NewError takes a string and serializes only
	// that, so the wrap has to be attached separately or the sentinel
	// dissolves into prose.
	cause := fmt.Errorf("%w: %s", ErrElevationRequired, message)
	return connect.NewError(connect.CodeFailedPrecondition, cause.Error()).WithCause(cause)
}

// RequireElevation refuses a sensitive action on a session that did not
// prove a factor inside the window.
//
// This is the PLAIN gate, and every sensitive action outside passkey
// management uses it. It has no first-credential branch, because it needs
// none: an account that can sign in holds at least one of a password, a
// passkey, or an OAuth link, and each of the three elevates.
//
// The two refusals are DIFFERENT, and the difference is the marker rather
// than the wording. A session that did not prove a factor gets
// ElevationRequiredError, which carries ElevationRequiredHeader and means
// "prove one and retry". A credential that can never carry an elevation --
// a delegation bearer, which a worker mints for an agent with nobody at a
// keyboard -- gets the plain refusal from RequireElevatableCredential, with
// NO marker, because a step-up prompt cannot help it: it would ask for a
// factor and then refuse the retry for the same reason.
func RequireElevation(userInfo *UserInfo, now time.Time) error {
	// Solo mode has no ceremony to prove a factor with, so the rule has
	// nothing to decide.
	if userInfo.SoloAuthenticated() {
		return nil
	}
	if userInfo.Elevated(now) {
		return nil
	}
	// Not elevated. Either the credential could be and is not, or it never
	// can be; only the second has no remedy the caller can act on here.
	if err := RequireElevatableCredential(userInfo); err != nil {
		return err
	}
	if userInfo.Credential.AppElevationDisabled() {
		return AppElevationDisabledError()
	}
	return ElevationRequiredError()
}

// AppElevationDisabledError refuses a command-line credential whose app owner
// disabled every step-up ceremony. It deliberately does not wrap
// ErrElevationRequired: a verification prompt cannot recover this request.
func AppElevationDisabledError() error {
	return connect.NewError(connect.CodeFailedPrecondition,
		"the application owner disabled identity verification for this credential; enable app elevation and try again")
}

// RequireElevatableCredential refuses a credential that can carry no
// elevation at all.
//
// TWO kinds can: a session cookie, and a command-line credential, which
// proves its factor through the browser path at /oauth/step-up. Each has a
// row to stamp and a person who can be prompted. A
// DELEGATION bearer has neither -- a worker mints it for an agent that reads
// untrusted input -- and neither does solo mode, which authenticates a
// synthetic user against no row.
//
// The refusal deliberately carries NO ElevationRequiredHeader. The marker
// means "prove a factor and retry", and there is nothing this caller can
// prove: a step-up prompt would collect a factor and then refuse the retry
// for the same reason. See RequireElevation, which chooses between the two
// refusals.
//
// Nil-safe, like every sibling in this family. RequireElevation reaches it
// through UserInfo.Elevated, which is documented nil-safe, so a nil that
// slipped past would turn a refusal into a handler panic here.
func RequireElevatableCredential(userInfo *UserInfo) error {
	if userInfo != nil {
		if !userInfo.Credential.ElevatableRef().IsZero() {
			return nil
		}
	}
	return CredentialNotElevatableError()
}

// RequireElevatableSession returns the acting SESSION id, refusing every
// other credential -- a command-line credential included.
//
// The narrower rule, used by the browser elevation RPCs. Those RPCs collect a
// factor and stamp a session row: a bearer that could call them would elevate
// ITSELF, which is exactly the property browser verification at /oauth/step-up
// exists to prevent (the factor must be proven somewhere the credential file
// cannot reach).
//
// Nil-safe, like every sibling in this family.
func RequireElevatableSession(userInfo *UserInfo) (string, error) {
	var sessionID string
	if userInfo != nil {
		sessionID = userInfo.Credential.SessionID()
	}
	if sessionID == "" {
		return "", CredentialNotElevatableError()
	}
	return sessionID, nil
}

// CredentialNotElevatableError is the refusal both elevatable-credential
// rules return. One constructor, because the two rules differ in WHICH
// credentials they admit and not in what they tell a caller that holds none
// of them: the remedy is the same sentence, and two copies of a security
// message drift the moment somebody rewords one.
func CredentialNotElevatableError() error {
	return connect.NewError(connect.CodeFailedPrecondition,
		"this credential cannot verify your identity; sign in from a browser to perform this action")
}
