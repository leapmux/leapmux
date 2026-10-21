package codex

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

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

// asyncQuestionCompletedLine builds the item/completed frame of an async agent
// message. questions is the JSON array of the questions the item carries.
func asyncQuestionCompletedLine(threadID, itemID, questions string) string {
	return `{"jsonrpc":"2.0","method":"item/completed","params":{"threadId":"` + threadID +
		`","turnId":"turn-1","item":{"type":"agentMessage","id":"` + itemID +
		`","text":"Which color?\n- Blue\n- Red","delivery":"async","questions":` + questions + `}}}`
}

// asyncQuestionRequestPayload builds the stored request payload of one async
// question, the shape that publishAsyncQuestions writes.
func asyncQuestionRequestPayload(t *testing.T, questionItemID, title string) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"type":       "control_request",
		"request_id": "codex-async-question-call_abc-0",
		"request": map[string]any{
			"tool_name": contracts.CodexControlToolAsyncQuestion,
			"input": contracts.CodexAsyncQuestionInput{
				QuestionItemID: questionItemID,
				Title:          title,
			},
		},
	})
	require.NoError(t, err)
	return payload
}

// decodePublishedAsyncQuestion reads one published async question request and
// returns its typed input and its raw input map. It also pins the envelope keys
// that the browser reads.
func decodePublishedAsyncQuestion(t *testing.T, record agenttest.ControlRequestRecord) (contracts.CodexAsyncQuestionInput, map[string]any) {
	t.Helper()
	var envelope struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		Request   struct {
			ToolName string                            `json:"tool_name"`
			Input    contracts.CodexAsyncQuestionInput `json:"input"`
		} `json:"request"`
	}
	require.NoError(t, json.Unmarshal(record.Payload, &envelope))
	var raw struct {
		Request struct {
			Input map[string]any `json:"input"`
		} `json:"request"`
	}
	require.NoError(t, json.Unmarshal(record.Payload, &raw))
	assert.Equal(t, "control_request", envelope.Type)
	assert.Equal(t, record.RequestID, envelope.RequestID)
	assert.Equal(t, contracts.CodexControlToolAsyncQuestion, envelope.Request.ToolName)
	return envelope.Request.Input, raw.Request.Input
}

