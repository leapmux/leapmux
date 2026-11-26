package droid

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

const archiveChildID = "52af9b7b-5e82-4932-83c0-a2bf1caa2fcd"

func TestChildArchivePreservesCommittedBlockOrder(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	a.HandleOutput([]byte(droidChildAvailable))
	child, ok := sink.BackgroundTask("child-1")
	require.True(t, ok)
	var record droidArchiveRecord
	require.NoError(t, json.Unmarshal([]byte(`{"type":"message","id":"committed","message":{"role":"assistant","content":[{"type":"text","text":"Before the tool"},{"type":"tool_use","id":"tool-1","name":"Read","input":{"file_path":"note.txt"}},{"type":"text","text":"After the tool"}]}}`), &record))
	tail := droidChildTail{agent: a, sessionID: "child-1"}
	tail.projectMessage(record)
	rows := sink.Child(child.ChildAgentID).Messages()
	require.Len(t, rows, 3)
	assert.Contains(t, string(rows[0].Content), "Before the tool")
	assert.Contains(t, string(rows[1].Content), `"tool_call"`)
	assert.Contains(t, string(rows[2].Content), "After the tool")
}

type droidPromptFailureSink struct {
	*agenttest.Sink
	failNext bool
}

func (s *droidPromptFailureSink) PersistChildPrompt(childAgentID, prompt string) error {
	if s.failNext {
		s.failNext = false
		return errors.New("injected child prompt write failure")
	}
	return s.Sink.PersistChildPrompt(childAgentID, prompt)
}

func droidArchiveHarness(t *testing.T) (*Agent, *agenttest.Sink, *quartz.Mock, string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv(droidHomeEnv, home)
	work := filepath.Join(home, "workspace")
	require.NoError(t, os.MkdirAll(work, 0o700))
	a, sink, _ := newSteerAgent(t)
	a.workingDir = work
	clock := testutil.NewQuartzMock(t)
	a.clock = clock
	dir := filepath.Join(home, ".factory", "sessions", droidSanitizeCwd(work))
	require.NoError(t, os.MkdirAll(dir, 0o700))
	return a, sink, clock, dir, work
}

func droidArchiveStart(id, work string) string {
	return fmt.Sprintf(`{"type":"session_start","id":%q,"cwd":%q}`, id, work)
}

func droidArchiveChildAvailable(id, toolUseID string) string {
	return fmt.Sprintf(`{"type":"notification","method":"droid.session_notification","params":{"sessionId":"main-session","notification":{"type":"child_session_available","childSessionId":%q,"toolUseId":%q,"subagentType":"explorer","description":"Inspect the note"}}}`, id, toolUseID)
}

func appendDroidArchive(t *testing.T, path string, records ...string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	for _, record := range records {
		_, err = file.WriteString(record + "\n")
		require.NoError(t, err)
	}
	require.NoError(t, file.Close())
}

func advanceDroidArchive(t *testing.T, clock *quartz.Mock) {
	t.Helper()
	clock.Advance(250 * time.Millisecond).MustWait(testutil.DeadlineContext(t))
}

func childArchiveRows(sink *agenttest.Sink, childID string) []agenttest.Message {
	row, ok := sink.BackgroundTask(childID)
	if !ok || row.ChildAgentID == "" {
		return nil
	}
	return sink.Child(row.ChildAgentID).Messages()
}

func archiveRowsContain(rows []agenttest.Message, marker string) bool {
	for _, row := range rows {
		if strings.Contains(string(row.Content), marker) {
			return true
		}
	}
	return false
}

func TestDroidRepeatedChildAnnouncementDoesNotInvertTailLocks(t *testing.T) {
	a, sink, _, _, _ := droidArchiveHarness(t)
	a.HandleOutput([]byte(droidTaskCall))
	announcement := []byte(droidArchiveChildAvailable(archiveChildID, "task-1"))
	a.HandleOutput(announcement)
	a.tailMu.Lock()
	old := a.childTails[archiveChildID]
	a.tailMu.Unlock()
	require.NotNil(t, old)
	row, found := sink.BackgroundTask(archiveChildID)
	require.True(t, found)
	before := len(sink.Child(row.ChildAgentID).Messages())

	old.mu.Lock()
	locked := true
	releaseOld := func() {
		if locked {
			old.mu.Unlock()
			locked = false
		}
	}
	defer releaseOld()
	entered := make(chan struct{})
	releaseHook := make(chan struct{})
	var hookOnce sync.Once
	release := func() { hookOnce.Do(func() { close(releaseHook) }) }
	defer release()
	a.beforeChildTailStateRead = func() {
		close(entered)
		<-releaseHook
	}
	duplicateDone := make(chan struct{})
	go func() {
		a.HandleOutput(announcement)
		close(duplicateDone)
	}()
	ctx := testutil.DeadlineContext(t)
	blocked := false
	select {
	case <-duplicateDone:
		// An idempotent replay has no tail state to read.
	case <-entered:
		probeStarted := make(chan struct{})
		probeAcquired := make(chan struct{})
		go func() {
			close(probeStarted)
			a.dispatchMu.Lock()
			close(probeAcquired)
			a.dispatchMu.Unlock()
		}()
		<-probeStarted
		release()
		select {
		case <-probeAcquired:
		case <-ctx.Done():
			blocked = true
		}
	case <-ctx.Done():
		t.Fatal("the repeated announcement neither returned nor reached the tail state")
	}
	releaseOld()
	select {
	case <-duplicateDone:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the repeated announcement did not finish after the tail lock was released")
	}
	assert.False(t, blocked, "the tail must acquire dispatchMu while a replay waits for its state")
	assert.Equal(t, before, len(sink.Child(row.ChildAgentID).Messages()),
		"a repeated announcement cannot add a second child prompt")
	a.tailMu.Lock()
	assert.Same(t, old, a.childTails[archiveChildID], "a replay keeps the current tail")
	a.tailMu.Unlock()
}

