package providerkit

import "strings"

type TextJoin uint8

const (
	// ACP chunks, Codex deltas, Pi deltas, and ZCode deltas use verbatim joins.
	JoinVerbatim TextJoin = iota
	// Codex reasoning summary parts use paragraph joins.
	JoinParagraph
)

func AppendText(builder *strings.Builder, fragment string, join TextJoin) {
	if fragment == "" {
		return
	}
	if builder.Len() > 0 && join == JoinParagraph {
		builder.WriteString(missingParagraphSeparator(builder.String(), fragment))
	}
	builder.WriteString(fragment)
}

func missingParagraphSeparator(previous, next string) string {
	existing := trailingLineBreaks(previous) + leadingLineBreaks(next)
	if existing >= 2 {
		return ""
	}
	return strings.Repeat("\n", 2-existing)
}

func trailingLineBreaks(text string) int {
	count := 0
	for index := len(text); index > 0; {
		switch text[index-1] {
		case '\n':
			index--
			if index > 0 && text[index-1] == '\r' {
				index--
			}
			count++
		case '\r':
			index--
			count++
		default:
			return count
		}
	}
	return count
}

func leadingLineBreaks(text string) int {
	count := 0
	for index := 0; index < len(text); {
		switch text[index] {
		case '\r':
			index++
			if index < len(text) && text[index] == '\n' {
				index++
			}
			count++
		case '\n':
			index++
			count++
		default:
			return count
		}
	}
	return count
}
