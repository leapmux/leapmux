package codex

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newCodexControlAgent returns an agent whose stdin a test reads, on the main thread.
func newCodexControlAgent(t *testing.T) (*Agent, *agenttest.ControlSink, *agenttest.Stdin) {
	t.Helper()
	sink := &agenttest.ControlSink{}
	stdin := &agenttest.Stdin{}
	a := &Agent{
		JSONRPCProcess: providerkit.JSONRPCProcess{Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID: "agent", Stdin: agenttest.NopStdin(stdin),
		})},
		sink:              agent.NewProviderServices(sink),
		threadID:          "main-thread",
		permissionMode:    DefaultPermissionMode,
		permissionProfile: contracts.CodexPermissionProfileWorkspace,
	}
	return a, sink, stdin
}

// setBypass puts the agent on the bypass preset, acknowledged or not.
func setBypass(a *Agent, acknowledged bool) {
	a.Mu.Lock()
	a.permissionMode = contracts.CodexPermissionModeNever
	a.permissionProfile = contracts.CodexPermissionProfileDangerFullAccess
	a.permissionsAcknowledged = acknowledged
	a.Mu.Unlock()
}

// stdinResponse returns the result of the JSON-RPC response with id that the agent
// wrote, once the queued writer delivers it.
func stdinResponse(t *testing.T, stdin *agenttest.Stdin, id string) json.RawMessage {
	t.Helper()
	var result json.RawMessage
	require.Eventually(t, func() bool {
		for _, line := range strings.Split(stdin.String(), "\n") {
			var frame struct {
				ID     json.RawMessage `json:"id"`
				Result json.RawMessage `json:"result"`
				Method *string         `json:"method"`
			}
			if json.Unmarshal([]byte(line), &frame) == nil && frame.Method == nil && string(frame.ID) == id {
				result = frame.Result
				return true
			}
		}
		return false
	}, 5*time.Second, time.Millisecond, "the agent never answered request %s", id)
	return result
}

// stdinHasResponse reports whether the agent wrote any response with id, after
// letting the queued writer drain a frame that a test sends after the response.
func stdinHasResponse(t *testing.T, a *Agent, stdin *agenttest.Stdin, id string) bool {
	t.Helper()
	// A detached marker frame queues behind every earlier write, so once it lands the
	// writer has delivered whatever came before it.
	a.SendResponseDetached(json.RawMessage(`"marker"`), map[string]any{}, "test marker")
	stdinResponse(t, stdin, `"marker"`)
	for _, line := range strings.Split(stdin.String(), "\n") {
		var frame struct {
			ID     json.RawMessage `json:"id"`
			Method *string         `json:"method"`
		}
		if json.Unmarshal([]byte(line), &frame) == nil && frame.Method == nil && string(frame.ID) == id {
			return true
		}
	}
	return false
}

const codexCommandApprovalLine = `{"jsonrpc":"2.0","id":7,"method":"item/commandExecution/requestApproval","params":{"threadId":"main-thread","turnId":"turn-1","itemId":"item-1","command":"make clean","availableDecisions":["accept","acceptForSession","decline","cancel"]}}`

// TestCodexApprovalAnswerConformance replays testdata/codex_approval_answer_conformance.json
// against the standing approval. The browser replays the same file against its Allow.
func TestCodexApprovalAnswerConformance(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile(testutil.RepoPath(t, "testdata", "codex_approval_answer_conformance.json"))
	require.NoError(t, err)
	var fixture struct {
		Cases []struct {
			Name           string          `json:"name"`
			Request        json.RawMessage `json:"request"`
			StandingResult json.RawMessage `json:"standingResult"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.NotEmpty(t, fixture.Cases)
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			answer, ok := codexStandingApprovalAnswer(tc.Request)
			if string(tc.StandingResult) == "null" {
				assert.False(t, ok, "only the user can answer this request")
				return
			}
			require.True(t, ok)
			encoded, err := json.Marshal(answer)
			require.NoError(t, err)
			assert.JSONEq(t, string(tc.StandingResult), string(encoded))
		})
	}
}

// TestCodexGuardianEventConversion replays testdata/codex_guardian_event_conversion.json,
// whose every event the codex app-server of its codexVersion accepted.
func TestCodexGuardianEventConversion(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile(testutil.RepoPath(t, "testdata", "codex_guardian_event_conversion.json"))
	require.NoError(t, err)
	var fixture struct {
		Cases []struct {
			Name         string          `json:"name"`
			Notification json.RawMessage `json:"notification"`
			Event        json.RawMessage `json:"event"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.NotEmpty(t, fixture.Cases)
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			var review codexAutoReviewCompleted
			require.NoError(t, json.Unmarshal(tc.Notification, &review))
			event, err := codexGuardianAssessmentEvent(review)
			require.NoError(t, err)
			encoded, err := json.Marshal(event)
			require.NoError(t, err)
			assert.JSONEq(t, string(tc.Event), string(encoded))
		})
	}
}

