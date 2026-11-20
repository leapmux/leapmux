package cmd

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
)

const checkpointTerminalType = leapmuxv1.TabType_TAB_TYPE_TERMINAL

func validCheckpoint() *eventCheckpoint {
	return &eventCheckpoint{
		Version: eventCheckpointVersion, WorkerID: "worker", TabID: "terminal", TabType: "terminal",
		JournalID: "journal", TabStreamID: "stream", Sequence: 3, Cursor: []byte("cursor at 3"), Filter: []string{kindTerminalBell},
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
	t.Parallel()
	path := filepath.Join(t.TempDir(), "position.json")
	first, err := openEventCheckpoint(path)
	require.NoError(t, err)
	t.Cleanup(first.close)
	state, err := first.read()
	require.NoError(t, err)
	require.Nil(t, state)

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

func TestEventCheckpointDataCannotReplaceAnotherCheckpointLock(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	for name, content := range map[string][]byte{
		"empty":             nil,
		"null":              []byte("null"),
		"oversize":          bytes.Repeat([]byte("x"), maxEventCheckpointBytes+1),
		"unknown field":     []byte(`{"unknown":true}`),
		"negative sequence": []byte(`{"version":1,"worker_id":"w","tab_type":"terminal","tab_id":"t","journal_id":"j","tab_stream_id":"s","kinds":[],"sequence":-1,"cursor":"YQ=="}`),
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
		tab:     scopedTab{ID: state.TabID, WorkerID: state.WorkerID, Type: checkpointTerminalType},
		printer: &cli.Printer{Out: destination, Err: io.Discard, Format: cli.FormatJSON},
		state:   state, kinds: state.Filter,
	}
}

type shortEventWriter struct{}

func (shortEventWriter) Write(data []byte) (int, error) { return len(data) - 1, nil }

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
	assert.Zero(t, replay.printed)
}

func TestCheckpointedEventsWritesBeforeCheckpointingAndCountsOnlyAfterSave(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	replay := makeCheckpointedEvents(t, &output)
	checkpointFailure := errors.New("directory changed")
	replay.save = func(state *eventCheckpoint) error {
		assert.Contains(t, output.String(), `"sequence":4,"replayed":true`)
		assert.Equal(t, int64(4), state.Sequence)
		assert.Zero(t, replay.printed)
		return checkpointFailure
	}
	after, err := replay.acceptFrame(checkpointBell(4), 3, 4)
	require.ErrorIs(t, err, checkpointFailure)
	assert.Equal(t, int64(3), after)
	assert.Equal(t, validCheckpoint(), replay.state)
	assert.Zero(t, replay.printed)

	output.Reset()
	replay.save = func(state *eventCheckpoint) error {
		assert.Contains(t, output.String(), `"kind":"terminal.bell"`)
		assert.Equal(t, []byte("next signed position"), state.Cursor)
		return nil
	}
	after, err = replay.acceptFrame(checkpointBell(4), 3, 3)
	require.NoError(t, err)
	assert.Equal(t, int64(4), after)
	assert.Equal(t, int64(4), replay.state.Sequence)
	assert.Equal(t, 1, replay.printed)
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

func TestCheckpointedEventsPersistsVisibleGapsWithoutUsingTheLimit(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	replay := makeCheckpointedEvents(t, io.Discard)
	replay.printer.Err = &output
	replay.save = func(state *eventCheckpoint) error {
		assert.Contains(t, output.String(), "terminal events expired")
		assert.Equal(t, int64(10), state.Sequence)
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
	assert.Zero(t, replay.printed)
}

func TestCheckpointedEventsChecksAndSavesFilteredPositions(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	replay := makeCheckpointedEvents(t, &output)
	saves := 0
	replay.save = func(state *eventCheckpoint) error {
		saves++
		assert.Equal(t, int64(4), state.Sequence)
		assert.Equal(t, []byte("skipped position"), state.Cursor)
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
	assert.Equal(t, int64(4), replay.state.Sequence)
	assert.Equal(t, 1, saves)
	assert.Empty(t, output.String())
	assert.Zero(t, replay.printed)
}
