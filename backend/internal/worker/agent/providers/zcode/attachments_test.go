package zcode

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// zcodeAttachment builds one wire attachment as the browser sends it.
func zcodeAttachment(filename, mimeType string, data []byte) *leapmuxv1.Attachment {
	return &leapmuxv1.Attachment{Filename: filename, MimeType: mimeType, Data: data}
}

// A text attachment is INLINED into the prompt, as Pi does: the model then quotes and
// reasons about it exactly as if the user had pasted it, and no capability question
// arises.
func TestBuildZCodeInput_TextIsInlinedIntoThePrompt(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.ControlSink{}))
	a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)

	text, wire, err := a.buildZCodeInput("read this", []*leapmuxv1.Attachment{
		zcodeAttachment("notes.txt", "text/plain", []byte("line one")),
	}, "builtin:zai/GLM-5.3")
	require.NoError(t, err)
	assert.Empty(t, wire, "an inlined attachment must not also travel on the wire")
	assert.Contains(t, text, "read this")
	assert.Contains(t, text, "notes.txt")
	assert.Contains(t, text, "line one")
}

// A prompt with no text of its own is the attachment alone, with no leading blank
// lines to make the model think something was elided.
func TestZCodeJoinPrompt(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "hi", zcodeJoinPrompt("hi", nil))
	assert.Equal(t, "hi\n\nblock", zcodeJoinPrompt("hi", []string{"block"}))
	assert.Equal(t, "a\n\nb", zcodeJoinPrompt("", []string{"a", "b"}))
	assert.Equal(t, "a", zcodeJoinPrompt("   ", []string{"a"}),
		"a whitespace-only message contributes nothing")
}

// An image travels as a wire attachment with the bytes INLINE. `sizeBytes` is the
// DECODED length: the app-server reads it to decide whether to treat the payload as
// text or as bytes, so a base64 length there would misroute it.
func TestBuildZCodeInput_ImageTravelsAsAWireAttachment(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.ControlSink{}))
	a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)
	data := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a}

	text, wire, err := a.buildZCodeInput("what is this", []*leapmuxv1.Attachment{
		zcodeAttachment("shot.png", "image/png", data),
	}, "builtin:zai/GLM-5.3-Flash")
	require.NoError(t, err)
	assert.Equal(t, "what is this", text, "an image does not touch the prompt text")
	require.Len(t, wire, 1)
	assert.Equal(t, zcodeAttachmentKindImage, wire[0].Kind)
	assert.Equal(t, "shot.png", wire[0].Filename)
	assert.Equal(t, "image/png", wire[0].MimeType)
	assert.Equal(t, len(data), wire[0].SizeBytes)
	assert.NotEqual(t, len(wire[0].DataBase64), wire[0].SizeBytes,
		"sizeBytes is the decoded length, not the encoded one")

	decoded, err := base64.StdEncoding.DecodeString(wire[0].DataBase64)
	require.NoError(t, err)
	assert.Equal(t, data, decoded, "the bytes must survive the round trip unchanged")
}

// The app-server ACCEPTS an image a text-only model cannot read, and the image never
// reaches the model -- so the user would see a confident answer about an image the
// model never saw. The refusal identifies the model, because switching it is the remedy.
func TestBuildZCodeInput_ImageToATextOnlyModelIsRefused(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.ControlSink{}))
	a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)

	_, _, err := a.buildZCodeInput("look", []*leapmuxv1.Attachment{
		zcodeAttachment("shot.png", "image/png", []byte("bytes")),
	}, "builtin:zai/GLM-5.3")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "builtin:zai/GLM-5.3", "the message must name the model to switch away from")
	assert.Contains(t, err.Error(), "shot.png")
}

// With no model pinned the app-server picks one and its capabilities are unknown
// here. Sending is the better failure: an ignored image is recoverable, and refusing
// every image on an unpinned session is not.
func TestBuildZCodeInput_ImageOnAnUnpinnedModelIsSent(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.ControlSink{}))
	a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)

	_, wire, err := a.buildZCodeInput("look", []*leapmuxv1.Attachment{
		zcodeAttachment("shot.png", "image/png", []byte("bytes")),
	}, "")
	require.NoError(t, err)
	assert.Len(t, wire, 1)
}

