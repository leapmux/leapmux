package claude

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Claude Code reads the prompt of an array message from its LAST block, and
// only when that block is text. These cases state the content that each input
// shape must put on stdin, so that the prompt text is that last block.
const (
	claudeOrderPrompt = "Summarize the attached files."

	claudeOrderPNGBlock      = `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}`
	claudeOrderGIFBlock      = `{"type":"image","source":{"type":"base64","media_type":"image/gif","data":"R0lGODlh"}}`
	claudeOrderPDFBlock      = `{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0xLjcK"}}`
	claudeOrderNotesBlock    = `{"type":"text","text":"<attached-file name=\"notes.txt\" mime-type=\"text/plain\">\nline one\nline two\n</attached-file>"}`
	claudeOrderPromptBlock   = `{"type":"text","text":"` + claudeOrderPrompt + `"}`
	claudeOrderPromptContent = `"` + claudeOrderPrompt + `"`
)

func claudeOrderPNG() *leapmuxv1.Attachment {
	return &leapmuxv1.Attachment{Filename: "shot.png", MimeType: "image/png", Data: []byte("\x89PNG\r\n\x1a\n")}
}

func claudeOrderGIF() *leapmuxv1.Attachment {
	return &leapmuxv1.Attachment{Filename: "anim.gif", MimeType: "image/gif", Data: []byte("GIF89a")}
}

func claudeOrderPDF() *leapmuxv1.Attachment {
	return &leapmuxv1.Attachment{Filename: "report.pdf", MimeType: "application/pdf", Data: []byte("%PDF-1.7\n")}
}

func claudeOrderNotes() *leapmuxv1.Attachment {
	return &leapmuxv1.Attachment{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("line one\nline two\n")}
}

// claudeUserContentCase is one input shape and the message.content JSON that
// Claude Code must receive for it.
//
// steerRefused marks a shape that SteerInput refuses. Claude Code folds a steer
// into the running turn as a queued command, and that fold keeps only the text
// blocks and the image blocks. The worker then sends the input as the next
// turn.
type claudeUserContentCase struct {
	name         string
	content      string
	attachments  []*leapmuxv1.Attachment
	wantContent  string
	steerRefused bool
}

func claudeUserContentCases() []claudeUserContentCase {
	return []claudeUserContentCase{
		{
			name:        "text only stays a plain string",
			content:     claudeOrderPrompt,
			wantContent: claudeOrderPromptContent,
		},
		{
			name:        "nil attachments only stay a plain string",
			content:     claudeOrderPrompt,
			attachments: []*leapmuxv1.Attachment{nil, nil},
			wantContent: claudeOrderPromptContent,
		},
		{
			name:        "an empty prompt without attachments stays an empty string",
			wantContent: `""`,
		},
		{
			name:        "an image comes before the prompt text",
			content:     claudeOrderPrompt,
			attachments: []*leapmuxv1.Attachment{claudeOrderPNG()},
			wantContent: `[` + claudeOrderPNGBlock + `,` + claudeOrderPromptBlock + `]`,
		},
		{
			name:        "two images keep their order before the prompt text",
			content:     claudeOrderPrompt,
			attachments: []*leapmuxv1.Attachment{claudeOrderPNG(), claudeOrderGIF()},
			wantContent: `[` + claudeOrderPNGBlock + `,` + claudeOrderGIFBlock + `,` + claudeOrderPromptBlock + `]`,
		},
		{
			name:         "a document comes before the prompt text",
			content:      claudeOrderPrompt,
			attachments:  []*leapmuxv1.Attachment{claudeOrderPDF()},
			wantContent:  `[` + claudeOrderPDFBlock + `,` + claudeOrderPromptBlock + `]`,
			steerRefused: true,
		},
		{
			name:        "a text attachment comes before the prompt text",
			content:     claudeOrderPrompt,
			attachments: []*leapmuxv1.Attachment{claudeOrderNotes()},
			wantContent: `[` + claudeOrderNotesBlock + `,` + claudeOrderPromptBlock + `]`,
		},
		{
			name:         "mixed attachments keep the attached order and drop nil entries",
			content:      claudeOrderPrompt,
			attachments:  []*leapmuxv1.Attachment{claudeOrderPNG(), nil, claudeOrderNotes(), claudeOrderPDF()},
			wantContent:  `[` + claudeOrderPNGBlock + `,` + claudeOrderNotesBlock + `,` + claudeOrderPDFBlock + `,` + claudeOrderPromptBlock + `]`,
			steerRefused: true,
		},
		{
			name:        "a text attachment and images keep the attached order",
			content:     claudeOrderPrompt,
			attachments: []*leapmuxv1.Attachment{claudeOrderNotes(), claudeOrderPNG(), claudeOrderGIF()},
			wantContent: `[` + claudeOrderNotesBlock + `,` + claudeOrderPNGBlock + `,` + claudeOrderGIFBlock + `,` + claudeOrderPromptBlock + `]`,
		},
		{
			name:        "an image without prompt text sends no text block",
			attachments: []*leapmuxv1.Attachment{claudeOrderPNG()},
			wantContent: `[` + claudeOrderPNGBlock + `]`,
		},
		{
			name:        "a text attachment without prompt text sends no prompt block",
			attachments: []*leapmuxv1.Attachment{claudeOrderNotes()},
			wantContent: `[` + claudeOrderNotesBlock + `]`,
		},
	}
}

