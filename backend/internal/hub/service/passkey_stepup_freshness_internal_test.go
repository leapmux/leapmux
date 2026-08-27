package service

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	hubwebauthn "github.com/leapmux/leapmux/internal/hub/webauthn"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// The first-credential admission is the one step-up that runs on session
// authority alone: an account with no password and no passkey has no
// credential to present. What it attaches is durable, silently usable, and
// enough to sign in later, so on an OAuth-only account a stolen cookie would
// otherwise become a permanent password of the attacker's choosing -- one
// that outlives both the cookie and any OAuth revocation.
//
// Recency is the step-up such an account CAN satisfy, because every
// authentication mints a fresh session row and AuthenticatedAt reads that
// row's created_at rather than its sliding expiry.

func freshUserInfo(sessionID string, authenticatedAt time.Time) *auth.UserInfo {
	return &auth.UserInfo{
		ID:              userid.MustNew("u-1"),
		Credential:      auth.SessionCredential(sessionID),
		AuthenticatedAt: authenticatedAt,
	}
}

func TestAssertFirstCredentialAuthIsFresh_AdmitsARecentSignIn(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	require.NoError(t, assertFirstCredentialAuthIsFresh(freshUserInfo("s-1", now), now, firstCredentialAuthFreshness))
	require.NoError(t, assertFirstCredentialAuthIsFresh(
		freshUserInfo("s-1", now.Add(-firstCredentialAuthFreshness+time.Second)), now, firstCredentialAuthFreshness),
		"just inside the window is still a recent sign-in")
}

func TestAssertFirstCredentialAuthIsFresh_RefusesAStaleSession(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()

	err := assertFirstCredentialAuthIsFresh(
		freshUserInfo("s-1", now.Add(-firstCredentialAuthFreshness-time.Second)), now, firstCredentialAuthFreshness)
	require.Error(t, err, "a cookie older than the window must not attach a first credential")
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "sign in again")

	// A day-old cookie is the actual attack shape.
	err = assertFirstCredentialAuthIsFresh(freshUserInfo("s-1", now.Add(-24*time.Hour)), now, firstCredentialAuthFreshness)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

// TestAssertFirstCredentialAuthIsFresh_RefusesABearer pins the credential
// kind. The hub mints an API or delegation token once, and it lives until a
// revoke ends it, so its creation time says nothing about who holds it now,
// and no human is present to re-authenticate. The rule refuses a bearer
// outright rather than giving it a window -- a freshly minted token would
// otherwise pass.
func TestAssertFirstCredentialAuthIsFresh_RefusesABearer(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()

	for name, cred := range map[string]auth.CredentialIdentity{
		"api":        auth.APICredential("tok-1"),
		"delegation": auth.DelegationCredential("tok-2", "w-1"),
	} {
		t.Run(name, func(t *testing.T) {
			info := &auth.UserInfo{
				ID:              userid.MustNew("u-1"),
				Credential:      cred,
				AuthenticatedAt: now, // Freshly minted, and still refused.
			}
			err := assertFirstCredentialAuthIsFresh(info, now, firstCredentialAuthFreshness)
			require.Error(t, err)
			assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
			assert.Contains(t, err.Error(), "from a browser")
		})
	}
}

// TestAssertFirstCredentialAuthIsFresh_RefusesAnUnknownCredential covers the
// zero value and the nil pointer. Both mean "this code cannot identify the
// credential", which must refuse rather than default to admitted.
func TestAssertFirstCredentialAuthIsFresh_RefusesAnUnknownCredential(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()

	require.Error(t, assertFirstCredentialAuthIsFresh(nil, now, firstCredentialAuthFreshness))

	zeroCredential := &auth.UserInfo{ID: userid.MustNew("u-1"), AuthenticatedAt: now}
	require.Error(t, assertFirstCredentialAuthIsFresh(zeroCredential, now, firstCredentialAuthFreshness))

	// A session whose row carries no creation time is unreadable, not fresh.
	noTimestamp := &auth.UserInfo{
		ID:         userid.MustNew("u-1"),
		Credential: auth.SessionCredential("s-1"),
	}
	err := assertFirstCredentialAuthIsFresh(noTimestamp, now, firstCredentialAuthFreshness)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sign in again")
}

