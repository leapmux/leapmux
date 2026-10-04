package codebuddy

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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
      {"type":"text","text":"inspect these files"},
      {"type":"text","text":"----- BEGIN ATTACHED FILE: notes.txt (text/plain) -----\nfile content\n----- END ATTACHED FILE: notes.txt -----"},
      {"type":"text","text":"Attached file \"shot.png\" (image/png)"},
      {"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw=="}},
      {"type":"text","text":"Attached file \"report.pdf\" (application/pdf)"},
      {"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERg=="}},
      {"type":"text","text":"Attached file \"blob.bin\" (application/octet-stream)"},
      {"type":"document","source":{"type":"base64","media_type":"application/octet-stream","data":"AP8="}}
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
      {"type":"text","text":"----- BEGIN ATTACHED FILE: empty.txt (text/plain) -----\n\n----- END ATTACHED FILE: empty.txt -----"},
      {"type":"text","text":"Attached file \"invalid.txt\" (text/plain)"},
      {"type":"document","source":{"type":"base64","media_type":"text/plain","data":"/w=="}}
    ]`, string(raw))
}

func TestCodebuddySteerInputCarriesAttachmentBytes(t *testing.T) {
	t.Parallel()

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
		sessionID: "session-1",
		active:    true,
	}
	t.Cleanup(a.Process.Stop)
	require.NoError(t, a.SteerInput("inspect the file", []*leapmuxv1.Attachment{{
		Filename: "blob.bin", MimeType: "application/octet-stream", Data: []byte{0, 255},
	}}))

	var frame struct {
		Type    string `json:"type"`
		Request struct {
			Subtype       string           `json:"subtype"`
			SessionID     string           `json:"session_id"`
			ContentBlocks []map[string]any `json:"content_blocks"`
		} `json:"request"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(stdin.String())), &frame))
	assert.Equal(t, "control_request", frame.Type)
	assert.Equal(t, "steer", frame.Request.Subtype)
	assert.Equal(t, "session-1", frame.Request.SessionID)
	require.Len(t, frame.Request.ContentBlocks, 3)
	assert.Equal(t, map[string]any{"type": "text", "text": "inspect the file"}, frame.Request.ContentBlocks[0])
	assert.Equal(t, map[string]any{"type": "text", "text": `Attached file "blob.bin" (application/octet-stream)`}, frame.Request.ContentBlocks[1])
	assert.Equal(t, map[string]any{
		"type":   "document",
		"source": map[string]any{"type": "base64", "media_type": "application/octet-stream", "data": "AP8="},
	}, frame.Request.ContentBlocks[2])
}
