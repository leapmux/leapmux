package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
)

type copilotNativeTool struct {
	Request          json.RawMessage
	Result           json.RawMessage
	Started          json.RawMessage
	Finished         json.RawMessage
	ToolName         string
	AgentID          string
	ParentToolCallID string
	Arguments        json.RawMessage
}

func copilotToolStorePath(sessionID, workingDir string) string {
	if sessionID == "" || sessionID == "." || sessionID == ".." || strings.ContainsAny(sessionID, "/\\\x00") {
		return ""
	}
	home := copilotHome(StoredSessionQuery{})
	if home == "" {
		return ""
	}
	if !filepath.IsAbs(home) {
		if workingDir == "" {
			return ""
		}
		home = filepath.Join(workingDir, home)
	}
	return filepath.Join(home, copilotSessionStateDirName, sessionID, "events.jsonl")
}

// Read recent events first. A request precedes its result and subagent events in this log.
// Enlarging the tail keeps old requests available without an independent persistent index.
func readCopilotNativeTool(ctx context.Context, path, toolCallID string) (*copilotNativeTool, error) {
	return readCopilotToolEvents(ctx, path, toolCallID, func(record *copilotNativeTool) bool { return record.Request != nil })
}

func readCopilotToolEvents(ctx context.Context, path, toolCallID string, complete func(*copilotNativeTool) bool) (*copilotNativeTool, error) {
	if path == "" || toolCallID == "" {
		return nil, nil
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("the Copilot event log is not a regular file")
	}
	for size := min(int64(64*1024), info.Size()); ; size = min(size*2, info.Size()) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lines, err := jsonlTail(path, size)
		if err != nil {
			return nil, err
		}
		record := &copilotNativeTool{}
		for i := len(lines) - 1; i >= 0; i-- {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			var event struct {
				Type    string `json:"type"`
				AgentID string `json:"agentId"`
				Data    struct {
					ToolCallID       string          `json:"toolCallId"`
					ToolName         string          `json:"toolName"`
					ParentToolCallID string          `json:"parentToolCallId"`
					Arguments        json.RawMessage `json:"arguments"`
				} `json:"data"`
			}
			// A provider can append the final line while the reader captures the file size.
			if json.Unmarshal(lines[i], &event) != nil || event.Data.ToolCallID != toolCallID {
				continue
			}
			switch event.Type {
			case contracts.CopilotEventToolStarted:
				record.Request = append(json.RawMessage(nil), lines[i]...)
				record.ToolName = event.Data.ToolName
				record.AgentID = event.AgentID
				record.ParentToolCallID = event.Data.ParentToolCallID
				record.Arguments = event.Data.Arguments
			case contracts.CopilotEventToolCompleted:
				if record.Result == nil {
					record.Result = append(json.RawMessage(nil), lines[i]...)
				}
			case contracts.CopilotEventSubagentStarted:
				if record.Started == nil {
					record.Started = append(json.RawMessage(nil), lines[i]...)
				}
			case contracts.CopilotEventSubagentCompleted, contracts.CopilotEventSubagentFailed:
				if record.Finished == nil {
					record.Finished = append(json.RawMessage(nil), lines[i]...)
				}
			}
			if complete(record) {
				return record, nil
			}
		}
		if size >= info.Size() {
			return nil, nil
		}
	}
}
