package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newStepUpTestStore(t *testing.T) store.Store {
	t.Helper()
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrator().Migrate(context.Background()))
	return st
}

func stepUpUser(t *testing.T, st store.Store, passwordSet bool) *store.User {
	t.Helper()
	userID := id.Generate()
	require.NoError(t, st.Users().Create(context.Background(), store.CreateUserParams{
		ID:           userID,
		Username:     "stepup" + userID[:8],
		PasswordHash: "hash",
		DisplayName:  "Step Up",
		PasswordSet:  passwordSet,
		// A VERIFIED address, because that is the durable identity the
		// first-credential rule requires and the locked re-check now
		// re-derives. A fixture without one is refused for a reason no case
		// here is about.
		Email:         "stepup" + userID[:8] + "@example.test",
		EmailVerified: true,
	}))
	u, err := st.Users().GetByID(context.Background(), userID)
	require.NoError(t, err)
	return u
}

// firstCredentialSession seeds an UN-elevated session whose authentication is
// fresh: the exact credential the first-credential branch admits.
func firstCredentialSession(t *testing.T, st store.Store, user *store.User, now time.Time) *auth.UserInfo {
	t.Helper()
	uid := userid.MustNew(user.ID)
	sessionID, _, err := auth.CreateSession(context.Background(), st, uid, auth.DefaultSessionDuration)
	require.NoError(t, err)
	return &auth.UserInfo{
		ID:              uid,
		Credential:      auth.SessionCredential(sessionID),
		AuthenticatedAt: now,
	}
}

// liveElevatedSessionAt seeds a session with a current elevation and returns
// the UserInfo a request on it would carry, together with the session id.
func liveElevatedSessionAt(t *testing.T, st store.Store, user *store.User, now time.Time) (*auth.UserInfo, string) {
	t.Helper()
	uid := userid.MustNew(user.ID)
	sessionID, _, err := auth.CreateSession(context.Background(), st, uid, auth.DefaultSessionDuration)
	require.NoError(t, err)
	n, err := st.Sessions().Elevate(context.Background(), store.ElevateSessionParams{
		SessionID:          sessionID,
		UserID:             uid,
		ElevationProvenAt:  now,
		ElevationExpiresAt: now.Add(auth.ElevationWindow),
	}, now)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	return &auth.UserInfo{ID: uid, Credential: auth.SessionCredential(sessionID)}, sessionID
}

