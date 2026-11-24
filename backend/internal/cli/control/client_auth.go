package control

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/internal/cli"
)

// AuthInterceptor adds the Authorization (or X-LeapMux-Token) header
// to every outgoing request. Exported so callers outside this package
// (e.g. cmd's hubrpc dispatch) can apply the same auth shape to a
// generically-constructed connect.NewClient.
//
// Wraps both unary AND streaming clients, which is the point: connect v2 has
// one interceptor for every RPC shape, so a header that reached only the
// unary path would silently vanish from `StreamInner`, `OpenChannel`, and
// friends -- the IPC server (or hub) then responds 401 and the CLI surfaces
// it as "unauthenticated: HTTP status 401 Unauthorized". The streaming path
// matters here because the hub's user-event stream (hubrpc.WatchUserStream,
// which `layout watch` reads over local IPC) and any future server-streaming
// RPC flow through it.
func (c *Client) AuthInterceptor() connect.ClientInterceptor {
	return (&authInterceptor{client: c}).intercept
}

// authInterceptor stamps c.ApplyAuth on every outgoing connect call,
// unary or streaming, and replays a refused unary call after a repair
// that can fix it.
type authInterceptor struct{ client *Client }

// unaryRepair is one repair the interceptor can apply to a refused unary
// call: the test that recognizes the refusal, and the action that removes
// its cause. The test also takes the response header, because connect v2
// carries no metadata on an error.
type unaryRepair struct {
	matches func(err error, responseHeader *connect.Header) bool
	repair  func(ctx context.Context) error
}

// repairs returns the repairs the interceptor can apply, in no significant
// order -- the loop picks whichever one matches the refusal it holds.
//
//   - Unauthenticated: the presented token is dead, so adopt or rotate the
//     credential. This repair exists because the proactive check reads a
//     STORED expiry, which can be wrong: a clock that moved, a token another
//     process rotated, a file written by an older build.
//   - The MARKED FailedPrecondition: the credential is live but proved no
//     factor recently, so run the browser step-up. It is keyed on the hub's
//     marker and not on the code, because a FailedPrecondition also means
//     half a dozen things a prompt cannot fix, and stopping a script to show
//     a URL for one of those would be worse than reporting it.
func (a *authInterceptor) repairs() []unaryRepair {
	return []unaryRepair{
		{matches: NeedsElevation, repair: a.client.Elevate},
		{
			matches: func(err error, _ *connect.Header) bool { return connect.CodeOf(err) == connect.CodeUnauthenticated },
			repair:  a.client.repairAfterUnauthenticated,
		},
	}
}

// intercept renews the credential before every attempt and stamps it on
// the request. A unary call is additionally replayed after each refusal
// it can repair; a stream is never replayed.
//
// Connect v2 client interceptors wrap the OPENING of a stream rather
// than the whole call, so the repair loop lives on the returned stream:
// it records the request message Send handed it and, when Receive
// reports a refusal a repair can fix, repairs and re-runs the open /
// Send / CloseSend / Receive sequence.
func (a *authInterceptor) intercept(next connect.ClientFunc) connect.ClientFunc {
	return func(ctx context.Context, spec connect.Spec) (connect.ClientStream, error) {
		// One CallInfo for the whole call, so a replay clears and restamps
		// the SAME metadata the outer caller reads back. Client call methods
		// attach one before the chain runs; make one otherwise.
		info, ok := connect.CallInfoForClientContext(ctx)
		if !ok {
			ctx, info = connect.NewClientContext(ctx)
		}
		stream, err := a.openAttempt(ctx, info, spec, next)
		if err != nil {
			return nil, err
		}
		if spec.StreamType != connect.StreamTypeUnary {
			return stream, nil
		}
		open := func() (connect.ClientStream, error) {
			return a.openAttempt(ctx, info, spec, next)
		}
		return &unaryReplayStream{ctx: ctx, info: info, open: open, stream: stream, repairs: a.repairs()}, nil
	}
}

// openAttempt renews the credential, clears earlier response metadata, and
// applies authentication before opening any RPC. Unary retries use the same
// path. Non-unary streams are opened once and are never retried.
func (a *authInterceptor) openAttempt(ctx context.Context, info *connect.CallInfo, spec connect.Spec, next connect.ClientFunc) (connect.ClientStream, error) {
	// The freshness check runs before EVERY attempt, not only the
	// first. A repair can block on a person for minutes (the browser
	// step-up lives for ten), and the access token it started with
	// lapses inside that window; a replay stamped with the token from
	// before the prompt would fail as unauthenticated, and report that
	// to a user who just finished verifying.
	if err := a.client.EnsureFreshBearer(ctx); err != nil {
		return nil, connect.NewError(bearerErrorCode(err), err.Error()).WithCause(err)
	}
	// A failed attempt left its own response metadata behind. The
	// transport overwrites only the keys the next response carries, so
	// clear first or a replayed success keeps the refusal's headers.
	clearHeader(info.ResponseHeader())
	clearHeader(info.ResponseTrailer())
	a.client.applyAuthTo(info.RequestHeader().Set)
	return next(ctx, spec)
}

