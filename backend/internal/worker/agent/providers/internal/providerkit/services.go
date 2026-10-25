package providerkit

import (
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// ToolSpanServices is the part of the provider services that OpenToolSpan uses.
type ToolSpanServices interface {
	agent.TranscriptServices
	agent.SpanServices
}

// GenerationServices is the part of the provider services that persists a
// finished generation and reports its progress.
type GenerationServices interface {
	agent.TranscriptServices
	agent.ProgressServices
}

// ToolLifecycleServices is the part of the provider services that closes a tool
// call: it persists the result row, reports progress, and closes the span.
type ToolLifecycleServices interface {
	GenerationServices
	agent.SpanServices
}

// OpenToolSpan persists a tool call and opens its span at the transcript root.
func OpenToolSpan(sink ToolSpanServices, content agent.MessageContent, spanID, spanType string, spawns bool) error {
	return OpenToolSpanWithParent(sink, content, spanID, spanType, "", spawns)
}

// OpenToolSpanWithParent persists a tool call and opens its span under the supplied parent.
// Reserve the color before persistence. Open the span after persistence, so the row has no connector to itself.
// A subagent spawn records its type but reserves no color and opens no span.
// A failed write still opens the span. The caller reports the error, and a later result can close the span.
// The provider decides whether the call spawns a subagent and whether its parent is open.
func OpenToolSpanWithParent(sink ToolSpanServices, content agent.MessageContent, spanID, spanType, parentSpanID string, spawns bool) error {
	var spanColor int32
	if !spawns {
		spanColor = sink.ReserveSpanColor(spanID, parentSpanID)
	}
	err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{
		SpanID: spanID, SpanType: spanType, ParentSpanID: parentSpanID, SpanColor: spanColor, NoSpan: spawns,
	})
	sink.SetSpanType(spanID, spanType)
	if !spawns {
		sink.OpenSpan(spanID, parentSpanID)
	}
	return err
}

// ScheduleOrCancelServerErrorAutoRetry schedules a server-error automatic
// retry when retry is true, or cancels any pending one otherwise. The queue
// derives the deadline from its durable backoff; the provider supplies no
// instant of its own. The payload is defensively copied because the caller's
// buffer may be reused by the stdout reader before the schedule is consumed.
func ScheduleOrCancelServerErrorAutoRetry(sink agent.AutoRetryServices, retry bool, payload []byte) {
	if !retry {
		sink.CancelServerErrorRetry()
		return
	}
	sink.ScheduleServerErrorRetry(append([]byte(nil), payload...))
}

// LogRegistryRefusal records a background-task write the registry REFUSED.
//
// `bgtask.ValidateRowKey` turned an unusable provider key from a silent rewrite
// into an error, so every one of these writes gained a failure mode it did not
// have before -- and every provider takes its key straight from the agent's own
// JSON with no length limit of its own. A bare `_ =` therefore meant a refused
// row simply never appeared in the sidebar, or a finished subagent never left
// the Running state, with nothing anywhere to say why: the failure mode the
// refusal was chosen to AVOID, moved from the data to the diagnosis.
//
// It lives here, beside the sink interface, rather than once per provider. The
// error belongs to the SINK's rule and not to any provider's wire format, and a
// helper each provider writes for itself is one the next provider forgets --
// which is what happened: Pi grew one and the other three did not.
//
// The write stays best-effort. A refused row must not fail the event around it.
func LogRegistryRefusal(provider, op string, err error) {
	if err != nil {
		slog.Warn("background task write refused", "provider", provider, "op", op, "error", err)
	}
}
