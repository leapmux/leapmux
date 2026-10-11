package mimo

import (
	"bytes"
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildPromptParts(t *testing.T) {
	t.Parallel()

	t.Run("a message alone is one text part", func(t *testing.T) {
		t.Parallel()
		parts, err := buildPromptParts("hello", nil)
		require.NoError(t, err)
		assert.Equal(t, []mimoPromptPart{{Type: promptPartText, Text: "hello"}}, parts)
	})

	t.Run("an empty message is one empty text part", func(t *testing.T) {
		t.Parallel()
		parts, err := buildPromptParts("", nil)
		require.NoError(t, err)
		assert.Equal(t, []mimoPromptPart{{Type: promptPartText}}, parts, "the server refuses a prompt with no part")
	})

	t.Run("a text attachment joins the message", func(t *testing.T) {
		t.Parallel()
		parts, err := buildPromptParts("see the notes", []*leapmuxv1.Attachment{
			{Filename: "notes.md", MimeType: "text/markdown", Data: []byte("# Notes")},
		})
		require.NoError(t, err)
		require.Len(t, parts, 1)
		assert.Equal(t, promptPartText, parts[0].Type)
		assert.True(t, strings.HasPrefix(parts[0].Text, "see the notes\n\n"), "the message comes first: %q", parts[0].Text)
		assert.Contains(t, parts[0].Text, "notes.md")
		assert.Contains(t, parts[0].Text, "# Notes")
	})

	t.Run("text attachments join the message in attachment order, before every file", func(t *testing.T) {
		t.Parallel()
		parts, err := buildPromptParts("compare them", []*leapmuxv1.Attachment{
			{Filename: "first.md", MimeType: "text/markdown", Data: []byte("FIRST-BODY")},
			{Filename: "diagram.png", MimeType: "image/png", Data: []byte{0x89, 0x50}},
			{Filename: "second.go", MimeType: "text/x-go", Data: []byte("SECOND-BODY")},
		})
		require.NoError(t, err)
		require.Len(t, parts, 2, "one text part, then one file part")
		assert.Equal(t, promptPartText, parts[0].Type)
		first := strings.Index(parts[0].Text, "FIRST-BODY")
		second := strings.Index(parts[0].Text, "SECOND-BODY")
		require.GreaterOrEqual(t, first, 0)
		require.GreaterOrEqual(t, second, 0)
		assert.Less(t, first, second)
		assert.True(t, strings.HasPrefix(parts[0].Text, "compare them\n\n"))
		assert.Equal(t, promptPartFile, parts[1].Type)
		assert.Equal(t, "diagram.png", parts[1].Filename)
	})

	t.Run("an image and a PDF are file parts in attachment order", func(t *testing.T) {
		t.Parallel()
		parts, err := buildPromptParts("look", []*leapmuxv1.Attachment{
			{Filename: "spec.pdf", MimeType: "application/pdf", Data: []byte("%PDF")},
			{Filename: "diagram.png", MimeType: "image/png", Data: []byte{0x89, 0x50}},
		})
		require.NoError(t, err)
		require.Len(t, parts, 3)
		assert.Equal(t, mimoPromptPart{Type: promptPartText, Text: "look"}, parts[0])
		assert.Equal(t, mimoPromptPart{Type: promptPartFile, URL: "data:application/pdf;base64,JVBERg==", Mime: "application/pdf", Filename: "spec.pdf"}, parts[1])
		assert.Equal(t, mimoPromptPart{Type: promptPartFile, URL: "data:image/png;base64,iVA=", Mime: "image/png", Filename: "diagram.png"}, parts[2])
	})

	t.Run("files alone carry no empty text part", func(t *testing.T) {
		t.Parallel()
		parts, err := buildPromptParts("", []*leapmuxv1.Attachment{
			{Filename: "diagram.png", MimeType: "image/png", Data: []byte{0x89, 0x50}},
		})
		require.NoError(t, err)
		require.Len(t, parts, 1)
		assert.Equal(t, promptPartFile, parts[0].Type, "a model API can refuse an empty text block")
	})

	t.Run("a binary attachment is refused", func(t *testing.T) {
		t.Parallel()
		_, err := buildPromptParts("run it", []*leapmuxv1.Attachment{
			{Filename: "tool.bin", MimeType: "application/octet-stream", Data: []byte{0x00, 0xff}},
		})
		assert.ErrorContains(t, err, "tool.bin")
	})

	t.Run("attachments over the cap are refused", func(t *testing.T) {
		t.Parallel()
		half := bytes.Repeat([]byte{0x89}, mimoMaxAttachmentBytes/2+1)
		_, err := buildPromptParts("", []*leapmuxv1.Attachment{
			{Filename: "a.png", MimeType: "image/png", Data: half},
			{Filename: "b.png", MimeType: "image/png", Data: half},
		})
		assert.ErrorContains(t, err, "exceed")
	})

	t.Run("attachments at the cap are accepted", func(t *testing.T) {
		t.Parallel()
		_, err := buildPromptParts("", []*leapmuxv1.Attachment{
			{Filename: "a.png", MimeType: "image/png", Data: bytes.Repeat([]byte{0x89}, mimoMaxAttachmentBytes)},
		})
		assert.NoError(t, err)
	})
}
