package junie

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"

	"github.com/google/uuid"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

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
	admissions, ok := readJunieSteerAdmissions(cursor, data, inputs)
	if !ok {
		return "", nil
	}
	task, ok := readJunieControlTask(cursor, data, selector)
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
		return junieControlSelector{toolCallID: id}, validJunieControlStep(id)
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

func validJunieControlStep(stepID string) bool {
	parsed, err := uuid.Parse(stepID)
	return err == nil && parsed.String() == stepID
}

type junieControlTask struct {
	id       string
	inputIDs []string
}

func readJunieControlTask(cursor junieJournalCursor, data []byte, selector junieControlSelector) (junieControlTask, bool) {
	appended, ok := cursor.appended(data)
	if !ok {
		return junieControlTask{}, false
	}
	var task junieControlTask
	var terminalInputs []string
	var questions map[string]juniePendingQuestion
	var questionOrder []string
	for line := range bytes.SplitSeq(appended, []byte{'\n'}) {
		if !bytes.Contains(line, []byte(`"TaskStartedEvent"`)) &&
			!bytes.Contains(line, []byte(`"TaskState"`)) &&
			!bytes.Contains(line, []byte(`"UserMessagesCommittedToHistory"`)) &&
			!bytes.Contains(line, []byte(`"TerminalBlockUpdatedEvent"`)) &&
			!bytes.Contains(line, []byte(`"AskAsyncRequestUpdatedEvent"`)) {
			continue
		}
		var event junieSteerJournalEvent
		if json.Unmarshal(line, &event) != nil {
			return junieControlTask{}, false
		}
		switch event.Kind {
		case "TaskStartedEvent", "TaskState":
			task = junieControlTask{}
			terminalInputs, questions, questionOrder = nil, nil, nil
			if event.Kind == "TaskStartedEvent" {
				task.id = event.TaskID
			}
		case "UserMessagesCommittedToHistory":
			task.inputIDs = event.CommittedInputIDs
		case "SessionA2uxEvent":
			body := event.Event.Payload
			if event.TaskID != task.id || body.Agent.Kind != "MainAgent" || body.Agent.ID != "main" {
				continue
			}
			switch body.Kind {
			case "TerminalBlockUpdatedEvent":
				if selector.toolCallID != "" && body.StepID == selector.toolCallID {
					pending := body.Status == "IN_PROGRESS" &&
						(event.Event.State == "IN_PROGRESS" || event.Event.State == "INPUT_REQUIRED")
					if !pending {
						terminalInputs = nil
					} else if len(terminalInputs) == 0 {
						terminalInputs = task.inputIDs
					}
				}
			case "AskAsyncRequestUpdatedEvent":
				if selector.form == nil || !validJunieControlStep(body.StepID) || !validJunieControlStep(body.Request.ID) {
					continue
				}
				if event.Event.State != "INPUT_REQUIRED" || body.Status != "IN_PROGRESS" {
					delete(questions, body.StepID)
					questionOrder = slices.DeleteFunc(questionOrder, func(step string) bool { return step == body.StepID })
					continue
				}
				if questions == nil {
					questions = make(map[string]juniePendingQuestion)
				}
				prior, exists := questions[body.StepID]
				if !exists {
					questionOrder = append(questionOrder, body.StepID)
				}
				inputIDs := task.inputIDs
				if exists && prior.question.ID == body.Request.ID {
					inputIDs = prior.inputIDs
				}
				questions[body.StepID] = juniePendingQuestion{question: body.Request, inputIDs: inputIDs}
			}
		}
	}
	if !junieTaskIDPattern.MatchString(task.id) {
		return junieControlTask{}, false
	}
	if selector.form != nil {
		inputIDs, ok := selector.form.matchInputIDs(questions, questionOrder)
		return junieControlTask{id: task.id, inputIDs: inputIDs}, ok
	}
	return junieControlTask{id: task.id, inputIDs: terminalInputs}, len(terminalInputs) > 0
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
