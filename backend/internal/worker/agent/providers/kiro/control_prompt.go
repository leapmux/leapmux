package kiro

import (
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Kiro's wire words that only these readers spell, so the contract holds none
// of them.
const (
	// kiroTurnApproval is the `_meta.kiro.type` of the request that reviews the
	// file changes of a Supervised turn.
	kiroTurnApproval = "turn_approval"
	// kiroRecommendedSuffix is the text that a recommended option carries
	// beside its title, as Kiro's own dialog shows it.
	kiroRecommendedSuffix = " (recommended)"
)

// kiroAlwaysRule is one always option of the decision row: its id, the name
// that it shows, and the scope of its rule.
type kiroAlwaysRule struct {
	optionID string
	name     string
	scope    leapmuxv1.PermissionScope
}

// kiroAlwaysAllowRules are the always-allow options, Kiro's own first.
//
// Kiro states one `always-accept`, named `Always allow`, which keeps its rule
// for the session, and it reads a wider scope from the reply. LeapMux states
// the two wider scopes as options of their own, and resolveKiroPermission turns
// each back into Kiro's `always-accept` with the scope. LeapMux writes these
// options, so it states the scope of each one.
var kiroAlwaysAllowRules = [...]kiroAlwaysRule{
	{optionID: contracts.KiroPermissionOptionAlwaysAccept, name: "Always allow for this session", scope: leapmuxv1.PermissionScope_PERMISSION_SCOPE_SESSION},
	{optionID: contracts.KiroScopedPermissionOptionAlwaysAcceptWorkspace, name: "Always allow in this workspace", scope: leapmuxv1.PermissionScope_PERMISSION_SCOPE_WORKSPACE},
	{optionID: contracts.KiroScopedPermissionOptionAlwaysAcceptUser, name: "Always allow everywhere", scope: leapmuxv1.PermissionScope_PERMISSION_SCOPE_USER},
}

// kiroAlwaysDenyRules are the always-deny options, Kiro's own first. Kiro's
// `always-reject` reads the scope of the reply as its `always-accept` does, so
// LeapMux states the same two wider scopes for it.
var kiroAlwaysDenyRules = [...]kiroAlwaysRule{
	{optionID: contracts.KiroPermissionOptionAlwaysReject, name: "Always deny for this session", scope: leapmuxv1.PermissionScope_PERMISSION_SCOPE_SESSION},
	{optionID: contracts.KiroScopedPermissionOptionAlwaysRejectWorkspace, name: "Always deny in this workspace", scope: leapmuxv1.PermissionScope_PERMISSION_SCOPE_WORKSPACE},
	{optionID: contracts.KiroScopedPermissionOptionAlwaysRejectUser, name: "Always deny everywhere", scope: leapmuxv1.PermissionScope_PERMISSION_SCOPE_USER},
}

// kiroControlRequest is the method and the params of one control request. A
// member of the wrong type reads as absent.
type kiroControlRequest struct {
	method string
	params map[string]json.RawMessage
}

func readKiroControlRequest(payload []byte) kiroControlRequest {
	fields := providerkit.RawFields(payload)
	return kiroControlRequest{method: fieldString(fields, "method"), params: providerkit.RawFields(fields["params"])}
}

// ReadControlRequest reads Kiro's own dialogs, and every other request through
// the base reader:
//
//   - `_kiro/userInput` is a question, with a question of its own for each
//     option that carries sub-options.
//   - `_kiro/mcp/elicitation` is an MCP form, with MCP's own request under
//     `elicitation`.
//   - A permission is the standard Agent Client Protocol request, which this
//     completes with what only Kiro states: the wider scopes of an always
//     option, the command of a shell call, and the files of a turn review.
//
// The standard `elicitation/create` still reads as a form, and any other
// request reads as unrecognized, as the base reader reads it.
func (kiroProvider) ReadControlRequest(input agent.ControlRequestInput) *leapmuxv1.ControlPrompt {
	request := readKiroControlRequest(input.Payload)
	switch request.method {
	case contracts.KiroMethodUserInput:
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
			Questions: kiroUserInputQuestions(fieldString(request.params, "question"), request.params["options"]),
		}}}
	case contracts.KiroMethodMcpElicitation:
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Elicitation{Elicitation: kiroElicitation(request)}}
	}
	if elicitation, ok := acp.ReadElicitation(input.Payload); ok {
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Elicitation{Elicitation: elicitation}}
	}
	permission := acp.ReadPermissionPrompt(input)
	if permission == nil {
		// As the base reader reads it (acp.ReadBaseControlRequest).
		return agent.UnrecognizedControlPrompt()
	}
	completeKiroPermission(permission, kiroFields(providerkit.RawFields(request.params["_meta"])[contracts.KiroMetaNamespace]))
	return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: permission}}
}

