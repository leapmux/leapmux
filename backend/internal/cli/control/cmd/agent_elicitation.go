package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
)

// fieldRuleView states how a form treats one field of its schema.
type fieldRuleView struct {
	Key string `json:"key"`
	// Hidden reports a field that the form never shows. Its default is sent
	// while the field is active.
	Hidden bool `json:"hidden,omitempty"`
	// Conditions must all hold, for the answers to earlier fields, for the
	// form to ask the field. An inactive field is neither sent nor required.
	Conditions []fieldConditionView `json:"conditions,omitempty"`
}

type fieldConditionView struct {
	Field string `json:"field"`
	// Comparison is "equal" or "not_equal". A list answer is equal when it
	// holds the value.
	Comparison string `json:"comparison"`
	// Value is a string, a number or a boolean.
	Value json.RawMessage `json:"value"`
}

// parseConditionScalar decodes raw as a JSON scalar (string, number, or
// boolean). Any other JSON value or malformed input returns (nil, false).
func parseConditionScalar(raw []byte) (any, bool) {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil, false
	}
	switch value.(type) {
	case string, float64, bool:
		return value, true
	default:
		return nil, false
	}
}

// fieldRuleViewsOf projects the field rules of a form. A condition of no
// known comparison, or whose value is not the JSON of a string, a number or
// a boolean, states nothing, so it is dropped, as the browser drops it.
func fieldRuleViewsOf(rules []*leapmuxv1.ElicitationFieldRule) []fieldRuleView {
	views := make([]fieldRuleView, 0, len(rules))
	for _, rule := range rules {
		view := fieldRuleView{Key: rule.GetKey(), Hidden: rule.GetHidden()}
		for _, condition := range rule.GetConditions() {
			comparison := enumToken(condition.GetComparison())
			if comparison == "" {
				continue
			}
			value, ok := parseConditionScalar([]byte(condition.GetValueJson()))
			if !ok {
				continue
			}
			compact, _ := json.Marshal(value)
			view.Conditions = append(view.Conditions, fieldConditionView{Field: condition.GetField(), Comparison: comparison, Value: compact})
		}
		views = append(views, view)
	}
	return views
}

// elicitationAccept accepts an MCP elicitation. A form sends its content: the
// defaults of the schema, with the fields over them, and every required
// field must have a value. A form in any other mode, such as a URL to visit,
// sends no content, as the browser does.
func elicitationAccept(e *leapmuxv1.ElicitationPrompt, assignments []string, acceptChoice string) (*leapmuxv1.ControlDecision, error) {
	decision := &leapmuxv1.ElicitationDecision{Action: leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, AcceptChoiceKey: acceptChoice}
	if e.GetMode() != "" && e.GetMode() != elicitationModeForm {
		if len(assignments) > 0 {
			return nil, cli.Usagef("an MCP elicitation in %s mode has no form to fill", e.GetMode())
		}
		return decisionOf(decision), nil
	}
	content, err := elicitationContent(e.GetSchemaJson(), fieldRuleViewsOf(e.GetFieldRules()), assignments)
	if err != nil {
		return nil, err
	}
	decision.ContentJson = content
	return decisionOf(decision), nil
}

// elicitationModeForm is the MCP elicitation mode of a form. An elicitation
// that states no mode is a form, as MCP defines.
const elicitationModeForm = "form"

// elicitationField is one property of an MCP elicitation schema.
type elicitationField struct {
	Name        string
	Type        string
	Required    bool
	Description string
	// Enum lists the values that the field accepts, or nil for any value.
	Enum []string
	// Default is the schema's default value, or nil.
	Default json.RawMessage
	// ItemType is the type of each value of an array field.
	ItemType string
	// Hidden reports a field that the form never shows. Its default is still
	// sent while the field is active.
	Hidden bool
	// Conditions must all hold for the form to ask the field.
	Conditions []fieldConditionView
}