// TestAssertFirstCredentialAuthIsFresh_ToleratesClockSkew keeps a session
// row stamped slightly ahead of this process from reading as stale. The
// window is a lower limit on recency, and a negative age is more recent
// than now, not less.
func TestAssertFirstCredentialAuthIsFresh_ToleratesClockSkew(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	assert.NoError(t, assertFirstCredentialAuthIsFresh(
		freshUserInfo("s-1", now.Add(2*time.Second)), now, firstCredentialAuthFreshness))
}

// TestStepUpMutationAuth_FirstCredentialRequiresFreshAuth exercises the
// admission itself, not only the predicate it calls. The account below is
// the shape the rule exists for: a verified email, no password, and no
// passkey, so the account can present nothing as a step-up and the session
// alone decides.
func TestStepUpMutationAuth_FirstCredentialRequiresFreshAuth(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrator().Migrate(ctx))

	userID := id.Generate()
	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID:            userID,
		Username:      "oauthonly",
		DisplayName:   "OAuth Only",
		Email:         "oauthonly@example.com",
		EmailVerified: true,
		PasswordSet:   false,
	}))
	user, err := st.Users().GetByID(ctx, userID)
	require.NoError(t, err)
	require.False(t, user.PasswordSet)

	svc := &UserService{store: st}
	info := func(authenticatedAt time.Time) *auth.UserInfo {
		return &auth.UserInfo{
			ID:              userid.MustNew(userID),
			Credential:      auth.SessionCredential("s-1"),
			AuthenticatedAt: authenticatedAt,
		}
	}

	admission, err := svc.stepUpMutationAuth(ctx, entryStepUp(info(time.Now().UTC())), user)
	require.NoError(t, err, "a session that just authenticated may set the first credential")
	assert.True(t, admission.firstCredential)

	admission, err = svc.stepUpMutationAuth(
		ctx, entryStepUp(info(time.Now().UTC().Add(-24*time.Hour))), user)
	require.Error(t, err, "a day-old session must not set the first credential")
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.True(t, admission.firstCredential,
		"the admission still records WHY it refused, so the locked re-check stays correct")

	// THE most important property of the whole change: an account with
	// nothing to elevate with must never be REQUIRED to elevate. If an
	// elevation were a precondition, this account could never attach its
	// first credential -- and could therefore never become elevatable, which
	// is a permanent refusal rather than a prompt.
	require.False(t, info(time.Now().UTC()).Elevated(time.Now()),
		"precondition: this session holds no elevation")
	admission, err = svc.stepUpMutationAuth(ctx, entryStepUp(info(time.Now().UTC())), user)
	require.NoError(t, err)
	assert.True(t, admission.firstCredential,
		"the first-credential rule is a SIBLING, reachable with no elevation at all")

	// And the OTHER half of "sibling": a live elevation admits this account
	// too, whatever its sign-in instant says.
	//
	// The OAuth re-authentication leg grants an elevation to EXACTLY this
	// account shape -- providerMayElevateAccount reads the same predicate
	// this branch does -- so with the first-credential rule as the only branch,
	// that leg could never help the two procedures it exists for. A user
	// proved a factor at their identity provider, came back, and the hub
	// refused them with the same message as before; only signing out and in
	// again worked.
	until := time.Now().UTC().Add(auth.ElevationWindow)
	stale := info(time.Now().UTC().Add(-24 * time.Hour))
	stale.Elevation = auth.NewElevation(&until, &until)
	require.True(t, stale.Elevated(time.Now().UTC()), "precondition: the session is elevated")

	admission, err = svc.stepUpMutationAuth(ctx, entryStepUp(stale), user)
	require.NoError(t, err, "a proven factor admits, however old the sign-in is")
	assert.False(t, admission.firstCredential,
		"admitted on the ELEVATED branch, so the locked re-check verifies the window")

	// The refusal a prompt CAN resolve carries the marker that opens one.
	// Without it the client printed the sentence as raw text beside a form
	// and offered nothing.
	_, err = svc.stepUpMutationAuth(
		ctx, entryStepUp(info(time.Now().UTC().Add(-24*time.Hour))), user)
	require.Error(t, err)
	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, "1", connectErr.Meta().Get(ElevationRequiredHeader),
		"a stale sign-in is now resolvable by proving a factor, so it must open a prompt")
}

