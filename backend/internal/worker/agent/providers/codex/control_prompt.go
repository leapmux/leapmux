package codex

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The decision words of a Codex approval request beside codexDecisionAccept
// and codexDecisionAcceptForSession. Each is a string value of the answer's
// `decision`. Codex reads decline as a refusal of the one call, and cancel as
// a refusal that also stops the turn.
const (
	codexDecisionDecline = "decline"
	codexDecisionCancel  = "cancel"
)

// The keys of the two decisions that carry a policy amendment. Such a decision
// is an object with exactly one of these keys, and the answer sends the object
// back as Codex offered it.
const (
	codexDecisionExecPolicyAmendment    = "acceptWithExecpolicyAmendment"
	codexDecisionNetworkPolicyAmendment = "applyNetworkPolicyAmendment"
)

// The actions of a network policy amendment.
const (
	codexNetworkPolicyAllow = "allow"
	codexNetworkPolicyDeny  = "deny"
)

// The labels of Codex's decisions. Each one is the label of the button that
// sends the decision.
const (
	codexLabelAllow           = "Allow"
	codexLabelAllowForSession = "Allow for Session"
	codexLabelDeny            = "Deny"
	codexLabelCancel          = "Cancel"
	codexLabelAllowRemember   = "Allow & Remember"
	codexLabelAllowHost       = "Allow Host & Remember"
	codexLabelBlockHost       = "Block Host & Remember"
)

// The scope labels of the two kinds of rule that Codex remembers. A client
// labels the scope choice that keeps the rule with them. A blocked host
// states the host rule's label too, so a client pairs the block with the
// host rule's choice alone.
const (
	codexScopeLabelCommandRule = "Command rule"
	codexScopeLabelHostRule    = "Host rule"
)

// codexRuleScope is the scope of an amendment. Codex writes each one into the
// user's own Codex configuration, so the rule reaches every project of the
// user.
const codexRuleScope = leapmuxv1.PermissionScope_PERMISSION_SCOPE_USER

// codexPermissionGrantScopeSession is the scope of a permissions grant that
// lasts for the session. codexPermissionGrantScopeTurn is its narrower sibling.
const codexPermissionGrantScopeSession = "session"

// The options of an item/permissions/requestApproval request. Codex states no
// option list for it: the answer grants the requested set for the turn or for
// the session, or grants nothing. The two grants use the scope words as their
// ids.
const (
	codexPermissionOptionTurn    = codexPermissionGrantScopeTurn
	codexPermissionOptionSession = codexPermissionGrantScopeSession
	codexPermissionOptionDeny    = "deny"
)

// The answer words of a requestUserInput question. Codex's own terminal UI
// sends a typed note as a separate answer with this prefix. When the question
// offers Codex's own free-form option (`isOther`) and the user selected no
// option, the note follows that option's label.
const (
	codexUserNotePrefix   = "user_note: "
	codexOtherOptionLabel = "None of the above"
)

// The elicitation modes that Codex spells in its own words for an MCP form.
// The prompt states each one as the MCP form mode.
const (
	codexElicitationModeOpenAIForm      = "openai/form"
	codexElicitationModeOpenAIFormCamel = "openaiForm"
)

// The accept choice of an MCP tool approval that remembers nothing. The two
// other choices use the persist scopes of the MCP elicitation contract as
// their keys.
const codexElicitationAcceptOnce = "once"

// codexElicitationApprovalTitle is the title of an MCP tool approval whose
// request states none.
const codexElicitationApprovalTitle = "Permission Required"

// codexApprovalTitles gives the operation that each approval method asks
// about. item/permissions/requestApproval is absent on purpose: it asks for a
// set of permissions, not for one operation, and the set itself is its
// content.
var codexApprovalTitles = map[string]string{
	contracts.CodexMethodCommandExecutionRequestApproval: "Command Execution",
	contracts.CodexMethodFileChangeRequestApproval:       "File Change",
}

