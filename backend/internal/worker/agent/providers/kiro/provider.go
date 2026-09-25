package kiro

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// kiroPermissionMethod is the standard ACP permission request, which Kiro raises
// for a tool call and for the review of a Supervised turn.
const kiroPermissionMethod = "session/request_permission"

// kiroProvider is the wire-format plugin for Kiro. It is an ACP provider, and
// it adds what only Kiro knows: where its session store lives, its attachment
// policy, its to-do list, and the replies of its own dialogs.
//
// The browser answers each dialog in the shared shape of its surface: an MCP
// form with the neutral elicitation envelope, a question with Kiro's own
// reply, and a permission with the selected option of the protocol's reply.
// This type rewrites the two that Kiro cannot read as they come: the form, and
// a permission option that LeapMux states for a wider consent scope.
type kiroProvider struct {
	acp.Provider
}

// ListStoredSessions reads Kiro's own session store. See sessions.go.
func (kiroProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return kiroStoredSessions(ctx, q)
}

// ValidateAttachment states what a Kiro prompt carries: text as an embedded
// resource, an image as an image block, and a PDF as an embedded blob. Kiro
// drops any other binary without a word, so LeapMux refuses it up front.
func (kiroProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	if attachment.Kind == agent.AttachmentKindBinary {
		return fmt.Errorf("attachment %s is binary, and Kiro does not support binary attachments", attachment.Filename)
	}
	return nil
}

// ResolveControlResponse rewrites the answer of a dialog that Kiro cannot read
// as the browser sends it, and forwards every other answer through the shared
// ACP resolution.
func (p kiroProvider) ResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	var request struct {
		Method string `json:"method"`
	}
	if len(ctx.RequestPayload) == 0 || json.Unmarshal(ctx.RequestPayload, &request) != nil {
		return p.Provider.ResolveControlResponse(ctx)
	}
	switch request.Method {
	case contracts.KiroMethodMcpElicitation:
		// Kiro reads MCP's own answer. An accepted answer carries no `_meta`
		// scope, because Kiro defines none.
		result, _ := providerkit.ResolveMCPElicitationResponse(ctx, contracts.KiroMethodMcpElicitation, nil)
		return result
	case kiroPermissionMethod:
		return resolveKiroPermission(ctx, p.Provider.ResolveControlResponse(ctx))
	default:
		return p.Provider.ResolveControlResponse(ctx)
	}
}

// kiroScopedOption is the answer of Kiro's that one consent-scoped option
// stands for: Kiro's own always option, and the consent scope that the reply
// states beside it.
type kiroScopedOption struct {
	optionID string
	scope    string
}

// kiroScopedOptions maps each consent-scoped option that the browser states
// onto Kiro's own always option and its consent scope.
var kiroScopedOptions = map[string]kiroScopedOption{
	contracts.KiroScopedPermissionOptionAlwaysAcceptWorkspace: {optionID: contracts.KiroPermissionOptionAlwaysAccept, scope: contracts.KiroConsentScopeWorkspace},
	contracts.KiroScopedPermissionOptionAlwaysAcceptUser:      {optionID: contracts.KiroPermissionOptionAlwaysAccept, scope: contracts.KiroConsentScopeUser},
	contracts.KiroScopedPermissionOptionAlwaysRejectWorkspace: {optionID: contracts.KiroPermissionOptionAlwaysReject, scope: contracts.KiroConsentScopeWorkspace},
	contracts.KiroScopedPermissionOptionAlwaysRejectUser:      {optionID: contracts.KiroPermissionOptionAlwaysReject, scope: contracts.KiroConsentScopeUser},
}

// kiroSelectedOutcome is the outcome of the protocol's permission reply.
type kiroSelectedOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId"`
}

