package cmd

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
)

type terminalScreenPosition struct {
	epoch    string
	offset   int64
	revision uint64
}

func (w *screenWatch) replayScreenTimeline(data *leapmuxv1.TerminalData, observe func() (bool, error)) error {
	timeline := data.GetScreenTimeline()
	if timeline.GetHistoryTruncated() {
		return w.screenHistoryUnavailable()
	}
	base := timeline.GetBase()
	start, end := timeline.GetStartOffset(), data.GetEndOffset()
	remaining := data.GetData()
	if base.GetEpoch() == "" || base.GetRevision() == 0 || base.GetOffset() < 0 || base.GetOffset() > start ||
		start < 0 || end < start || end-start != int64(len(remaining)) {
		return cli.Newf(cli.CodeInternal, "the worker returned an invalid screen timeline for terminal %s", w.id)
	}
	from := terminalScreenPosition{epoch: base.GetEpoch(), offset: start, revision: base.GetRevision()}
	replacement := data.GetIsSnapshot()
	if replacement {
		if w.position.epoch != "" && w.position.offset != 0 && !w.scrollback {
			return w.screenHistoryUnavailable()
		}
		cols, rows, err := renderDimensions(w.id, base.GetCols(), base.GetRows())
		if err != nil {
			return err
		}
		w.cols, w.rows = cols, rows
		if err := w.reset(timeline.GetModePrefix()); err != nil {
			return err
		}
		w.position = from
	} else if from != w.position || int(base.GetCols()) != w.cols || int(base.GetRows()) != w.rows || len(timeline.GetModePrefix()) != 0 {
		return w.screenHistoryUnavailable()
	}
	for _, change := range timeline.GetChanges() {
		offset := change.GetOffset()
		if offset < w.position.offset || offset > end {
			return cli.Newf(cli.CodeInternal, "the worker returned a resize outside the screen timeline for terminal %s", w.id)
		}
		length := int(offset - w.position.offset)
		if err := w.write(remaining[:length]); err != nil {
			return err
		}
		remaining = remaining[length:]
		w.position.offset = offset
		if !replacement && length != 0 && observe != nil {
			if done, err := observe(); done || err != nil {
				return err
			}
		}
		if err := w.applyScreenGeometry(change, !replacement); err != nil {
			return err
		}
		if !replacement && observe != nil {
			if done, err := observe(); done || err != nil {
				return err
			}
		}
	}
	if err := w.write(remaining); err != nil {
		return err
	}
	w.position.offset = end
	if replacement {
		w.followInitialViewport()
	}
	return nil
}

func (w *screenWatch) applyLiveScreenGeometry(geometry *leapmuxv1.TerminalScreenGeometry) error {
	if geometry.GetEpoch() != "" && geometry.GetEpoch() != w.position.epoch && geometry.GetRevision() == 1 && geometry.GetOffset() >= 0 {
		cols, rows, err := renderDimensions(w.id, geometry.GetCols(), geometry.GetRows())
		if err != nil {
			return err
		}
		w.cols, w.rows = cols, rows
		w.position = terminalScreenPosition{epoch: geometry.GetEpoch(), offset: geometry.GetOffset(), revision: 1}
		return w.reset(nil)
	}
	return w.applyScreenGeometry(geometry, true)
}

func (w *screenWatch) applyScreenGeometry(geometry *leapmuxv1.TerminalScreenGeometry, preserveBoundary bool) (err error) {
	if w.position.epoch == "" || geometry.GetEpoch() != w.position.epoch || geometry.GetOffset() != w.position.offset ||
		geometry.GetRevision() == 0 || geometry.GetRevision() != w.position.revision+1 {
		return w.screenHistoryUnavailable()
	}
	cols, rows, err := renderDimensions(w.id, geometry.GetCols(), geometry.GetRows())
	if err != nil {
		return err
	}
	defer func() {
		if r := recover(); r != nil {
			err = cli.Newf(cli.CodeInternal, "the terminal emulator failed to resize terminal %s: %v", w.id, r)
		}
	}()
	if cols != w.cols || rows != w.rows {
		if preserveBoundary {
			if err := w.resizeScreenKeepingBoundary(cols, rows); err != nil {
				return err
			}
		} else {
			w.term.Resize(cols, rows)
		}
		w.cols, w.rows = cols, rows
	}
	w.position.revision = geometry.GetRevision()
	return nil
}

// resizeScreenKeepingBoundary follows the first row of the wrapped line. The
// row that held the boundary can disappear while its cells move to that row.
func (w *screenWatch) resizeScreenKeepingBoundary(cols, rows int) error {
	if w.scrollback || w.normal.Lines.Length() == 0 {
		w.term.Resize(cols, rows)
		w.follow(w.term.NormalBuffer(), 0)
		return nil
	}
	lines := w.normal.Lines
	anchor := w.boundary
	if anchor >= lines.Length() || lines.Get(anchor) == nil {
		return w.screenHistoryUnavailable()
	}
	for anchor > 0 && lines.Get(anchor).IsWrapped {
		anchor--
	}
	cells := w.boundaryColumn
	for y := anchor; y < w.boundary; y++ {
		cells += wrappedRowLength(lines.Get(y), lines.Get(y+1))
	}
	marker := w.normal.AddMarker(anchor)
	defer marker.Dispose()
	w.term.Resize(cols, rows)
	normal := w.term.NormalBuffer()
	if normal != w.normal {
		return w.screenHistoryUnavailable()
	}
	if marker.IsDisposed {
		if normal.Lines.Length() != 0 && normal.Lines.Get(0).IsWrapped {
			// Part of the boundary's logical line survived a trim. The marker
			// no longer tells us which cells of that line preceded the wait.
			return w.screenHistoryUnavailable()
		}
		w.follow(normal, 0)
		return nil
	}
	row := marker.Line
	for row+1 < normal.Lines.Length() {
		next := normal.Lines.Get(row + 1)
		if next == nil || !next.IsWrapped {
			break
		}
		width := wrappedRowLength(normal.Lines.Get(row), next)
		if cells < width {
			break
		}
		cells -= width
		row++
	}
	w.follow(normal, row)
	w.boundaryColumn = min(cells, cols)
	return nil
}

func (w *screenWatch) screenHistoryUnavailable() error {
	return cli.Newf(cli.CodeUnavailable, "the worker could not supply enough screen history to render terminal %s", w.id)
}

func (w *screenWatch) consumeScreenOutput(data *leapmuxv1.TerminalData, observe func() (bool, error)) error {
	if data.GetScreenTimeline().GetBase() != nil {
		return w.replayScreenTimeline(data, observe)
	}
	end, output := data.GetEndOffset(), data.GetData()
	if data.GetScreenEpoch() != w.position.epoch || data.GetGeometryRevision() != w.position.revision ||
		w.position.epoch != "" && (data.GetIsSnapshot() || end < w.position.offset || end-w.position.offset != int64(len(output))) {
		return w.screenHistoryUnavailable()
	}
	w.position.offset = end
	if data.GetIsSnapshot() {
		return w.reset(output)
	}
	return w.write(output)
}
