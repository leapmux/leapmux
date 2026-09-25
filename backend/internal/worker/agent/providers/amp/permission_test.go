package amp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// The permission tests run the REAL helper function against the agent's real
// bridge, over the bridge's Unix domain socket, so each test covers the path
// that Amp's delegate rule takes: helper -> bridge -> agent -> banner -> answer
// -> exit code.

// syncBuffer is a stderr that a helper goroutine writes and a test reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(data)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// helperRun is one run of the permission helper.
type helperRun struct {
	code   chan int
	stderr *syncBuffer
	cancel context.CancelFunc
}

// exitCode waits for the helper to exit.
func (r *helperRun) exitCode(t *testing.T) int {
	t.Helper()
	select {
	case code := <-r.code:
		return code
	case <-time.After(30 * time.Second):
		t.Fatal("the permission helper did not exit")
		return -1
	}
}

// assertStillWaits checks that the helper still runs.
func (r *helperRun) assertStillWaits(t *testing.T) {
	t.Helper()
	select {
	case code := <-r.code:
		t.Fatalf("the permission helper exited with %d before an answer; stderr: %s", code, r.stderr.String())
	default:
	}
}

func (h *harness) helperConfig() json.RawMessage {
	h.t.Helper()
	return bridgeHelperConfig(h.t, h.agent.bridge)
}

// bridgeHelperConfig is the helper spec that points at bridge.
func bridgeHelperConfig(t *testing.T, bridge *permissionBridge) json.RawMessage {
	t.Helper()
	config, err := json.Marshal(helperConfig{Endpoint: bridge.endpoint(), Secret: bridge.secretText()})
	require.NoError(t, err)
	return config
}

// runHelper starts the helper as Amp's delegate rule does for one call.
func (h *harness) runHelper(tool, input string) *helperRun {
	return runHelperWith(h.t, h.helperConfig(), map[string]string{envToolName: tool, envThreadID: "T-1"}, strings.NewReader(input))
}

func runHelperWith(t *testing.T, config json.RawMessage, env map[string]string, stdin io.Reader) *helperRun {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	run := &helperRun{code: make(chan int, 1), stderr: &syncBuffer{}, cancel: cancel}
	go func() {
		run.code <- runPermissionHelper(ctx, agent.HelperInvocation{
			Config: config,
			Stdin:  stdin,
			Stdout: io.Discard,
			Stderr: run.stderr,
			Getenv: func(key string) string { return env[key] },
		})
	}()
	return run
}

// awaitPublished waits until n permission requests reached the sink.
func (h *harness) awaitPublished(n int) {
	h.t.Helper()
	require.Eventually(h.t, func() bool { return h.sink.PublishedControlCount() >= n },
		30*time.Second, 2*time.Millisecond, "the agent published %d permission requests", n)
}

// awaitCanceled waits until the sink withdrew the banner of requestID.
func (h *harness) awaitCanceled(requestID string) {
	h.t.Helper()
	require.Eventually(h.t, func() bool {
		for _, id := range h.sink.CanceledControls() {
			if id == requestID {
				return true
			}
		}
		return false
	}, 30*time.Second, 2*time.Millisecond, "the agent withdrew the banner of %s", requestID)
}

// controlAnswer is the neutral envelope the browser sends for a banner.
func controlAnswer(t *testing.T, requestID, behavior, message string) []byte {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response":   map[string]any{"behavior": behavior, "message": message},
		},
	})
	require.NoError(t, err)
	return encoded
}

const shellInput = `{"command":"rm -rf build","cwd":"/work"}`

// startToolTurn sends a message and feeds the tool call that the helper then
// asks about.
func (h *harness) startToolTurn(blocks ...string) *fakeProc {
	h.t.Helper()
	fp := h.send("go")
	h.feed(fp, initLine("T-1"))
	if len(blocks) == 0 {
		blocks = []string{toolUseBlock("TU-shell", "shell_command", shellInput)}
	}
	h.feed(fp, assistantLine("["+strings.Join(blocks, ",")+"]", "tool_use"))
	return fp
}

