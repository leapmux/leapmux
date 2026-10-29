package goose

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit/providerkittest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGooseCompactTurnStoresTheBoundaryRow pins the manual compaction flow:
// the live compaction runs while the /compact prompt runs, and the prompt's
// end stores the one boundary row (decision 21: no native frame exists).
func TestGooseCompactTurnStoresTheBoundaryRow(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newGooseOutputAgent(agent.NewProviderServices(sink))
	a.SetPromptActiveForTest(true)
	a.Mu.Lock()
	a.compaction = &gooseCompactionMark{sessionID: "s1"}
	a.Mu.Unlock()
	providerkit.PublishCompaction(a.Sink(), "manual")
	a.FinishPromptRequestForTest("s1", []byte(`{"stopReason":"end_turn","usage":{"totalTokens":2}}`), nil)
	ends := sink.LeapMuxNotifications()
	require.Len(t, ends, 1, "the compact turn stores one boundary row")
	assert.Equal(t, contracts.NotificationTypeCompactionEnded, ends[0][contracts.NotificationFieldType])
	assert.NotContains(t, ends[0], contracts.NotificationFieldOutcome)
	info := sink.SessionInfoValues(contracts.SessionInfoKeyCompaction)
	assert.Equal(t, []interface{}{map[string]interface{}{"trigger": "manual"}, nil}, info, "the live compaction withdraws at the end")
}

// gooseCompactNetworkUpdate is CONSTRUCTED from the Goose 1.56.0 source: a
// failed manual compaction prints the bare error (ops_compaction.rs,
// command_error), and a refused connection prints the network error of
// gooseNetworkUpdate (goose-provider-types errors.rs). The verdict reads the
// message as a transient failure.
const gooseCompactNetworkUpdate = `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Network error: Could not connect to 127.0.0.1:18999 — check your network connection and try again."},"messageId":"msg_compact_network","_meta":{"goose":{"created":1793148864,"messageId":"msg_compact_network"}}}`

// gooseEndTurn is Goose's answer to a prompt that ended, also after a failure
// that it printed as an agent message.
const gooseEndTurn = `{"stopReason":"end_turn"}`

// newGooseCompactionAgent builds the agent of newGooseRetryAgent with a
// recording stdin and no running prompt, so CompactContext and SendInput write
// their prompt.
func newGooseCompactionAgent(t *testing.T, sink *agenttest.Sink) (*Agent, *agenttest.Stdin) {
	t.Helper()
	clock := testutil.NewQuartzMock(t)
	clock.Set(gooseCapturedAt)
	stdin := &agenttest.Stdin{}
	a := &Agent{}
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{
		AgentID: "goose-1", ProviderName: "goose", Clock: clock, Stdin: stdin, Ctx: t.Context(),
	})
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetSessionIDForTest("s1")
	*a.HooksForTest() = a.configure()
	return a, stdin
}

// gooseCompactionEndOps is the turn end of a compaction that Goose answers
// with end_turn: the turn-end row, then one cancel of the server-error retry
// with the compaction's reason.
var gooseCompactionEndOps = providerkittest.TurnEndThen(providerkit.CompactionEnd())

// A failed manual compaction schedules no retry, whatever the classifier reads
// in the failure: a refused connection, a turn_failed prompt error, or
// exhausted credits, which would also cancel a pending usage-limit retry.
func TestGooseFailedCompactionSchedulesNoRetry(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		update   string
		response string
		err      string
		wantOps  []agenttest.TimelineEntry
	}{
		"a refused connection": {update: gooseCompactNetworkUpdate, response: gooseEndTurn, wantOps: gooseCompactionEndOps},
		"the automatic compaction's captured failure": {
			update: gooseCompactionFailedUpdate, response: gooseEndTurn, wantOps: gooseCompactionEndOps,
		},
		"a turn_failed prompt error": {update: gooseServerErrorUpdate, err: gooseTurnFailedLine, wantOps: gooseCompactionEndOps[1:]},
		"exhausted credits":          {err: gooseCreditsExhaustedLine, wantOps: gooseCompactionEndOps[1:]},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a, _ := newGooseCompactionAgent(t, sink)
			require.NoError(t, a.CompactContext())
			if tc.update != "" {
				a.HandleOutput(gooseUpdateLine(tc.update))
			}
			var err error
			if tc.err != "" {
				err = gooseResponseError(t, tc.err)
			}
			var response json.RawMessage
			if tc.response != "" {
				response = json.RawMessage(tc.response)
			}

			a.FinishPromptRequestForTest("s1", response, err)
			assert.Equal(t, tc.wantOps, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			assert.Zero(t, sink.AutoScheduleCount())
			assert.Equal(t, []interface{}{map[string]interface{}{"trigger": "manual"}, nil}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction),
				"the live compaction withdraws at the end")
		})
	}
}