func TestDroidStopPreventsDeferredChildTail(t *testing.T) {
	a, _, _, _, _ := droidArchiveHarness(t)
	a.HandleOutput([]byte(droidTaskCall))
	entered := make(chan struct{})
	releaseStart := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseStart) }) }
	defer release()
	a.beforeChildTailStart = func() {
		close(entered)
		<-releaseStart
	}
	announcementDone := make(chan struct{})
	go func() {
		a.HandleOutput([]byte(droidArchiveChildAvailable(archiveChildID, "task-1")))
		close(announcementDone)
	}()
	ctx := testutil.DeadlineContext(t)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the child announcement did not reach the deferred tail start")
	}
	stopDone := make(chan struct{})
	go func() {
		a.Stop()
		close(stopDone)
	}()
	testutil.RequireEventually(t, func() bool {
		a.Mu.Lock()
		stopped := a.stopped
		a.Mu.Unlock()
		return stopped
	}, "Stop marks the agent before the deferred tail starts")
	a.SimulateExitForTest()
	select {
	case <-stopDone:
	case <-ctx.Done():
		t.Fatal("Stop did not finish after the fake process exited")
	}
	release()
	select {
	case <-announcementDone:
	case <-ctx.Done():
		t.Fatal("the child announcement did not finish after Stop")
	}
	a.tailMu.Lock()
	tail := a.childTails[archiveChildID]
	a.tailMu.Unlock()
	assert.Nil(t, tail, "Stop prevents a tail from starting after it snapshots existing readers")
}

func TestDroidStopWaitsForPublishedTailToStart(t *testing.T) {
	a, _, _, _, _ := droidArchiveHarness(t)
	a.HandleOutput([]byte(droidTaskCall))
	entered := make(chan struct{})
	releaseStart := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseStart) }) }
	defer release()
	a.beforeChildTailRun = func() {
		close(entered)
		<-releaseStart
	}
	announcementDone := make(chan struct{})
	go func() {
		a.HandleOutput([]byte(droidArchiveChildAvailable(archiveChildID, "task-1")))
		close(announcementDone)
	}()
	ctx := testutil.DeadlineContext(t)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the tail did not reach the map-publish/start boundary")
	}
	// Stop can snapshot the published tail safely only after its reader starts.
	locked := a.tailMu.TryLock()
	if locked {
		a.tailMu.Unlock()
	}
	assert.False(t, locked, "the tail map lock must cover reader startup")
	stopDone := make(chan struct{})
	go func() {
		a.Stop()
		close(stopDone)
	}()
	a.SimulateExitForTest()
	release()
	select {
	case <-announcementDone:
	case <-ctx.Done():
		t.Fatal("the child announcement did not finish after startup resumed")
	}
	select {
	case <-stopDone:
	case <-ctx.Done():
		t.Fatal("Stop did not finish after the tail started")
	}
}

func TestDroidRepeatedChildAnnouncementKeepsOnePrompt(t *testing.T) {
	a, sink, _, _, _ := droidArchiveHarness(t)
	a.HandleOutput([]byte(droidTaskCall))
	announcement := []byte(droidArchiveChildAvailable(archiveChildID, "task-1"))
	a.HandleOutput(announcement)
	row, found := sink.BackgroundTask(archiveChildID)
	require.True(t, found)
	require.NotEmpty(t, row.ChildAgentID)
	before := len(sink.Child(row.ChildAgentID).Messages())
	a.tailMu.Lock()
	tail := a.childTails[archiveChildID]
	a.tailMu.Unlock()
	require.NotNil(t, tail)

	a.HandleOutput(announcement)
	assert.Equal(t, before, len(sink.Child(row.ChildAgentID).Messages()),
		"a native replay cannot append another child prompt")
	a.tailMu.Lock()
	assert.Same(t, tail, a.childTails[archiveChildID])
	a.tailMu.Unlock()
}

func TestDroidFinishedChildAnnouncementDoesNotRestartTail(t *testing.T) {
	a, sink, clock, dir, work := droidArchiveHarness(t)
	path := filepath.Join(dir, archiveChildID+".jsonl")
	appendDroidArchive(t, path,
		droidArchiveStart(archiveChildID, work),
		`{"type":"agent_turn_outcome","turnId":"turn-1","reason":"completed","resultKind":"text"}`,
	)
	a.HandleOutput([]byte(droidTaskCall))
	announcement := []byte(droidArchiveChildAvailable(archiveChildID, "task-1"))
	a.HandleOutput(announcement)
	advanceDroidArchive(t, clock)
	testutil.RequireEventually(t, func() bool {
		row, found := sink.BackgroundTask(archiveChildID)
		return found && row.Status == bgtask.StatusSucceeded
	}, "the archive closes the first child turn")
	a.tailMu.Lock()
	finished := a.childTails[archiveChildID]
	a.tailMu.Unlock()
	require.NotNil(t, finished)
	finished.mu.Lock()
	require.True(t, finished.finished)
	finished.mu.Unlock()

	a.HandleOutput(announcement)
	a.tailMu.Lock()
	assert.Same(t, finished, a.childTails[archiveChildID],
		"a repeated announcement cannot start an idle reader at the old file offset")
	a.tailMu.Unlock()
}