// resolveKiroPermission rewrites an always-allow or an always-deny at a wider
// consent scope.
//
// Kiro offers one `always-accept` and one `always-reject`. Each keeps its rule
// for the session, and each reads a wider scope from the reply's
// `_meta.kiro.consent.scope`: the workspace, or every workspace of the user.
// The browser states the wider scopes as options of their own, so the reader
// picks one with the scope pills of the shared decision row. This turns such
// an option back into Kiro's own option with its scope.
//
// Every other answer passes on as the shared resolution built it. These
// answers are withheld, because Kiro would refuse them or apply them to no
// workspace:
//
//   - A scoped option of a stored request that did not offer Kiro's own
//     option. Kiro offers `always-accept` only for an implicit ask whose rule
//     can persist, and `always-reject` only for an ask whose rule can persist.
//   - The workspace scope for a request that states no workspace root in its
//     consent. The browser offers that scope only with a root.
func resolveKiroPermission(ctx agent.ControlResponseContext, shared agent.ControlResponseResolution) agent.ControlResponseResolution {
	if shared.Withhold || len(shared.Content) == 0 {
		return shared
	}
	// The reply is read as raw members, so each member that this rewrite does
	// not touch passes on unchanged.
	var reply map[string]json.RawMessage
	if json.Unmarshal(shared.Content, &reply) != nil {
		return shared
	}
	var result map[string]json.RawMessage
	if json.Unmarshal(reply["result"], &result) != nil {
		return shared
	}
	var outcome kiroSelectedOutcome
	if json.Unmarshal(result["outcome"], &outcome) != nil {
		return shared
	}
	scoped, ok := kiroScopedOptions[outcome.OptionID]
	if !ok {
		return shared
	}
	request := readKiroPermissionRequest(ctx.RequestPayload)
	if !request.offers(scoped.optionID) {
		return withheld(ctx)
	}
	if scoped.scope == contracts.KiroConsentScopeWorkspace && request.workspaceRoot() == "" {
		return withheld(ctx)
	}
	outcome.OptionID = scoped.optionID
	content, err := rewriteScopedReply(reply, result, outcome, scoped.scope)
	if err != nil {
		return withheld(ctx)
	}
	shared.Content = content
	return shared
}

// rewriteScopedReply writes outcome and the consent scope into a permission
// reply, and encodes the reply again.
func rewriteScopedReply(reply, result map[string]json.RawMessage, outcome kiroSelectedOutcome, scope string) ([]byte, error) {
	encodedOutcome, err := json.Marshal(outcome)
	if err != nil {
		return nil, err
	}
	result["outcome"] = encodedOutcome
	meta, err := withConsentScope(result["_meta"], scope)
	if err != nil {
		return nil, err
	}
	result["_meta"] = meta
	encodedResult, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	reply["result"] = encodedResult
	return json.Marshal(reply)
}

// withConsentScope sets `kiro.consent.scope` in a reply's `_meta`, and keeps
// every other key that the reply carried.
func withConsentScope(raw json.RawMessage, scope string) (json.RawMessage, error) {
	meta, err := rawObject(raw)
	if err != nil {
		return nil, fmt.Errorf("read the reply's metadata: %w", err)
	}
	kiro, err := rawObject(meta[contracts.KiroMetaNamespace])
	if err != nil {
		return nil, fmt.Errorf("read the reply's Kiro metadata: %w", err)
	}
	consent, err := rawObject(kiro[contracts.KiroMetaConsent])
	if err != nil {
		return nil, fmt.Errorf("read the reply's consent: %w", err)
	}
	if consent[contracts.KiroMetaScope], err = json.Marshal(scope); err != nil {
		return nil, err
	}
	if kiro[contracts.KiroMetaConsent], err = json.Marshal(consent); err != nil {
		return nil, err
	}
	if meta[contracts.KiroMetaNamespace], err = json.Marshal(kiro); err != nil {
		return nil, err
	}
	return json.Marshal(meta)
}

// rawObject reads one JSON object into its members. An absent member or a
// JSON null is an empty object. Any other value that is not an object fails.
func rawObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	if len(raw) == 0 || string(raw) == "null" {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]json.RawMessage{}
	}
	return out, nil
}

// kiroPermissionRequest is the part of a stored permission request that the
// rewrite of a scoped answer reads.
type kiroPermissionRequest struct {
	Params struct {
		Options []struct {
			OptionID string `json:"optionId"`
		} `json:"options"`
		Meta struct {
			Kiro struct {
				Consent struct {
					WorkspaceRoot string `json:"workspaceRoot"`
				} `json:"consent"`
			} `json:"kiro"`
		} `json:"_meta"`
	} `json:"params"`
}

// readKiroPermissionRequest reads a stored permission request. A request that
// it cannot read offers no option and states no root.
func readKiroPermissionRequest(payload []byte) kiroPermissionRequest {
	var request kiroPermissionRequest
	if json.Unmarshal(payload, &request) != nil {
		return kiroPermissionRequest{}
	}
	return request
}

// offers reports whether the request offered one option.
func (r kiroPermissionRequest) offers(optionID string) bool {
	for _, option := range r.Params.Options {
		if option.OptionID == optionID {
			return true
		}
	}
	return false
}

// workspaceRoot is the workspace root that the request states in its consent,
// or "" for none.
func (r kiroPermissionRequest) workspaceRoot() string {
	return strings.TrimSpace(r.Params.Meta.Kiro.Consent.WorkspaceRoot)
}

// withheld is the resolution of an answer that Kiro could not read. The
// service refuses it and keeps the request open for another answer.
func withheld(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	result := agent.DefaultControlResponseResolution(ctx)
	result.Withhold = true
	return result
}