// TestCodexGuardianEventConversionRefusesUnknownWords covers a word that a newer Codex
// can add: the conversion states the gap instead of sending a value Codex cannot parse.
func TestCodexGuardianEventConversionRefusesUnknownWords(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		status string
		action string
		want   string
	}{
		{name: "an unknown status", status: "pending", action: `{"type":"command","source":"shell","command":"ls","cwd":"/work"}`, want: "status"},
		{name: "an unknown action", status: "denied", action: `{"type":"teleport"}`, want: "action type"},
		{name: "an unknown command source", status: "denied", action: `{"type":"command","source":"ssh","command":"ls","cwd":"/work"}`, want: "command source"},
		{name: "an unknown network protocol", status: "denied", action: `{"type":"networkAccess","target":"x","host":"x","protocol":"quic","port":1}`, want: "network protocol"},
		{name: "an action that is not an object", status: "denied", action: `"command"`, want: "action is invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			review := codexAutoReviewCompleted{ReviewID: "review-1", Action: json.RawMessage(test.action)}
			review.Review.Status = test.status
			_, err := codexGuardianAssessmentEvent(review)
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.want)
		})
	}
}

func TestCodexFileURI(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "file:///work", codexFileURI("/work"))
	assert.Equal(t, "file:///my%20dir/sub", codexFileURI("/my dir/sub"), "a space is escaped")
	assert.Equal(t, "file:///C:/repo", codexFileURI("C:/repo"), "a drive path gains the leading slash")
	assert.Equal(t, "file:///already", codexFileURI("file:///already"), "a file URI passes through")
	assert.Equal(t, "file://server/share/my%20dir", codexFileURIForOS(`\\server\share\my dir`, "windows"))
	assert.Equal(t, "file:///C:/repo", codexFileURIForOS(`C:\repo`, "windows"))
	assert.Equal(t, "file:////server/share/repo", codexFileURIForOS("//server/share/repo", "linux"))
	assert.Equal(t, "file:///a%5Cb", codexFileURIForOS(`/a\b`, "linux"))
}

func TestCodexStandingApprovalAnswersAPermissionRequestUnpublished(t *testing.T) {
	t.Parallel()

	a, sink, stdin := newCodexControlAgent(t)
	setBypass(a, true)

	handleCodexOutput(a, providerkit.ParseLine([]byte(codexCommandApprovalLine)))

	assert.JSONEq(t, `{"decision":"accept"}`, string(stdinResponse(t, stdin, "7")))
	assert.Zero(t, sink.PublishedControlCount(), "native full access asks nothing, so no card shows")
	assert.Empty(t, sink.Notifications(), "an answer without a card needs no notice")
	require.Eventually(t, func() bool { return !a.OutstandingControlForTest("jsonrpc:7") }, 30*time.Second, time.Millisecond)
}

// TestCodexStandingApprovalWaitsForAcknowledgement covers the window between a switch
// to Bypass and Codex's acceptance of it: Codex can still refuse the values, so the
// worker approves nothing in their name until then.
func TestCodexStandingApprovalWaitsForAcknowledgement(t *testing.T) {
	t.Parallel()

	a, sink, stdin := newCodexControlAgent(t)
	setBypass(a, false)

	handleCodexOutput(a, providerkit.ParseLine([]byte(codexCommandApprovalLine)))

	require.Equal(t, 1, sink.PublishedControlCount(), "the request reaches the reader")
	assert.False(t, stdinHasResponse(t, a, stdin, "7"))
	assert.True(t, a.OutstandingControlForTest("jsonrpc:7"))
}