// newClaudeContentAgent returns an agent whose stdin records every frame.
func newClaudeContentAgent(t *testing.T, turnActive bool) (*Agent, *agenttest.Stdin) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	// A fake process never calls Wait, so Stop needs a closed exit channel.
	processDone := make(chan struct{})
	close(processDone)
	stdin := &agenttest.Stdin{}
	a := &Agent{
		Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID: "test-agent", ProviderName: "claude", Ctx: ctx, Cancel: cancel,
			Stdin: stdin, ProcessDone: processDone,
		}),
		sink:       agent.NewProviderServices(&agenttest.Sink{}),
		turnActive: turnActive,
	}
	t.Cleanup(a.Process.Stop)
	return a, stdin
}

// assertOneClaudeFrame asserts that the agent wrote exactly one NDJSON line,
// and that the line equals want.
func assertOneClaudeFrame(t *testing.T, written, want string) {
	t.Helper()
	require.True(t, strings.HasSuffix(written, "\n"), "a frame must end with a newline: %q", written)
	lines := strings.Split(strings.TrimSuffix(written, "\n"), "\n")
	require.Len(t, lines, 1, "the agent must write exactly one frame")
	assert.JSONEq(t, want, lines[0])
}

func TestClaudeSendInputPutsThePromptTextLast(t *testing.T) {
	t.Parallel()
	for _, tc := range claudeUserContentCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, stdin := newClaudeContentAgent(t, false)
			require.NoError(t, a.SendInput(tc.content, tc.attachments))
			assertOneClaudeFrame(t, stdin.String(),
				`{"type":"user","message":{"role":"user","content":`+tc.wantContent+`}}`)
		})
	}
}

func TestClaudeSteerInputPutsThePromptTextLast(t *testing.T) {
	t.Parallel()
	for _, tc := range claudeUserContentCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, stdin := newClaudeContentAgent(t, true)
			err := a.SteerInput(tc.content, tc.attachments)
			if tc.steerRefused {
				require.ErrorIs(t, err, agent.ErrSteeringUnsupported,
					"the worker keeps the input, document and all, for the next turn")
				assert.Empty(t, stdin.String(), "a refused steer writes nothing")
				return
			}
			require.NoError(t, err)
			assertOneClaudeFrame(t, stdin.String(),
				`{"type":"user","message":{"role":"user","content":`+tc.wantContent+`},"priority":"next"}`)
		})
	}
}

// TestClaudeSteerInputRefusesWhatTheFoldDrops pins the refusal of each
// attachment kind that Claude Code's queued-command fold drops. The fold keeps
// the text blocks and the image blocks only (the queued_command attachment of
// 2.1.289). ValidateAttachment refuses a binary file before it reaches here.
// The refusal covers it all the same, because the fold would drop it too.
func TestClaudeSteerInputRefusesWhatTheFoldDrops(t *testing.T) {
	t.Parallel()
	blob := &leapmuxv1.Attachment{Filename: "blob.bin", MimeType: "application/octet-stream", Data: []byte{0, 255}}
	for _, tc := range []struct {
		name        string
		content     string
		attachments []*leapmuxv1.Attachment
	}{
		{name: "a PDF", content: claudeOrderPrompt, attachments: []*leapmuxv1.Attachment{claudeOrderPDF()}},
		{name: "a PDF without prompt text", attachments: []*leapmuxv1.Attachment{claudeOrderPDF()}},
		{name: "a PDF after an image", content: claudeOrderPrompt, attachments: []*leapmuxv1.Attachment{claudeOrderPNG(), claudeOrderPDF()}},
		{name: "a binary file", content: claudeOrderPrompt, attachments: []*leapmuxv1.Attachment{blob}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, stdin := newClaudeContentAgent(t, true)
			require.ErrorIs(t, a.SteerInput(tc.content, tc.attachments), agent.ErrSteeringUnsupported)
			assert.Empty(t, stdin.String(), "a refused steer writes nothing")
		})
	}
}

// TestClaudeSteerInputChecksTheFilesBeforeTheTurn pins the order of the two
// refusals. SteerInput refuses a steer that the fold cannot carry as
// unsupported also when no turn runs, so that answer depends on the input
// alone.
func TestClaudeSteerInputChecksTheFilesBeforeTheTurn(t *testing.T) {
	t.Parallel()
	a, stdin := newClaudeContentAgent(t, false)
	assert.ErrorIs(t, a.SteerInput(claudeOrderPrompt, []*leapmuxv1.Attachment{claudeOrderPDF()}), agent.ErrSteeringUnsupported)
	assert.ErrorIs(t, a.SteerInput(claudeOrderPrompt, []*leapmuxv1.Attachment{claudeOrderPNG()}), agent.ErrNoActiveTurn)
	assert.Empty(t, stdin.String())
}
