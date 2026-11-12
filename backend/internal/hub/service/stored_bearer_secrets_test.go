package service_test

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
)

// sessionCookieSecret reads both the original cookie and the split token format.
// Keeping the assertion independent of the parser lets it reproduce the leak.
func sessionCookieSecret(token string) string {
	if i := strings.LastIndexByte(token, '_'); i >= 0 {
		return token[i+1:]
	}
	return token
}

func assertStoredText(t *testing.T, st store.Store, table, text string, want bool) {
	t.Helper()
	testable, ok := st.(store.TestableStore)
	require.True(t, ok, "the store must expose its test helper")
	found, err := testable.TestHelper().TableHoldsText(t.Context(), table, text)
	require.NoError(t, err)
	assert.Equal(t, want, found, "table %s", table)
}

func TestStoredBearerSecrets_UserSessions(t *testing.T) {
	env := setupRegKeyEnv(t)
	token := env.login(t, hubtestutil.TestAdminUsername, hubtestutil.TestAdminPassword)
	assertStoredText(t, env.store, "user_sessions", env.adminID(t), true)
	assertStoredText(t, env.store, "user_sessions", sessionCookieSecret(token), false)
	assertStoredText(t, env.store, "user_sessions", token, false)
}

func TestStoredBearerSecrets_Workers(t *testing.T) {
	env := setupRegKeyEnv(t)
	token := env.login(t, hubtestutil.TestAdminUsername, hubtestutil.TestAdminPassword)
	key, err := env.mgmtClient.CreateRegistrationKey(authedReq(context.Background(), &leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	registered, err := env.registerCall(t, t.Context(), key.GetRegistrationKey(), newRegisterAttempt(t), false)
	require.NoError(t, err)
	assertStoredText(t, env.store, "workers", registered.GetWorkerId(), true)
	assertStoredText(t, env.store, "workers", registered.GetAuthToken(), false)
}

func TestStoredBearerSecrets_RegistrationKeys(t *testing.T) {
	env := setupRegKeyEnv(t)
	token := env.login(t, hubtestutil.TestAdminUsername, hubtestutil.TestAdminPassword)
	key, err := env.mgmtClient.CreateRegistrationKey(authedReq(t.Context(), &leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	assertStoredText(t, env.store, "worker_registration_keys", env.adminID(t), true)
	assertStoredText(t, env.store, "worker_registration_keys", key.GetRegistrationKey(), false)
}

func TestStoredBearerSecrets_DeviceAuthorizations(t *testing.T) {
	env := setupAPIAuth(t)
	deviceCode, userCode := startDeviceAuthorization(t, env, url.Values{})
	assertStoredText(t, env.store, "device_authorizations", strings.ReplaceAll(userCode, "-", ""), true)
	assertStoredText(t, env.store, "device_authorizations", deviceCode, false)
}
