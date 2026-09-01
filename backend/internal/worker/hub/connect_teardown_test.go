package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
)

// receiveBudget bounds every await in this file. Generous against what the
// teardown actually costs (milliseconds) and well under the package's test
// timeout, so a wedge fails here with a named message rather than as a panic.
const receiveBudget = 5 * time.Second

// requireReceive returns the value ch delivers, failing the test with failMsg
// if nothing arrives inside receiveBudget. Every await in this file is "the
// teardown must publish X"; folding the select into a helper keeps the
// ASSERTION on the received value visible instead of buried in boilerplate.
func requireReceive[T any](t *testing.T, ch <-chan T, failMsg string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(receiveBudget):
		t.Fatal(failMsg)
		var zero T
		return zero
	}
}

// greetThenSilentConnector is a Hub stand-in that greets the worker with its
// identity -- exactly as the real Connect handler does, before it publishes the
// connection -- and then says nothing more, which is the shape a Hub takes the
// moment it starts shutting down.
//
// Both halves matter. The greeting delivers the HTTP/2 response headers, which
// is what makes the transport's RoundTrip return and retire the only per-stream
// context watcher there is; after it, a parked read can only be woken by a
// frame or by an explicit close. The silence that follows is what leaves the
// worker's receive loop parked there.
type greetThenSilentConnector struct {
	streamEnd chan error
}

