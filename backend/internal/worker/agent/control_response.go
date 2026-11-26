package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
)

// ControlResponseContext is the pure provider input for interpreting a
// control response. The service loads the stored control request once, extracts the
// shared metadata, and passes both payloads here; providers must not perform I/O.
type ControlResponseContext struct {
	// RequestID is the key of the stored request, and it is never empty. The
	// Worker resolves only an answer that addresses this key
	// (Provider.ControlResponseRequestID), so a provider does not check the
	// answer's own id against it. The Worker also resolves only a stored
	// payload that agrees with the key (StoredControlRequestAgrees), so a
	// provider does not check that agreement either.
	RequestID       string
	RequestPayload  json.RawMessage
	ResponseContent []byte
	ToolName        string
	PlanApproval    *leapmuxv1.PlanApprovalSettings
}

// ControlResponseResolution is the provider-owned interpretation of a control
// response. The service executes side effects from this plan: it persists the
// structured control-response row, deletes control requests, mutates plan-mode
// settings, and forwards Content.
type ControlResponseResolution struct {
	Content []byte
	// FollowUp queues a separate user input when the native response cannot
	// carry the text safely.
	FollowUp ControlFollowUp
	// LeapMuxOwned states that LeapMux published the request, and no provider
	// request waits for an answer. The service never forwards Content to the
	// provider. A QUESTION_ANSWER follow-up requires it.
	LeapMuxOwned  bool
	SelfDisplayed bool
	// Withhold prevents forwarding a response that the provider cannot read.
	// Withhold covers that case alone. The service returns Refusal as the error
	// of the answer and keeps the request available for another answer.
	//
	// Set it through Refuse, which also states the reason that the browser shows.
	// A Withhold that is set directly states ControlResponseRefusedText.
	//
	// The service refuses an answer whose control request row is gone, and that
	// refusal runs before the service reads Withhold. A withheld resolution
	// therefore always describes a request that still exists.
	//
	// Empty Content cannot express this decision because the service restores the original response bytes.
	Withhold        bool
	PlanModeControl PlanModeControlKind
	// refusalReason states why the provider withholds the response. It is
	// unexported, so only Refuse sets it, and always together with Withhold.
	refusalReason string
}

// ControlFollowUp is the input that the worker queues after it records an
// answer. The zero value queues nothing.
type ControlFollowUp struct {
	// Kind is CONTROL_FEEDBACK or QUESTION_ANSWER.
	Kind leapmuxv1.AgentInputKind
	Text string
}

// FeedbackFollowUp queues rejection feedback that the native response cannot
// carry. An empty text gives the zero value.
func FeedbackFollowUp(text string) ControlFollowUp {
	if text == "" {
		return ControlFollowUp{}
	}
	return ControlFollowUp{Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK, Text: text}
}

// QuestionAnswerFollowUp queues the answer to a LeapMux-owned question. An
// empty text gives the zero value.
func QuestionAnswerFollowUp(text string) ControlFollowUp {
	if text == "" {
		return ControlFollowUp{}
	}
	return ControlFollowUp{Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER, Text: text}
}

// Valid reports whether the value is the zero value, or one of the two kinds
// with a non-empty text.
func (f ControlFollowUp) Valid() bool {
	if f.Kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_UNSPECIFIED {
		return f.Text == ""
	}
	return f.Text != "" && (f.Kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK ||
		f.Kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER)
}

// ControlResponseRefusedText is the reason that a withheld response states when
// its provider gives none. The browser shows it after "The response was not
// sent: ". It is the one copy of this text.
const ControlResponseRefusedText = "the agent provider could not read this control response"