func TestCodexStandingApprovalLeavesTheUserTheirOwnRequests(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		line string
	}{
		{name: "a question", line: `{"jsonrpc":"2.0","id":7,"method":"item/tool/requestUserInput","params":{"threadId":"main-thread","questions":[{"id":"q1","question":"Which?"}]}}`},
		{name: "an approval that only writes a lasting rule", line: `{"jsonrpc":"2.0","id":7,"method":"item/commandExecution/requestApproval","params":{"availableDecisions":[{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["git"]}},"cancel"]}}`},
		{name: "a form that asks for data", line: `{"jsonrpc":"2.0","id":7,"method":"mcpServer/elicitation/request","params":{"requestedSchema":{"type":"object","properties":{"branch":{"type":"string"}}},"_meta":{"codex_approval_kind":"mcp_tool_call"}}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			a, sink, stdin := newCodexControlAgent(t)
			setBypass(a, true)

			handleCodexOutput(a, providerkit.ParseLine([]byte(test.line)))

			require.Equal(t, 1, sink.PublishedControlCount())
			assert.False(t, stdinHasResponse(t, a, stdin, "7"))
			a.approveOutstandingForBypass()
			assert.False(t, stdinHasResponse(t, a, stdin, "7"), "the sweep leaves it for the user too")
			assert.Empty(t, sink.Notifications())
		})
	}
}

// TestCodexBypassApprovesPendingRequests covers the user's case: a turn that started
// before the switch keeps asking, and the switch must answer the requests that are
// already on screen.
func TestCodexBypassApprovesPendingRequests(t *testing.T) {
	t.Parallel()

	a, sink, stdin := newCodexControlAgent(t)
	handleCodexOutput(a, providerkit.ParseLine([]byte(codexCommandApprovalLine)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":8,"method":"item/permissions/requestApproval","params":{"permissions":{"network":{"enabled":true}}}}`)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":9,"method":"item/tool/requestUserInput","params":{"questions":[{"id":"q1","question":"Which?"}]}}`)))
	require.Equal(t, 3, sink.PublishedControlCount())

	setBypass(a, true)
	a.approveOutstandingForBypass()

	assert.JSONEq(t, `{"decision":"accept"}`, string(stdinResponse(t, stdin, "7")))
	assert.JSONEq(t, `{"permissions":{"network":{"enabled":true}},"scope":"turn"}`, string(stdinResponse(t, stdin, "8")))
	require.Eventually(t, func() bool { return len(sink.Notifications()) == 1 }, 30*time.Second, time.Millisecond)
	assert.ElementsMatch(t, []string{"jsonrpc:7", "jsonrpc:8"}, sink.CanceledControls())
	assert.True(t, a.OutstandingControlForTest("jsonrpc:9"), "a question still waits for the user")
	require.Len(t, sink.Notifications(), 1)
	assert.Equal(t, contracts.NotificationTypePendingRequestsApproved, sink.Notifications()[0][contracts.NotificationFieldType])
	assert.Equal(t, 2, sink.Notifications()[0][contracts.NotificationFieldCount])

	// A second sweep finds nothing and writes no second notice.
	a.approveOutstandingForBypass()
	assert.Len(t, sink.Notifications(), 1)
}

func TestCodexBypassSweepWaitsForAcknowledgement(t *testing.T) {
	t.Parallel()

	a, sink, stdin := newCodexControlAgent(t)
	handleCodexOutput(a, providerkit.ParseLine([]byte(codexCommandApprovalLine)))
	setBypass(a, false)

	a.approveOutstandingForBypass()

	assert.False(t, stdinHasResponse(t, a, stdin, "7"))
	assert.True(t, a.OutstandingControlForTest("jsonrpc:7"))
	assert.Empty(t, sink.Notifications())
}

