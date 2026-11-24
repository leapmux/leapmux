package opencode

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// ReadControlRequest reads the control requests that the native control
// registry publishes (openCodeControls in control.go). Each payload is
// `{type, data, request}`: the native event type, the event's own data, and
// the header of the tool call that the request is about.
//
//   - A `form.created` event of the question tool is a question
//     (openCodeQuestionPrompt).
//   - Any other `form.created` event is a form (openCodeElicitationPrompt).
//   - A `permission.asked` event is a permission
//     (readOpenCodePermissionPrompt).
//   - Any other request reads as unrecognized. The registry publishes no
//     other request, and the resolver refuses an answer to one.
func (opencodeProvider) ReadControlRequest(input agent.ControlRequestInput) *leapmuxv1.ControlPrompt {
	if form, ok := readOpenCodePromptForm(input.Payload); ok {
		if form.isQuestion() {
			return openCodeQuestionPrompt(form)
		}
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Elicitation{Elicitation: openCodeElicitationPrompt(form)}}
	}
	if permission, ok := readOpenCodePermissionPrompt(input.Payload); ok {
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: permission}}
	}
	return agent.UnrecognizedControlPrompt()
}

// AnswerControlRequest answers the control requests of the native protocol
// in the shapes that ResolveControlResponse turns into the native reply:
//
//   - A question takes the native form reply (answerOpenCodeQuestions).
//   - A form takes the neutral elicitation envelope. The resolver turns an
//     accept into the form's answer, with each external field confirmed, and
//     a decline or a cancel into the form's cancellation.
//   - A permission selects one native decision (answerOpenCodePermission).
//
// No other request has a prompt that takes an answer (ReadControlRequest).
func (opencodeProvider) AnswerControlRequest(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	switch input.Prompt.GetKind().(type) {
	case *leapmuxv1.ControlPrompt_Question:
		return answerOpenCodeQuestions(input)
	case *leapmuxv1.ControlPrompt_Elicitation:
		return agent.AnswerElicitation(input, nil)
	}
	if _, ok := readOpenCodePermissionPrompt(input.Request.Payload); ok {
		return answerOpenCodePermission(input)
	}
	return agent.ControlAnswer{}, agent.InvalidControlDecision("OpenCode publishes no such request")
}

// answerOpenCodePermission answers a native permission:
//
//   - An option without feedback selects its native decision.
//   - A refusal with feedback is the neutral deny with the feedback as its
//     message. The resolver sends the native reject with that message, so
//     the reason reaches the agent in the reply itself.
//
// agent.PermissionDecisionOf admits feedback on a refusing option only, and
// the native reject is the one refusing option.
func answerOpenCodePermission(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, option, err := agent.PermissionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	if feedback := decision.GetFeedback(); feedback != "" {
		return agent.ControlAnswer{Content: agent.DenyResponse(input.RequestID, feedback)}, nil
	}
	return agent.ControlAnswer{Content: agent.SelectedOptionResult(input.RequestID, option.GetOptionId())}, nil
}

// answerOpenCodeQuestions answers the questions of a question-tool form in
// the native form reply, which the resolver reads back
// (readOpenCodeBrowserFormAnswer):
//
//   - A cancelled decision cancels the form. The native cancellation has no
//     place for text, so the answer drops the decision's feedback.
//   - Otherwise the answer maps the key of each field to its value. A
//     multiple-choice field takes its list of values
//     (agent.QuestionAnswerValues), an empty list included. Any other field
//     takes its one value, and the answer leaves out a field with no value.
func answerOpenCodeQuestions(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, questions, err := agent.QuestionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	if decision.GetCancelled() {
		return agent.ControlAnswer{Content: agent.JSONRPCResult(input.RequestID, map[string]any{
			contracts.OpenCodeNativeAnswerFieldCancel: true,
		})}, nil
	}
	answer := make(map[string]any, len(questions))
	for i, question := range questions {
		values := agent.QuestionAnswerValues(question, agent.QuestionAnswerAt(decision, i))
		switch {
		case question.GetMultiSelect():
			if values == nil {
				values = []string{}
			}
			answer[question.GetId()] = values
		case len(values) > 0:
			answer[question.GetId()] = values[0]
		}
	}
	return agent.ControlAnswer{Content: agent.JSONRPCResult(input.RequestID, map[string]any{
		contracts.OpenCodeNativeAnswerFieldAnswer: answer,
	})}, nil
}

