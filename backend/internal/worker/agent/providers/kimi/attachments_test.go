package kimi

import (
	"bytes"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func TestBuildKimiContent(t *testing.T) {
	t.Parallel()

	t.Run("text alone is one part", func(t *testing.T) {
		t.Parallel()
		parts, err := buildKimiContent("Hello.", nil, false)
		require.NoError(t, err)
		assert.Equal(t, []map[string]any{{"type": "text", "text": "Hello."}}, parts)
	})

	t.Run("a text file joins the text inline", func(t *testing.T) {
		t.Parallel()
		parts, err := buildKimiContent("Read this.", []*leapmuxv1.Attachment{{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("line one")}}, false)
		require.NoError(t, err)
		require.Len(t, parts, 1)
		text, _ := parts[0]["text"].(string)
		assert.Contains(t, text, "Read this.")
		assert.Contains(t, text, "notes.txt")
		assert.Contains(t, text, "line one")
	})

	t.Run("an image is an inline part for a model that takes images", func(t *testing.T) {
		t.Parallel()
		data := []byte{0x89, 0x50, 0x4e, 0x47}
		parts, err := buildKimiContent("", []*leapmuxv1.Attachment{{Filename: "shot.png", MimeType: "image/png", Data: data}}, true)
		require.NoError(t, err)
		require.Len(t, parts, 1, "an image with no text is a prompt of its own")
		assert.Equal(t, map[string]any{"type": "image", "source": map[string]any{
			"kind": "base64", "media_type": "image/png", "data": base64.StdEncoding.EncodeToString(data),
		}}, parts[0])
	})

	t.Run("an image is refused for a text-only model", func(t *testing.T) {
		t.Parallel()
		_, err := buildKimiContent("Look.", []*leapmuxv1.Attachment{{Filename: "shot.png", MimeType: "image/png", Data: []byte{0x89}}}, false)
		require.ErrorContains(t, err, "does not take images")
	})

	t.Run("a PDF and a binary file are refused", func(t *testing.T) {
		t.Parallel()
		_, err := buildKimiContent("", []*leapmuxv1.Attachment{{Filename: "a.pdf", MimeType: "application/pdf", Data: []byte("%PDF")}}, true)
		require.ErrorContains(t, err, "PDF")
		_, err = buildKimiContent("", []*leapmuxv1.Attachment{{Filename: "a.bin", MimeType: "application/octet-stream", Data: []byte{0xff, 0x00}}}, true)
		require.ErrorContains(t, err, "binary")
	})

	t.Run("the total size is capped", func(t *testing.T) {
		t.Parallel()
		big := make([]byte, kimiMaxAttachmentBytes/2+1)
		for i := range big {
			big[i] = 'a'
		}
		_, err := buildKimiContent("", []*leapmuxv1.Attachment{
			{Filename: "a.txt", MimeType: "text/plain", Data: big},
			{Filename: "b.txt", MimeType: "text/plain", Data: big},
		}, false)
		require.ErrorContains(t, err, "20 MB")

		_, err = buildKimiContent("", []*leapmuxv1.Attachment{{Filename: "a.txt", MimeType: "text/plain", Data: big}}, false)
		require.NoError(t, err, "one file under the cap is taken")
	})

	t.Run("an empty prompt is refused", func(t *testing.T) {
		t.Parallel()
		_, err := buildKimiContent("", nil, true)
		require.ErrorContains(t, err, "needs text or an attachment")
	})

	t.Run("the cap takes exactly its limit and refuses one byte more", func(t *testing.T) {
		t.Parallel()
		half := bytes.Repeat([]byte("a"), kimiMaxAttachmentBytes/2)
		_, err := buildKimiContent("", []*leapmuxv1.Attachment{
			{Filename: "a.txt", MimeType: "text/plain", Data: half},
			{Filename: "b.txt", MimeType: "text/plain", Data: half},
		}, false)
		require.NoError(t, err, "attachments that sum to the cap are taken")

		_, err = buildKimiContent("", []*leapmuxv1.Attachment{
			{Filename: "a.txt", MimeType: "text/plain", Data: half},
			{Filename: "b.txt", MimeType: "text/plain", Data: append(half, 'b')},
		}, false)
		require.ErrorContains(t, err, "20 MB")
	})

	t.Run("the text precedes the images, which keep their order", func(t *testing.T) {
		t.Parallel()
		parts, err := buildKimiContent("Compare them.", []*leapmuxv1.Attachment{
			{Filename: "first.png", MimeType: "image/png", Data: []byte{0x89, 0x01}},
			{Filename: "second.png", MimeType: "image/png", Data: []byte{0x89, 0x02}},
		}, true)
		require.NoError(t, err)
		require.Len(t, parts, 3)
		assert.Equal(t, map[string]any{"type": "text", "text": "Compare them."}, parts[0])
		for i, data := range [][]byte{{0x89, 0x01}, {0x89, 0x02}} {
			source, ok := parts[i+1]["source"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, base64.StdEncoding.EncodeToString(data), source["data"])
		}
	})

	t.Run("blank text beside a text file sends the file alone", func(t *testing.T) {
		t.Parallel()
		file := &leapmuxv1.Attachment{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("line one")}
		parts, err := buildKimiContent(" \n ", []*leapmuxv1.Attachment{file}, false)
		require.NoError(t, err)
		classified := agent.ClassifyAttachments([]*leapmuxv1.Attachment{file})
		require.Len(t, classified, 1)
		assert.Equal(t, []map[string]any{{"type": "text", "text": providerkit.BuildInlineTextAttachmentBlock(classified[0])}}, parts,
			"the blank text does not open the prompt")
	})
}

func TestKimiValidateAttachmentMatchesTheContentRule(t *testing.T) {
	t.Parallel()

	for _, attachment := range []*leapmuxv1.Attachment{
		{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("hi")},
		{Filename: "shot.png", MimeType: "image/png", Data: []byte{0x89, 0x50}},
	} {
		classified := agent.ClassifyAttachments([]*leapmuxv1.Attachment{attachment})
		require.Len(t, classified, 1)
		assert.NoError(t, kimiProvider{}.ValidateAttachment(classified[0]), attachment.Filename)
	}
	for _, attachment := range []*leapmuxv1.Attachment{
		{Filename: "spec.pdf", MimeType: "application/pdf", Data: []byte("%PDF")},
		{Filename: "a.bin", MimeType: "application/octet-stream", Data: []byte{0xff, 0x00}},
	} {
		classified := agent.ClassifyAttachments([]*leapmuxv1.Attachment{attachment})
		require.Len(t, classified, 1)
		assert.Error(t, kimiProvider{}.ValidateAttachment(classified[0]), attachment.Filename)
	}
}