// A manual compaction that ends well, or that the reader stops, cancels the
// server-error retry once, with the compaction's own reason.
func TestGooseCompactionEndStatesTheCompactionReason(t *testing.T) {
	t.Parallel()
	for name, stop := range map[string]bool{"a compaction that ends well": false, "a compaction that the reader stops": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a, _ := newGooseCompactionAgent(t, sink)
			require.NoError(t, a.CompactContext())
			response := gooseEndTurn
			if stop {
				a.NoteInterruptRequestedForTest()
				response = `{"stopReason":"cancelled"}`
			}

			a.FinishPromptRequestForTest("s1", json.RawMessage(response), nil)
			assert.Equal(t, gooseCompactionEndOps, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			assert.Equal(t, 1, sink.AutoCancelCount())
			ends := 0
			for _, notification := range sink.LeapMuxNotifications() {
				if notification[contracts.NotificationFieldType] == contracts.NotificationTypeCompactionEnded {
					ends++
				}
			}
			assert.Equal(t, map[bool]int{false: 1, true: 0}[stop], ends, "only a compaction that ends well stores the boundary row")
		})
	}
}

// Goose 1.56.0 runs its compaction for /compact with arguments too
// (ops_slash_command.rs and ops_compaction.rs ignore them), and for its two
// other compaction triggers. A typed trigger starts the same compaction turn
// as CompactContext: the live compaction runs, and the failure schedules no
// retry.
func TestGooseTypedCompactionSchedulesNoRetry(t *testing.T) {
	t.Parallel()
	routes := map[string]func(a *Agent, content string) error{
		"SendInput":           func(a *Agent, content string) error { return a.SendInput(content, nil) },
		"SendInputForSession": func(a *Agent, content string) error { return a.SendInputForSession("s1", content, nil) },
	}
	for _, content := range []string{"/compact keep the plan", "/compact  keep the plan", "  /compact\n", "Please compact this conversation", "/summarize"} {
		for route, send := range routes {
			t.Run(route+" "+strconv.Quote(content), func(t *testing.T) {
				t.Parallel()
				sink := &agenttest.Sink{}
				a, _ := newGooseCompactionAgent(t, sink)
				require.NoError(t, send(a, content))
				assert.Equal(t, []interface{}{map[string]interface{}{"trigger": "manual"}}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction),
					"the typed command starts the live compaction")
				a.HandleOutput(gooseUpdateLine(gooseCompactNetworkUpdate))

				a.FinishPromptRequestForTest("s1", json.RawMessage(gooseEndTurn), nil)
				assert.Equal(t, gooseCompactionEndOps, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
				assert.Zero(t, sink.AutoScheduleCount())
			})
		}
	}
}

// Goose ends its command name at the first literal space, so a tab or a
// newline after /compact makes another command. That input, a command with an
// attachment, and a message that only mentions the command are conversation
// turns: a refused connection schedules the server-error retry.
func TestGooseOtherInputIsNoCompaction(t *testing.T) {
	t.Parallel()
	attachment := []*leapmuxv1.Attachment{{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("Keep the plan.")}}
	for name, input := range map[string]struct {
		content     string
		attachments []*leapmuxv1.Attachment
	}{
		"a tab after the command":             {"/compact\tkeep the plan", nil},
		"a newline after the command":         {"/compact\nkeep the plan", nil},
		"the command with an attachment":      {"/compact", attachment},
		"a longer command name":               {"/compactor", nil},
		"summarize with arguments":            {"/summarize the plan", nil},
		"a message that mentions the command": {"Run /compact later.", nil},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a, _ := newGooseCompactionAgent(t, sink)
			require.NoError(t, a.SendInput(input.content, input.attachments))
			a.HandleOutput(gooseUpdateLine(gooseNetworkUpdate))

			a.FinishPromptRequestForTest("s1", json.RawMessage(gooseEndTurn), nil)
			assert.Equal(t, []string{"turn_end", "schedule_server_error"}, providerkittest.TurnEndAndRetryOpNames(sink.Timeline()))
			assert.Empty(t, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
		})
	}
}

