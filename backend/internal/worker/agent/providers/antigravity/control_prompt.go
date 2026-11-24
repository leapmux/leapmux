package antigravity

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The options of an Antigravity permission. The language server's answer
// states `allow` for this one step, so the prompt offers a once pair.
const (
	antigravityOptionAllow  = "allow"
	antigravityOptionReject = "reject"
)

// antigravityPermissionScopeOnce is the language server's scope of an answer
// that decides this one step.
const antigravityPermissionScopeOnce = "PERMISSION_SCOPE_ONCE"

// antigravityRequestIDPrefix starts the id of a request that the language
// server raised: `antigravity:<cascade>:<trajectory>:<step>`
// (antigravityAddress.requestID).
const antigravityRequestIDPrefix = "antigravity"

// The titles of a permission that states no description of its own, by its
// resource's action. Any other action reads as antigravityPermissionTitle.
var antigravityPermissionTitles = map[string]string{
	contracts.AntigravityPermissionActionCommand:   "Run command",
	contracts.AntigravityPermissionActionReadFile:  "Read file",
	contracts.AntigravityPermissionActionWriteFile: "Write file",
	contracts.AntigravityPermissionActionMcp:       "MCP tool",
}

// The titles of a permission whose action has no title of its own, and of
// an interaction that is neither a question nor a permission.
const (
	antigravityPermissionTitle  = "Permission"
	antigravityInteractionTitle = "Antigravity interaction"
)

// antigravityPermissionOptions are the answers of every Antigravity
// permission: allow once and reject.
func antigravityPermissionOptions() []*leapmuxv1.PermissionOption {
	return []*leapmuxv1.PermissionOption{
		{OptionId: antigravityOptionAllow, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: "Allow once"},
		{OptionId: antigravityOptionReject, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: "Reject"},
	}
}

// ReadControlRequest reads the control requests that the Worker publishes
// for Antigravity CLI:
//
//   - An interaction that asks questions is a question.
//   - The plan review that the Worker raises itself is a plan, with the plan.
//   - An interaction that asks a permission is a permission for its
//     resource. A command states its command line.
//   - Any other interaction is a permission to answer the interaction as a
//     whole.
//
// Each permission offers the language server's allow and reject. Any other
// payload reads as unrecognized: the Worker publishes no interaction without
// the interaction that its step requests, so LeapMux cannot know what such a
// step asks, and an answer would guess.
func (antigravityProvider) ReadControlRequest(input agent.ControlRequestInput) *leapmuxv1.ControlPrompt {
	raw, interaction, interacts := antigravityInteraction(input.Payload)
	if ask, ok := providerkit.RawObject(interaction["askQuestion"]); ok {
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{Questions: antigravityQuestions(ask)}}}
	}
	if agent.ControlToolName(input.Payload) == contracts.AntigravityControlToolPlanReview {
		review, _ := providerkit.RawObject(agent.ControlToolInput(input.Payload))
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{
			Text: providerkit.ObjectString(review, contracts.AntigravityControlPayloadPlanContent),
		}}}
	}
	if !interacts {
		return agent.UnrecognizedControlPrompt()
	}
	if permission, ok := providerkit.RawObject(interaction["permission"]); ok {
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: antigravityPermissionPrompt(permission)}}
	}
	// raw is a JSON object, so the arguments are valid JSON as they stand.
	return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{
		Title:     antigravityInteractionTitle,
		InputJson: `{"interaction":` + string(raw) + `}`,
		Options:   antigravityPermissionOptions(),
	}}}
}

// antigravityInteraction reads the `requestedInteraction` object of a stored
// payload, as its JSON text and as its fields. It reports false when the
// payload states none.
func antigravityInteraction(payload []byte) (json.RawMessage, map[string]json.RawMessage, bool) {
	fields, _ := providerkit.RawObject(payload)
	raw := fields["requestedInteraction"]
	interaction, ok := providerkit.RawObject(raw)
	if !ok {
		return nil, nil, false
	}
	return raw, interaction, true
}