// TestCodexSendRawInputRefusesAnAnswerToAClosedRequest covers a reader answer that
// arrives after a standing approval, a withdrawal or another answer closed the
// request: it must not reach Codex as a second answer.
func TestCodexSendRawInputRefusesAnAnswerToAClosedRequest(t *testing.T) {
	t.Parallel()

	a, _, stdin := newCodexControlAgent(t)
	handleCodexOutput(a, providerkit.ParseLine([]byte(codexCommandApprovalLine)))

	answer := []byte(`{"jsonrpc":"2.0","id":7,"result":{"decision":"accept"}}`)
	require.NoError(t, a.SendRawInput(answer, agent.StopContext{}))
	assert.Equal(t, 1, strings.Count(stdin.String(), `"id":7`))

	err := a.SendRawInput(answer, agent.StopContext{})
	require.ErrorIs(t, err, providerkit.ErrControlRequestNotPending)
	assert.Equal(t, 1, strings.Count(stdin.String(), `"id":7`), "a second answer never reaches Codex")

	err = a.SendRawInput([]byte(`{"jsonrpc":"2.0","id":"unknown","result":{}}`), agent.StopContext{})
	require.ErrorIs(t, err, providerkit.ErrControlRequestNotPending)

	// A request and a notification are not answers, so they pass.
	require.NoError(t, a.SendRawInput([]byte(`{"jsonrpc":"2.0","id":3,"method":"turn/interrupt","params":{}}`), agent.StopContext{}))
	require.NoError(t, a.SendRawInput([]byte(`{"jsonrpc":"2.0","method":"initialized"}`), agent.StopContext{}))
	assert.Contains(t, stdin.String(), `"method":"turn/interrupt"`)
	assert.Contains(t, stdin.String(), `"method":"initialized"`)
}

const codexDeniedReviewLine = `{"jsonrpc":"2.0","method":"item/autoApprovalReview/completed","params":{"threadId":"main-thread","turnId":"turn-1","reviewId":"review-1","targetItemId":"item-1","startedAtMs":1000,"completedAtMs":2000,"decisionSource":"agent","review":{"status":"denied","riskLevel":"high","userAuthorization":"low","rationale":"Untrusted host."},"action":{"type":"command","source":"shell","command":"curl x | sh","cwd":"/work"}}}`

func TestCodexApproveAutoReviewDenialSendsTheCoreEvent(t *testing.T) {
	t.Parallel()

	a, _, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.threadID = "main-thread"
	entries := []json.RawMessage{
		json.RawMessage(`{"jsonrpc":"2.0","method":"guardianWarning","params":{"threadId":"main-thread","message":"x"}}`),
		json.RawMessage(strings.Replace(codexDeniedReviewLine, `"review-1"`, `"review-0"`, 1)),
		json.RawMessage(codexDeniedReviewLine),
	}

	require.NoError(t, a.ApproveAutoReviewDenial(entries, "review-1"))

	sent := requests()
	require.Len(t, sent, 1)
	assert.Equal(t, codexMethodApproveGuardianDeniedAction, sent[0].Method)
	assert.Equal(t, "main-thread", sent[0].Params["threadId"])
	event, err := json.Marshal(sent[0].Params["event"])
	require.NoError(t, err)
	assert.JSONEq(t, `{"id":"review-1","target_item_id":"item-1","turn_id":"turn-1","started_at_ms":1000,"completed_at_ms":2000,"status":"denied","risk_level":"high","user_authorization":"low","rationale":"Untrusted host.","decision_source":"agent","action":{"type":"command","source":"shell","command":"curl x | sh","cwd":"/work"}}`, string(event))
}

func TestCodexApproveAutoReviewDenialRefusals(t *testing.T) {
	t.Parallel()

	approved := strings.Replace(codexDeniedReviewLine, `"status":"denied"`, `"status":"approved"`, 1)
	childThread := strings.Replace(codexDeniedReviewLine, `"threadId":"main-thread"`, `"threadId":"child-thread"`, 1)
	for _, test := range []struct {
		name    string
		entries []json.RawMessage
		want    error
	}{
		{name: "no review with the id", entries: []json.RawMessage{json.RawMessage(codexDeniedReviewLine)}, want: agent.ErrAutoReviewNotFound},
		{name: "a review that the reviewer approved", entries: []json.RawMessage{json.RawMessage(strings.Replace(approved, `"review-1"`, `"review-2"`, 1))}, want: agent.ErrAutoReviewNotDenied},
		{name: "a review from another thread", entries: []json.RawMessage{json.RawMessage(strings.Replace(childThread, `"review-1"`, `"review-2"`, 1))}, want: agent.ErrAutoReviewThreadRetired},
		{name: "an entry that is not JSON", entries: []json.RawMessage{json.RawMessage(`not json`)}, want: agent.ErrAutoReviewNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			a, _, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
				return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
			})
			a.threadID = "main-thread"
			err := a.ApproveAutoReviewDenial(test.entries, "review-2")
			require.ErrorIs(t, err, test.want)
			assert.Empty(t, requests(), "a refusal sends nothing to Codex")
		})
	}
}

