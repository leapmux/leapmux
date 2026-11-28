package junie

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"

	"github.com/google/uuid"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

func (a *Agent) junieControlOwner(sessionID string, line *providerkit.ParsedLine, needsTurn bool) (acp.ControlOwnerResolution, error) {
	if !needsTurn {
		return acp.ControlOwnerResolution{}, nil
	}
	turnID, inputIDs := a.junieSteerControlOwner(sessionID, line)
	return acp.ControlOwnerResolution{TurnID: turnID, InputIDs: inputIDs}, nil
}

func (a *Agent) junieSteerControlOwner(sessionID string, line *providerkit.ParsedLine) (string, []string) {
	selector, ok := junieControlSelectorOf(line)
	if !ok || sessionID == "" || a.CurrentSessionID() != sessionID {
		return "", nil
	}
	a.steerReplies.mu.Lock()
	turn := a.steerReplies.active
	if turn == nil || turn.sessionID != sessionID {
		a.steerReplies.mu.Unlock()
		return "", nil
	}
	cursor, inputs := turn.cursor, slices.Clone(turn.inputs)
	a.steerReplies.mu.Unlock()

	data, err := readJunieEvents(junieHome(agent.StoredSessionQuery{HomeDir: a.homeDir}), sessionID)
	if err != nil {
		return "", nil
	}
	admissions, task, ok := readJunieControlEvents(cursor, data, inputs, selector)
	if !ok || a.CurrentSessionID() != sessionID {
		return "", nil
	}
	inputIDs := make([]string, 0, len(task.inputIDs))
	seen := make(map[string]bool, len(task.inputIDs))
	for _, id := range task.inputIDs {
		receipt, found := admissions[id]
		if !found || seen[receipt.InputID] {
			return "", nil
		}
		seen[receipt.InputID] = true
		inputIDs = append(inputIDs, receipt.InputID)
	}
	return task.id, inputIDs
}

type junieControlSelector struct {
	toolCallID string
	form       *junieACPForm
}

func junieControlSelectorOf(line *providerkit.ParsedLine) (junieControlSelector, bool) {
	switch line.Method {
	case "session/request_permission":
		params, _ := providerkit.RawObject(line.Params)
		id := providerkit.StringField(params["toolCall"], "toolCallId")
		return junieControlSelector{toolCallID: id}, validJunieStepID(id)
	case "elicitation/create":
		var form junieACPForm
		if json.Unmarshal(line.Params, &form) != nil || form.Mode != "form" ||
			form.Schema.Type != "object" || form.Schema.Title != "Questions" {
			return junieControlSelector{}, false
		}
		return junieControlSelector{form: &form}, true
	default:
		return junieControlSelector{}, false
	}
}

func validJunieStepID(stepID string) bool {
	parsed, err := uuid.Parse(stepID)
	return err == nil && parsed.String() == stepID
}

type junieControlTask struct {
	id       string
	inputIDs []string
}

type junieControlTaskParser struct {
	selector       junieControlSelector
	task           junieControlTask
	terminalInputs []string
	questions      map[string]juniePendingQuestion
	questionOrder  []string
}