// unaryReplayStream records the request message and replays the call
// through a fresh stream after a repair. Connect v2 gives a client
// interceptor the stream OPENING and not the whole call, so the repair
// loop has to drive the stream rather than wrap the call.
type unaryReplayStream struct {
	ctx     context.Context
	info    *connect.CallInfo
	open    func() (connect.ClientStream, error)
	repairs []unaryRepair
	stream  connect.ClientStream
	sent    any
}

func (s *unaryReplayStream) SendHeaders() error { return s.stream.SendHeaders() }
func (s *unaryReplayStream) Send(msg any) error { s.sent = msg; return s.stream.Send(msg) }
func (s *unaryReplayStream) CloseSend() error   { return s.stream.CloseSend() }
func (s *unaryReplayStream) Close() error       { return s.stream.Close() }

// Receive replays the call while some unused repair matches the refusal.
// Each repair runs AT MOST ONCE, in any order, and the call is replayed
// while some unused repair matches. Both properties are load-bearing:
//
//   - A call can need BOTH repairs. Another process rotates the
//     credential, so the hub answers Unauthenticated; the replay then
//     reaches the hub with a live credential that proved no factor, which
//     is the marked refusal. A fixed sequence of one repair reported that
//     second refusal raw, with no prompt.
//   - A repair that already ran cannot run again, so a hub that keeps
//     refusing ends the loop. A second refusal of the same kind is the
//     truth -- the factor was proven and the action is still refused --
//     and repeating the repair would ask the user to verify the same
//     credential for ever.
//
// A unary call is always safe to replay: the hub refused it, so it acted
// on nothing.
//
// A proactive-refresh failure that leaves a usable token is swallowed
// inside EnsureFreshBearer: the stored token may still work, and the call
// that follows reports the truth.
func (s *unaryReplayStream) Receive(res any) error {
	for {
		err := s.stream.Receive(res)
		if err == nil {
			return nil
		}
		i := slices.IndexFunc(s.repairs, func(r unaryRepair) bool {
			return r.matches(err, s.info.ResponseHeader())
		})
		if i < 0 {
			return err
		}
		repair := s.repairs[i].repair
		s.repairs = slices.Delete(s.repairs, i, i+1)
		if repairErr := repair(s.ctx); repairErr != nil {
			if errors.Is(repairErr, ErrCredentialsNotSaved) {
				return connect.NewError(bearerErrorCode(repairErr), repairErr.Error()).WithCause(repairErr)
			}
			// Keep the hub's own refusal as the primary error, and attach why
			// the step-up prompt could not run so headless callers see both the
			// refusal and how to resolve it.
			if errors.Is(repairErr, ErrElevationNeedsAPerson) {
				return &hintedError{
					err:  err,
					hint: fmt.Sprintf("cannot open an interactive verification prompt (%s); run this command in an interactive terminal", noPromptReason()),
				}
			}
			return &hintedError{
				err:  err,
				hint: fmt.Sprintf("automatic session repair failed: %v", repairErr),
			}
		}
		_ = s.stream.Close()
		next, openErr := s.open()
		if openErr != nil {
			return openErr
		}
		s.stream = next
		if msg, ok := res.(proto.Message); ok {
			// Discard the failed attempt's partial response before sending
			// the retry. The retry can fail before its decoder runs.
			proto.Reset(msg)
		}
		if sendErr := s.stream.Send(s.sent); sendErr != nil {
			return sendErr
		}
		if closeErr := s.stream.CloseSend(); closeErr != nil {
			return closeErr
		}
	}
}

// hintedError preserves the wrapped RPC error (including connect.CodeOf) while
// attaching an actionable hint when rendered through cli.FromError.
type hintedError struct {
	err  error
	hint string
}

func (h *hintedError) Error() string { return h.err.Error() }
func (h *hintedError) Unwrap() error { return h.err }
func (h *hintedError) CLIError() *cli.Error {
	return cli.FromError(h.err).WithHint("%s", h.hint)
}

// clearHeader drops every entry h holds. It is the connect.Header
// equivalent of the reset the library runs on a fresh client call.
func clearHeader(h *connect.Header) {
	for key := range h.All() {
		h.Delete(key)
	}
}

// buildHTTPClients returns the unary client, the WebSocket client, and the
// base URL ConnectRPC should target for hubURL.
//
// Both clients come from one hubtransport.Endpoint, so they share its
// connection pools and its h2c verdict. The WebSocket client is never nil,
// including for a remote hub: a nil one made coder/websocket fall back to
// http.DefaultClient, which carries no timeout of its own and shares the
// process-global pool with every other caller in the program.
func buildHTTPClients(hubURL string) (*http.Client, *http.Client, string, error) {
	endpoint, err := hubtransport.New(hubURL)
	if err != nil {
		return nil, nil, "", err
	}
	return endpoint.UnaryClient(hubtransport.DefaultUnaryTimeout), endpoint.WebSocketClient(), endpoint.BaseURL(), nil
}
