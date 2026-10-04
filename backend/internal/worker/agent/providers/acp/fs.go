package acp

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The ACP host supplies fs/read_text_file and fs/write_text_file.
// The agent edits the files that the host reads. Dirac's edit_file requires
// both negotiated filesystem capabilities. LeapMux has no editor buffers,
// so these handlers read and write the working tree directly.
// The same agent can run arbitrary commands through terminal/create.

type acpFSReadTextFileParams struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
}

type acpFSWriteTextFileParams struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	Content   string `json:"content"`
}

// handleFSMethod dispatches an inbound ACP filesystem JSON-RPC request.
// Both methods detach their replies from the goroutine that drains stdout.
// A child that does not read stdin must not block that goroutine.
//
// Fast Agent opens a filesystem tool call without rawInput and never revises it.
// The host request supplies the path and, for a write, the content.
// The supplement carries these arguments as a later input revision.
// The renderer can then display the file and its change.
func (b *Base) handleFSMethod(line *providerkit.ParsedLine) {
	if !line.HasID() {
		slog.Warn("acp fs method missing id", "agent_id", b.AgentID(), "method", line.Method)
		return
	}
	switch line.Method {
	case acpMethodFSReadTextFile:
		var p acpFSReadTextFileParams
		if err := json.Unmarshal(line.Params, &p); err != nil {
			b.SendErrorResponseDetached(line.ID, -32602, "invalid fs/read_text_file params: "+err.Error(), "fs error")
			return
		}
		content, err := fsReadTextFile(p.Path)
		if err != nil {
			b.sendFSError(line, p.Path, err)
			return
		}
		b.noteFSRequestInput(map[string]any{"path": p.Path})
		b.SendResponseDetached(line.ID, map[string]interface{}{"content": content}, "fs reply")
	case acpMethodFSWriteTextFile:
		var p acpFSWriteTextFileParams
		if err := json.Unmarshal(line.Params, &p); err != nil {
			b.SendErrorResponseDetached(line.ID, -32602, "invalid fs/write_text_file params: "+err.Error(), "fs error")
			return
		}
		if err := fsWriteTextFile(p.Path, p.Content); err != nil {
			b.sendFSError(line, p.Path, err)
			return
		}
		b.noteFSRequestInput(map[string]any{"path": p.Path, "content": p.Content})
		b.SendResponseDetached(line.ID, map[string]interface{}{}, "fs reply")
	}
}

// sendFSError preserves the standard ACP error that the native filesystem service recognizes.
func (b *Base) sendFSError(line *providerkit.ParsedLine, path string, err error) {
	code, message := -32603, line.Method+": "+err.Error()
	switch {
	case errors.Is(err, errFSPathRequired):
		code, message = -32602, "Invalid params: a path is required"
	case errors.Is(err, os.ErrNotExist):
		code, message = -32002, "Resource not found: "+path
	}
	b.SendErrorResponseDetached(line.ID, code, message, "fs error")
}

// noteFSRequestInput adds the filesystem arguments to the open tool call that lacks input.
func (b *Base) noteFSRequestInput(args map[string]any) {
	rawInput, err := json.Marshal(args)
	if err != nil {
		return
	}
	b.main().noteToolRequestFields(map[string]json.RawMessage{
		contracts.ACPSupplementRequestRawInput: rawInput,
	})
}

var errFSPathRequired = errors.New("a path is required")

// fsReadTextFile returns the file's text.
func fsReadTextFile(path string) (string, error) {
	if path == "" {
		return "", errFSPathRequired
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

// fsWriteTextFile replaces the file's text.
func fsWriteTextFile(path, content string) error {
	if path == "" {
		return errFSPathRequired
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
