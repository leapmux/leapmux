package inputqueue

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// The automatic-retry log states each event in a retry's life with its own
// message. These tests capture the default logger, which is process-global, so
// none of them runs in parallel.

const (
	usageLimitRetry  = leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT
	serverErrorRetry = leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR
)

// retryLogEntries returns the fields of each captured line with the message,
// in the order that the code wrote them.
func retryLogEntries(t *testing.T, logs *testutil.LogBuffer, message string) []map[string]string {
	t.Helper()
	var entries []map[string]string
	for _, line := range strings.Split(logs.String(), "\n") {
		if line == "" {
			continue
		}
		if fields := parseLogLine(t, line); fields["msg"] == message {
			entries = append(entries, fields)
		}
	}
	return entries
}

// parseLogLine splits one line of slog's text handler into its key=value
// fields. The handler quotes a value that holds a space or a quote.
func parseLogLine(t *testing.T, line string) map[string]string {
	t.Helper()
	fields := map[string]string{}
	for rest := line; rest != ""; rest = strings.TrimPrefix(rest, " ") {
		key, value, found := strings.Cut(rest, "=")
		require.True(t, found, "a field without '=' in %q", line)
		if strings.HasPrefix(value, `"`) {
			quoted, err := strconv.QuotedPrefix(value)
			require.NoError(t, err, "an unterminated quoted value in %q", line)
			fields[key], err = strconv.Unquote(quoted)
			require.NoError(t, err)
			rest = value[len(quoted):]
			continue
		}
		fields[key], rest, _ = strings.Cut(value, " ")
	}
	return fields
}

func pendingRetry(t *testing.T, manager *Manager, kind leapmuxv1.AgentInputKind) SnapshotItem {
	t.Helper()
	snapshot, err := manager.Snapshot(t.Context(), "agent-1")
	require.NoError(t, err)
	for _, item := range snapshot.Items {
		if item.Kind == kind && item.State == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED {
			return item
		}
	}
	require.FailNow(t, "no pending retry of the kind", kind.String())
	return SnapshotItem{}
}

func formattedDeadline(t *testing.T, item SnapshotItem) string {
	t.Helper()
	require.NotNil(t, item.ScheduledAt)
	return timefmt.Format(*item.ScheduledAt)
}

func TestAutoRetryLogSeparatesAScheduleFromAReschedule(t *testing.T) {
	for _, kind := range []leapmuxv1.AgentInputKind{usageLimitRetry, serverErrorRetry} {
		t.Run(kind.String(), func(t *testing.T) {
			logs := testutil.CaptureDefaultLogger(t)
			clock, _, _, manager, _ := newDeadlineFixture(t)
			// A server-error retry ignores ResetAt and takes its deadline from
			// the backoff, which doubles with each schedule. Either way, the
			// jitter windows of the two deadlines do not overlap.
			schedule := func(resetAt time.Time) {
				require.NoError(t, manager.ScheduleAutoRetry(t.Context(), "agent-1", agent.AutoRetrySchedule{Kind: kind, ResetAt: resetAt}))
			}

			schedule(clock.Now().Add(time.Hour))
			first := pendingRetry(t, manager, kind)
			scheduled := retryLogEntries(t, logs, "automatic retry scheduled")
			require.Len(t, scheduled, 1)
			assert.Equal(t, "INFO", scheduled[0]["level"])
			assert.Equal(t, "agent-1", scheduled[0]["agent_id"])
			assert.Equal(t, kind.String(), scheduled[0]["kind"])
			assert.Equal(t, first.ID, scheduled[0]["input_id"])
			assert.Equal(t, formattedDeadline(t, first), scheduled[0]["scheduled_at"])
			assert.NotContains(t, scheduled[0], "previous_scheduled_at", "a new row replaces no deadline")
			assert.Empty(t, retryLogEntries(t, logs, "automatic retry rescheduled"), "a new row is no reschedule")

			schedule(clock.Now().Add(2 * time.Hour))
			moved := pendingRetry(t, manager, kind)
			require.Equal(t, first.ID, moved.ID, "a reschedule keeps the pending row")
			rescheduled := retryLogEntries(t, logs, "automatic retry rescheduled")
			require.Len(t, rescheduled, 1)
			assert.Equal(t, "INFO", rescheduled[0]["level"])
			assert.Equal(t, kind.String(), rescheduled[0]["kind"])
			assert.Equal(t, first.ID, rescheduled[0]["input_id"])
			assert.Equal(t, formattedDeadline(t, moved), rescheduled[0]["scheduled_at"])
			assert.Equal(t, formattedDeadline(t, first), rescheduled[0]["previous_scheduled_at"])
			assert.NotEqual(t, rescheduled[0]["previous_scheduled_at"], rescheduled[0]["scheduled_at"])
			assert.Len(t, retryLogEntries(t, logs, "automatic retry scheduled"), 1, "a reschedule is no new schedule")
		})
	}
}