// kiroElicitation reads the MCP form or URL that `_kiro/mcp/elicitation` asks
// for. Kiro states no server, title or description for it.
func kiroElicitation(request kiroControlRequest) *leapmuxv1.ElicitationPrompt {
	elicitation := providerkit.RawFields(request.params["elicitation"])
	mode, ok := providerkit.RawString(elicitation["mode"])
	if !ok {
		mode = contracts.MCPElicitationModeForm
	}
	prompt := &leapmuxv1.ElicitationPrompt{
		Mode:    mode,
		Message: fieldString(elicitation, "message"),
		Url:     fieldString(elicitation, "url"),
	}
	if schema := elicitation["requestedSchema"]; providerkit.RawPresent(schema) {
		prompt.SchemaJson = string(schema)
	}
	return prompt
}

// completeKiroPermission adds what only Kiro states to a permission that the
// base reader read. meta is the request's `_meta.kiro`, or nil.
//
//   - Each always option gains its wider scopes.
//   - A turn review states the files that it holds as its reason.
//   - Any other permission takes the command from meta when the base reader
//     found none, because Kiro states a shell call as its title alone. The
//     command that the base reader found is what the call runs, so it wins.
func completeKiroPermission(permission *leapmuxv1.PermissionPrompt, meta map[string]json.RawMessage) {
	permission.Options = kiroPermissionOptions(permission.GetOptions(), meta)
	if fieldString(meta, "type") == kiroTurnApproval {
		permission.Reason = kiroTurnApprovalReason(kiroTurnApprovalFiles(meta))
		return
	}
	if command := fieldString(meta, "command"); command != "" && permission.GetCommand() == "" {
		permission.Command = command
	}
}

// kiroPermissionOptions returns options with the wider scopes of each always
// option.
//
//   - Kiro offers `always-accept` only for an implicit ask whose rule can
//     persist, and `always-reject` for each ask whose rule can persist. The
//     option id alone is not Kiro's always rule: the kind must say so too.
//   - The wider scopes ride the scope pills of the decision row, and
//     `always-accept` is what draws the pills. With no `always-accept`, the
//     always-deny keeps the session alone, because each wider scope would be
//     one more button.
//   - A rule for the workspace needs the workspace root, which the request
//     states in its consent. With no root, the workspace options stay out,
//     because Kiro would refuse them.
//
// It returns options itself when the request offers no always option.
func kiroPermissionOptions(options []*leapmuxv1.PermissionOption, meta map[string]json.RawMessage) []*leapmuxv1.PermissionOption {
	allow := firstKiroOption(options, contracts.KiroPermissionOptionAlwaysAccept, leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS)
	deny := firstKiroOption(options, contracts.KiroPermissionOptionAlwaysReject, leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ALWAYS)
	if allow == nil && deny == nil {
		return options
	}
	wider := allow != nil
	workspace := strings.TrimSpace(fieldString(providerkit.RawFields(meta[contracts.KiroMetaConsent]), "workspaceRoot")) != ""
	expanded := make([]*leapmuxv1.PermissionOption, 0, len(options)+4)
	for _, option := range options {
		switch option {
		case allow:
			expanded = append(expanded, kiroAlwaysRuleOptions(option, kiroAlwaysAllowRules[:], wider, workspace)...)
		case deny:
			expanded = append(expanded, kiroAlwaysRuleOptions(option, kiroAlwaysDenyRules[:], wider, workspace)...)
		default:
			expanded = append(expanded, option)
		}
	}
	return expanded
}

// firstKiroOption is the first option with the id and the kind, or nil.
func firstKiroOption(options []*leapmuxv1.PermissionOption, optionID string, kind leapmuxv1.PermissionOptionKind) *leapmuxv1.PermissionOption {
	for _, option := range options {
		if option.GetOptionId() == optionID && option.GetKind() == kind {
			return option
		}
	}
	return nil
}

// kiroAlwaysRuleOptions returns the options of one always rule, one for each
// scope that the request can keep. Kiro's own option keeps its fields and
// takes the name and the scope of the rule. A wider scope is left out when
// wider is false, and the workspace scope is left out when the request states
// no workspace root.
func kiroAlwaysRuleOptions(own *leapmuxv1.PermissionOption, rules []kiroAlwaysRule, wider, workspace bool) []*leapmuxv1.PermissionOption {
	var options []*leapmuxv1.PermissionOption
	for _, rule := range rules {
		if rule.optionID == own.GetOptionId() {
			named := proto.Clone(own).(*leapmuxv1.PermissionOption)
			named.Name = rule.name
			named.Scope = rule.scope
			options = append(options, named)
			continue
		}
		if !wider || (rule.scope == leapmuxv1.PermissionScope_PERMISSION_SCOPE_WORKSPACE && !workspace) {
			continue
		}
		options = append(options, &leapmuxv1.PermissionOption{OptionId: rule.optionID, Kind: own.GetKind(), Name: rule.name, Scope: rule.scope})
	}
	return options
}