func TestDroidChildAnnouncementRejectsReusedSessionWithDifferentTool(t *testing.T) {
	a, sink, clock, dir, work := droidArchiveHarness(t)
	path := filepath.Join(dir, archiveChildID+".jsonl")
	appendDroidArchive(t, path,
		droidArchiveStart(archiveChildID, work),
		`{"type":"agent_turn_outcome","turnId":"turn-1","reason":"completed","resultKind":"text"}`,
	)
	a.HandleOutput([]byte(droidTaskCall))
	a.HandleOutput([]byte(droidArchiveChildAvailable(archiveChildID, "task-1")))
	advanceDroidArchive(t, clock)
	testutil.RequireEventually(t, func() bool {
		row, found := sink.BackgroundTask(archiveChildID)
		return found && row.Status == bgtask.StatusSucceeded
	}, "the first child announcement reaches its final result")
	row, found := sink.BackgroundTask(archiveChildID)
	require.True(t, found)
	a.tailMu.Lock()
	firstTail := a.childTails[archiveChildID]
	a.tailMu.Unlock()
	require.NotNil(t, firstTail)

	a.HandleOutput([]byte(droidArchiveChildAvailable(archiveChildID, "task-2")))
	updated, found := sink.BackgroundTask(archiveChildID)
	require.True(t, found)
	assert.Equal(t, row.ChildAgentID, updated.ChildAgentID,
		"a different Task call cannot claim the old native child session")
	assert.Equal(t, bgtask.StatusSucceeded, updated.Status)
	a.tailMu.Lock()
	assert.Same(t, firstTail, a.childTails[archiveChildID])
	a.tailMu.Unlock()
}

func TestDroidChildAnnouncementRetriesFailedPromptOnReplay(t *testing.T) {
	a, sink, _, _, _ := droidArchiveHarness(t)
	a.sink = agent.NewProviderServices(&droidPromptFailureSink{Sink: sink, failNext: true})
	a.HandleOutput([]byte(droidTaskCall))
	announcement := []byte(droidArchiveChildAvailable(archiveChildID, "task-1"))
	a.HandleOutput(announcement)
	first, found := sink.BackgroundTask(archiveChildID)
	require.True(t, found, "EnsureChildAgent creates the child before the prompt write")
	require.NotEmpty(t, first.ChildAgentID)
	assert.Empty(t, sink.Child(first.ChildAgentID).Messages())
	assert.Empty(t, a.childAnnouncements, "a failed prompt does not complete registration")
	a.tailMu.Lock()
	assert.Nil(t, a.childTails[archiveChildID])
	a.tailMu.Unlock()

	a.HandleOutput(announcement)
	second, found := sink.BackgroundTask(archiveChildID)
	require.True(t, found)
	assert.Equal(t, first.ChildAgentID, second.ChildAgentID)
	assert.Equal(t, bgtask.StatusRunning, second.Status)
	rows := sink.Child(second.ChildAgentID).Messages()
	require.Len(t, rows, 1)
	assert.Contains(t, string(rows[0].Content), "Read the child note.")
	assert.Equal(t, droidChildAnnouncementID{parentSessionID: "main-session", toolUseID: "task-1"},
		a.childAnnouncements[archiveChildID])
	a.tailMu.Lock()
	tail := a.childTails[archiveChildID]
	a.tailMu.Unlock()
	require.NotNil(t, tail)

	a.HandleOutput(announcement)
	assert.Len(t, sink.Child(second.ChildAgentID).Messages(), 1)
	a.tailMu.Lock()
	assert.Same(t, tail, a.childTails[archiveChildID])
	a.tailMu.Unlock()
}

func TestDroidChildTailContinuesAtSavedOffset(t *testing.T) {
	a, sink, clock, dir, work := droidArchiveHarness(t)
	path := filepath.Join(dir, archiveChildID+".jsonl")
	appendDroidArchive(t, path,
		droidArchiveStart(archiveChildID, work),
		`{"type":"message","id":"assistant-first","message":{"role":"assistant","content":[{"type":"text","text":"ARCHIVE_FIRST_TURN"}]}}`,
		`{"type":"agent_turn_outcome","turnId":"turn-1","reason":"completed","resultKind":"text"}`,
	)
	a.HandleOutput([]byte(droidTaskCall))
	a.HandleOutput([]byte(droidArchiveChildAvailable(archiveChildID, "task-1")))
	advanceDroidArchive(t, clock)
	testutil.RequireEventually(t, func() bool {
		return archiveRowsContain(childArchiveRows(sink, archiveChildID), "ARCHIVE_FIRST_TURN")
	}, "the first child turn appears")
	a.tailMu.Lock()
	first := a.childTails[archiveChildID]
	a.tailMu.Unlock()
	require.NotNil(t, first)
	first.mu.Lock()
	require.True(t, first.finished)
	firstOffset := first.offset
	firstFileInfo := first.fileInfo
	firstHeaderSeen := first.headerSeen
	first.mu.Unlock()
	require.Positive(t, firstOffset)
	require.NotNil(t, firstFileInfo)
	require.True(t, firstHeaderSeen)

	appendDroidArchive(t, path,
		`{"type":"message","id":"assistant-second","message":{"role":"assistant","content":[{"type":"text","text":"ARCHIVE_SECOND_TURN"}]}}`,
		`{"type":"agent_turn_outcome","turnId":"turn-2","reason":"completed","resultKind":"text"}`,
	)
	// Continuation calls reuse the saved offset through ensureChildTail.
	a.startChildTail(archiveChildID)
	a.tailMu.Lock()
	second := a.childTails[archiveChildID]
	a.tailMu.Unlock()
	require.NotNil(t, second)
	require.NotSame(t, first, second)
	second.mu.Lock()
	assert.True(t, second.headerSeen)
	assert.True(t, os.SameFile(firstFileInfo, second.fileInfo))
	assert.GreaterOrEqual(t, second.offset, firstOffset)
	second.mu.Unlock()
	advanceDroidArchive(t, clock)
	testutil.RequireEventually(t, func() bool {
		return archiveRowsContain(childArchiveRows(sink, archiveChildID), "ARCHIVE_SECOND_TURN")
	}, "the next child turn appears after the saved offset")
	firstCount, secondCount := 0, 0
	for _, row := range childArchiveRows(sink, archiveChildID) {
		text := string(row.Content)
		if strings.Contains(text, "ARCHIVE_FIRST_TURN") {
			firstCount++
		}
		if strings.Contains(text, "ARCHIVE_SECOND_TURN") {
			secondCount++
		}
	}
	assert.Equal(t, 1, firstCount)
	assert.Equal(t, 1, secondCount)
}