// TestStepUpMutationAuth_ElevationRequiredOnceACredentialExists is the
// other side of the same fork: as soon as the account holds something to
// prove, the session must prove it.
func TestStepUpMutationAuth_ElevationRequiredOnceACredentialExists(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrator().Migrate(ctx))

	userID := id.Generate()
	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID:            userID,
		Username:      "haspassword",
		DisplayName:   "Has Password",
		Email:         "haspassword@example.com",
		EmailVerified: true,
		PasswordHash:  "hash",
		PasswordSet:   true,
	}))
	user, err := st.Users().GetByID(ctx, userID)
	require.NoError(t, err)

	svc := &UserService{store: st}
	base := &auth.UserInfo{
		ID:              userid.MustNew(userID),
		Credential:      auth.SessionCredential("s-1"),
		AuthenticatedAt: time.Now().UTC(),
	}

	// Un-elevated: refused, and with FailedPrecondition rather than
	// Unauthenticated -- the frontend's interceptor reads Unauthenticated
	// as "signed out" and would discard the session the user is about to
	// prove a factor for.
	_, err = svc.stepUpMutationAuth(ctx, entryStepUp(base), user)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	// Elevated: admitted, and NOT as a first-credential admission, so the
	// locked re-check does not pay for a count query.
	elevatedInfo := *base
	now := time.Now().UTC()
	elevatedInfo.Elevation = auth.Elevation{ExpiresAt: now.Add(time.Hour)}
	admission, err := svc.stepUpMutationAuth(ctx, entryStepUp(&elevatedInfo), user)
	require.NoError(t, err)
	assert.False(t, admission.firstCredential)

	// A LAPSED elevation is refused: the deadline is compared at the point
	// of use, so a cached UserInfo cannot keep granting past its window.
	lapsed := *base
	lapsed.Elevation = auth.Elevation{ExpiresAt: now.Add(-time.Hour)}
	_, err = svc.stepUpMutationAuth(ctx, entryStepUp(&lapsed), user)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	// An ELEVATED command-line credential is ADMITTED, on the same branch a
	// session takes. It proves its factor in a browser through the
	// /oauth/step-up leg and carries a window of its own, so
	// refusing it here would answer one question two ways -- every other
	// protected surface admits it. The whole path below is credential-shaped:
	// recheckActingCredentialUnderLock re-reads whichever row carries the
	// window, and revokeOtherCredentialsPreservingActingCredential keeps
	// whichever row asked.
	cli := *base
	cli.Credential = auth.APICredential("tok-1")
	cli.Elevation = auth.Elevation{ExpiresAt: now.Add(time.Hour)}
	cliAdmission, err := svc.stepUpMutationAuth(ctx, entryStepUp(&cli), user)
	require.NoError(t, err)
	assert.False(t, cliAdmission.firstCredential,
		"admitted on the ELEVATED branch, so the locked re-check verifies the window")

	// UN-elevated, it is refused WITH the marker: the CLI runs the browser
	// leg and retries, rather than reporting a refusal it cannot act on.
	unelevatedCLI := *base
	unelevatedCLI.Credential = auth.APICredential("tok-1")
	_, err = svc.stepUpMutationAuth(ctx, entryStepUp(&unelevatedCLI), user)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	var cliErr *connect.Error
	require.ErrorAs(t, err, &cliErr)
	assert.Equal(t, "1", cliErr.Meta().Get(ElevationRequiredHeader))

	// A LAPSED window on the same credential is refused the same way.
	lapsedCLI := *base
	lapsedCLI.Credential = auth.APICredential("tok-1")
	lapsedCLI.Elevation = auth.Elevation{ExpiresAt: now.Add(-time.Hour)}
	_, err = svc.stepUpMutationAuth(ctx, entryStepUp(&lapsedCLI), user)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	// A DELEGATION bearer cannot be elevated even carrying the field: a
	// worker mints it for an agent that reads untrusted input, and there is
	// nobody present to re-authenticate it.
	delegated := *base
	delegated.Credential = auth.DelegationCredential("del-1", "worker-1")
	delegated.Elevation = auth.Elevation{ExpiresAt: now.Add(time.Hour)}
	_, err = svc.stepUpMutationAuth(ctx, entryStepUp(&delegated), user)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	// And it is NOT offered a step-up prompt. The marker means "prove a
	// factor and retry", and this caller has nothing to prove: a prompt would
	// collect a factor and then refuse the retry for the same reason.
	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	assert.Empty(t, connectErr.Meta().Get(ElevationRequiredHeader))

	// The FIRST-CREDENTIAL shape is the one case that still refuses a
	// command-line credential, and it refuses it without a marker. The
	// account holds nothing to elevate with, so the elevated branch has no
	// window to read and the rule rests on a recent SIGN-IN instead -- which
	// a credential the hub minted once and lets live until a revoke does not
	// have. assertFirstCredentialAuthIsFresh is the refusal.
	shell := id.Generate()
	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID:            shell,
		Username:      "nocredential",
		DisplayName:   "No Credential",
		Email:         "nocredential@example.com",
		EmailVerified: true,
		PasswordSet:   false,
	}))
	shellUser, err := st.Users().GetByID(ctx, shell)
	require.NoError(t, err)
	freshCLI := &auth.UserInfo{
		ID:              userid.MustNew(shell),
		Credential:      auth.APICredential("tok-2"),
		AuthenticatedAt: now,
	}
	_, err = svc.stepUpMutationAuth(ctx, entryStepUp(freshCLI), shellUser)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	var shellErr *connect.Error
	require.ErrorAs(t, err, &shellErr)
	assert.Empty(t, shellErr.Meta().Get(ElevationRequiredHeader))
}

