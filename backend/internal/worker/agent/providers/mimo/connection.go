package mimo

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// mimoListenPattern matches the stdout line `mimo serve` prints once it
// listens. MiMo 0.1.14 tries port 4096 first for `--port 0` and falls back to a
// free port, so the port is read from this line and never assumed. The address
// ends at an escape character as well as at a space, so a terminal color code
// around the line does not become part of it.
var mimoListenPattern = regexp.MustCompile(`mimocode server listening on (http://[^\s\x1b]+)`)

// The launch environment. MiMo reads the credential from its environment, and
// never from argv, where any local user could read it from the process list.
//
// The credential is exposed to every command that the agent runs. MiMo removes
// only MIMOCODE_AUTH_CONTENT and MIMOCODE_CONFIG_CONTENT from the environment
// of its child processes (util/credential-env.ts), so each `bash` command and
// each local MCP server inherits MIMOCODE_SERVER_PASSWORD. Such a process can
// call the server: for example, it can turn the skip-all switch on, or answer
// its own permission request, while LeapMux still shows the old policy. The
// worker cannot remove a variable that MiMo itself must read, so only MiMo can
// stop this exposure, by adding the variable to that list. That change stops
// the inheritance only: on Linux, a process of the same user can still read the
// server's environment from /proc.
const (
	envServerPassword = "MIMOCODE_SERVER_PASSWORD"
	envServerUsername = "MIMOCODE_SERVER_USERNAME"
	// envQuestionTool turns on the question tool. MiMo turns it on by itself for
	// the `cli` client that `serve` runs as, and the pin keeps an inherited `0`
	// from turning it off.
	envQuestionTool = "MIMOCODE_ENABLE_QUESTION_TOOL"
	// envClient names the client MiMo runs for. An inherited `acp` would turn
	// the question tool off, so the launch strips it, and MiMo's default applies.
	envClient = "MIMOCODE_CLIENT"
	// envAutoApproveDelete and envSkipPermissions seed MiMo's auto-approve-delete
	// switch at startup, and envSkipPermissions also merges an allow-all rule
	// under the user's permission rules. The permission-policy axis owns both
	// switches, so an inherited value must not decide them.
	envAutoApproveDelete = "MIMOCODE_AUTO_APPROVE_DELETE"
	envSkipPermissions   = "MIMOCODE_DANGEROUSLY_SKIP_PERMISSIONS"
)

// mimoStripEnvKeys are the inherited variables that the launch removes. The
// shell wrapper removes them after the login profile runs, so a profile that
// exports one cannot put it back.
var mimoStripEnvKeys = []string{envClient, envAutoApproveDelete, envSkipPermissions}

// mimoLaunchEnv pins the variables the server needs over whatever the worker
// inherited: an inherited password would lock the worker out of its own server.
func mimoLaunchEnv(environ []string, secret string, opts agent.Options) []string {
	env := envutil.PinEnv(environ,
		envServerPassword+"="+secret,
		envServerUsername+"="+serverUser,
		envQuestionTool+"=1",
	)
	return providerkit.FinalizeAgentEnv(env, opts)
}

// Event-stream limits.
const (
	// mimoMaxEventBytes limits one event. A tool part carries the tool's whole
	// output and a diff, which MiMo truncates to tens of kilobytes; the limit
	// is far above that and still stops a runaway event.
	mimoMaxEventBytes = 32 << 20
	// The wait between two connections of the stream, doubling from the first
	// to the cap.
	mimoStreamRetryFirst = 100 * time.Millisecond
	mimoStreamRetryMax   = 5 * time.Second
	// mimoRestateTimeout limits the reads that restate the server's state after
	// a reconnect. The stream waits for them, so they must end.
	mimoRestateTimeout = 30 * time.Second
)