func (p *junieControlTaskParser) observe(event *junieSteerJournalEvent) {
	switch event.Kind {
	case "TaskStartedEvent", "TaskState":
		p.task = junieControlTask{}
		p.terminalInputs, p.questions, p.questionOrder = nil, nil, nil
		if event.Kind == "TaskStartedEvent" {
			p.task.id = event.TaskID
		}
	case "UserMessagesCommittedToHistory":
		p.task.inputIDs = event.CommittedInputIDs
	case "SessionA2uxEvent":
		body := event.Event.Payload
		if event.TaskID != p.task.id || body.Agent.Kind != "MainAgent" || body.Agent.ID != "main" {
			return
		}
		switch body.Kind {
		case "TerminalBlockUpdatedEvent":
			if p.selector.toolCallID != "" && body.StepID == p.selector.toolCallID {
				pending := body.Status == "IN_PROGRESS" &&
					(event.Event.State == "IN_PROGRESS" || event.Event.State == "INPUT_REQUIRED")
				if !pending {
					p.terminalInputs = nil
				} else if len(p.terminalInputs) == 0 {
					p.terminalInputs = p.task.inputIDs
				}
			}
		case "AskAsyncRequestUpdatedEvent":
			if p.selector.form == nil || !validJunieStepID(body.StepID) || !validJunieStepID(body.Request.ID) {
				return
			}
			if event.Event.State != "INPUT_REQUIRED" || body.Status != "IN_PROGRESS" {
				delete(p.questions, body.StepID)
				p.questionOrder = slices.DeleteFunc(p.questionOrder, func(step string) bool { return step == body.StepID })
				return
			}
			if p.questions == nil {
				p.questions = make(map[string]juniePendingQuestion)
			}
			prior, exists := p.questions[body.StepID]
			if !exists {
				p.questionOrder = append(p.questionOrder, body.StepID)
			}
			inputIDs := p.task.inputIDs
			if exists && prior.question.ID == body.Request.ID {
				inputIDs = prior.inputIDs
			}
			p.questions[body.StepID] = juniePendingQuestion{question: body.Request, inputIDs: inputIDs}
		}
	}
}

func (p *junieControlTaskParser) result() (junieControlTask, bool) {
	if !junieTaskIDPattern.MatchString(p.task.id) {
		return junieControlTask{}, false
	}
	if p.selector.form != nil {
		inputIDs, ok := p.selector.form.matchInputIDs(p.questions, p.questionOrder)
		return junieControlTask{id: p.task.id, inputIDs: inputIDs}, ok
	}
	return junieControlTask{id: p.task.id, inputIDs: p.terminalInputs}, len(p.terminalInputs) > 0
}

// readJunieControlEvents verifies the journal prefix once and extracts both
// steer admissions and the active control task in a single pass.
func readJunieControlEvents(cursor sessionstore.JournalCursor, data []byte, inputs []junieSteerInput, selector junieControlSelector) (map[string]agent.NativeInputReceipt, junieControlTask, bool) {
	appended, ok := cursor.AppendedBytes(data)
	if !ok {
		return nil, junieControlTask{}, false
	}
	admissions := newJunieAdmissionParser(inputs)
	control := junieControlTaskParser{selector: selector}
	for line := range bytes.SplitSeq(appended, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		if !bytes.Contains(line, []byte(`"UserPromptEvent"`)) &&
			!bytes.Contains(line, []byte(`"TaskStartedEvent"`)) &&
			!bytes.Contains(line, []byte(`"TaskState"`)) &&
			!bytes.Contains(line, []byte(`"UserMessagesCommittedToHistory"`)) &&
			!bytes.Contains(line, []byte(`"TerminalBlockUpdatedEvent"`)) &&
			!bytes.Contains(line, []byte(`"AskAsyncRequestUpdatedEvent"`)) {
			continue
		}
		kind, valid := decodeJunieEventKind(line)
		if !valid {
			return nil, junieControlTask{}, false
		}
		switch kind {
		case "UserPromptEvent", "TaskStartedEvent", "TaskState", "UserMessagesCommittedToHistory", "SessionA2uxEvent":
		default:
			continue
		}
		var event junieSteerJournalEvent
		if json.Unmarshal(line, &event) != nil {
			return nil, junieControlTask{}, false
		}
		if kind == "UserPromptEvent" {
			if !admissions.observe(&event) {
				return nil, junieControlTask{}, false
			}
			continue
		}
		control.observe(&event)
	}
	if len(admissions.matched) != len(inputs) {
		return nil, junieControlTask{}, false
	}
	task, matched := control.result()
	return admissions.matched, task, matched
}

