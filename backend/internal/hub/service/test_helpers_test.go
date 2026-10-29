package service_test

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
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

// probeStream is the unused ServerStream of a probe call: the handler under
// test reads and writes no messages.
type probeStream struct{}

func (probeStream) Receive(any) error  { return io.EOF }
func (probeStream) SendHeaders() error { return nil }
func (probeStream) Send(any) error     { return nil }

// callUnderServerInfo runs fn the way a transport would: through
// connect.Server.Call, which attaches the server CallInfo that response
// headers ride to base. It returns that CallInfo so a test can read the
// headers fn set. A bare method call carries no CallInfo, so no header would
// be written or observable at all.
func callUnderServerInfo(base context.Context, fn func(ctx context.Context) error) (*connect.CallInfo, error) {
	const procedure = "/leapmux.v1.test/Probe"
	srv := connect.NewServer()
	srv.Register(connect.Method{
		Spec:    connect.Spec{Procedure: procedure, StreamType: connect.StreamTypeUnary},
		Handler: func(ctx context.Context, _ connect.Spec, _ connect.ServerStream) error { return fn(ctx) },
	})
	info := &connect.CallInfo{}
	err := srv.Call(base, procedure, info, probeStream{})
	return info, err
}

// clientCallInfo returns the client CallInfo a call on ctx will use: the
// handle the caller already seeded, so the caller keeps reading the response
// through it, or a fresh one attached to the returned context.
func clientCallInfo(ctx context.Context) (context.Context, *connect.CallInfo) {
	if info, ok := connect.CallInfoForClientContext(ctx); ok {
		return ctx, info
	}
	return connect.NewClientContext(ctx)
}

// authedReq returns a context carrying the session cookie and the message
// unchanged, so a client call takes the pair as its whole (ctx, req)
// argument list: client.Foo(authedReq(ctx, msg, token)).
func authedReq[T any](ctx context.Context, msg *T, token string) (context.Context, *T) {
	ctx, info := clientCallInfo(ctx)
	info.RequestHeader().Set("Cookie", auth.CookieName+"="+token)
	return ctx, msg
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