// Reasons that several providers give for a refusal. Like every reason, each one
// completes the browser's sentence "The response was not sent: ...".
const (
	// RefusalUnreadableRequest: the stored control request does not decode, it
	// lacks a field that the reply needs, or its payload gives no key or another
	// key than the one that stores it (StoredControlRequestAgrees).
	RefusalUnreadableRequest = "LeapMux cannot read the stored request that this answer is for"
	// RefusalUnreadableAnswer: the answer does not decode into the fields that the
	// reply needs.
	RefusalUnreadableAnswer = "LeapMux cannot read this answer"
	// RefusalNoDecision: the answer is not the neutral decision envelope, or its
	// behavior is neither allow nor deny.
	RefusalNoDecision = "the answer states neither allow nor deny"
	// RefusalUnencodableReply: the reply to the agent cannot be encoded.
	RefusalUnencodableReply = "LeapMux cannot encode the reply to the agent"
	// RefusalAnswersDoNotFit: the answers of a question request do not match the
	// questions of the stored request.
	RefusalAnswersDoNotFit = "the answers do not fit the questions of the request"
)

// RefusalUnofferedOption states that a provider did not offer an option, in the
// provider's own display name: "Factory Droid did not offer proceed_always".
func RefusalUnofferedOption(provider leapmuxv1.AgentProvider, option string) string {
	return agentlabels.DisplayName(provider) + " did not offer " + option
}

// Refuse withholds the response and states why, in one sentence for the reader.
// An empty reason states ControlResponseRefusedText.
func (r *ControlResponseResolution) Refuse(reason string) {
	r.Withhold = true
	r.refusalReason = strings.TrimSpace(reason)
}

// Refusal is the error that the service returns for a withheld response: the
// provider's reason, or ControlResponseRefusedText when it gave none. It is nil
// for a response that the service forwards.
func (r ControlResponseResolution) Refusal() error {
	if !r.Withhold {
		return nil
	}
	if r.refusalReason != "" {
		return errors.New(r.refusalReason)
	}
	return errors.New(ControlResponseRefusedText)
}

func DefaultControlResponseResolution(ctx ControlResponseContext) ControlResponseResolution {
	return restoreControlResponseID(ctx)
}

// ControlResponseRequestID reads the stored-control-request lookup id from a raw
// control response. It tries the neutral approve/reject envelope first
// (DecodeControlBehavior: {response:{request_id, ...}}, which ControlResponseEnvelope writes) and
// falls back to a top-level JSON-RPC id (ExtractJSONRPCID, used by the ACP family and Codex). When
// a payload carries BOTH -- a top-level id AND a nested response.request_id -- the nested envelope
// id wins, because the pending control_request row is keyed by response.request_id. Returns "" when
// neither shape yields a non-empty id. Shared by every provider's ControlResponseRequestID; no
// provider narrows it, since narrowing to one shape would drop the other's real flows.
//
// The JSON-RPC branch returns the id TEXT, and it must not canonicalize it. An answer carries
// the worker's own request id -- the "jsonrpc:"-prefixed key PublishControlRequest stored the row
// under -- as a JSON STRING in that field (JSONRPCResult). So the text already IS the stored
// key, and running it through
// NewControlRequestIdentity would prefix it a second time and match nothing.
func (ProviderDefaults) ControlResponseRequestID(content []byte) string {
	if requestID, _, _, ok := DecodeControlBehavior(content); ok && requestID != "" {
		return requestID
	}
	if _, requestID, ok := ExtractJSONRPCID(content); ok {
		return requestID
	}
	return ""
}

func (ProviderDefaults) ResolveControlResponse(ctx ControlResponseContext) ControlResponseResolution {
	return DefaultControlResponseResolution(ctx)
}

// ControlBehaviorEnvelope describes the shared approval and rejection fields.
// The decoder and plan service use this type. LeapMux plan settings remain outside provider JSON.
type ControlBehaviorEnvelope struct {
	Response struct {
		RequestID string `json:"request_id"`
		Response  struct {
			Behavior string `json:"behavior"`
			Message  string `json:"message"`
			// Choice is the id of one of the choices a control request offered
			// beside its plain approve and reject: a plan's own approach, a
			// refusal that also ends plan mode. Empty for a plain decision.
			// AnswerControlRequest writes it (ControlBehaviorResponse.Choice), and each
			// provider maps the id onto its own wire answer.
			Choice string `json:"choice"`
		} `json:"response"`
	} `json:"response"`
}