func TestPermissionAskPublishesABannerAndAllowExitsZero(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.startToolTurn()

	run := h.runHelper("shell_command", shellInput)
	h.awaitPublished(1)
	run.assertStillWaits(t)

	published := h.sink.LastPublishedControl()
	payload := permissionPayload(t, published.Payload)
	assert.Equal(t, contracts.AmpPermissionRequestTypeRequest, payload.Type)
	assert.Equal(t, "shell_command", payload.ToolName)
	assert.Equal(t, "TU-shell", payload.ToolUseID, "the banner links the call it is about")
	assert.JSONEq(t, shellInput, string(payload.Input))
	stored, err := h.sink.ReadToolRequest("TU-shell")
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, stored.Seq, published.SourceSeq, "the banner points at the row that opened the call")
	assert.True(t, strings.HasPrefix(published.RequestID, "amp-permission-"))

	require.NoError(t, h.agent.SendRawInput(controlAnswer(t, published.RequestID, agent.ControlBehaviorAllow, "")))
	assert.Equal(t, helperExitAllow, run.exitCode(t))
	assert.Empty(t, run.stderr.String(), "an allowed call leaves stderr empty")
	assert.Zero(t, h.agent.bridge.pendingCount())
}

func TestPermissionRejectWithFeedbackReachesTheModel(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.startToolTurn()
	run := h.runHelper("shell_command", shellInput)
	h.awaitPublished(1)

	requestID := h.sink.LastPublishedControl().RequestID
	require.NoError(t, h.agent.SendRawInput(controlAnswer(t, requestID, agent.ControlBehaviorDeny, "  Use the clean target instead.  ")))
	assert.Equal(t, helperExitReject, run.exitCode(t))
	assert.Equal(t, "Use the clean target instead.\n", run.stderr.String(),
		"Amp hands stderr to the model as the reason, so it holds the reason and nothing else")
}

func TestPermissionRejectWithoutFeedbackLeavesStderrEmpty(t *testing.T) {
	t.Parallel()
	for name, message := range map[string]string{
		"empty":       "",
		"placeholder": agent.ControlRejectedByUserMessage,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.startToolTurn()
			run := h.runHelper("shell_command", shellInput)
			h.awaitPublished(1)
			requestID := h.sink.LastPublishedControl().RequestID
			require.NoError(t, h.agent.SendRawInput(controlAnswer(t, requestID, agent.ControlBehaviorDeny, message)))
			assert.Equal(t, helperExitReject, run.exitCode(t))
			assert.Empty(t, run.stderr.String(), "Amp refuses in its own wording")
		})
	}
}

func TestPermissionAllowAllAnswersWithNoBanner(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withOptions(map[string]string{agent.OptionIDPermissionMode: contracts.AmpPermissionModeAllowAll}))
	h.startToolTurn()
	run := h.runHelper("shell_command", shellInput)
	assert.Equal(t, helperExitAllow, run.exitCode(t))
	assert.Zero(t, h.sink.PublishedControlCount())
}

// The mode is read when each request arrives, so a change applies to the next
// call with no restart.
func TestPermissionFollowsTheCurrentMode(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.startToolTurn(
		toolUseBlock("TU-1", "shell_command", `{"command":"ls"}`),
		toolUseBlock("TU-2", "shell_command", `{"command":"pwd"}`),
	)

	h.agent.UpdateSettings(map[string]string{agent.OptionIDPermissionMode: contracts.AmpPermissionModeAllowAll})
	assert.Equal(t, helperExitAllow, h.runHelper("shell_command", `{"command":"ls"}`).exitCode(t))
	assert.Zero(t, h.sink.PublishedControlCount())

	h.agent.UpdateSettings(map[string]string{agent.OptionIDPermissionMode: contracts.AmpPermissionModeAsk})
	run := h.runHelper("shell_command", `{"command":"pwd"}`)
	h.awaitPublished(1)
	assert.Equal(t, "TU-2", permissionPayload(t, h.sink.LastPublishedControl().Payload).ToolUseID)
	require.NoError(t, h.agent.SendRawInput(controlAnswer(t, h.sink.LastPublishedControl().RequestID, agent.ControlBehaviorAllow, "")))
	assert.Equal(t, helperExitAllow, run.exitCode(t))
}

