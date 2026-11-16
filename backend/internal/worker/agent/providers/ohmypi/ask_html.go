package ohmypi

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/net/html"

	"github.com/leapmux/leapmux/generated/contracts"
)

// Text forms keep native dialogs because those dialogs carry drafts and validation
// prompts. This bridge interprets only the choice fields it can answer as a group.
const maxAskFormDepth = 128

type askHTMLOption struct {
	label, value, markup string
	checked, disabled    bool
}

type askHTMLField struct {
	name, label, caption, section string
	options                       []askHTMLOption
	multi, menu, required         bool
}

type askHTMLReader struct {
	fields           []*askHTMLField
	groups           map[string]*askHTMLField
	labels           map[string]*html.Node
	controlCounts    map[*html.Node]int
	section, caption string
	inline           strings.Builder
	fieldsetDepth    int
	err              error
}

func askHTMLAttr(node *html.Node, key string) (string, bool) {
	for _, attr := range node.Attr {
		if attr.Key == key {
			return attr.Val, true
		}
	}
	return "", false
}

func askHTMLValue(node *html.Node, key string) string {
	value, _ := askHTMLAttr(node, key)
	return value
}

func askHTMLControl(node *html.Node) bool {
	return node.Type == html.ElementNode && (node.Data == "input" || node.Data == "select" || node.Data == "textarea")
}

func askHTMLSkipped(tag string) bool {
	switch tag {
	case "button", "head", "iframe", "link", "meta", "noscript", "script", "style", "template", "title":
		return true
	}
	return false
}

func askHTMLBlock(tag string) bool {
	switch tag {
	case "address", "article", "aside", "blockquote", "dd", "details", "div", "dl", "dt", "figure", "figcaption", "footer", "header", "li", "main", "nav", "p", "section", "h1", "h2", "h3", "h4", "h5", "h6", "summary":
		return true
	}
	return false
}

// Native question and menu strings collapse JavaScript whitespace.
func askOneLine(value string) string {
	return strings.Join(strings.FieldsFunc(value, func(r rune) bool { return r == '\uFEFF' || r != '\u0085' && unicode.IsSpace(r) }), " ")
}

