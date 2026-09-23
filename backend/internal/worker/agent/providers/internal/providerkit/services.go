package providerkit

import (
	"log/slog"
	"time"

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

// OpenToolSpan persists a tool call's opening row and opens its span.
//
// A call that spawns a subagent owns no span: it reserves no color and opens
// nothing, so it draws no rail and its card takes the neutral border. It still
// records its span type, which a provider's closing message reads back.
//
// Each provider decides `spawns` itself, from its own wire shape -- that
// decision must not move into shared code. What lives here is the ORDER the
// decision drives, which every provider needs identically: reserve before the
// persist so the row carries the color its rail will use, persist before the
// open so the row sits at the parent's depth, record the type either way.
//
// The parent span id is empty at every call site: a provider's tool calls are
// flat in the transcript that holds them.
//
// A failed persist is logged by the caller and does NOT stop the span from
// opening, which is what each of these call sites did before.
func OpenToolSpan(sink ToolSpanServices, content agent.MessageContent, spanID, spanType string, spawns bool) error {
	var spanColor int32
	if !spawns {
		spanColor = sink.ReserveSpanColor(spanID, "")
	}
	err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{
		SpanID: spanID, SpanType: spanType, SpanColor: spanColor, NoSpan: spawns,
	})
	sink.SetSpanType(spanID, spanType)
	if !spawns {
		sink.OpenSpan(spanID, "")
	}
	return err
}

// ScheduleOrCancelAPIErrorAutoContinue schedules an immediate API-error
// auto-continue when retry is true, or cancels any pending API-error
// schedule otherwise. The payload is defensively copied because the
// caller's buffer may be reused by the stdout reader before the schedule
// is consumed.
func ScheduleOrCancelAPIErrorAutoContinue(sink agent.AutoContinueServices, retry bool, payload []byte) {
	if !retry {
		sink.CancelAutoContinue(agent.AutoContinueReasonAPIError)
		return
	}
	sink.ScheduleAutoContinue(agent.AutoContinueSchedule{
		Reason:        agent.AutoContinueReasonAPIError,
		DueAt:         time.Now().UTC(),
		SourcePayload: append([]byte(nil), payload...),
	})
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
