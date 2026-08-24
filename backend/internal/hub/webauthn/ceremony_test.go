package webauthn_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	gowebauthn "github.com/go-webauthn/webauthn/webauthn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/hub/webauthn"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
)

func newMigratedWebAuthnService(t *testing.T) (*webauthn.Service, store.Store) {
	t.Helper()
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrator().Migrate(context.Background()))

	key, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)

	svc, err := webauthn.NewService(webauthn.RPConfig{
		RPID:          "localhost",
		RPDisplayName: "LeapMux",
		RPOrigins:     []string{"http://localhost"},
	}, st, ks)
	require.NoError(t, err)
	return svc, st
}

func seedUser(t *testing.T, st store.Store) string {
	t.Helper()
	userID := id.Generate()
	require.NoError(t, st.Users().Create(context.Background(), store.CreateUserParams{
		ID:           userID,
		Username:     "wauser" + userID[:8],
		PasswordHash: "hash",
		DisplayName:  "WebAuthn User",
		PasswordSet:  true,
	}))
	return userID
}

func TestBeginSignUp_AllocatesStableUserID(t *testing.T) {
	svc, st := newMigratedWebAuthnService(t)

	sessionID, optionsJSON, _, err := svc.BeginSignUp(context.Background(), webauthn.SignupDraft{
		Username:    "newuser",
		Email:       "new@example.com",
		DisplayName: "New User",
	}, "http://localhost")
	require.NoError(t, err)
	assert.NotEmpty(t, sessionID)
	assert.NotEmpty(t, optionsJSON)

	row, err := st.WebAuthnSessions().Get(context.Background(), sessionID)
	require.NoError(t, err)
	assert.Equal(t, "signup", row.Kind)
	assert.Empty(t, row.UserID, "signup ceremony row has no users FK yet")

	var draft webauthn.SignupDraft
	plain, err := svc.DecryptPayloadJSON(sessionID, row.PayloadJSON)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(plain), &draft))
	assert.NotEmpty(t, draft.UserID)
	assert.NotContains(t, row.PayloadJSON, "new@example.com", "signup draft must not sit in payload_json as plaintext")

	// Options embed the same user id as the WebAuthn user handle (base64url of the UUID bytes).
	var options map[string]any
	require.NoError(t, json.Unmarshal([]byte(optionsJSON), &options))
	publicKey, ok := options["publicKey"].(map[string]any)
	require.True(t, ok)
	user, ok := publicKey["user"].(map[string]any)
	require.True(t, ok)
	userIDField, ok := user["id"].(string)
	require.True(t, ok)
	decoded, err := base64.RawURLEncoding.DecodeString(userIDField)
	if err != nil {
		decoded, err = base64.URLEncoding.DecodeString(userIDField)
		require.NoError(t, err)
	}
	assert.Equal(t, draft.UserID, string(decoded))
}