// readOpenCodePermissionPrompt reads a stored `permission.asked` event into a
// permission prompt, and reports false for any other payload:
//
//   - The title is the event's action: the permission that the call needs.
//   - A shell permission states its resources as the command, one on each
//     line.
//   - The arguments are the event's metadata. A permission with no command
//     also states its resources there. They replace a metadata member of the
//     same name, because they are what the user approves.
//   - The reason is the event's message.
//   - The options are the native decisions: allow once, always allow when
//     the event states patterns to save, and reject.
//
// A member of the wrong type reads as absent, and the reader drops a
// resource that is no string.
func readOpenCodePermissionPrompt(payload []byte) (*leapmuxv1.PermissionPrompt, bool) {
	event := providerkit.RawFields(payload)
	if eventType, _ := providerkit.RawString(event["type"]); eventType != contracts.OpenCodeNativeEventPermissionAsked {
		return nil, false
	}
	data, ok := providerkit.RawObject(event["data"])
	if !ok {
		return nil, false
	}
	action := providerkit.ObjectString(data, "action")
	resources := providerkit.RawStrings(data["resources"])
	command := ""
	if action == contracts.OpenCodeNativeToolShell {
		command = strings.Join(resources, "\n")
	}
	arguments := providerkit.ReadOrderedObject(data["metadata"])
	if command == "" {
		arguments.Set("resources", providerkit.JSONText(resources))
	}
	return &leapmuxv1.PermissionPrompt{
		Title:     action,
		Reason:    providerkit.ObjectString(data, "message"),
		Command:   command,
		InputJson: string(arguments.Encode()),
		Options:   openCodePermissionOptions(data["save"]),
	}, true
}

// openCodePermissionOptions are the native decisions of a permission. The
// event states the patterns that an always allow saves, and the resolver
// admits `always` only for an event that states one (openCodeStoredControl).
func openCodePermissionOptions(save json.RawMessage) []*leapmuxv1.PermissionOption {
	options := []*leapmuxv1.PermissionOption{{
		OptionId: contracts.OpenCodeNativePermissionDecisionOnce,
		Kind:     leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE,
		Name:     "Allow",
	}}
	if patterns, ok := providerkit.RawArray(save); ok && len(patterns) > 0 {
		options = append(options, &leapmuxv1.PermissionOption{
			OptionId: contracts.OpenCodeNativePermissionDecisionAlways,
			Kind:     leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS,
			Name:     "Always allow",
			Scope:    leapmuxv1.PermissionScope_PERMISSION_SCOPE_PROJECT,
		})
	}
	return append(options, &leapmuxv1.PermissionOption{
		OptionId: contracts.OpenCodeNativePermissionDecisionReject,
		Kind:     leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE,
		Name:     "Deny",
	})
}

// openCodePromptForm is a native form as a prompt reads it. Each field keeps
// all of its members, so the prompt keeps each field's constraints and
// defaults.
type openCodePromptForm struct {
	title    string
	metadata map[string]json.RawMessage
	fields   []openCodePromptField
}

// openCodePromptField is one field of a native form.
type openCodePromptField struct {
	key       string
	fieldType string
	members   map[string]json.RawMessage
}

// readOpenCodePromptForm reads the form of a stored `form.created` event,
// and reports false for any other payload. A form that states no field, or
// a field that is no object, has no string key or has a type outside
// contracts.OpenCodeNativeFormFieldType*, reads as no form at all: a prompt
// that dropped the field would send an answer that the form does not take.
func readOpenCodePromptForm(payload []byte) (openCodePromptForm, bool) {
	event := providerkit.RawFields(payload)
	if eventType, _ := providerkit.RawString(event["type"]); eventType != contracts.OpenCodeNativeEventFormCreated {
		return openCodePromptForm{}, false
	}
	form, ok := providerkit.RawObject(providerkit.RawFields(event["data"])["form"])
	if !ok {
		return openCodePromptForm{}, false
	}
	elements, ok := providerkit.RawArray(form["fields"])
	if !ok || len(elements) == 0 {
		return openCodePromptForm{}, false
	}
	fields := make([]openCodePromptField, 0, len(elements))
	for _, element := range elements {
		members, ok := providerkit.RawObject(element)
		if !ok {
			return openCodePromptForm{}, false
		}
		key, ok := providerkit.RawString(members["key"])
		if !ok {
			return openCodePromptForm{}, false
		}
		fieldType := providerkit.ObjectString(members, "type")
		switch fieldType {
		case contracts.OpenCodeNativeFormFieldTypeString, contracts.OpenCodeNativeFormFieldTypeNumber,
			contracts.OpenCodeNativeFormFieldTypeInteger, contracts.OpenCodeNativeFormFieldTypeBoolean,
			contracts.OpenCodeNativeFormFieldTypeMultiselect, contracts.OpenCodeNativeFormFieldTypeExternal:
		default:
			return openCodePromptForm{}, false
		}
		fields = append(fields, openCodePromptField{key: key, fieldType: fieldType, members: members})
	}
	return openCodePromptForm{
		title:    providerkit.ObjectString(form, "title"),
		metadata: providerkit.RawFields(form["metadata"]),
		fields:   fields,
	}, true
}