// The locked re-check is the guard a concurrent rotation must not skip:
// the admission ran against the peeked row, the write runs against the
// locked row, and the two must agree.
func TestRecheckStepUpUnderLock(t *testing.T) {
	t.Parallel()

	elevated := stepUpAdmission{}
	// The first-credential branch never reaches the session re-read, so the
	// credential it carries is never dereferenced on that path.
	noCredential := &auth.UserInfo{}

	liveElevatedSession := liveElevatedSessionAt

	t.Run("an unchanged row on a live elevated session passes", func(t *testing.T) {
		t.Parallel()
		st := newStepUpTestStore(t)
		user := stepUpUser(t, st, true)
		now := time.Now().UTC()
		info, _ := liveElevatedSession(t, st, user, now)
		assert.NoError(t, recheckStepUpUnderLock(context.Background(), st, user, user, elevated, entryStepUp(info), now))
	})

	// A password ROTATED under the lock is no longer a re-verification
	// case: the admission verified no secret at this call, so there is
	// nothing to re-check it against. What still matters is the
	// STRUCTURAL flip, because that is what changes which branch of
	// stepUpMutationAuth applies.
	t.Run("a rotated hash alone is not a state move", func(t *testing.T) {
		t.Parallel()
		st := newStepUpTestStore(t)
		user := stepUpUser(t, st, true)
		now := time.Now().UTC()
		info, _ := liveElevatedSession(t, st, user, now)
		peek := &store.User{ID: user.ID, PasswordSet: true, PasswordHash: "stale-hash"}
		locked := &store.User{ID: user.ID, PasswordSet: true, PasswordHash: "rotated-hash"}
		assert.NoError(t, recheckStepUpUnderLock(context.Background(), st, locked, peek, elevated, entryStepUp(info), now))
	})

	t.Run("structural flip refuses with a retry error", func(t *testing.T) {
		t.Parallel()
		for name, tc := range map[string]struct{ peek, locked bool }{
			"password added under the lock":   {peek: false, locked: true},
			"password removed under the lock": {peek: true, locked: false},
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				peek := &store.User{PasswordSet: tc.peek}
				locked := &store.User{PasswordSet: tc.locked}
				// The flip is decided before any query, so no store is needed.
				err := recheckStepUpUnderLock(context.Background(), nil, locked, peek, elevated, entryStepUp(noCredential), time.Now().UTC())
				requireStepUpStateMoved(t, err)
			})
		}
	})

	// The re-check TOLERATES an ABSENT session row, and this pins that on purpose.
	// Sessions().Delete does not contend on the user-auth lock, so a
	// same-user "sign out everywhere" can remove the acting session in the
	// middle of a change the user legitimately started; rolling that change
	// back was a real regression once already. See
	// TestChangePassword_ToleratesConcurrentActingSessionDeletion, which
	// drives the same race through the whole RPC.
	t.Run("a deleted acting session is tolerated", func(t *testing.T) {
		t.Parallel()
		st := newStepUpTestStore(t)
		user := stepUpUser(t, st, true)
		now := time.Now().UTC()
		info, sessionID := liveElevatedSession(t, st, user, now)

		require.NoError(t, recheckStepUpUnderLock(context.Background(), st, user, user, elevated, entryStepUp(info), now),
			"precondition: the live session passes")

		_, err := st.Sessions().Delete(context.Background(), sessionID)
		require.NoError(t, err)
		assert.NoError(t,
			recheckStepUpUnderLock(context.Background(), st, user, user, elevated, entryStepUp(info), now),
			"a concurrent sign-out must not roll back a change the user started")
	})

	// The re-check refuses a LAPSED window: nothing legitimate waits two
	// hours on this lock, so a request whose window closed while it queued is
	// not a race to tolerate.
	t.Run("a lapsed elevation refuses with a retry error", func(t *testing.T) {
		t.Parallel()
		st := newStepUpTestStore(t)
		user := stepUpUser(t, st, true)
		now := time.Now().UTC()
		info, _ := liveElevatedSession(t, st, user, now)

		later := now.Add(auth.ElevationWindow + time.Minute)
		requireStepUpStateMoved(t,
			recheckStepUpUnderLock(context.Background(), st, user, user, elevated, entryStepUp(info), later))
	})

	// The first-credential admission is the one input a concurrent write
	// can invalidate: stepUpMutationAuth admitted the account BECAUSE it held
	// no credential to present, so a registration that commits in the window
	// means the caller must elevate instead.
	//
	// Without this re-read, two concurrent first-credential mutations both
	// commit and the second sets a password with no step-up at all.
	t.Run("first-credential admission re-reads the count under the lock", func(t *testing.T) {
		t.Parallel()
		st := newStepUpTestStore(t)
		user := stepUpUser(t, st, false)
		firstCredential := stepUpAdmission{firstCredential: true}
		now := time.Now().UTC()
		acting := firstCredentialSession(t, st, user, now)

		// Nothing committed in the window: the admission still holds.
		assert.NoError(t, recheckStepUpUnderLock(context.Background(), st, user, user, firstCredential, entryStepUp(acting), now))

		// A passkey committed in the window: the account now HAS a
		// credential, so the admission no longer holds.
		require.NoError(t, st.PasskeyCredentials().Create(context.Background(), store.CreatePasskeyCredentialParams{
			ID:           id.Generate(),
			UserID:       user.ID,
			CredentialID: []byte("raced-credential"),
			PublicKey:    []byte("key"),
			FriendlyName: "Raced",
		}))

		requireStepUpStateMoved(t,
			recheckStepUpUnderLock(context.Background(), st, user, user, firstCredential, entryStepUp(acting), now))
	})

	// The DURABLE IDENTITY half of the same rule, which the re-check used to
	// leave unguarded.
	//
	// It re-read the passkey count alone -- an enumeration of the inputs
	// somebody believed a concurrent write could move. An administrator who
	// cleared the account's verified address while this request queued left
	// the count at zero, so the enumeration still passed and the session
	// attached the account's first password or passkey on an authority that
	// already disappeared. Re-deriving the whole admission is what closes it.
	t.Run("a durable identity withdrawn under the lock refuses", func(t *testing.T) {
		t.Parallel()
		st := newStepUpTestStore(t)
		user := stepUpUser(t, st, false)
		firstCredential := stepUpAdmission{firstCredential: true}
		now := time.Now().UTC()
		acting := firstCredentialSession(t, st, user, now)

		require.NoError(t,
			recheckStepUpUnderLock(context.Background(), st, user, user, firstCredential, entryStepUp(acting), now),
			"precondition: a verified address admits")

		// The administrator's clear, exactly as UpdateUser writes it: the
		// address goes and the flag goes with it.
		require.NoError(t, st.Users().UpdateEmail(context.Background(), store.UpdateUserEmailParams{
			ID: user.ID, Email: "", EmailVerified: false,
		}))
		locked, err := st.Users().GetByID(context.Background(), user.ID)
		require.NoError(t, err)

		// The refusal is the RULE's own message, not the retry error: it
		// states what the caller must do next, and a retry would meet the
		// same answer.
		err = recheckStepUpUnderLock(context.Background(), st, locked, user, firstCredential, entryStepUp(acting), now)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "verify your email or link an OAuth provider")
	})

	// The five-minute window is re-evaluated at NOW, so a request whose
	// window lapsed during the Argon2 hash and the lock wait is refused --
	// the same answer the elevated branch gives for its own lapsed window.
	t.Run("a first-credential window that lapsed under the lock refuses", func(t *testing.T) {
		t.Parallel()
		st := newStepUpTestStore(t)
		user := stepUpUser(t, st, false)
		firstCredential := stepUpAdmission{firstCredential: true}
		now := time.Now().UTC()
		acting := firstCredentialSession(t, st, user, now)

		later := now.Add(firstCredentialAuthFreshness + time.Minute)
		err := recheckStepUpUnderLock(context.Background(), st, user, user, firstCredential, entryStepUp(acting), later)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "sign in again")
	})
}

