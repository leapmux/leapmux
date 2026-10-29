package grok

import (
	"encoding/json"
	"strconv"
	"sync"
	"testing"

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

// compactionNotification builds one auto-compaction session notification line.
func compactionNotification(t *testing.T, update map[string]any) []byte {
	t.Helper()
	return notification(t, grokTestSession, update)
}

// The start of an automatic compaction is live state only: no row, one live
// broadcast with its trigger.
func TestGrokCompactionStartIsLiveState(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)

	a.HandleOutput(compactionNotification(t, map[string]any{
		"sessionUpdate": "auto_compact_started", "percent_of_window": 81,
	}))

	assert.Empty(t, sink.PersistedNotifications(), "the start stores no row")
	assert.Equal(t, []interface{}{
		map[string]interface{}{contracts.CompactionFieldTrigger: "auto"},
	}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
}

// Each of the three end frames stores its native frame, ends the live state,
// and classifies as a compaction boundary.
func TestGrokCompactionEndStoresTheEndAndEndsLiveState(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		frame map[string]any
	}{
		{"completed", map[string]any{"sessionUpdate": "auto_compact_completed", "tokens_before": 120000, "tokens_after": 30000}},
		{"failed", map[string]any{"sessionUpdate": "auto_compact_failed", "error": "the provider refused the summary"}},
		{"cancelled", map[string]any{"sessionUpdate": "auto_compact_cancelled"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
			a.SetPromptActiveForTest(true)

			a.HandleOutput(compactionNotification(t, map[string]any{"sessionUpdate": "auto_compact_started", "percent_of_window": 81}))
			// Buffered text before the end keeps its own row above the boundary.
			a.HandleOutput(chunk(t, "before the end"))
			a.HandleOutput(compactionNotification(t, tc.frame))
			a.FinishPromptRequestForTest(grokTestSession, json.RawMessage(`{"stopReason":"end_turn"}`), nil)

			rows := sink.PersistedNotifications()
			require.Len(t, rows, 1, "one native end row")
			assert.Contains(t, string(rows[0].Content), tc.frame["sessionUpdate"].(string))
			assert.Equal(t, agent.CompactionBoundaryNotification(), grokProvider{}.Classify(rows[0].Content))
			// The third value is the prompt end's own clear.
			assert.Equal(t, []interface{}{
				map[string]interface{}{contracts.CompactionFieldTrigger: "auto"},
				nil,
				nil,
			}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
			texts := assembledTexts(t, sink)
			require.Len(t, texts, 1)
			assert.Equal(t, "before the end", texts[0])
		})
	}
}

// A cancelled compaction ends a live start even though the 1.0.62 worker knew
// no case for it: without the case the start row span forever.
func TestGrokCancelledCompactionEndsTheStart(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)

	a.HandleOutput(compactionNotification(t, map[string]any{"sessionUpdate": "auto_compact_started", "percent_of_window": 81}))
	a.HandleOutput(compactionNotification(t, map[string]any{"sessionUpdate": "auto_compact_cancelled"}))

	assert.Equal(t, []interface{}{
		map[string]interface{}{contracts.CompactionFieldTrigger: "auto"},
		nil,
	}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
	require.Len(t, sink.PersistedNotifications(), 1)
}

// The prompt end ends a live compaction that no native end frame reached, as a
// stop during a compaction does.
func TestGrokPromptEndEndsALiveCompaction(t *testing.T) {
	t.Parallel()
	for _, stopped := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed", true: "stopped"}[stopped], func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
			a.HandleOutput(compactionNotification(t, map[string]any{"sessionUpdate": "auto_compact_started", "percent_of_window": 81}))
			a.HooksForTest().PromptEnded(nil, stopped)
			assert.Equal(t, []interface{}{
				map[string]interface{}{contracts.CompactionFieldTrigger: "auto"},
				nil,
			}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
		})
	}
}

