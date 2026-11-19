package ohmypi

import (
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

type askHTMLSpan struct {
	style string
	parts []string
}

func askHTMLSpace(character rune) bool {
	return character == '\uFEFF' || character != '\u0085' && unicode.IsSpace(character)
}

func askHTMLStyle(tag, inherited string) string {
	switch tag {
	case "b", "strong":
		return "strong"
	case "code", "kbd", "samp", "tt":
		return "code"
	case "em", "i":
		return "em"
	default:
		return inherited
	}
}

func appendAskHTMLSpan(spans *[]askHTMLSpan, text, style string) {
	if text == "" {
		return
	}
	if count := len(*spans); count > 0 && (*spans)[count-1].style == style {
		(*spans)[count-1].parts = append((*spans)[count-1].parts, text)
		return
	}
	*spans = append(*spans, askHTMLSpan{style: style, parts: []string{text}})
}

// Native normalizeSpans collapses each style run separately. It preserves
// newlines and keeps spaces on opposite sides of a style boundary separate.
func askHTMLSpanText(spans []askHTMLSpan) string {
	parts := make([]string, 0, len(spans))
	for _, span := range spans {
		var normalized strings.Builder
		space := false
		for _, character := range strings.Join(span.parts, "") {
			if character != '\n' && askHTMLSpace(character) {
				if !space {
					normalized.WriteByte(' ')
				}
				space = true
			} else {
				normalized.WriteRune(character)
				space = false
			}
		}
		text := strings.ReplaceAll(strings.ReplaceAll(normalized.String(), " \n", "\n"), "\n ", "\n")
		if text != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	parts[0] = strings.TrimLeftFunc(parts[0], askHTMLSpace)
	last := len(parts) - 1
	parts[last] = strings.TrimRightFunc(parts[last], askHTMLSpace)
	return strings.Join(parts, "")
}

func askHTMLSpansText(node *html.Node) string {
	var spans []askHTMLSpan
	var visit func(*html.Node, string)
	visit = func(current *html.Node, style string) {
		if current.Type == html.TextNode {
			appendAskHTMLSpan(&spans, current.Data, style)
			return
		}
		if current.Type != html.ElementNode || askHTMLSkipped(current.Data) || askHTMLControl(current) || current.Data == "svg" {
			return
		}
		switch current.Data {
		case "br":
			appendAskHTMLSpan(&spans, "\n", "")
			return
		case "img":
			appendAskHTMLSpan(&spans, askHTMLValue(current, "alt"), style)
			return
		}
		if askHTMLBlock(current.Data) {
			appendAskHTMLSpan(&spans, " ", "")
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			visit(child, askHTMLStyle(current.Data, style))
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		visit(child, "")
	}
	return askHTMLSpanText(spans)
}

func askHTMLListText(node *html.Node, ordered bool) (string, bool) {
	var items []string
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != html.ElementNode || child.Data != "li" {
			continue
		}
		text := askHTMLSpansText(child)
		if text == "" {
			continue
		}
		prefix := "- "
		if ordered {
			prefix = strconv.Itoa(len(items)+1) + ". "
		}
		items = append(items, prefix+text)
	}
	return strings.Join(items, "\n"), len(items) > 0
}

func askHTMLTableText(table *html.Node) (string, bool) {
	var rows []string
	cellsFound := false
	var visit func(*html.Node)
	visit = func(node *html.Node) {
		if node != table && node.Type == html.ElementNode && node.Data == "table" {
			return
		}
		if node.Type == html.ElementNode && node.Data == "tr" {
			var cells []string
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				if child.Type == html.ElementNode && (child.Data == "td" || child.Data == "th") {
					cells = append(cells, askHTMLSpansText(child))
				}
			}
			cellsFound = cellsFound || len(cells) > 0
			rows = append(rows, strings.Join(cells, " | "))
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(table)
	return strings.Join(rows, "\n"), cellsFound
}