// TestFirstCredentialCeremonyWindowOutlastsOneCeremony pins the
// relationship the Finish leg depends on, not a literal value.
//
// Finish re-runs the admission its own Begin already passed, against the
// same fixed sign-in instant. One window evaluated at both legs cannot make
// the guarantee for ANY value: Finish refuses a Begin admitted at the last
// moment, after the user answered the biometric prompt, and the hub discards
// the credential the authenticator created. The Finish window must
// therefore exceed the entry window by at least one whole ceremony.
func TestFirstCredentialCeremonyWindowOutlastsOneCeremony(t *testing.T) {
	t.Parallel()

	// Through the methods the handlers call, never through a constant only
	// this test reads: a window that no production path takes its value
	// from is a second source of truth, and this assertion would keep
	// passing while the handlers applied a different rule.
	assert.GreaterOrEqual(t,
		finishStepUp(nil).firstCredentialWindow()-entryStepUp(nil).firstCredentialWindow(),
		hubwebauthn.CeremonyTTL,
		"the Finish window must cover the whole ceremony its Begin admitted")
}

// TestFirstCredentialAdmittedAtBeginSurvivesFinish is the same property
// driven through the two predicates the two legs actually call.
func TestFirstCredentialAdmittedAtBeginSurvivesFinish(t *testing.T) {
	t.Parallel()

	signedInAt := time.Now().UTC()
	// The user opens the dialog with a second of the entry window left.
	beginAt := signedInAt.Add(firstCredentialAuthFreshness - time.Second)
	require.NoError(t,
		assertFirstCredentialAuthIsFresh(freshUserInfo("s-1", signedInAt), beginAt, firstCredentialAuthFreshness),
		"precondition: Begin admits this session")

	// The biometric prompt takes as long as the hub still accepts the
	// ceremony for.
	finishAt := beginAt.Add(hubwebauthn.CeremonyTTL)
	assert.NoError(t,
		assertFirstCredentialAuthIsFresh(freshUserInfo("s-1", signedInAt), finishAt, finishStepUp(nil).firstCredentialWindow()),
		"a ceremony the hub still accepts must not be refused after the user answered the prompt")

	// The wider window is still a window: a day-old cookie stays refused.
	assert.Error(t,
		assertFirstCredentialAuthIsFresh(freshUserInfo("s-1", signedInAt), signedInAt.Add(24*time.Hour), finishStepUp(nil).firstCredentialWindow()))
}