// A model the catalog does not hold declares no modality, so it reads as text-only.
// Refusing is right: a model the configuration lost cannot be shown to accept images.
func TestBuildZCodeInput_ImageToAnUnknownModelIsRefused(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.ControlSink{}))
	a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)

	_, _, err := a.buildZCodeInput("look", []*leapmuxv1.Attachment{
		zcodeAttachment("shot.png", "image/png", []byte("bytes")),
	}, "gone/model")
	require.Error(t, err)
}

// A PDF travels as the normalizer's `pdf` kind with its bytes INLINE, which the
// app-server gives the model as a file part.
func TestBuildZCodeInput_PDFTravelsAsAWireAttachment(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.ControlSink{}))
	a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)
	data := []byte("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")

	text, wire, err := a.buildZCodeInput("read this", []*leapmuxv1.Attachment{
		zcodeAttachment("spec.pdf", "application/pdf", data),
	}, "builtin:zai/GLM-5.3-Flash")
	require.NoError(t, err)
	assert.Equal(t, "read this", text, "a PDF does not touch the prompt text")
	require.Len(t, wire, 1)
	assert.Equal(t, zcodeAttachmentKindPDF, wire[0].Kind)
	assert.Equal(t, "spec.pdf", wire[0].Filename)
	assert.Equal(t, "application/pdf", wire[0].MimeType)
	assert.Equal(t, len(data), wire[0].SizeBytes)
	assert.Empty(t, wire[0].TextContent)

	decoded, err := base64.StdEncoding.DecodeString(wire[0].DataBase64)
	require.NoError(t, err)
	assert.Equal(t, data, decoded, "the bytes must survive the round trip unchanged")
}

// The app-server ACCEPTS a PDF that the model declares no PDF input for, and
// replaces it with a note that the media was omitted. The refusal identifies the
// model, because switching it is the remedy.
func TestBuildZCodeInput_PDFToAModelWithoutPDFInputIsRefused(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.ControlSink{}))
	a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)

	_, _, err := a.buildZCodeInput("read", []*leapmuxv1.Attachment{
		zcodeAttachment("spec.pdf", "application/pdf", []byte("%PDF-1.7")),
	}, "builtin:zai/GLM-5.3")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "builtin:zai/GLM-5.3", "the message must name the model to switch away from")
	assert.Contains(t, err.Error(), "spec.pdf")
	assert.Contains(t, err.Error(), "does not accept PDF attachments")
	assert.Contains(t, err.Error(), "declares PDF input")
}

// The live model list is the app-server's own reading of a model, so it wins over the
// configuration: a live model that declares images but no PDF takes the image and
// refuses the PDF, although its configuration declares both.
func TestBuildZCodeInput_TheLiveModelListDecidesEachKind(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.ControlSink{}))
	a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)
	setZCodeLiveModalities(a, "builtin:zai/GLM-5.3-Flash", zcodeModalityText, zcodeModalityImage)

	_, wire, err := a.buildZCodeInput("look", []*leapmuxv1.Attachment{
		zcodeAttachment("shot.png", "image/png", []byte("bytes")),
	}, "builtin:zai/GLM-5.3-Flash")
	require.NoError(t, err)
	assert.Len(t, wire, 1)

	_, _, err = a.buildZCodeInput("read", []*leapmuxv1.Attachment{
		zcodeAttachment("spec.pdf", "application/pdf", []byte("%PDF-1.7")),
	}, "builtin:zai/GLM-5.3-Flash")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not accept PDF attachments")
}

// With no model pinned, a PDF is sent for the same reason an image is.
func TestBuildZCodeInput_PDFOnAnUnpinnedModelIsSent(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.ControlSink{}))
	a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)

	_, wire, err := a.buildZCodeInput("read", []*leapmuxv1.Attachment{
		zcodeAttachment("spec.pdf", "application/pdf", []byte("%PDF-1.7")),
	}, "")
	require.NoError(t, err)
	require.Len(t, wire, 1)
	assert.Equal(t, zcodeAttachmentKindPDF, wire[0].Kind)
}

// The cap is the app-server's own upload limit. A payload it would refuse on one path
// is not worth building on the other, and the boundary itself must be inclusive: the
// limit is a size that WORKS, and one byte more is what fails.
func TestBuildZCodeInput_TotalAttachmentBytesAreCapped(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		size    int
		refused bool
	}{
		"exactly at the cap": {zcodeMaxAttachmentBytes, false},
		"one byte over":      {zcodeMaxAttachmentBytes + 1, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.ControlSink{}))
			a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)

			_, wire, err := a.buildZCodeInput("look", []*leapmuxv1.Attachment{
				zcodeAttachment("big.png", "image/png", bytes.Repeat([]byte{1}, tc.size)),
			}, "builtin:zai/GLM-5.3-Flash")
			if tc.refused {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "limit")
				return
			}
			require.NoError(t, err)
			require.Len(t, wire, 1)
			assert.Equal(t, tc.size, wire[0].SizeBytes)
		})
	}
}

