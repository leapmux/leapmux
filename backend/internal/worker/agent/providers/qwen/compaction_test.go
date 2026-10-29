package qwen

import (
	"encoding/json"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit/providerkittest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This error is the native reply in qwen/logs/r2-compress-1.log.
const qwenTestCompressRefusal = `{"code":-32603,"message":"Internal error","data":{"details":"Compression was not beneficial for this history size."}}`

type qwenCompactionFailureCase struct {
	manual       bool
	tryWhileBusy bool
	stopped      bool
	reply        json.RawMessage
}

func runQwenCompactionFailure(t *testing.T, tc qwenCompactionFailureCase) *agenttest.ControlSink {
	t.Helper()
	release := make(chan struct{})
	var once sync.Once
	a, sink, _ := newQwenAgent(t, nil, func(request agenttest.RecordedRequest) agenttest.RPCReply {
		if request.Method == acp.MethodSessionPrompt {
			<-release
			return agenttest.RPCReply{Error: tc.reply}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	if tc.manual {
		require.NoError(t, a.CompactContext())
	} else {
		require.NoError(t, a.SendInput("ordinary prompt", nil))
	}
	if tc.tryWhileBusy {
		require.ErrorIs(t, a.CompactContext(), agent.ErrAgentBusy)
	}
	a.HandleOutput(metaChunk(t, "Compressing context...", map[string]any{"contextCompression": map[string]any{"phase": "progress"}}))
	if tc.stopped {
		a.SetStoppedForTest(true)
	}
	once.Do(func() { close(release) })
	testutil.RequireEventually(t, func() bool { return !a.PromptActive() })
	live, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
	require.True(t, ok)
	assert.Nil(t, live, "every failed or stopped compaction ends its live state")
	return sink
}

func TestQwenCompressRefusalIsAStatus(t *testing.T) {
	t.Parallel()
	for _, busy := range []bool{false, true} {
		sink := runQwenCompactionFailure(t, qwenCompactionFailureCase{manual: true, tryWhileBusy: busy, reply: json.RawMessage(qwenTestCompressRefusal)})
		require.Equal(t, []map[string]any{{
			"type": contracts.NotificationTypeAgentStatus,
			"text": "Qwen Code did not compact the context: the compression was not beneficial for this history size.",
		}}, sink.Notifications())
	}
}

func TestQwenOtherCompressErrorStillFails(t *testing.T) {
	t.Parallel()
	for _, reply := range []string{
		`{"code":-32603,"message":"Internal error","data":{"details":"Another failure."}}`,
		`{"code":-32000,"message":"Internal error","data":{"details":"Compression was not beneficial for this history size."}}`,
	} {
		sink := runQwenCompactionFailure(t, qwenCompactionFailureCase{manual: true, reply: json.RawMessage(reply)})
		require.Len(t, sink.Notifications(), 1)
		assert.Equal(t, contracts.NotificationTypeAgentError, sink.Notifications()[0]["type"])
	}
}

func TestQwenCompressRefusalDoesNotChangeAnOrdinaryPrompt(t *testing.T) {
	t.Parallel()
	for _, busy := range []bool{false, true} {
		sink := runQwenCompactionFailure(t, qwenCompactionFailureCase{tryWhileBusy: busy, reply: json.RawMessage(qwenTestCompressRefusal)})
		require.Len(t, sink.Notifications(), 1)
		assert.Equal(t, contracts.NotificationTypeAgentError, sink.Notifications()[0]["type"])
	}
}

func TestQwenStoppedCompactionEndsLiveStateWithoutAFailureNote(t *testing.T) {
	t.Parallel()
	sink := runQwenCompactionFailure(t, qwenCompactionFailureCase{manual: true, stopped: true, reply: json.RawMessage(qwenTestCompressRefusal)})
	assert.Empty(t, sink.Notifications())
}

func TestQwenCompactionWriteFailureClearsItsPendingRequest(t *testing.T) {
	t.Parallel()
	a, _, _ := newQwenAgent(t, nil, nil)
	a.SetStdinForTest(agenttest.FailingStdin{})
	require.Error(t, a.CompactContext())
	assert.False(t, a.PromptActive())
	a.stateMu.Lock()
	pending := a.compressPending
	a.stateMu.Unlock()
	assert.Nil(t, pending)
}

// qwenRetryObservedAt is the agent clock of the compaction verdict tests.
var qwenRetryObservedAt = time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)

// qwenCompactionCancel is the retry operation of a compaction's end: one
// cancel of the server-error retry, with the compaction's reason.
var qwenCompactionCancel = providerkittest.TurnEndThen(providerkit.CompactionEnd())[1:]

// compactQwen runs CompactContext and waits for the prompt's end.
func compactQwen(t *testing.T, a *Agent) {
	t.Helper()
	require.NoError(t, a.CompactContext())
	testutil.RequireEventually(t, func() bool { return !a.PromptActive() })
}

// A failed manual compaction schedules no retry, even for a failure that the
// classifier retries in a conversation turn.
func TestQwenFailedCompactionSchedulesNoRetry(t *testing.T) {
	t.Parallel()
	for name, response := range map[string]string{
		"a 500":              qwenFixture500Error,
		"a network failure":  qwenFixtureConnectionError,
		"a 429":              qwenFixtureRateLimit,
		"a refused API key":  qwenFixture401Error,
		"the OAuth tier end": qwenOAuthDiscontinuedError,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink := newQwenRetryAgent(t, qwenRetryObservedAt, response)

			compactQwen(t, a)
			assert.Equal(t, []string{"leapmux_notification", "cancel_server_error"}, timelineOps(sink.Timeline()), "the failure note, then the verdict")
			assert.Equal(t, qwenCompactionCancel, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			assert.Zero(t, sink.AutoScheduleCount())
		})
	}
}

// A manual compaction that ends well, that Qwen refuses as not beneficial, or
// that the reader stops cancels the server-error retry once, with the
// compaction's own reason.
func TestQwenCompactionEndStatesTheCompactionReason(t *testing.T) {
	t.Parallel()
	t.Run("a compaction that ends well", func(t *testing.T) {
		t.Parallel()
		a, sink := newQwenRetryAgent(t, qwenRetryObservedAt, qwenFixtureSuccess)

		compactQwen(t, a)
		assert.Equal(t, providerkittest.TurnEndThen(providerkit.CompactionEnd()), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
	})
	t.Run("a compaction that Qwen refuses", func(t *testing.T) {
		t.Parallel()
		a, sink := newQwenRetryAgent(t, qwenRetryObservedAt, `{"jsonrpc":"2.0","id":3,"error":`+qwenTestCompressRefusal+`}`)

		compactQwen(t, a)
		assert.Equal(t, []string{"leapmux_notification", "cancel_server_error"}, timelineOps(sink.Timeline()), "the refusal's status, then the verdict")
		assert.Equal(t, qwenCompactionCancel, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
	})
	t.Run("a compaction that the reader stops", func(t *testing.T) {
		t.Parallel()
		a, sink := newQwenRetryAgent(t, qwenRetryObservedAt, qwenFixture500Error)
		a.SetPromptActiveForTest(true)
		a.stateMu.Lock()
		a.compressPending = &qwenCompactionRequest{}
		a.stateMu.Unlock()
		a.NoteInterruptRequestedForTest()

		a.FinishPromptRequestForTest(qwenTestSession, nil, qwenResponseError(t, qwenFixture500Error))
		assert.Equal(t, qwenCompactionCancel, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
		assert.Equal(t, 1, sink.AutoCancelCount())
	})
}

// Qwen Code 0.26.0 runs /compress and its alias /summarize as its compaction,
// and reads their arguments as instructions for the summary
// (compressCommand). A typed compaction command starts the same compaction
// turn as CompactContext, so its failure schedules no retry.
func TestQwenTypedCompactionSchedulesNoRetry(t *testing.T) {
	t.Parallel()
	routes := map[string]func(a *Agent, content string) error{
		"SendInput":           func(a *Agent, content string) error { return a.SendInput(content, nil) },
		"SendInputForSession": func(a *Agent, content string) error { return a.SendInputForSession(qwenTestSession, content, nil) },
	}
	for _, content := range []string{"/compress", "/compress keep the test plan", "/summarize keep the test plan", "  /compress\n"} {
		for route, send := range routes {
			t.Run(route+" "+strconv.Quote(content), func(t *testing.T) {
				t.Parallel()
				a, sink := newQwenRetryAgent(t, qwenRetryObservedAt, qwenFixture500Error)

				require.NoError(t, send(a, content))
				testutil.RequireEventually(t, func() bool { return !a.PromptActive() })
				assert.Equal(t, qwenCompactionCancel, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
				assert.Zero(t, sink.AutoScheduleCount())
			})
		}
	}
}

// Qwen Code has no /compact command, and a message with an attachment is a
// conversation turn. Such input takes the classifier's verdict, so a 500
// schedules the server-error retry.
func TestQwenOtherInputIsNoCompaction(t *testing.T) {
	t.Parallel()
	attachment := []*leapmuxv1.Attachment{{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("Keep the plan.")}}
	for name, input := range map[string]struct {
		content     string
		attachments []*leapmuxv1.Attachment
	}{
		"the command that Qwen does not know": {"/compact keep the plan", nil},
		"the command with an attachment":      {"/compress", attachment},
		"a longer command name":               {"/compressor", nil},
		"a message that mentions the command": {"Run /compress later.", nil},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink := newQwenRetryAgent(t, qwenRetryObservedAt, qwenFixture500Error)

			require.NoError(t, a.SendInput(input.content, input.attachments))
			testutil.RequireEventually(t, func() bool { return !a.PromptActive() })
			assert.Equal(t, []string{"leapmux_notification", "schedule_server_error"}, timelineOps(sink.Timeline()))
		})
	}
}

// The prompt after a compaction takes the classifier's verdict again: a
// message that the reader typed next, and a continuation prompt that carries
// input steered into the compaction. The mark of the compaction is gone at
// each prompt's start.
func TestQwenPromptAfterACompactionAppliesItsOwnVerdict(t *testing.T) {
	t.Parallel()
	t.Run("the next message", func(t *testing.T) {
		t.Parallel()
		a, sink := newQwenRetryAgent(t, qwenRetryObservedAt, qwenFixture500Error)
		compactQwen(t, a)
		compactionOps := len(sink.Timeline())

		sendQwenPrompt(t, a)
		assert.Equal(t, []string{"leapmux_notification", "schedule_server_error"}, timelineOps(sink.Timeline()[compactionOps:]))
	})
	t.Run("a continuation of steered input", func(t *testing.T) {
		t.Parallel()
		release := make(chan struct{})
		var once sync.Once
		t.Cleanup(func() { once.Do(func() { close(release) }) })
		var prompts atomic.Int32
		failure := qwenResponseErrorObject(t, qwenFixture500Error)
		a, _, requests := newQwenAgent(t, nil, func(request agenttest.RecordedRequest) agenttest.RPCReply {
			if request.Method != acp.MethodSessionPrompt {
				return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
			}
			if prompts.Add(1) == 1 {
				<-release
			}
			return agenttest.RPCReply{Error: failure}
		})
		sink := &agenttest.Sink{}
		a.SetSinkForTest(agent.NewProviderServices(sink))
		require.NoError(t, a.CompactContext())
		require.NoError(t, a.SteerInput("Keep the test plan.", nil))

		once.Do(func() { close(release) })
		testutil.RequireEventually(t, func() bool { return prompts.Load() == 2 && !a.PromptActive() })
		var promptRequests int
		for _, request := range requests() {
			if request.Method == acp.MethodSessionPrompt {
				promptRequests++
			}
		}
		require.Equal(t, 2, promptRequests, "the steered input becomes a continuation prompt")
		assert.Equal(t, append(append([]agenttest.TimelineEntry(nil), qwenCompactionCancel...), agenttest.TimelineEntry{
			Op: "schedule_server_error", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR,
		}), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
	})
}

// A typed compaction command for a session that is no longer current starts
// no prompt.
func TestQwenTypedCompactionForAnotherSessionStartsNothing(t *testing.T) {
	t.Parallel()
	a, _, requests := newQwenAgent(t, nil, nil)

	require.ErrorIs(t, a.SendInputForSession("session-replaced", "/compress keep the plan", nil), agent.ErrInputSessionChanged)
	assert.False(t, a.PromptActive())
	for _, request := range requests() {
		assert.NotEqual(t, acp.MethodSessionPrompt, request.Method)
	}
}

// The preparation of a compaction prompt checks the session that the base
// claimed for the prompt, because the session can change after the check
// before the send. It marks only a prompt of the expected session.
func TestQwenCompactionPreparationChecksTheClaimedSession(t *testing.T) {
	t.Parallel()
	expected := qwenTestSession
	for name, tc := range map[string]struct {
		expected *string
		claimed  string
		marks    bool
	}{
		"the expected session":            {&expected, qwenTestSession, true},
		"a session that replaced it":      {&expected, "session-replaced", false},
		"any session for the current one": {nil, "session-replaced", true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, _, _ := newQwenAgent(t, nil, nil)
			request := &qwenCompactionRequest{}
			err := a.prepareCompressPrompt(tc.expected, request)(tc.claimed)
			if tc.marks {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, agent.ErrInputSessionChanged)
			}
			a.stateMu.Lock()
			pending := a.compressPending
			a.stateMu.Unlock()
			assert.Equal(t, tc.marks, pending == request)
			assert.Equal(t, tc.marks, pending != nil)
		})
	}
}