// codexControlRequest is what a Codex control request states, each field read
// on its own, so a malformed field leaves the others readable. A value of the
// wrong type reads as absent.
type codexControlRequest struct {
	// method is the JSON-RPC method of a request that Codex sent.
	method string
	// toolName is the tool name of a request that LeapMux published itself:
	// the plan-mode prompt or an async question.
	toolName string
	// toolInput is the request's tool input object when toolName is set.
	toolInput json.RawMessage
	// params is the request's params object, or nil when it states none.
	params map[string]json.RawMessage
	// rawParams is the request's params as Codex sent them.
	rawParams json.RawMessage
}

// readCodexControlRequest reads the fields of a control request that the
// prompt and the answer dispatch on. A payload that is no JSON object reads
// as a request that states nothing.
func readCodexControlRequest(payload []byte) codexControlRequest {
	fields := providerkit.RawFields(payload)
	reqFields := providerkit.RawFields(fields["request"])
	return codexControlRequest{
		method:    providerkit.ObjectString(fields, "method"),
		toolName:  providerkit.ObjectString(reqFields, "tool_name"),
		toolInput: providerkit.RawObjectOrEmpty(reqFields["input"]),
		params:    providerkit.RawFields(fields["params"]),
		rawParams: fields["params"],
	}
}

// ReadControlRequest reads Codex's control requests. The order decides a
// payload that matches two readers:
//
//   - A question: LeapMux's own async question, or item/tool/requestUserInput.
//   - An MCP elicitation that Codex relays.
//   - LeapMux's own plan-mode prompt, which approves the plan of the turn.
//   - Any other request is an approval, whose options are Codex's decisions.
//     item/permissions/requestApproval offers a grant for the turn or for the
//     session instead.
func (codexProvider) ReadControlRequest(input agent.ControlRequestInput) *leapmuxv1.ControlPrompt {
	request := readCodexControlRequest(input.Payload)
	switch {
	case request.toolName == contracts.CodexControlToolAsyncQuestion:
		return codexQuestionPrompt(codexAsyncQuestions(request.toolInput))
	case request.method == contracts.CodexMethodToolRequestUserInput:
		return codexQuestionPrompt(providerkit.QuestionsFromWire(request.params["questions"]))
	case request.method == contracts.MCPElicitationMethodCodex:
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Elicitation{Elicitation: codexElicitationPrompt(request.params)}}
	case request.toolName == contracts.CodexControlToolPlanModePrompt:
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{}}}
	default:
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: codexPermissionPrompt(request)}}
	}
}

// codexQuestionPrompt states questions. Codex reads an option selection and
// a typed note together in one answer, so neither replaces the other.
func codexQuestionPrompt(questions []*leapmuxv1.ControlQuestion) *leapmuxv1.ControlPrompt {
	return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
		Questions:               questions,
		PreservesSelectionNotes: true,
	}}}
}

// codexAsyncQuestions reads the one question of an async question request
// that publishAsyncQuestions wrote: its title, and each option that is not
// blank. A request with no title states no question, so a client can only
// skip it.
func codexAsyncQuestions(toolInput json.RawMessage) []*leapmuxv1.ControlQuestion {
	input := providerkit.RawFields(toolInput)
	title := providerkit.ObjectString(input, contracts.CodexAsyncQuestionInputTitle)
	if title == "" {
		return nil
	}
	entries, _ := providerkit.RawArray(input[contracts.CodexAsyncQuestionInputOptions])
	var options []*leapmuxv1.QuestionOption
	for _, entry := range entries {
		if label, ok := providerkit.RawString(entry); ok && strings.TrimSpace(label) != "" {
			options = append(options, &leapmuxv1.QuestionOption{Label: label})
		}
	}
	return []*leapmuxv1.ControlQuestion{{Question: title, Options: options}}
}

// codexQuestionsWithOther reports, for each question that
// providerkit.QuestionsFromWire reads from raw, whether Codex adds its own
// free-form option to it: `isOther` is true and the question states at least
// one valid option object. It uses providerkit.QuestionsFromWireWith so the
// two slices stay aligned by index.
func codexQuestionsWithOther(raw json.RawMessage) []bool {
	var withOther []bool
	providerkit.QuestionsFromWireWith(raw, func(fields map[string]json.RawMessage, question *leapmuxv1.ControlQuestion) {
		withOther = append(withOther, providerkit.RawBool(fields["isOther"]) && len(question.GetOptions()) > 0)
	})
	return withOther
}

