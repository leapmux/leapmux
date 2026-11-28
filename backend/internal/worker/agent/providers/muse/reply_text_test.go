package muse

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
)

func TestReplyTextConformance(t *testing.T) {
	agenttest.RunReplyTextConformance(t, Registration())
}

func TestReplyTextHidesUnprojectedViewFrames(t *testing.T) {
	t.Parallel()
	const validFrame = `{"method":"item/completed","params":{"sessionId":"child-1","item":{"itemId":"i1","kind":"agentMessage","text":"Child answer"}}}`
	field := contracts.MuseSupplementFieldViewProjectionUnavailable

	for _, tc := range []struct {
		name         string
		original     string
		supplemental string
		wantText     string
		wantOK       bool
	}{
		{
			name:         "matching session and reason hides frame",
			original:     validFrame,
			supplemental: `{"` + field + `":{"sessionId":"child-1","reason":"The Muse view could not be projected."}}`,
			wantOK:       false,
		},
		{
			name:         "U+0085 session and reason are non-blank in JavaScript",
			original:     `{"method":"item/completed","params":{"sessionId":"\u0085","item":{"itemId":"i1","kind":"agentMessage","text":"Child answer"}}}`,
			supplemental: `{"` + field + `":{"sessionId":"\u0085","reason":"\u0085"}}`,
			wantOK:       false,
		},
		{
			name:         "mismatched session does not hide frame",
			original:     validFrame,
			supplemental: `{"` + field + `":{"sessionId":"other-child","reason":"The Muse view could not be projected."}}`,
			wantText:     "Child answer",
			wantOK:       true,
		},
		{
			name:         "blank reason does not hide frame",
			original:     validFrame,
			supplemental: `{"` + field + `":{"sessionId":"child-1","reason":" \t\n\uFEFF"}}`,
			wantText:     "Child answer",
			wantOK:       true,
		},
		{
			name:         "blank session does not hide frame",
			original:     `{"method":"item/completed","params":{"sessionId":"  ","item":{"itemId":"i1","kind":"agentMessage","text":"Child answer"}}}`,
			supplemental: `{"` + field + `":{"sessionId":"  ","reason":"failed"}}`,
			wantText:     "Child answer",
			wantOK:       true,
		},
		{
			name:         "wrong key case in supplement does not hide frame",
			original:     validFrame,
			supplemental: `{"NativeViewProjectionUnavailable":{"sessionId":"child-1","reason":"failed"}}`,
			wantText:     "Child answer",
			wantOK:       true,
		},
		{
			name:         "wrong nested sessionId case in supplement does not hide frame",
			original:     validFrame,
			supplemental: `{"` + field + `":{"SessionId":"child-1","reason":"failed"}}`,
			wantText:     "Child answer",
			wantOK:       true,
		},
		{
			name:         "wrong nested reason case in supplement does not hide frame",
			original:     validFrame,
			supplemental: `{"` + field + `":{"sessionId":"child-1","Reason":"failed"}}`,
			wantText:     "Child answer",
			wantOK:       true,
		},
		{
			name:         "malformed supplemental json does not hide frame",
			original:     validFrame,
			supplemental: `not json`,
			wantText:     "Child answer",
			wantOK:       true,
		},
		{
			name:         "null supplemental does not hide frame",
			original:     validFrame,
			supplemental: `null`,
			wantText:     "Child answer",
			wantOK:       true,
		},
		{
			name:         "non-object supplement does not hide frame",
			original:     validFrame,
			supplemental: `{"` + field + `":"failed"}`,
			wantText:     "Child answer",
			wantOK:       true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			text, ok := (museProvider{}).ReplyText(agent.MessageContent{
				Original:     []byte(tc.original),
				Supplemental: []byte(tc.supplemental),
			})
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantText, text)
		})
	}
}

func TestReplyTextRequiresNonEmptyStringTextAndSupportsAllItemMethods(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		original string
		wantText string
		wantOK   bool
	}{
		{name: "empty text returns false", original: `{"method":"item/completed","params":{"item":{"itemId":"i1","kind":"agentMessage","text":""}}}`, wantOK: false},
		{name: "absent text returns false", original: `{"method":"item/completed","params":{"item":{"itemId":"i1","kind":"agentMessage"}}}`, wantOK: false},
		{name: "non-string text returns false", original: `{"method":"item/completed","params":{"item":{"itemId":"i1","kind":"agentMessage","text":42}}}`, wantOK: false},
		{name: "whitespace text is non-empty", original: `{"method":"item/completed","params":{"item":{"itemId":"i1","kind":"agentMessage","text":" "}}}`, wantText: " ", wantOK: true},
		{name: "item started returns text", original: `{"method":"item/started","params":{"item":{"itemId":"i1","kind":"agentMessage","text":"Opening"}}}`, wantText: "Opening", wantOK: true},
		{name: "item updated returns text", original: `{"method":"item/updated","params":{"item":{"itemId":"i1","kind":"agentMessage","text":"Streaming"}}}`, wantText: "Streaming", wantOK: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			text, ok := (museProvider{}).ReplyText(agent.MessageContent{Original: []byte(tc.original)})
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantText, text)
		})
	}
}

func TestReplyTextRequiresExactJSONKeys(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		original string
	}{
		{name: "uppercase method", original: `{"METHOD":"item/completed","params":{"item":{"itemId":"i1","kind":"agentMessage","text":"Hi"}}}`},
		{name: "uppercase params", original: `{"method":"item/completed","Params":{"item":{"itemId":"i1","kind":"agentMessage","text":"Hi"}}}`},
		{name: "uppercase item", original: `{"method":"item/completed","params":{"Item":{"itemId":"i1","kind":"agentMessage","text":"Hi"}}}`},
		{name: "uppercase itemId", original: `{"method":"item/completed","params":{"item":{"ItemId":"i1","kind":"agentMessage","text":"Hi"}}}`},
		{name: "uppercase kind", original: `{"method":"item/completed","params":{"item":{"itemId":"i1","Kind":"agentMessage","text":"Hi"}}}`},
		{name: "uppercase text", original: `{"method":"item/completed","params":{"item":{"itemId":"i1","kind":"agentMessage","Text":"Hi"}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			text, ok := (museProvider{}).ReplyText(agent.MessageContent{Original: []byte(tc.original)})
			assert.False(t, ok)
			assert.Empty(t, text)
		})
	}
}
