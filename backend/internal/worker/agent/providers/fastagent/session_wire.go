package fastagent

import "encoding/json"

type fastagentSessionSnapshot struct {
	SessionID string `json:"session_id"`
	Metadata  struct {
		Extras struct {
			Label       string `json:"subagent_label"`
			TaskPreview string `json:"subagent_task_preview"`
			Ordinal     int    `json:"subagent_ordinal"`
		} `json:"extras"`
	} `json:"metadata"`
	Execution struct {
		Resumable bool `json:"resumable"`
		ChildLink *struct {
			ParentSessionID  string `json:"parent_session_id"`
			ParentToolCallID string `json:"parent_tool_call_id"`
		} `json:"child_link"`
	} `json:"execution"`
	Continuation struct {
		CWD         string `json:"cwd"`
		ActiveAgent string `json:"active_agent"`
		Agents      map[string]struct {
			HistoryFile string `json:"history_file"`
		} `json:"agents"`
	} `json:"continuation"`
}

// fastagentHistoryMessage preserves the order of native content and tool maps.
type fastagentHistoryMessage struct {
	Timestamp   string                       `json:"timestamp"`
	Role        string                       `json:"role"`
	Content     []json.RawMessage            `json:"content"`
	ToolCalls   json.RawMessage              `json:"tool_calls"`
	ToolResults json.RawMessage              `json:"tool_results"`
	Channels    map[string][]json.RawMessage `json:"channels"`
}