func TestDroidChildTailReadsTwoTurnsInOneChunk(t *testing.T) {
	a, sink, clock, dir, work := droidArchiveHarness(t)
	path := filepath.Join(dir, archiveChildID+".jsonl")
	appendDroidArchive(t, path,
		droidArchiveStart(archiveChildID, work),
		`{"type":"message","id":"assistant-first","message":{"role":"assistant","content":[{"type":"text","text":"ARCHIVE_FIRST_TURN"}]}}`,
		`{"type":"agent_turn_outcome","turnId":"turn-1","reason":"completed","resultKind":"text"}`,
		`{"type":"message","id":"assistant-second","message":{"role":"assistant","content":[{"type":"text","text":"ARCHIVE_SECOND_TURN"}]}}`,
		`{"type":"agent_turn_outcome","turnId":"turn-2","reason":"completed","resultKind":"text"}`,
	)
	a.HandleOutput([]byte(droidTaskCall))
	a.HandleOutput([]byte(droidArchiveChildAvailable(archiveChildID, "task-1")))
	testutil.RequireEventually(t, func() bool {
		a.tailMu.Lock()
		tail := a.childTails[archiveChildID]
		a.tailMu.Unlock()
		if tail == nil {
			return false
		}
		tail.mu.Lock()
		defer tail.mu.Unlock()
		return tail.finished
	}, "the first archive outcome stops its reader")
	a.tailMu.Lock()
	first := a.childTails[archiveChildID]
	a.tailMu.Unlock()
	first.mu.Lock()
	firstOffset := first.offset
	first.mu.Unlock()
	a.startChildTail(archiveChildID)
	advanceDroidArchive(t, clock)
	testutil.RequireEventually(t, func() bool {
		return archiveRowsContain(childArchiveRows(sink, archiveChildID), "ARCHIVE_SECOND_TURN")
	}, "a later reader finds the second turn in the first read chunk")
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Less(t, firstOffset, info.Size(), "the first reader keeps unread bytes for the next turn")
	firstCount, secondCount := 0, 0
	for _, row := range childArchiveRows(sink, archiveChildID) {
		if strings.Contains(string(row.Content), "ARCHIVE_FIRST_TURN") {
			firstCount++
		}
		if strings.Contains(string(row.Content), "ARCHIVE_SECOND_TURN") {
			secondCount++
		}
	}
	assert.Equal(t, 1, firstCount)
	assert.Equal(t, 1, secondCount)
}

func bindDroidArchiveChild(t *testing.T, root *Agent) *Agent {
	t.Helper()
	child, _, _ := newSteerAgent(t)
	child.Mu.Lock()
	child.sessionID = archiveChildID
	child.Mu.Unlock()
	ready := make(chan struct{})
	close(ready)
	root.childConnMu.Lock()
	root.childConns = map[string]*droidChildConnection{archiveChildID: {ready: ready, agent: child}}
	root.childConnMu.Unlock()
	return child
}

func droidEarlySendArchive(t *testing.T, sends int) (*Agent, *agenttest.Sink, *quartz.Mock, string) {
	t.Helper()
	a, sink, clock, dir, work := droidArchiveHarness(t)
	path := filepath.Join(dir, archiveChildID+".jsonl")
	appendDroidArchive(t, path,
		droidArchiveStart(archiveChildID, work),
		`{"type":"message","id":"assistant-first","message":{"role":"assistant","content":[{"type":"text","text":"ARCHIVE_FIRST_TURN"}]}}`,
	)
	a.HandleOutput([]byte(droidTaskCall))
	a.HandleOutput([]byte(droidArchiveChildAvailable(archiveChildID, "task-1")))
	testutil.RequireEventually(t, func() bool {
		return archiveRowsContain(childArchiveRows(sink, archiveChildID), "ARCHIVE_FIRST_TURN")
	}, "the first child message appears before the archive outcome")
	child := bindDroidArchiveChild(t, a)
	a.disarmChildTurn(archiveChildID)
	for index := range sends {
		if index > 0 {
			// Native idle can reach the bound child before the archive ticker runs.
			child.endTurn()
		}
		require.NoError(t, agenttest.SendChildInput(a, archiveChildID, agent.ChildInput{Content: fmt.Sprintf("follow-up %d", index+1)}))
	}
	records := []string{`{"type":"agent_turn_outcome","turnId":"turn-1","reason":"completed","resultKind":"text"}`}
	for index := range sends {
		records = append(records,
			fmt.Sprintf(`{"type":"message","id":"assistant-%d","message":{"role":"assistant","content":[{"type":"text","text":"ARCHIVE_FOLLOWUP_%d"}]}}`, index+2, index+1),
			fmt.Sprintf(`{"type":"agent_turn_outcome","turnId":"turn-%d","reason":"completed","resultKind":"text"}`, index+2),
		)
	}
	appendDroidArchive(t, path, records...)
	return a, sink, clock, path
}

func TestDroidChildTailStopsAtInitialOutcomeAfterBoundChildSend(t *testing.T) {
	a, sink, clock, _ := droidEarlySendArchive(t, 1)
	advanceDroidArchive(t, clock)
	var tail *droidChildTail
	testutil.RequireEventually(t, func() bool {
		a.tailMu.Lock()
		tail = a.childTails[archiveChildID]
		a.tailMu.Unlock()
		if tail == nil {
			return false
		}
		tail.mu.Lock()
		defer tail.mu.Unlock()
		return tail.finished
	}, "the archive reader stops after the initial background turn outcome")
	select {
	case <-tail.stopped:
	default:
		t.Fatal("tail.stopped must be closed after SendChildInput stops the tail")
	}
	assert.False(t, archiveRowsContain(childArchiveRows(sink, archiveChildID), "ARCHIVE_FOLLOWUP_1"),
		"the bound child stream owns follow-up rows so the archive never duplicates them")
	row, ok := sink.BackgroundTask(archiveChildID)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status,
		"the delayed initial archive outcome must not close an active bound child turn")
}

