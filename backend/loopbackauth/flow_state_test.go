package loopbackauth

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

func TestWait_WrongStateKeepsWaiting(t *testing.T) {
	flow, err := Start(Request{HubBaseURL: "https://hub.example", App: ControlCLI})
	require.NoError(t, err)
	t.Cleanup(flow.Close)

	handler := flow.callbackHandler()
	answer := func(query url.Values) int {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/callback?"+query.Encode(), nil))
		return rec.Code
	}
	require.Equal(t, http.StatusBadRequest, answer(url.Values{"state": {"wrong"}, "code": {"evil"}}))
	require.Equal(t, http.StatusBadRequest, answer(url.Values{"state": {"wrong"}, "error": {"access_denied"}}))
	select {
	case <-flow.ready:
		t.Fatal("a wrong state must leave the flow pending")
	default:
	}

	require.Equal(t, http.StatusOK, answer(url.Values{"state": {flow.state}, "code": {"C-1"}}))
	code, err := flow.Wait(testutil.DeadlineContext(t))
	require.NoError(t, err)
	assert.Equal(t, "C-1", code)
}

func TestWait_PublishesTimeoutToConcurrentAndLaterWaiters(t *testing.T) {
	clock := testutil.NewQuartzMock(t)
	flow, err := Start(Request{HubBaseURL: "https://hub.example", App: ControlCLI, Clock: clock})
	require.NoError(t, err)
	t.Cleanup(flow.Close)
	ctx := testutil.DeadlineContext(t)

	const waiterCount = 3
	waiting := make(chan struct{}, waiterCount)
	results := make(chan error, waiterCount)
	for range waiterCount {
		go func() {
			waiting <- struct{}{}
			_, err := flow.Wait(ctx)
			results <- err
		}()
	}
	for range waiterCount {
		select {
		case <-waiting:
		case <-ctx.Done():
			t.Fatal("a waiter did not start")
		}
	}
	clock.Advance(Timeout).MustWait(ctx)
	select {
	case err := <-results:
		require.ErrorIs(t, err, ErrTimeout)
	case <-ctx.Done():
		t.Fatal("no waiter received the timeout")
	}
	select {
	case <-flow.ready:
	default:
		t.Fatal("Wait returned the timeout without publishing it to the other waiters")
	}
	for range waiterCount - 1 {
		select {
		case err := <-results:
			assert.ErrorIs(t, err, ErrTimeout)
		case <-ctx.Done():
			t.Fatal("a concurrent waiter did not receive the timeout")
		}
	}
	code, err := flow.Wait(ctx)
	assert.ErrorIs(t, err, ErrTimeout)
	assert.Empty(t, code)
}

func TestClose_ClosesAnActiveCallbackThatDoesNotFinish(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	called := make(chan struct{})
	stopped := make(chan struct{})
	flow := &Flow{
		timer: testutil.NewQuartzMock(t).NewTimer(Timeout, timeoutTag),
		srv: &http.Server{Handler: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			close(called)
			<-r.Context().Done()
			close(stopped)
		})},
	}
	t.Cleanup(func() { _ = flow.srv.Close() })
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = flow.srv.Serve(listener) }()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+listener.Addr().String()+"/callback", nil)
	require.NoError(t, err)
	finishedRequest := make(chan struct{})
	go func() {
		defer close(finishedRequest)
		if res, err := http.DefaultClient.Do(req); err == nil {
			_ = res.Body.Close()
		}
	}()
	select {
	case <-called:
	case <-ctx.Done():
		t.Fatal("the callback request did not reach the server")
	}

	closed := make(chan struct{})
	go func() {
		defer close(closed)
		flow.Close()
	}()
	for _, completed := range []<-chan struct{}{closed, stopped, finishedRequest} {
		select {
		case <-completed:
		case <-ctx.Done():
			t.Fatal("Close did not finish an active callback")
		}
	}
}