// The cap is on the TOTAL, so two files that each fit but together do not are
// refused, whichever kinds they are. Counting them one at a time would let an
// arbitrarily large send through.
func TestBuildZCodeInput_TheCapCountsEveryFileTogether(t *testing.T) {
	t.Parallel()

	half := bytes.Repeat([]byte{1}, zcodeMaxAttachmentBytes/2+1)
	cases := map[string][]*leapmuxv1.Attachment{
		"two images": {
			zcodeAttachment("a.png", "image/png", half),
			zcodeAttachment("b.png", "image/png", half),
		},
		"an image and a PDF": {
			zcodeAttachment("a.png", "image/png", half),
			zcodeAttachment("b.pdf", "application/pdf", half),
		},
	}
	for name, attachments := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.ControlSink{}))
			a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)

			_, _, err := a.buildZCodeInput("look", attachments, "builtin:zai/GLM-5.3-Flash")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "limit")
		})
	}
}

// A zero-byte image is a real upload: it declares zero bytes and carries an empty
// body, rather than being silently dropped or read as an absent attachment.
func TestBuildZCodeInput_AZeroByteImageIsStillSent(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.ControlSink{}))
	a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)

	_, wire, err := a.buildZCodeInput("look", []*leapmuxv1.Attachment{
		zcodeAttachment("empty.png", "image/png", nil),
	}, "builtin:zai/GLM-5.3-Flash")
	require.NoError(t, err)
	require.Len(t, wire, 1)
	assert.Equal(t, 0, wire[0].SizeBytes)
	assert.Empty(t, wire[0].DataBase64)
}

// A mixed send keeps each kind on its own route, in the order the user attached them.
func TestBuildZCodeInput_MixedKindsTakeTheirOwnRoutes(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.ControlSink{}))
	a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)

	text, wire, err := a.buildZCodeInput("both", []*leapmuxv1.Attachment{
		zcodeAttachment("a.txt", "text/plain", []byte("alpha")),
		zcodeAttachment("one.png", "image/png", []byte("1")),
		zcodeAttachment("spec.pdf", "application/pdf", []byte("%PDF-1.7")),
		zcodeAttachment("b.txt", "text/plain", []byte("beta")),
		zcodeAttachment("two.png", "image/png", []byte("2")),
	}, "builtin:zai/GLM-5.3-Flash")
	require.NoError(t, err)

	assert.Contains(t, text, "alpha")
	assert.Contains(t, text, "beta")
	assert.Less(t, strings.Index(text, "alpha"), strings.Index(text, "beta"),
		"the inlined blocks keep the order the user attached them")
	assert.NotContains(t, text, "%PDF", "a PDF is not inlined into the prompt")

	require.Len(t, wire, 3)
	assert.Equal(t, []string{"one.png", "spec.pdf", "two.png"},
		[]string{wire[0].Filename, wire[1].Filename, wire[2].Filename})
	assert.Equal(t, []string{zcodeAttachmentKindImage, zcodeAttachmentKindPDF, zcodeAttachmentKindImage},
		[]string{wire[0].Kind, wire[1].Kind, wire[2].Kind})
}

// No attachment at all must leave the prompt byte-identical: an empty list is not the
// same as an attachment that contributed nothing.
func TestBuildZCodeInput_NoAttachmentsLeaveThePromptUntouched(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.ControlSink{}))

	for _, attachments := range [][]*leapmuxv1.Attachment{nil, {}, {nil}} {
		text, wire, err := a.buildZCodeInput("just text", attachments, "builtin:zai/GLM-5.3")
		require.NoError(t, err)
		assert.Equal(t, "just text", text)
		assert.Empty(t, wire)
	}
}

