package providerkit

import "strings"

// JoinParagraphs joins the parts of one message as the browser's paragraph
// joiner does (createParagraphJoiner in frontend/src/lib/contentBlocks.ts),
// so a reply text that a provider reads in Go matches the transcript. An
// empty part is skipped, and each other part follows the text before it after
// at least two newlines: the newlines that the text already ends with count,
// and a longer run stays as it is.
func JoinParagraphs(parts []string) string {
	var out strings.Builder
	for _, part := range parts {
		if part == "" {
			continue
		}
		if out.Len() > 0 {
			text := out.String()
			trailing := len(text) - len(strings.TrimRight(text, "\n"))
			for range max(0, 2-trailing) {
				out.WriteByte('\n')
			}
		}
		out.WriteString(part)
	}
	return out.String()
}
