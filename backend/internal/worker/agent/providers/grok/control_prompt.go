package grok

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The fields of Grok's reply to a question dialog, beside its outcome.
const (
	grokReplyFieldAnswers     = "answers"
	grokReplyFieldAnnotations = "annotations"
	grokAnnotationPreview     = "preview"
	grokAnnotationNotes       = "notes"
)

// grokFolderTrustOptions are the two answers to Grok's folder-trust question.
//
// Grok sends no option list for this request, so LeapMux states one. The option
// ids are Grok's own two reply words, which resolveGrokFolderTrust reads back
// without a table of its own. Grok keeps the answer for the whole workspace, so
// the names say that.
func grokFolderTrustOptions() []*leapmuxv1.PermissionOption {
	return []*leapmuxv1.PermissionOption{
		{OptionId: contracts.GrokTrustOutcomeTrust, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: "Trust this workspace"},
		{OptionId: contracts.GrokTrustOutcomeReject, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: "Do not trust"},
	}
}

// grokControlRequest is the method and the params of one control request. A
// member of the wrong type reads as absent: a method that is no string matches
// no method, and params that are no object hold no field.
type grokControlRequest struct {
	method string
	params map[string]json.RawMessage
}

func readGrokControlRequest(payload []byte) grokControlRequest {
	fields := providerkit.RawFields(payload)
	method, _ := providerkit.RawString(fields["method"])
	return grokControlRequest{method: method, params: providerkit.RawFields(fields["params"])}
}

// param reads one string field of the params, or "".
func (r grokControlRequest) param(key string) string {
	value, _ := providerkit.RawString(r.params[key])
	return value
}

// ReadControlRequest reads Grok's own dialogs, and every other request through
// the base reader:
//
//   - `_x.ai/ask_user_question` is a question. Grok's reply carries the chosen
//     options and the reader's notes for one question, so the prompt keeps
//     both.
//   - `_x.ai/mcp/elicit` is an MCP form, with the server under `serverName`.
//   - `_x.ai/exit_plan_mode` is a plan approval, with the plan that Grok sent.
//   - `_x.ai/folder_trust/request` is a permission with LeapMux's two answers.
//
// Every tool permission is the standard Agent Client Protocol request, and the
// standard `elicitation/create` still reads as a form.
func (grokProvider) ReadControlRequest(input agent.ControlRequestInput) *leapmuxv1.ControlPrompt {
	request := readGrokControlRequest(input.Payload)
	switch request.method {
	case contracts.GrokMethodAskUserQuestion:
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
			Questions:               providerkit.QuestionsFromWire(request.params["questions"]),
			PreservesSelectionNotes: true,
		}}}
	case contracts.GrokMethodMcpElicit:
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Elicitation{Elicitation: grokElicitation(request)}}
	case contracts.GrokMethodExitPlanMode:
		// Grok sends the plan's Markdown, or null for an empty plan. The prompt
		// then states no plan text, as the plan file holds none either.
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{
			Text: request.param("planContent"),
		}}}
	case contracts.GrokMethodFolderTrust:
		// resolveGrokFolderTrust sends Grok the decision alone, so a typed
		// reason would reach nobody: the editor takes none.
		return &leapmuxv1.ControlPrompt{
			Kind:          &leapmuxv1.ControlPrompt_Permission{Permission: grokFolderTrustPermission(request)},
			EditorPurpose: leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_NONE,
		}
	default:
		return acp.ReadBaseControlRequest(input)
	}
}

// grokElicitation reads the MCP form or URL that `_x.ai/mcp/elicit` asks for.
// Grok states no title or description for it.
func grokElicitation(request grokControlRequest) *leapmuxv1.ElicitationPrompt {
	mode, ok := providerkit.RawString(request.params["mode"])
	if !ok {
		mode = contracts.MCPElicitationModeForm
	}
	prompt := &leapmuxv1.ElicitationPrompt{
		Mode:    mode,
		Message: request.param("message"),
		Server:  request.param("serverName"),
		Url:     request.param("url"),
	}
	if schema := request.params["requestedSchema"]; providerkit.RawPresent(schema) {
		prompt.SchemaJson = string(schema)
	}
	return prompt
}

// grokFolderTrustPermission reads the question whether the repository's own
// configuration may load. The title states the workspace, else the working
// directory. The reason lists each kind of configuration that the repository
// holds; an element that is no string, or an empty one, is no kind.
func grokFolderTrustPermission(request grokControlRequest) *leapmuxv1.PermissionPrompt {
	workspace := request.param("workspace")
	if workspace == "" {
		workspace = request.param("cwd")
	}
	title := "Trust this workspace"
	if workspace != "" {
		title = "Trust the workspace " + workspace
	}
	var elements []json.RawMessage
	_ = json.Unmarshal(request.params["configKinds"], &elements)
	var kinds []string
	for _, element := range elements {
		if kind, ok := providerkit.RawString(element); ok && kind != "" {
			kinds = append(kinds, kind)
		}
	}
	found := ""
	if len(kinds) > 0 {
		found = fmt.Sprintf(" (%s)", strings.Join(kinds, ", "))
	}
	return &leapmuxv1.PermissionPrompt{
		Title:   title,
		Reason:  "The repository holds its own Grok Build configuration" + found + ". Grok Build loads it only from a workspace you trust, and it keeps your answer for the workspace.",
		Options: grokFolderTrustOptions(),
	}
}

