package droid

import (
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
	return fmt.Sprintf(`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"child_session_available","childSessionId":%q,"toolUseId":%q,"subagentType":"explorer","description":"Inspect the note"}}}`, id, toolUseID)
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
		`{"type":"agent_turn_outcome","turnId":"turn-1","reason":"end_turn","resultKind":"success"}`,
	)
	a.HandleOutput([]byte(droidTaskCall))
	announcement := []byte(droidArchiveChildAvailable(archiveChildID, "task-1"))
	a.HandleOutput(announcement)
	advanceDroidArchive(t, clock)
	testutil.RequireEventually(t, func() bool {
		row, found := sink.BackgroundTask(archiveChildID)
		return found && row.Status == bgtask.StatusCompleted
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
		`{"type":"agent_turn_outcome","turnId":"turn-1","reason":"end_turn","resultKind":"success"}`,
	)
	a.HandleOutput([]byte(droidTaskCall))
	a.HandleOutput([]byte(droidArchiveChildAvailable(archiveChildID, "task-1")))
	advanceDroidArchive(t, clock)
	testutil.RequireEventually(t, func() bool {
		row, found := sink.BackgroundTask(archiveChildID)
		return found && row.Status == bgtask.StatusCompleted
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
	assert.Equal(t, bgtask.StatusCompleted, updated.Status)
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
		`{"type":"agent_turn_outcome","turnId":"turn-1","reason":"end_turn","resultKind":"success"}`,
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
		`{"type":"agent_turn_outcome","turnId":"turn-2","reason":"end_turn","resultKind":"success"}`,
	)
	// SendChildInput resumes this same native session through startChildTail.
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
		`{"type":"agent_turn_outcome","turnId":"turn-1","reason":"end_turn","resultKind":"success"}`,
		`{"type":"message","id":"assistant-second","message":{"role":"assistant","content":[{"type":"text","text":"ARCHIVE_SECOND_TURN"}]}}`,
		`{"type":"agent_turn_outcome","turnId":"turn-2","reason":"end_turn","resultKind":"success"}`,
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
			child.disarmTurn()
		}
		require.NoError(t, a.SendChildInput(archiveChildID, fmt.Sprintf("follow-up %d", index+1), nil))
	}
	records := []string{`{"type":"agent_turn_outcome","turnId":"turn-1","reason":"end_turn","resultKind":"success"}`}
	for index := range sends {
		records = append(records,
			fmt.Sprintf(`{"type":"message","id":"assistant-%d","message":{"role":"assistant","content":[{"type":"text","text":"ARCHIVE_FOLLOWUP_%d"}]}}`, index+2, index+1),
			fmt.Sprintf(`{"type":"agent_turn_outcome","turnId":"turn-%d","reason":"end_turn","resultKind":"success"}`, index+2),
		)
	}
	appendDroidArchive(t, path, records...)
	return a, sink, clock, path
}

func TestDroidChildTailContinuesAfterEarlySend(t *testing.T) {
	_, sink, clock, _ := droidEarlySendArchive(t, 1)
	advanceDroidArchive(t, clock)
	testutil.RequireEventually(t, func() bool {
		return archiveRowsContain(childArchiveRows(sink, archiveChildID), "ARCHIVE_FOLLOWUP_1")
	}, "the accepted follow-up survives the first unpolled outcome")
}

func TestDroidChildTailCountsEarlySends(t *testing.T) {
	_, sink, clock, _ := droidEarlySendArchive(t, 2)
	advanceDroidArchive(t, clock)
	testutil.RequireEventually(t, func() bool {
		return archiveRowsContain(childArchiveRows(sink, archiveChildID), "ARCHIVE_FOLLOWUP_2")
	}, "both accepted follow-ups survive unpolled outcomes")
	for _, marker := range []string{"ARCHIVE_FIRST_TURN", "ARCHIVE_FOLLOWUP_1", "ARCHIVE_FOLLOWUP_2"} {
		count := 0
		for _, row := range childArchiveRows(sink, archiveChildID) {
			if strings.Contains(string(row.Content), marker) {
				count++
			}
		}
		assert.Equal(t, 1, count, "each child message appears once")
	}
}

func TestDroidChildTailStopWithPendingContinuation(t *testing.T) {
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
		t.Fatal("Stop did not finish with a pending child continuation")
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
		`{"type":"agent_turn_outcome","turnId":"turn-1","reason":"end_turn","resultKind":"success"}`,
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
		`{"type":"agent_turn_outcome","turnId":"turn-2","reason":"end_turn","resultKind":"success"}`)
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
		`{"type":"agent_turn_outcome","turnId":"turn-1","reason":"end_turn","resultKind":"success"}`,
	)
	advanceDroidArchive(t, clock)
	testutil.RequireEventually(t, func() bool {
		item, exists := sink.BackgroundTask(archiveChildID)
		return exists && item.Status == bgtask.StatusCompleted && archiveRowsContain(childArchiveRows(sink, archiveChildID), "ARCHIVE_CHILD_FINAL")
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