// askHTMLLabelText interprets the label string needed to match the native RPC
// dialog. The unchanged markup, rather than presentation derived here, reaches
// the browser for descriptions and previews.
func askHTMLLabelText(node *html.Node) string {
	var text strings.Builder
	var visit func(*html.Node)
	visit = func(current *html.Node) {
		if current.Type == html.TextNode {
			text.WriteString(current.Data)
			return
		}
		if current.Type != html.ElementNode {
			return
		}
		if askHTMLSkipped(current.Data) || askHTMLControl(current) || current.Data == "svg" {
			return
		}
		if current.Data == "br" {
			text.WriteByte('\n')
			return
		}
		if current.Data == "img" {
			text.WriteString(askHTMLValue(current, "alt"))
			return
		}
		if askHTMLBlock(current.Data) {
			text.WriteByte(' ')
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		visit(child)
	}
	return askOneLine(text.String())
}

func askHTMLTextContent(node *html.Node) string {
	var text strings.Builder
	var visit func(*html.Node)
	visit = func(current *html.Node) {
		if current.Type == html.TextNode {
			text.WriteString(current.Data)
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(node)
	return text.String()
}

func readAskChoiceQuestions(markup string) ([]contracts.OhMyPiAskQuestion, error) {
	document, err := html.Parse(strings.NewReader(markup))
	if err != nil {
		return nil, err
	}
	root := document
	var nodes []*html.Node
	var collect func(*html.Node, int) error
	collect = func(node *html.Node, depth int) error {
		if depth > maxAskFormDepth {
			return fmt.Errorf("the ask form exceeds the supported grouping depth")
		}
		nodes = append(nodes, node)
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if err := collect(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := collect(document, 0); err != nil {
		return nil, err
	}
	for _, node := range nodes {
		if node.Type == html.ElementNode && node.Data == "form" {
			root = node
			break
		}
	}
	nodes = nil
	if err := collect(root, 0); err != nil {
		return nil, err
	}
	reader := askHTMLReader{groups: make(map[string]*askHTMLField), labels: make(map[string]*html.Node), controlCounts: make(map[*html.Node]int)}
	ids := make(map[string]struct{})
	for _, node := range nodes {
		if !askHTMLControl(node) {
			continue
		}
		if id := askHTMLValue(node, "id"); id != "" {
			ids[id] = struct{}{}
		}
		for parent := node.Parent; parent != nil; parent = parent.Parent {
			reader.controlCounts[parent]++
			if parent == root {
				break
			}
		}
	}
	for _, node := range nodes {
		if node.Type != html.ElementNode || node.Data != "label" {
			continue
		}
		target := askHTMLValue(node, "for")
		if _, exists := ids[target]; exists && reader.labels[target] == nil {
			reader.labels[target] = node
		}
	}
	reader.children(root)
	reader.flush()
	if reader.err != nil {
		return nil, reader.err
	}
	var fields []contracts.OhMyPiAskQuestion
	seen := make(map[string]struct{})
	for _, field := range reader.fields {
		if field.name == "" {
			continue
		}
		if _, duplicate := seen[field.name]; duplicate {
			return nil, fmt.Errorf("the ask form repeats a field name across independent controls")
		}
		seen[field.name] = struct{}{}
		converted, err := field.nativeQuestion()
		if err != nil {
			return nil, err
		}
		fields = append(fields, converted)
	}
	return fields, nil
}

func (reader *askHTMLReader) flush() {
	if text := askOneLine(reader.inline.String()); text != "" {
		reader.caption = text
	}
	reader.inline.Reset()
}

func (reader *askHTMLReader) children(node *html.Node) {
	for child := node.FirstChild; child != nil && reader.err == nil; child = child.NextSibling {
		reader.walk(child)
	}
}

func (reader *askHTMLReader) walk(node *html.Node) {
	if node.Type == html.TextNode {
		reader.inline.WriteString(node.Data)
		return
	}
	if node.Type != html.ElementNode || askHTMLSkipped(node.Data) {
		return
	}
	switch node.Data {
	case "input", "select", "textarea":
		reader.control(node, nil)
		return
	case "label":
		if target := askHTMLValue(node, "for"); target != "" && reader.labels[target] == node {
			return
		}
		if reader.controlCounts[node] > 0 {
			var visit func(*html.Node)
			visit = func(current *html.Node) {
				if reader.err != nil {
					return
				}
				if askHTMLControl(current) {
					reader.control(current, node)
				}
				for child := current.FirstChild; child != nil; child = child.NextSibling {
					visit(child)
				}
			}
			visit(node)
			return
		}
	case "fieldset":
		reader.flush()
		var legend *html.Node
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == html.ElementNode && child.Data == "legend" {
				legend = child
				break
			}
		}
		if reader.fieldsetDepth == 0 {
			reader.section = ""
			reader.caption = ""
			if legend != nil {
				reader.section = askHTMLLabelText(legend)
			}
		} else if legend != nil {
			reader.caption = askHTMLLabelText(legend)
		}
		reader.fieldsetDepth++
		for child := node.FirstChild; child != nil && reader.err == nil; child = child.NextSibling {
			if child != legend {
				reader.walk(child)
			}
		}
		reader.fieldsetDepth--
		reader.flush()
		if reader.fieldsetDepth == 0 {
			reader.section = ""
			reader.caption = ""
		}
		return
	case "table":
		if reader.controlCounts[node] > 0 {
			reader.err = fmt.Errorf("the ask form uses native table control labels")
			return
		}
		reader.flush()
		reader.caption = ""
		return
	case "pre", "svg":
		reader.flush()
		reader.caption = ""
		return
	case "img":
		if askHTMLValue(node, "src") != "" {
			reader.flush()
			reader.caption = ""
		}
		return
	case "br":
		reader.inline.WriteByte('\n')
		return
	case "hr":
		reader.flush()
		return
	case "legend", "ul", "ol":
		reader.flush()
		reader.children(node)
		reader.flush()
		return
	}
	if askHTMLBlock(node.Data) {
		reader.flush()
		reader.children(node)
		reader.flush()
		return
	}
	reader.children(node)
}

func (reader *askHTMLReader) control(node, labelNode *html.Node) {
	reader.flush()
	label, markup := "", ""
	if labelNode != nil {
		label = askHTMLLabelText(labelNode)
	}
	if label == "" {
		labelNode = reader.labels[askHTMLValue(node, "id")]
		if labelNode != nil {
			label = askHTMLLabelText(labelNode)
		} else if ariaLabel, present := askHTMLAttr(node, "aria-label"); present {
			label = askOneLine(ariaLabel)
		} else {
			label = askOneLine(askHTMLValue(node, "title"))
		}
	}
	if labelNode != nil {
		var encoded bytes.Buffer
		if err := html.Render(&encoded, labelNode); err != nil {
			reader.err = err
			return
		}
		markup = encoded.String()
	}
	name := askHTMLValue(node, "name")
	_, required := askHTMLAttr(node, "required")
	if node.Data == "select" {
		_, multi := askHTMLAttr(node, "multiple")
		field := &askHTMLField{name: name, label: label, caption: reader.caption, section: reader.section, multi: multi, menu: true, required: required}
		var visit func(*html.Node)
		visit = func(current *html.Node) {
			if current.Type == html.ElementNode && current.Data == "option" {
				text := askOneLine(askHTMLTextContent(current))
				value, present := askHTMLAttr(current, "value")
				if !present {
					value = text
				}
				_, checked := askHTMLAttr(current, "selected")
				_, disabled := askHTMLAttr(current, "disabled")
				field.options = append(field.options, askHTMLOption{label: text, value: value, checked: checked, disabled: disabled})
				return
			}
			for child := current.FirstChild; child != nil; child = child.NextSibling {
				visit(child)
			}
		}
		visit(node)
		if !multi {
			anyChecked := false
			for _, option := range field.options {
				anyChecked = anyChecked || option.checked
			}
			if !anyChecked {
				for index := range field.options {
					if !field.options[index].disabled {
						field.options[index].checked = true
						break
					}
				}
			}
		}
		reader.fields = append(reader.fields, field)
		reader.caption = ""
		return
	}
	typeName := strings.ToLower(askHTMLValue(node, "type"))
	switch typeName {
	case "button", "file", "image", "reset", "submit", "hidden":
		return
	}
	if node.Data == "textarea" || typeName != "radio" && typeName != "checkbox" {
		reader.err = fmt.Errorf("the ask form needs native text input or editor dialogs")
		return
	}
	value, present := askHTMLAttr(node, "value")
	if !present {
		value = "on"
	}
	if label == "" {
		if present {
			label = value
		} else {
			label = name
		}
	}
	_, checked := askHTMLAttr(node, "checked")
	_, disabled := askHTMLAttr(node, "disabled")
	option := askHTMLOption{label: label, value: value, markup: markup, checked: checked, disabled: disabled}
	key := typeName + "\x00" + name
	if field := reader.groups[key]; field != nil {
		field.options = append(field.options, option)
		field.required = field.required || required
		return
	}
	field := &askHTMLField{name: name, caption: reader.caption, section: reader.section, options: []askHTMLOption{option}, multi: typeName == "checkbox", required: required}
	reader.fields = append(reader.fields, field)
	reader.groups[key] = field
	reader.caption = ""
}

var askOtherLabelPrefix = regexp.MustCompile(`(?i)^other\b`)

func (field *askHTMLField) nativeQuestion() (contracts.OhMyPiAskQuestion, error) {
	question := field.label
	if question == "" {
		question = field.caption
	}
	if question == "" {
		question = field.section
	}
	if question == "" && len(field.options) == 1 {
		question = field.options[0].label
	}
	if question == "" {
		question = field.name
	}
	question = askOneLine(question)
	if question == "" {
		return contracts.OhMyPiAskQuestion{}, fmt.Errorf("the ask form has no usable native question title")
	}
	converted := contracts.OhMyPiAskQuestion{ID: field.name, Question: question, Multi: field.multi, AllowEmpty: field.multi && !field.required}
	if field.section != "" && field.section != question {
		converted.Header = field.section
	}
	other := !field.menu && len(field.options) >= 2
	for _, option := range field.options {
		if askOtherLabelPrefix.MatchString(askOneLine(option.label)) {
			other = false
		}
	}
	taken := make(map[string]struct{})
	for index, option := range field.options {
		if option.checked {
			converted.InitialSelections = append(converted.InitialSelections, strconv.Itoa(index))
		}
		if option.disabled {
			continue
		}
		label := askOneLine(option.label)
		if label == "" {
			label = option.value
		}
		if askOneLine(label) == "" {
			return contracts.OhMyPiAskQuestion{}, fmt.Errorf("the ask form has an empty native choice label")
		}
		base := label
		for suffix := 2; ; suffix++ {
			if _, exists := taken[label]; !exists {
				break
			}
			label = base + " (" + strconv.Itoa(suffix) + ")"
		}
		taken[label] = struct{}{}
		value := strconv.Itoa(index)
		converted.Options = append(converted.Options, contracts.OhMyPiAskOption{Value: value, Label: label, Markup: option.markup})
	}
	converted.AllowCustomText = other
	return converted, nil
}