// kiroTurnApprovalFiles reads the files of one turn review, the path of each.
// An element that is no object, or that states no path, is dropped.
func kiroTurnApprovalFiles(meta map[string]json.RawMessage) []string {
	var elements []json.RawMessage
	if json.Unmarshal(meta["files"], &elements) != nil {
		return nil
	}
	var files []string
	for _, element := range elements {
		if path := fieldString(providerkit.RawFields(element), "path"); path != "" {
			files = append(files, path)
		}
	}
	return files
}

// kiroTurnApprovalReason states what a turn review asks, with one line for
// each file that it holds.
func kiroTurnApprovalReason(files []string) string {
	const reason = "Kiro holds the file changes of this turn for your review. Allow applies them, and Deny restores each file"
	if len(files) == 0 {
		return reason + "."
	}
	lines := make([]string, 0, len(files))
	for _, file := range files {
		lines = append(lines, "- "+file)
	}
	return reason + ":\n" + strings.Join(lines, "\n")
}

// kiroUserInputOption is one option of Kiro's question, as Kiro states it.
type kiroUserInputOption struct {
	title           string
	description     string
	recommended     bool
	subOptionsLabel string
	subOptions      []kiroUserInputOption
}

// kiroUserInputOptions reads the options of one question. The model may state
// an option as its title alone, a bare string. An option that states no title
// is dropped, and an option is recommended only when Kiro states true.
func kiroUserInputOptions(raw json.RawMessage) []kiroUserInputOption {
	var elements []json.RawMessage
	if json.Unmarshal(raw, &elements) != nil {
		return nil
	}
	var options []kiroUserInputOption
	for _, element := range elements {
		if title, ok := providerkit.RawString(element); ok {
			if title != "" {
				options = append(options, kiroUserInputOption{title: title})
			}
			continue
		}
		fields := providerkit.RawFields(element)
		title := fieldString(fields, "title")
		if title == "" {
			continue
		}
		options = append(options, kiroUserInputOption{
			title:           title,
			description:     fieldString(fields, "description"),
			recommended:     providerkit.RawBool(fields["recommended"]),
			subOptionsLabel: fieldString(fields, "subOptionsLabel"),
			subOptions:      kiroUserInputOptions(fields["subOptions"]),
		})
	}
	return options
}

// kiroQuestionOption is one option in the neutral model. The value is Kiro's
// own title, which the answer states.
func kiroQuestionOption(option kiroUserInputOption) *leapmuxv1.QuestionOption {
	title := option.title
	label := title
	if option.recommended {
		label += kiroRecommendedSuffix
	}
	return &leapmuxv1.QuestionOption{Value: &title, Label: label, Description: option.description}
}

func kiroQuestionOptions(options []kiroUserInputOption) []*leapmuxv1.QuestionOption {
	converted := make([]*leapmuxv1.QuestionOption, 0, len(options))
	for _, option := range options {
		converted = append(converted, kiroQuestionOption(option))
	}
	return converted
}

// kiroUserInputQuestions reads Kiro's one question into the neutral model: the
// question itself, then one question for each option that carries
// sub-options.
//
// Kiro asks ONE question, with a list of options or none. The reader chooses
// one option or types an answer. An option can carry sub-options, of which the
// reader chooses any number, and Kiro's own dialog starts with every
// sub-option chosen. A client starts each list with nothing selected, so a
// sub-option question lists the sub-options to LEAVE OUT: the empty start
// means every sub-option, as it does in Kiro. A client asks each sub-option
// question whatever the reader chose, so each one states the option that it
// belongs to as its header, and the answer reads the selection of the chosen
// option's question alone.
func kiroUserInputQuestions(question string, rawOptions json.RawMessage) []*leapmuxv1.ControlQuestion {
	options := kiroUserInputOptions(rawOptions)
	questions := []*leapmuxv1.ControlQuestion{{Question: question, Options: kiroQuestionOptions(options)}}
	for _, option := range options {
		if len(option.subOptions) == 0 {
			continue
		}
		label := option.subOptionsLabel
		if label == "" {
			label = "Choices"
		}
		questions = append(questions, &leapmuxv1.ControlQuestion{
			Header:      option.title,
			Question:    fmt.Sprintf("%s for %s: select any to leave out", label, option.title),
			Options:     kiroQuestionOptions(option.subOptions),
			MultiSelect: true,
			AllowEmpty:  true,
		})
	}
	return questions
}