func TestDroidChildHasActiveBoundConnectionAndFeedPreservesRunningRow(t *testing.T) {
	t.Run("childHasActiveBoundConnection covers all connection states", func(t *testing.T) {
		t.Parallel()
		root, _, _ := newSteerAgent(t)
		assert.False(t, root.childHasActiveBoundConnection(archiveChildID), "nil connection returns false")

		// Unready connection (open ready channel).
		root.childConnMu.Lock()
		root.childConns = map[string]*droidChildConnection{
			archiveChildID: {ready: make(chan struct{})},
		}
		root.childConnMu.Unlock()
		assert.False(t, root.childHasActiveBoundConnection(archiveChildID), "unready connection returns false")

		// Ready connection with nil agent.
		closedReady := make(chan struct{})
		close(closedReady)
		root.childConnMu.Lock()
		root.childConns[archiveChildID] = &droidChildConnection{ready: closedReady, agent: nil}
		root.childConnMu.Unlock()
		assert.False(t, root.childHasActiveBoundConnection(archiveChildID), "ready connection with nil agent returns false")

		// Ready connection with idle agent (turnActive == false).
		child, _, _ := newSteerAgent(t)
		root.childConnMu.Lock()
		root.childConns[archiveChildID] = &droidChildConnection{ready: closedReady, agent: child}
		root.childConnMu.Unlock()
		assert.False(t, root.childHasActiveBoundConnection(archiveChildID), "inactive bound child turn returns false")

		// Ready connection with active agent (turnActive == true).
		child.startTurn()
		assert.True(t, root.childHasActiveBoundConnection(archiveChildID), "active bound child turn returns true")
	})

	t.Run("tail.feed preserves running row on agent_turn_outcome while bound child turn is active", func(t *testing.T) {
		a, sink, _, _, work := droidArchiveHarness(t)
		a.HandleOutput([]byte(droidTaskCall))
		a.HandleOutput([]byte(droidArchiveChildAvailable(archiveChildID, "task-1")))
		child := bindDroidArchiveChild(t, a)
		child.startTurn()
		require.True(t, a.childHasActiveBoundConnection(archiveChildID))

		tail := newDroidChildTail(a, archiveChildID, "", nil, nil)
		tail.workDir = work
		tail.feed([]byte(droidArchiveStart(archiveChildID, work)))
		require.True(t, tail.headerSeen)

		tail.feed([]byte(`{"type":"agent_turn_outcome","turnId":"turn-1","reason":"completed","resultKind":"text"}`))
		assert.True(t, tail.finished)
		row, ok := sink.BackgroundTask(archiveChildID)
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusRunning, row.Status,
			"agent_turn_outcome must keep the row running when a bound child turn is active")
		rows := childArchiveRows(sink, archiveChildID)
		require.NotEmpty(t, rows)
		assert.True(t, rows[len(rows)-1].TurnEnd, "the initial turn-end divider must still persist")

		// Contrast: once the bound child turn ends, a tail.feed outcome closes the row.
		child.endTurn()
		require.False(t, a.childHasActiveBoundConnection(archiveChildID))
		tail2 := newDroidChildTail(a, archiveChildID, "", nil, nil)
		tail2.workDir = work
		tail2.feed([]byte(droidArchiveStart(archiveChildID, work)))
		tail2.feed([]byte(`{"type":"agent_turn_outcome","turnId":"turn-2","reason":"completed","resultKind":"text"}`))
		assert.True(t, tail2.finished)
		row, ok = sink.BackgroundTask(archiveChildID)
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	})
}

func TestDroidChildTailStopWithActiveBoundChild(t *testing.T) {
	a, sink, clock, _ := droidEarlySendArchive(t, 1)
	a.childConnMu.Lock()
	child := a.childConns[archiveChildID].agent
	a.childConnMu.Unlock()
	require.NotNil(t, child)
	stopped := make(chan struct{})
	go func() {
		a.Stop()
		close(stopped)
	}()
	a.SimulateExitForTest()
	child.SimulateExitForTest()
	select {
	case <-stopped:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("Stop did not finish with an active bound child")
	}
	advanceDroidArchive(t, clock)
	assert.False(t, archiveRowsContain(childArchiveRows(sink, archiveChildID), "ARCHIVE_FOLLOWUP_1"),
		"a stopped tail cannot add a late child row")
}

func TestDroidChildTailKeepsPartialNextTurnAfterOutcome(t *testing.T) {
	a, sink, clock, dir, work := droidArchiveHarness(t)
	path := filepath.Join(dir, archiveChildID+".jsonl")
	appendDroidArchive(t, path,
		droidArchiveStart(archiveChildID, work),
		`{"type":"message","id":"assistant-first","message":{"role":"assistant","content":[{"type":"text","text":"ARCHIVE_FIRST_TURN"}]}}`,
		`{"type":"agent_turn_outcome","turnId":"turn-1","reason":"completed","resultKind":"text"}`,
	)
	second := `{"type":"message","id":"assistant-second","message":{"role":"assistant","content":[{"type":"text","text":"ARCHIVE_PARTIAL_SECOND_TURN"}]}}`
	cut := len(second) / 2
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = file.WriteString(second[:cut])
	require.NoError(t, err)
	require.NoError(t, file.Close())
	a.HandleOutput([]byte(droidTaskCall))
	a.HandleOutput([]byte(droidArchiveChildAvailable(archiveChildID, "task-1")))
	testutil.RequireEventually(t, func() bool {
		a.tailMu.Lock()
		tail := a.childTails[archiveChildID]
		a.tailMu.Unlock()
		if tail == nil {
			return false
		}
		tail.mu.Lock()
		defer tail.mu.Unlock()
		return tail.finished
	}, "the first outcome stops before the partial next record")
	appendDroidArchive(t, path, second[cut:],
		`{"type":"agent_turn_outcome","turnId":"turn-2","reason":"completed","resultKind":"text"}`)
	a.startChildTail(archiveChildID)
	advanceDroidArchive(t, clock)
	testutil.RequireEventually(t, func() bool {
		return archiveRowsContain(childArchiveRows(sink, archiveChildID), "ARCHIVE_PARTIAL_SECOND_TURN")
	}, "the next tail reads the whole split JSONL record")
}

