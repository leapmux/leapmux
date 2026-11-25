package providerkit

import (
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// The workspace-trust question as a control prompt, and its answer.
//
// WorkspaceTrust (workspace_trust.go) publishes the question, LeapMux's own
// payload {type, path, configuration}, and ResolveWorkspaceTrustResponse turns
// the neutral allow or deny into the saved {decision}. This file reads the
// question into the permission prompt that a client draws, and builds that
// neutral answer from the client's decision. Each provider that WorkspaceTrust
// asks for reads and answers the question here before its own requests.

// The option IDs of the two answers. No agent's own option IDs reuse them.
const (
	WorkspaceTrustOptionTrust  = "workspace-trust"
	WorkspaceTrustOptionReject = "workspace-reject"
)

// workspaceTrustAgentFallback is the subject of the reason when the provider
// has no display name.
const workspaceTrustAgentFallback = "The agent"

// IsWorkspaceTrustQuestion reports whether payload is the workspace-trust
// question that WorkspaceTrust publishes.
func IsWorkspaceTrustQuestion(payload []byte) bool {
	return StringField(payload, contracts.WorkspaceTrustFieldType) == contracts.WorkspaceTrustControlTypeQuestion
}

// ReadWorkspaceTrustPrompt reads the workspace-trust question into a
// permission prompt. The title states the folder, and the reason states which
// agent keeps the trusted list and what the trust controls. It returns false
// for any other payload.
//
// The configuration words keep only their non-empty strings, so a malformed
// element reads as absent.
func ReadWorkspaceTrustPrompt(payload []byte, provider leapmuxv1.AgentProvider) (*leapmuxv1.ControlPrompt, bool) {
	if !IsWorkspaceTrustQuestion(payload) {
		return nil, false
	}
	fields := RawFields(payload)
	path := ObjectString(fields, contracts.WorkspaceTrustFieldPath)
	title := "Trust this folder"
	if path != "" {
		title = "Trust the folder " + path
	}
	agentName := contracts.ProviderDisplayName[provider]
	if agentName == "" {
		agentName = workspaceTrustAgentFallback
	}
	reason := fmt.Sprintf("%s loads %s of a project only from a folder that you trust, and it keeps your answer for the folder.",
		agentName, workspaceTrustConfigurationPhrase(workspaceTrustConfiguration(fields[contracts.WorkspaceTrustFieldConfiguration])))
	// ResolveWorkspaceTrustResponse sends the runtime the decision alone, so
	// a typed reason would reach nobody: the editor takes none.
	return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{
		Title:  title,
		Reason: reason,
		Options: []*leapmuxv1.PermissionOption{
			{OptionId: WorkspaceTrustOptionTrust, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: "Trust this folder"},
			{OptionId: WorkspaceTrustOptionReject, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: "Do not trust"},
		},
	}}, EditorPurpose: leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_NONE}, true
}

// workspaceTrustConfiguration reads the words of what the trust controls. An
// element that is no string, or an empty one, is dropped. A value that is no
// array reads as no words.
func workspaceTrustConfiguration(raw []byte) []string {
	var words []string
	for _, word := range RawStrings(raw) {
		if word != "" {
			words = append(words, word)
		}
	}
	return words
}

// workspaceTrustConfigurationPhrase joins the configuration words into one
// phrase: "the configuration" for none, "the MCP servers" for one, and
// "the A, B and C" for more.
func workspaceTrustConfigurationPhrase(words []string) string {
	switch len(words) {
	case 0:
		return "the configuration"
	case 1:
		return "the " + words[0]
	default:
		return "the " + strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
	}
}

// AnswerWorkspaceTrust answers the workspace-trust question with the neutral
// envelope, which ResolveWorkspaceTrustResponse turns into the saved answer:
//
//   - Trust allows, and takes no feedback.
//   - Do not trust denies with no message.
//   - A decision with feedback fails: the saved answer keeps the decision
//     alone, so a reason would reach nobody.
func AnswerWorkspaceTrust(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, option, err := agent.PermissionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	if decision.GetFeedback() != "" {
		// ResolveWorkspaceTrustResponse sends the decision alone, so a
		// reason would reach nobody.
		return agent.ControlAnswer{}, agent.InvalidControlDecision("the workspace-trust question takes no reason")
	}
	switch option.GetOptionId() {
	case WorkspaceTrustOptionTrust:
		return agent.ControlAnswer{Content: agent.ControlResponseEnvelope(input.RequestID, map[string]any{"behavior": contracts.ControlAnswerBehaviorAllow})}, nil
	case WorkspaceTrustOptionReject:
		return agent.ControlAnswer{Content: agent.ControlResponseEnvelope(input.RequestID, map[string]any{"behavior": contracts.ControlAnswerBehaviorDeny})}, nil
	default:
		return agent.ControlAnswer{}, agent.InvalidControlDecision("the workspace-trust question offers no option %q", option.GetOptionId())
	}
}
