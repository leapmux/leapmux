package providerkittest

import (
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// TurnEndAndRetryOps returns the turn-end rows and the retry operations of a
// timeline, in call order. It leaves out every other row, so a test of a
// turn-failure verdict states only the order that the verdict decides.
func TurnEndAndRetryOps(timeline []agenttest.TimelineEntry) []agenttest.TimelineEntry {
	var out []agenttest.TimelineEntry
	for _, entry := range timeline {
		switch entry.Op {
		case "message", "notification", "leapmux_notification":
			continue
		}
		out = append(out, entry)
	}
	return out
}

// TurnEndThen is the timeline of one turn end that failure decides: the
// turn-end row, then exactly the retry operations of the verdict.
func TurnEndThen(failure providerkit.TurnFailure) []agenttest.TimelineEntry {
	reference := &agenttest.Sink{}
	providerkit.ApplyTurnFailure(reference, failure, nil)
	return append([]agenttest.TimelineEntry{{Op: "turn_end"}}, reference.Timeline()...)
}

// TurnEndAndRetryOpNames lists the operations that TurnEndAndRetryOps keeps,
// in call order. A usage-limit schedule also states its reset instant, as
// `schedule_usage_limit@<RFC 3339 UTC>`.
func TurnEndAndRetryOpNames(timeline []agenttest.TimelineEntry) []string {
	return retryOperationNames(TurnEndAndRetryOps(timeline))
}

// TurnEndRetryAndIdleOpNames lists stored turn ends, retry operations, and idle
// publications from the sink's complete operation log. Failed write attempts
// do not establish a stored turn end.
func TurnEndRetryAndIdleOpNames(operations []agenttest.TimelineEntry) []string {
	var selected []agenttest.TimelineEntry
	for _, entry := range operations {
		switch entry.Op {
		case "turn_end_stored":
			entry.Op = "turn_end"
		case "turn_active":
			if entry.Active {
				continue
			}
			entry.Op = "idle"
		case "schedule_usage_limit", "schedule_server_error", "cancel_usage_limit", "cancel_server_error":
		default:
			continue
		}
		selected = append(selected, entry)
	}
	return retryOperationNames(selected)
}

func retryOperationNames(operations []agenttest.TimelineEntry) []string {
	var names []string
	for _, entry := range operations {
		name := entry.Op
		if entry.Op == "schedule_usage_limit" {
			name += "@" + entry.ResetAt.UTC().Format(time.RFC3339Nano)
		}
		names = append(names, name)
	}
	return names
}
