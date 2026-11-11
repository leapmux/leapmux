package hub

import (
	"net/http"
	"time"

	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/util/drain"
)

// webSocketDrainTimeout limits the wait for the hijacked WebSocket handlers.
// http.Server.Shutdown and Close cannot reach one -- a hijacked handler leaves
// net/http's connection map -- so this drain is the only join they get.
const webSocketDrainTimeout = 5 * time.Second

// webSocketShutdownBody is the 503 a refused upgrade carries. On the browser
// side an abnormal upgrade failure is a reconnect signal, which is the right
// answer during a shutdown.
const webSocketShutdownBody = "hub is shutting down"

// trackWebSocket counts a hijacked WebSocket handler into gate for the whole
// life of its handler, and refuses the upgrade with a 503 once the gate is
// closed.
//
// NewServer wraps every WebSocket route with one gate, so the shutdown in
// Serve closes admission, cancels the handlers, and drains, up to a limit, before
// the store closes. Without it, an in-flight relay could lease an auth context
// after AuthContextRegistry.Stop and then read a closed store.
//
// The wrapper passes w through unchanged, so a handler that asserts
// http.Hijacker still finds it: the counting adds no response writing of its
// own on an admitted call.
func trackWebSocket(gate *drain.Gate, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		done, admitted := gate.Begin(nil)
		if !admitted {
			http.Error(w, webSocketShutdownBody, http.StatusServiceUnavailable)
			return
		}
		defer done()
		next.ServeHTTP(w, r)
	})
}

// backgroundLoopDrainTimeout limits the join of the periodic background loops.
const backgroundLoopDrainTimeout = max(5*time.Second, service.OAuthTokenRefreshJoinTimeout)

// joinLoops waits, under one deadline, for every background loop's stopped
// channel to close. The deadline spans the whole set, so a wedged loop costs
// the shutdown once. An expired wait leaves the pass running: the store close
// fails its next read, which the loop logs, and the process exit ends it.
func joinLoops(timeout time.Duration, loops []<-chan struct{}) {
	deadline := time.Now().Add(timeout)
	for _, loop := range loops {
		if loop == nil {
			continue
		}
		drain.WaitBounded(loop, time.Until(deadline),
			"hub: background loop did not stop in the shutdown budget; abandoning its pass")
	}
}
