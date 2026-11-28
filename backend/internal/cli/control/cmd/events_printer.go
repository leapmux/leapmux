package cmd

import (
	"github.com/leapmux/leapmux/internal/cli"
)

// eventPrinter prints the events that --kind selects on stdout, and tracks
// --limit. Unselected stream.* notices print on stderr so a filter never hides
// a gap or outage.
type eventPrinter struct {
	p      *cli.Printer
	filter kindFilter
	// limit ends the stream after this many counted events. Zero runs without a limit.
	limit int
	count int
}

func newEventPrinter(printer *cli.Printer, filter kindFilter, limit int) *eventPrinter {
	return &eventPrinter{p: printer, filter: filter, limit: limit}
}

// print prints event for a live stream, increments the printed count for
// selected non-heartbeat events, and reports whether --limit was reached.
func (e *eventPrinter) print(event streamEvent) (limitReached bool, err error) {
	if event.Kind == kindStreamHeartbeat {
		return false, e.writeSelected(event)
	}
	if !e.filter.selects(event.Kind) {
		if kindFamily(event.Kind) == "stream" {
			return false, e.writeNotice(event)
		}
		return false, nil
	}
	if err := e.writeSelected(event); err != nil {
		return false, err
	}
	return e.recordCounted(), nil
}

// printCheckpointed writes a selected event to stdout or an unselected
// stream.* notice to stderr without incrementing the printed count yet, so the
// caller can persist the checkpoint before counting the event.
func (e *eventPrinter) printCheckpointed(event streamEvent) (selected bool, err error) {
	if event.Kind == kindStreamHeartbeat {
		return false, e.writeSelected(event)
	}
	if !e.filter.selects(event.Kind) {
		if kindFamily(event.Kind) == "stream" {
			return false, e.writeNotice(event)
		}
		return false, nil
	}
	if err := e.writeSelected(event); err != nil {
		return false, err
	}
	return true, nil
}

// recordCounted increments the counted event total after a durable write and
// reports whether --limit was reached.
func (e *eventPrinter) recordCounted() (limitReached bool) {
	e.count++
	return e.limitReached()
}

// printed returns the number of counted events printed so far.
func (e *eventPrinter) printed() int {
	return e.count
}

// limitReached reports whether --limit has been reached.
func (e *eventPrinter) limitReached() bool {
	return e.limit > 0 && e.count >= e.limit
}

func (e *eventPrinter) writeSelected(event streamEvent) error {
	return e.p.Event(event)
}

func (e *eventPrinter) writeNotice(event streamEvent) error {
	return e.p.Notice("%s", cli.SanitizeSingleLine(event.TextLine()))
}
