package service_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// The deadline the hub reports on a SLIDE.
//
// A grant reports its deadline in the response body, so a client learns the
// window it just proved. Every sensitive action after that slides the window
// forward, and the slide deliberately emits no user_info event -- a cache
// still holding the shorter deadline fails closed. The client therefore keeps
// showing the deadline it adopted hours ago, up to a whole auth.ElevationWindow
// early, on the one screen the operating documentation points a user at when
// they step away from a shared machine.
//
// ElevationExpiresAtHeader is what closes that. These tests pin three things:
// a verb that slides reports, a verb that does not slide reports nothing, and
// the value is the deadline the STORE holds rather than the one the slide
// asked for.

// signupKey is a real settings key whose write path takes the elevation gate.
func signupKey() string { return settings.KeySignupEnabled.Name() }

// writeOneSetting performs one real write verb, authenticated by `authorize`,
// and returns the response so a test can read its header.
func writeOneSetting(t *testing.T, env *adminSettingsEnv, authorize requestAuth) *connect.Response[leapmuxv1.UpdateSettingResponse] {
	t.Helper()
	resp, err := env.client.UpdateSetting(context.Background(),
		authorized(&leapmuxv1.UpdateSettingRequest{Key: signupKey(), PartialJson: `true`}, authorize))
	require.NoError(t, err)
	return resp
}

// storedSessionDeadline reads the elevation deadline the session row holds
// now. The tests compare the reported value against THIS rather than against
// their own arithmetic, because the store is what the hub enforces.
func storedSessionDeadline(t *testing.T, st store.Store, sessionID string) time.Time {
	t.Helper()
	row, err := st.Sessions().GetByID(context.Background(), sessionID, time.Now())
	require.NoError(t, err)
	require.NotNil(t, row.ElevationExpiresAt, "the session must still carry a window")
	return *row.ElevationExpiresAt
}

// storedTokenDeadline is the api_tokens twin of storedSessionDeadline.
func storedTokenDeadline(t *testing.T, st store.Store, tokenID string) time.Time {
	t.Helper()
	row, err := st.APITokens().GetByID(context.Background(), tokenID)
	require.NoError(t, err)
	require.NotNil(t, row.ElevationExpiresAt, "the credential must still carry a window")
	return *row.ElevationExpiresAt
}

// aheadOfTheStamp is a clock the service reads that sits a minute past the
// instant the fixture stamped the elevation.
//
// A FIXED instant, and deliberately not the wall clock. The slide statement
// refuses a deadline no later than the stored one, and every dialect stores
// these columns at millisecond resolution -- so a test whose request lands in
// the same millisecond as its own elevate call would slide nothing and report
// nothing, on a machine fast enough. Whole seconds, so the value survives the
// stored layout unchanged and the expected string can be written out.
func aheadOfTheStamp() time.Time {
	return time.Now().UTC().Truncate(time.Second).Add(time.Minute)
}

// TestElevationSlideReportsTheStoredDeadline is the happy path: a restricted
// verb slides the window, and its response carries the deadline the store now
// holds.
func TestElevationSlideReportsTheStoredDeadline(t *testing.T) {
	env := setupAdminSettingsTest(t, &config.Config{})
	base := aheadOfTheStamp()
	env.svc.Now = func() time.Time { return base }

	resp := writeOneSetting(t, env, cookieAuth(env.token))

	reported := resp.Header().Get(service.ElevationExpiresAtHeader)
	require.NotEmpty(t, reported, "a verb that slides the window must report the new deadline")

	stored := storedSessionDeadline(t, env.st, env.token)
	assert.Equal(t, stored.UTC().Format(time.RFC3339Nano), reported,
		"the reported deadline must be the one the store holds")
	// And the store moved, so the assertion above is not comparing two copies
	// of a value that never changed: an unslid window would still read as the
	// fixture's own stamp.
	assert.Equal(t, base.Add(2*time.Hour).UTC().Format(time.RFC3339Nano), reported,
		"the slide extends the window by auth.ElevationWindow from the acting clock")
}

// TestElevationSlideReportIsRFC3339InUTC pins the WIRE FORMAT, which the
// frontend parses with the browser's own Date and no format of its own.
func TestElevationSlideReportIsRFC3339InUTC(t *testing.T) {
	env := setupAdminSettingsTest(t, &config.Config{})
	base := aheadOfTheStamp()
	env.svc.Now = func() time.Time { return base }

	reported := writeOneSetting(t, env, cookieAuth(env.token)).Header().Get(service.ElevationExpiresAtHeader)
	require.NotEmpty(t, reported)

	parsed, err := time.Parse(time.RFC3339Nano, reported)
	require.NoError(t, err, "the value must parse as RFC 3339")
	assert.Equal(t, "Z", reported[len(reported)-1:],
		"the value must be in UTC, so no reader has to guess the zone")
	assert.True(t, parsed.After(base), "the reported deadline must still be in the future")
}