// isQuestion reports whether the form asks the questions of the question
// tool: its metadata states the kind `question`, and each field is a visible
// text or multiple-choice field that no condition hides. Any other form
// needs the shared form, which draws every field type, hides a field and
// reads the conditions.
func (form openCodePromptForm) isQuestion() bool {
	if providerkit.ObjectString(form.metadata, "kind") != "question" {
		return false
	}
	for _, field := range form.fields {
		if field.fieldType != contracts.OpenCodeNativeFormFieldTypeString && field.fieldType != contracts.OpenCodeNativeFormFieldTypeMultiselect {
			return false
		}
		if providerkit.RawBool(field.members["hidden"]) {
			return false
		}
		if _, conditional := field.members["when"]; conditional {
			return false
		}
	}
	return true
}

// openCodeQuestionPrompt reads a question-tool form into a question prompt,
// one question for each field, in order:
//
//   - The id is the field's key, which the answer maps to the value.
//   - The header is the field's title, and the question is its description,
//     else its title.
//   - The options are the field's options that state a string value and a
//     string label.
//   - A multiselect field takes several options, within its minItems and
//     maxItems.
//   - A field that is not required takes an empty answer, and only a field
//     that states custom takes typed text.
func openCodeQuestionPrompt(form openCodePromptForm) *leapmuxv1.ControlPrompt {
	questions := make([]*leapmuxv1.ControlQuestion, 0, len(form.fields))
	for _, field := range form.fields {
		header := providerkit.ObjectString(field.members, "title")
		text := providerkit.ObjectString(field.members, "description")
		if text == "" {
			text = header
		}
		questions = append(questions, &leapmuxv1.ControlQuestion{
			Id:                field.key,
			Header:            header,
			Question:          text,
			Options:           openCodeQuestionOptions(field.members["options"]),
			MultiSelect:       field.fieldType == contracts.OpenCodeNativeFormFieldTypeMultiselect,
			AllowEmpty:        !providerkit.RawBool(field.members["required"]),
			AllowCustomText:   proto.Bool(providerkit.RawBool(field.members["custom"])),
			MinimumSelections: openCodeSelectionCount(field.members["minItems"], math.Ceil),
			MaximumSelections: openCodeSelectionCount(field.members["maxItems"], math.Floor),
		})
	}
	return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{Questions: questions}}}
}

// openCodeQuestionOptions reads the options of one question field. The reader
// drops an option that is no object, or that states no string value or no
// string label, because the answer sends the value.
func openCodeQuestionOptions(raw json.RawMessage) []*leapmuxv1.QuestionOption {
	elements, _ := providerkit.RawArray(raw)
	options := make([]*leapmuxv1.QuestionOption, 0, len(elements))
	for _, element := range elements {
		members, ok := providerkit.RawObject(element)
		if !ok {
			continue
		}
		value, hasValue := providerkit.RawString(members["value"])
		label, hasLabel := providerkit.RawString(members["label"])
		if !hasValue || !hasLabel {
			continue
		}
		options = append(options, &leapmuxv1.QuestionOption{
			Value:       &value,
			Label:       label,
			Description: providerkit.ObjectString(members, "description"),
		})
	}
	return options
}

