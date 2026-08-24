package service_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/settings"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

var ptrTime = ptrconv.Ptr[time.Time]

func authedReq[T any](msg *T, token string) *connect.Request[T] {
	req := connect.NewRequest(msg)
	req.Header().Set("Cookie", auth.CookieName+"="+token)
	return req
}

func sessionFromCookie(t *testing.T, setCookie string) string {
	return hubtestutil.SessionFromCookie(t, setCookie)
}

// insecureCookies answers the secure-cookie policy these plain-HTTP test
// servers run under.
func insecureCookies() bool { return false }

func testConfig() *config.Config {
	return &config.Config{}
}

// The seed helpers below configure the runtime settings that used to be
// config fields. They write through the settings manager the test's
// services share, so the values apply to the next request.

// enableSignup opens public signup (off by default).
func enableSignup(t *testing.T, set *settings.Manager) {
	t.Helper()
	require.NoError(t, settings.KeySignupEnabled.Set(context.Background(), set, true))
}

// seedSMTP configures a relay so email-consuming paths (verification
// mail, the registration-instructions RPC) see email as available.
func seedSMTP(t *testing.T, set *settings.Manager) {
	t.Helper()
	require.NoError(t, set.Update(context.Background(), settings.KeySMTP, json.RawMessage(
		`{"host":"smtp.example.test","port":587,"from_address":"hub@example.test","tls_mode":"starttls"}`)))
}

// enableEmailVerification turns the verification gate on by configuring SMTP.
// EmailVerificationEffective follows SMTP.Enabled() only.
func enableEmailVerification(t *testing.T, set *settings.Manager) {
	t.Helper()
	seedSMTP(t, set)
}

// setSessionDuration stamps every session a service mints with the given
// lifetime instead of the default.
func setSessionDuration(t *testing.T, set *settings.Manager, d time.Duration) {
	t.Helper()
	require.NoError(t, settings.KeySessionDurationSeconds.Set(context.Background(), set, int64(d/time.Second)))
}