// TestElevationSlideReportIsClampedToTheCap is the case that decides HOW the
// deadline is obtained.
//
// The slide asks for now + auth.ElevationWindow and the statement clamps it
// against the stored elevation_proven_at plus store.ElevationMaxTotal, in SQL,
// because Go never reads that anchor. A request in the last stretch of an
// eight-hour elevation therefore gets the ceiling. Reporting what the slide
// ASKED for would promise two hours the hub will not honour -- the same defect
// this header exists to correct, pointing the other way, and the direction
// that fails open.
func TestElevationSlideReportIsClampedToTheCap(t *testing.T) {
	env := setupAdminSettingsTestUnelevated(t, &config.Config{})
	base := time.Now().UTC().Truncate(time.Second)
	owner, ok := userid.New(env.adminID)
	require.True(t, ok)
	// Seven hours into an eight-hour elevation, with half an hour left to run.
	n, err := env.st.Sessions().Elevate(context.Background(), store.ElevateSessionParams{
		SessionID:          env.token,
		UserID:             owner,
		ElevationProvenAt:  base.Add(-7 * time.Hour),
		ElevationExpiresAt: base.Add(30 * time.Minute),
	}, time.Now())
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	env.svc.Now = func() time.Time { return base }

	reported := writeOneSetting(t, env, cookieAuth(env.token)).Header().Get(service.ElevationExpiresAtHeader)
	require.NotEmpty(t, reported)

	ceiling := base.Add(time.Hour)
	assert.Equal(t, ceiling.UTC().Format(time.RFC3339Nano), reported,
		"the report must be the ceiling the statement wrote, not the deadline the slide asked for")
	assert.NotEqual(t, base.Add(2*time.Hour).UTC().Format(time.RFC3339Nano), reported,
		"reporting the requested deadline would promise a window the hub refuses to honour")
	assert.Equal(t, storedSessionDeadline(t, env.st, env.token).UTC().Format(time.RFC3339Nano), reported)
}

// TestElevationSlideReportsACommandLineCredentialsOwnRow covers the other
// elevatable row. A command-line credential carries a window of its own and
// slides it exactly as a session does, so it must be reported the same way.
func TestElevationSlideReportsACommandLineCredentialsOwnRow(t *testing.T) {
	env := setupAdminSettingsTest(t, &config.Config{})
	bearer, tokenID := env.adminBearer(t)
	hubtestutil.ElevateAPIToken(t, env.st, tokenID, env.adminID)
	base := aheadOfTheStamp()
	env.svc.Now = func() time.Time { return base }

	reported := writeOneSetting(t, env, bearerAuth(bearer)).Header().Get(service.ElevationExpiresAtHeader)
	require.NotEmpty(t, reported, "a command-line credential slides its own row, so it reports too")

	assert.Equal(t, storedTokenDeadline(t, env.st, tokenID).UTC().Format(time.RFC3339Nano), reported)
	assert.Equal(t, base.Add(2*time.Hour).UTC().Format(time.RFC3339Nano), reported)
}

// TestElevationSlideReportsNothingOnAReadVerb is the other half of the rule.
// A read slides nothing, so it must report nothing: a client that adopted a
// header from a listing would be adopting a value no write produced.
func TestElevationSlideReportsNothingOnAReadVerb(t *testing.T) {
	env := setupAdminSettingsTest(t, &config.Config{})

	resp, err := env.client.ListSettings(context.Background(),
		authedReq(&leapmuxv1.ListSettingsRequest{}, env.token))
	require.NoError(t, err)

	assert.Empty(t, resp.Header().Get(service.ElevationExpiresAtHeader),
		"a verb that does not slide the window must report no deadline")
}

// TestElevationSlideReportsNothingOnARefusal keeps the report off the one
// answer that must not carry it. An un-elevated session slid nothing, and a
// client that adopted a deadline here would render a window it does not hold.
func TestElevationSlideReportsNothingOnARefusal(t *testing.T) {
	env := setupAdminSettingsTestUnelevated(t, &config.Config{})

	_, err := env.client.UpdateSetting(context.Background(),
		authorized(&leapmuxv1.UpdateSettingRequest{Key: signupKey(), PartialJson: `true`}, cookieAuth(env.token)))
	require.Error(t, err)

	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, "1", connectErr.Meta().Get(service.ElevationRequiredHeader),
		"the refusal is still the one a step-up prompt can clear")
	assert.Empty(t, connectErr.Meta().Get(service.ElevationExpiresAtHeader),
		"a refused request slid nothing, so it must report no deadline")
}

// TestElevationExpiresAtHeaderValueIsPinned keeps the Go constant and the
// frontend's copy from drifting silently, for the reason
// TestElevationRequiredHeaderValueIsPinned states: the frontend cannot import
// this package, so only the literal below and the comment on each side join
// the two. A rename that changes one and not the other leaves every client
// showing a deadline up to two hours early again, with nothing failing.
//
// It writes the literal out rather than compares it to the constant, so
// renaming the constant alone does not make this pass.
func TestElevationExpiresAtHeaderValueIsPinned(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "Leapmux-Elevation-Expires-At", service.ElevationExpiresAtHeader,
		"frontend/src/api/transport.ts keys on the lowercased form of this value")
}