func TestDroidChildArchiveProjectsLiveRows(t *testing.T) {
	a, sink, clock, dir, work := droidArchiveHarness(t)
	path := filepath.Join(dir, archiveChildID+".jsonl")
	appendDroidArchive(t, path,
		droidArchiveStart(archiveChildID, work),
		`{"type":"message","id":"assistant-1","message":{"role":"assistant","content":[{"type":"text","text":"ARCHIVE_CHILD_EARLY"},{"type":"tool_use","id":"read-1","name":"Read","input":{"file_path":"/note.txt"}}]}}`,
		`{"type":"message","id":"result-1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"read-1","is_error":false,"content":"ARCHIVE_READ_MARKER"}]}}`,
	)
	a.HandleOutput([]byte(droidTaskCall))
	a.HandleOutput([]byte(droidArchiveChildAvailable(archiveChildID, "task-1")))
	rootCount := len(sink.Messages())
	advanceDroidArchive(t, clock)
	testutil.RequireEventually(t, func() bool {
		return archiveRowsContain(childArchiveRows(sink, archiveChildID), "ARCHIVE_READ_MARKER")
	}, "the native child archive supplies the tool result while the child still runs")
	rows := childArchiveRows(sink, archiveChildID)
	require.Len(t, rows, 4, "prompt, early answer, tool call, and tool result stay separate")
	assert.Contains(t, string(rows[1].Content), "ARCHIVE_CHILD_EARLY")
	assert.Contains(t, string(rows[2].Content), `"name":"Read"`)
	assert.Contains(t, string(rows[3].Content), "ARCHIVE_READ_MARKER")
	assert.Equal(t, rootCount, len(sink.Messages()), "child archive records never enter the root")
	row, ok := sink.BackgroundTask(archiveChildID)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status)

	appendDroidArchive(t, path,
		`{"type":"message","id":"assistant-2","message":{"role":"assistant","content":[{"type":"text","text":"ARCHIVE_CHILD_FINAL"}]}}`,
		`{"type":"agent_turn_outcome","turnId":"turn-1","reason":"completed","resultKind":"text"}`,
	)
	advanceDroidArchive(t, clock)
	testutil.RequireEventually(t, func() bool {
		item, exists := sink.BackgroundTask(archiveChildID)
		return exists && item.Status == bgtask.StatusSucceeded && archiveRowsContain(childArchiveRows(sink, archiveChildID), "ARCHIVE_CHILD_FINAL")
	}, "the final native record closes only the child")
}

func TestDroidChildArchiveWaitsForACompleteLine(t *testing.T) {
	a, sink, clock, dir, work := droidArchiveHarness(t)
	path := filepath.Join(dir, archiveChildID+".jsonl")
	appendDroidArchive(t, path, droidArchiveStart(archiveChildID, work))
	a.HandleOutput([]byte(droidTaskCall))
	a.HandleOutput([]byte(droidArchiveChildAvailable(archiveChildID, "task-1")))
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = file.WriteString(`{"type":"message","id":"assistant-partial","message":{"role":"assistant","content":[{"type":"text","text":"ARCHIVE_PARTIAL`)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	advanceDroidArchive(t, clock)
	assert.False(t, archiveRowsContain(childArchiveRows(sink, archiveChildID), "ARCHIVE_PARTIAL"), "an incomplete line produces no row")
	file, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = file.WriteString(`_COMPLETE"}]}}` + "\n")
	require.NoError(t, err)
	require.NoError(t, file.Close())
	advanceDroidArchive(t, clock)
	testutil.RequireEventually(t, func() bool {
		return archiveRowsContain(childArchiveRows(sink, archiveChildID), "ARCHIVE_PARTIAL_COMPLETE")
	}, "the completed line appears once after the next write")
	rows := childArchiveRows(sink, archiveChildID)
	count := 0
	for _, row := range rows {
		if strings.Contains(string(row.Content), "ARCHIVE_PARTIAL_COMPLETE") {
			count++
		}
	}
	assert.Equal(t, 1, count)
}

func TestDroidChildArchiveValidatesIdentityAndReadOffset(t *testing.T) {
	a, sink, clock, dir, work := droidArchiveHarness(t)
	wrongPath := filepath.Join(dir, archiveChildID+".jsonl")
	appendDroidArchive(t, wrongPath,
		droidArchiveStart("d43e95b8-2c61-4ce8-8765-dbecde5fa3a1", work),
		`{"type":"message","id":"wrong","message":{"role":"assistant","content":[{"type":"text","text":"WRONG_CHILD_MARKER"}]}}`,
	)
	a.HandleOutput([]byte(droidArchiveChildAvailable(archiveChildID, "task-1")))
	advanceDroidArchive(t, clock)
	assert.False(t, archiveRowsContain(childArchiveRows(sink, archiveChildID), "WRONG_CHILD_MARKER"), "the file header must match the announced child")

	const secondID = "971553e6-da26-45b3-9702-909b2fd4d2c0"
	secondPath := filepath.Join(dir, secondID+".jsonl")
	padding := strings.Repeat("x", (1<<20)+128)
	appendDroidArchive(t, secondPath,
		droidArchiveStart(secondID, work),
		fmt.Sprintf(`{"type":"message","id":"context-padding","message":{"role":"user","content":[{"type":"text","text":%q}]}}`, padding),
		`{"type":"message","id":"assistant-valid","message":{"role":"assistant","content":[{"type":"text","text":"AFTER_READ_CAP_MARKER"}]}}`,
	)
	a.HandleOutput([]byte(droidArchiveChildAvailable(secondID, "task-2")))
	advanceDroidArchive(t, clock)
	testutil.RequireEventually(t, func() bool {
		return archiveRowsContain(childArchiveRows(sink, secondID), "AFTER_READ_CAP_MARKER")
	}, "the tail continues after its first 1 MiB read")
	advanceDroidArchive(t, clock)
	rows := childArchiveRows(sink, secondID)
	count := 0
	for _, row := range rows {
		if strings.Contains(string(row.Content), "AFTER_READ_CAP_MARKER") {
			count++
		}
	}
	assert.Equal(t, 1, count, "a second poll does not repeat an earlier record")
}