// A pending retry whose edit cleared its schedule has no deadline, so the
// reschedule that gives it one states "none" as the deadline it replaced.
func TestAutoRetryLogStatesAClearedDeadlineAsNone(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	clock, _, _, manager, store := newDeadlineFixture(t)
	require.NoError(t, manager.ScheduleAutoRetry(t.Context(), "agent-1", agent.AutoRetrySchedule{Kind: usageLimitRetry, ResetAt: clock.Now().Add(time.Hour)}))
	retry := pendingRetry(t, manager, usageLimitRetry)
	// The store edit leaves the queue manager out, so the cleared row cannot
	// dispatch before the reschedule below.
	snapshot, _, _, err := store.BeginEdit(t.Context(), "agent-1", retry.ID, "client", false)
	require.NoError(t, err)
	_, err = store.Update(t.Context(), UpdateRequest{
		AgentID: "agent-1", InputID: retry.ID, ClientID: "client", ExpectedVersion: snapshot.Items[0].Version,
		Text: snapshot.Items[0].TextPreview, Schedule: ReplaceSchedule(nil),
	})
	require.NoError(t, err)

	require.NoError(t, manager.ScheduleAutoRetry(t.Context(), "agent-1", agent.AutoRetrySchedule{Kind: usageLimitRetry, ResetAt: clock.Now().Add(2 * time.Hour)}))

	rescheduled := retryLogEntries(t, logs, "automatic retry rescheduled")
	require.Len(t, rescheduled, 1)
	assert.Equal(t, retry.ID, rescheduled[0]["input_id"])
	assert.Equal(t, "none", rescheduled[0]["previous_scheduled_at"])
}

// A refused schedule writes no schedule line: the row it describes does not
// exist.
func TestAutoRetryLogStatesNoScheduleForARefusedSchedule(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	_, _, _, manager, _ := newDeadlineFixture(t)

	err := manager.ScheduleAutoRetry(t.Context(), "closed-agent", agent.AutoRetrySchedule{Kind: serverErrorRetry})
	require.ErrorIs(t, err, ErrConflict)

	assert.Empty(t, retryLogEntries(t, logs, "automatic retry scheduled"))
	assert.Empty(t, retryLogEntries(t, logs, "automatic retry rescheduled"))
}

