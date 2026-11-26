package cline

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The tools of Cline's agent teams that start an agent: a new teammate, and a
// teammate's task run.
const (
	clineToolTeamSpawnTeammate = "team_spawn_teammate"
	clineToolTeamRunTask       = "team_run_task"
)

// clineToolTitleFallback is the title of an approval that states no tool name.
const clineToolTitleFallback = "Tool"

// clineEventPayload returns the `payload` member of payload when payload is
// Cline's own event envelope for event: an object whose `event` is that
// name. A `payload` member that is not an object reads as an empty one. It
// reports false for another event and for a row that is no envelope.
func clineEventPayload(payload []byte, event string) (map[string]json.RawMessage, bool) {
	envelope, ok := providerkit.RawObject(payload)
	if !ok {
		return nil, false
	}
	if name, _ := providerkit.RawString(envelope[contracts.ClineEventFieldEvent]); name != event {
		return nil, false
	}
	fields, ok := providerkit.RawObject(envelope[contracts.ClineEventFieldPayload])
	if !ok {
		fields = map[string]json.RawMessage{}
	}
	return fields, true
}

// ReadControlRequest reads the two requests that the Worker publishes for
// Cline, each Cline's own event envelope:
//
//   - A `capability.requested` of the question executor is a question.
//   - An `approval.requested` of the plan tool, `switch_to_act_mode`, is the
//     plan approval. The model calls the tool after it presented the plan in
//     its answer, so the plan is in the transcript.
//   - Any other `approval.requested` is a permission for the call it states.
//
// Any other payload reads as unrecognized: Cline's hub takes an answer only
// to an approval or to a question, and the resolution refuses any other.
func (clineProvider) ReadControlRequest(input agent.ControlRequestInput) *leapmuxv1.ControlPrompt {
	if questions, ok := clineQuestions(input.Payload); ok {
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
			Questions: questions,
		}}}
	}
	approval, ok := clineEventPayload(input.Payload, contracts.ClineEventApprovalRequested)
	if !ok {
		return agent.UnrecognizedControlPrompt()
	}
	toolName, _ := providerkit.RawString(approval["toolName"])
	if toolName == contracts.ClineToolSwitchToActMode {
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{}}}
	}
	return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: clineApprovalPrompt(toolName, approval)}}
}

// clineApprovalPrompt is the prompt of one tool approval: the tool name as
// its title, the arguments, LeapMux's allow and deny, the command of a
// command call, and a warning for a call that starts an agent. Cline's
// approval.respond takes a yes or a no and a reason, so the approval offers
// no options of its own.
//
// The arguments are the JSON text of `inputJson`. Text that does not decode
// to a JSON object states no arguments, which read as `{}`.
func clineApprovalPrompt(toolName string, approval map[string]json.RawMessage) *leapmuxv1.PermissionPrompt {
	inputJSON, _ := providerkit.RawString(approval["inputJson"])
	arguments := json.RawMessage("{}")
	if _, ok := providerkit.RawObject(json.RawMessage(inputJSON)); ok {
		var compact bytes.Buffer
		if json.Compact(&compact, []byte(inputJSON)) == nil {
			arguments = compact.Bytes()
		}
	}
	title := toolName
	if title == "" {
		title = clineToolTitleFallback
	}
	permission := &leapmuxv1.PermissionPrompt{
		Title:     title,
		InputJson: string(arguments),
		Options:   agent.AllowDenyOptions(),
	}
	if startsAgent(toolName) {
		permission.Reason = contracts.ClinePromptReasonSpawnAgent
	}
	if toolName == contracts.ClineToolRunCommands {
		permission.Command = clineCommandLines(arguments)
	}
	return permission
}

// startsAgent reports whether a call starts an agent that runs its tools
// without asking: a subagent, a configured agent, a new teammate, or a
// teammate's task run.
func startsAgent(toolName string) bool {
	return startsSubagent(toolName) || toolName == clineToolTeamSpawnTeammate || toolName == clineToolTeamRunTask
}