// A compaction of a child session is the child's own: it never reaches the
// parent's live state.
func TestGrokChildCompactionChangesNothing(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.AttachChildSession(acp.ChildSessionTarget{RootSessionID: a.CurrentSessionID(), SessionID: "child-session", RowKey: "call_spawn"})

	a.HandleOutput(notification(t, "child-session", map[string]any{"sessionUpdate": "auto_compact_started", "percent_of_window": 81}))
	a.HandleOutput(notification(t, "child-session", map[string]any{"sessionUpdate": "auto_compact_completed", "tokens_after": 100}))

	assert.Empty(t, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
	assert.Empty(t, sink.PersistedNotifications())
}

func TestGrokClassifyEveryCompactionEndIsABoundary(t *testing.T) {
	t.Parallel()
	for _, word := range []string{
		contracts.GrokNotificationAutoCompactCompleted,
		contracts.GrokNotificationAutoCompactFailed,
		contracts.GrokNotificationAutoCompactCancelled,
	} {
		raw := notification(t, grokTestSession, map[string]any{"sessionUpdate": word})
		assert.Equal(t, agent.CompactionBoundaryNotification(), grokProvider{}.Classify(raw), word)
	}
	assert.Equal(t, agent.NotificationClassification{}, grokProvider{}.Classify(
		notification(t, grokTestSession, map[string]any{"sessionUpdate": "auto_compact_started"})))
}

// grokCompactionCancel is the retry operation of a compaction's end: one
// cancel of the server-error retry, with the compaction's reason.
var grokCompactionCancel = providerkittest.TurnEndThen(providerkit.CompactionEnd())[1:]

// grokHeldPrompt is a Grok agent whose peer holds the first session/prompt
// until the test releases it, and then answers every prompt with reply. The
// test feeds the frames of the running prompt before the release.
type grokHeldPrompt struct {
	agent   *Agent
	sink    *agenttest.Sink
	release func()
}

func newGrokHeldPrompt(t *testing.T, sessionID string, reply agenttest.RPCReply) *grokHeldPrompt {
	t.Helper()
	held := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(held) }) }
	t.Cleanup(release)
	a, sink := newGrokRetryAgent(t, func(request agenttest.RecordedRequest) agenttest.RPCReply {
		if request.Method != acp.MethodSessionPrompt {
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		}
		<-held
		return reply
	})
	a.SetSessionIDForTest(sessionID)
	return &grokHeldPrompt{agent: a, sink: sink, release: release}
}

// end releases the held prompt and waits for its end.
func (h *grokHeldPrompt) end(t *testing.T) {
	t.Helper()
	h.release()
	testutil.RequireEventually(t, func() bool { return !h.agent.PromptActive() })
}

// A failed manual compaction schedules no retry, whatever the classifier reads
// in the failure: a 5xx, a dropped stream, a 429, exhausted free usage, or a
// 402, which would also cancel a pending usage-limit retry.
func TestGrokFailedCompactionSchedulesNoRetry(t *testing.T) {
	t.Parallel()
	fixtures := grokTurnFixtures(t)
	for _, name := range []string{"e503", "e529", "streamdrop", "e429", "freeusage", "e402"} {
		t.Run("the "+name+" failure", func(t *testing.T) {
			t.Parallel()
			fixture := fixtures[name]
			h := newGrokHeldPrompt(t, fixture.sessionID(t), agenttest.RPCReply{Error: fixture.errorObject(t)})
			require.NoError(t, h.agent.CompactContext())
			h.agent.HandleOutput(fixture.RetryState)

			h.end(t)
			assert.Equal(t, []string{"leapmux_notification", "cancel_server_error"}, grokTimelineOps(h.sink.Timeline()), "the failure note, then the verdict")
			assert.Equal(t, grokCompactionCancel, providerkittest.TurnEndAndRetryOps(h.sink.Timeline()))
			assert.Zero(t, h.sink.AutoScheduleCount())
		})
	}
}

// A manual compaction that ends well, or that the reader stops, cancels the
// server-error retry once, with the compaction's own reason.
func TestGrokCompactionEndStatesTheCompactionReason(t *testing.T) {
	t.Parallel()
	t.Run("a compaction that ends well", func(t *testing.T) {
		t.Parallel()
		h := newGrokHeldPrompt(t, grokTestSession, agenttest.RPCReply{Result: json.RawMessage(`{"stopReason":"end_turn"}`)})
		require.NoError(t, h.agent.CompactContext())
		h.agent.HandleOutput(compactionNotification(t, map[string]any{"sessionUpdate": "auto_compact_completed", "tokens_before": 120000, "tokens_after": 30000}))

		h.end(t)
		assert.Equal(t, providerkittest.TurnEndThen(providerkit.CompactionEnd()), providerkittest.TurnEndAndRetryOps(h.sink.Timeline()))
	})
	t.Run("a compaction that the reader stops", func(t *testing.T) {
		t.Parallel()
		h := newGrokHeldPrompt(t, grokTestSession, agenttest.RPCReply{Result: json.RawMessage(`{"stopReason":"cancelled"}`)})
		require.NoError(t, h.agent.CompactContext())
		h.agent.NoteInterruptRequestedForTest()

		h.end(t)
		assert.Equal(t, providerkittest.TurnEndThen(providerkit.CompactionEnd()), providerkittest.TurnEndAndRetryOps(h.sink.Timeline()))
		assert.Equal(t, 1, h.sink.AutoCancelCount())
	})
}