// AnswerControlRequest builds Kiro's answer to one decision. A question takes
// Kiro's own reply, which ResolveControlResponse passes on unchanged. An MCP
// form and a permission take the base answer, and a refusal's reason rides in
// Kiro's own `_meta.kiro.rejectionReason`, so it never follows as a message.
func (kiroProvider) AnswerControlRequest(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	if _, ok := input.Prompt.GetKind().(*leapmuxv1.ControlPrompt_Question); ok {
		return kiroQuestionAnswer(input)
	}
	return acp.AnswerBaseControlRequest(input, kiroRejectReason)
}

var _ acp.RejectReason = kiroRejectReason

// kiroRejectReason adds the reason of a refused permission to Kiro's reply, in
// `_meta.kiro.rejectionReason`, and keeps every other key of the reply's
// metadata. resolveKiroPermission keeps it too when it adds a consent scope.
func kiroRejectReason(result map[string]any, reason string) map[string]any {
	return acp.WithMeta(result, reason, contracts.KiroMetaNamespace, contracts.KiroMetaRejectionReason)
}

// kiroQuestionAnswer answers Kiro's question. A cancellation, and an answer
// that states nothing, take Kiro's `dismissed`. That reply has no field for a
// reason, so a reason that the decision states is not sent. Kiro's native tool
// reports the dismissal to the model as the user's readiness to move on.
func kiroQuestionAnswer(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, questions, err := agent.QuestionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	reply := map[string]any{"action": contracts.KiroUserInputActionDismissed}
	if !decision.GetCancelled() {
		if answer, ok := kiroUserInputAnswer(questions, decision); ok {
			reply = map[string]any{"action": contracts.KiroUserInputActionAnswered, "answer": answer}
		}
	}
	return agent.ControlAnswer{Content: agent.JSONRPCResult(input.RequestID, reply)}, nil
}

// kiroUserInputAnswer is the answer that one decision states, as Kiro's own
// dialog writes it, or false when the reader chose nothing and typed nothing.
//
// Kiro takes ONE answer: the title of the chosen option, or typed text. The
// main question takes one of the two, never both, so:
//
//   - A chosen option that carries sub-options states in brackets the ones
//     that the reader kept: every sub-option that its question did not leave
//     out, which can be none. Its sub-option question takes a selection AND
//     typed text, as every multiple-choice question does, so the typed text is
//     one more kept entry (see agent.QuestionAnswerValues). The browser sent
//     the typed text alone instead, and dropped the chosen option.
//   - Typed text on any other question is the whole answer, and the first
//     question that holds some gives it. It wins over a chosen option,
//     because the reader typed it last: a client saves the text to the
//     question that shows when the reader types, which can be the sub-option
//     question of an option that the reader did not choose.
func kiroUserInputAnswer(questions []*leapmuxv1.ControlQuestion, decision *leapmuxv1.QuestionDecision) (string, bool) {
	if len(questions) == 0 {
		return "", false
	}
	chosen := ""
	if selected := agent.QuestionAnswerAt(decision, 0).GetSelected(); len(selected) > 0 {
		chosen = selected[0]
	}
	sub := -1
	for i := 1; chosen != "" && i < len(questions); i++ {
		if questions[i].GetHeader() == chosen {
			sub = i
			break
		}
	}
	for i := range questions {
		if i == sub {
			continue
		}
		if typed := strings.TrimSpace(agent.QuestionAnswerAt(decision, i).GetCustomText()); typed != "" {
			return typed, true
		}
	}
	if chosen == "" {
		return "", false
	}
	if sub < 0 {
		return chosen, true
	}
	return fmt.Sprintf("%s [%s]", chosen, strings.Join(kiroKeptSubOptions(questions[sub], agent.QuestionAnswerAt(decision, sub)), ", ")), true
}

// kiroKeptSubOptions is what one sub-option question keeps: each sub-option
// that its answer did not leave out, in the order of the question, then the
// typed text as one more entry. The answer's selection lists the sub-options
// to leave out, so only the text that agent.QuestionAnswerValues adds after
// the selection is kept as it is.
func kiroKeptSubOptions(question *leapmuxv1.ControlQuestion, answer *leapmuxv1.QuestionAnswer) []string {
	leftOut := map[string]bool{}
	for _, value := range answer.GetSelected() {
		leftOut[value] = true
	}
	kept := make([]string, 0, len(question.GetOptions())+1)
	for _, option := range question.GetOptions() {
		if value := agent.QuestionOptionValue(option); !leftOut[value] {
			kept = append(kept, value)
		}
	}
	values := agent.QuestionAnswerValues(question, answer)
	return append(kept, values[len(answer.GetSelected()):]...)
}
