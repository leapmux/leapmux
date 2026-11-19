package ohmypi

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"

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
	name, label           string
	options               []askHTMLOption
	multi, menu, required bool
}

type askHTMLReader struct {
	sections      []*askHTMLSection
	section       *askHTMLSection
	groups        map[string]*askHTMLGroup
	labels        map[string]*html.Node
	controlCounts map[*html.Node]int
	inline        []askHTMLSpan
	fieldsetDepth int
	metadataLeft  int
	err           error
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
	return strings.Join(strings.FieldsFunc(value, askHTMLSpace), " ")
}

// askHTMLLabelText interprets the label string needed to match the native RPC
// dialog. The unchanged markup, rather than presentation derived here, reaches
// the control prompt reader (readChoiceMarkup) for descriptions and previews.
func askHTMLLabelText(node *html.Node) string {
	return askOneLine(askHTMLSpansText(node))
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
	if len(markup) > contracts.MaxMessageSize {
		return nil, fmt.Errorf("the ask form exceeds the Worker message limit")
	}
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
	reader := askHTMLReader{groups: make(map[string]*askHTMLGroup), labels: make(map[string]*html.Node), controlCounts: make(map[*html.Node]int), metadataLeft: contracts.MaxMessageSize}
	reader.startSection("")
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
	reader.children(root, "")
	reader.flush()
	if reader.err != nil {
		return nil, reader.err
	}
	return reader.questions()
}

func (reader *askHTMLReader) flush() {
	if text := askHTMLSpanText(reader.inline); text != "" {
		reader.section.blocks = append(reader.section.blocks, &askHTMLFormBlock{text: text, isText: true})
	}
	reader.inline = nil
}

func (reader *askHTMLReader) children(node *html.Node, style string) {
	for child := node.FirstChild; child != nil && reader.err == nil; child = child.NextSibling {
		reader.walk(child, style)
	}
}

func (reader *askHTMLReader) walk(node *html.Node, style string) {
	if node.Type == html.TextNode {
		appendAskHTMLSpan(&reader.inline, node.Data, style)
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
			title := ""
			if legend != nil {
				title = askHTMLSpansText(legend)
			}
			reader.startSection(title)
		} else if legend != nil {
			reader.walk(legend, "")
		}
		reader.fieldsetDepth++
		for child := node.FirstChild; child != nil && reader.err == nil; child = child.NextSibling {
			if child != legend {
				reader.walk(child, "")
			}
		}
		reader.flush()
		reader.fieldsetDepth--
		if reader.fieldsetDepth == 0 {
			reader.startSection("")
		}
		return
	case "table":
		if reader.controlCounts[node] > 0 {
			reader.err = fmt.Errorf("the ask form uses native table control labels")
			return
		}
		reader.flush()
		if text, exists := askHTMLTableText(node); exists {
			reader.section.blocks = append(reader.section.blocks, &askHTMLFormBlock{text: text})
		}
		return
	case "pre":
		reader.flush()
		text := strings.TrimRightFunc(strings.TrimPrefix(askHTMLTextContent(node), "\n"), askHTMLSpace)
		reader.section.blocks = append(reader.section.blocks, &askHTMLFormBlock{text: text})
		return
	case "svg":
		reader.flush()
		reader.section.blocks = append(reader.section.blocks, &askHTMLFormBlock{})
		return
	case "img":
		if strings.TrimFunc(askHTMLValue(node, "src"), askHTMLSpace) != "" {
			reader.flush()
			reader.section.blocks = append(reader.section.blocks, &askHTMLFormBlock{})
		}
		return
	case "br":
		appendAskHTMLSpan(&reader.inline, "\n", "")
		return
	case "hr":
		reader.flush()
		return
	case "legend":
		reader.flush()
		reader.children(node, style)
		reader.flush()
		return
	case "ul", "ol":
		reader.flush()
		if reader.controlCounts[node] > 0 {
			reader.children(node, "")
			reader.flush()
		} else if text, exists := askHTMLListText(node, node.Data == "ol"); exists {
			reader.section.blocks = append(reader.section.blocks, &askHTMLFormBlock{text: text})
		}
		return
	}
	if askHTMLBlock(node.Data) {
		reader.flush()
		reader.children(node, style)
		reader.flush()
		return
	}
	reader.children(node, askHTMLStyle(node.Data, style))
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
	name := askHTMLValue(node, "name")
	_, required := askHTMLAttr(node, "required")
	if node.Data == "select" {
		_, multi := askHTMLAttr(node, "multiple")
		field := &askHTMLField{name: name, label: label, multi: multi, menu: true, required: required}
		var visit func(*html.Node)
		visit = func(current *html.Node) {
			if reader.err != nil {
				return
			}
			if current.Type == html.ElementNode && current.Data == "option" {
				text := askOneLine(askHTMLTextContent(current))
				if !reader.retainMetadata(text) {
					return
				}
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
		if reader.err != nil {
			return
		}
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
		reader.addField(field)
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
	if labelNode != nil {
		var encoded bytes.Buffer
		if err := html.Render(&encoded, labelNode); err != nil {
			reader.err = err
			return
		}
		markup = encoded.String()
	}
	if !reader.retainMetadata(label, markup) {
		return
	}
	_, checked := askHTMLAttr(node, "checked")
	_, disabled := askHTMLAttr(node, "disabled")
	option := askHTMLOption{label: label, value: value, markup: markup, checked: checked, disabled: disabled}
	key := typeName + "\x00" + name
	if group := reader.groups[key]; group != nil {
		group.field.options = append(group.field.options, option)
		group.field.required = group.field.required || required
		if group.section == reader.section && reader.section.blocks[len(reader.section.blocks)-1] != group.block {
			group.block.optionsEnd = len(reader.section.blocks)
		}
		return
	}
	field := &askHTMLField{name: name, options: []askHTMLOption{option}, multi: typeName == "checkbox", required: required}
	block := reader.addField(field)
	reader.groups[key] = &askHTMLGroup{field: field, section: reader.section, block: block}
}

var askOtherLabelPrefix = regexp.MustCompile(`(?i)^other\b`)

func (field *askHTMLField) nativeQuestion(caption, section string) (contracts.OhMyPiAskQuestion, error) {
	question := field.label
	if question == "" {
		question = caption
	}
	if question == "" {
		question = section
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
	if header := askOneLine(section); header != "" && header != question {
		converted.Header = header
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