// codexQuestionReplyEntry decodes the one entry of a reply envelope and checks
// the tag lines that frame it.
func codexQuestionReplyEntry(t *testing.T, input string) (answer, question, questionItemID string) {
	t.Helper()
	require.True(t, strings.HasPrefix(input, "<"+codexQuestionReplyTag+">\n"), "the envelope opens with the reply tag")
	require.True(t, strings.HasSuffix(input, "\n</"+codexQuestionReplyTag+">"), "the envelope closes with the reply tag")
	body := strings.TrimSuffix(strings.TrimPrefix(input, "<"+codexQuestionReplyTag+">\n"), "\n</"+codexQuestionReplyTag+">")
	var entries []struct {
		Answer         string `json:"answer"`
		Question       string `json:"question"`
		QuestionItemID string `json:"questionItemId"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &entries))
	require.Len(t, entries, 1)
	return entries[0].Answer, entries[0].Question, entries[0].QuestionItemID
}

// childRecordingControlSink gives each child transcript a recording
// agenttest.ControlSink. Sink.ChildSink gives a child a plain Sink, which drops
// control requests, so a child-thread publish would stay invisible without it.
type childRecordingControlSink struct {
	*agenttest.ControlSink
	childMu  sync.Mutex
	children map[string]*agenttest.ControlSink
}

var _ agent.ServiceFacets = (*childRecordingControlSink)(nil)

// ChildSink returns the recording services of one child transcript.
func (s *childRecordingControlSink) ChildSink(childAgentID string) agent.ProviderServices {
	s.childMu.Lock()
	defer s.childMu.Unlock()
	if s.children == nil {
		s.children = make(map[string]*agenttest.ControlSink)
	}
	child := s.children[childAgentID]
	if child == nil {
		child = &agenttest.ControlSink{}
		s.children[childAgentID] = child
	}
	return agent.NewProviderServices(child)
}

// onlyControlChild returns the one child transcript that ChildSink created.
func (s *childRecordingControlSink) onlyControlChild(t *testing.T) *agenttest.ControlSink {
	t.Helper()
	s.childMu.Lock()
	defer s.childMu.Unlock()
	require.Len(t, s.children, 1, "one child route")
	for _, child := range s.children {
		return child
	}
	return nil
}

// TestCodexPublishAsyncQuestionsPublishesOneNonBlockingRequestPerQuestion covers
// the main-thread item/completed of an async agent message: one non-blocking
// request per question, with the identity that Codex's reply format carries.
func TestCodexPublishAsyncQuestionsPublishesOneNonBlockingRequestPerQuestion(t *testing.T) {
	t.Parallel()

	a, sink, _ := newCodexControlAgent(t)
	handleCodexOutput(a, providerkit.ParseLine([]byte(asyncQuestionCompletedLine("main-thread", "call_abc",
		`[{"title":"Which color?","options":["Blue","Red"]},{"title":"Which shade?","options":["Light","Dark"]}]`))))

	records := sink.PublishedControls()
	require.Len(t, records, 2)
	assert.Equal(t, "codex-async-question-call_abc-0", records[0].RequestID)
	assert.True(t, records[0].NonBlocking, "the turn continues while the question waits")
	assert.JSONEq(t,
		`{"type":"control_request","request_id":"codex-async-question-call_abc-0","request":{"tool_name":"CodexAsyncQuestion","input":{"question_item_id":"[\"request_user_input_async\",\"call_abc\",0]","title":"Which color?","options":["Blue","Red"]}}}`,
		string(records[0].Payload))
	assert.Equal(t, "codex-async-question-call_abc-1", records[1].RequestID)
	assert.True(t, records[1].NonBlocking, "the turn continues while the question waits")
	assert.JSONEq(t,
		`{"type":"control_request","request_id":"codex-async-question-call_abc-1","request":{"tool_name":"CodexAsyncQuestion","input":{"question_item_id":"[\"request_user_input_async\",\"call_abc\",1]","title":"Which shade?","options":["Light","Dark"]}}}`,
		string(records[1].Payload))
}

// TestCodexPublishAsyncQuestionsPublishesNothing covers each item that reaches
// the same handler with no question for the user.
func TestCodexPublishAsyncQuestionsPublishesNothing(t *testing.T) {
	t.Parallel()

	t.Run("a normal agent message", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newCodexControlAgent(t)
		handleCodexOutput(a, providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","method":"item/completed","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"agentMessage","id":"m1","text":"Hello."}}}`)))

		require.Equal(t, 1, sink.MessageCount(), "the transcript still receives the message")
		assert.Zero(t, sink.PublishedControlCount())
	})

	t.Run("an async message with no questions", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newCodexControlAgent(t)
		handleCodexOutput(a, providerkit.ParseLine([]byte(asyncQuestionCompletedLine("main-thread", "call_abc", `[]`))))

		require.Equal(t, 1, sink.MessageCount(), "the transcript still receives the message")
		assert.Zero(t, sink.PublishedControlCount())
	})

	t.Run("an async message on a child thread", func(t *testing.T) {
		t.Parallel()
		a, _, _ := newCodexControlAgent(t)
		parent := &childRecordingControlSink{ControlSink: &agenttest.ControlSink{}}
		a.sink = agent.NewProviderServices(parent)
		handleCodexOutput(a, providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"spawn-1","kind":"started","agentThreadId":"child-1","agentPath":"/root/child"}}}`)))
		handleCodexOutput(a, providerkit.ParseLine([]byte(asyncQuestionCompletedLine("child-1", "call_child", `[{"title":"Which color?"}]`))))

		child := parent.onlyControlChild(t)
		found := false
		for _, message := range child.Messages() {
			if strings.Contains(string(message.Content), "call_child") {
				found = true
			}
		}
		require.True(t, found, "the child transcript receives the item")
		assert.Zero(t, child.PublishedControlCount(), "a child-thread question never reaches the user")
		assert.Zero(t, parent.PublishedControlCount())
	})
}

// TestCodexPublishAsyncQuestionsSkipsABlankTitle keeps the index of each question
// even when an earlier one publishes nothing.
func TestCodexPublishAsyncQuestionsSkipsABlankTitle(t *testing.T) {
	t.Parallel()

	a, sink, _ := newCodexControlAgent(t)
	handleCodexOutput(a, providerkit.ParseLine([]byte(asyncQuestionCompletedLine("main-thread", "call_abc", `[{"title":"   "},{"title":"Second?"}]`))))

	records := sink.PublishedControls()
	require.Len(t, records, 1)
	assert.True(t, records[0].NonBlocking)
	assert.JSONEq(t,
		`{"type":"control_request","request_id":"codex-async-question-call_abc-1","request":{"tool_name":"CodexAsyncQuestion","input":{"question_item_id":"[\"request_user_input_async\",\"call_abc\",1]","title":"Second?"}}}`,
		string(records[0].Payload))
}

// TestCodexPublishAsyncQuestionsKeepsTheOptionsCodexShows applies the limits of
// Codex's own question UI: the first 32 non-blank options of at most 512 bytes.
func TestCodexPublishAsyncQuestionsKeepsTheOptionsCodexShows(t *testing.T) {
	t.Parallel()

	options := []string{"", "   ", strings.Repeat("x", 600)}
	for i := 0; i < 40; i++ {
		options = append(options, fmt.Sprintf("opt-%02d", i))
	}
	encoded, err := json.Marshal(options)
	require.NoError(t, err)
	a, sink, _ := newCodexControlAgent(t)
	handleCodexOutput(a, providerkit.ParseLine([]byte(asyncQuestionCompletedLine("main-thread", "call_abc",
		`[{"title":"Pick one.","options":`+string(encoded)+`},{"title":"Free text?"}]`))))

	records := sink.PublishedControls()
	require.Len(t, records, 2)
	input, _ := decodePublishedAsyncQuestion(t, records[0])
	assert.Equal(t, "Pick one.", input.Title)
	var expected []string
	for i := 0; i < 32; i++ {
		expected = append(expected, fmt.Sprintf("opt-%02d", i))
	}
	assert.Equal(t, expected, input.Options, "the blank and long entries drop, and the first 32 stay in order")
	freeInput, freeRawInput := decodePublishedAsyncQuestion(t, records[1])
	assert.Equal(t, "Free text?", freeInput.Title)
	assert.Nil(t, freeInput.Options)
	assert.NotContains(t, freeRawInput, "options", "a question with no options carries no options key")
	assert.Equal(t, []string{strings.Repeat("x", 512)}, codexAsyncQuestionOptions([]string{strings.Repeat("x", 512)}),
		"an option of the full width stays")
}

// TestCodexQuestionReplyWritesTheReplyEnvelope builds Codex's own answer format:
// the reply tag, then one JSON entry whose keys come in Codex's order (answer,
// question, questionItemId).
func TestCodexQuestionReplyWritesTheReplyEnvelope(t *testing.T) {
	t.Parallel()

	payload := asyncQuestionRequestPayload(t, `["request_user_input_async","call_abc",0]`, "Which color?")
	reply, err := codexProvider{}.QuestionReply(payload, "Red")
	require.NoError(t, err)
	want := "<send_user_message_question_reply>\n" +
		`[{"answer":"Red","question":"Which color?","questionItemId":"[\"request_user_input_async\",\"call_abc\",0]"}]` +
		"\n</send_user_message_question_reply>"
	assert.Equal(t, want, reply)
}

// TestCodexQuestionReplyKeepsTheAnswerUnescaped covers the answer text that Go's
// JSON encoder escapes by default. Codex reads the raw bytes.
func TestCodexQuestionReplyKeepsTheAnswerUnescaped(t *testing.T) {
	t.Parallel()

	payload := asyncQuestionRequestPayload(t, `["request_user_input_async","call_abc",0]`, "Which color?")
	reply, err := codexProvider{}.QuestionReply(payload, "<b>&</b>")
	require.NoError(t, err)
	assert.Contains(t, reply, `"answer":"<b>&</b>"`)
	assert.NotContains(t, reply, `\u003c`)
	assert.NotContains(t, reply, `\u003e`)
	assert.NotContains(t, reply, `\u0026`)
}

// TestCodexQuestionReplyCutsTheQuestionAtAUTF8RuneBoundary covers a title past
// the width of Codex's own question UI, with a multi-byte rune across the cut and
// line breaks inside.
func TestCodexQuestionReplyCutsTheQuestionAtAUTF8RuneBoundary(t *testing.T) {
	t.Parallel()

	// The euro sign starts at byte 510 and crosses byte 512.
	title := strings.Repeat("a", 506) + "\r\n" + "bb" + "\u20ac" + strings.Repeat("b", 87)
	require.Len(t, title, 600)
	payload := asyncQuestionRequestPayload(t, `["request_user_input_async","call_abc",0]`, title)
	reply, err := codexProvider{}.QuestionReply(payload, "Red")
	require.NoError(t, err)
	_, question, _ := codexQuestionReplyEntry(t, reply)
	assert.Equal(t, strings.Repeat("a", 506)+"  bb", question,
		"the line breaks become spaces and the cut lands before the straddling rune")
	assert.True(t, utf8.ValidString(question))
	assert.LessOrEqual(t, len(question), codexQuestionReplyMaxBytes)
	assert.Equal(t, strings.Repeat("a", 512), codexQuestionLine(strings.Repeat("a", 512)),
		"a title of the full width passes through")
}

// TestCodexQuestionReplyUsesThePlainFormForALongIdentity falls back to a plain
// quote-and-answer message when the question identity does not fit Codex's reply
// envelope.
func TestCodexQuestionReplyUsesThePlainFormForALongIdentity(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		fill  int
		plain bool
	}{
		{name: "an identity of the full width", fill: 479},
		{name: "an identity one byte wider", fill: 480, plain: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			identity := `["request_user_input_async","` + strings.Repeat("x", test.fill) + `",0]`
			if test.plain {
				require.Greater(t, len(identity), codexQuestionReplyMaxBytes)
			} else {
				require.Len(t, identity, codexQuestionReplyMaxBytes)
			}
			payload := asyncQuestionRequestPayload(t, identity, "Which color?")
			reply, err := codexProvider{}.QuestionReply(payload, "Red")
			require.NoError(t, err)
			if test.plain {
				assert.Equal(t, "> Which color?\n\nRed", reply)
				assert.NotContains(t, reply, codexQuestionReplyTag)
				return
			}
			_, _, questionItemID := codexQuestionReplyEntry(t, reply)
			assert.Equal(t, identity, questionItemID)
		})
	}
}

// TestCodexQuestionReplyPreservesTheSourceTitle checks native normalization without source mutation.
func TestCodexQuestionReplyPreservesTheSourceTitle(t *testing.T) {
	t.Parallel()

	payload := asyncQuestionRequestPayload(t, `["request_user_input_async","call_abc",0]`, "Which color?\nAnd which shade?")
	reply, err := codexProvider{}.QuestionReply(payload, "Red")
	require.NoError(t, err)
	_, question, _ := codexQuestionReplyEntry(t, reply)
	assert.Equal(t, "Which color? And which shade?", question)
	assert.JSONEq(t, string(asyncQuestionRequestPayload(t, `["request_user_input_async","call_abc",0]`, "Which color?\nAnd which shade?")), string(payload))
}

// TestCodexQuestionReplyRefusesAnUnusablePayload covers the request payloads
// whose question the reply cannot identify or quote.
func TestCodexQuestionReplyRefusesAnUnusablePayload(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		payload string
		want    string
	}{
		{name: "a request without a title", payload: `{"request":{"input":{"question_item_id":"[\"request_user_input_async\",\"call_abc\",0]"}}}`, want: "no title"},
		{name: "a request without an identity", payload: `{"request":{"input":{"title":"Which color?"}}}`, want: "no identity"},
		{name: "a payload that is not JSON", payload: `{`, want: "read the async question request"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := codexProvider{}.QuestionReply(json.RawMessage(test.payload), "Red")
			require.Error(t, err)
			assert.ErrorContains(t, err, test.want)
		})
	}
}

func TestCodexAsyncQuestionInstanceSurvivesRepeatedPublication(t *testing.T) {
	t.Parallel()
	a, sink, _ := newCodexControlAgent(t)
	const questions = `[{"title":" "},{"title":"Which color?"},{"title":"Which color?"}]`
	line := asyncQuestionCompletedLine("main-thread", "call-1", questions)
	feedCodexLines(a, line, line)
	feedCodexLines(a, asyncQuestionCompletedLine("main-thread", "call-2", questions))
	a.threadID = "replacement-thread"
	feedCodexLines(a, asyncQuestionCompletedLine("replacement-thread", "call-1", questions))
	records := sink.PublishedControls()
	require.Len(t, records, 8)
	unique := make(map[string]struct{})
	for index, record := range records {
		require.NotEmpty(t, record.InstanceID)
		unique[record.InstanceID] = struct{}{}
		expectedSession := "main-thread"
		if index >= 6 {
			expectedSession = "replacement-thread"
		}
		assert.Equal(t, expectedSession, record.AgentSessionID)
	}
	assert.Len(t, unique, 6, "only repeated events reuse question identities")
	assert.Equal(t, records[0].InstanceID, records[2].InstanceID)
	assert.Equal(t, records[1].InstanceID, records[3].InstanceID)
	assert.NotEqual(t, records[0].InstanceID, records[1].InstanceID, "question positions distinguish identical text")
	assert.NotEqual(t, records[0].InstanceID, records[4].InstanceID, "native items distinguish identical text")
	assert.NotEqual(t, records[0].InstanceID, records[6].InstanceID, "native sessions distinguish identical items")
	assert.Equal(t, "codex-async-question-call-1-1", records[0].RequestID)
	assert.Equal(t, "codex-async-question-call-1-2", records[1].RequestID)
}

func TestCodexQuestionIdentityPreservesUnicodeSeparators(t *testing.T) {
	t.Parallel()
	for _, itemID := range []string{"line\u2028separator", "paragraph\u2029separator", `literal\u2028`, `literal\u2029`, "slash\\\u2028"} {
		identity, err := encodeCodexQuestionItemID(itemID, 0)
		require.NoError(t, err)
		var values []any
		require.NoError(t, json.Unmarshal([]byte(identity), &values))
		require.Len(t, values, 3)
		assert.Equal(t, itemID, values[1])
		if strings.ContainsAny(itemID, "\u2028\u2029") {
			assert.Contains(t, identity, strings.ReplaceAll(itemID, `\`, `\\`), "native JSON identities retain Unicode separators")
		}
	}
}

func BenchmarkCodexOrdinaryQuestionDetection(b *testing.B) {
	params, err := json.Marshal(map[string]any{"threadId": "main-thread", "item": map[string]any{
		"id": "message-1", "type": "agentMessage", "text": strings.Repeat("x", 1<<20),
	}})
	if err != nil {
		b.Fatal(err)
	}
	event, ok := newCodexItemEvent(nil, params)
	if !ok {
		b.Fatal("the benchmark item is invalid")
	}
	a := &Agent{}
	sink := agent.NewProviderServices(&agenttest.Sink{})
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		a.publishAsyncQuestions(sink, event)
	}
}
