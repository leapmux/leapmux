package agent

import (
	"encoding/json"
	"errors"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
)

// ControlResponseContext is the pure provider input for interpreting a frontend
// control response. The service loads the stored control request once, extracts the
// shared metadata, and passes both payloads here; providers must not perform I/O.
type ControlResponseContext struct {
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
	// Feedback must use a separate user input when the native response cannot carry it safely.
	Feedback      string
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

// ControlResponseRefusedText is the reason that a withheld response states when
// its provider gives none. The browser shows it after "The response was not
// sent: ". It is the one copy of this text.
const ControlResponseRefusedText = "the agent provider could not read this control response"

// Reasons that several providers give for a refusal. Like every reason, each one
// completes the browser's sentence "The response was not sent: ...".
const (
	// RefusalUnreadableRequest: the stored control request does not decode, or it
	// lacks a field that the reply needs.
	RefusalUnreadableRequest = "LeapMux cannot read the stored request that this answer is for"
	// RefusalUnreadableAnswer: the answer does not decode into the fields that the
	// reply needs.
	RefusalUnreadableAnswer = "LeapMux cannot read this answer"
	// RefusalNoDecision: the answer is not the neutral decision envelope, or its
	// behavior is neither allow nor deny.
	RefusalNoDecision = "the answer states neither allow nor deny"
	// RefusalOtherRequest: the answer identifies a request other than the stored
	// one.
	RefusalOtherRequest = "the answer is for a different request"
	// RefusalUnencodableReply: the reply to the agent cannot be encoded.
	RefusalUnencodableReply = "LeapMux cannot encode the reply to the agent"
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

// defaultControlResponseRequestID reads the stored-control-request lookup id from a raw frontend
// control response. It tries the neutral approve/reject envelope first
// (DecodeControlBehavior: {response:{request_id, ...}}, which every provider's frontend emits) and
// falls back to a top-level JSON-RPC id (ExtractJSONRPCID, used by the ACP family and Codex). When
// a payload carries BOTH -- a top-level id AND a nested response.request_id -- the nested envelope
// id wins, because the pending control_request row is keyed by response.request_id. Returns "" when
// neither shape yields a non-empty id. Shared by every provider's ControlResponseRequestID; no
// provider narrows it, since narrowing to one shape would drop the other's real flows.
//
// The JSON-RPC branch returns the id TEXT, and it must not canonicalize it. The browser echoes
// the worker's own request id -- the "jsonrpc:"-prefixed key PublishControlRequest stored the row
// under -- as a JSON STRING in that field (buildJsonRpcResult), because a JSON number loses
// precision there. So the text already IS the stored key, and running it through
// NewControlRequestIdentity would prefix it a second time and match nothing.
func defaultControlResponseRequestID(content []byte) string {
	if requestID, _, _, ok := DecodeControlBehavior(content); ok && requestID != "" {
		return requestID
	}
	if _, requestID, ok := ExtractJSONRPCID(content); ok {
		return requestID
	}
	return ""
}

func (ProviderDefaults) ControlResponseRequestID(content []byte) string {
	return defaultControlResponseRequestID(content)
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
			// refusal that also ends plan mode. Empty for a plain decision. The
			// browser's plan control writes it (withControlChoice in
			// frontend/src/utils/controlResponse.ts), and each provider maps the
			// id onto its own wire answer.
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
// It is the reader for `buildAllowResponse`'s `updatedInput`
// (frontend/src/utils/controlResponse.ts): the browser sends the whole tool
// input back with the answer it changed -- an AskUserQuestion reply folds its
// answers into that input -- and a provider that forwards a modified input to
// its own CLI reads it here. DecodeControlBehavior leaves it alone, so its
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

// DecodeControlBehavior decodes the frontend's neutral approve/reject control-response envelope
// (ControlBehaviorEnvelope), returning the trimmed request id, behavior, and rejection
// message. ok is false only when the bytes don't parse as JSON. The message is the user's typed
// rejection reason, with the ControlRejectedByUserMessage sentinel (an auto-filled placeholder,
// not a real reason) collapsed to "". The SINGLE home for the sentinel rule, shared by the Codex
// feedback path, the Cursor create-plan transform, and the shared control-response request-id
// default (defaultControlResponseRequestID), so a sentinel change lands in exactly one place.
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
// ControlRejectedByUserMessage placeholder (the auto-filled "declined without a reason" text) to
// "". The SINGLE home for the deny-feedback rule, shared by DecodeControlBehavior (which decodes
// the raw wire bytes) and the service's controlResponsePlan.rejectionMessage accessor (which reads
// the already-decoded plan), so a sentinel-value change lands in exactly one place instead of two.
func NormalizeRejectionMessage(message string) string {
	message = strings.TrimSpace(message)
	if message == ControlRejectedByUserMessage {
		return ""
	}
	return message
}

// Control response behavior values (shared protocol between frontend and backend).
const (
	ControlBehaviorAllow = "allow"
	ControlBehaviorDeny  = "deny"
)

// ControlRejectedByUserMessage is the placeholder reject message the frontend emits when
// the user declines a control request WITHOUT typing a reason (buildDenyResponse in
// frontend utils/controlResponse.ts). The backend treats it as "no feedback" -- it is not
// shown as the user's answer -- so every deny-with-feedback path compares against this one
// constant instead of re-spelling the literal (which must stay in lockstep with the
// frontend producer).
const ControlRejectedByUserMessage = "Rejected by user."
