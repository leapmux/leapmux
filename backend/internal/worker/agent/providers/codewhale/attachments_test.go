package codewhale

import (
	"bytes"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func image(name string, size int) agent.ClassifiedAttachment {
	return agent.ClassifiedAttachment{Kind: agent.AttachmentKindImage, Filename: name, MIMEType: "image/png", Data: bytes.Repeat([]byte{1}, size)}
}

func TestBuildTurnInput(t *testing.T) {
	t.Parallel()
	prompt, images, err := buildTurnInput("Hi.", nil, imageInputUnknown)
	require.NoError(t, err)
	assert.Equal(t, "Hi.", prompt)
	assert.Nil(t, images)

	text := agent.ClassifiedAttachment{Kind: agent.AttachmentKindText, Filename: "notes.txt", MIMEType: "text/plain", Data: []byte("alpha")}
	prompt, images, err = buildTurnInput("Look.", []agent.ClassifiedAttachment{text, image("a.png", 3)}, imageInputSupported)
	require.NoError(t, err)
	assert.Equal(t, "Look.\n\n"+providerkit.BuildInlineTextAttachmentBlock(text), prompt, "the message comes first, then each text block, and the image stays out of the prompt")
	require.Len(t, images, 1)
	assert.Equal(t, turnImage{Mime: "image/png", DataBase64: base64.StdEncoding.EncodeToString([]byte{1, 1, 1})}, images[0])

	// An empty message with an attachment sends the attachment alone.
	prompt, _, err = buildTurnInput("", []agent.ClassifiedAttachment{text}, imageInputUnknown)
	require.NoError(t, err)
	assert.Equal(t, providerkit.BuildInlineTextAttachmentBlock(text), prompt)

	// A model whose image capability is unknown leaves the image to the runtime.
	_, images, err = buildTurnInput("Look.", []agent.ClassifiedAttachment{image("a.png", 1)}, imageInputUnknown)
	require.NoError(t, err)
	assert.Len(t, images, 1)
}

func TestBuildTurnInputRefusesWhatTheRuntimeRefuses(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		attachments []agent.ClassifiedAttachment
		want        string
	}{
		"a PDF":          {[]agent.ClassifiedAttachment{{Kind: agent.AttachmentKindPDF, Filename: "a.pdf"}}, "does not support PDF attachments: a.pdf"},
		"a binary":       {[]agent.ClassifiedAttachment{{Kind: agent.AttachmentKindBinary, Filename: "a.bin"}}, "does not support binary attachments: a.bin"},
		"a large image":  {[]agent.ClassifiedAttachment{image("big.png", maxTurnImageBytes+1)}, "the image big.png is larger than the 4 MiB"},
		"too many bytes": {[]agent.ClassifiedAttachment{image("a.png", 3<<20), image("b.png", 3<<20)}, "the images of one message are larger than the 5 MiB"},
		// A text block before the PDF does not stop the refusal.
		"a PDF after a text": {[]agent.ClassifiedAttachment{{Kind: agent.AttachmentKindText, Filename: "a.txt", Data: []byte("x")}, {Kind: agent.AttachmentKindPDF, Filename: "b.pdf"}}, "b.pdf"},
	} {
		prompt, images, err := buildTurnInput("x", tc.attachments, imageInputSupported)
		assert.ErrorContains(t, err, tc.want, name)
		assert.Empty(t, prompt, name)
		assert.Nil(t, images, name)
	}
	var many []agent.ClassifiedAttachment
	for range maxTurnImages + 1 {
		many = append(many, image("a.png", 1))
	}
	_, _, err := buildTurnInput("x", many, imageInputSupported)
	assert.ErrorContains(t, err, "at most")
	_, _, err = buildTurnInput("x", many[:maxTurnImages], imageInputSupported)
	assert.NoError(t, err, "exactly the limit is accepted")

	_, _, err = buildTurnInput("x", []agent.ClassifiedAttachment{image("a.png", 1)}, imageInputUnsupported)
	assert.ErrorContains(t, err, "does not accept images")
}

// The runtime measures its image limits inclusively: an image of exactly 4 MiB,
// and images of exactly 5 MiB in all, pass.
func TestBuildTurnInputAcceptsImagesAtTheLimits(t *testing.T) {
	t.Parallel()
	_, images, err := buildTurnInput("x", []agent.ClassifiedAttachment{image("a.png", maxTurnImageBytes)}, imageInputSupported)
	require.NoError(t, err, "one image of exactly the per-image limit")
	assert.Len(t, images, 1)

	atTotal := []agent.ClassifiedAttachment{image("a.png", maxTurnImageBytes), image("b.png", maxTurnImagesTotalBytes-maxTurnImageBytes)}
	_, images, err = buildTurnInput("x", atTotal, imageInputSupported)
	require.NoError(t, err, "images of exactly the total limit")
	assert.Len(t, images, 2)

	overTotal := []agent.ClassifiedAttachment{image("a.png", maxTurnImageBytes), image("b.png", maxTurnImagesTotalBytes-maxTurnImageBytes+1)}
	_, _, err = buildTurnInput("x", overTotal, imageInputSupported)
	assert.ErrorContains(t, err, "larger than the 5 MiB", "one byte past the total limit")
}