func readJunieControlTask(cursor sessionstore.JournalCursor, data []byte, selector junieControlSelector) (junieControlTask, bool) {
	appended, ok := cursor.AppendedBytes(data)
	if !ok {
		return junieControlTask{}, false
	}
	control := junieControlTaskParser{selector: selector}
	for line := range bytes.SplitSeq(appended, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		if !bytes.Contains(line, []byte(`"TaskStartedEvent"`)) &&
			!bytes.Contains(line, []byte(`"TaskState"`)) &&
			!bytes.Contains(line, []byte(`"UserMessagesCommittedToHistory"`)) &&
			!bytes.Contains(line, []byte(`"TerminalBlockUpdatedEvent"`)) &&
			!bytes.Contains(line, []byte(`"AskAsyncRequestUpdatedEvent"`)) {
			continue
		}
		kind, valid := decodeJunieEventKind(line)
		if !valid {
			return junieControlTask{}, false
		}
		switch kind {
		case "TaskStartedEvent", "TaskState", "UserMessagesCommittedToHistory", "SessionA2uxEvent":
		default:
			continue
		}
		var event junieSteerJournalEvent
		if json.Unmarshal(line, &event) != nil {
			return junieControlTask{}, false
		}
		control.observe(&event)
	}
	return control.result()
}

type juniePendingQuestion struct {
	question junieJournalQuestion
	inputIDs []string
}

type junieJournalQuestion struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Question string `json:"question"`
	Options  []struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		Description string `json:"description"`
	} `json:"options"`
	AllowMultiple bool `json:"allowMultiple"`
	IsRequired    bool `json:"isRequired"`
}

type junieACPForm struct {
	Mode    string `json:"mode"`
	Message string `json:"message"`
	Schema  struct {
		Type       string                    `json:"type"`
		Title      string                    `json:"title"`
		Properties map[string]junieFormField `json:"properties"`
		Required   []string                  `json:"required"`
	} `json:"requestedSchema"`
}

type junieFormField struct {
	Type        string            `json:"type"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	OneOf       []junieFormChoice `json:"oneOf"`
	Items       struct {
		AnyOf []junieFormChoice `json:"anyOf"`
	} `json:"items"`
	Meta struct {
		CustomAnswer bool `json:"_askUserQuestionCustomAnswer"`
	} `json:"_meta"`
}

type junieFormChoice struct {
	Value       string `json:"const"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// Junie sends this elicitation without a tool ID. Match all ordered questions,
// including their choices, against the same task's pending native requests.
func (form junieACPForm) matchInputIDs(questions map[string]juniePendingQuestion, order []string) ([]string, bool) {
	if len(questions) == 0 {
		return nil, false
	}
	message := "Please answer the following questions."
	index, used, required := 0, 0, 0
	var inputIDs []string
	for _, step := range order {
		pending, found := questions[step]
		if !found {
			continue
		}
		if index == 0 {
			inputIDs = pending.inputIDs
		} else if !slices.Equal(inputIDs, pending.inputIDs) {
			return nil, false
		}
		question := pending.question
		key := "question_" + strconv.Itoa(index)
		field, found := form.Schema.Properties[key]
		isRequired := slices.Contains(form.Schema.Required, key)
		if !found || field.Title != question.Name || isRequired != question.IsRequired ||
			!field.matchesChoices(question) {
			return nil, false
		}
		if len(questions) == 1 {
			message = question.Question
		} else if field.Description != question.Question {
			return nil, false
		}
		used++
		if custom, found := form.Schema.Properties[key+"_custom"]; found {
			if custom.Type != "string" || !custom.Meta.CustomAnswer {
				return nil, false
			}
			used++
		}
		if isRequired {
			required++
		}
		index++
	}
	ok := len(inputIDs) > 0 && form.Message == message &&
		used == len(form.Schema.Properties) && required == len(form.Schema.Required)
	return inputIDs, ok
}

func (field junieFormField) matchesChoices(question junieJournalQuestion) bool {
	choices := field.OneOf
	if question.AllowMultiple {
		if field.Type != "array" || len(field.OneOf) != 0 {
			return false
		}
		choices = field.Items.AnyOf
	} else if field.Type != "string" || len(field.Items.AnyOf) != 0 {
		return false
	}
	if len(choices) != len(question.Options) {
		return false
	}
	for i, option := range question.Options {
		if choices[i] != (junieFormChoice{Value: option.ID, Title: option.Title, Description: option.Description}) {
			return false
		}
	}
	return true
}