// openCodeSelectionCount reads a count of selections, or nil for a value that
// is no number. round turns a fraction into the whole count that admits the
// same selections: a minimum rounds up, and a maximum rounds down. The count
// saturates at the limits of int32.
func openCodeSelectionCount(raw json.RawMessage, round func(float64) float64) *int32 {
	value, ok := providerkit.RawFloat64(raw)
	if !ok {
		return nil
	}
	count := int32(max(math.MinInt32, min(math.MaxInt32, round(value))))
	return &count
}

// openCodeElicitationPrompt reads a form that asks no question-tool question
// into an MCP elicitation, which the shared form draws:
//
//   - A form of one external field is a URL elicitation: the user completes
//     it at the field's URL.
//   - Any other form is a form elicitation. Each field is one property of
//     its JSON Schema, in the field's order (openCodeFieldSchema). A required
//     field and an external field are required. A field rule states whether
//     the form hides each field and which earlier answers it needs
//     (openCodeFieldRule).
//
// The form's metadata states the server and the message of an MCP
// elicitation.
func openCodeElicitationPrompt(form openCodePromptForm) *leapmuxv1.ElicitationPrompt {
	prompt := &leapmuxv1.ElicitationPrompt{
		Title:   form.title,
		Message: providerkit.ObjectString(form.metadata, "message"),
		Server:  providerkit.ObjectString(form.metadata, "server"),
	}
	if len(form.fields) == 1 && form.fields[0].fieldType == contracts.OpenCodeNativeFormFieldTypeExternal {
		prompt.Mode = contracts.MCPElicitationModeURL
		prompt.Url = providerkit.ObjectString(form.fields[0].members, "url")
		return prompt
	}
	properties := &providerkit.OrderedObject{}
	required := []string{}
	for _, field := range form.fields {
		prompt.FieldRules = append(prompt.FieldRules, openCodeFieldRule(field))
		properties.Set(field.key, openCodeFieldSchema(field).Encode())
		if providerkit.RawBool(field.members["required"]) || field.fieldType == contracts.OpenCodeNativeFormFieldTypeExternal {
			required = append(required, field.key)
		}
	}
	schema := &providerkit.OrderedObject{}
	schema.Set("type", providerkit.JSONText("object"))
	schema.Set("properties", properties.Encode())
	schema.Set("required", providerkit.JSONText(required))
	prompt.Mode = contracts.MCPElicitationModeForm
	prompt.SchemaJson = string(schema.Encode())
	return prompt
}

// openCodeSchemaMembers are the members of a native field that its JSON
// Schema keeps unchanged, in the order that the schema states them.
var openCodeSchemaMembers = []string{"title", "description", "default", "format", "minimum", "maximum", "minLength", "maxLength", "pattern"}

// openCodeFieldSchema is the JSON Schema of one native field:
//
//   - The field's own constraints and default stay (openCodeSchemaMembers).
//   - A text, number, integer or boolean field keeps its type. A multiselect
//     field is a list of strings within its minItems and maxItems.
//   - An external field is a confirmation that must be true. Its
//     description states the field's description and its URL.
//   - A required text field needs one character at least, and a required
//     multiselect field one selection at least.
//   - A field with options that takes no typed value admits only their
//     values, with their labels as the names of the values. A multiselect
//     field admits them as its items. Any other field becomes a choice of
//     text.
func openCodeFieldSchema(field openCodePromptField) *providerkit.OrderedObject {
	schema := &providerkit.OrderedObject{}
	for _, name := range openCodeSchemaMembers {
		if value, ok := field.members[name]; ok {
			schema.Set(name, value)
		}
	}
	switch field.fieldType {
	case contracts.OpenCodeNativeFormFieldTypeMultiselect:
		schema.Set("type", providerkit.JSONText("array"))
		schema.Set("items", providerkit.JSONText(map[string]string{"type": "string"}))
		for _, name := range []string{"minItems", "maxItems"} {
			if value, ok := field.members[name]; ok {
				schema.Set(name, value)
			}
		}
	case contracts.OpenCodeNativeFormFieldTypeExternal:
		schema.Set("type", providerkit.JSONText("boolean"))
		schema.Set("const", providerkit.JSONText(true))
		var description []string
		for _, text := range []string{providerkit.ObjectString(field.members, "description"), providerkit.ObjectString(field.members, "url")} {
			if text != "" {
				description = append(description, text)
			}
		}
		schema.Set("description", providerkit.JSONText(strings.Join(description, "\n\n")))
	default:
		schema.Set("type", providerkit.JSONText(field.fieldType))
	}
	if providerkit.RawBool(field.members["required"]) {
		switch field.fieldType {
		case contracts.OpenCodeNativeFormFieldTypeString:
			schema.Set("minLength", atLeastOne(schema.Get("minLength")))
		case contracts.OpenCodeNativeFormFieldTypeMultiselect:
			schema.Set("minItems", atLeastOne(schema.Get("minItems")))
		}
	}
	if options, ok := providerkit.RawArray(field.members["options"]); ok && !providerkit.RawBool(field.members["custom"]) {
		choices := openCodeChoices(options)
		if field.fieldType == contracts.OpenCodeNativeFormFieldTypeMultiselect {
			schema.Set("items", choices.Encode())
		} else {
			schema.Merge(choices)
		}
	}
	return schema
}