// runEventStream reads the server's event stream until ctx ends or the process
// exits, and connects again whenever the stream ends before that. Only this
// goroutine dispatches events.
func (a *Agent) runEventStream(ctx context.Context) {
	defer close(a.streamDone)
	delay := mimoStreamRetryFirst
	for {
		connected, err := a.consumeEventStream(ctx)
		if ctx.Err() != nil {
			return
		}
		select {
		case <-a.ProcessDone():
			return
		default:
		}
		if connected {
			delay = mimoStreamRetryFirst
		}
		slog.Debug("mimo event stream ended; reconnecting", "agent_id", a.AgentID(), "error", err, "delay", delay)
		timer := a.clock.NewTimer(delay, mimoStreamReconnectTimerTag)
		select {
		case <-ctx.Done():
			timer.Stop(mimoStreamReconnectTimerTag)
			return
		case <-a.ProcessDone():
			timer.Stop(mimoStreamReconnectTimerTag)
			return
		case <-timer.C:
		}
		delay = min(delay*2, mimoStreamRetryMax)
	}
}

// consumeEventStream reads ONE connection of the stream until it ends. It
// reports whether the connection reached server.connected.
func (a *Agent) consumeEventStream(ctx context.Context) (bool, error) {
	response, err := a.rpc.endpoint.OpenStream(ctx, http.MethodGet, routeEvents, http.Header{"Accept": {"text/event-stream"}})
	if err != nil {
		return false, err
	}
	// Closed without draining: the stream stays open for the whole session, and a
	// drain would block until the server exits.
	defer func() { _ = response.Body.Close() }()
	connected := false
	err = providerkit.ReadSSE(response.Body, mimoMaxEventBytes, func(event providerkit.SSEEvent) {
		if isConnectedEvent(event.Data) {
			connected = true
			a.onStreamConnected(ctx)
			return
		}
		a.dispatchEvent(event.Data)
	})
	return connected, err
}

// isConnectedEvent reports whether data is server.connected, the first event of
// every connection.
func isConnectedEvent(data []byte) bool {
	if !bytes.Contains(data, []byte(eventServerConnected)) {
		return false
	}
	var event struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(data, &event) == nil && event.Type == eventServerConnected
}

// onStreamConnected runs at each server.connected. The first one releases the
// start path, which creates the session only once no event of it can be lost.
// A later one follows a reconnect, and restates what the gap hid.
func (a *Agent) onStreamConnected(ctx context.Context) {
	first := false
	a.connectedOnce.Do(func() {
		first = true
		close(a.connected)
	})
	if first {
		return
	}
	restateCtx, cancel := context.WithTimeout(ctx, mimoRestateTimeout)
	defer cancel()
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	a.reconcileTurnLocked(restateCtx)
	a.restatePendingControls(restateCtx)
}

// reconcileTurnLocked brings the turn flag in line with the server's own
// status. An event stream that dropped an idle, or an abort that the server
// answered without one, leaves a turn armed that nothing would end. The caller
// holds a.dispatchMu.
func (a *Agent) reconcileTurnLocked(ctx context.Context) {
	a.Mu.Lock()
	sessionID, active := a.sessionID, a.turnActive
	a.Mu.Unlock()
	if sessionID == "" {
		return
	}
	statuses, err := a.rpc.sessionStatuses(ctx)
	if err != nil {
		slog.Debug("mimo read session status", "agent_id", a.AgentID(), "error", err)
		return
	}
	status, busy := statuses[sessionID]
	switch {
	case busy && status.Type != contracts.MiMoStatusTypeIdle && !active:
		a.beginTurn()
	case (!busy || status.Type == contracts.MiMoStatusTypeIdle) && active:
		a.endTurn(syntheticIdleEvent(sessionID))
	}
}

// reconcileTurn is reconcileTurnLocked for a caller outside the stream.
func (a *Agent) reconcileTurn() {
	ctx, cancel := context.WithTimeout(a.Context(), a.APITimeout())
	defer cancel()
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	a.reconcileTurnLocked(ctx)
}

// syntheticIdleEvent is the idle status the server sends at a turn end, for a
// turn the worker ends on the server's word without that event. It has the
// exact shape of the real one, so the row reads the same.
func syntheticIdleEvent(sessionID string) []byte {
	raw, err := json.Marshal(map[string]any{
		"type": contracts.MiMoEventSessionStatus,
		"properties": map[string]any{
			"sessionID": sessionID,
			"status":    map[string]string{"type": contracts.MiMoStatusTypeIdle},
		},
	})
	if err != nil {
		// Every value is a string, so this cannot fail.
		slog.Error("mimo marshal idle event", "error", err)
	}
	return raw
}