// elicitationFields lists the properties of schema in the order that the
// schema states them, each with its rule. A schema that does not parse lists
// none.
func elicitationFields(schema json.RawMessage, rules []fieldRuleView) []elicitationField {
	ruleOf := make(map[string]fieldRuleView, len(rules))
	for _, rule := range rules {
		ruleOf[rule.Key] = rule
	}
	var parsed struct {
		Properties json.RawMessage `json:"properties"`
		Required   []string        `json:"required"`
	}
	if len(schema) == 0 || json.Unmarshal(schema, &parsed) != nil {
		return nil
	}
	names, properties := orderedObject(parsed.Properties)
	fields := make([]elicitationField, 0, len(names))
	for _, name := range names {
		var property struct {
			Type        string            `json:"type"`
			Title       string            `json:"title"`
			Description string            `json:"description"`
			Enum        []json.RawMessage `json:"enum"`
			OneOf       []schemaConst     `json:"oneOf"`
			Default     json.RawMessage   `json:"default"`
			Items       struct {
				Type  string            `json:"type"`
				Enum  []json.RawMessage `json:"enum"`
				AnyOf []schemaConst     `json:"anyOf"`
			} `json:"items"`
		}
		_ = json.Unmarshal(properties[name], &property)
		description := property.Description
		if property.Title != "" && description == "" {
			description = property.Title
		}
		var defaultVal json.RawMessage
		if hasNonNullDefault(property.Default) {
			defaultVal = property.Default
		}
		field := elicitationField{
			Name:        name,
			Type:        property.Type,
			Required:    slices.Contains(parsed.Required, name),
			Description: description,
			Enum:        enumValues(property.Enum, property.OneOf),
			Default:     defaultVal,
			ItemType:    property.Items.Type,
			Hidden:      ruleOf[name].Hidden,
			Conditions:  ruleOf[name].Conditions,
		}
		if property.Type == "array" {
			field.Enum = enumValues(property.Items.Enum, property.Items.AnyOf)
		}
		fields = append(fields, field)
	}
	return fields
}

// hasNonNullDefault reports whether raw carries a non-null JSON Schema default.
func hasNonNullDefault(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

// schemaConst is one titled value of a JSON Schema oneOf or anyOf.
type schemaConst struct {
	Const json.RawMessage `json:"const"`
}

// enumValues lists the values of an enum, or of a oneOf of constants.
func enumValues(enum []json.RawMessage, consts []schemaConst) []string {
	var out []string
	for _, v := range enum {
		if token, ok := enumTokenString(v); ok {
			out = append(out, token)
		}
	}
	for _, c := range consts {
		if token, ok := enumTokenString(c.Const); ok {
			out = append(out, token)
		}
	}
	return out
}

// enumTokenString formats one JSON Schema enum or const entry as a string,
// using json.Number so large integers keep their exact spelling (e.g.
// "1000000" rather than "1e+06").
func enumTokenString(raw json.RawMessage) (string, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", false
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || v == nil {
		return "", false
	}
	return fmt.Sprint(v), true
}

// orderedObject returns the keys of a JSON object in their order, with each
// value. Anything other than an object gives no keys.
func orderedObject(raw json.RawMessage) ([]string, map[string]json.RawMessage) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, nil
	}
	values := map[string]json.RawMessage{}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, nil
		}
		key, ok := tok.(string)
		if !ok {
			return nil, nil
		}
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil, nil
		}
		if _, seen := values[key]; !seen {
			keys = append(keys, key)
		}
		values[key] = val
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, nil
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, nil
	}
	return keys, values
}

// elicitationContent builds the content of a form, as the browser's form
// reads it. It reads the fields in the order of the schema. A field is
// active when every condition of its rule holds for the content of the
// earlier active fields, and an inactive field is neither sent nor required.
// An active field sends its assignment, or else its default. Each
// assignment is "<name>=<value>", converted to the field's type, and an
// array field takes one assignment for each of its values.
func elicitationContent(schemaJSON string, rules []fieldRuleView, assignments []string) (string, error) {
	fields := elicitationFields(json.RawMessage(schemaJSON), rules)
	byName := map[string]elicitationField{}
	for _, field := range fields {
		byName[field.Name] = field
	}
	values := map[string]any{}
	for _, assignment := range assignments {
		name, value, found := strings.Cut(assignment, "=")
		if !found || name == "" {
			return "", cli.Usagef("--field %q is not <name>=<value>", assignment)
		}
		field, ok := byName[name]
		if ok && field.Hidden {
			return "", cli.Usagef("the form keeps field %q hidden and sends its default", name)
		}
		if !ok {
			var names []string
			for _, f := range fields {
				if !f.Hidden {
					names = append(names, f.Name)
				}
			}
			return "", cli.Usagef("the form has no field %q", name).WithHint("its fields are: %s", strings.Join(names, ", "))
		}
		converted, err := fieldValue(field, value)
		if err != nil {
			return "", err
		}
		if field.Type == "array" {
			list, _ := values[name].([]any)
			values[name] = append(list, converted)
			continue
		}
		if _, twice := values[name]; twice {
			return "", cli.Usagef("--field gives %q twice", name)
		}
		values[name] = converted
	}
	content := map[string]any{}
	var missing []string
	for _, field := range fields {
		if !conditionsHold(field.Conditions, content) {
			if _, given := values[field.Name]; given {
				return "", cli.Usagef("the form asks field %q only %s", field.Name, conditionText(field.Conditions))
			}
			continue
		}
		switch value, given := values[field.Name]; {
		case given:
			content[field.Name] = value
		case hasNonNullDefault(field.Default):
			content[field.Name] = field.Default
		case field.Required:
			missing = append(missing, field.Name)
		}
	}
	if len(missing) > 0 {
		return "", cli.Usagef("the form requires %s", strings.Join(missing, ", ")).
			WithHint(`fill each with "leapmux agent answer <agent> <request> --field <name>=<value>"`)
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		return "", cli.Wrap(cli.CodeInternal, err, "encode the form")
	}
	return string(encoded), nil
}