// TestElevationAdmittedAtBeginSurvivesFinish is the SAME property on the
// other branch of the fork, which is the half that had no grace at all.
//
// An account that holds a password takes the elevation branch, so Finish
// refused a Begin admitted in the last seconds of the two-hour window
// -- after the user answered the biometric prompt. The client's gate re-runs
// the WHOLE action on that refusal, so it opened a second ceremony and the
// hub never stored the credential the authenticator created on the first.
func TestElevationAdmittedAtBeginSurvivesFinish(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrator().Migrate(ctx))

	userID := id.Generate()
	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID: userID, Username: "ceremonygrace", DisplayName: "Ceremony Grace",
		Email: "ceremonygrace@example.com", EmailVerified: true,
		PasswordHash: "hash", PasswordSet: true,
	}))
	user, err := st.Users().GetByID(ctx, userID)
	require.NoError(t, err)

	provenAt := time.Now().UTC()
	// The user opens the dialog with a second of the window left.
	beginAt := provenAt.Add(auth.ElevationWindow - time.Second)
	// The biometric prompt takes as long as the hub still accepts the
	// ceremony for, so the window closes while it is on screen.
	finishAt := beginAt.Add(hubwebauthn.CeremonyTTL)

	info := &auth.UserInfo{
		ID:         userid.MustNew(userID),
		Credential: auth.SessionCredential("s-1"),
		Elevation:  auth.Elevation{ExpiresAt: provenAt.Add(auth.ElevationWindow)},
	}
	at := func(now time.Time) *UserService {
		return &UserService{store: st, clockSeam: clockSeam{Now: func() time.Time { return now }}}
	}

	_, err = at(beginAt).stepUpMutationAuth(ctx, entryStepUp(info), user)
	require.NoError(t, err, "precondition: Begin admits this session")

	_, err = at(finishAt).stepUpMutationAuth(ctx, entryStepUp(info), user)
	require.Error(t, err, "precondition: the window really did close during the ceremony")

	_, err = at(finishAt).stepUpMutationAuth(ctx, finishStepUp(info), user)
	assert.NoError(t, err,
		"a ceremony the hub still accepts must not be refused after the user answered the prompt")

	// The grace is still a limit: a window that lapsed long ago stays refused.
	_, err = at(provenAt.Add(24*time.Hour)).stepUpMutationAuth(ctx, finishStepUp(info), user)
	assert.Error(t, err)
}

// TestFirstCredentialDurableIdentityNeedsAnAddress pins the half of the
// durable-identity rule the flag alone cannot carry.
//
// email_verified says somebody confirmed THIS address. The two can
// separate: resolveEmailVerified excludes a CLEARED address from the lowering
// rule on purpose, so an administrator who clears a verified address leaves
// the flag raised over an empty column. Read alone, that raised flag then
// admitted a session-only first password or passkey -- durable, silently
// usable, and enough to sign in later -- on an account with no address, no
// password, no passkey and no link.
func TestFirstCredentialDurableIdentityNeedsAnAddress(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrator().Migrate(ctx))

	seed := func(t *testing.T, username, email string, verified bool) *store.User {
		t.Helper()
		uid := id.Generate()
		require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
			ID: uid, Username: username, DisplayName: username, Email: email, EmailVerified: verified,
		}))
		user, err := st.Users().GetByID(ctx, uid)
		require.NoError(t, err)
		return user
	}

	// The flag with no address: refused.
	stranded := seed(t, "stranded", "", true)
	err = assertFirstCredentialWithoutPasswordAllowed(ctx, st, stranded)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	// The flag WITH an address: admitted, which is the rule this guards.
	confirmed := seed(t, "confirmed", "confirmed@example.com", true)
	assert.NoError(t, assertFirstCredentialWithoutPasswordAllowed(ctx, st, confirmed))

	// No address and no flag, but a live OAuth link: admitted through the
	// other branch, so the narrowing left that route open.
	linked := seed(t, "linked", "", false)
	require.NoError(t, st.OAuthProviders().Create(ctx, store.CreateOAuthProviderParams{
		ID: "gh", ProviderType: "github", Name: "GitHub", ClientID: "cid",
		ClientSecret: []byte("secret"), Enabled: true,
	}))
	require.NoError(t, st.OAuthUserLinks().Create(ctx, store.CreateOAuthUserLinkParams{
		UserID: userid.MustNew(linked.ID), ProviderID: "gh", ProviderSubject: "sub-1",
	}))
	assert.NoError(t, assertFirstCredentialWithoutPasswordAllowed(ctx, st, linked))
}