// The refusal must happen BEFORE anything is built, so a rejected send leaves no
// partial payload for a caller to send by mistake.
func TestBuildZCodeInput_ARefusalReturnsNoPartialPayload(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		refused *leapmuxv1.Attachment
		// imageOnly makes the live model read images and no PDF.
		imageOnly bool
	}{
		"a binary file": {refused: zcodeAttachment("archive.bin", "application/octet-stream", []byte{0xff, 0x00})},
		"a PDF on a model that reads images and no PDF": {
			refused:   zcodeAttachment("spec.pdf", "application/pdf", []byte("%PDF")),
			imageOnly: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.ControlSink{}))
			a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)
			if tc.imageOnly {
				setZCodeLiveModalities(a, "builtin:zai/GLM-5.3-Flash", zcodeModalityText, zcodeModalityImage)
			}

			text, wire, err := a.buildZCodeInput("mixed", []*leapmuxv1.Attachment{
				zcodeAttachment("fine.png", "image/png", []byte("1")),
				tc.refused,
			}, "builtin:zai/GLM-5.3-Flash")
			require.Error(t, err)
			assert.Empty(t, text)
			assert.Empty(t, wire)
		})
	}
}

// setZCodeLiveModalities makes the live model list hold one model that reads the
// modalities.
func setZCodeLiveModalities(a *Agent, model string, modalities ...string) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	a.liveModels = map[string]zcodeLiveModelRecord{model: {modalities: modalities}}
}

// SendInput refuses before it reaches the wire, so a rejected attachment never
// becomes a turn the user has to interrupt.
func TestZCodeSendInput_RefusesARejectedAttachmentWithoutSending(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)
	a.Mu.Lock()
	a.model = "builtin:zai/GLM-5.3"
	a.Mu.Unlock()

	err := a.SendInput("look", []*leapmuxv1.Attachment{
		zcodeAttachment("shot.png", "image/png", []byte("bytes")),
	})
	require.Error(t, err)
	assert.Empty(t, stdin.Frames(), "a refused attachment must not reach the app-server")
}

func TestZCodeSendInput_RefusesWithNoSession(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.Mu.Lock()
	a.sessionID = ""
	a.Mu.Unlock()

	require.Error(t, a.SendInput("hi", nil))
	assert.Empty(t, stdin.Frames())
}

// The send carries the resolved text and the attachments, and an inputId of its own
// so the app-server can correlate the turn it starts.
func TestZCodeSendInput_SendsTheResolvedPayload(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)
	a.Mu.Lock()
	a.model = "builtin:zai/GLM-5.3-Flash"
	a.Mu.Unlock()
	// Nothing answers this stdin, so the send fails at the await. The FRAME is what
	// this test is about.
	a.CancelForTest()

	_ = a.SendInput("look", []*leapmuxv1.Attachment{
		zcodeAttachment("shot.png", "image/png", []byte("bytes")),
		zcodeAttachment("notes.txt", "text/plain", []byte("inline me")),
		zcodeAttachment("spec.pdf", "application/pdf", []byte("%PDF-1.7")),
	})

	requests := stdin.Requests(t)
	require.Len(t, requests, 1)
	assert.Equal(t, MethodSessionSend, requests[0].Method)

	var params struct {
		SessionID   string                `json:"sessionId"`
		Content     string                `json:"content"`
		InputID     string                `json:"inputId"`
		Attachments []zcodeWireAttachment `json:"attachments"`
	}
	require.NoError(t, json.Unmarshal(requests[0].Params, &params))
	assert.Equal(t, "sess-1", params.SessionID)
	assert.Contains(t, params.Content, "look")
	assert.Contains(t, params.Content, "inline me")
	assert.NotEmpty(t, params.InputID, "the app-server correlates the turn by this id")
	require.Len(t, params.Attachments, 2)
	assert.Equal(t, "shot.png", params.Attachments[0].Filename)
	assert.Equal(t, zcodeAttachmentKindImage, params.Attachments[0].Kind)
	assert.Equal(t, "spec.pdf", params.Attachments[1].Filename)
	assert.Equal(t, zcodeAttachmentKindPDF, params.Attachments[1].Kind)
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("%PDF-1.7")), params.Attachments[1].DataBase64)
}

// A send with no attachment omits the key entirely rather than sending an empty
// array, which the app-server would still walk.
func TestZCodeSendInput_OmitsTheAttachmentsKeyWhenThereAreNone(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.CancelForTest()

	_ = a.SendInput("hi", nil)

	requests := stdin.Requests(t)
	require.Len(t, requests, 1)
	var params map[string]any
	require.NoError(t, json.Unmarshal(requests[0].Params, &params))
	assert.NotContains(t, params, "attachments")
}