// clineCommandLines returns the commands that a `run_commands` call runs, one
// line each. The call states a list in `commands`, whose elements are a line
// of shell or a program with its arguments, or one line in `command`. An
// empty line is no command, so a call whose lines are all empty states none.
func clineCommandLines(arguments json.RawMessage) string {
	fields, _ := providerkit.RawObject(arguments)
	var elements []json.RawMessage
	if json.Unmarshal(fields["commands"], &elements) != nil || elements == nil {
		// `commands` is not a list, so the call states one line in `command`.
		command, _ := providerkit.RawString(fields["command"])
		return command
	}
	lines := make([]string, 0, len(elements))
	for _, element := range elements {
		if line := clineCommandText(element); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// clineCommandText returns the text of one element of `commands`: a line of
// shell as it stands, or a program and its text arguments joined with
// spaces. Any other element, and an empty part, states nothing.
func clineCommandText(element json.RawMessage) string {
	if line, ok := providerkit.RawString(element); ok {
		return line
	}
	command, ok := providerkit.RawObject(element)
	if !ok {
		return ""
	}
	var parts []string
	if program, _ := providerkit.RawString(command["command"]); program != "" {
		parts = append(parts, program)
	}
	for _, text := range providerkit.RawStrings(command["args"]) {
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, " ")
}

// clineQuestions reads a question request: the `capability.requested` of the
// question executor. It reports false for any other payload.
//
// The executor's arguments are the question and its options, in that order.
// A question that is not text, or that is blank, states no question, and an
// option that is not text is dropped. The question is trimmed, and each
// option's label is also the value that the answer sends.
func clineQuestions(payload []byte) ([]*leapmuxv1.ControlQuestion, bool) {
	request, ok := clineEventPayload(payload, contracts.ClineEventCapabilityRequested)
	if !ok {
		return nil, false
	}
	if name, _ := providerkit.RawString(request["capabilityName"]); name != contracts.ClineCapabilityAskQuestion {
		return nil, false
	}
	executor, _ := providerkit.RawObject(request["payload"])
	var arguments []json.RawMessage
	if json.Unmarshal(executor["args"], &arguments) != nil || len(arguments) == 0 {
		return nil, true
	}
	text, _ := providerkit.RawString(arguments[0])
	text = agent.TrimJavaScriptSpace(text)
	if text == "" {
		return nil, true
	}
	question := &leapmuxv1.ControlQuestion{Question: text}
	if len(arguments) > 1 {
		for _, label := range providerkit.RawStrings(arguments[1]) {
			question.Options = append(question.Options, &leapmuxv1.QuestionOption{Value: &label, Label: label})
		}
	}
	return []*leapmuxv1.ControlQuestion{question}, true
}

// AnswerControlRequest builds Cline's answer in the neutral envelope, which
// ResolveControlResponse turns into Cline's own reply:
//
//   - A question allows with the answer text (clineQuestionAnswer), and a
//     refusal denies with the user's feedback.
//   - A plan approval allows with the LeapMux settings, and a refusal denies
//     with the feedback.
//   - A permission allows, or denies with the feedback.
func (clineProvider) AnswerControlRequest(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	switch input.Prompt.GetKind().(type) {
	case *leapmuxv1.ControlPrompt_Question:
		return clineQuestionAnswer(input)
	case *leapmuxv1.ControlPrompt_Plan:
		return agent.AnswerNeutralPlan(input)
	case *leapmuxv1.ControlPrompt_Permission:
		return agent.AnswerAllowDeny(input)
	default:
		return agent.ControlAnswer{}, agent.InvalidControlDecision("Cline states no %T prompt", input.Prompt.GetKind())
	}
}

// clineQuestionAnswer answers the one question of a question request. The
// answer is one line of text: the answer values of the question
// (agent.QuestionAnswerValues), or nothing. ResolveControlResponse refuses
// an answer with no text, because Cline's executor needs one.
//
// A Cline question is never a multiple choice (clineQuestions), so it has one
// value at most: the selected option, or else the typed text.
// QuestionDecisionOf refuses a decision that states both. The values are
// joined with ", ", as the Claude shape joins a multiple-choice answer, so a
// prompt with a multiple-choice question would lose no value.
//
// A cancelled decision denies with its feedback.
func clineQuestionAnswer(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, questions, err := agent.QuestionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	if decision.GetCancelled() {
		return agent.ControlAnswer{Content: agent.DenyResponse(input.RequestID, decision.GetFeedback())}, nil
	}
	var text string
	if len(questions) > 0 {
		text = strings.Join(agent.QuestionAnswerValues(questions[0], agent.QuestionAnswerAt(decision, 0)), ", ")
	}
	return agent.ControlAnswer{Content: agent.ControlResponseEnvelope(input.RequestID, map[string]any{
		"behavior":                          contracts.ControlAnswerBehaviorAllow,
		contracts.ClineQuestionAnswerAnswer: text,
	})}, nil
}
