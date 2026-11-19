package ohmypi

import (
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// choicePresentation is what one option of a bridge question shows: its
// title, the text beside the title, and the code that the label quotes as
// Markdown code blocks.
type choicePresentation struct {
	label, description, preview string
}

// choiceDisambiguation matches the suffix that the bridge adds to the second
// and each later option with the same label text: " (2)", " (3)", and so on
// (askHTMLField.nativeQuestion).
var choiceDisambiguation = regexp.MustCompile(`^ \((?:[2-9]|[1-9]\d+)\)$`)

// backtickRun matches one run of backticks.
var backtickRun = regexp.MustCompile("`+")

// choiceMarkupContext is the element that a label's markup is parsed inside.
// A <template> parses any element where it stands, as an HTML parser does
// for a fragment that states no context of its own.
func choiceMarkupContext() *html.Node {
	return &html.Node{Type: html.ElementNode, Data: atom.Template.String(), DataAtom: atom.Template}
}

// readChoiceMarkup reads the presentation of one option from the label HTML
// that the bridge keeps beside it (contracts.OhMyPiAskOption.Markup).
// fallback is the option's own label, which the bridge answers omp with.
//
//   - A label that opens with <strong> or <b> takes that element's text as
//     its title, and the rest of its text as the description.
//   - Any other label takes its text as the title, and states no description.
//   - Each <pre> of the label is a code block of the preview, and its text
//     is no part of the title or the description.
//   - A title keeps the suffix that tells two options with the same text
//     apart (choiceDisambiguation).
//
// Form controls, embedded graphics and elements that show no text are no part
// of any text. An empty markup, one that holds no <label>, and one that the
// HTML parser refuses show fallback alone.
func readChoiceMarkup(markup, fallback string) choicePresentation {
	plain := choicePresentation{label: fallback}
	if markup == "" {
		return plain
	}
	nodes, err := html.ParseFragment(strings.NewReader(markup), choiceMarkupContext())
	if err != nil {
		return plain
	}
	fragment := &html.Node{Type: html.DocumentNode}
	for _, node := range nodes {
		fragment.AppendChild(node)
	}
	label := firstChoiceLabel(fragment)
	if label == nil {
		return plain
	}
	omitted := make(map[*html.Node]bool)
	var code []*html.Node
	for _, node := range choiceMarkupNodesBelow(label, choiceMarkupSkipped) {
		if node.Type == html.ElementNode && node.Data == "pre" {
			code = append(code, node)
			omitted[node] = true
		}
	}
	var title, description string
	if heading := choiceMarkupHeading(label); heading != nil {
		title = choiceMarkupText(heading, nil)
		omitted[heading] = true
		description = choiceMarkupText(label, omitted)
	} else {
		title = choiceMarkupText(label, omitted)
	}
	presentation := choicePresentation{label: fallback, description: description, preview: choiceMarkupPreview(code)}
	if title != "" {
		presentation.label = title + choiceMarkupSuffix(choiceMarkupText(label, nil), fallback)
	}
	return presentation
}

// firstChoiceLabel returns the first <label> of fragment in document order,
// or nil when it holds none.
func firstChoiceLabel(fragment *html.Node) *html.Node {
	for _, node := range choiceMarkupNodesBelow(fragment, choiceMarkupSkipped) {
		if node.Type == html.ElementNode && node.Data == "label" {
			return node
		}
	}
	return nil
}

// choiceMarkupHeading returns the first child of label that shows anything,
// when that child is <strong> or <b>, and nil otherwise. A comment, a skipped
// element and white space show nothing.
func choiceMarkupHeading(label *html.Node) *html.Node {
	for child := label.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.CommentNode || choiceMarkupSkipped(child) {
			continue
		}
		if child.Type == html.TextNode && agent.TrimJavaScriptSpace(child.Data) == "" {
			continue
		}
		if child.Type == html.ElementNode && (child.Data == "strong" || child.Data == "b") {
			return child
		}
		return nil
	}
	return nil
}

// choiceMarkupSuffix returns the disambiguation suffix that fallback, the
// option's label, adds after text, the label's whole text. It returns "" when
// fallback does not continue text with such a suffix.
func choiceMarkupSuffix(text, fallback string) string {
	if text == "" {
		return ""
	}
	suffix, found := strings.CutPrefix(fallback, text)
	if !found || !choiceDisambiguation.MatchString(suffix) {
		return ""
	}
	return suffix
}

// choiceMarkupPreview returns the text of each code element as a Markdown
// code block, with a blank line between two blocks. An element whose text is
// only white space adds no block.
func choiceMarkupPreview(code []*html.Node) string {
	blocks := make([]string, 0, len(code))
	for _, node := range code {
		if text := choiceMarkupCodeText(node); agent.TrimJavaScriptSpace(text) != "" {
			blocks = append(blocks, literalCodeBlock(text))
		}
	}
	return strings.Join(blocks, "\n\n")
}

// choiceMarkupSkipped reports an element whose text a label never shows: a
// form control, an embedded graphic, and an element that holds no visible
// text, such as a script.
func choiceMarkupSkipped(node *html.Node) bool {
	return node.Type == html.ElementNode && (askHTMLSkipped(node.Data) || askHTMLControl(node) || node.Data == "svg")
}

// choiceMarkupNodesBelow lists the nodes below root in document order. A node
// that skip reports is left out together with every node below it.
func choiceMarkupNodesBelow(root *html.Node, skip func(*html.Node) bool) []*html.Node {
	var nodes, pending []*html.Node
	pushChildren := func(node *html.Node) {
		for child := node.LastChild; child != nil; child = child.PrevSibling {
			pending = append(pending, child)
		}
	}
	pushChildren(root)
	for len(pending) > 0 {
		node := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if skip(node) {
			continue
		}
		nodes = append(nodes, node)
		pushChildren(node)
	}
	return nodes
}

// choiceMarkupText returns the visible text below root on one line: the text
// of each node, the alternative text of each image, and a space for each line
// break and around each block. White space collapses to one space, and the
// text is trimmed. A node of omitted adds no text, and neither does any node
// below it.
func choiceMarkupText(root *html.Node, omitted map[*html.Node]bool) string {
	var text strings.Builder
	skip := func(node *html.Node) bool { return omitted[node] || choiceMarkupSkipped(node) }
	for _, node := range choiceMarkupNodesBelow(root, skip) {
		switch {
		case node.Type == html.TextNode:
			text.WriteString(node.Data)
		case node.Type != html.ElementNode:
		case node.Data == "img":
			text.WriteString(askHTMLValue(node, "alt"))
		case node.Data == "br" || askHTMLBlock(node.Data):
			text.WriteByte(' ')
		}
	}
	return askOneLine(text.String())
}

// choiceMarkupCodeText returns the text below root exactly as the markup
// states it, the text of skipped elements included.
func choiceMarkupCodeText(root *html.Node) string {
	var text strings.Builder
	for _, node := range choiceMarkupNodesBelow(root, func(*html.Node) bool { return false }) {
		if node.Type == html.TextNode {
			text.WriteString(node.Data)
		}
	}
	return text.String()
}

// literalCodeBlock wraps text in a Markdown code fence that the text cannot
// close: the fence is one backtick longer than the longest run of backticks
// in text, and three backticks at least.
func literalCodeBlock(text string) string {
	longest := 0
	for _, run := range backtickRun.FindAllString(text, -1) {
		longest = max(longest, len(run))
	}
	fence := strings.Repeat("`", max(3, longest+1))
	return fence + "\n" + text + "\n" + fence
}