// codexElicitationPrompt reads an MCP elicitation that Codex relays. An MCP
// tool approval (`_meta.codex_approval_kind`) is a permission: it states the
// tool's arguments, a default title and description, and the accept choices
// that remember the approval.
func codexElicitationPrompt(params map[string]json.RawMessage) *leapmuxv1.ElicitationPrompt {
	mode, ok := providerkit.RawString(params["mode"])
	if !ok || mode == codexElicitationModeOpenAIForm || mode == codexElicitationModeOpenAIFormCamel {
		mode = contracts.MCPElicitationModeForm
	}
	prompt := &leapmuxv1.ElicitationPrompt{
		Mode:    mode,
		Message: providerkit.ObjectString(params, "message"),
		Server:  providerkit.ObjectString(params, "serverName"),
		Url:     providerkit.ObjectString(params, "url"),
	}
	if schema := params["requestedSchema"]; providerkit.RawPresent(schema) {
		prompt.SchemaJson = string(schema)
	}
	title, hasTitle := providerkit.RawString(params["title"])
	description, hasDescription := providerkit.RawString(params["description"])
	meta := providerkit.RawFields(params["_meta"])
	if codexIsToolApprovalElicitation(meta) {
		prompt.Purpose = leapmuxv1.ElicitationPurpose_ELICITATION_PURPOSE_PERMISSION
		if arguments := meta["tool_params"]; providerkit.RawPresent(arguments) {
			prompt.ArgumentsJson = string(arguments)
		}
		if !hasTitle {
			title = codexElicitationApprovalTitle
		}
		if !hasDescription {
			description = providerkit.ObjectString(meta, "tool_description")
		}
		for _, choice := range codexElicitationAcceptChoices(meta) {
			prompt.AcceptChoices = append(prompt.AcceptChoices, &leapmuxv1.ElicitationAcceptChoice{Key: choice.key, Label: choice.label})
		}
	}
	prompt.Title = title
	prompt.Description = description
	return prompt
}

// codexIsToolApprovalElicitation reports whether an elicitation's `_meta`
// states an MCP tool-call or tool-suggestion approval.
func codexIsToolApprovalElicitation(meta map[string]json.RawMessage) bool {
	kind := providerkit.ObjectString(meta, "codex_approval_kind")
	return kind == contracts.MCPElicitationApprovalKindToolCall || kind == contracts.MCPElicitationApprovalKindToolSuggestion
}

// codexElicitationAcceptChoice is one accept answer of an MCP tool approval.
type codexElicitationAcceptChoice struct {
	key   string
	label string
	// persist is the scope that the answer's `_meta.persist` states, or ""
	// for the choice that remembers nothing.
	persist string
}

// codexElicitationAcceptChoices lists the accept answers of an MCP tool
// approval: once, then each scope of session and always that `_meta.persist`
// offers. An elicitation that is no tool approval offers none.
//
// It is the one reader of the offered scopes: the prompt offers these
// choices, the answer reads a choice's scope from them, and
// codexMCPElicitationMeta admits only a scope that they hold.
func codexElicitationAcceptChoices(meta map[string]json.RawMessage) []codexElicitationAcceptChoice {
	if !codexIsToolApprovalElicitation(meta) {
		return nil
	}
	persist := providerkit.RawStrings(meta["persist"])
	choices := []codexElicitationAcceptChoice{{key: codexElicitationAcceptOnce, label: "Once"}}
	if slices.Contains(persist, contracts.MCPElicitationApprovalScopeSession) {
		choices = append(choices, codexElicitationAcceptChoice{
			key: contracts.MCPElicitationApprovalScopeSession, label: "Session", persist: contracts.MCPElicitationApprovalScopeSession,
		})
	}
	if slices.Contains(persist, contracts.MCPElicitationApprovalScopeAlways) {
		choices = append(choices, codexElicitationAcceptChoice{
			key: contracts.MCPElicitationApprovalScopeAlways, label: "Always", persist: contracts.MCPElicitationApprovalScopeAlways,
		})
	}
	return choices
}