// Two identical calls in parallel: the helper states no tool-use id, so the
// requests claim the calls first in, first out, and each banner links a row of
// its own. Each answer reaches its own helper.
func TestPermissionTwoIdenticalCallsMapOntoTwoRows(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.startToolTurn(
		toolUseBlock("TU-a", "shell_command", shellInput),
		toolUseBlock("TU-b", "shell_command", shellInput),
	)

	first := h.runHelper("shell_command", shellInput)
	h.awaitPublished(1)
	// Key order differs: the match compares the JSON values, not the bytes.
	second := h.runHelper("shell_command", `{"cwd":"/work","command":"rm -rf build"}`)
	h.awaitPublished(2)

	published := h.sink.PublishedControls()
	require.Len(t, published, 2)
	assert.Equal(t, "TU-a", permissionPayload(t, published[0].Payload).ToolUseID, "the older call goes to the first request")
	assert.Equal(t, "TU-b", permissionPayload(t, published[1].Payload).ToolUseID)
	assert.NotEqual(t, published[0].SourceSeq, published[1].SourceSeq)
	assert.NotEqual(t, published[0].RequestID, published[1].RequestID)

	require.NoError(t, h.agent.SendRawInput(controlAnswer(t, published[1].RequestID, agent.ControlBehaviorDeny, "not the second")))
	assert.Equal(t, helperExitReject, second.exitCode(t))
	assert.Equal(t, "not the second\n", second.stderr.String())
	first.assertStillWaits(t)

	require.NoError(t, h.agent.SendRawInput(controlAnswer(t, published[0].RequestID, agent.ControlBehaviorAllow, "")))
	assert.Equal(t, helperExitAllow, first.exitCode(t))
}

// Two requests that arrive at the same moment still claim two different calls.
func TestPermissionConcurrentIdenticalRequestsClaimDistinctCalls(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.startToolTurn(
		toolUseBlock("TU-a", "shell_command", shellInput),
		toolUseBlock("TU-b", "shell_command", shellInput),
	)
	runs := []*helperRun{h.runHelper("shell_command", shellInput), h.runHelper("shell_command", shellInput)}
	h.awaitPublished(2)

	ids := map[string]bool{}
	for _, published := range h.sink.PublishedControls() {
		ids[permissionPayload(t, published.Payload).ToolUseID] = true
		require.NoError(t, h.agent.SendRawInput(controlAnswer(t, published.RequestID, agent.ControlBehaviorAllow, "")))
	}
	assert.Equal(t, map[string]bool{"TU-a": true, "TU-b": true}, ids)
	for _, run := range runs {
		assert.Equal(t, helperExitAllow, run.exitCode(t))
	}
}

// A request that arrives before its call's line waits for the line.
func TestPermissionWaitsForTheCallToAppear(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	trap := h.clock.Trap().NewTimer("amp", "permission-match")
	defer trap.Close()
	fp := h.send("go")

	run := h.runHelper("shell_command", shellInput)
	call := trap.MustWait(t.Context())
	call.MustRelease(t.Context())
	assert.Zero(t, h.sink.PublishedControlCount(), "the request waits for its call")

	h.feed(fp, assistantLine("["+toolUseBlock("TU-late", "shell_command", shellInput)+"]", "tool_use"))
	h.awaitPublished(1)
	assert.Equal(t, "TU-late", permissionPayload(t, h.sink.LastPublishedControl().Payload).ToolUseID)
	require.NoError(t, h.agent.SendRawInput(controlAnswer(t, h.sink.LastPublishedControl().RequestID, agent.ControlBehaviorAllow, "")))
	assert.Equal(t, helperExitAllow, run.exitCode(t))
}

// A call whose line never appears -- a subagent's, which Amp does not print --
// gets a banner with no transcript link once the wait ends.
func TestPermissionWithNoMatchingCallPublishesWithoutALink(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	trap := h.clock.Trap().NewTimer("amp", "permission-match")
	defer trap.Close()
	h.startToolTurn(toolUseBlock("TU-other", "shell_command", `{"command":"ls"}`))

	run := h.runHelper("edit_file", `{"path":"/work/a.go"}`)
	call := trap.MustWait(t.Context())
	call.MustRelease(t.Context())
	h.clock.Advance(toolCallWait).MustWait(t.Context())
	h.awaitPublished(1)

	published := h.sink.LastPublishedControl()
	payload := permissionPayload(t, published.Payload)
	assert.Empty(t, payload.ToolUseID)
	assert.Equal(t, "edit_file", payload.ToolName)
	assert.Zero(t, published.SourceSeq)
	require.NoError(t, h.agent.SendRawInput(controlAnswer(t, published.RequestID, agent.ControlBehaviorAllow, "")))
	assert.Equal(t, helperExitAllow, run.exitCode(t))
}

// A helper that goes away -- Amp killed it, or it received a signal --
// withdraws its banner, so no answer waits for a helper that is gone.
func TestPermissionHelperThatGoesAwayWithdrawsTheBanner(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.startToolTurn()
	run := h.runHelper("shell_command", shellInput)
	h.awaitPublished(1)
	requestID := h.sink.LastPublishedControl().RequestID

	run.cancel()
	assert.Equal(t, helperExitReject, run.exitCode(t))
	assert.Contains(t, run.stderr.String(), "termination signal")
	h.awaitCanceled(requestID)
	assert.Zero(t, h.agent.bridge.pendingCount())
	assert.ErrorContains(t, h.agent.SendRawInput(controlAnswer(t, requestID, agent.ControlBehaviorAllow, "")), "no longer waits",
		"SendRawInput refuses a late answer to a withdrawn banner")
}