// TestRecheckStepUpUnderLock_AppliesTheLegsGrace drives the Finish leg's
// ceremony grace through the LOCKED re-check, which is where it was dropped.
//
// The admission and the locked re-check are two predicates over the same
// window, and the grace has to reach both. It reached only the first, so a
// FinishPasskeyRegistration whose window lapsed during the browser prompt
// passed the admission, and the transaction then refused it with
// stepUpStateMovedError -- which carries no ElevationRequiredHeader, so the
// client's gate does not even open a prompt. The user answered the biometric
// prompt and the hub discarded the credential the authenticator created.
//
// Nothing slides the window between the two legs: Begin does not slide, and
// the slide runs after the commit.
func TestRecheckStepUpUnderLock_AppliesTheLegsGrace(t *testing.T) {
	t.Parallel()

	st := newStepUpTestStore(t)
	user := stepUpUser(t, st, true)
	provenAt := time.Now().UTC()
	info, _ := liveElevatedSessionAt(t, st, user, provenAt)

	// The window closed while the authenticator prompt was on screen.
	finishAt := provenAt.Add(auth.ElevationWindow + time.Second)

	requireStepUpStateMoved(t,
		recheckStepUpUnderLock(context.Background(), st, user, user, stepUpAdmission{}, entryStepUp(info), finishAt))

	assert.NoError(t,
		recheckStepUpUnderLock(context.Background(), st, user, user, stepUpAdmission{}, finishStepUp(info), finishAt),
		"a ceremony the hub still accepts must not be refused after the user answered the prompt")

	// The grace is a limit, not an amnesty: a window that lapsed a day ago
	// stays refused on the Finish leg too.
	requireStepUpStateMoved(t,
		recheckStepUpUnderLock(context.Background(), st, user, user, stepUpAdmission{}, finishStepUp(info),
			provenAt.Add(24*time.Hour)))
}

// TestRecheckStepUpUnderLock_RefusesARevokedCredentialEpoch is the guard that
// replaces the password re-verification the elevation model removed.
//
// The attack: an attacker holds a stolen elevated cookie and fires
// DeletePasskey, which blocks on the user-auth lock. The owner changes their
// password on another session; that rotation DELETES the attacker's session
// row and moves the account's credential epoch. Row presence alone cannot
// refuse this, because a plain "sign out everywhere" deletes the row too and
// must stay tolerated -- so the epoch is what separates them.
func TestRecheckStepUpUnderLock_RefusesARevokedCredentialEpoch(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := newStepUpTestStore(t)
	user := stepUpUser(t, st, true)
	now := time.Now().UTC()
	info, sessionID := liveElevatedSessionAt(t, st, user, now)

	require.NoError(t,
		recheckStepUpUnderLock(ctx, st, user, user, stepUpAdmission{}, entryStepUp(info), now),
		"precondition: the live session passes")

	// The rotation, as ChangePassword performs it: revoke every credential
	// (which moves the epoch) and delete the other sessions.
	_, _, err := auth.RevokeAllUserCredentials(ctx, st, userid.MustNew(user.ID))
	require.NoError(t, err)
	_, err = st.Sessions().Delete(ctx, sessionID)
	require.NoError(t, err)

	locked, err := st.Users().GetByID(ctx, user.ID)
	require.NoError(t, err)
	require.Greater(t, locked.AuthGeneration, info.UserAuthGeneration,
		"precondition: the revocation moved the account's credential epoch")

	requireStepUpStateMoved(t,
		recheckStepUpUnderLock(ctx, st, locked, user, stepUpAdmission{}, entryStepUp(info), now))

	// The first-credential branch reads the same epoch, so a revocation
	// refuses it too rather than only the elevated branch.
	requireStepUpStateMoved(t,
		recheckStepUpUnderLock(ctx, st, locked, user, stepUpAdmission{firstCredential: true}, entryStepUp(info), now))
}

