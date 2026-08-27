package service_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// The passkey_enabled flag answers one question for the client: can THIS page
// run a ceremony? Every passkey affordance requires it -- the sign-in form's
// Passkey option, the sign-up form's, and the account panel's Add passkey
// button -- so a hub-wide answer offered controls that could only fail on a
// page whose origin the hub does not serve.
//
// The harness publishes the hub at passkeyTestOrigin, so its allowed set is
// that origin plus the loopback spellings of the same scheme and port.

func systemInfoFromOrigin(t *testing.T, env passkeyAuthTestEnv, origin string) *leapmuxv1.GetSystemInfoResponse {
	t.Helper()
	req := connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{})
	if origin != "" {
		req.Header().Set("Origin", origin)
	}
	resp, err := env.client.GetSystemInfo(context.Background(), req)
	require.NoError(t, err)
	return resp.Msg
}

func TestGetSystemInfo_PasskeyEnabledFollowsTheRequestOrigin(t *testing.T) {
	t.Parallel()

	env := setupPasskeyAuthTestServer(t, nil, nil)

	assert.True(t, systemInfoFromOrigin(t, env, passkeyTestOrigin).GetPasskeyEnabled(),
		"the published origin runs ceremonies")

	// A hub bound on loopback answers to every loopback spelling, and the
	// browser -- not the server -- picks which one the ceremony runs on.
	assert.True(t, systemInfoFromOrigin(t, env, "https://127.0.0.1").GetPasskeyEnabled(),
		"a loopback spelling of the published origin runs ceremonies")

	// The state this flag exists for: a hub published at one address and
	// reached at another. Every Begin answers ErrOriginNotAllowed there, so
	// the client must not offer the option at all.
	assert.False(t, systemInfoFromOrigin(t, env, "https://elsewhere.example").GetPasskeyEnabled(),
		"an origin the hub does not serve runs no ceremony")

	// The port is part of the origin: a host-only match would admit this and
	// leave the browser to fail the assertion after the biometric prompt.
	assert.False(t, systemInfoFromOrigin(t, env, "https://localhost:8443").GetPasskeyEnabled(),
		"a different port is a different origin")

	// The scheme is too.
	assert.False(t, systemInfoFromOrigin(t, env, "http://localhost").GetPasskeyEnabled(),
		"a different scheme is a different origin")
}

func TestGetSystemInfo_PasskeyEnabledWithoutAnOriginHeader(t *testing.T) {
	t.Parallel()

	env := setupPasskeyAuthTestServer(t, nil, nil)

	// A non-browser client sends no Origin header and has no browser ceremony
	// to mislead, so it gets the hub-wide answer rather than a refusal.
	assert.True(t, systemInfoFromOrigin(t, env, "").GetPasskeyEnabled(),
		"a caller with no Origin header gets the hub-wide answer")
}

func TestGetSystemInfo_PasskeyDisabledWithoutAKeystore(t *testing.T) {
	t.Parallel()

	// No keystore: the hub runs no ceremony from any origin, so the allowed
	// origin must not report otherwise.
	client, _, _ := setupAuthTestServerBase(t, testConfig(), nil)
	req := connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{})
	req.Header().Set("Origin", passkeyTestOrigin)
	resp, err := client.GetSystemInfo(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, resp.Msg.GetPasskeyEnabled(),
		"a hub with no keystore runs no ceremony, whatever the origin")
}
