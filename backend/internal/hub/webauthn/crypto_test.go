package webauthn_test

import (
	"testing"

	gowebauthn "github.com/go-webauthn/webauthn/webauthn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/hub/webauthn"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
)

func newWebAuthnTestService(t *testing.T) *webauthn.Service {
	t.Helper()
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

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
	return svc
}

func newTestKeystore(t *testing.T) *keystore.Keystore {
	t.Helper()
	key, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)
	return ks
}

func TestEncryptDecryptPublicKeyRoundtrip(t *testing.T) {
	svc := newWebAuthnTestService(t)
	const rowID = "cred_test"
	plain := []byte("cose-public-key-bytes")

	enc, version, err := svc.EncryptPublicKey(rowID, plain)
	require.NoError(t, err)
	assert.EqualValues(t, 1, version)

	got, err := svc.DecryptPublicKey(rowID, enc)
	require.NoError(t, err)
	assert.Equal(t, plain, got)
}

func TestEncryptDecryptPublicKeyWrongRowIDFails(t *testing.T) {
	svc := newWebAuthnTestService(t)
	enc, _, err := svc.EncryptPublicKey("cred_a", []byte("key"))
	require.NoError(t, err)

	_, err = svc.DecryptPublicKey("cred_b", enc)
	assert.Error(t, err)
}

func TestPasskeyPublicKeyAADIsRowBound(t *testing.T) {
	ks := newTestKeystore(t)
	plain := []byte("secret")
	aad := keystore.PasskeyPublicKeyAAD("row-1")

	enc, err := ks.Encrypt(plain, aad)
	require.NoError(t, err)

	_, err = ks.Decrypt(enc, keystore.PasskeyPublicKeyAAD("row-2"))
	assert.Error(t, err)

	got, err := ks.Decrypt(enc, aad)
	require.NoError(t, err)
	assert.Equal(t, plain, got)
}

func TestWebAuthnPayloadAADIsSessionBound(t *testing.T) {
	ks := newTestKeystore(t)
	plain := []byte(`{"email":"a@example.com"}`)
	aad := keystore.WebAuthnPayloadAAD("sess-1")

	enc, err := ks.Encrypt(plain, aad)
	require.NoError(t, err)

	_, err = ks.Decrypt(enc, keystore.WebAuthnPayloadAAD("sess-2"))
	assert.Error(t, err)

	got, err := ks.Decrypt(enc, aad)
	require.NoError(t, err)
	assert.Equal(t, plain, got)
}

func TestWebAuthnSessionDataAADIsSessionBound(t *testing.T) {
	ks := newTestKeystore(t)
	plain := []byte(`{"challenge":"abc"}`)
	aad := keystore.WebAuthnSessionDataAAD("sess-1")

	enc, err := ks.Encrypt(plain, aad)
	require.NoError(t, err)

	_, err = ks.Decrypt(enc, keystore.WebAuthnSessionDataAAD("sess-2"))
	assert.Error(t, err)

	got, err := ks.Decrypt(enc, aad)
	require.NoError(t, err)
	assert.Equal(t, plain, got)
}

func TestRoundTripSessionData(t *testing.T) {
	svc := newWebAuthnTestService(t)
	data := &gowebauthn.SessionData{Challenge: "test-challenge", UserID: []byte("user-1")}

	got, err := svc.RoundTripSessionData("sess-1", data)
	require.NoError(t, err)
	assert.Equal(t, data.Challenge, got.Challenge)
	assert.Equal(t, data.UserID, got.UserID)
}