func TestBeginReauth_RejectsEmptyCredentials(t *testing.T) {
	svc, st := newMigratedWebAuthnService(t)
	userID := seedUser(t, st)

	_, _, _, err := svc.BeginReauth(context.Background(), userID, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no passkeys registered")
}

func TestBeginReauth_ReplacesPriorCeremony(t *testing.T) {
	svc, st := newMigratedWebAuthnService(t)
	userID := seedUser(t, st)

	credID := []byte{0x11, 0x22, 0x33, 0x44}
	rowID := id.Generate()
	enc, version, err := svc.EncryptPublicKey(rowID, []byte("cose-public-key"))
	require.NoError(t, err)
	now := time.Now().UTC()
	require.NoError(t, st.PasskeyCredentials().Create(context.Background(), store.CreatePasskeyCredentialParams{
		ID: rowID, UserID: userID, CredentialID: credID, PublicKey: enc,
		SignCount: 1, Transports: `["internal"]`, FriendlyName: "Phone",
		KeyVersion: version, CreatedAt: now,
	}))

	firstID, _, _, err := svc.BeginReauth(context.Background(), userID, "")
	require.NoError(t, err)
	secondID, _, _, err := svc.BeginReauth(context.Background(), userID, "")
	require.NoError(t, err)
	assert.NotEqual(t, firstID, secondID)

	_, err = st.WebAuthnSessions().Get(context.Background(), firstID)
	assert.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.WebAuthnSessions().Get(context.Background(), secondID)
	require.NoError(t, err)
}

func TestBeginLogin_ReplacesPriorCeremony(t *testing.T) {
	svc, st := newMigratedWebAuthnService(t)
	userID := seedUser(t, st)

	credID := []byte{0x11, 0x22, 0x33, 0x44}
	rowID := id.Generate()
	enc, version, err := svc.EncryptPublicKey(rowID, []byte("cose-public-key"))
	require.NoError(t, err)
	now := time.Now().UTC()
	require.NoError(t, st.PasskeyCredentials().Create(context.Background(), store.CreatePasskeyCredentialParams{
		ID: rowID, UserID: userID, CredentialID: credID, PublicKey: enc,
		SignCount: 1, Transports: `["internal"]`, FriendlyName: "Phone",
		KeyVersion: version, CreatedAt: now,
	}))

	firstID, _, _, err := svc.BeginLogin(context.Background(), userID, "")
	require.NoError(t, err)
	secondID, _, _, err := svc.BeginLogin(context.Background(), userID, "")
	require.NoError(t, err)
	assert.NotEqual(t, firstID, secondID)

	_, err = st.WebAuthnSessions().Get(context.Background(), firstID)
	assert.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.WebAuthnSessions().Get(context.Background(), secondID)
	require.NoError(t, err)
}

func TestBeginRegistration_ReplacesPriorCeremony(t *testing.T) {
	svc, st := newMigratedWebAuthnService(t)
	userID := seedUser(t, st)

	firstID, _, _, err := svc.BeginRegistration(context.Background(), userID, "")
	require.NoError(t, err)
	secondID, _, _, err := svc.BeginRegistration(context.Background(), userID, "")
	require.NoError(t, err)
	assert.NotEqual(t, firstID, secondID)

	_, err = st.WebAuthnSessions().Get(context.Background(), firstID)
	assert.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.WebAuthnSessions().Get(context.Background(), secondID)
	require.NoError(t, err)
}

func TestBeginRegistration_PersistsEncryptedSession(t *testing.T) {
	svc, st := newMigratedWebAuthnService(t)
	userID := seedUser(t, st)

	sessionID, optionsJSON, _, err := svc.BeginRegistration(context.Background(), userID, "")
	require.NoError(t, err)
	assert.NotEmpty(t, sessionID)
	assert.NotEmpty(t, optionsJSON)

	row, err := st.WebAuthnSessions().Get(context.Background(), sessionID)
	require.NoError(t, err)
	assert.Equal(t, "register", row.Kind)
	assert.Equal(t, userID, row.UserID)
	assert.NotEmpty(t, row.SessionData)
	assert.True(t, row.ExpiresAt.After(time.Now().UTC()))

	// Ciphertext must not equal plaintext session JSON shape.
	assert.NotContains(t, string(row.SessionData), `"challenge"`)
}

func TestBeginReauth_ConstrainsAllowCredentials(t *testing.T) {
	svc, st := newMigratedWebAuthnService(t)
	userID := seedUser(t, st)

	credID := []byte{0x01, 0x02, 0x03, 0x04, 0xaa, 0xbb}
	rowID := id.Generate()
	enc, version, err := svc.EncryptPublicKey(rowID, []byte("cose-public-key"))
	require.NoError(t, err)
	now := time.Now().UTC()
	require.NoError(t, st.PasskeyCredentials().Create(context.Background(), store.CreatePasskeyCredentialParams{
		ID:           rowID,
		UserID:       userID,
		CredentialID: credID,
		PublicKey:    enc,
		SignCount:    3,
		Transports:   `["internal"]`,
		FriendlyName: "Phone",
		KeyVersion:   version,
		CreatedAt:    now,
	}))

	sessionID, optionsJSON, _, err := svc.BeginReauth(context.Background(), userID, "")
	require.NoError(t, err)
	assert.NotEmpty(t, sessionID)

	var envelope map[string]any
	require.NoError(t, json.Unmarshal([]byte(optionsJSON), &envelope))
	publicKey, ok := envelope["publicKey"].(map[string]any)
	if !ok {
		// Some serializers flatten; accept either envelope.
		publicKey = envelope
	}
	allow, ok := publicKey["allowCredentials"].([]any)
	require.True(t, ok, "allowCredentials must be present: %s", optionsJSON)
	require.Len(t, allow, 1, "reauth must constrain allowCredentials to the user's passkeys")

	entry, ok := allow[0].(map[string]any)
	require.True(t, ok)
	idField, ok := entry["id"].(string)
	require.True(t, ok)
	decoded, err := base64.RawURLEncoding.DecodeString(idField)
	if err != nil {
		decoded, err = base64.StdEncoding.DecodeString(idField)
	}
	require.NoError(t, err)
	assert.Equal(t, credID, decoded)
}

func TestRejectIfCloneWarning(t *testing.T) {
	assert.NoError(t, webauthn.RejectIfCloneWarning(nil))
	assert.NoError(t, webauthn.RejectIfCloneWarning(&gowebauthn.Credential{}))
	assert.ErrorIs(t, webauthn.RejectIfCloneWarning(&gowebauthn.Credential{
		Authenticator: gowebauthn.Authenticator{CloneWarning: true},
	}), webauthn.ErrCloneDetected)
}
