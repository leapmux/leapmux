package codebuddy

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func TestCodebuddyUserContentKeepsPlainText(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "plain prompt", codebuddyUserContent("plain prompt", nil))
	assert.Equal(t, "plain prompt", codebuddyUserContent("plain prompt", []*leapmuxv1.Attachment{nil}))
	assert.Equal(t, "", codebuddyUserContent("", nil))
}

func TestCodebuddyUserContentCarriesEveryAttachmentKind(t *testing.T) {
	t.Parallel()

	attachments := []*leapmuxv1.Attachment{
		{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("file content")},
		{Filename: "shot.png", MimeType: "image/png", Data: []byte("\x89PNG")},
		{Filename: "report.pdf", MimeType: "application/pdf", Data: []byte("%PDF")},
		{Filename: "blob.bin", MimeType: "application/octet-stream", Data: []byte{0, 255}},
	}
	raw, err := json.Marshal(codebuddyUserContent("inspect these files", attachments))
	require.NoError(t, err)
	assert.JSONEq(t, `[
      {"type":"text","text":"<attached-file name=\"notes.txt\" mime-type=\"text/plain\">\nfile content\n</attached-file>"},
      {"type":"text","text":"Attached file \"shot.png\" (image/png)"},
      {"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw=="}},
      {"type":"text","text":"Attached file \"report.pdf\" (application/pdf)"},
      {"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERg=="}},
      {"type":"text","text":"Attached file \"blob.bin\" (application/octet-stream)"},
      {"type":"document","source":{"type":"base64","media_type":"application/octet-stream","data":"AP8="}},
      {"type":"text","text":"inspect these files"}
    ]`, string(raw))
}

func TestCodebuddyUserContentKeepsEmptyAndInvalidUTF8Files(t *testing.T) {
	t.Parallel()

	attachments := []*leapmuxv1.Attachment{
		{Filename: "empty.txt", MimeType: "text/plain"},
		{Filename: "invalid.txt", MimeType: "text/plain", Data: []byte{0xff}},
	}
	raw, err := json.Marshal(codebuddyUserContent("", attachments))
	require.NoError(t, err)
	assert.JSONEq(t, `[
      {"type":"text","text":"<attached-file name=\"empty.txt\" mime-type=\"text/plain\">\n\n</attached-file>"},
      {"type":"text","text":"Attached file \"invalid.txt\" (text/plain)"},
      {"type":"document","source":{"type":"base64","media_type":"text/plain","data":"/w=="}}
    ]`, string(raw))
}

// CodeBuddy takes the LAST plain text block of a user message as the words of
// the user. These cases state the content that each input shape must send, so
// that the prompt text is that last block.
const (
	codebuddyOrderPrompt = "Inspect these files."

	codebuddyOrderPNGLabel   = `{"type":"text","text":"Attached file \"shot.png\" (image/png)"}`
	codebuddyOrderPNGBlock   = `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}`
	codebuddyOrderPDFLabel   = `{"type":"text","text":"Attached file \"report.pdf\" (application/pdf)"}`
	codebuddyOrderPDFBlock   = `{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0xLjcK"}}`
	codebuddyOrderBlobLabel  = `{"type":"text","text":"Attached file \"blob.bin\" (application/octet-stream)"}`
	codebuddyOrderBlobBlock  = `{"type":"document","source":{"type":"base64","media_type":"application/octet-stream","data":"AP8="}}`
	codebuddyOrderNotesBlock = `{"type":"text","text":"<attached-file name=\"notes.txt\" mime-type=\"text/plain\">\nline one\nline two\n</attached-file>"}`
	codebuddyOrderPromptText = `{"type":"text","text":"` + codebuddyOrderPrompt + `"}`
)

func codebuddyOrderPNG() *leapmuxv1.Attachment {
	return &leapmuxv1.Attachment{Filename: "shot.png", MimeType: "image/png", Data: []byte("\x89PNG\r\n\x1a\n")}
}

