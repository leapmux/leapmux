package service

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/leapmux/leapmux/internal/hub/mail"
	pwdhash "github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
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
	}))
	u, err := st.Users().GetByID(context.Background(), userID)
	require.NoError(t, err)
	return u
}

// The locked re-check is the guard a concurrent rotation must not skip:
// the step-up ran against the peeked row, the write runs against the
// locked row, and the two must agree.
func TestRecheckStepUpUnderLock(t *testing.T) {
	t.Parallel()

	// The password and reauth-proof admissions never re-read the count, so
	// a nil store proves the writer lock pays for no query on those paths.
	passwordAdmission := stepUpAdmission{}

	t.Run("unchanged row passes without re-verification", func(t *testing.T) {
		t.Parallel()
		peek := &store.User{PasswordSet: true, PasswordHash: "h1"}
		locked := &store.User{PasswordSet: true, PasswordHash: "h1"}
		assert.NoError(t, recheckStepUpUnderLock(context.Background(), nil, locked, peek, "pw", passwordAdmission))
	})

	t.Run("structural flip refuses with a retry error", func(t *testing.T) {
		t.Parallel()
		peek := &store.User{PasswordSet: false}
		locked := &store.User{PasswordSet: true, PasswordHash: "h2"}
		err := recheckStepUpUnderLock(context.Background(), nil, locked, peek, "pw", passwordAdmission)
		require.Error(t, err)
		connectErr := new(connect.Error)
		require.ErrorAs(t, err, &connectErr)
		assert.Equal(t, connect.CodeFailedPrecondition, connectErr.Code())
	})

	t.Run("rotated hash re-verifies against the locked row", func(t *testing.T) {
		t.Parallel()
		// The peek and the locked row disagree: the password the caller
		// presented must be checked against the LOCKED hash, which
		// verifyPasswordForPasskeyManagement does through Argon2. Use a
		// real hash so the verify runs the real path.
		realHash, err := pwdhash.Hash("stepup-password")
		require.NoError(t, err)
		peek := &store.User{PasswordSet: true, PasswordHash: "stale-hash"}
		locked := &store.User{PasswordSet: true, PasswordHash: realHash}
		assert.NoError(t, recheckStepUpUnderLock(context.Background(), nil, locked, peek, "stepup-password", passwordAdmission))

		wrong := recheckStepUpUnderLock(context.Background(), nil, locked, peek, "not-the-password", passwordAdmission)
		require.Error(t, wrong)
		connectErr := new(connect.Error)
		require.ErrorAs(t, wrong, &connectErr)
		assert.Equal(t, connect.CodeUnauthenticated, connectErr.Code())
	})

	// The first-credential admission is the one input a concurrent write
	// can invalidate: the account was admitted BECAUSE it held no
	// credential to present, so a registration that commits in the window
	// means the caller should have presented a reauth proof for it.
	//
	// Without this re-read, two concurrent first-credential mutations both
	// commit and the second sets a password with no step-up at all.
	t.Run("first-credential admission re-reads the count under the lock", func(t *testing.T) {
		t.Parallel()
		st := newStepUpTestStore(t)
		user := stepUpUser(t, st, false)
		firstCredential := stepUpAdmission{firstCredential: true}

		// Nothing committed in the window: the admission still holds.
		assert.NoError(t, recheckStepUpUnderLock(context.Background(), st, user, user, "", firstCredential))

		// A passkey committed in the window: the account now HAS a
		// credential, so the admission no longer holds.
		require.NoError(t, st.PasskeyCredentials().Create(context.Background(), store.CreatePasskeyCredentialParams{
			ID:           id.Generate(),
			UserID:       user.ID,
			CredentialID: []byte("raced-credential"),
			PublicKey:    []byte("key"),
			FriendlyName: "Raced",
		}))

		err := recheckStepUpUnderLock(context.Background(), st, user, user, "", firstCredential)
		require.Error(t, err)
		connectErr := new(connect.Error)
		require.ErrorAs(t, err, &connectErr)
		assert.Equal(t, connect.CodeFailedPrecondition, connectErr.Code())
	})

	// The count re-read is scoped to the admission that needs it: a
	// proof-bearing caller already presented a credential, so the writer
	// lock must not pay for a query on that path.
	t.Run("a proof-bearing admission does not re-read the count", func(t *testing.T) {
		t.Parallel()
		user := &store.User{ID: "u", PasswordSet: false}
		// A nil store panics on any query, so passing gives the assertion.
		assert.NoError(t, recheckStepUpUnderLock(context.Background(), nil, user, user, "", stepUpAdmission{needsReauth: true}))
	})
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
// record of what the account is trying to verify -- clearing it leaves
// ResendVerificationEmail with nothing to re-send to.
func TestIssuePendingEmailVerificationFailedSendKeepsAddressDropsCode(t *testing.T) {
	t.Parallel()
	st := newStepUpTestStore(t)
	user := stepUpUser(t, st, true)

	sent, err := issuePendingEmailVerification(context.Background(), st, failingSender{}, mail.Renderer{}, user.ID, "retry@example.com")
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
	sent2, err2 := issuePendingEmailVerification(context.Background(), st, failingSender{}, mail.Renderer{}, user.ID, "retry@example.com")
	require.NoError(t, err2)
	assert.False(t, sent2)
}

// The regression this pair exists for: a sign-up that requires
// verification stores the address ONLY in pending_email, so a failed
// resend that wiped the row left the account with no address anywhere and
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

	_, err := issuePendingEmailVerification(context.Background(), st, failingSender{}, mail.Renderer{}, user.ID, "signup@example.com")
	require.NoError(t, err)

	after, err := st.Users().GetByID(context.Background(), user.ID)
	require.NoError(t, err)
	require.Empty(t, after.Email, "precondition: the address is only in pending_email")
	assert.Equal(t, "signup@example.com", after.PendingEmail,
		"ResendVerificationEmail reads PendingEmail; an empty one is a permanent dead end")
}
