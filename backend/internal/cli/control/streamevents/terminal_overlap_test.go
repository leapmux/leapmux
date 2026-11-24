package streamevents

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func TestTerminalReplayUsesTheObservedByteRange(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		initial   int64
		frames    []*leapmuxv1.TerminalData
		delivered []string
		offset    int64
		resets    []string
	}{
		{
			name: "overlap", initial: 5,
			frames: []*leapmuxv1.TerminalData{
				{Data: []byte("hello"), EndOffset: 5},
				{Data: []byte("lo world"), EndOffset: 11},
				{Data: []byte("world"), EndOffset: 11},
			},
			delivered: []string{" world"}, offset: 11,
		},
		{
			name: "snapshot reset", initial: 11,
			frames: []*leapmuxv1.TerminalData{
				{Data: []byte("\x1b[?25lXY"), EndOffset: 2, IsSnapshot: true},
				{Data: []byte("Z"), EndOffset: 3},
			},
			delivered: []string{"\x1b[?25lXY", "Z"}, offset: 3, resets: []string{"terminal-1"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			const terminalID = "terminal-1"
			cursor := NewTerminalCursor()
			cursor.Track(terminalID, tc.initial)
			var delivered, resets []string
			sub := NewSubscription(&fakeTransport{}, NewAgentCursor(), cursor, nil,
				func(event *leapmuxv1.TerminalEvent) { delivered = append(delivered, string(event.GetData().GetData())) },
				func(id string) { resets = append(resets, id) })
			t.Cleanup(sub.Cancel)
			for _, data := range tc.frames {
				original := string(data.GetData())
				sub.dispatchTerminalEvent(&leapmuxv1.TerminalEvent{
					TerminalId: terminalID, Event: &leapmuxv1.TerminalEvent_Data{Data: data},
				})
				assert.Equal(t, original, string(data.GetData()), "the subscriber preserves the original frame")
			}
			assert.Equal(t, tc.delivered, delivered)
			assert.Equal(t, tc.offset, cursor.Get(terminalID))
			require.Equal(t, tc.resets, resets)
		})
	}
}