// openCodeChoices is the schema of a choice among the options of a field:
// text whose enum lists the value of each option, and whose enumNames list
// each label. The reader skips an option that is no object. An option with
// no value or no label states null in its place, so the two lists stay in
// step.
func openCodeChoices(options []json.RawMessage) *providerkit.OrderedObject {
	values := []json.RawMessage{}
	labels := []json.RawMessage{}
	for _, raw := range options {
		option, ok := providerkit.RawObject(raw)
		if !ok {
			continue
		}
		values = append(values, rawOrNull(option["value"]))
		labels = append(labels, rawOrNull(option["label"]))
	}
	choices := &providerkit.OrderedObject{}
	choices.Set("type", providerkit.JSONText("string"))
	choices.Set("enum", providerkit.JSONText(values))
	choices.Set("enumNames", providerkit.JSONText(labels))
	return choices
}

// rawOrNull returns raw, or JSON null for an absent member.
func rawOrNull(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage("null")
	}
	return raw
}

// atLeastOne is the bound of a required field: the stated number when it is
// one or more, else one. A value that is no number states no bound.
func atLeastOne(raw json.RawMessage) json.RawMessage {
	if value, ok := providerkit.RawFloat64(raw); ok && value > 1 {
		return providerkit.JSONText(value)
	}
	return providerkit.JSONText(1)
}

// openCodeFieldRule is the rule of one native field: whether the form hides
// it, and the conditions of its `when` list. A condition compares the answer
// to an earlier field, by its key, with `eq` or `neq`, to a string, a number
// or a boolean. The reader drops any other condition.
func openCodeFieldRule(field openCodePromptField) *leapmuxv1.ElicitationFieldRule {
	rule := &leapmuxv1.ElicitationFieldRule{Key: field.key, Hidden: providerkit.RawBool(field.members["hidden"])}
	conditions, _ := providerkit.RawArray(field.members["when"])
	for _, raw := range conditions {
		condition, ok := providerkit.RawObject(raw)
		if !ok {
			continue
		}
		source, ok := providerkit.RawString(condition["key"])
		if !ok {
			continue
		}
		var comparison leapmuxv1.ElicitationFieldComparison
		switch providerkit.ObjectString(condition, "op") {
		case "eq":
			comparison = leapmuxv1.ElicitationFieldComparison_ELICITATION_FIELD_COMPARISON_EQUAL
		case "neq":
			comparison = leapmuxv1.ElicitationFieldComparison_ELICITATION_FIELD_COMPARISON_NOT_EQUAL
		default:
			continue
		}
		value, ok := openCodeConditionValue(condition["value"])
		if !ok {
			continue
		}
		rule.Conditions = append(rule.Conditions, &leapmuxv1.ElicitationFieldCondition{Field: source, Comparison: comparison, ValueJson: string(value)})
	}
	return rule
}

// openCodeConditionValue is the value of a condition as JSON text, and false
// for a value that is no string, number or boolean.
func openCodeConditionValue(raw json.RawMessage) (json.RawMessage, bool) {
	if text, ok := providerkit.RawString(raw); ok {
		return providerkit.JSONText(text), true
	}
	if number, ok := providerkit.RawFloat64(raw); ok {
		return providerkit.JSONText(number), true
	}
	switch string(bytes.TrimSpace(raw)) {
	case "true", "false":
		return bytes.TrimSpace(raw), true
	}
	return nil, false
}