// codexPermissionPrompt reads an approval request. Codex states the operation
// in its method, and the reason, the command and the working directory in its
// params.
func codexPermissionPrompt(request codexControlRequest) *leapmuxv1.PermissionPrompt {
	permission := &leapmuxv1.PermissionPrompt{
		Title:            codexApprovalTitles[request.method],
		Reason:           providerkit.ObjectString(request.params, "reason"),
		Command:          providerkit.ObjectString(request.params, "command"),
		WorkingDirectory: providerkit.ObjectString(request.params, "cwd"),
	}
	if request.method == contracts.CodexMethodPermissionsRequestApproval {
		// The request asks for a set of permissions, and the set is what the
		// prompt draws as its arguments.
		requested, err := json.Marshal(codexRequestedPermissions(request.rawParams))
		if err == nil {
			permission.InputJson = string(requested)
		}
		permission.Options = codexPermissionGrantOptions()
		return permission
	}
	for _, choice := range codexDecisionChoices(request.params) {
		permission.Options = append(permission.Options, choice.option)
	}
	return permission
}

// codexPermissionGrantOptions are the answers to a permissions request: grant
// the requested set for the turn, grant it for the session, or grant nothing.
func codexPermissionGrantOptions() []*leapmuxv1.PermissionOption {
	return []*leapmuxv1.PermissionOption{
		{OptionId: codexPermissionOptionTurn, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: codexLabelAllow},
		{
			OptionId: codexPermissionOptionSession, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS,
			Name: codexLabelAllowForSession, Scope: leapmuxv1.PermissionScope_PERMISSION_SCOPE_SESSION,
		},
		{OptionId: codexPermissionOptionDeny, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: codexLabelDeny},
	}
}

// codexDecisionChoice is one decision that an approval can answer with: the
// option that a client chooses, and the `decision` value that the answer
// sends for it.
type codexDecisionChoice struct {
	option   *leapmuxv1.PermissionOption
	decision json.RawMessage
}

// codexDecisionChoices lists the decisions of an approval request, as
// options in the order of `availableDecisions`:
//
//   - A decision word keeps the word as its id, and a repeated word is one
//     option, because both copies send the same answer.
//   - An amendment's id is its key and its place in the list, so two rules of
//     one kind stay apart. The answer sends the object as Codex offered it.
//   - A value that is no known decision is dropped.
//
// A request that offers no known decision gets accept and cancel. A request
// whose list held an unknown value and no known approval also gets accept,
// because that value may have been the approval. A request with no refusal
// gets cancel, because a typed refusal must still answer the blocked turn.
func codexDecisionChoices(params map[string]json.RawMessage) []codexDecisionChoice {
	offered, _ := providerkit.RawArray(params["availableDecisions"])
	var choices []codexDecisionChoice
	known := 0
	for index, raw := range offered {
		choice, ok := parseCodexDecision(raw, index)
		if !ok {
			continue
		}
		known++
		if slices.ContainsFunc(choices, func(seen codexDecisionChoice) bool { return seen.option.GetOptionId() == choice.option.GetOptionId() }) {
			continue
		}
		choices = append(choices, choice)
	}
	if known == 0 {
		return []codexDecisionChoice{codexWordDecision(codexDecisionAccept), codexWordDecision(codexDecisionCancel)}
	}
	approves := slices.ContainsFunc(choices, func(choice codexDecisionChoice) bool { return agent.IsAllowOption(choice.option) })
	refuses := slices.ContainsFunc(choices, func(choice codexDecisionChoice) bool { return !agent.IsAllowOption(choice.option) })
	if known < len(offered) && !approves {
		choices = append(choices, codexWordDecision(codexDecisionAccept))
	}
	if !refuses {
		choices = append(choices, codexWordDecision(codexDecisionCancel))
	}
	return choices
}

