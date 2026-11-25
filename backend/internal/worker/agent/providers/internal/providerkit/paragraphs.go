package providerkit

import "strings"

// JoinParagraphs joins the parts of one message as the browser's paragraph
// joiner does (createParagraphJoiner in frontend/src/lib/contentBlocks.ts),
// so a reply text that a provider reads in Go matches the transcript. An
// empty part is skipped, and each other part follows the text before it after
// at least two newlines: the newlines that the text already ends with count,
// and a longer run stays as it is.
func JoinParagraphs(parts []string) string {
	total, nonEmpty := 0, 0
	for _, part := range parts {
		if part != "" {
			total += len(part)
			nonEmpty++
		}
	}
	if nonEmpty == 0 {
		return ""
	}
	var out strings.Builder
	out.Grow(total + 2*(nonEmpty-1))
	for _, part := range parts {
		if part == "" {
			continue
		}
		if out.Len() > 0 {
			text := out.String()
			trailing := 0
			for i := len(text) - 1; i >= 0 && text[i] == '\n' && trailing < 2; i-- {
				trailing++
			}
			for range 2 - trailing {
				out.WriteByte('\n')
			}
		}
		out.WriteString(part)
	}
	return out.String()
}
