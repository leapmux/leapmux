package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

const checkpointTerminalType = leapmuxv1.TabType_TAB_TYPE_TERMINAL

func validCheckpoint() *eventCheckpoint {
	return &eventCheckpoint{
		Version: eventCheckpointVersion,
		Scope:   tabCheckpointScope,
		Filter:  []string{kindTerminalBell},
		Tab: &tabEventCheckpoint{
			WorkerID:    "worker",
			TabID:       "terminal",
			TabType:     "terminal",
			TabStreamID: "stream",
			Position: &eventHistoryPosition{
				JournalID: "journal",
				Sequence:  3,
				Cursor:    []byte("cursor at 3"),
			},
		},
	}
}

func checkpointBell(sequence int64) *leapmuxv1.WatchSemanticEventsResponse {
	return &leapmuxv1.WatchSemanticEventsResponse{
		Event: &leapmuxv1.WatchSemanticEventsResponse_Record{
			Record: &leapmuxv1.SemanticWatchRecord{
				Sequence: sequence, ObservedUnixMillis: 1700000000123, Cursor: []byte("next signed position"),
				Fact: &leapmuxv1.SemanticTabFact{Event: &leapmuxv1.SemanticTabFact_TerminalBell{TerminalBell: &leapmuxv1.TerminalBell{}}},
			},
		},
	}
}

func TestEventCheckpointLocksAndRoundTripsItsPosition(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", cfgDir)
	dir := t.TempDir()
	path := filepath.Join(dir, "position.json")
	first, err := openEventCheckpoint(path)
	require.NoError(t, err)
	t.Cleanup(first.close)
	state, err := first.read()
	require.NoError(t, err)
	require.Nil(t, state)

	// Lock files live under <configDir>/event-locks, never beside the checkpoint.
	_, legacyErr := os.Stat(filepath.Join(dir, ".leapmux-event-locks"))
	assert.ErrorIs(t, legacyErr, os.ErrNotExist)
	lockEntries, err := os.ReadDir(filepath.Join(cfgDir, eventCheckpointLockDirName))
	require.NoError(t, err)
	assert.Len(t, lockEntries, 1)

	contender, err := openEventCheckpoint(path)
	require.Error(t, err)
	assert.Nil(t, contender)
	assert.Equal(t, cli.CodeConflict, cli.FromError(err).Code)

	require.NoError(t, first.save(validCheckpoint()))
	first.close()
	next, err := openEventCheckpoint(path)
	require.NoError(t, err)
	defer next.close()
	got, err := next.read()
	require.NoError(t, err)
	assert.Equal(t, validCheckpoint(), got)
}

func TestEventCheckpointCreatesMissingParentAndAutoExcludesLeapMuxTmp(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", cfgDir)
	repo := testutil.NewGitRepo(t)

	path := filepath.Join(repo, ".leapmux", "tmp", "nested", "events.ckpt")
	f, err := openEventCheckpoint(path)
	require.NoError(t, err)
	defer f.close()
	require.NoError(t, f.save(validCheckpoint()))

	// No lock directory is created inside the repository.
	_, legacyErr := os.Stat(filepath.Join(filepath.Dir(path), ".leapmux-event-locks"))
	assert.ErrorIs(t, legacyErr, os.ErrNotExist)

	// .leapmux/tmp/ is automatically excluded in .git/info/exclude.
	excludeBytes, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	require.NoError(t, err)
	assert.Contains(t, string(excludeBytes), ".leapmux/tmp/")

	// Checkpoint paths inside the lock directory are refused.
	badPath := filepath.Join(cfgDir, eventCheckpointLockDirName, "bad.ckpt")
	_, err = openEventCheckpoint(badPath)
	require.Error(t, err)
	assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
}

func TestAccountCheckpointJSONOmitsFlatTabAndLayoutFields(t *testing.T) {
	t.Parallel()
	cp := &eventCheckpoint{
		Version: eventCheckpointVersion,
		Scope:   accountCheckpointScope,
		Filter:  []string{kindTerminalBell},
		Account: &accountEventCheckpoint{
			OwnerID:            "u-1",
			EnrollmentRevision: 1,
			Workers:            map[string]accountWorkerPositions{},
		},
	}
	raw, err := json.Marshal(cp)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	assert.Equal(t, accountCheckpointScope, decoded["scope"])
	assert.Contains(t, decoded, "account")
	for _, absent := range []string{"tab", "layout", "journal_id", "sequence", "cursor", "worker_id", "tab_id", "workspace_id"} {
		assert.NotContains(t, decoded, absent)
	}
}