func TestAutoRetryLogStatesWhoRemovedEachRetry(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	clock, _, _, manager, _ := newDeadlineFixture(t)
	scheduleBoth := func() (usage, server SnapshotItem) {
		t.Helper()
		require.NoError(t, manager.ScheduleAutoRetry(t.Context(), "agent-1", agent.AutoRetrySchedule{Kind: usageLimitRetry, ResetAt: clock.Now().Add(time.Hour)}))
		require.NoError(t, manager.ScheduleAutoRetry(t.Context(), "agent-1", agent.AutoRetrySchedule{Kind: serverErrorRetry}))
		return pendingRetry(t, manager, usageLimitRetry), pendingRetry(t, manager, serverErrorRetry)
	}
	removedSince := func(step func()) []map[string]string {
		t.Helper()
		logs.Reset()
		step()
		return retryLogEntries(t, logs, "automatic retry removed")
	}

	usage, server := scheduleBoth()
	removed := removedSince(func() {
		require.NoError(t, manager.CancelAutoRetry(t.Context(), "agent-1", usageLimitRetry, "the usage limit lifted", nil))
	})
	require.Len(t, removed, 1, "a provider cancellation removes its own kind only")
	assert.Equal(t, "INFO", removed[0]["level"])
	assert.Equal(t, "agent-1", removed[0]["agent_id"])
	assert.Equal(t, usageLimitRetry.String(), removed[0]["kind"])
	assert.Equal(t, usage.ID, removed[0]["input_id"])
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED.String(), removed[0]["state"])
	assert.Equal(t, formattedDeadline(t, usage), removed[0]["scheduled_at"])
	assert.Equal(t, "the usage limit lifted", removed[0]["reason"])

	removed = removedSince(func() {
		require.NoError(t, manager.CancelAutoRetry(t.Context(), "agent-1", usageLimitRetry, "the usage limit lifted", nil))
	})
	assert.Empty(t, removed, "a cancellation that finds no pending retry writes nothing")

	removed = removedSince(func() {
		require.NoError(t, manager.CancelAutoRetries(t.Context(), "agent-1", "the context was cleared"))
	})
	require.Len(t, removed, 1)
	assert.Equal(t, server.ID, removed[0]["input_id"])
	assert.Equal(t, serverErrorRetry.String(), removed[0]["kind"])
	assert.Equal(t, "the context was cleared", removed[0]["reason"])

	usage, server = scheduleBoth()
	removed = removedSince(func() {
		require.NoError(t, manager.CancelAutoRetries(t.Context(), "agent-1", "the workspace was archived"))
	})
	require.Len(t, removed, 2, "a lifecycle cleanup removes both kinds, one line each")
	// The lines follow order_index, the order of insertion, and not the
	// snapshot's order, where the sooner server-error deadline comes first.
	assert.Equal(t, []string{usage.ID, server.ID}, []string{removed[0]["input_id"], removed[1]["input_id"]})
	assert.Equal(t, []string{usageLimitRetry.String(), serverErrorRetry.String()}, []string{removed[0]["kind"], removed[1]["kind"]})
	for _, entry := range removed {
		assert.Equal(t, "the workspace was archived", entry["reason"])
	}

	usage, _ = scheduleBoth()
	removed = removedSince(func() {
		_, err := manager.Delete(t.Context(), "agent-1", usage.ID)
		require.NoError(t, err)
	})
	require.Len(t, removed, 1)
	assert.Equal(t, usage.ID, removed[0]["input_id"])
	assert.Equal(t, "the user deleted the retry", removed[0]["reason"])

	removed = removedSince(func() {
		// The pause keeps the input queued until the delete below.
		_, err := manager.Pause(t.Context(), "agent-1", leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL)
		require.NoError(t, err)
		_, err = manager.Enqueue(t.Context(), NewItem{ID: "typed", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hello"})
		require.NoError(t, err)
		_, err = manager.Delete(t.Context(), "agent-1", "typed")
		require.NoError(t, err)
	})
	assert.Empty(t, removed, "the delete of an ordinary input is no retry removal")

	removed = removedSince(func() {
		require.NoError(t, manager.RetireAgent(t.Context(), "agent-1"))
	})
	require.Len(t, removed, 1, "the server-error retry was still pending")
	assert.Equal(t, serverErrorRetry.String(), removed[0]["kind"])
	assert.Equal(t, "the agent closed", removed[0]["reason"])
}

// A cancellation that finds no pending retry writes no removal line, also
// when it resets a grown backoff. A FAILED retry is not pending, so neither a
// provider cancellation nor a lifecycle cleanup removes it.
func TestAutoRetryLogStatesNoRemovalWhenNoRetryIsPending(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	clock, _, _, manager, store := newDeadlineFixture(t)
	failedID := failDueRetry(t, clock, store, `{"error":"overloaded"}`)
	require.Equal(t, int64(20000), storedServerErrorDelay(t, store.db), "the failed dispatch grew the backoff")
	logs.Reset()

	require.NoError(t, manager.CancelAutoRetry(t.Context(), "agent-1", serverErrorRetry, "the turn ended without a retryable failure", nil))
	assert.Equal(t, int64(10000), storedServerErrorDelay(t, store.db), "the cancellation reset the backoff")
	require.NoError(t, manager.CancelAutoRetries(t.Context(), "agent-1", "the context was cleared"))
	require.NoError(t, manager.RetireAgent(t.Context(), "agent-1"))

	assert.Empty(t, retryLogEntries(t, logs, "automatic retry removed"))
	snapshot, err := manager.Snapshot(t.Context(), "agent-1")
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	assert.Equal(t, failedID, snapshot.Items[0].ID)
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_FAILED, snapshot.Items[0].State)
}

// A refused cancellation or delete removes nothing and writes no removal line.
func TestAutoRetryLogStatesNoRemovalForARefusedRequest(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	clock, _, _, manager, _ := newDeadlineFixture(t)
	require.NoError(t, manager.ScheduleAutoRetry(t.Context(), "agent-1", agent.AutoRetrySchedule{Kind: usageLimitRetry, ResetAt: clock.Now().Add(time.Hour)}))
	pending := pendingRetry(t, manager, usageLimitRetry)

	err := manager.CancelAutoRetry(t.Context(), "agent-1", leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, "the turn ended", nil)
	require.ErrorIs(t, err, ErrInvalidInput)
	_, err = manager.Delete(t.Context(), "agent-1", "no-such-input")
	require.ErrorIs(t, err, ErrNotFound)

	assert.Empty(t, retryLogEntries(t, logs, "automatic retry removed"))
	assert.Equal(t, pending, pendingRetry(t, manager, usageLimitRetry), "the refused requests change nothing")
}

// The user can delete a retry whose dispatch failed. Its line states the
// FAILED state, which separates it from a pending retry.
func TestAutoRetryLogStatesTheStateOfADeletedFailedRetry(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	clock, _, _, manager, store := newDeadlineFixture(t)
	failedID := failDueRetry(t, clock, store, `{"error":"overloaded"}`)

	_, err := manager.Delete(t.Context(), "agent-1", failedID)
	require.NoError(t, err)

	removed := retryLogEntries(t, logs, "automatic retry removed")
	require.Len(t, removed, 1)
	assert.Equal(t, failedID, removed[0]["input_id"])
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_FAILED.String(), removed[0]["state"])
	assert.Equal(t, "the user deleted the retry", removed[0]["reason"])
}