// antigravityPermissionPrompt reads a permission interaction: its own
// description as the title, else the title of its resource's action, and the
// action and the target as the arguments.
func antigravityPermissionPrompt(permission map[string]json.RawMessage) *leapmuxv1.PermissionPrompt {
	resource, _ := providerkit.RawObject(permission["resource"])
	action := providerkit.ObjectString(resource, "action")
	target := providerkit.ObjectString(resource, "target")
	title := providerkit.ObjectString(permission, "actionDescription")
	if title == "" {
		title = antigravityPermissionTitles[action]
	}
	if title == "" {
		title = antigravityPermissionTitle
	}
	prompt := &leapmuxv1.PermissionPrompt{Title: title, Options: antigravityPermissionOptions()}
	if arguments, err := json.Marshal(map[string]string{"action": action, "target": target}); err == nil {
		prompt.InputJson = string(arguments)
	}
	if action == contracts.AntigravityPermissionActionCommand {
		prompt.Command = target
	}
	return prompt
}

type antigravityQuestionRecord struct {
	wire   map[string]json.RawMessage
	prompt *leapmuxv1.ControlQuestion
}

// antigravityQuestionRecords preserves every native position. Records with no
// displayable question receive a skipped response and stay out of the prompt.
func antigravityQuestionRecords(ask map[string]json.RawMessage) []antigravityQuestionRecord {
	elements, _ := providerkit.RawArray(ask["questions"])
	records := make([]antigravityQuestionRecord, 0, len(elements))
	for _, element := range elements {
		wire := providerkit.RawFields(element)
		record := antigravityQuestionRecord{wire: wire}
		if question := providerkit.ObjectString(wire, "question"); agent.TrimJavaScriptSpace(question) != "" {
			record.prompt = &leapmuxv1.ControlQuestion{
				Id:          providerkit.ObjectString(wire, "id"),
				Question:    question,
				MultiSelect: providerkit.RawBool(wire["isMultiSelect"]),
			}
			options, _ := providerkit.RawArray(wire["options"])
			for _, raw := range options {
				option := providerkit.RawFields(raw)
				label := providerkit.ObjectString(option, "text")
				if agent.TrimJavaScriptSpace(label) == "" {
					continue
				}
				choice := &leapmuxv1.QuestionOption{Label: label}
				if id, ok := providerkit.RawString(option["id"]); ok {
					choice.Value = &id
				}
				record.prompt.Options = append(record.prompt.Options, choice)
			}
		}
		records = append(records, record)
	}
	return records
}

func antigravityQuestions(ask map[string]json.RawMessage) []*leapmuxv1.ControlQuestion {
	var questions []*leapmuxv1.ControlQuestion
	for _, record := range antigravityQuestionRecords(ask) {
		if record.prompt != nil {
			questions = append(questions, record.prompt)
		}
	}
	return questions
}

// antigravityAddress is the step of a cascade that one interaction belongs
// to, which the language server's answer states.
type antigravityAddress struct {
	cascadeID    string
	trajectoryID string
	stepIndex    int
}

// antigravityAddressOf reads the address out of the id of a request that the
// language server raised. It reads only an id that a valid address writes
// (antigravityAddress.requestID). Any other id fails, because an answer
// echoes the address, and ControlResponseRequestID reads from the answer an
// id that would not find the request.
func antigravityAddressOf(requestID string) (antigravityAddress, error) {
	parts := strings.Split(requestID, ":")
	if len(parts) == 4 && parts[0] == antigravityRequestIDPrefix {
		if step, err := strconv.Atoi(parts[3]); err == nil {
			address := antigravityAddress{cascadeID: parts[1], trajectoryID: parts[2], stepIndex: step}
			if address.valid() && address.requestID() == requestID {
				return address, nil
			}
		}
	}
	return antigravityAddress{}, fmt.Errorf("the Antigravity request ID %q is malformed", requestID)
}

// valid reports whether the address states each field that an answer needs:
// a cascade, a trajectory, and a step index that is not negative. A colon in
// the cascade or the trajectory is invalid too, because the request ID joins
// the fields with colons, and antigravityAddressOf could not read such an ID
// back: every answer to the request would fail.
func (a antigravityAddress) valid() bool {
	return a.cascadeID != "" && a.trajectoryID != "" && a.stepIndex >= 0 &&
		!strings.Contains(a.cascadeID, ":") && !strings.Contains(a.trajectoryID, ":")
}

// requestID is the id of the request that the language server raised at the
// address: `antigravity:<cascade>:<trajectory>:<step>`.
func (a antigravityAddress) requestID() string {
	return fmt.Sprintf("%s:%s:%s:%d", antigravityRequestIDPrefix, a.cascadeID, a.trajectoryID, a.stepIndex)
}