func codebuddyOrderPDF() *leapmuxv1.Attachment {
	return &leapmuxv1.Attachment{Filename: "report.pdf", MimeType: "application/pdf", Data: []byte("%PDF-1.7\n")}
}

func codebuddyOrderBlob() *leapmuxv1.Attachment {
	return &leapmuxv1.Attachment{Filename: "blob.bin", MimeType: "application/octet-stream", Data: []byte{0, 255}}
}

func codebuddyOrderNotes() *leapmuxv1.Attachment {
	return &leapmuxv1.Attachment{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("line one\nline two\n")}
}

// codebuddyUserContentCase is one input shape and the content JSON that
// CodeBuddy must receive for it. A user frame carries wantContent as its
// message.content. A steer frame always carries a block array, so it carries
// wantBlocks, which is wantContent unless the case sends plain text.
//
// steerRefused marks a shape that SteerInput refuses, because the steer drain
// of CodeBuddy keeps the text of the blocks only. The queue then sends the
// input as the next turn.
type codebuddyUserContentCase struct {
	name         string
	content      string
	attachments  []*leapmuxv1.Attachment
	wantContent  string
	wantBlocks   string
	steerRefused bool
}

func (tc codebuddyUserContentCase) steerBlocks() string {
	if tc.wantBlocks != "" {
		return tc.wantBlocks
	}
	return tc.wantContent
}

func codebuddyUserContentCases() []codebuddyUserContentCase {
	return []codebuddyUserContentCase{
		{
			name:        "text only stays a plain string",
			content:     codebuddyOrderPrompt,
			wantContent: `"` + codebuddyOrderPrompt + `"`,
			wantBlocks:  `[` + codebuddyOrderPromptText + `]`,
		},
		{
			name:        "nil attachments only stay a plain string",
			content:     codebuddyOrderPrompt,
			attachments: []*leapmuxv1.Attachment{nil, nil},
			wantContent: `"` + codebuddyOrderPrompt + `"`,
			wantBlocks:  `[` + codebuddyOrderPromptText + `]`,
		},
		{
			name:         "an image and its label come before the prompt text",
			content:      codebuddyOrderPrompt,
			attachments:  []*leapmuxv1.Attachment{codebuddyOrderPNG()},
			wantContent:  `[` + codebuddyOrderPNGLabel + `,` + codebuddyOrderPNGBlock + `,` + codebuddyOrderPromptText + `]`,
			steerRefused: true,
		},
		{
			name:         "a document and its label come before the prompt text",
			content:      codebuddyOrderPrompt,
			attachments:  []*leapmuxv1.Attachment{codebuddyOrderPDF()},
			wantContent:  `[` + codebuddyOrderPDFLabel + `,` + codebuddyOrderPDFBlock + `,` + codebuddyOrderPromptText + `]`,
			steerRefused: true,
		},
		{
			name:         "a binary file and its label come before the prompt text",
			content:      codebuddyOrderPrompt,
			attachments:  []*leapmuxv1.Attachment{codebuddyOrderBlob()},
			wantContent:  `[` + codebuddyOrderBlobLabel + `,` + codebuddyOrderBlobBlock + `,` + codebuddyOrderPromptText + `]`,
			steerRefused: true,
		},
		{
			name:        "a text attachment comes before the prompt text",
			content:     codebuddyOrderPrompt,
			attachments: []*leapmuxv1.Attachment{codebuddyOrderNotes()},
			wantContent: `[` + codebuddyOrderNotesBlock + `,` + codebuddyOrderPromptText + `]`,
		},
		{
			name:    "mixed attachments keep the attached order and drop nil entries",
			content: codebuddyOrderPrompt,
			attachments: []*leapmuxv1.Attachment{
				codebuddyOrderNotes(), codebuddyOrderPNG(), nil, codebuddyOrderPDF(), codebuddyOrderBlob(),
			},
			wantContent: `[` + strings.Join([]string{
				codebuddyOrderNotesBlock,
				codebuddyOrderPNGLabel, codebuddyOrderPNGBlock,
				codebuddyOrderPDFLabel, codebuddyOrderPDFBlock,
				codebuddyOrderBlobLabel, codebuddyOrderBlobBlock,
				codebuddyOrderPromptText,
			}, ",") + `]`,
			steerRefused: true,
		},
		{
			name:         "an image without prompt text sends no text block of its own",
			attachments:  []*leapmuxv1.Attachment{codebuddyOrderPNG()},
			wantContent:  `[` + codebuddyOrderPNGLabel + `,` + codebuddyOrderPNGBlock + `]`,
			steerRefused: true,
		},
		{
			name:        "a text attachment without prompt text sends no prompt block",
			attachments: []*leapmuxv1.Attachment{codebuddyOrderNotes()},
			wantContent: `[` + codebuddyOrderNotesBlock + `]`,
		},
	}
}

