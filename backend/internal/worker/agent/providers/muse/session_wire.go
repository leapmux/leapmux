package muse

import "encoding/json"

type frame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type initializeResult struct {
	ServerInfo struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"serverInfo"`
	MuseHome            string   `json:"museHome"`
	GrantedCapabilities []string `json:"grantedCapabilities"`
	ExperimentalAPI     bool     `json:"experimentalApi"`
	SessionDurability   string   `json:"sessionDurability"`
	Schema              struct {
		Version     int    `json:"version"`
		Fingerprint string `json:"fingerprint"`
	} `json:"schema"`
}

type nativePosition struct {
	ID       string `json:"id"`
	Sequence int64  `json:"sequence"`
}

type nativeStream struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type nativeSourceRange struct {
	Stream nativeStream   `json:"stream"`
	First  nativePosition `json:"first"`
	Last   nativePosition `json:"last"`
}

type nativeSession struct {
	ID              string          `json:"sessionId"`
	Path            string          `json:"path"`
	Status          string          `json:"status"`
	ActiveTurnID    *string         `json:"activeTurnId"`
	WorkspaceRoot   *string         `json:"workspaceRoot"`
	ProviderID      *string         `json:"providerId"`
	ModelID         *string         `json:"modelId"`
	Name            string          `json:"name"`
	Title           string          `json:"title"`
	FirstUserPrompt string          `json:"firstUserPrompt"`
	CreatedAt       string          `json:"createdAt"`
	UpdatedAt       string          `json:"updatedAt"`
	LastActivityAt  json.RawMessage `json:"lastActivityAt"`
	Kind            string          `json:"kind"`
	ParentSessionID string          `json:"parentSessionId"`
	ApprovalMode    *struct {
		Mode string `json:"mode"`
	} `json:"approvalMode"`
}

type sessionResult struct {
	Session         nativeSession   `json:"session"`
	ViewCursor      string          `json:"viewCursor"`
	History         json.RawMessage `json:"history"`
	PendingRequests json.RawMessage `json:"pendingRequests"`
}

type nativeOutputReference struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Availability string `json:"availability"`
	ByteLength   int64  `json:"byteLen"`
	MediaType    string `json:"mediaType"`
	Path         string `json:"path"`
	URI          string `json:"uri"`
}

type nativeItem struct {
	ID                  string                 `json:"itemId"`
	Kind                string                 `json:"kind"`
	TurnID              *string                `json:"turnId"`
	Revision            int64                  `json:"revision"`
	Status              string                 `json:"status"`
	RecordedAt          string                 `json:"recordedAt"`
	Text                string                 `json:"text"`
	Tool                string                 `json:"tool"`
	CallID              string                 `json:"callId"`
	Args                string                 `json:"args"`
	VisibleOutput       string                 `json:"visibleOutput"`
	FailureKind         string                 `json:"failureKind"`
	FailureReason       string                 `json:"failureReason"`
	FallbackText        string                 `json:"fallbackText"`
	OutputReference     *nativeOutputReference `json:"outputRef"`
	PatchReference      *nativeOutputReference `json:"patchRef"`
	ModelVisibleContent json.RawMessage        `json:"modelVisibleContent"`
	ChildSessionID      string                 `json:"childSessionId"`
	SubagentID          string                 `json:"subagentId"`
	AgentPath           string                 `json:"agentPath"`
	Objective           string                 `json:"objective"`
	Role                string                 `json:"role"`
	ControlStatus       string                 `json:"controlStatus"`
	WorkflowRunID       string                 `json:"workflowRunId"`
	EntryID             string                 `json:"entryId"`
	Children            json.RawMessage        `json:"children"`
	Background          bool                   `json:"background"`
	TaskID              string                 `json:"taskId"`
	Outcome             string                 `json:"outcome"`
	Reason              string                 `json:"reason"`
	Trigger             string                 `json:"trigger"`
}

type itemParams struct {
	SessionID   string            `json:"sessionId"`
	ViewCursor  string            `json:"viewCursor"`
	SourceRange nativeSourceRange `json:"sourceRange"`
	Item        nativeItem        `json:"item"`
}

type turnResult struct {
	CommandID      string `json:"commandId"`
	Status         string `json:"status"`
	TurnID         string `json:"turnId"`
	Disposition    string `json:"disposition"`
	StartedNewTurn bool   `json:"startedNewTurn"`
}

type turnCompletion struct {
	SessionID string          `json:"sessionId"`
	TurnID    string          `json:"turnId"`
	Outcome   string          `json:"terminal"`
	Reason    string          `json:"reason"`
	Error     json.RawMessage `json:"error"`
}