// answer is the language server's answer to the step: the interaction field
// key holding value. ControlResponseRequestID reads the address back out of
// it.
func (a antigravityAddress) answer(key string, value any) ([]byte, error) {
	encoded, err := json.Marshal(map[string]any{
		"cascadeId": a.cascadeID,
		"interaction": map[string]any{
			"trajectoryId": a.trajectoryID,
			"stepIndex":    a.stepIndex,
			key:            value,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("encode the Antigravity answer: %w", err)
	}
	return encoded, nil
}

// antigravityPermissionAnswer is the language server's answer to a
// permission: allow or refuse this one step, with the user's instruction for
// a refusal.
func antigravityPermissionAnswer(requestID string, allow bool, instruction string) ([]byte, error) {
	address, err := antigravityAddressOf(requestID)
	if err != nil {
		return nil, err
	}
	permission := map[string]any{"allow": allow, "scope": antigravityPermissionScopeOnce}
	if instruction != "" {
		permission["userDenyInstruction"] = instruction
	}
	return address.answer("permission", permission)
}

// AnswerControlRequest builds Antigravity's answer:
//
//   - A permission answers the language server natively: allow or refuse
//     this step, and a refusal's feedback as the instruction that the agent
//     reads. The language server reads no other answer, and
//     ResolveControlResponse refuses the shared envelope.
//   - A question answers natively with each question's selected option ids
//     and typed text. A cancel dismisses the questions; the language server
//     reads no reason for it.
//   - The plan review takes the shared plan answer, which the Worker reads
//     itself.
func (antigravityProvider) AnswerControlRequest(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	switch input.Prompt.GetKind().(type) {
	case *leapmuxv1.ControlPrompt_Question:
		return antigravityQuestionAnswer(input)
	case *leapmuxv1.ControlPrompt_Plan:
		return agent.AnswerNeutralPlan(input)
	case *leapmuxv1.ControlPrompt_Permission:
		return antigravityPermissionDecisionAnswer(input)
	default:
		return agent.ControlAnswer{}, agent.InvalidControlDecision("Antigravity CLI states no %T prompt", input.Prompt.GetKind())
	}
}

// antigravityPermissionDecisionAnswer answers a permission prompt with the
// language server's own permission answer.
func antigravityPermissionDecisionAnswer(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, option, err := agent.PermissionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	content, err := antigravityPermissionAnswer(input.RequestID, !agent.IsRefusalOption(option), decision.GetFeedback())
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	return agent.ControlAnswer{Content: content}, nil
}

// antigravityQuestionAnswer returns a response for each native question slot.
// QuestionDecisionOf validates the answers in prompt order. Skipped native
// slots leave the prompt index unchanged.
func antigravityQuestionAnswer(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, questions, err := agent.QuestionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	address, err := antigravityAddressOf(input.RequestID)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	if decision.GetCancelled() {
		content, err := address.answer("askQuestion", map[string]any{"cancelled": true})
		if err != nil {
			return agent.ControlAnswer{}, err
		}
		return agent.ControlAnswer{Content: content}, nil
	}
	_, interaction, _ := antigravityInteraction(input.Request.Payload)
	ask, _ := providerkit.RawObject(interaction["askQuestion"])
	records := antigravityQuestionRecords(ask)
	responses := make([]map[string]any, 0, len(records))
	questionIndex := 0
	for _, record := range records {
		response := make(map[string]any, len(record.wire)+2)
		for key, value := range record.wire {
			response[key] = value
		}
		responses = append(responses, response)
		if record.prompt == nil {
			response["skipped"] = true
			continue
		}
		if questionIndex >= len(questions) {
			return agent.ControlAnswer{}, agent.InvalidControlDecision("the Antigravity question prompt differs from the native request")
		}
		answer := agent.QuestionAnswerAt(decision, questionIndex)
		values := agent.QuestionAnswerValues(questions[questionIndex], answer)
		questionIndex++
		// QuestionAnswerValues lists selected option values before typed text.
		selected, typed := values[:len(answer.GetSelected())], values[len(answer.GetSelected()):]
		if len(selected) > 0 {
			response["selectedOptionIds"] = selected
		}
		if len(typed) > 0 {
			response["writeInResponse"] = typed[0]
		}
	}
	if questionIndex != len(questions) {
		return agent.ControlAnswer{}, agent.InvalidControlDecision("the Antigravity question prompt differs from the native request")
	}
	content, err := address.answer("askQuestion", map[string]any{"responses": responses})
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	return agent.ControlAnswer{Content: content}, nil
}
