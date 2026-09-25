package codewhale

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

// codewhaleProvider is the stateless wire-format plugin for Codewhale. It
// answers the questions the service layer asks without a running agent.
//
// It embeds ProviderDefaults for every question Codewhale answers the neutral
// way. Among them:
//
//   - IsSelfDisplayingControlTool: the runtime never echoes an answer into its
//     stream, so the service's own answer row is the record.
//   - PlanModeControl: Codewhale has no plan approval. Plan mode is a thread
//     setting, the runtime refuses every mutating call in it, and no tool asks
//     to leave it.
//   - ResolveResumeHandle: a thread id (`thr_` and eight hex digits) is a token.
//   - TurnEndToolUses: the turn end carries the worker's own tool count, under
//     the key the default reads.
//   - EndsSubagentTranscript: a child transcript simply stops.
//   - SupportsChildSteering: only the model steers a child, through its own
//     `agent` tool.
type codewhaleProvider struct {
	agent.ProviderDefaults
}

// Classify groups the runtime's status notices, which repeat while the
// runtime works through one turn, so a thread of them shows the latest.
func (codewhaleProvider) Classify(raw json.RawMessage) agent.NotificationClassification {
	env, ok := parseEnvelope(raw)
	if !ok || env.Event != contracts.CodewhaleEventItemCompleted {
		return agent.NotificationClassification{}
	}
	var payload itemEventPayload
	if json.Unmarshal(env.Payload, &payload) != nil || payload.Item.Kind != contracts.CodewhaleItemKindStatus {
		return agent.NotificationClassification{}
	}
	return agent.NotificationClassification{Kind: agent.NotificationKindStatus, Key: codewhaleProviderName + ":status"}
}

// IsInterrupt recognizes the interrupt frame that SendRawInput runs. The
// frontend interrupts through the InterruptAgent RPC; this serves a raw frame
// from another caller.
func (codewhaleProvider) IsInterrupt(content string) bool {
	var frame struct {
		Frame string `json:"frame"`
	}
	if err := json.Unmarshal([]byte(content), &frame); err != nil {
		return false
	}
	return frame.Frame == contracts.CodewhaleReplyFrameInterrupt
}

// ValidateAttachment enforces the static half of Codewhale's attachment
// policy: text and images, nothing else. The model-dependent image check runs
// in SendInput. See attachments.go.
func (codewhaleProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	return validateCodewhaleAttachment(attachment)
}

// ListStoredSessions reads LeapMux's own Codewhale stores. See sessions.go.
func (codewhaleProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return codewhaleStoredSessions(ctx, q)
}

// ExtractTodoEvent reads the to-do list off a finished `todo_write` call. See
// todo.go.
func (codewhaleProvider) ExtractTodoEvent(spanType string, content []byte, _ func() []byte) (todoevents.Event, bool) {
	return extractCodewhaleTodoEvent(spanType, content)
}

// ResolveControlResponse turns the reader's neutral answer into the reply
// frame that SendRawInput posts. See resolveControlReply.
//
// A response it cannot address is WITHHELD rather than forwarded: the runtime
// keeps waiting, and the reader can answer again.
func (codewhaleProvider) ResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	res := agent.DefaultControlResponseResolution(ctx)
	if len(ctx.RequestPayload) == 0 {
		res.Withhold = true
		return res
	}
	if !providerkit.WarnUnmarshal(ctx.RequestPayload, new(map[string]json.RawMessage), "codewhale control response request") {
		res.Withhold = true
		return res
	}
	frame, feedback, ok := resolveControlReply(ctx.RequestPayload, ctx.ResponseContent)
	if !ok {
		res.Withhold = true
		return res
	}
	encoded, err := json.Marshal(frame)
	if err != nil {
		slog.Warn("codewhale marshal a reply frame", "error", err)
		res.Withhold = true
		return res
	}
	res.Content = encoded
	res.Feedback = feedback
	return res
}