// parseCodexDecision reads one element of `availableDecisions`, at index in
// the list. It accepts the four decision words, and an object with exactly
// one key that states a well-formed exec policy or network policy amendment.
// Any other value is no decision that LeapMux knows, and ok is false.
func parseCodexDecision(raw json.RawMessage, index int) (choice codexDecisionChoice, ok bool) {
	if word, isWord := providerkit.RawString(raw); isWord {
		switch word {
		case codexDecisionAccept, codexDecisionAcceptForSession, codexDecisionDecline, codexDecisionCancel:
			return codexWordDecision(word), true
		default:
			return codexDecisionChoice{}, false
		}
	}
	object := providerkit.RawFields(raw)
	if len(object) != 1 {
		return codexDecisionChoice{}, false
	}
	id := func(key string) string { return fmt.Sprintf("%s:%d", key, index) }
	if body, isExec := object[codexDecisionExecPolicyAmendment]; isExec {
		command, valid := codexExecPolicyAmendment(body)
		if !valid {
			return codexDecisionChoice{}, false
		}
		return codexDecisionChoice{
			option: &leapmuxv1.PermissionOption{
				OptionId:   id(codexDecisionExecPolicyAmendment),
				Kind:       leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS,
				Name:       codexLabelWithDetail(codexLabelAllowRemember, strings.Join(command, " ")),
				Scope:      codexRuleScope,
				ScopeLabel: codexScopeLabelCommandRule,
			},
			decision: raw,
		}, true
	}
	if body, isNetwork := object[codexDecisionNetworkPolicyAmendment]; isNetwork {
		host, action, valid := codexNetworkPolicyAmendment(body)
		if !valid {
			return codexDecisionChoice{}, false
		}
		option := &leapmuxv1.PermissionOption{
			OptionId:   id(codexDecisionNetworkPolicyAmendment),
			Kind:       leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS,
			Name:       codexLabelWithDetail(codexLabelAllowHost, host),
			Scope:      codexRuleScope,
			ScopeLabel: codexScopeLabelHostRule,
		}
		if action == codexNetworkPolicyDeny {
			option.Kind = leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ALWAYS
			option.Name = codexLabelWithDetail(codexLabelBlockHost, host)
		}
		return codexDecisionChoice{option: option, decision: raw}, true
	}
	return codexDecisionChoice{}, false
}

// codexWordDecision is the choice of one decision word. The caller passes one
// of the four words that parseCodexDecision accepts.
func codexWordDecision(word string) codexDecisionChoice {
	option := &leapmuxv1.PermissionOption{OptionId: word}
	switch word {
	case codexDecisionAccept:
		option.Kind, option.Name = leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, codexLabelAllow
	case codexDecisionAcceptForSession:
		option.Kind, option.Name = leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS, codexLabelAllowForSession
		option.Scope = leapmuxv1.PermissionScope_PERMISSION_SCOPE_SESSION
	case codexDecisionDecline:
		option.Kind, option.Name = leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, codexLabelDeny
	default:
		// Codex reads cancel as a refusal that also stops the turn: the agent
		// does nothing more until the user's next message.
		option.Kind, option.Name = leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, codexLabelCancel
		option.EndsTurn = true
	}
	return codexDecisionChoice{option: option, decision: json.RawMessage(providerkit.JSONText(word))}
}

// codexExecPolicyAmendment reads the body of an exec policy amendment: an
// object whose `execpolicy_amendment` is an array of strings, the command
// prefix that the rule allows.
func codexExecPolicyAmendment(body json.RawMessage) (command []string, ok bool) {
	parts, isArray := providerkit.RawArray(providerkit.RawFields(body)["execpolicy_amendment"])
	if !isArray {
		return nil, false
	}
	command = make([]string, 0, len(parts))
	for _, part := range parts {
		word, isString := providerkit.RawString(part)
		if !isString {
			return nil, false
		}
		command = append(command, word)
	}
	return command, true
}