func TestEventCheckpointDataCannotReplaceAnotherCheckpointLock(t *testing.T) {
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	path := filepath.Join(t.TempDir(), "position")
	other, err := openEventCheckpoint(path + ".lock")
	require.NoError(t, err)
	defer other.close()
	require.NoError(t, other.save(validCheckpoint()))

	first, err := openEventCheckpoint(path)
	require.NoError(t, err)
	defer first.close()
	require.NoError(t, other.save(validCheckpoint()))
	contender, err := openEventCheckpoint(path)
	if contender != nil {
		defer contender.close()
	}
	require.Error(t, err)
	assert.Equal(t, cli.CodeConflict, cli.FromError(err).Code)
}

func TestEventCheckpointRejectsInvalidFilesAndLeavesThemIntact(t *testing.T) {
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	for name, content := range map[string][]byte{
		"empty":             nil,
		"null":              []byte("null"),
		"oversize":          bytes.Repeat([]byte("x"), maxEventCheckpointBytes+1),
		"unknown field":     []byte(`{"unknown":true}`),
		"negative sequence": []byte(`{"version":1,"scope":"tab","kinds":[],"tab":{"worker_id":"w","tab_type":"terminal","tab_id":"t","tab_stream_id":"s","position":{"journal_id":"j","sequence":-1,"cursor":"YQ=="}}}`),
		"missing scope":     []byte(`{"version":1,"kinds":[],"tab":{"worker_id":"w","tab_type":"terminal","tab_id":"t","tab_stream_id":"s","position":{"journal_id":"j","sequence":0,"cursor":"YQ=="}}}`),
		"mixed scopes":      []byte(`{"version":1,"scope":"tab","kinds":[],"tab":{"worker_id":"w","tab_type":"terminal","tab_id":"t","tab_stream_id":"s","position":{"journal_id":"j","sequence":0,"cursor":"YQ=="}},"layout":{"position":{"journal_id":"h","sequence":0,"cursor":"YQ=="}}}`),
		"trailing data":     []byte(`{}{}`),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "position.json")
			require.NoError(t, os.WriteFile(path, content, 0o600))
			checkpoint, err := openEventCheckpoint(path)
			require.NoError(t, err)
			defer checkpoint.close()
			state, err := checkpoint.read()
			require.Error(t, err)
			assert.Nil(t, state)
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.True(t, bytes.Equal(content, data))
		})
	}
}

func makeCheckpointedEvents(t *testing.T, destination io.Writer) *checkpointedEvents {
	t.Helper()
	filter, err := parseKindFilter([]string{kindTerminalBell})
	require.NoError(t, err)
	state := validCheckpoint()
	return &checkpointedEvents{
		opts:    eventsOptions{filter: filter, limit: 1},
		tab:     scopedTab{ID: state.Tab.TabID, WorkerID: state.Tab.WorkerID, Type: checkpointTerminalType},
		printer: newEventPrinter(&cli.Printer{Out: destination, Err: io.Discard, Format: cli.FormatJSON}, filter, 1),
		state:   state, kinds: state.Filter,
	}
}

type shortEventWriter struct{}

func (shortEventWriter) Write(data []byte) (int, error) { return len(data) - 1, nil }

func TestCheckpointedRetirementRequiresTheFullSavedPosition(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	replay := makeCheckpointedEvents(t, &output)
	replay.opts.filter = kindFilter{}
	replay.printer.filter = kindFilter{}
	replay.save = func(*eventCheckpoint) error {
		t.Error("a retirement confirmation does not advance the saved position")
		return nil
	}
	final := &leapmuxv1.SemanticWatchRetired{FinalSequence: 3}
	require.Error(t, replay.acceptRetirement(final, 3, 4), "the Worker promised another record")
	require.Error(t, replay.acceptRetirement(final, 2, 2), "the final sequence is beyond the printed records")
	require.Empty(t, output.String())
	require.NoError(t, replay.acceptRetirement(final, 3, 3))
	event := decodeEventLine(t, output.String())
	assert.Equal(t, kindStreamRetired, event.Kind)
	require.NotNil(t, event.Sequence)
	assert.Equal(t, int64(3), *event.Sequence)
	assert.Zero(t, replay.printer.printed())
}

func TestCheckpointedEventsDoesNotSaveAfterAShortWrite(t *testing.T) {
	t.Parallel()
	replay := makeCheckpointedEvents(t, shortEventWriter{})
	replay.save = func(*eventCheckpoint) error {
		t.Error("a short write must not publish a checkpoint")
		return nil
	}
	after, err := replay.acceptFrame(checkpointBell(4), 3, 4)
	require.ErrorIs(t, err, io.ErrShortWrite)
	assert.Equal(t, int64(3), after)
	assert.Equal(t, validCheckpoint(), replay.state)
	assert.Zero(t, replay.printer.printed())
}