// DecodeControlChoice returns the trimmed choice a control response carries
// beside its behavior, or "" for a plain decision and for bytes that are not
// JSON. See ControlBehaviorEnvelope.
func DecodeControlChoice(content []byte) string {
	var cr ControlBehaviorEnvelope
	if err := json.Unmarshal(content, &cr); err != nil {
		return ""
	}
	return strings.TrimSpace(cr.Response.Response.Choice)
}

// DecodeControlUpdatedInput returns the modified tool input a control response
// carries beside its behavior, or nil when it carries none.
//
// It is the reader for the `updatedInput` that AllowResponse writes: an
// answer sends the whole tool input back with its changes -- an
// AskUserQuestion answer folds its answers into that input
// (providerkit.AskUserQuestionAnswers) -- and a provider that forwards a
// modified input to its own CLI reads it here. DecodeControlBehavior leaves it alone, so its
// callers keep the shape they had.
//
// A present but non-object value reads as absent, because a tool input is an
// object and anything else is a payload this reader must not hand on as one.
func DecodeControlUpdatedInput(content []byte) map[string]any {
	var cr struct {
		Response struct {
			Response struct {
				UpdatedInput json.RawMessage `json:"updatedInput"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(content, &cr); err != nil {
		return nil
	}
	var updated map[string]any
	if err := json.Unmarshal(cr.Response.Response.UpdatedInput, &updated); err != nil {
		return nil
	}
	return updated
}

// DecodeControlInnerResponse returns the raw inner response object carried by
// the neutral control-response envelope, or "{}" when absent or unreadable.
func DecodeControlInnerResponse(content []byte) json.RawMessage {
	var envelope struct {
		Response struct {
			Response json.RawMessage `json:"response"`
		} `json:"response"`
	}
	if json.Unmarshal(content, &envelope) != nil {
		return json.RawMessage(`{}`)
	}
	trimmed := bytes.TrimSpace(envelope.Response.Response)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return json.RawMessage(`{}`)
	}
	return envelope.Response.Response
}

// DecodeControlBehavior decodes the neutral approve/reject control-response envelope
// (ControlBehaviorEnvelope), returning the trimmed request id, behavior, and rejection
// message. ok is false only when the bytes don't parse as JSON. The message is the user's typed
// rejection reason, with the contracts.ControlAnswerMessageRejectedByUser sentinel (an auto-filled placeholder,
// not a real reason) collapsed to "". The SINGLE home for the sentinel rule, shared by the Codex
// feedback path, the Cursor create-plan transform, and the shared control-response request-id
// default (ProviderDefaults.ControlResponseRequestID), so a sentinel change lands in exactly one place.
func DecodeControlBehavior(content []byte) (requestID, behavior, message string, ok bool) {
	var cr ControlBehaviorEnvelope
	if err := json.Unmarshal(content, &cr); err != nil {
		return "", "", "", false
	}
	requestID = strings.TrimSpace(cr.Response.RequestID)
	behavior = strings.TrimSpace(cr.Response.Response.Behavior)
	message = NormalizeRejectionMessage(cr.Response.Response.Message)
	return requestID, behavior, message, true
}

// NormalizeRejectionMessage trims a control-response reject reason and collapses the
// contracts.ControlAnswerMessageRejectedByUser placeholder (the auto-filled "declined without a reason" text) to
// "". The SINGLE home for the deny-feedback rule, shared by DecodeControlBehavior (which decodes
// the raw wire bytes) and the service's controlResponsePlan.rejectionMessage accessor (which reads
// the already-decoded plan), so a sentinel-value change lands in exactly one place instead of two.
func NormalizeRejectionMessage(message string) string {
	message = strings.TrimSpace(message)
	if message == contracts.ControlAnswerMessageRejectedByUser {
		return ""
	}
	return message
}
