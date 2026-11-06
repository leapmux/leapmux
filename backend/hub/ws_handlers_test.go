package hub

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/util/drain"
)

// drainGateForTest aliases the gate type for readability in this file.
func drainGateForTest() *drain.Gate { return &drain.Gate{} }

// A closed gate refuses the upgrade with a 503, and the relay behind it never
// runs: a WebSocket that the shutdown answered would otherwise hijack a
// connection and lease an auth context the registry already stopped.
func TestTrackWebSocketRefusesAfterClose(t *testing.T) {
	gate := drainGateForTest()
	gate.Close(nil)
	var nextRan bool
	handler := trackWebSocket(gate, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		nextRan = true
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ws", nil))

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code, "a refused upgrade answers 503")
	assert.False(t, nextRan, "the relay must not run behind a refused admission")
}

// The wrapper counts a running handler for its whole life, so the shutdown
// drain parks on it until the handler returns.
func TestTrackWebSocketCountsARunningHandler(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := drainGateForTest()
		release := make(chan struct{})
		admitted := make(chan struct{})
		handler := trackWebSocket(gate, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			close(admitted)
			<-release
		}))

		done := make(chan struct{})
		go func() {
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ws", nil))
			close(done)
		}()
		// The drain starts only once the handler holds its count, so the
		// park below is the join and not an admission race.
		<-admitted

		drained := make(chan bool, 1)
		go func() { drained <- gate.Drain(time.Second, "") }()
		synctest.Wait()
		select {
		case <-drained:
			t.Fatal("the drain returned while the handler was parked")
		default:
		}

		close(release)
		assert.True(t, <-drained, "the released handler joins inside the budget")
		<-done
	})
}
