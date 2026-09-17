package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// registryCancelSink records every withdrawal the registry asks for.
type registryCancelSink struct {
	recordingControlSink
	cancelled []string
}

func (s *registryCancelSink) CancelControlRequest(id string) {
	s.cancelled = append(s.cancelled, id)
}

// newRegistryBase gives a jsonrpcBase whose stdin a test can read back.
func newRegistryBase() (*jsonrpcBase, *syncBuffer) {
	stdin := &syncBuffer{}
	return &jsonrpcBase{processBase: processBase{agentID: "agent", stdin: stdin}}, stdin
}

// drainStdin returns once every frame queued before it reached the fake stdin.
//
// One goroutine performs the writes in order, so a SYNCHRONOUS write that lands
// proves that each detached frame ahead of it landed too. The frame carries no
// JSON-RPC id, so it answers nothing and leaves the registry as it was.
func drainStdin(t *testing.T, base *jsonrpcBase) {
	t.Helper()
	require.NoError(t, base.writeStdin([]byte("{}\n")))
}

func TestControlRegistryAnswersEachKindWithItsOwnCancelAnswer(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		answer any
		want   string
	}{
		{"acp permission", acpPermissionCancelAnswer(), `{"outcome":{"outcome":"cancelled"}}`},
		{"mcp elicitation", mcpElicitationCancelAnswer(), `{"action":"cancel"}`},
		{"cursor plan", cursorPlanCancelAnswer(), `{"outcome":{"outcome":"rejected"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			base, stdin := newRegistryBase()
			sink := &registryCancelSink{}
			base.publishControlRequest(sink, []byte(`{"jsonrpc":"2.0","id":7,"method":"request"}`), tc.answer)
			require.Len(t, sink.PublishedControls(), 1)
			base.withdrawAllControlRequests(sink)
			assert.JSONEq(t, `{"jsonrpc":"2.0","id":7,"result":`+tc.want+`}`, stdin.String())
			assert.Equal(t, []string{"jsonrpc:7"}, sink.cancelled)
		})
	}
}

// A request the provider does not block on takes no answer, and its card still goes.
func TestControlRegistryWithoutACancelAnswerSendsNothing(t *testing.T) {
	t.Parallel()
	base, stdin := newRegistryBase()
	sink := &registryCancelSink{}
	base.publishControlRequest(sink, []byte(`{"jsonrpc":"2.0","id":"q-1","method":"cursor/ask_question"}`), nil)
	base.withdrawAllControlRequests(sink)
	assert.Empty(t, stdin.String())
	assert.Equal(t, []string{`jsonrpc:"q-1"`}, sink.cancelled)
}

// The agent withdrew the request itself, so it waits for no answer.
func TestControlRegistryWithdrawalWithoutAnAnswerOnlyDropsTheCard(t *testing.T) {
	t.Parallel()
	base, stdin := newRegistryBase()
	sink := &registryCancelSink{}
	base.publishControlRequest(sink, []byte(`{"jsonrpc":"2.0","id":7,"method":"session/request_permission"}`), acpPermissionCancelAnswer())
	base.withdrawControlRequest(sink, "jsonrpc:7")
	assert.Empty(t, stdin.String())
	assert.Equal(t, []string{"jsonrpc:7"}, sink.cancelled)
	// The record is gone, so a later stop cannot answer it a second time.
	base.withdrawAllControlRequests(sink)
	assert.Empty(t, stdin.String())
}

// A publication that storage refused leaves no record behind.
func TestControlRegistryDropsTheRecordWhenPublicationFails(t *testing.T) {
	t.Parallel()
	base, stdin := newRegistryBase()
	sink := &registryCancelSink{recordingControlSink: recordingControlSink{publicationError: errors.New("storage unavailable")}}
	base.publishControlRequest(sink, []byte(`{"jsonrpc":"2.0","id":7,"method":"session/request_permission"}`), acpPermissionCancelAnswer())
	// The refusal answers the provider DETACHED, so that frame lands on the writer's
	// own goroutine. Waiting for it is what makes the reset below take it: without
	// the wait the frame could arrive after the reset and fail the assertion, or
	// before it and pass one that proved nothing.
	drainStdin(t, base)
	stdin.Reset()
	base.withdrawAllControlRequests(sink)
	assert.Empty(t, stdin.String(), "a request that was never published must not receive a cancel answer")
	assert.Empty(t, sink.cancelled)
}

// The answered request leaves the registry only once the write lands, so a failed
// write keeps it available for another answer.
func TestControlRegistryForgetsAnAnsweredRequestAfterTheWrite(t *testing.T) {
	t.Parallel()
	base, stdin := newRegistryBase()
	sink := &registryCancelSink{}
	base.publishControlRequest(sink, []byte(`{"jsonrpc":"2.0","id":7,"method":"session/request_permission"}`), acpPermissionCancelAnswer())
	require.NoError(t, base.SendRawInput([]byte(`{"jsonrpc":"2.0","id":7,"result":{"outcome":{"optionId":"once"}}}`)))
	stdin.Reset()
	base.withdrawAllControlRequests(sink)
	assert.Empty(t, stdin.String(), "the answered request must not receive a second answer")
}

func TestControlRegistryKeepsARequestWhoseAnswerCouldNotBeWritten(t *testing.T) {
	t.Parallel()
	base := &jsonrpcBase{processBase: processBase{agentID: "agent", stdin: failingWriteCloser{}}}
	sink := &registryCancelSink{}
	base.publishControlRequest(sink, []byte(`{"jsonrpc":"2.0","id":7,"method":"session/request_permission"}`), acpPermissionCancelAnswer())
	require.Error(t, base.SendRawInput([]byte(`{"jsonrpc":"2.0","id":7,"result":{}}`)))
	base.outstandingMu.Lock()
	_, found := base.outstandingControls["jsonrpc:7"]
	base.outstandingMu.Unlock()
	assert.True(t, found, "a write that failed leaves the request waiting, so the reader can answer it again")
}

// newRegistryACPBase gives an acpBase whose stdin a test can read back.
func newRegistryACPBase(sink ProviderServices) (*acpBase, *bytes.Buffer) {
	var stdin bytes.Buffer
	b := &acpBase{sink: sink}
	b.agentID = "agent"
	b.stdin = nopWriteCloser{&stdin}
	b.sessionID = "session-1"
	return b, &stdin
}

// jsonrpcResultsByID reads every response frame one agent wrote, keyed by its raw id.
func jsonrpcResultsByID(t *testing.T, written string) map[string]string {
	t.Helper()
	results := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(written), "\n") {
		if line == "" {
			continue
		}
		var frame struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &frame), line)
		if frame.Method != "" {
			continue
		}
		results[string(frame.ID)] = string(frame.Result)
	}
	return results
}

// An ACP elicitation and a permission request each carry their own cancel answer, and
// one stop answers both.
func TestACPInterruptAnswersEveryOpenControlRequestByKind(t *testing.T) {
	t.Parallel()
	sink := &registryCancelSink{}
	b, stdin := newRegistryACPBase(sink)
	b.promptActive = true
	b.HandleOutput([]byte(`{"jsonrpc":"2.0","id":11,"method":"session/request_permission","params":{}}`))
	b.HandleOutput([]byte(`{"jsonrpc":"2.0","id":"e-1","method":"` + contracts.MCPElicitationMethodACP + `","params":{}}`))
	require.Len(t, sink.PublishedControls(), 2)
	require.NoError(t, b.Interrupt())
	answers := jsonrpcResultsByID(t, stdin.String())
	assert.JSONEq(t, `{"outcome":{"outcome":"cancelled"}}`, answers[`11`])
	assert.JSONEq(t, `{"action":"cancel"}`, answers[`"e-1"`])
	assert.ElementsMatch(t, []string{"jsonrpc:11", `jsonrpc:"e-1"`}, sink.cancelled)
}

// A cancel notification from the agent prunes the record. Without that the map grew by
// one entry for every withdrawn request, and the next stop answered a request nobody
// waited on.
func TestACPCancelNotificationPrunesTheOutstandingRecord(t *testing.T) {
	t.Parallel()
	sink := &registryCancelSink{}
	b, stdin := newRegistryACPBase(sink)
	b.promptActive = true
	b.HandleOutput([]byte(`{"jsonrpc":"2.0","id":11,"method":"session/request_permission","params":{}}`))
	b.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"` + acpMethodCancelRequestCamel + `","params":{"requestId":11}}`))
	assert.Equal(t, []string{"jsonrpc:11"}, sink.cancelled)
	b.outstandingMu.Lock()
	remaining := len(b.outstandingControls)
	b.outstandingMu.Unlock()
	assert.Zero(t, remaining)
	stdin.Reset()
	require.NoError(t, b.Interrupt())
	assert.Empty(t, jsonrpcResultsByID(t, stdin.String()), "a withdrawn request must not receive a cancel answer")
}

