package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAppendTextKeepsVerbatimFragmentsUnchanged(t *testing.T) {
	t.Parallel()

	var text strings.Builder
	appendText(&text, "**Verifying terminal release synchronization", joinVerbatim)
	appendText(&text, "Analyzing lock acquisition order and concurrency**", joinVerbatim)
	assert.Equal(t,
		"**Verifying terminal release synchronizationAnalyzing lock acquisition order and concurrency**",
		text.String(),
	)
}

func TestAppendTextAddsOnlyTheMissingParagraphNewlines(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		previous string
		next     string
		want     string
	}{
		{name: "no newline", previous: "first", next: "second", want: "first\n\nsecond"},
		{name: "previous provides one", previous: "first\n", next: "second", want: "first\n\nsecond"},
		{name: "next provides one", previous: "first", next: "\nsecond", want: "first\n\nsecond"},
		{name: "previous provides two", previous: "first\n\n", next: "second", want: "first\n\nsecond"},
		{name: "next provides two", previous: "first", next: "\n\nsecond", want: "first\n\nsecond"},
		{name: "both provide one", previous: "first\n", next: "\nsecond", want: "first\n\nsecond"},
		{name: "multiple breaks remain", previous: "first\n\n\n\n", next: "second", want: "first\n\n\n\nsecond"},
		{name: "previous provides two CRLF breaks", previous: "first\r\n\r\n", next: "second", want: "first\r\n\r\nsecond"},
		{name: "both provide one CRLF break", previous: "first\r\n", next: "\r\nsecond", want: "first\r\n\r\nsecond"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var text strings.Builder
			text.WriteString(test.previous)
			appendText(&text, test.next, joinParagraph)
			assert.Equal(t, test.want, text.String())
		})
	}
}