func TestCheckpointedEventsWritesBeforeCheckpointingAndCountsOnlyAfterSave(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	replay := makeCheckpointedEvents(t, &output)
	checkpointFailure := errors.New("directory changed")
	replay.save = func(state *eventCheckpoint) error {
		assert.Contains(t, output.String(), `"sequence":4,"replayed":true`)
		assert.Equal(t, int64(4), state.Tab.Position.Sequence)
		assert.Zero(t, replay.printer.printed())
		return checkpointFailure
	}
	after, err := replay.acceptFrame(checkpointBell(4), 3, 4)
	require.ErrorIs(t, err, checkpointFailure)
	assert.Equal(t, int64(3), after)
	assert.Equal(t, validCheckpoint(), replay.state)
	assert.Zero(t, replay.printer.printed())

	output.Reset()
	replay.save = func(state *eventCheckpoint) error {
		assert.Contains(t, output.String(), `"kind":"terminal.bell"`)
		assert.Equal(t, []byte("next signed position"), state.Tab.Position.Cursor)
		return nil
	}
	after, err = replay.acceptFrame(checkpointBell(4), 3, 3)
	require.NoError(t, err)
	assert.Equal(t, int64(4), after)
	assert.Equal(t, int64(4), replay.state.Tab.Position.Sequence)
	assert.Equal(t, 1, replay.printer.printed())
	assert.Contains(t, output.String(), `"replayed":false`)
}

func TestCheckpointedEventsRejectsPositionsWithUnreportedLoss(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	replay := makeCheckpointedEvents(t, &output)
	replay.save = func(*eventCheckpoint) error {
		t.Error("a discontinuous frame must not publish a checkpoint")
		return nil
	}
	for _, seq := range []int64{-1, 3, 5, 1 << 60} {
		after, err := replay.acceptFrame(checkpointBell(seq), 3, 5)
		assert.Error(t, err)
		assert.Equal(t, int64(3), after)
	}
	assert.Empty(t, output.String())
}

func TestCheckpointedEventsPersistsVisibleGapsWithoutUsingTheLimitAndFiltersLostKinds(t *testing.T) {
	t.Parallel()
	var out, errOut bytes.Buffer
	filter, err := parseKindFilter([]string{kindStreamGap, kindTerminalBell})
	require.NoError(t, err)
	state := validCheckpoint()
	replay := &checkpointedEvents{
		opts:    eventsOptions{filter: filter, limit: 1},
		tab:     scopedTab{ID: state.Tab.TabID, WorkerID: state.Tab.WorkerID, Type: checkpointTerminalType},
		printer: newEventPrinter(&cli.Printer{Out: &out, Err: &errOut, Format: cli.FormatJSON}, filter, 1),
		state:   state, kinds: state.Filter,
	}
	replay.save = func(state *eventCheckpoint) error {
		assert.Contains(t, out.String(), "tab events expired")
		assert.Equal(t, int64(10), state.Tab.Position.Sequence)
		return nil
	}
	frame := &leapmuxv1.WatchSemanticEventsResponse{
		Event: &leapmuxv1.WatchSemanticEventsResponse_Gap{
			Gap: &leapmuxv1.SemanticWatchGap{
				Reason:        leapmuxv1.SemanticWatchGapReason_SEMANTIC_WATCH_GAP_REASON_RETENTION,
				AfterSequence: 3, ResumeAfterSequence: 10, Cursor: []byte("cursor at 10"),
			},
		},
	}
	after, err := replay.acceptFrame(frame, 3, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(10), after)
	assert.Zero(t, replay.printer.printed())
	ev := decodeEventLine(t, out.String())
	assert.Equal(t, []string{kindTerminalBell}, ev.Lost)
}

func TestCheckpointedEventsDefersSaveForFilteredRecordsUntilFlush(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	replay := makeCheckpointedEvents(t, &output)
	saves := 0
	replay.save = func(state *eventCheckpoint) error {
		saves++
		assert.Equal(t, int64(4), state.Tab.Position.Sequence)
		assert.Equal(t, []byte("skipped position"), state.Tab.Position.Cursor)
		return nil
	}
	frame := checkpointBell(4)
	record := frame.GetRecord()
	record.Fact = &leapmuxv1.SemanticTabFact{
		Event: &leapmuxv1.SemanticTabFact_TerminalTitle{
			TerminalTitle: &leapmuxv1.TerminalTitleChanged{Title: "filtered"},
		},
	}
	record.Cursor = nil
	after, err := replay.acceptFrame(frame, 3, 6)
	require.Error(t, err)
	assert.Equal(t, int64(3), after)
	assert.Zero(t, saves)
	record.Cursor = []byte("skipped position")
	after, err = replay.acceptFrame(frame, 3, 6)
	require.NoError(t, err)
	assert.Equal(t, int64(4), after)
	assert.Equal(t, int64(4), replay.state.Tab.Position.Sequence)
	assert.Zero(t, saves, "filtered backlog records must not fsync per record")
	require.NoError(t, replay.flush())
	assert.Equal(t, 1, saves)
	assert.Empty(t, output.String())
	assert.Zero(t, replay.printer.printed())
}