// The prompt after a compaction takes the classifier's verdict again, so its
// refused connection schedules the server-error retry.
func TestGoosePromptAfterACompactionAppliesItsOwnVerdict(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a, _ := newGooseCompactionAgent(t, sink)
	require.NoError(t, a.CompactContext())
	a.FinishPromptRequestForTest("s1", json.RawMessage(gooseEndTurn), nil)
	compactionOps := len(sink.Timeline())

	require.NoError(t, a.SendInput("Continue the task.", nil))
	a.HandleOutput(gooseUpdateLine(gooseNetworkUpdate))
	a.FinishPromptRequestForTest("s1", json.RawMessage(gooseEndTurn), nil)
	assert.Equal(t, []string{"turn_end", "schedule_server_error"}, providerkittest.TurnEndAndRetryOpNames(sink.Timeline()[compactionOps:]))
}

// A compaction prompt whose end never runs, as when the session changes while
// it runs, leaves no mark on the next prompt. The next prompt's start ends the
// live compaction, and the next prompt's failure schedules its own retry.
func TestGooseCompactionThatNeverEndedMarksNoLaterPrompt(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a, _ := newGooseCompactionAgent(t, sink)
	require.NoError(t, a.CompactContext())
	// The session swap resets the turn, and the prompt's own end never comes.
	a.SetPromptActiveForTest(false)

	require.NoError(t, a.SendInput("Continue the task.", nil))
	assert.Equal(t, []interface{}{map[string]interface{}{"trigger": "manual"}, nil}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction),
		"the next prompt's start ends the live compaction")
	a.HandleOutput(gooseUpdateLine(gooseNetworkUpdate))
	a.FinishPromptRequestForTest("s1", json.RawMessage(gooseEndTurn), nil)
	assert.Equal(t, []string{"turn_end", "schedule_server_error"}, providerkittest.TurnEndAndRetryOpNames(sink.Timeline()))
	assert.Empty(t, sink.LeapMuxNotifications(), "the conversation turn stores no boundary row")
}

// A typed /compact for a session that is no longer current starts no prompt
// and no live compaction.
func TestGooseTypedCompactionForAnotherSessionStartsNothing(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a, stdin := newGooseCompactionAgent(t, sink)

	require.ErrorIs(t, a.SendInputForSession("s-replaced", "/compact keep the plan", nil), agent.ErrInputSessionChanged)
	assert.False(t, a.PromptActive())
	assert.NotContains(t, stdin.String(), `"method":"session/prompt"`)
	assert.Empty(t, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
}

// A compaction prompt whose native write fails never runs. The send ends the
// live compaction that its preparation published and clears the mark, so the
// next prompt is a conversation turn with its own verdict.
func TestGooseCompactionThatFailsToSendEndsItsLiveCompaction(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a, _ := newGooseCompactionAgent(t, sink)
	a.SetStdinForTest(agenttest.FailingStdin{})

	require.Error(t, a.CompactContext())
	assert.False(t, a.PromptActive())
	assert.Equal(t, []interface{}{map[string]interface{}{"trigger": "manual"}, nil}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
	a.Mu.Lock()
	assert.Nil(t, a.compaction)
	a.Mu.Unlock()
	assert.Empty(t, providerkittest.TurnEndAndRetryOps(sink.Timeline()), "the prompt that never ran applies no verdict")
}

// The preparation of a compaction prompt checks the session that the base
// claimed for the prompt, because the session can change after the check
// before the send. It marks and publishes only for the expected session.
func TestGooseCompactionPreparationChecksTheClaimedSession(t *testing.T) {
	t.Parallel()
	expected := "s1"
	for name, tc := range map[string]struct {
		expected *string
		claimed  string
		marks    bool
	}{
		"the expected session":            {&expected, "s1", true},
		"a session that replaced it":      {&expected, "s-replaced", false},
		"any session for the current one": {nil, "s-replaced", true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a, _ := newGooseCompactionAgent(t, sink)
			mark, err := a.prepareCompactPrompt(tc.expected)(tc.claimed)
			if tc.marks {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, agent.ErrInputSessionChanged)
			}
			a.Mu.Lock()
			current := a.compaction
			a.Mu.Unlock()
			assert.Equal(t, tc.marks, current != nil)
			assert.Same(t, mark, current, "the preparation returns the mark that it set")
			assert.Equal(t, tc.marks, len(sink.SessionInfoValues(contracts.SessionInfoKeyCompaction)) == 1)
		})
	}
}