// conditionsHold reports whether every condition holds for content. A
// condition on a field with no value holds for no comparison, as in the
// browser.
func conditionsHold(conditions []fieldConditionView, content map[string]any) bool {
	equal := enumToken(leapmuxv1.ElicitationFieldComparison_ELICITATION_FIELD_COMPARISON_EQUAL)
	for _, condition := range conditions {
		value, ok := content[condition.Field]
		if !ok || answerMatches(value, condition.Value) != (condition.Comparison == equal) {
			return false
		}
	}
	return true
}

// answerMatches reports whether a field's value is want, or holds want for
// a list. Both are compared as JSON values, so the number 1 of an integer
// field matches the condition value 1.
func answerMatches(value any, want json.RawMessage) bool {
	target, ok := parseConditionScalar(want)
	if !ok {
		return false
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return false
	}
	var got any
	if json.Unmarshal(encoded, &got) != nil {
		return false
	}
	if list, ok := got.([]any); ok {
		return slices.Contains(list, target)
	}
	return got == target
}

// conditionText states conditions for a reader, such as
// `when env = "prod" and replicas != 0`.
func conditionText(conditions []fieldConditionView) string {
	equal := enumToken(leapmuxv1.ElicitationFieldComparison_ELICITATION_FIELD_COMPARISON_EQUAL)
	parts := make([]string, 0, len(conditions))
	for _, condition := range conditions {
		operator := "!="
		if condition.Comparison == equal {
			operator = "="
		}
		parts = append(parts, fmt.Sprintf("%s %s %s", condition.Field, operator, condition.Value))
	}
	return "when " + strings.Join(parts, " and ")
}

// isJSONNumberSyntax reports whether s is a valid RFC 8259 JSON number with no
// leading or trailing whitespace.
func isJSONNumberSyntax(s string) bool {
	if s == "" || strings.TrimSpace(s) != s {
		return false
	}
	if s[0] != '-' && (s[0] < '0' || s[0] > '9') {
		return false
	}
	return json.Valid([]byte(s))
}

// fieldValue converts value to the type of field.
func fieldValue(field elicitationField, value string) (any, error) {
	if len(field.Enum) > 0 && !slices.Contains(field.Enum, value) {
		return nil, cli.Usagef("field %q takes one of %s, not %q", field.Name, strings.Join(field.Enum, ", "), value)
	}
	kind := field.Type
	if kind == "array" {
		kind = field.ItemType
	}
	switch kind {
	case "number":
		if !isJSONNumberSyntax(value) {
			return nil, cli.Usagef("field %q takes a number, not %q", field.Name, value)
		}
		if f, err := strconv.ParseFloat(value, 64); err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, cli.Usagef("field %q takes a number, not %q", field.Name, value)
		}
		return json.Number(value), nil
	case "integer":
		if !isJSONNumberSyntax(value) {
			return nil, cli.Usagef("field %q takes an integer, not %q", field.Name, value)
		}
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			return nil, cli.Usagef("field %q takes an integer, not %q", field.Name, value)
		}
		return json.Number(value), nil
	case "boolean":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return nil, cli.Usagef("field %q takes true or false, not %q", field.Name, value)
		}
		return b, nil
	default:
		return value, nil
	}
}