// codexNetworkPolicyAmendment reads the body of a network policy amendment:
// an object whose `network_policy_amendment` states a host and an action of
// allow or deny.
func codexNetworkPolicyAmendment(body json.RawMessage) (host, action string, ok bool) {
	amendment := providerkit.RawFields(providerkit.RawFields(body)["network_policy_amendment"])
	host, isHost := providerkit.RawString(amendment["host"])
	action, _ = providerkit.RawString(amendment["action"])
	if !isHost || (action != codexNetworkPolicyAllow && action != codexNetworkPolicyDeny) {
		return "", "", false
	}
	return host, action, true
}

// codexLabelWithDetail appends the subject of a remembered rule to its label,
// after a colon, so two rules of one kind read apart. The option states its
// scope and its scope label, so a client reads no scope from this text.
func codexLabelWithDetail(label, detail string) string {
	if detail == "" {
		return label
	}
	return label + ": " + detail
}

// AnswerControlRequest builds Codex's answer to a decision:
//
//   - An approval answers with a JSON-RPC result: the chosen decision, or the
//     permissions grant. A refusal's feedback follows the answer as a message,
//     because Codex's answer has no field for it.
//   - A requestUserInput question answers with a JSON-RPC result of answers.
//   - An async question, and the plan-mode prompt, answer in the neutral
//     envelope, because LeapMux owns them.
//   - An MCP elicitation answers in the neutral envelope, which
//     ResolveControlResponse turns into the JSON-RPC result.
func (codexProvider) AnswerControlRequest(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	request := readCodexControlRequest(input.Request.Payload)
	switch input.Prompt.GetKind().(type) {
	case *leapmuxv1.ControlPrompt_Question:
		if request.toolName == contracts.CodexControlToolAsyncQuestion {
			return codexAsyncQuestionAnswer(input)
		}
		return codexUserInputAnswer(input, request.params)
	case *leapmuxv1.ControlPrompt_Elicitation:
		return agent.AnswerElicitation(input, codexElicitationMetadata(request.params))
	case *leapmuxv1.ControlPrompt_Plan:
		return agent.AnswerNeutralPlan(input)
	case *leapmuxv1.ControlPrompt_Permission:
		return codexPermissionAnswer(input, request)
	default:
		return agent.ControlAnswer{}, agent.InvalidControlDecision("Codex states no %T prompt", input.Prompt.GetKind())
	}
}

// codexPermissionAnswer answers an approval with the chosen option. An option
// that allows takes no feedback: a client sends typed text only in place of a
// refusal.
func codexPermissionAnswer(input agent.ControlAnswerInput, request codexControlRequest) (agent.ControlAnswer, error) {
	decision, option, err := agent.PermissionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	feedback := decision.GetFeedback()
	var result any
	if request.method == contracts.CodexMethodPermissionsRequestApproval {
		result, err = codexPermissionGrant(option.GetOptionId(), request.rawParams)
	} else {
		result, err = codexDecisionResult(option.GetOptionId(), request.params)
	}
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	answer := agent.ControlAnswer{Content: agent.JSONRPCResult(input.RequestID, result)}
	// Text of whitespace alone still refuses, but says nothing worth a message.
	if strings.TrimSpace(feedback) != "" {
		answer.FollowUp = feedback
	}
	return answer, nil
}

// codexPermissionGrant is the result that answers a permissions request: the
// requested set for the chosen scope, or an empty set for the turn, which
// grants nothing.
func codexPermissionGrant(optionID string, rawParams json.RawMessage) (map[string]any, error) {
	switch optionID {
	case codexPermissionOptionTurn, codexPermissionOptionSession:
		return map[string]any{"permissions": codexRequestedPermissions(rawParams), "scope": optionID}, nil
	case codexPermissionOptionDeny:
		return map[string]any{"permissions": map[string]any{}, "scope": codexPermissionGrantScopeTurn}, nil
	default:
		return nil, agent.InvalidControlDecision("a permissions request offers no option %q", optionID)
	}
}

