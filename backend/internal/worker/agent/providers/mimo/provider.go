package mimo

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

// mimoProvider is MiMo Code's stateless plugin.
type mimoProvider struct {
	agent.ProviderDefaults
}

// ValidateAttachment accepts text, images and PDF files, and refuses any other
// binary. See attachments.go for how each kind reaches MiMo, and why a binary
// of an unknown type cannot.
func (mimoProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	if attachment.Kind == agent.AttachmentKindBinary {
		return fmt.Errorf("MiMo Code does not support binary attachments: %s", attachment.Filename)
	}
	return nil
}

// ListStoredSessions reads MiMo's own session database; see sessions.go.
func (mimoProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return mimoStoredSessions(ctx, q)
}

// SupportsChildSteering is true: a message to a running subagent reaches it
// through the same prompt route, addressed by its actor id, and joins its turn.
// The capability is static, so a subagent's tab keeps its composer while the
// subagent is idle, and Agent.SendChildInput refuses that message with the
// reason.
func (mimoProvider) SupportsChildSteering() bool { return true }

// PlanModeControl classifies MiMo's plan approval, which the worker records
// under the plan tool's own name, as the exit from plan mode.
func (mimoProvider) PlanModeControl(toolName string) agent.PlanModeControlKind {
	if toolName == contracts.MiMoToolPlanExit {
		return agent.PlanModeControlExit
	}
	return agent.PlanModeControlNone
}

// PlanModePermissionMode names the agents an approved transition moves the
// session to: build after a plan, plan for an entry. MiMo has no tool that
// enters plan mode, and the entry answer states the axis's plan value for
// completeness.
func (mimoProvider) PlanModePermissionMode(kind agent.PlanModeControlKind) string {
	switch kind {
	case agent.PlanModeControlEnter:
		return contracts.MiMoModePlan
	case agent.PlanModeControlExit:
		return contracts.MiMoModeBuild
	default:
		return ""
	}
}

// ResolveControlResponse checks a browser answer against its stored request.
//
// It changes no byte. The answer keeps the shape the browser wrote, because
// the persisted answer row shows that shape and the browser labels it, and
// SendRawInput reads the same shape when it sends the answer to MiMo. What this
// adds is the refusal: an answer that does not fit its request is withheld, so
// MiMo keeps waiting and the user can answer again.
func (mimoProvider) ResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	res := agent.DefaultControlResponseResolution(ctx)
	if len(ctx.RequestPayload) == 0 {
		// The request is gone, so there is nothing to send the answer to.
		res.Withhold = true
		return res
	}
	kind, ok := controlKindOfPayload(ctx.RequestPayload)
	if !ok {
		slog.Warn("mimo control response names an unreadable request", "request_id", ctx.RequestID)
		res.Withhold = true
		return res
	}
	if kind == controlPlan {
		res.PlanModeControl = agent.PlanModeControlExit
	}
	answer, err := readControlAnswer(kind, ctx.RequestPayload, ctx.ResponseContent)
	if err != nil {
		slog.Warn("mimo control response does not fit its request", "request_id", ctx.RequestID, "error", err)
		res.Withhold = true
		return res
	}
	if ctx.RequestID != "" && answer.requestID != ctx.RequestID {
		slog.Warn("mimo control response addressed another request", "answered", answer.requestID, "stored", ctx.RequestID)
		res.Withhold = true
	}
	return res
}

// Classify groups MiMo's notifications in a thread: the retry attempts of one
// turn fold into the latest, and a compaction's start gives way to its end.
func (mimoProvider) Classify(raw json.RawMessage) agent.NotificationClassification {
	event, ok := parseEvent(raw)
	if !ok {
		return agent.NotificationClassification{}
	}
	switch event.Type {
	case contracts.MiMoEventSessionStatus:
		var payload mimoStatusEvent
		if json.Unmarshal(event.Properties, &payload) == nil && payload.Status.Type == contracts.MiMoStatusTypeRetry {
			return agent.NotificationClassification{Kind: agent.NotificationKindAPIRetry, Key: "mimo:retry"}
		}
	case contracts.MiMoEventMessagePartUpdated:
		var payload mimoPartEvent
		if json.Unmarshal(event.Properties, &payload) != nil || payload.Part.Type != contracts.MiMoPartTypeCompaction {
			return agent.NotificationClassification{}
		}
		if !compactionEndedIn(payload.Part) {
			// The start is a status: the boundary that ends the compaction clears it.
			return agent.NotificationClassification{Kind: agent.NotificationKindStatus, Key: "mimo:compaction"}
		}
		return agent.NotificationClassification{Kind: agent.NotificationKindCompactionBoundary, Key: "mimo:compaction"}
	}
	return agent.NotificationClassification{}
}

// ExtractTodoEvent reads a `task` call's result; see todo.go.
func (mimoProvider) ExtractTodoEvent(spanType string, content []byte, _ func() []byte) (todoevents.Event, bool) {
	return extractTaskTodo(spanType, content)
}