func TestOpenCheckedEventCheckpointReportsExactSavedScopeOnMismatch(t *testing.T) {
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	cases := []struct {
		name      string
		saved     *eventCheckpoint
		expected  string
		wantError string
	}{
		{
			name: "account opened as tab",
			saved: &eventCheckpoint{
				Version: eventCheckpointVersion, Scope: accountCheckpointScope, Filter: []string{kindTerminalBell},
				Account: &accountEventCheckpoint{OwnerID: "u-1", EnrollmentRevision: 1, Workers: map[string]accountWorkerPositions{}},
			},
			expected:  tabCheckpointScope,
			wantError: "belongs to an account stream",
		},
		{
			name: "account opened as layout",
			saved: &eventCheckpoint{
				Version: eventCheckpointVersion, Scope: accountCheckpointScope, Filter: []string{kindTerminalBell},
				Account: &accountEventCheckpoint{OwnerID: "u-1", EnrollmentRevision: 1, Workers: map[string]accountWorkerPositions{}},
			},
			expected:  layoutCheckpointScope,
			wantError: "belongs to an account stream",
		},
		{
			name:      "tab opened as layout",
			saved:     validCheckpoint(),
			expected:  layoutCheckpointScope,
			wantError: "belongs to a tab stream",
		},
		{
			name: "layout opened as account",
			saved: &eventCheckpoint{
				Version: eventCheckpointVersion, Scope: layoutCheckpointScope, Filter: []string{kindTabOpened},
				Layout: &layoutEventCheckpoint{
					Position: &eventHistoryPosition{JournalID: "hub", Sequence: 1, Cursor: []byte("c1")},
				},
			},
			expected:  accountCheckpointScope,
			wantError: "belongs to a layout stream",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cp.json")
			f, err := openEventCheckpoint(path)
			require.NoError(t, err)
			require.NoError(t, f.save(tc.saved))
			f.close()

			_, _, err = openCheckedEventCheckpoint(path, tc.expected)
			e := cli.FromError(err)
			require.NotNil(t, e)
			assert.Equal(t, cli.CodeConflict, e.Code)
			assert.Contains(t, e.Message, tc.wantError)
		})
	}
}

func TestLayoutEventReplaySnapshotFinishesOnRetentionGapAndFiltersLostKinds(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	filter, err := parseKindFilter([]string{kindStreamGap, kindTabOpened})
	require.NoError(t, err)
	saves := 0
	replay := &layoutEventReplay{
		opts:    eventsOptions{filter: filter, snapshot: true},
		printer: newEventPrinter(&cli.Printer{Out: &out, Err: io.Discard, Format: cli.FormatJSON}, filter, 0),
		clock:   testutil.NewQuartzMock(t),
		kinds:   []string{kindTabOpened},
		state: &eventCheckpoint{
			Version: eventCheckpointVersion, Scope: layoutCheckpointScope, Filter: []string{kindTabOpened},
			Layout: &layoutEventCheckpoint{
				Position: &eventHistoryPosition{JournalID: "hub", Sequence: 1, Cursor: []byte("c1")},
			},
		},
		save: func(next *eventCheckpoint) error {
			saves++
			return nil
		},
	}
	done, err := replay.acceptPage(&leapmuxv1.ReadLayoutEventsResponse{
		JournalId: "hub", AfterSequence: 1, HighWaterSequence: 5, ScannedSequence: 1,
		Cursor: []byte("c1"), ScannedCursor: []byte("c1"),
		Gap: &leapmuxv1.LayoutEventGap{ResumeAfterSequence: 5, Cursor: []byte("c5")},
	})
	require.NoError(t, err)
	assert.True(t, done, "snapshot must finish immediately when retention gap reaches replayHigh")
	assert.Equal(t, 1, saves)
	ev := decodeEventLine(t, out.String())
	assert.Equal(t, kindStreamGap, ev.Kind)
	assert.Equal(t, "hub", ev.Source)
	assert.Equal(t, []string{kindTabOpened}, ev.Lost)
}

