package acp

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The ACP host filesystem methods (`fs/read_text_file`, `fs/write_text_file`).
//
// An agent that edits through the client asks the host to read and write text
// files, so the edit lands on the same file state the host sees. Dirac's
// `edit_file` refuses to run at all without the negotiated capability ("ACP
// file editing requires negotiated fs.readTextFile and fs.writeTextFile").
// LeapMux has no editor buffers of its own, so the handlers touch the working
// tree directly. That adds no privilege the agent lacks: the same agent runs
// arbitrary commands through terminal/create.

type acpFSReadTextFileParams struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
}

type acpFSWriteTextFileParams struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	Content   string `json:"content"`
}

// handleFSMethod dispatches an inbound ACP fs/* JSON-RPC request. Both methods
// answer from the goroutine that drains the child's stdout, for the reason
// terminalError documents: a child that is not reading its stdin must never
// block the reply.
//
// The request also carries the call's arguments to the row. A filesystem
// runtime (fast-agent's) opens the tool call with no rawInput and never revises
// it, so this request is the first place the path -- and for a write, the
// content -- appears. They land as the late-input revision the supplement
// carries, so the row states its file and the renderer draws the change.
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
			b.SendErrorResponseDetached(line.ID, -32603, "fs/read_text_file: "+err.Error(), "fs error")
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
			b.SendErrorResponseDetached(line.ID, -32603, "fs/write_text_file: "+err.Error(), "fs error")
			return
		}
		b.noteFSRequestInput(map[string]any{"path": p.Path, "content": p.Content})
		b.SendResponseDetached(line.ID, map[string]interface{}{}, "fs reply")
	}
}

// noteFSRequestInput folds the arguments of an fs/* request into the open tool
// call that states none. See handleFSMethod.
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