// codexDecisionResult is the result that sends the decision of one option.
func codexDecisionResult(optionID string, params map[string]json.RawMessage) (map[string]any, error) {
	for _, choice := range codexDecisionChoices(params) {
		if choice.option.GetOptionId() == optionID {
			return map[string]any{"decision": choice.decision}, nil
		}
	}
	return nil, agent.InvalidControlDecision("the approval offers no decision %q", optionID)
}

// codexUserInputAnswer answers a requestUserInput question with one entry for
// each question, keyed by its id, else its header, else "q<index>". An entry
// lists the selected option values, then the typed note as a `user_note`
// answer. A note with no selection on a question with Codex's own free-form
// option follows that option's label. An unanswered question keeps an empty
// list, as Codex's own terminal UI sends it.
//
// A cancelled decision answers with no entries. Codex's answer has no field
// for a reason, so the decision's feedback goes nowhere.
func codexUserInputAnswer(input agent.ControlAnswerInput, params map[string]json.RawMessage) (agent.ControlAnswer, error) {
	decision, questions, err := agent.QuestionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	type entry struct {
		Answers []string `json:"answers"`
	}
	answers := map[string]entry{}
	if !decision.GetCancelled() {
		withOther := codexQuestionsWithOther(params["questions"])
		for i, question := range questions {
			key := question.GetId()
			if key == "" {
				key = question.GetHeader()
			}
			if key == "" {
				key = fmt.Sprintf("q%d", i)
			}
			answer := agent.QuestionAnswerAt(decision, i)
			values := append([]string{}, answer.GetSelected()...)
			if note := agent.TrimJavaScriptSpace(answer.GetCustomText()); note != "" {
				if len(values) == 0 && i < len(withOther) && withOther[i] {
					values = append(values, codexOtherOptionLabel)
				}
				values = append(values, codexUserNotePrefix+note)
			}
			answers[key] = entry{Answers: values}
		}
	}
	return agent.ControlAnswer{Content: agent.JSONRPCResult(input.RequestID, map[string]any{"answers": answers})}, nil
}

// codexAsyncQuestionAnswer answers one async question in the neutral
// envelope, which resolveCodexAsyncQuestionResponse reads. The answer is the
// selected option, the typed note, or both, with a blank line between them,
// because Codex keeps no pending question state and the whole answer travels
// in one message. An answer with neither is no answer.
//
// A cancelled decision skips the question with a bare deny. Nothing reads a
// reason for a skip, so the decision's feedback goes nowhere.
func codexAsyncQuestionAnswer(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, _, err := agent.QuestionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	if decision.GetCancelled() {
		return agent.ControlAnswer{Content: agent.DenyResponse(input.RequestID, "")}, nil
	}
	answer := agent.QuestionAnswerAt(decision, 0)
	selection := ""
	if selected := answer.GetSelected(); len(selected) > 0 {
		selection = selected[0]
	}
	note := agent.TrimJavaScriptSpace(answer.GetCustomText())
	var text string
	switch {
	case selection != "" && note != "":
		text = selection + "\n\n" + note
	case selection != "":
		text = selection
	default:
		text = note
	}
	if text == "" {
		return agent.ControlAnswer{}, agent.InvalidControlDecision("the async question takes a selected option or a typed answer")
	}
	return agent.ControlAnswer{Content: agent.ControlResponseEnvelope(input.RequestID, map[string]any{
		"behavior": contracts.ControlAnswerBehaviorAllow,
		"answer":   text,
	})}, nil
}

// codexElicitationMetadata reads the `_meta` of one accept choice of an MCP
// tool approval: the persist scope that the choice remembers. The choice that
// remembers nothing has none.
func codexElicitationMetadata(params map[string]json.RawMessage) func(key string) (json.RawMessage, bool) {
	choices := codexElicitationAcceptChoices(providerkit.RawFields(params["_meta"]))
	return func(key string) (json.RawMessage, bool) {
		for _, choice := range choices {
			if choice.key != key || choice.persist == "" {
				continue
			}
			encoded, err := json.Marshal(map[string]string{"persist": choice.persist})
			if err != nil {
				return nil, false
			}
			return encoded, true
		}
		return nil, false
	}
}