// AnswerControlRequest builds Grok's answer to one decision:
//
//   - A question takes Grok's own reply, which ResolveControlResponse passes
//     on unchanged.
//   - A plan takes the neutral allow or deny, which resolveGrokPlanApproval
//     turns into Grok's reply.
//   - An MCP form and a permission take the base answer. A refusal's reason
//     rides in Grok's own `_meta.followup_message`, so it never follows as a
//     message.
func (grokProvider) AnswerControlRequest(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	switch input.Prompt.GetKind().(type) {
	case *leapmuxv1.ControlPrompt_Question:
		return grokQuestionAnswer(input)
	case *leapmuxv1.ControlPrompt_Plan:
		return agent.AnswerNeutralPlan(input)
	}
	if readGrokControlRequest(input.Request.Payload).method == contracts.GrokMethodFolderTrust && input.Decision.GetPermission().GetFeedback() != "" {
		// resolveGrokFolderTrust sends the decision alone.
		return agent.ControlAnswer{}, agent.InvalidControlDecision("Grok's folder trust takes no reason")
	}
	return acp.AnswerBaseControlRequest(input, grokRejectReason)
}

var _ acp.RejectReason = grokRejectReason

// grokRejectReason adds the reason of a refused permission to Grok's reply.
// Grok reads it from `_meta.followup_message`: it gives the text to the model
// and the turn goes on, where a bare refusal ends the turn.
func grokRejectReason(result map[string]any, reason string) map[string]any {
	return acp.WithMeta(result, reason, contracts.GrokReplyFieldFollowupMessage)
}

// grokQuestionAnswer answers a question dialog. A cancellation takes Grok's
// `cancelled`, which carries no reason: Grok tells the model that the reader
// declined to answer, so a reason that the decision states is not sent.
func grokQuestionAnswer(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, questions, err := agent.QuestionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	if decision.GetCancelled() {
		return agent.ControlAnswer{Content: agent.JSONRPCResult(input.RequestID, map[string]any{
			contracts.GrokReplyFieldOutcome: contracts.GrokQuestionOutcomeCancelled,
		})}, nil
	}
	return agent.ControlAnswer{Content: agent.JSONRPCResult(input.RequestID, grokQuestionReply(questions, decision))}, nil
}

// grokQuestionReply is the reply to one question dialog, in Grok's own shape.
//
// Grok keys each answer by the question's text and states it as the list of
// values that the reader chose. A question that the reader left empty is
// omitted. The reader's typed words go in the notes, beside the chosen
// options; with no option chosen, the answer is the free-text label alone. A
// single choice whose option carries a preview sends the preview back, which
// Grok shows the model beside the choice.
//
// Two questions with the same text share one key, and the later answer wins.
// An annotation stays from the earlier question when the later one states
// none.
func grokQuestionReply(questions []*leapmuxv1.ControlQuestion, decision *leapmuxv1.QuestionDecision) map[string]any {
	answers := map[string][]string{}
	annotations := map[string]map[string]string{}
	for i, question := range questions {
		answer := agent.QuestionAnswerAt(decision, i)
		selected := answer.GetSelected()
		notes := strings.TrimSpace(answer.GetCustomText())
		if len(selected) == 0 && notes == "" {
			continue
		}
		key := question.GetQuestion()
		if len(selected) > 0 {
			answers[key] = selected
		} else {
			answers[key] = []string{contracts.GrokAnswerLabelFreeText}
		}
		annotation := map[string]string{}
		if !question.GetMultiSelect() && len(selected) == 1 {
			if preview := chosenPreview(question, selected[0]); preview != "" {
				annotation[grokAnnotationPreview] = preview
			}
		}
		if notes != "" {
			annotation[grokAnnotationNotes] = notes
		}
		if len(annotation) > 0 {
			annotations[key] = annotation
		}
	}
	reply := map[string]any{
		contracts.GrokReplyFieldOutcome: contracts.GrokQuestionOutcomeAccepted,
		grokReplyFieldAnswers:           answers,
	}
	if len(annotations) > 0 {
		reply[grokReplyFieldAnnotations] = annotations
	}
	return reply
}

// chosenPreview is the preview of the first option of question whose value is
// value, or "".
func chosenPreview(question *leapmuxv1.ControlQuestion, value string) string {
	for _, option := range question.GetOptions() {
		if agent.QuestionOptionValue(option) == value {
			return option.GetPreview()
		}
	}
	return ""
}
