package terminal

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScreenReplayCanResumeSeveralResizesAtOneByteOffset(t *testing.T) {
	t.Parallel()
	buffer := NewScreenBuffer()
	owner := buffer.beginGeometry(6, 2, nil)
	buffer.Write([]byte("abc"))
	initial, ok := buffer.ScreenReplaySince("", 0, 0)
	require.True(t, ok)
	require.True(t, initial.Snapshot)
	assert.Equal(t, uint16(6), initial.Timeline.Base.Cols)
	assert.Equal(t, int64(3), initial.EndOffset)

	var observed []ScreenGeometry
	deliver := func(output Output) {
		assert.Empty(t, output.Data)
		assert.Equal(t, int64(3), output.EndOffset)
		require.NotNil(t, output.Resize)
		observed = append(observed, *output.Resize)
	}
	resize := func() error { return nil }
	require.NoError(t, buffer.resizeWithGeometry(owner, 9, 2, resize, deliver))
	require.NoError(t, buffer.resizeWithGeometry(owner, 12, 2, resize, deliver))
	require.NoError(t, buffer.resizeWithGeometry(owner, 12, 2, resize, deliver))
	require.Len(t, observed, 2)
	assert.Equal(t, uint64(2), observed[0].Revision)
	assert.Equal(t, uint64(3), observed[1].Revision)
	assert.Equal(t, uint16(12), observed[1].Cols)

	replay, ok := buffer.ScreenReplaySince(initial.Timeline.Base.Epoch, 3, 1)
	require.True(t, ok)
	assert.False(t, replay.Snapshot)
	assert.Empty(t, replay.Data)
	assert.Equal(t, observed, replay.Timeline.Changes)
	replay, ok = buffer.ScreenReplaySince(initial.Timeline.Base.Epoch, 3, 2)
	require.True(t, ok)
	assert.False(t, replay.Snapshot)
	assert.Equal(t, observed[0], replay.Timeline.Base)
	assert.Equal(t, observed[1:], replay.Timeline.Changes)
	buffer.WriteAndDeliver(owner, []byte("d"), func(output Output) {
		assert.Equal(t, "d", string(output.Data))
		assert.Equal(t, int64(4), output.EndOffset)
		assert.Equal(t, initial.Timeline.Base.Epoch, output.ScreenEpoch)
		assert.Equal(t, uint64(3), output.GeometryRevision)
	})
}

func TestScreenReplayUsesTheGeometryAtTheStartOfTheRetainedBytes(t *testing.T) {
	t.Parallel()
	buffer := NewScreenBuffer()
	buffer.buf = make([]byte, 5)
	owner := buffer.beginGeometry(6, 2, nil)
	buffer.Write([]byte("old\n"))
	require.NoError(t, buffer.resizeWithGeometry(owner, 12, 2, func() error { return nil }, nil))
	buffer.Write([]byte("abcdef"))
	replay, ok := buffer.ScreenReplaySince("", 4, 1)
	require.True(t, ok)
	assert.True(t, replay.Snapshot)
	assert.Equal(t, int64(5), replay.Timeline.StartOffset)
	assert.Equal(t, uint16(12), replay.Timeline.Base.Cols)
	assert.Equal(t, "bcdef", string(replay.Data))
	assert.Empty(t, replay.Timeline.Changes)
	replay, ok = buffer.ScreenReplaySince(replay.Timeline.Base.Epoch, 5, 1)
	require.True(t, ok)
	assert.True(t, replay.Snapshot, "a byte cursor past a missed resize requires a replacement")
}

func TestScreenResizePublishesOnlyASuccessfulCurrentResize(t *testing.T) {
	t.Parallel()
	buffer := NewScreenBuffer()
	oldOwner := buffer.beginGeometry(6, 2, nil)
	newOwner := buffer.beginGeometry(6, 2, nil)
	deliver := func(Output) { t.Fatal("this resize publishes no geometry") }
	native := func() error {
		t.Fatal("an obsolete instance cannot resize the current screen")
		return nil
	}
	assert.ErrorIs(t, buffer.resizeWithGeometry(oldOwner, 12, 2, native, deliver), ErrTerminalInstanceChanged)
	refused := errors.New("the PTY refused the size")
	assert.ErrorIs(t, buffer.resizeWithGeometry(newOwner, 12, 2, func() error { return refused }, deliver), refused)
	replay, ok := buffer.ScreenReplaySince("", 0, 0)
	require.True(t, ok)
	assert.Empty(t, replay.Timeline.Changes)
	assert.Equal(t, uint16(6), replay.Timeline.Base.Cols)
}

func TestScreenReplayKeepsAnObsoletePtysLateBytesOutOfTheNewScreen(t *testing.T) {
	t.Parallel()
	buffer := NewScreenBuffer()
	oldOwner := buffer.beginGeometry(6, 2, nil)
	lateOldOutput := func() {
		buffer.WriteAndDeliver(oldOwner, []byte("old\r\n"), func(Output) {})
	}
	newOwner := buffer.beginGeometry(12, 2, nil)
	lateOldOutput()
	buffer.WriteAndDeliver(newOwner, []byte("new"), func(Output) {})
	replay, ok := buffer.ScreenReplaySince("", 0, 0)
	require.True(t, ok)
	assert.Equal(t, "new", string(replay.Data))
	assert.Equal(t, int64(3), replay.EndOffset)
}

func TestScreenReplayReportsGeometryEvictionAheadOfRetainedBytes(t *testing.T) {
	t.Parallel()
	buffer := NewScreenBuffer()
	owner := buffer.beginGeometry(6, 2, nil)
	buffer.Write([]byte("READY"))
	for i := range retainedScreenResizeLimit + 1 {
		cols := uint16(7 + i%2)
		require.NoError(t, buffer.resizeWithGeometry(owner, cols, 2, func() error { return nil }, nil))
	}
	screen, offset, timeline := buffer.snapshotWithTimeline()
	assert.Equal(t, "READY", string(screen))
	assert.Equal(t, int64(5), offset)
	require.NotNil(t, timeline)
	assert.True(t, timeline.HistoryTruncated)
	replay, ok := buffer.ScreenReplaySince("", 0, 0)
	require.True(t, ok)
	assert.True(t, replay.Timeline.HistoryTruncated)
}