func TestDroidChildTailInvalidArchiveClosesOpenChildRow(t *testing.T) {
	a, sink, clock, dir, _ := droidArchiveHarness(t)
	path := filepath.Join(dir, archiveChildID+".jsonl")
	appendDroidArchive(t, path, `{not valid json`)

	a.HandleOutput([]byte(droidTaskCall))
	a.HandleOutput([]byte(droidArchiveChildAvailable(archiveChildID, "task-1")))
	advanceDroidArchive(t, clock)

	testutil.RequireEventually(t, func() bool {
		row, ok := sink.BackgroundTask(archiveChildID)
		return ok && row.Status == bgtask.StatusEndedWithUnknownOutcome && !a.ActiveChildTurnState(archiveChildID).Active
	}, "invalid child archive must close open child row and disarm childTurns")
}

func TestDroidArchiveSkipIfUnchangedAndIdentityChecks(t *testing.T) {
	t.Parallel()

	newArchive := func(t *testing.T, content string) (dataRoot, work, archivePath string) {
		t.Helper()
		home := t.TempDir()
		work = filepath.Join(home, "workspace")
		require.NoError(t, os.MkdirAll(work, 0o700))
		dataRoot = filepath.Join(home, ".factory")
		sessionsDir := filepath.Join(dataRoot, droidSessionsDirName, droidSanitizeCwd(work))
		require.NoError(t, os.MkdirAll(sessionsDir, 0o700))
		archivePath = filepath.Join(sessionsDir, archiveChildID+droidSessionSuffix)
		require.NoError(t, os.WriteFile(archivePath, []byte(content), 0o600))
		return dataRoot, work, archivePath
	}

	t.Run("withValidatedDroidArchive skips callback when skipIfUnchanged returns true", func(t *testing.T) {
		t.Parallel()
		dataRoot, work, _ := newArchive(t, `{"type":"session_start"}`+"\n")
		skipCalled := false
		fnCalled := false
		err := withValidatedDroidArchive(dataRoot, work, archiveChildID, func(os.FileInfo) bool {
			skipCalled = true
			return true
		}, func(*os.File, os.FileInfo) error {
			fnCalled = true
			return nil
		})
		require.NoError(t, err)
		assert.True(t, skipCalled)
		assert.False(t, fnCalled, "callback must not run when skipIfUnchanged returns true")
	})

	t.Run("tail.read returns nil on unchanged file and rejects same-size replacement", func(t *testing.T) {
		t.Parallel()
		initial := `{"type":"session_start","id":"1"}` + "\n"
		dataRoot, work, archivePath := newArchive(t, initial)
		tail := &droidChildTail{dataRoot: dataRoot, workDir: work, sessionID: archiveChildID}

		chunk, err := tail.read()
		require.NoError(t, err)
		require.Equal(t, initial, string(chunk))
		tail.offset = int64(len(chunk))
		require.NotNil(t, tail.fileInfo)

		// Unchanged file at EOF skips opening and returns (nil, nil).
		chunk, err = tail.read()
		require.NoError(t, err)
		assert.Nil(t, chunk)

		// Atomic replacement with a new file of identical byte size must not skip.
		replacement := `{"type":"session_start","id":"2"}` + "\n"
		require.Len(t, replacement, len(initial))
		tmpPath := archivePath + ".replacement"
		require.NoError(t, os.WriteFile(tmpPath, []byte(replacement), 0o600))
		require.NoError(t, os.Rename(tmpPath, archivePath))

		replacedInfo, statErr := os.Stat(archivePath)
		require.NoError(t, statErr)
		require.False(t, os.SameFile(tail.fileInfo, replacedInfo))
		require.Equal(t, tail.offset, replacedInfo.Size())

		chunk, err = tail.read()
		require.ErrorContains(t, err, "droid child archive file changed identity")
		assert.Nil(t, chunk)
	})

	t.Run("tail.read rejects in-place truncation", func(t *testing.T) {
		t.Parallel()
		initial := `{"type":"session_start","id":"truncate-test"}` + "\n"
		dataRoot, work, archivePath := newArchive(t, initial)
		tail := &droidChildTail{dataRoot: dataRoot, workDir: work, sessionID: archiveChildID}

		chunk, err := tail.read()
		require.NoError(t, err)
		tail.offset = int64(len(chunk))
		require.NotNil(t, tail.fileInfo)

		require.NoError(t, os.Truncate(archivePath, 4))
		truncInfo, statErr := os.Stat(archivePath)
		require.NoError(t, statErr)
		require.True(t, os.SameFile(tail.fileInfo, truncInfo))
		require.Less(t, truncInfo.Size(), tail.offset)

		chunk, err = tail.read()
		require.ErrorContains(t, err, "droid child archive was truncated")
		assert.Nil(t, chunk)
	})
}