// A withdrawal must find the record the publisher wrote, whichever JSON spelling each
// frame used for the same number.
func TestACPCancelNotificationMatchesEveryNumericSpelling(t *testing.T) {
	t.Parallel()
	for _, spelling := range []string{`12`, `12.0`, `1.2e1`} {
		t.Run(spelling, func(t *testing.T) {
			t.Parallel()
			sink := &registryCancelSink{}
			b, _ := newRegistryACPBase(sink)
			b.HandleOutput([]byte(`{"jsonrpc":"2.0","id":12,"method":"session/request_permission","params":{}}`))
			b.HandleOutput([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","method":%q,"params":{"requestId":%s}}`, acpMethodCancelRequestCamel, spelling)))
			assert.Equal(t, []string{"jsonrpc:12"}, sink.cancelled)
		})
	}
}

// Codex is the one publisher that registers a REAL cancel answer: it retires its
// own approval requests, but it defines no outcome for an MCP elicitation the
// client withdraws, so the elicitation blocks inside the CLI until somebody
// answers it. Without a drain on Interrupt that answer could never be delivered
// and the state was dead.
func TestCodexInterruptAnswersAnOutstandingElicitation(t *testing.T) {
	t.Parallel()

	sink := &registryCancelSink{}
	stdin := &syncBuffer{}
	a := &CodexAgent{
		jsonrpcBase: jsonrpcBase{processBase: processBase{agentID: "agent", stdin: nopWriteCloser{stdin}}},
		sink:        sink,
	}
	a.publishControlRequest(a.sink, []byte(`{"jsonrpc":"2.0","id":7,"method":"`+contracts.MCPElicitationMethodCodex+`"}`), mcpElicitationCancelAnswer())

	// No thread or turn, so Interrupt has nothing to cancel and returns early --
	// the drain must still run, because a request the runtime waits on outlives
	// the turn that raised it.
	require.NoError(t, a.Interrupt())

	assert.Contains(t, stdin.String(), `{"action":"cancel"}`,
		"the elicitation's own cancel answer releases the blocked MCP call")
	assert.Equal(t, []string{"jsonrpc:7"}, sink.cancelled, "and the browser card goes with it")
}

// A withdrawal that lands between the registration and the publish cancelled a
// card that did not exist yet: CancelControlRequest found no row and returned in
// silence, and the publish then created the card it meant to retire. The result
// was a live card with no record behind it, which no later withdrawal could reach
// and the reader could never dismiss.
func TestControlRegistryRetiresACardAStopStoleDuringThePublish(t *testing.T) {
	t.Parallel()

	base, _ := newRegistryBase()
	sink := &stealingPublishSink{base: base}
	base.publishControlRequest(sink, []byte(`{"jsonrpc":"2.0","id":7,"method":"session/request_permission"}`), acpPermissionCancelAnswer())

	assert.Equal(t, []string{"jsonrpc:7"}, sink.cancelled,
		"the publish must retire a card whose record a stop already took")
}

// stealingPublishSink drains the registry from INSIDE PublishControlRequest, which
// is the window a stop lands in: the record is registered, and the card does not
// exist yet.
type stealingPublishSink struct {
	registryCancelSink
	base *jsonrpcBase
}

func (s *stealingPublishSink) PublishControlRequest(request ControlRequest) error {
	s.base.withdrawAllControlRequests(&s.registryCancelSink)
	s.cancelled = nil // The stop's own cancel found no row; only the publish's counts.
	return nil
}

// The OTHER half of the same window: the reader answers while the publish is still
// in flight.
//
// PublishControlRequest completes the browser broadcast before it returns, so the card
// is on screen and answerable inside the window. Every control response reaches the
// provider through SendRawInput, which removes the record -- so a bare "is the record
// still there?" test read an ANSWERED request as a stolen one and cancelled it. The
// provider had the answer, but the transcript row was deleted and a controlCancel was
// broadcast, so the card the reader had just allowed vanished as CANCELLED.
func TestControlRegistryKeepsACardTheReaderAnsweredDuringThePublish(t *testing.T) {
	t.Parallel()

	base, _ := newRegistryBase()
	sink := &answeringPublishSink{base: base}
	base.publishControlRequest(sink, []byte(`{"jsonrpc":"2.0","id":7,"method":"session/request_permission"}`), acpPermissionCancelAnswer())

	assert.Empty(t, sink.cancelled, "an answer inside the publish window must not cancel the card")
	base.outstandingMu.Lock()
	defer base.outstandingMu.Unlock()
	assert.NotContains(t, base.outstandingControls, "jsonrpc:7", "the answer still retires the record")
}

// answeringPublishSink ANSWERS the request from inside PublishControlRequest, which is
// the window a fast reader lands in: the card is broadcast, and the publisher has not
// yet re-taken outstandingMu.
type answeringPublishSink struct {
	registryCancelSink
	base *jsonrpcBase
}

func (s *answeringPublishSink) PublishControlRequest(ControlRequest) error {
	// The ordinary answer path: the frame carries the request's own id, and
	// SendRawInput forgets the record after it writes.
	if err := s.base.SendRawInput([]byte(`{"jsonrpc":"2.0","id":7,"result":{"outcome":"allow"}}`)); err != nil {
		return err
	}
	return nil
}