func TestAutoRetryLogStatesTheDeliveryOfARetryOnly(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	clock, dispatcher, observer, manager, _ := newDeadlineFixture(t)
	accepted := func() []AcceptedTranscript {
		observer.mu.Lock()
		defer observer.mu.Unlock()
		return append([]AcceptedTranscript(nil), observer.accepted...)
	}

	_, err := manager.Enqueue(t.Context(), NewItem{ID: "typed", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hello"})
	require.NoError(t, err)
	eventuallyDispatches(t, dispatcher, []string{"typed"})
	require.Eventually(t, func() bool { return len(accepted()) == 1 }, eventuallyWait, 10*time.Millisecond)
	_, err = manager.TurnEnded(t.Context(), "agent-1")
	require.NoError(t, err)

	// A reset in the past makes the retry due at once, so it can dispatch
	// before any read of the queue. Its schedule line states its ID.
	require.NoError(t, manager.ScheduleAutoRetry(t.Context(), "agent-1", agent.AutoRetrySchedule{Kind: usageLimitRetry, ResetAt: clock.Now().Add(-time.Hour)}))
	scheduled := retryLogEntries(t, logs, "automatic retry scheduled")
	require.Len(t, scheduled, 1)
	retryID := scheduled[0]["input_id"]
	require.Eventually(t, func() bool { return len(accepted()) == 2 }, eventuallyWait, 10*time.Millisecond)
	delivery := accepted()[1]
	require.Equal(t, retryID, delivery.ID)

	delivered := retryLogEntries(t, logs, "automatic retry delivered")
	require.Len(t, delivered, 1, "the ordinary input writes no delivery line")
	assert.Equal(t, "INFO", delivered[0]["level"])
	assert.Equal(t, "agent-1", delivered[0]["agent_id"])
	assert.Equal(t, usageLimitRetry.String(), delivered[0]["kind"])
	assert.Equal(t, retryID, delivered[0]["input_id"])
	assert.Equal(t, strconv.FormatInt(delivery.Seq, 10), delivered[0]["seq"])
	assert.Empty(t, retryLogEntries(t, logs, "automatic retry removed"), "a delivery is no removal")
}

// A provider owner that a process restart replaced asks for nothing. The queue
// changes nothing and states the dropped request at the debug level.
func TestAutoRetryLogStatesAnIgnoredRequestAtTheDebugLevel(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	clock, _, _, manager, _ := newDeadlineFixture(t)
	require.NoError(t, manager.ScheduleAutoRetry(t.Context(), "agent-1", agent.AutoRetrySchedule{Kind: usageLimitRetry, ResetAt: clock.Now().Add(time.Hour)}))
	pending := pendingRetry(t, manager, usageLimitRetry)
	logs.Reset()
	replaced := &retryTestAdmission{refused: true}

	require.NoError(t, manager.ScheduleAutoRetry(t.Context(), "agent-1", agent.AutoRetrySchedule{
		Kind: usageLimitRetry, ResetAt: clock.Now().Add(2 * time.Hour), Admission: replaced,
	}))
	require.NoError(t, manager.CancelAutoRetry(t.Context(), "agent-1", usageLimitRetry, "the usage limit lifted", replaced))

	ignored := retryLogEntries(t, logs, "automatic retry request ignored")
	require.Len(t, ignored, 2)
	for i, operation := range []string{"schedule", "cancel"} {
		assert.Equal(t, "DEBUG", ignored[i]["level"])
		assert.Equal(t, operation, ignored[i]["operation"])
		assert.Equal(t, usageLimitRetry.String(), ignored[i]["kind"])
		assert.Equal(t, "the provider owner that asked was replaced", ignored[i]["reason"])
	}
	assert.Empty(t, retryLogEntries(t, logs, "automatic retry rescheduled"))
	assert.Empty(t, retryLogEntries(t, logs, "automatic retry removed"))
	assert.Equal(t, pending, pendingRetry(t, manager, usageLimitRetry), "the ignored requests change nothing")

	// The current owner's request is no ignored request.
	current := &retryTestAdmission{}
	require.NoError(t, manager.CancelAutoRetry(t.Context(), "agent-1", usageLimitRetry, "the usage limit lifted", current))
	removed := retryLogEntries(t, logs, "automatic retry removed")
	require.Len(t, removed, 1)
	assert.Equal(t, pending.ID, removed[0]["input_id"])
	assert.Equal(t, "the usage limit lifted", removed[0]["reason"])
	assert.Len(t, retryLogEntries(t, logs, "automatic retry request ignored"), 2)
}