// Every event that ends the turn answers the helper, so Amp never blocks on a
// helper whose turn or agent is gone.
func TestPermissionPendingHelperEndsWithTheTurn(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		end    func(h *harness, fp *fakeProc)
		reason string
	}{
		{
			name:   "agent stops",
			end:    func(h *harness, _ *fakeProc) { h.agent.Stop() },
			reason: errAgentStopped.Error(),
		},
		{
			name: "user interrupts",
			end: func(h *harness, _ *fakeProc) {
				require.NoError(h.t, h.agent.Interrupt())
			},
			reason: errTurnInterrupted.Error(),
		},
		{
			name: "process exits",
			end: func(_ *harness, fp *fakeProc) {
				fp.exit()
			},
			reason: errProcessExited.Error(),
		},
		{
			name: "turn ends",
			end: func(h *harness, fp *fakeProc) {
				h.feed(fp, textLine("gave up", stopReasonEndTurn))
			},
			reason: errTurnEnded.Error(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			fp := h.startToolTurn()
			run := h.runHelper("shell_command", shellInput)
			h.awaitPublished(1)
			requestID := h.sink.LastPublishedControl().RequestID

			tc.end(h, fp)
			assert.Equal(t, helperExitReject, run.exitCode(t))
			assert.Equal(t, "LeapMux withdrew the permission request: "+tc.reason+"\n", run.stderr.String())
			h.awaitCanceled(requestID)
			assert.Zero(t, h.agent.bridge.pendingCount())
		})
	}
}

// A helper that Amp starts after the agent stopped finds no bridge and refuses
// at once: the stop removed the agent's directory, and with it the socket.
func TestPermissionHelperAfterStopRefusesAtOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	config := h.helperConfig()
	h.agent.Stop()
	run := runHelperWith(t, config, map[string]string{envToolName: "shell_command"}, strings.NewReader(shellInput))
	assert.Equal(t, helperExitReject, run.exitCode(t))
	assert.Contains(t, run.stderr.String(), "LeapMux ended the permission request before an answer came")
}

// A turn that ends while a banner is on its way leaves no banner behind. The
// refusal finds no banner to withdraw, so the request withdraws its banner once
// the banner exists: the withdrawal must FOLLOW the publication.
func TestPermissionRefusedBeforeItsBannerWithdrawsTheBanner(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.startToolTurn()
	h.events.runBeforeNextPublish(func() { h.agent.bridge.cancelAll(errTurnEnded) })

	run := h.runHelper("shell_command", shellInput)
	assert.Equal(t, helperExitReject, run.exitCode(t))
	assert.Contains(t, run.stderr.String(), "LeapMux withdrew the permission request: the turn ended")

	published := h.sink.PublishedControls()
	require.Len(t, published, 1)
	requestID := published[0].RequestID
	assert.Equal(t, []string{"publish " + requestID, "cancel " + requestID}, h.events.log())
	assert.Zero(t, h.agent.bridge.pendingCount())
}

// When the turn ends after the banner exists, the refusal alone withdraws the
// banner, exactly once.
func TestPermissionRefusedAfterItsBannerWithdrawsTheBannerOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.startToolTurn()

	run := h.runHelper("shell_command", shellInput)
	h.awaitPublished(1)
	requestID := h.sink.LastPublishedControl().RequestID
	h.agent.bridge.cancelAll(errTurnEnded)
	assert.Equal(t, helperExitReject, run.exitCode(t))
	assert.Equal(t, []string{"publish " + requestID, "cancel " + requestID}, h.events.log())
}

func TestPermissionPublicationFailureRefusesTheCall(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.sink.PublicationError = errors.New("the hub is gone")
	h.startToolTurn()
	run := h.runHelper("shell_command", shellInput)
	assert.Equal(t, helperExitReject, run.exitCode(t))
	assert.Contains(t, run.stderr.String(), "could not show the permission request: the hub is gone")
	assert.Zero(t, h.agent.bridge.pendingCount())
}

func TestPermissionAnswerForAnUnknownRequestFails(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	err := h.agent.SendRawInput(controlAnswer(t, "amp-permission-00000000-9", agent.ControlBehaviorAllow, ""))
	assert.ErrorContains(t, err, "no longer waits")
}

