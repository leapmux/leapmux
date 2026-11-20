package service

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

func terminalScreenGeometryProto(value terminal.ScreenGeometry) *leapmuxv1.TerminalScreenGeometry {
	return &leapmuxv1.TerminalScreenGeometry{
		Epoch: value.Epoch, Offset: value.Offset, Revision: value.Revision,
		Cols: uint32(value.Cols), Rows: uint32(value.Rows),
	}
}

func terminalScreenTimelineProto(timeline *terminal.ScreenTimeline) *leapmuxv1.TerminalScreenTimeline {
	if timeline == nil {
		return nil
	}
	result := &leapmuxv1.TerminalScreenTimeline{
		Base: terminalScreenGeometryProto(timeline.Base), StartOffset: timeline.StartOffset, ModePrefix: timeline.ModePrefix,
		HistoryTruncated: timeline.HistoryTruncated,
		Changes:          make([]*leapmuxv1.TerminalScreenGeometry, 0, len(timeline.Changes)),
	}
	for _, change := range timeline.Changes {
		result.Changes = append(result.Changes, terminalScreenGeometryProto(change))
	}
	return result
}