func (*greetThenSilentConnector) Register(
	context.Context, *connect.Request[leapmuxv1.RegisterRequest],
) (*connect.Response[leapmuxv1.RegisterResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (s *greetThenSilentConnector) Connect(
	_ context.Context, stream *connect.BidiStream[leapmuxv1.ConnectRequest, leapmuxv1.ConnectResponse],
) error {
	greeted := false
	for {
		if _, err := stream.Receive(); err != nil {
			s.streamEnd <- err
			return nil
		}
		if !greeted {
			greeted = true
			if err := stream.Send(&leapmuxv1.ConnectResponse{
				Payload: &leapmuxv1.ConnectResponse_WorkerIdentity{
					WorkerIdentity: &leapmuxv1.WorkerIdentity{RegisteredBy: "test-owner"},
				},
			}); err != nil {
				return err
			}
		}
	}
}

// startGreetThenSilentConnector serves the connector over unencrypted HTTP/2,
// which is what the worker's own client speaks, and returns it alongside the
// server so a test can drive the same graceful shutdown the Hub runs.
func startGreetThenSilentConnector(t *testing.T) (*greetThenSilentConnector, *httptest.Server) {
	t.Helper()

	svc := &greetThenSilentConnector{streamEnd: make(chan error, 1)}
	mux := http.NewServeMux()
	mux.Handle(leapmuxv1connect.NewWorkerConnectorServiceHandler(svc))

	server := httptest.NewUnstartedServer(mux)
	server.Config.Protocols = &http.Protocols{}
	server.Config.Protocols.SetHTTP1(true)
	server.Config.Protocols.SetUnencryptedHTTP2(true)
	server.Start()
	t.Cleanup(server.Close)

	return svc, server
}

// connectAndPark dials the server and returns once the worker has consumed the
// greeting -- at which point its receive loop has nothing left to do but park
// in Receive. The returned cancel stands in for a reconnect, the identity
// watchdog, or worker shutdown.
func connectAndPark(t *testing.T, server *httptest.Server) (client *Client, cancel context.CancelFunc, connectDone <-chan error) {
	t.Helper()

	client = newTestClient(t, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	done := make(chan error, 1)
	go func() { done <- client.Connect(ctx, "test-token") }()

	require.Eventually(t, client.identityReceived.Load, 10*time.Second, 5*time.Millisecond,
		"worker never consumed the hub's greeting")
	// Let the receive loop park INSIDE the read rather than at connect-go's
	// pre-read context check, which would pass these tests for the wrong
	// reason. Overshooting only costs the wait; undershooting costs a false
	// pass, never a false failure.
	time.Sleep(100 * time.Millisecond)

	return client, cancel, done
}

// TestConnect_CancellingTheContextEndsTheStreamForTheHub pins the teardown
// every worker-initiated reconnect depends on.
//
// Cancelling the connection context is not, by itself, visible to the peer:
// connect-go checks the context before each read, so a Receive already parked
// inside the HTTP/2 response body has to be woken by the transport -- and once
// the response headers have arrived there is no per-stream context watcher left
// to do it. Without an explicit close the worker parks forever: the identity
// watchdog and the write-failure path both "force a reconnect" by cancelling,
// and Connect would never return for the reconnect loop to run.
func TestConnect_CancellingTheContextEndsTheStreamForTheHub(t *testing.T) {
	svc, server := startGreetThenSilentConnector(t)
	_, cancel, connectDone := connectAndPark(t, server)

	cancel()

	streamErr := requireReceive(t, svc.streamEnd,
		"hub still holds the stream after the worker cancelled its connection")
	assert.Error(t, streamErr, "the Hub's Receive must fail rather than block on a stream nobody will feed")

	connectErr := requireReceive(t, connectDone,
		"Connect never returned after its context was cancelled")
	require.Error(t, connectErr, "Connect must return so the reconnect loop can decide what to do next")
}

// TestConnect_CancelWhileSendingIsSafe covers the collision the teardown
// introduces: closing the request side runs on its own goroutine while the send
// queue's drain may be mid-write on the very pipe being closed, and the
// shutdown path is exactly where that overlap is likeliest -- Shutdown's last
// act broadcasts the disconnect notice, then the connection is cancelled.
//
// A closed pipe must surface as an ordinary send error, never a panic or a
// torn write, and the cancellation must still end the stream. Run under -race
// this also pins that the two goroutines share nothing unguarded.
func TestConnect_CancelWhileSendingIsSafe(t *testing.T) {
	svc, server := startGreetThenSilentConnector(t)
	client, cancel, connectDone := connectAndPark(t, server)

	stopSending := make(chan struct{})
	sendingDone := make(chan struct{})
	go func() {
		defer close(sendingDone)
		for {
			select {
			case <-stopSending:
				return
			default:
			}
			// Errors are the expected outcome once the writer is torn down;
			// what must not happen is a panic or a hang.
			_ = client.Send(heartbeatMsg())
		}
	}()

	cancel()

	connectErr := requireReceive(t, connectDone,
		"Connect never returned while sends raced the teardown")
	require.Error(t, connectErr, "Connect must return even while a producer is still calling Send")

	close(stopSending)
	requireReceive(t, sendingDone,
		"Send is wedged on a request body the teardown closed underneath it")

	streamErr := requireReceive(t, svc.streamEnd,
		"hub still holds the stream after a cancellation that raced in-flight sends")
	assert.Error(t, streamErr, "the Hub must see the stream end, not a half-written frame it waits on")
}

// TestConnect_CancelAfterGoAwayStillReleasesTheHubsDrain is the shape that
// actually broke CI: the Hub decides to shut down first (GOAWAY, no more
// frames), and only afterwards does the worker cancel its connection.
//
// The Hub's Connect handler then runs to its 10s idle timeout, and
// http.Server.Shutdown -- which waits on the connection that handler holds,
// since an unencrypted-HTTP/2 connection counts as ACTIVE for its whole life --
// burns its entire budget and reports a deadline the operator reads as a failed
// shutdown.
func TestConnect_CancelAfterGoAwayStillReleasesTheHubsDrain(t *testing.T) {
	svc, server := startGreetThenSilentConnector(t)
	_, cancel, connectDone := connectAndPark(t, server)

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancelShutdown)
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- server.Config.Shutdown(shutdownCtx) }()

	// Let the drain get as far as GOAWAY before the worker reacts. Same
	// asymmetry as above: too short only risks a false pass.
	time.Sleep(100 * time.Millisecond)
	cancel()

	shutdownErr := requireReceive(t, shutdownDone,
		"hub drain is still waiting on the worker's connection")
	require.NoError(t, shutdownErr, "the drain must finish once the worker lets its stream go")

	connectErr := requireReceive(t, connectDone,
		"Connect never returned after its context was cancelled")
	require.Error(t, connectErr, "Connect must return so the reconnect loop can apply the Hub's retry delay")

	// Deliberately NOT requireReceive: this one must NOT wait. The drain has
	// already finished, so the handler's observation must already be recorded.
	select {
	case err := <-svc.streamEnd:
		assert.Error(t, err, "the Hub's Connect handler must have observed the stream end, not just stopped waiting")
	default:
		assert.Fail(t, "hub drain finished without its Connect handler seeing the stream end")
	}
}