// Amp's stdin takes user lines alone, and Amp ends the whole session at any
// other line ("Invalid message format on stdin line N"). So SendRawInput
// refuses an answer that it cannot read and every line that is not a user
// line. Nothing reaches Amp, and the permission request still waits for a real
// answer.
func TestRawInputRefusesALineThatAmpDoesNotTake(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.startToolTurn()
	run := h.runHelper("shell_command", shellInput)
	h.awaitPublished(1)
	requestID := h.sink.LastPublishedControl().RequestID

	for name, line := range map[string][]byte{
		"an answer with a behavior that LeapMux does not know": controlAnswer(t, requestID, "maybe", ""),
		"an answer with no behavior":                           controlAnswer(t, requestID, "", ""),
		"an answer with no request id":                         controlAnswer(t, "", agent.ControlBehaviorAllow, ""),
		"a line that is not JSON":                              []byte("not json"),
		"a JSON value that is not an object":                   []byte(`["user"]`),
		"a line of another provider":                           []byte(`{"type":"control_request","request":{"subtype":"interrupt"}}`),
		"a line with no type":                                  []byte(`{"message":{"role":"user","content":[]}}`),
	} {
		assert.Errorf(t, h.agent.SendRawInput(line), "SendRawInput refuses %s", name)
	}
	assert.Len(t, fp.stdin.lines(), 1, "only the prompt reached Amp")
	run.assertStillWaits(t)
	assert.Equal(t, 1, h.agent.bridge.pendingCount(), "the request still waits for a real answer")

	require.NoError(t, h.agent.SendRawInput(controlAnswer(t, requestID, agent.ControlBehaviorAllow, "")))
	assert.Equal(t, helperExitAllow, run.exitCode(t))
}

