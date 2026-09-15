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
func newRegistryBase() (*jsonrpcBase, *bytes.Buffer) {
	var stdin bytes.Buffer
	return &jsonrpcBase{processBase: processBase{agentID: "agent", stdin: nopWriteCloser{&stdin}}}, &stdin
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
	base.withdrawControlRequest(sink, "jsonrpc:7", false)
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