func TestLayoutPageConsumerSkipsSourceOnNormalRecordsAndBatchesFilteredSaves(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	filter, err := parseKindFilter([]string{kindTabOpened})
	require.NoError(t, err)
	saves := 0
	consumer := &layoutPageConsumer{
		filter:     filter,
		printer:    newEventPrinter(&cli.Printer{Out: &out, Err: io.Discard, Format: cli.FormatJSON}, filter, 0),
		clock:      testutil.NewQuartzMock(t),
		position:   &eventHistoryPosition{JournalID: "hub", Sequence: 0, Cursor: []byte("c0")},
		replayHigh: 3,
		persist: func(*eventHistoryPosition) error {
			saves++
			return nil
		},
	}
	endReached, limitReached, err := consumer.consumePage(&leapmuxv1.ReadLayoutEventsResponse{
		JournalId: "hub", AfterSequence: 0, HighWaterSequence: 3, ScannedSequence: 3,
		Cursor: []byte("c0"), ScannedCursor: []byte("c3"),
		Records: []*leapmuxv1.LayoutEventRecord{
			{
				Sequence: 1, Cursor: []byte("c1"),
				Fact: &leapmuxv1.LayoutEventFact{Event: &leapmuxv1.LayoutEventFact_Workspace{
					Workspace: &leapmuxv1.LayoutWorkspaceChange{
						WorkspaceId: "ws-1", Operation: leapmuxv1.WorkspaceLifecycleOp_WORKSPACE_LIFECYCLE_OP_RENAME, Title: "Filtered",
					},
				}},
			},
			{
				Sequence: 2, Cursor: []byte("c2"),
				Fact: &leapmuxv1.LayoutEventFact{Event: &leapmuxv1.LayoutEventFact_Tab{
					Tab: &leapmuxv1.LayoutTabChange{
						Kind:  leapmuxv1.LayoutTabChangeKind_LAYOUT_TAB_CHANGE_KIND_OPENED,
						Tab:   &leapmuxv1.TabIdent{TabType: checkpointTerminalType, TabId: "tab-1"},
						After: &leapmuxv1.LayoutTabPlacement{WorkspaceId: "ws-1", WorkerId: "w-1"},
					},
				}},
			},
			{
				Sequence: 3, Cursor: []byte("c3"),
				Fact: &leapmuxv1.LayoutEventFact{Event: &leapmuxv1.LayoutEventFact_Workspace{
					Workspace: &leapmuxv1.LayoutWorkspaceChange{
						WorkspaceId: "ws-1", Operation: leapmuxv1.WorkspaceLifecycleOp_WORKSPACE_LIFECYCLE_OP_RENAME, Title: "Filtered2",
					},
				}},
			},
		},
	})
	require.NoError(t, err)
	assert.False(t, endReached)
	assert.False(t, limitReached)
	// Save 1: printed record at seq 2; Save 2: page boundary flush for filtered seq 3.
	assert.Equal(t, 2, saves)
	ev := decodeEventLine(t, out.String())
	assert.Equal(t, kindTabOpened, ev.Kind)
	assert.Empty(t, ev.Source, "normal layout records must not set Source")
}

func TestWaitReplayRetryUsesInjectedClock(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	trap := clock.Trap().NewTimer("events-replay-retry")
	defer trap.Close()
	done := make(chan bool, 1)
	go func() {
		done <- waitReplayRetry(ctx, clock, 5*time.Second)
	}()
	assert.Equal(t, 5*time.Second, testutil.WaitForTimer(t, ctx, trap))
	clock.Advance(5 * time.Second).MustWait(ctx)
	assert.True(t, <-done)
}

func TestCheckpointDispatch(t *testing.T) {
	t.Parallel()
	allKinds, err := parseKindFilter(nil)
	require.NoError(t, err)
	hubOnly, err := parseKindFilter([]string{"workspace", "tab.opened"})
	require.NoError(t, err)
	terminalOnly, err := parseKindFilter([]string{"terminal.bell"})
	require.NoError(t, err)

	assert.Equal(t, tabCheckpointScope, checkpointDispatch(eventsOptions{tab: "t-1", filter: allKinds}))
	assert.Equal(t, layoutCheckpointScope, checkpointDispatch(eventsOptions{workspace: "ws-1", filter: allKinds}))
	assert.Equal(t, layoutCheckpointScope, checkpointDispatch(eventsOptions{filter: hubOnly}),
		"unscoped Hub-only kind selections stay on layout for workspace:read least privilege")
	assert.Equal(t, accountCheckpointScope, checkpointDispatch(eventsOptions{filter: allKinds}))
	assert.Equal(t, accountCheckpointScope, checkpointDispatch(eventsOptions{filter: terminalOnly}))
}