// requireStepUpStateMoved asserts the one refusal the locked re-check
// raises: FailedPrecondition, which tells the caller to retry.
func requireStepUpStateMoved(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	connectErr := new(connect.Error)
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeFailedPrecondition, connectErr.Code())
}

// The friendly-name limit counts characters: a CJK or emoji name under the
// limit is valid input and must not be rejected over its byte length.
func TestValidatePasskeyFriendlyNameCountsCharacters(t *testing.T) {
	t.Parallel()
	name, err := validatePasskeyFriendlyName("비밀번호가-긴-이름인-경우에도-문자로-세면-통과합니다") // 30 characters, 90 UTF-8 bytes
	require.NoError(t, err)
	assert.Equal(t, "비밀번호가-긴-이름인-경우에도-문자로-세면-통과합니다", name)

	_, err = validatePasskeyFriendlyName(string(make([]rune, 65)))
	require.Error(t, err)
}

type failingSender struct{}

func (failingSender) Send(_ context.Context, _ mail.Message) error {
	return errors.New("smtp unavailable")
}

// A failed verification send must drop the undelivered CODE and keep the
// pending ADDRESS. The code must not linger and arm the resend cooldown,
// and the failure must not surface as err (the callers report it through
// the sent flag instead). The address must survive, because a
// verification-required sign-up leaves users.email empty, so it is the only
// record of the address the account must verify -- clearing it leaves
// ResendVerificationEmail with nothing to re-send to.
func TestIssuePendingEmailVerificationFailedSendKeepsAddressDropsCode(t *testing.T) {
	t.Parallel()
	st := newStepUpTestStore(t)
	user := stepUpUser(t, st, true)

	sent, err := issuePendingEmailVerification(context.Background(), st, failingSender{}, mail.Renderer{}, user.ID, "retry@example.com", time.Now())
	require.NoError(t, err)
	assert.False(t, sent)

	after, err := st.Users().GetByID(context.Background(), user.ID)
	require.NoError(t, err)
	assert.Equal(t, "retry@example.com", after.PendingEmail,
		"the address must survive a failed send: it is the only record of what to re-send to")
	assert.Empty(t, after.PendingEmailToken, "an undelivered code must not stay live")
	assert.Nil(t, after.PendingEmailExpiresAt,
		"no expiry means no armed cooldown, so the retry the failure invites is not blocked")

	// The dropped code means an immediate retry is not cooldown-blocked.
	sent2, err2 := issuePendingEmailVerification(context.Background(), st, failingSender{}, mail.Renderer{}, user.ID, "retry@example.com", time.Now())
	require.NoError(t, err2)
	assert.False(t, sent2)
}

// The regression this pair exists for: a sign-up that requires
// verification stores the address ONLY in pending_email, so a failed
// resend that deleted the row left the account with no address anywhere and
// ResendVerificationEmail refusing forever with "no pending email change".
func TestResendAfterFailedSendStillFindsTheAddress(t *testing.T) {
	t.Parallel()
	st := newStepUpTestStore(t)
	user := stepUpUser(t, st, true)
	// A verification-required sign-up: the address lives in pending_email
	// and users.email is empty.
	require.NoError(t, st.Users().UpdateEmail(context.Background(), store.UpdateUserEmailParams{
		ID: user.ID, Email: "", EmailVerified: false,
	}))

	_, err := issuePendingEmailVerification(context.Background(), st, failingSender{}, mail.Renderer{}, user.ID, "signup@example.com", time.Now())
	require.NoError(t, err)

	after, err := st.Users().GetByID(context.Background(), user.ID)
	require.NoError(t, err)
	require.Empty(t, after.Email, "precondition: the address is only in pending_email")
	assert.Equal(t, "signup@example.com", after.PendingEmail,
		"ResendVerificationEmail reads PendingEmail; an empty one refuses forever")
}