// An answer that states a behavior LeapMux does not know gives that behavior
// and the request as the reason. It does not report a stale request or a
// missing process.
func TestRawInputStatesTheBehaviorOfAnAnswerItCannotRead(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	err := h.agent.SendRawInput(controlAnswer(t, "amp-permission-00000000-1", "maybe", ""))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"maybe"`)
	assert.Contains(t, err.Error(), "amp-permission-00000000-1")
}

// A connection with the wrong secret learns nothing and publishes nothing.
func TestPermissionBridgeRefusesAWrongSecret(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.startToolTurn()

	config, err := json.Marshal(helperConfig{Endpoint: h.agent.bridge.endpoint(), Secret: "not-the-secret"})
	require.NoError(t, err)
	run := runHelperWith(t, config, map[string]string{envToolName: "shell_command"}, strings.NewReader(shellInput))
	assert.Equal(t, helperExitReject, run.exitCode(t))
	assert.Contains(t, run.stderr.String(), "the agent closed the connection")
	assert.Zero(t, h.sink.PublishedControlCount())
}

func TestPermissionBridgeClosesAConnectionThatSendsGarbage(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	conn := dialBridge(t, h.agent.bridge)
	_, err := conn.Write([]byte("not json\n"))
	require.NoError(t, err)
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	_, err = bufio.NewReader(conn).ReadByte()
	assert.ErrorIs(t, err, io.EOF, "the bridge closes the connection with no answer")
	assert.Zero(t, h.sink.PublishedControlCount())
}

func TestPermissionHelperRefusesUnusableInput(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	config := h.helperConfig()
	cases := []struct {
		name   string
		config json.RawMessage
		env    map[string]string
		stdin  io.Reader
		want   string
	}{
		{
			name:   "unreadable config",
			config: json.RawMessage(`[]`),
			env:    map[string]string{envToolName: "shell_command"},
			stdin:  strings.NewReader(shellInput),
			want:   "could not read the configuration",
		},
		{
			name:   "config with no secret",
			config: json.RawMessage(`{"endpoint":"/nowhere/bridge.sock"}`),
			env:    map[string]string{envToolName: "shell_command"},
			stdin:  strings.NewReader(shellInput),
			want:   "could not read the configuration",
		},
		{
			name:   "no tool name",
			config: config,
			env:    map[string]string{},
			stdin:  strings.NewReader(shellInput),
			want:   envToolName,
		},
		{
			name:   "input that is not JSON",
			config: config,
			env:    map[string]string{envToolName: "shell_command"},
			stdin:  strings.NewReader(`{"command":`),
			want:   "is not valid JSON",
		},
		{
			name:   "input that is too large",
			config: config,
			env:    map[string]string{envToolName: "shell_command"},
			stdin:  io.LimitReader(repeatReader('a'), maxHelperMessageBytes+10),
			want:   "too large",
		},
		{
			name:   "input that fails to read",
			config: config,
			env:    map[string]string{envToolName: "shell_command"},
			stdin:  failingReader{},
			want:   "could not read the input of the shell_command call",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			run := runHelperWith(t, tc.config, tc.env, tc.stdin)
			assert.Equal(t, helperExitReject, run.exitCode(t))
			assert.Contains(t, run.stderr.String(), tc.want)
		})
	}
	assert.Zero(t, h.sink.PublishedControlCount(), "no unusable request reaches the user")
}

// An empty stdin is a call with no input, which the banner shows as `{}`.
func TestPermissionHelperTreatsEmptyInputAsAnEmptyObject(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	trap := h.clock.Trap().NewTimer("amp", "permission-match")
	defer trap.Close()
	h.startToolTurn()
	run := h.runHelper("mcp__server__ping", "  \n")
	call := trap.MustWait(t.Context())
	call.MustRelease(t.Context())
	h.clock.Advance(toolCallWait).MustWait(t.Context())
	h.awaitPublished(1)
	assert.JSONEq(t, `{}`, string(permissionPayload(t, h.sink.LastPublishedControl().Payload).Input))
	require.NoError(t, h.agent.SendRawInput(controlAnswer(t, h.sink.LastPublishedControl().RequestID, agent.ControlBehaviorAllow, "")))
	assert.Equal(t, helperExitAllow, run.exitCode(t))
}

// fakeBridge listens where a bridge would, in a private directory, and answers
// the first connection with answer. It hands back the secret line and the
// request that the helper sent.
type fakeBridge struct {
	path     string
	received chan fakeBridgeRequest
}

type fakeBridgeRequest struct {
	secret  string
	request helperRequest
}

func newFakeBridge(t *testing.T, answer string) *fakeBridge {
	t.Helper()
	path := filepath.Join(shortTempDir(t), bridgeSocketName)
	listener, err := net.Listen(bridgeNetwork, path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	fake := &fakeBridge{path: path, received: make(chan fakeBridgeRequest, 1)}
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		reader := bufio.NewReader(conn)
		secret, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		request, err := readHelperLine[helperRequest](reader)
		if err != nil {
			return
		}
		fake.received <- fakeBridgeRequest{secret: strings.TrimSuffix(secret, "\n"), request: request}
		_, _ = conn.Write([]byte(answer + "\n"))
	}()
	return fake
}

// The helper refuses a decision it does not know rather than allow the call.
func TestPermissionHelperRefusesAnUnknownDecision(t *testing.T) {
	t.Parallel()
	fake := newFakeBridge(t, `{"decision":"maybe"}`)
	config, err := json.Marshal(helperConfig{Endpoint: fake.path, Secret: "s"})
	require.NoError(t, err)
	run := runHelperWith(t, config, map[string]string{envToolName: "shell_command"}, strings.NewReader(shellInput))
	assert.Equal(t, helperExitReject, run.exitCode(t))
	assert.Contains(t, run.stderr.String(), `"maybe"`)
}

// The helper sends the secret on a line of its own, then the request, which
// carries what Amp gave it.
func TestPermissionHelperSendsTheCallAndTheThread(t *testing.T) {
	t.Parallel()
	fake := newFakeBridge(t, `{"decision":"allow"}`)
	config, err := json.Marshal(helperConfig{Endpoint: fake.path, Secret: "the-secret"})
	require.NoError(t, err)
	run := runHelperWith(t, config, map[string]string{envToolName: "edit_file", envThreadID: "T-42"}, strings.NewReader(" {\"path\":\"a\"}\n"))
	assert.Equal(t, helperExitAllow, run.exitCode(t))
	received := <-fake.received
	assert.Equal(t, "the-secret", received.secret)
	assert.Equal(t, "edit_file", received.request.Tool)
	assert.Equal(t, "T-42", received.request.Thread)
	assert.JSONEq(t, `{"path":"a"}`, string(received.request.Input))
}

func TestPermissionDecisionAfterStopRefuses(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.agent.Stop()
	decision := h.agent.decidePermission(context.Background(), helperRequest{Tool: "shell_command", Input: json.RawMessage(shellInput)})
	assert.Equal(t, decisionReject, decision.Decision)
	assert.Zero(t, h.sink.PublishedControlCount())
}

func TestPermissionBridgeRequestIDsDifferAcrossBridges(t *testing.T) {
	t.Parallel()
	first := newTestBridge(t)
	second := newTestBridge(t)
	assert.NotEqual(t, first.newRequestID(), second.newRequestID(),
		"a stored request of an earlier agent never matches a new one")
	assert.NotEqual(t, first.newRequestID(), first.newRequestID())
}

func TestPermissionBridgeCloseIsIdempotentAndRefusesNewRequests(t *testing.T) {
	t.Parallel()
	bridge := newTestBridge(t)
	bridge.serve(func(context.Context, helperRequest) helperDecision { return allowDecision() }, nil)
	pending, ok := bridge.register("r-1")
	require.True(t, ok)

	bridge.close(errAgentStopped)
	bridge.close(errAgentStopped)
	select {
	case decision := <-pending.answer:
		assert.Equal(t, decisionReject, decision.Decision)
	default:
		t.Fatal("close answered no waiting request")
	}
	_, ok = bridge.register("r-2")
	assert.False(t, ok)
	_, err := bridge.listener.Accept()
	assert.ErrorIs(t, err, net.ErrClosed, "the listener is closed")
}

func TestReadHelperLineRefusesAnOversizedLine(t *testing.T) {
	t.Parallel()
	reader := bufio.NewReader(io.LimitReader(repeatReader('a'), maxHelperMessageBytes+1))
	_, err := readHelperLine[helperDecision](reader)
	assert.ErrorContains(t, err, "exceeds")
}

// repeatReader yields one byte forever.
type repeatReader byte

func (r repeatReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(r)
	}
	return len(p), nil
}

// failingReader fails every read.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("broken pipe") }

// dialBridge connects to the bridge the way a helper does.
func dialBridge(t *testing.T, b *permissionBridge) net.Conn {
	t.Helper()
	conn, err := net.Dial(bridgeNetwork, b.endpoint())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// trackedConns reports how many connections the bridge holds.
func trackedConns(b *permissionBridge) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.conns)
}

// awaitEOF fails the test unless the peer closes conn. The deadline is a
// deadlock guard: a bridge that closes the connection answers at once.
func awaitEOF(t *testing.T, conn net.Conn) {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(30*time.Second)))
	_, err := io.Copy(io.Discard, conn)
	require.NoError(t, err, "the bridge closes the connection, and the read ends with EOF")
}

// longSecretTimeout outlasts every deadline of a test. A test that expects the
// bridge to end a connection at once sets it, so the secret deadline cannot end
// the connection in its place.
const longSecretTimeout = time.Hour

// A stranger that sends a long line with no secret costs the bridge a few
// hundred bytes, not the size of the whole line.
func TestPermissionBridgeReadsLittleBeforeTheSecret(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	conn := dialBridge(t, h.agent.bridge)
	written := make(chan error, 1)
	go func() {
		_, err := conn.Write(bytes.Repeat([]byte("x"), 1<<20))
		written <- err
	}()
	awaitEOF(t, conn)
	assert.Error(t, <-written, "the bridge closed the connection before it read the whole line")
	assert.Zero(t, h.sink.PublishedControlCount())
}

// close ends a connection that never stated the secret at once. Such a
// connection needs no refusal, and it must not delay the agent's stop.
func TestPermissionBridgeCloseEndsAnUnauthenticatedConnectionAtOnce(t *testing.T) {
	t.Parallel()
	bridge := newTestBridge(t)
	bridge.secretTimeout = longSecretTimeout
	bridge.serve(func(context.Context, helperRequest) helperDecision { return allowDecision() }, nil)
	conn := dialBridge(t, bridge)
	require.Eventually(t, func() bool { return trackedConns(bridge) == 1 }, 30*time.Second, time.Millisecond)

	closed := make(chan struct{})
	go func() {
		bridge.close(errAgentStopped)
		close(closed)
	}()
	awaitEOF(t, conn)
	select {
	case <-closed:
	case <-time.After(30 * time.Second):
		t.Fatal("close waited for a connection that never stated the secret")
	}
}

// The secret line holds at most maxSecretLineBytes before its newline. The
// reader takes nothing past the newline, so the request that follows stays for
// the request reader.
func TestReadSecretLine(t *testing.T) {
	t.Parallel()
	reader := strings.NewReader("the-secret\n{\"tool\":\"x\"}\n")
	secret, err := readSecretLine(reader)
	require.NoError(t, err)
	assert.Equal(t, "the-secret", string(secret))
	rest, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, "{\"tool\":\"x\"}\n", string(rest), "the request line stays unread")

	secret, err = readSecretLine(strings.NewReader("\n"))
	require.NoError(t, err)
	assert.Empty(t, secret, "an empty line is an empty secret, which the compare refuses")

	atLimit := strings.Repeat("s", maxSecretLineBytes)
	secret, err = readSecretLine(strings.NewReader(atLimit + "\n"))
	require.NoError(t, err)
	assert.Len(t, secret, maxSecretLineBytes)

	_, err = readSecretLine(strings.NewReader(atLimit + "s\n"))
	assert.ErrorContains(t, err, "exceeds")

	_, err = readSecretLine(strings.NewReader("no newline"))
	assert.ErrorIs(t, err, io.EOF, "a connection that ends before the newline states no secret")
	_, err = readSecretLine(strings.NewReader(""))
	assert.ErrorIs(t, err, io.EOF)
}

// The bridge counts the connections that did not state the secret yet. A
// connection that states it leaves the count at once, so a helper never holds a
// place under the cap, and no connection leaves the count twice.
func TestPermissionBridgeCountsTheConnectionsWithNoSecret(t *testing.T) {
	t.Parallel()
	bridge := newTestBridge(t)
	unauthenticated := func() int {
		bridge.mu.Lock()
		defer bridge.mu.Unlock()
		return bridge.unauthenticated
	}
	pipe := func() net.Conn {
		client, server := net.Pipe()
		t.Cleanup(func() {
			_ = client.Close()
			_ = server.Close()
		})
		return server
	}

	helper := pipe()
	require.Equal(t, connTracked, bridge.track(helper))
	assert.Equal(t, 1, unauthenticated())
	bridge.authenticate(helper)
	assert.Equal(t, 0, unauthenticated(), "a connection that stated the secret leaves the count")
	bridge.authenticate(helper)
	assert.Equal(t, 0, unauthenticated(), "a second authentication changes nothing")
	bridge.untrack(helper)
	assert.Equal(t, 0, unauthenticated(), "an authenticated connection that ends leaves the count alone")
	assert.Zero(t, trackedConns(bridge))

	strangers := make([]net.Conn, 0, maxUnauthenticatedConns)
	for range maxUnauthenticatedConns {
		conn := pipe()
		require.Equal(t, connTracked, bridge.track(conn))
		strangers = append(strangers, conn)
	}
	assert.Equal(t, connOverCap, bridge.track(pipe()), "the bridge refuses a connection past the cap")
	bridge.authenticate(strangers[0])
	assert.Equal(t, connTracked, bridge.track(pipe()), "a connection that stated the secret frees its place")
	bridge.untrack(strangers[1])
	assert.Equal(t, maxUnauthenticatedConns-1, unauthenticated(), "a connection with no secret that ends frees its place")

	bridge.untrack(pipe())
	assert.Equal(t, maxUnauthenticatedConns-1, unauthenticated(), "an unknown connection changes nothing")

	bridge.close(errAgentStopped)
	assert.Equal(t, connAfterClose, bridge.track(pipe()), "a closed bridge tracks nothing")
}

// A helper that goes away while its request waits for the call's line
// publishes no banner: no answer could reach it.
func TestPermissionHelperThatGoesAwayBeforeItsCallAppearsPublishesNothing(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	trap := h.clock.Trap().NewTimer("amp", "permission-match")
	defer trap.Close()
	h.send("go")

	run := h.runHelper("shell_command", shellInput)
	ctx := testutil.DeadlineContext(t)
	trap.MustWait(ctx).MustRelease(ctx)
	run.cancel()
	assert.Equal(t, helperExitReject, run.exitCode(t))
	// The bridge's handler returns only after the decision, so no tracked
	// connection means that the decision is final.
	require.Eventually(t, func() bool { return trackedConns(h.agent.bridge) == 0 },
		30*time.Second, 2*time.Millisecond, "the bridge handled the request")
	assert.Zero(t, h.sink.PublishedControlCount())
	assert.Zero(t, h.agent.bridge.pendingCount())
}

// The bridge holds at most maxUnauthenticatedConns connections that did not
// state the secret yet, and closes each one past that at once.
func TestPermissionBridgeCapsUnauthenticatedConnections(t *testing.T) {
	t.Parallel()
	bridge := newTestBridge(t)
	bridge.secretTimeout = longSecretTimeout
	bridge.serve(func(context.Context, helperRequest) helperDecision { return allowDecision() }, nil)
	t.Cleanup(func() { bridge.close(errAgentStopped) })
	for range maxUnauthenticatedConns {
		dialBridge(t, bridge)
	}
	require.Eventually(t, func() bool { return trackedConns(bridge) == maxUnauthenticatedConns }, 30*time.Second, time.Millisecond)
	awaitEOF(t, dialBridge(t, bridge))
	assert.Equal(t, maxUnauthenticatedConns, trackedConns(bridge))
}
