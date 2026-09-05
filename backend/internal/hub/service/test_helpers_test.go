package service_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/settings"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

var ptrTime = ptrconv.Ptr[time.Time]

// mailSenderDouble is the one mail.Sender test double this package uses:
// it records everything that reached the mail layer and can fail every
// Send. The mutex is load-bearing for the suites whose servers Serve on
// their own goroutines (the worker-registration env); the readers below
// take the same lock, so an assertion never races a Serve-side Send.
type mailSenderDouble struct {
	mu   sync.Mutex
	err  error
	msgs []mail.Message
}

func (m *mailSenderDouble) Send(_ context.Context, msg mail.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.msgs = append(m.msgs, msg)
	return nil
}

// snapshot returns every recorded message, copied so a caller cannot race
// a later append.
func (m *mailSenderDouble) snapshot() []mail.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]mail.Message(nil), m.msgs...)
}

// last returns the newest message, or nil when nothing was sent.
func (m *mailSenderDouble) last() *mail.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.msgs) == 0 {
		return nil
	}
	out := m.msgs[len(m.msgs)-1]
	return &out
}

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
func insecureCookies(context.Context) bool { return false }

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