func TestDroidChildTailDoesNotOverlapBoundChildTurn(t *testing.T) {
	a, sink, clock, dir, work := droidArchiveHarness(t)
	path := filepath.Join(dir, archiveChildID+".jsonl")
	appendDroidArchive(t, path,
		droidArchiveStart(archiveChildID, work),
		`{"type":"message","id":"assistant-first","message":{"role":"assistant","content":[{"type":"text","text":"ARCHIVE_FIRST_TURN"}]}}`,
	)
	a.HandleOutput([]byte(droidTaskCall))
	a.HandleOutput([]byte(droidArchiveChildAvailable(archiveChildID, "task-1")))
	testutil.RequireEventually(t, func() bool {
		return archiveRowsContain(childArchiveRows(sink, archiveChildID), "ARCHIVE_FIRST_TURN")
	}, "initial archive row appears")
	_ = bindDroidArchiveChild(t, a)
	a.disarmChildTurn(archiveChildID)
	require.NoError(t, agenttest.SendChildInput(a, archiveChildID, agent.ChildInput{Content: "follow-up"}))

	// Any subsequent record written to the archive while the bound child owns the turn must not be projected by the tail.
	appendDroidArchive(t, path,
		`{"type":"message","id":"assistant-late","message":{"role":"assistant","content":[{"type":"text","text":"OVERLAPPING_ARCHIVE_ROW"}]}}`,
	)
	a.tailMu.Lock()
	tail := a.childTails[archiveChildID]
	a.tailMu.Unlock()
	require.NotNil(t, tail)
	tail.mu.Lock()
	assert.True(t, tail.finished, "SendChildInput must mark the initial archive tail finished")
	tail.mu.Unlock()
	select {
	case <-tail.stopped:
	default:
		t.Fatal("tail.stopped must be closed after SendChildInput stops the archive tail")
	}
	tail.poll()
	advanceDroidArchive(t, clock)
	assert.False(t, archiveRowsContain(childArchiveRows(sink, archiveChildID), "OVERLAPPING_ARCHIVE_ROW"),
		"archive tail must not concurrently write to the child sink after a bound child owns follow-up turns")
}

func TestDroidStopChildTailFlushAbsentAndIdempotent(t *testing.T) {
	a, sink, clock, dir, work := droidArchiveHarness(t)
	path := filepath.Join(dir, archiveChildID+".jsonl")
	appendDroidArchive(t, path,
		droidArchiveStart(archiveChildID, work),
		`{"type":"message","id":"assistant-initial","message":{"role":"assistant","content":[{"type":"text","text":"INITIAL_TAIL_ROW"}]}}`,
	)
	a.HandleOutput([]byte(droidTaskCall))
	a.HandleOutput([]byte(droidArchiveChildAvailable(archiveChildID, "task-1")))
	testutil.RequireEventually(t, func() bool {
		return archiveRowsContain(childArchiveRows(sink, archiveChildID), "INITIAL_TAIL_ROW")
	}, "initial archive row appears")

	// Absent sessionID is a clean no-op.
	a.stopChildTail("nonexistent-session-id")

	// Append a record without advancing the clock; stopChildTail must flush it before stopping.
	appendDroidArchive(t, path,
		`{"type":"message","id":"assistant-pre-stop","message":{"role":"assistant","content":[{"type":"text","text":"FLUSHED_BEFORE_STOP"}]}}`,
	)
	a.stopChildTail(archiveChildID)
	assert.True(t, archiveRowsContain(childArchiveRows(sink, archiveChildID), "FLUSHED_BEFORE_STOP"),
		"stopChildTail must flush unread records before stopping the tail")

	a.tailMu.Lock()
	tail := a.childTails[archiveChildID]
	a.tailMu.Unlock()
	require.NotNil(t, tail)
	tail.mu.Lock()
	assert.True(t, tail.finished)
	tail.mu.Unlock()
	select {
	case <-tail.stopped:
	default:
		t.Fatal("stopChildTail must close tail.stopped")
	}

	// Idempotent repeat call and subsequent archive writes add no rows.
	countAfterStop := len(childArchiveRows(sink, archiveChildID))
	appendDroidArchive(t, path,
		`{"type":"message","id":"assistant-after-stop","message":{"role":"assistant","content":[{"type":"text","text":"WRITTEN_AFTER_STOP"}]}}`,
	)
	a.stopChildTail(archiveChildID)
	advanceDroidArchive(t, clock)
	assert.Equal(t, countAfterStop, len(childArchiveRows(sink, archiveChildID)))
	assert.False(t, archiveRowsContain(childArchiveRows(sink, archiveChildID), "WRITTEN_AFTER_STOP"))
}

func TestDroidChildTailClosesRowWhenPendingBoundConnectionNotReady(t *testing.T) {
	a, sink, clock, dir, work := droidArchiveHarness(t)
	path := filepath.Join(dir, archiveChildID+".jsonl")
	appendDroidArchive(t, path,
		droidArchiveStart(archiveChildID, work),
		`{"type":"message","id":"assistant-first","message":{"role":"assistant","content":[{"type":"text","text":"ARCHIVE_FIRST_TURN"}]}}`,
	)
	a.HandleOutput([]byte(droidTaskCall))
	a.HandleOutput([]byte(droidArchiveChildAvailable(archiveChildID, "task-1")))
	testutil.RequireEventually(t, func() bool {
		return archiveRowsContain(childArchiveRows(sink, archiveChildID), "ARCHIVE_FIRST_TURN")
	}, "initial archive row appears")

	// Simulate startProcess still in flight (connection.ready open).
	a.childConnMu.Lock()
	a.childConns = map[string]*droidChildConnection{archiveChildID: {ready: make(chan struct{}), done: make(chan struct{})}}
	a.childConnMu.Unlock()

	appendDroidArchive(t, path, `{"type":"agent_turn_outcome","turnId":"turn-1","reason":"completed","resultKind":"text"}`)
	advanceDroidArchive(t, clock)
	testutil.RequireEventually(t, func() bool {
		row, ok := sink.BackgroundTask(archiveChildID)
		return ok && row.Status == bgtask.StatusSucceeded
	}, "initial archive outcome must still close the row while bound connection startup is not ready")
}