// newCodebuddyContentAgent returns an idle agent whose stdin records every
// frame.
func newCodebuddyContentAgent(t *testing.T) (*Agent, *agenttest.Stdin) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	// A fake process never calls Wait, so Stop needs a closed exit channel.
	processDone := make(chan struct{})
	close(processDone)
	stdin := &agenttest.Stdin{}
	a := &Agent{
		Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID: "test-agent", ProviderName: "codebuddy", Ctx: ctx, Cancel: cancel,
			Stdin: stdin, ProcessDone: processDone,
		}),
		sink:      agent.NewProviderServices(&agenttest.Sink{}),
		sessionID: "session-1",
	}
	t.Cleanup(a.Process.Stop)
	return a, stdin
}

// oneCodebuddyFrame asserts that the agent wrote exactly one NDJSON line, and
// returns it.
func oneCodebuddyFrame(t *testing.T, written string) string {
	t.Helper()
	require.True(t, strings.HasSuffix(written, "\n"), "a frame must end with a newline: %q", written)
	lines := strings.Split(strings.TrimSuffix(written, "\n"), "\n")
	require.Len(t, lines, 1, "the agent must write exactly one frame")
	return lines[0]
}

func TestCodebuddySendInputPutsThePromptTextLast(t *testing.T) {
	t.Parallel()
	for _, tc := range codebuddyUserContentCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, stdin := newCodebuddyContentAgent(t)
			require.NoError(t, a.SendInput(tc.content, tc.attachments))
			assert.JSONEq(t, `{"type":"user","message":{"role":"user","content":`+tc.wantContent+`}}`,
				oneCodebuddyFrame(t, stdin.String()))
		})
	}
}

func TestCodebuddySteerInputPutsThePromptTextLast(t *testing.T) {
	t.Parallel()
	for _, tc := range codebuddyUserContentCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, peer := newCodebuddySteerAgent(t, nil, codebuddyControlAnswer(codebuddySteered))
			err := a.SteerInput(tc.content, tc.attachments)
			if tc.steerRefused {
				require.ErrorIs(t, err, agent.ErrSteeringUnsupported,
					"the queue keeps the input, files and all, for the next turn")
				assert.Empty(t, peer.String(), "a refused steer writes nothing")
				return
			}
			require.NoError(t, err)

			var frame struct {
				Type      string `json:"type"`
				RequestID string `json:"request_id"`
				Request   struct {
					Subtype       string          `json:"subtype"`
					SessionID     string          `json:"session_id"`
					ContentBlocks json.RawMessage `json:"content_blocks"`
				} `json:"request"`
			}
			require.NoError(t, json.Unmarshal([]byte(oneCodebuddyFrame(t, peer.String())), &frame))
			assert.Equal(t, "control_request", frame.Type)
			assert.NotEmpty(t, frame.RequestID)
			assert.Equal(t, "steer", frame.Request.Subtype)
			assert.Equal(t, "session-1", frame.Request.SessionID)
			assert.JSONEq(t, tc.steerBlocks(), string(frame.Request.ContentBlocks))
		})
	}
}