func TestWithClockTimeout_CancelsDeterministicallyWithQuartzMock(t *testing.T) {
	t.Parallel()
	parent := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)

	// Non-positive duration returns parent unchanged without scheduling a timer.
	unchanged, noop := withClockTimeout(parent, clock, 0, eventTimeoutTimerTag)
	assert.Equal(t, parent, unchanged)
	noop()

	ctx, cancel := withClockTimeout(parent, clock, 12*time.Second, eventTimeoutTimerTag)
	defer cancel()
	require.NoError(t, ctx.Err())

	clock.Advance(11 * time.Second).MustWait(parent)
	assert.NoError(t, ctx.Err(), "timeout must not fire before the full duration elapses")

	clock.Advance(time.Second).MustWait(parent)
	<-ctx.Done()
	assert.ErrorIs(t, context.Cause(ctx), context.DeadlineExceeded)
	assert.NoError(t, parent.Err(), "parent context must remain uncanceled")

	// Cancelling early stops the timer and reports context.Canceled rather than DeadlineExceeded.
	ctx2, cancel2 := withClockTimeout(parent, clock, 5*time.Second, eventTimeoutTimerTag)
	cancel2()
	clock.Advance(5 * time.Second).MustWait(parent)
	assert.ErrorIs(t, context.Cause(ctx2), context.Canceled)
}

