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

	t.Run("unchanged row passes without re-verification", func(t *testing.T) {
		t.Parallel()
		peek := &store.User{PasswordSet: true, PasswordHash: "h1"}
		locked := &store.User{PasswordSet: true, PasswordHash: "h1"}
		assert.NoError(t, recheckStepUpUnderLock(locked, peek, "pw"))
	})

	t.Run("structural flip refuses with a retry error", func(t *testing.T) {
		t.Parallel()
		peek := &store.User{PasswordSet: false}
		locked := &store.User{PasswordSet: true, PasswordHash: "h2"}
		err := recheckStepUpUnderLock(locked, peek, "pw")
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
		assert.NoError(t, recheckStepUpUnderLock(locked, peek, "stepup-password"))

		wrong := recheckStepUpUnderLock(locked, peek, "not-the-password")
		require.Error(t, wrong)
		connectErr := new(connect.Error)
		require.ErrorAs(t, wrong, &connectErr)
		assert.Equal(t, connect.CodeUnauthenticated, connectErr.Code())
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

// A failed verification send must clear the freshly stamped pending row:
// a code that was never delivered must not block the immediate retry
// behind the resend cooldown, and the failure must not surface as err
// (the callers report it through the sent flag instead).
func TestIssuePendingEmailVerificationFailedSendClearsRow(t *testing.T) {
	t.Parallel()
	st := newStepUpTestStore(t)
	user := stepUpUser(t, st, true)

	sent, err := issuePendingEmailVerification(context.Background(), st, failingSender{}, mail.Renderer{}, user.ID, "retry@example.com")
	require.NoError(t, err)
	assert.False(t, sent)

	after, err := st.Users().GetByID(context.Background(), user.ID)
	require.NoError(t, err)
	assert.Empty(t, after.PendingEmail, "an undelivered code must not linger and arm the cooldown")

	// The cleared row means an immediate retry is not cooldown-blocked.
	sent2, err2 := issuePendingEmailVerification(context.Background(), st, failingSender{}, mail.Renderer{}, user.ID, "retry@example.com")
	require.NoError(t, err2)
	assert.False(t, sent2)
}
