package service_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/oauthapp"
)

// startDeviceAuthorization opens a device grant and returns its two codes.
//
// It fills client_id, which RFC 8628 section 3.1 requires and every caller here
// wants the same value for. A test that omitted it got a 400 whose JSON body
// carried no device_code, and the type assertion that followed panicked -- which
// killed the whole binary, so the rest of the package reported as passing.
func startDeviceAuthorization(t *testing.T, env *apiAuthEnv, form url.Values) (deviceCode, userCode string) {
	t.Helper()
	values := url.Values{"client_id": {oauthapp.ControlCLIClientID}}
	for k, v := range form {
		values[k] = v
	}
	resp, err := http.PostForm(env.server.URL+"/oauth/device-authorization", values)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var grant map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&grant))
	require.Equalf(t, http.StatusOK, resp.StatusCode, "the device leg refused: %v", grant)

	deviceCode, _ = grant["device_code"].(string)
	userCode, _ = grant["user_code"].(string)
	require.NotEmpty(t, deviceCode)
	require.NotEmpty(t, userCode)
	return deviceCode, userCode
}