func TestValidateLayoutPageHeader_RejectsInvalidHeaders(t *testing.T) {
	t.Parallel()
	saved := &eventHistoryPosition{JournalID: "hub", Sequence: 2, Cursor: []byte("c2")}

	validFresh, err := validateLayoutPageHeader(&leapmuxv1.ReadLayoutEventsResponse{
		JournalId: "hub", AfterSequence: 5, HighWaterSequence: 5, ScannedSequence: 5,
		Cursor: []byte("c5"), ScannedCursor: []byte("c5"),
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, int64(5), validFresh.Sequence)

	validResume, err := validateLayoutPageHeader(&leapmuxv1.ReadLayoutEventsResponse{
		JournalId: "hub", AfterSequence: 2, HighWaterSequence: 5, ScannedSequence: 3,
		Cursor: []byte("c2"), ScannedCursor: []byte("c3"),
	}, saved)
	require.NoError(t, err)
	assert.Equal(t, saved, validResume)

	cases := []struct {
		name  string
		saved *eventHistoryPosition
		page  *leapmuxv1.ReadLayoutEventsResponse
	}{
		{
			name:  "pos.Sequence > scanned",
			saved: saved,
			page: &leapmuxv1.ReadLayoutEventsResponse{
				JournalId: "hub", AfterSequence: 2, HighWaterSequence: 5, ScannedSequence: 1,
				Cursor: []byte("c2"), ScannedCursor: []byte("c1"),
			},
		},
		{
			name:  "scanned > high",
			saved: saved,
			page: &leapmuxv1.ReadLayoutEventsResponse{
				JournalId: "hub", AfterSequence: 2, HighWaterSequence: 5, ScannedSequence: 6,
				Cursor: []byte("c2"), ScannedCursor: []byte("c6"),
			},
		},
		{
			name:  "empty ScannedCursor",
			saved: saved,
			page: &leapmuxv1.ReadLayoutEventsResponse{
				JournalId: "hub", AfterSequence: 2, HighWaterSequence: 5, ScannedSequence: 3,
				Cursor: []byte("c2"), ScannedCursor: nil,
			},
		},
		{
			name:  "fresh start with pos.Sequence != high",
			saved: nil,
			page: &leapmuxv1.ReadLayoutEventsResponse{
				JournalId: "hub", AfterSequence: 2, HighWaterSequence: 5, ScannedSequence: 2,
				Cursor: []byte("c2"), ScannedCursor: []byte("c2"),
			},
		},
		{
			name:  "saved journal mismatch",
			saved: saved,
			page: &leapmuxv1.ReadLayoutEventsResponse{
				JournalId: "other-hub", AfterSequence: 2, HighWaterSequence: 5, ScannedSequence: 3,
				Cursor: []byte("c2"), ScannedCursor: []byte("c3"),
			},
		},
		{
			name:  "saved sequence mismatch",
			saved: saved,
			page: &leapmuxv1.ReadLayoutEventsResponse{
				JournalId: "hub", AfterSequence: 3, HighWaterSequence: 5, ScannedSequence: 3,
				Cursor: []byte("c2"), ScannedCursor: []byte("c3"),
			},
		},
		{
			name:  "saved cursor mismatch",
			saved: saved,
			page: &leapmuxv1.ReadLayoutEventsResponse{
				JournalId: "hub", AfterSequence: 2, HighWaterSequence: 5, ScannedSequence: 3,
				Cursor: []byte("wrong-cursor"), ScannedCursor: []byte("c3"),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pos, err := validateLayoutPageHeader(tc.page, tc.saved)
			require.ErrorIs(t, err, errInvalidLayoutHistory)
			assert.Nil(t, pos)
		})
	}
}

func TestRetainedLayoutEvent_LossAndMovedEdgeCases(t *testing.T) {
	t.Parallel()
	filter, err := parseKindFilter([]string{kindTabOpened, kindTabMoved})
	require.NoError(t, err)

	lossEv, ok := retainedLayoutEvent(&leapmuxv1.LayoutEventFact{
		Event: &leapmuxv1.LayoutEventFact_Loss{Loss: &leapmuxv1.LayoutEventLoss{}},
	}, filter)
	require.True(t, ok)
	assert.Equal(t, kindStreamGap, lossEv.Kind)
	assert.Equal(t, "hub", lossEv.Source)
	assert.Equal(t, "unreadable_lifecycle", lossEv.GapReason)
	assert.Equal(t, []string{kindTabOpened, kindTabMoved}, lossEv.Lost)

	leftEv, ok := retainedLayoutEvent(&leapmuxv1.LayoutEventFact{
		Event: &leapmuxv1.LayoutEventFact_Tab{
			Tab: &leapmuxv1.LayoutTabChange{
				Kind:   leapmuxv1.LayoutTabChangeKind_LAYOUT_TAB_CHANGE_KIND_MOVED,
				Tab:    &leapmuxv1.TabIdent{TabType: checkpointTerminalType, TabId: "tab-1"},
				Before: &leapmuxv1.LayoutTabPlacement{WorkspaceId: "ws-old", WorkerId: "w-before"},
				After:  nil,
			},
		},
	}, filter)
	require.True(t, ok)
	assert.Equal(t, kindTabMoved, leftEv.Kind)
	assert.Equal(t, "w-before", leftEv.WorkerID)
	assert.Equal(t, "ws-old", leftEv.FromWorkspaceID)
	assert.Empty(t, leftEv.WorkspaceID)
	assert.Equal(t, "terminal tab left the watched workspace", leftEv.Summary)

	enteredEv, ok := retainedLayoutEvent(&leapmuxv1.LayoutEventFact{
		Event: &leapmuxv1.LayoutEventFact_Tab{
			Tab: &leapmuxv1.LayoutTabChange{
				Kind:   leapmuxv1.LayoutTabChangeKind_LAYOUT_TAB_CHANGE_KIND_MOVED,
				Tab:    &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tab-2"},
				Before: nil,
				After:  &leapmuxv1.LayoutTabPlacement{WorkspaceId: "ws-new", WorkerId: "w-after"},
			},
		},
	}, filter)
	require.True(t, ok)
	assert.Equal(t, kindTabMoved, enteredEv.Kind)
	assert.Equal(t, "w-after", enteredEv.WorkerID)
	assert.Equal(t, "ws-new", enteredEv.WorkspaceID)
	assert.Empty(t, enteredEv.FromWorkspaceID)
	assert.Equal(t, "agent tab entered the watched workspace", enteredEv.Summary)
}

func TestLayoutPageConsumer_LossGapMidPageLimitAndTrailingFiltered(t *testing.T) {
	t.Parallel()

	t.Run("loss record emits unreadable_lifecycle gap without counting against limit", func(t *testing.T) {
		var out bytes.Buffer
		filter, err := parseKindFilter([]string{kindStreamGap, kindTabOpened})
		require.NoError(t, err)
		var persisted []*eventHistoryPosition
		consumer := &layoutPageConsumer{
			workspace:  "ws-1",
			filter:     filter,
			printer:    newEventPrinter(&cli.Printer{Out: &out, Err: io.Discard, Format: cli.FormatJSON}, filter, 1),
			clock:      testutil.NewQuartzMock(t),
			position:   &eventHistoryPosition{JournalID: "hub", Sequence: 2, Cursor: []byte("c2")},
			replayHigh: 3,
			persist: func(pos *eventHistoryPosition) error {
				cp := *pos
				persisted = append(persisted, &cp)
				return nil
			},
		}
		endReached, limitReached, err := consumer.consumePage(&leapmuxv1.ReadLayoutEventsResponse{
			JournalId: "hub", AfterSequence: 2, HighWaterSequence: 3, ScannedSequence: 3,
			Cursor: []byte("c2"), ScannedCursor: []byte("c3"),
			Records: []*leapmuxv1.LayoutEventRecord{{
				Sequence: 3, Cursor: []byte("c3"),
				Fact: &leapmuxv1.LayoutEventFact{Event: &leapmuxv1.LayoutEventFact_Loss{Loss: &leapmuxv1.LayoutEventLoss{}}},
			}},
		})
		require.NoError(t, err)
		assert.False(t, endReached)
		assert.False(t, limitReached)
		assert.Zero(t, consumer.printer.printed(), "gap records must not consume --limit")
		require.Len(t, persisted, 1)
		assert.Equal(t, int64(3), persisted[0].Sequence)
		ev := decodeEventLine(t, out.String())
		assert.Equal(t, kindStreamGap, ev.Kind)
		assert.Equal(t, "unreadable_lifecycle", ev.GapReason)
		assert.Equal(t, "ws-1", ev.WorkspaceID)
		require.NotNil(t, ev.AfterSequence)
		assert.Equal(t, int64(2), *ev.AfterSequence)
		assert.Equal(t, []string{kindTabOpened}, ev.Lost)
	})

	t.Run("mid-page --limit stops early and persists stopped record cursor", func(t *testing.T) {
		var out bytes.Buffer
		filter, err := parseKindFilter([]string{kindTabOpened})
		require.NoError(t, err)
		var persisted []*eventHistoryPosition
		consumer := &layoutPageConsumer{
			filter:     filter,
			printer:    newEventPrinter(&cli.Printer{Out: &out, Err: io.Discard, Format: cli.FormatJSON}, filter, 1),
			clock:      testutil.NewQuartzMock(t),
			position:   &eventHistoryPosition{JournalID: "hub", Sequence: 0, Cursor: []byte("c0")},
			replayHigh: 2,
			persist: func(pos *eventHistoryPosition) error {
				cp := *pos
				persisted = append(persisted, &cp)
				return nil
			},
		}
		tabOpened := func(seq int64, cursor, tabID string) *leapmuxv1.LayoutEventRecord {
			return &leapmuxv1.LayoutEventRecord{
				Sequence: seq, Cursor: []byte(cursor),
				Fact: &leapmuxv1.LayoutEventFact{Event: &leapmuxv1.LayoutEventFact_Tab{
					Tab: &leapmuxv1.LayoutTabChange{
						Kind:  leapmuxv1.LayoutTabChangeKind_LAYOUT_TAB_CHANGE_KIND_OPENED,
						Tab:   &leapmuxv1.TabIdent{TabType: checkpointTerminalType, TabId: tabID},
						After: &leapmuxv1.LayoutTabPlacement{WorkspaceId: "ws-1", WorkerId: "w-1"},
					},
				}},
			}
		}
		endReached, limitReached, err := consumer.consumePage(&leapmuxv1.ReadLayoutEventsResponse{
			JournalId: "hub", AfterSequence: 0, HighWaterSequence: 2, ScannedSequence: 2,
			Cursor: []byte("c0"), ScannedCursor: []byte("c2"),
			Records: []*leapmuxv1.LayoutEventRecord{
				tabOpened(1, "c1", "tab-1"),
				tabOpened(2, "c2", "tab-2"),
			},
		})
		require.NoError(t, err)
		assert.False(t, endReached)
		assert.True(t, limitReached)
		require.Len(t, persisted, 1)
		assert.Equal(t, int64(1), persisted[0].Sequence)
		assert.Equal(t, []byte("c1"), persisted[0].Cursor)
		assert.Equal(t, int64(1), consumer.position.Sequence)
		ev := decodeEventLine(t, out.String())
		assert.Equal(t, "tab-1", ev.TabID)
	})

	t.Run("trailing filtered records advance cursor to ScannedCursor", func(t *testing.T) {
		var out bytes.Buffer
		filter, err := parseKindFilter([]string{kindTabOpened})
		require.NoError(t, err)
		var persisted []*eventHistoryPosition
		consumer := &layoutPageConsumer{
			filter:     filter,
			printer:    newEventPrinter(&cli.Printer{Out: &out, Err: io.Discard, Format: cli.FormatJSON}, filter, 0),
			clock:      testutil.NewQuartzMock(t),
			position:   &eventHistoryPosition{JournalID: "hub", Sequence: 1, Cursor: []byte("c1")},
			replayHigh: 4,
			persist: func(pos *eventHistoryPosition) error {
				cp := *pos
				persisted = append(persisted, &cp)
				return nil
			},
		}
		endReached, limitReached, err := consumer.consumePage(&leapmuxv1.ReadLayoutEventsResponse{
			JournalId: "hub", AfterSequence: 1, HighWaterSequence: 4, ScannedSequence: 4,
			Cursor: []byte("c1"), ScannedCursor: []byte("c4"),
		})
		require.NoError(t, err)
		assert.False(t, endReached)
		assert.False(t, limitReached)
		assert.Empty(t, out.String())
		require.Len(t, persisted, 1)
		assert.Equal(t, int64(4), persisted[0].Sequence)
		assert.Equal(t, []byte("c4"), persisted[0].Cursor)
		assert.Equal(t, int64(4), consumer.position.Sequence)
		assert.Equal(t, []byte("c4"), consumer.position.Cursor)
	})
}