// A /compact that reaches SendInput is Grok's compaction too, as a control
// answer can send it. Its failure schedules no retry.
func TestGrokTypedCompactionSchedulesNoRetry(t *testing.T) {
	t.Parallel()
	fixture := grokTurnFixtures(t)["e503"]
	routes := map[string]func(a *Agent, sessionID, content string) error{
		"SendInput":           func(a *Agent, _, content string) error { return a.SendInput(content, nil) },
		"SendInputForSession": func(a *Agent, sessionID, content string) error { return a.SendInputForSession(sessionID, content, nil) },
	}
	for _, content := range []string{"/compact", "  /compact\n"} {
		for route, send := range routes {
			t.Run(route+" "+strconv.Quote(content), func(t *testing.T) {
				t.Parallel()
				sessionID := fixture.sessionID(t)
				h := newGrokHeldPrompt(t, sessionID, agenttest.RPCReply{Error: fixture.errorObject(t)})
				require.NoError(t, send(h.agent, sessionID, content))

				h.end(t)
				assert.Equal(t, grokCompactionCancel, providerkittest.TurnEndAndRetryOps(h.sink.Timeline()))
				assert.Zero(t, h.sink.AutoScheduleCount())
			})
		}
	}
}

// Grok Build 1.0.70 states "/compact takes no arguments.", so a /compact with
// arguments runs no compaction. That input, a command with an attachment, and
// a message that only mentions the command are conversation turns: a 503
// schedules the server-error retry.
func TestGrokOtherInputIsNoCompaction(t *testing.T) {
	t.Parallel()
	fixture := grokTurnFixtures(t)["e503"]
	attachment := []*leapmuxv1.Attachment{{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("Keep the plan.")}}
	for name, input := range map[string]struct {
		content     string
		attachments []*leapmuxv1.Attachment
	}{
		"the command with arguments":          {"/compact keep the plan", nil},
		"the command with an attachment":      {"/compact", attachment},
		"a longer command name":               {"/compactor", nil},
		"a message that mentions the command": {"Run /compact later.", nil},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newGrokHeldPrompt(t, fixture.sessionID(t), agenttest.RPCReply{Error: fixture.errorObject(t)})
			require.NoError(t, h.agent.SendInput(input.content, input.attachments))

			h.end(t)
			assert.Equal(t, []string{"leapmux_notification", "schedule_server_error"}, grokTimelineOps(h.sink.Timeline()))
		})
	}
}

// The prompt after a compaction takes the classifier's verdict again, so its
// 503 schedules the server-error retry.
func TestGrokPromptAfterACompactionAppliesItsOwnVerdict(t *testing.T) {
	t.Parallel()
	fixture := grokTurnFixtures(t)["e503"]
	h := newGrokHeldPrompt(t, fixture.sessionID(t), agenttest.RPCReply{Error: fixture.errorObject(t)})
	require.NoError(t, h.agent.CompactContext())
	h.end(t)
	compactionOps := len(h.sink.Timeline())

	require.NoError(t, h.agent.SendInput("Continue the task.", nil))
	testutil.RequireEventually(t, func() bool { return !h.agent.PromptActive() })
	assert.Equal(t, []string{"leapmux_notification", "schedule_server_error"}, grokTimelineOps(h.sink.Timeline()[compactionOps:]))
}

// A /compact for a session that is no longer current starts no prompt.
func TestGrokTypedCompactionForAnotherSessionStartsNothing(t *testing.T) {
	t.Parallel()
	a, _, requests := newGrokAgent(t, agent.Options{}, nil)

	require.ErrorIs(t, a.SendInputForSession("session-replaced", "/compact", nil), agent.ErrInputSessionChanged)
	assert.False(t, a.PromptActive())
	assert.Empty(t, requestsFor(requests(), acp.MethodSessionPrompt))
}

// The preparation of a compaction prompt checks the session that the base
// claimed for the prompt, because the session can change after the check
// before the send. It marks only a prompt of the expected session.
func TestGrokCompactionPreparationChecksTheClaimedSession(t *testing.T) {
	t.Parallel()
	expected := grokTestSession
	for name, tc := range map[string]struct {
		expected *string
		claimed  string
		marks    bool
	}{
		"the expected session":            {&expected, grokTestSession, true},
		"a session that replaced it":      {&expected, "session-replaced", false},
		"any session for the current one": {nil, "session-replaced", true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, _, _ := newGrokAgent(t, agent.Options{}, nil)
			err := a.prepareCompactPrompt(tc.expected)(tc.claimed)
			if tc.marks {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, agent.ErrInputSessionChanged)
			}
			a.stateMu.Lock()
			marked := a.compactionPrompt
			a.stateMu.Unlock()
			assert.Equal(t, tc.marks, marked)
		})
	}
}