func TestCodexApproveAutoReviewDenialStatesACodexRefusal(t *testing.T) {
	t.Parallel()

	a, _, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32600,"message":"direct input is not allowed"}`)}
	})
	a.threadID = "main-thread"
	err := a.ApproveAutoReviewDenial([]json.RawMessage{json.RawMessage(codexDeniedReviewLine)}, "review-1")
	require.Error(t, err)
	var refusal *providerkit.JSONRPCResponseError
	assert.True(t, errors.As(err, &refusal), "the Codex refusal stays readable: %v", err)
	assert.Contains(t, err.Error(), "direct input is not allowed")
}

// switchingControlSink runs onPublish before it stores a request, so a test can
// switch the agent's settings between the agent's first standing-approval check and
// the registration of the request.
type switchingControlSink struct {
	*agenttest.ControlSink
	onPublish func()
}

func (s *switchingControlSink) PublishControlRequest(request agent.ControlRequest) error {
	if s.onPublish != nil {
		s.onPublish()
	}
	return s.ControlSink.PublishControlRequest(request)
}

// TestCodexStandingApprovalRereadsAfterRegistration covers a switch to Bypass that
// lands between the agent's first check and the registration of the request. The
// switch's sweep ran first and found no record, so the second read after the
// registration must answer the request.
func TestCodexStandingApprovalRereadsAfterRegistration(t *testing.T) {
	t.Parallel()

	a, sink, stdin := newCodexControlAgent(t)
	answer, ok := codexStandingApprovalAnswer([]byte(codexCommandApprovalLine))
	require.True(t, ok)
	// The first check read the old settings; the switch and its empty sweep follow.
	setBypass(a, true)
	a.approveOutstandingForBypass()
	require.Empty(t, sink.Notifications(), "the sweep found no record")

	a.registerApprovalRequest([]byte(codexCommandApprovalLine), nil, answer)

	assert.JSONEq(t, `{"decision":"accept"}`, string(stdinResponse(t, stdin, "7")))
	require.Eventually(t, func() bool { return len(sink.CanceledControls()) > 0 }, 30*time.Second, time.Millisecond)
	assert.Equal(t, []string{"jsonrpc:7"}, sink.CanceledControls(), "the card that the publish created leaves again")
	assert.False(t, a.OutstandingControlForTest("jsonrpc:7"))
}

// TestCodexStandingApprovalDuringPublication covers a switch to Bypass that lands
// after the registration and before the card is stored. The switch's sweep answers
// the request and cancels a card that does not exist yet, and the publication then
// retires the card that it stored. Codex receives one answer.
func TestCodexStandingApprovalDuringPublication(t *testing.T) {
	t.Parallel()

	a, _, stdin := newCodexControlAgent(t)
	sink := &switchingControlSink{ControlSink: &agenttest.ControlSink{}}
	sink.onPublish = func() {
		setBypass(a, true)
		a.approveOutstandingForBypass()
	}
	a.sink = agent.NewProviderServices(sink)

	handleCodexOutput(a, providerkit.ParseLine([]byte(codexCommandApprovalLine)))

	assert.JSONEq(t, `{"decision":"accept"}`, string(stdinResponse(t, stdin, "7")))
	assert.False(t, stdinHasResponse(t, a, stdin, "8"))
	answers := 0
	for _, line := range strings.Split(stdin.String(), "\n") {
		if strings.Contains(line, `"id":7`) {
			answers++
		}
	}
	assert.Equal(t, 1, answers, "Codex receives one answer")
	require.Eventually(t, func() bool { return len(sink.CanceledControls()) > 0 }, 30*time.Second, time.Millisecond)
	assert.Contains(t, sink.CanceledControls(), "jsonrpc:7", "the stored card is retired")
	assert.False(t, a.OutstandingControlForTest("jsonrpc:7"))
}
