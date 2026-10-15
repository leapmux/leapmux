package codex

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"runtime"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

var _ agent.AutoReviewDenialApprover = (*Agent)(nil)

// codexMethodApproveGuardianDeniedAction records the user's approval of one action
// that the auto-review reviewer denied. Only the worker sends it.
const codexMethodApproveGuardianDeniedAction = "thread/approveGuardianDeniedAction"

// The scope that a permission grant answers with. The browser's Allow grants the
// requested permissions for the turn, and a standing approval grants the same.
const codexPermissionGrantScopeTurn = "turn"

// Standing approval can grant a command or file change for this request or session.
// It never selects a policy amendment.
const (
	codexDecisionAccept           = "accept"
	codexDecisionAcceptForSession = "acceptForSession"
)

// codexStandingApprovalAnswer returns the result that the bypass preset sends for a
// permission request. ok is false for a request that only the user can answer: a
// question, a form that asks for data, and a decision request that offers no plain
// approval.
//
// The browser's Allow sends the same shapes; testdata/codex_approval_answer_conformance.json
// states both answers for each request kind, and both suites replay it.
func codexStandingApprovalAnswer(payload []byte) (answer any, ok bool) {
	var request struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if json.Unmarshal(payload, &request) != nil {
		return nil, false
	}
	switch request.Method {
	case contracts.CodexMethodCommandExecutionRequestApproval, contracts.CodexMethodFileChangeRequestApproval:
		decision, ok := codexStandingDecision(request.Params)
		if !ok {
			return nil, false
		}
		return map[string]any{"decision": decision}, true
	case contracts.CodexMethodPermissionsRequestApproval:
		return map[string]any{
			"permissions": codexRequestedPermissions(request.Params),
			"scope":       codexPermissionGrantScopeTurn,
		}, true
	case contracts.MCPElicitationMethodCodex:
		if !codexIsToolCallApprovalElicitation(request.Params) {
			return nil, false
		}
		return map[string]any{"action": contracts.MCPElicitationActionAccept, "content": map[string]any{}}, true
	}
	return nil, false
}

// codexStandingDecision returns the approval decision that a standing approval sends:
// accept when Codex offers it, else acceptForSession. A request that lists no
// decisions offers accept, as the browser also assumes.
func codexStandingDecision(params json.RawMessage) (string, bool) {
	var request struct {
		AvailableDecisions []json.RawMessage `json:"availableDecisions"`
	}
	if json.Unmarshal(params, &request) != nil {
		return "", false
	}
	if len(request.AvailableDecisions) == 0 {
		return codexDecisionAccept, true
	}
	offered := make(map[string]bool, len(request.AvailableDecisions))
	for _, raw := range request.AvailableDecisions {
		var decision string
		if json.Unmarshal(raw, &decision) == nil {
			offered[decision] = true
		}
	}
	for _, decision := range []string{codexDecisionAccept, codexDecisionAcceptForSession} {
		if offered[decision] {
			return decision, true
		}
	}
	return "", false
}

// codexRequestedPermissions returns the network and file-system permissions that a
// permissions request asks for, as the grant that approves them.
func codexRequestedPermissions(params json.RawMessage) map[string]json.RawMessage {
	var request struct {
		Permissions map[string]json.RawMessage `json:"permissions"`
	}
	granted := make(map[string]json.RawMessage)
	if json.Unmarshal(params, &request) != nil {
		return granted
	}
	for _, key := range []string{"network", "fileSystem"} {
		if value, ok := request.Permissions[key]; ok && strings.HasPrefix(strings.TrimSpace(string(value)), "{") {
			granted[key] = value
		}
	}
	return granted
}

// codexIsToolCallApprovalElicitation reports whether an MCP elicitation is Codex's
// approval of an MCP tool call: its metadata states that approval kind, and its form
// asks for no data. Any other elicitation needs the user's input.
func codexIsToolCallApprovalElicitation(params json.RawMessage) bool {
	var request struct {
		Meta struct {
			Kind string `json:"codex_approval_kind"`
		} `json:"_meta"`
		RequestedSchema *struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"requestedSchema"`
	}
	if json.Unmarshal(params, &request) != nil {
		return false
	}
	if request.Meta.Kind != contracts.MCPElicitationApprovalKindToolCall {
		return false
	}
	return request.RequestedSchema == nil || len(request.RequestedSchema.Properties) == 0
}

// publishApprovalRequest answers a permission request in the name of the bypass
// preset, or publishes it to the reader.
//
// It runs on the stdout reader, so every answer is QUEUED: a waiting stdin write
// there can deadlock both processes (see providerkit.RefuseUnsupportedRequest).
//
// When the standing approval applies, the request is answered and never published:
// native full access asks nothing either, so no card and no transcript row record it.
// Otherwise the request is registered with its approval answer, and the standing
// approval is read AGAIN after the registration, so a switch to Bypass that ran
// between the two reads still answers the request.
func (a *Agent) publishApprovalRequest(line *providerkit.ParsedLine, cancelAnswer any) {
	answer, approvable := codexStandingApprovalAnswer(line.Raw)
	if !approvable {
		a.PublishControlRequest(a.sink, line.Raw, cancelAnswer)
		return
	}
	a.Mu.Lock()
	queued := a.bypassActiveLocked() && a.ApproveUnpublishedControlRequest(a.sink, line.Raw, cancelAnswer, answer)
	a.Mu.Unlock()
	if queued {
		return
	}
	a.registerApprovalRequest(line.Raw, cancelAnswer, answer)
}

// registerApprovalRequest publishes a permission request with its approval answer, then
// reads the standing approval again. A switch to Bypass whose sweep ran before this
// registration found no record, so this second read is what answers the request.
func (a *Agent) registerApprovalRequest(raw []byte, cancelAnswer, answer any) {
	publication := a.PublishControlRequestWithApproval(a.sink, raw, cancelAnswer, answer)
	a.Mu.Lock()
	if publication.RequestID != "" && a.bypassActiveLocked() {
		a.ApproveOutstandingControlRequest(a.sink, publication)
	}
	a.Mu.Unlock()
}

// approveOutstandingForBypass answers every pending permission request when the
// standing approval applies, and records how many it answered. The reader saw those
// cards, so the transcript states why they left.
func (a *Agent) approveOutstandingForBypass() {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if !a.bypassActiveLocked() {
		return
	}
	a.ApproveOutstandingControlRequests(a.sink, func(count int) {
		if count == 0 {
			return
		}
		slog.Info("codex bypass approved pending permission requests", "agent_id", a.AgentID(), "count", count)
		a.sink.PersistLeapMuxNotification(map[string]interface{}{
			contracts.NotificationFieldType:  contracts.NotificationTypePendingRequestsApproved,
			contracts.NotificationFieldCount: count,
		})
	})
}

// SendRawInput refuses a control response whose request is no longer pending. Every
// Codex JSON-RPC control request registers in the outstanding registry, so an absent
// record means that a reader answer, a withdrawal, or a standing approval already
// closed the request, and a second answer must not reach Codex.
func (a *Agent) SendRawInput(data []byte, stop agent.StopContext) error {
	return a.SendRawInputToOutstandingRequest(data, stop)
}

// codexAutoReviewNotification is the part of an item/autoApprovalReview/completed
// notification that the approval of a denial reads.
type codexAutoReviewNotification struct {
	Method string                   `json:"method"`
	Params codexAutoReviewCompleted `json:"params"`
}

type codexAutoReviewCompleted struct {
	ThreadID       string  `json:"threadId"`
	TurnID         string  `json:"turnId"`
	ReviewID       string  `json:"reviewId"`
	TargetItemID   *string `json:"targetItemId"`
	StartedAtMs    int64   `json:"startedAtMs"`
	CompletedAtMs  *int64  `json:"completedAtMs"`
	DecisionSource *string `json:"decisionSource"`
	Review         struct {
		Status            string  `json:"status"`
		RiskLevel         *string `json:"riskLevel"`
		UserAuthorization *string `json:"userAuthorization"`
		Rationale         *string `json:"rationale"`
	} `json:"review"`
	Action json.RawMessage `json:"action"`
}

// ApproveAutoReviewDenial records authorization for an action that automatic review denied.
// Codex adds the approval to the thread's context without starting a turn.
// The next model request carries the approval. Codex does not enforce a one-use execution limit.
func (a *Agent) ApproveAutoReviewDenial(entries []json.RawMessage, reviewID string) error {
	review, err := findCodexAutoReview(entries, reviewID)
	if err != nil {
		return err
	}
	if review.Review.Status != contracts.CodexAutoReviewStatusDenied {
		return agent.ErrAutoReviewNotDenied
	}
	a.Mu.Lock()
	threadID := a.threadID
	a.Mu.Unlock()
	// Codex refuses this call for a Multi-Agent V2 subagent, and a thread that a
	// context clear retired takes no more input, so only the current main thread
	// qualifies.
	if review.ThreadID == "" || review.ThreadID != threadID {
		return agent.ErrAutoReviewThreadRetired
	}
	event, err := codexGuardianAssessmentEvent(review)
	if err != nil {
		return err
	}
	paramsJSON, err := json.Marshal(struct {
		ThreadID string              `json:"threadId"`
		Event    *codexGuardianEvent `json:"event"`
	}{ThreadID: threadID, Event: event})
	if err != nil {
		return fmt.Errorf("marshal %s params: %w", codexMethodApproveGuardianDeniedAction, err)
	}
	if _, err := a.SendRequest(codexMethodApproveGuardianDeniedAction, paramsJSON, a.APITimeout()); err != nil {
		return providerkit.ClassifyJSONRPCDeliveryError(codexMethodApproveGuardianDeniedAction, err)
	}
	return nil
}

// findCodexAutoReview returns the completed review with reviewID among the raw
// entries of one transcript row.
func findCodexAutoReview(entries []json.RawMessage, reviewID string) (codexAutoReviewCompleted, error) {
	for _, entry := range entries {
		var notification codexAutoReviewNotification
		if json.Unmarshal(entry, &notification) != nil {
			continue
		}
		if notification.Method == contracts.CodexMethodAutoApprovalReviewCompleted && notification.Params.ReviewID == reviewID {
			return notification.Params, nil
		}
	}
	return codexAutoReviewCompleted{}, agent.ErrAutoReviewNotFound
}

// codexGuardianStatus maps the v2 review status to the core spelling that
// thread/approveGuardianDeniedAction parses.
var codexGuardianStatus = map[string]string{
	contracts.CodexAutoReviewStatusInProgress: "in_progress",
	contracts.CodexAutoReviewStatusApproved:   "approved",
	contracts.CodexAutoReviewStatusDenied:     "denied",
	contracts.CodexAutoReviewStatusTimedOut:   "timed_out",
	contracts.CodexAutoReviewStatusAborted:    "aborted",
}

// codexGuardianCommandSource maps the v2 command source to its core spelling.
var codexGuardianCommandSource = map[string]string{
	"shell":       "shell",
	"unifiedExec": "unified_exec",
}

// codexGuardianNetworkProtocol maps the v2 network protocol to its core spelling.
var codexGuardianNetworkProtocol = map[string]string{
	"http":      "http",
	"https":     "https",
	"socks5Tcp": "socks5_tcp",
	"socks5Udp": "socks5_udp",
}

// codexGuardianEvent is the core event that the approval RPC accepts.
type codexGuardianEvent struct {
	ID                string                   `json:"id"`
	TurnID            string                   `json:"turn_id"`
	StartedAtMs       int64                    `json:"started_at_ms"`
	Status            string                   `json:"status"`
	Action            codexGuardianActionValue `json:"action"`
	TargetItemID      *string                  `json:"target_item_id,omitempty"`
	CompletedAtMs     *int64                   `json:"completed_at_ms,omitempty"`
	RiskLevel         *string                  `json:"risk_level,omitempty"`
	UserAuthorization *string                  `json:"user_authorization,omitempty"`
	Rationale         *string                  `json:"rationale,omitempty"`
	DecisionSource    *string                  `json:"decision_source,omitempty"`
}

type codexGuardianActionValue interface{ guardianAction() }
type codexGuardianActionType struct {
	Type string `json:"type"`
}

func (codexGuardianActionType) guardianAction() {}

type codexGuardianCommand struct {
	codexGuardianActionType
	Source  string `json:"source"`
	Command string `json:"command"`
	Cwd     string `json:"cwd"`
}
type codexGuardianExecve struct {
	codexGuardianActionType
	Source  string   `json:"source"`
	Program string   `json:"program"`
	Argv    []string `json:"argv"`
	Cwd     string   `json:"cwd"`
}
type codexGuardianStdin struct {
	codexGuardianActionType
	ApprovalID string `json:"approval_id"`
	ProcessID  string `json:"process_id"`
	Stdin      string `json:"stdin"`
	Cwd        string `json:"cwd"`
}
type codexGuardianPatch struct {
	codexGuardianActionType
	Cwd   string   `json:"cwd"`
	Files []string `json:"files"`
}
type codexGuardianNetwork struct {
	codexGuardianActionType
	Target   string `json:"target"`
	Host     string `json:"host"`
	Protocol string `json:"protocol"`
	Port     int    `json:"port"`
}
type codexGuardianMcp struct {
	codexGuardianActionType
	Server        string  `json:"server"`
	ToolName      string  `json:"tool_name"`
	ConnectorID   *string `json:"connector_id"`
	ConnectorName *string `json:"connector_name"`
	ToolTitle     *string `json:"tool_title"`
}
type codexGuardianPermissions struct {
	codexGuardianActionType
	Reason      *string                       `json:"reason"`
	Permissions codexGuardianPermissionValues `json:"permissions"`
}
type codexGuardianPermissionValues struct {
	Network    json.RawMessage         `json:"network"`
	FileSystem codexGuardianFileSystem `json:"file_system"`
}
type codexGuardianFileSystem interface{ guardianFileSystem() }
type codexGuardianLegacyFileSystem struct {
	Read  *[]string `json:"read,omitempty"`
	Write *[]string `json:"write,omitempty"`
}

func (codexGuardianLegacyFileSystem) guardianFileSystem() {}

type codexGuardianCanonicalFileSystem struct {
	Entries          []json.RawMessage `json:"entries"`
	GlobScanMaxDepth *int              `json:"glob_scan_max_depth,omitempty"`
}

func (codexGuardianCanonicalFileSystem) guardianFileSystem() {}

// codexGuardianAssessmentEvent converts a v2 auto-review notification into the core
// GuardianAssessmentEvent that thread/approveGuardianDeniedAction parses. The two
// differ in their casing and in two field types; testdata/codex_guardian_event_conversion.json
// pins every action variant against what Codex accepts.
func codexGuardianAssessmentEvent(review codexAutoReviewCompleted) (*codexGuardianEvent, error) {
	status, ok := codexGuardianStatus[review.Review.Status]
	if !ok {
		return nil, fmt.Errorf("codex auto-review status %q has no core spelling", review.Review.Status)
	}
	action, err := codexGuardianAction(review.Action)
	if err != nil {
		return nil, err
	}
	return &codexGuardianEvent{
		ID: review.ReviewID, TurnID: review.TurnID, StartedAtMs: review.StartedAtMs,
		Status: status, Action: action, TargetItemID: review.TargetItemID, CompletedAtMs: review.CompletedAtMs,
		RiskLevel: review.Review.RiskLevel, UserAuthorization: review.Review.UserAuthorization,
		Rationale: review.Review.Rationale, DecisionSource: review.DecisionSource,
	}, nil
}

// codexGuardianAction converts one v2 review action into its core spelling.
func codexGuardianAction(raw json.RawMessage) (codexGuardianActionValue, error) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, fmt.Errorf("codex auto-review action is invalid: %w", err)
	}
	switch head.Type {
	case contracts.CodexAutoReviewActionCommand:
		var action struct {
			Source  string `json:"source"`
			Command string `json:"command"`
			Cwd     string `json:"cwd"`
		}
		if err := json.Unmarshal(raw, &action); err != nil {
			return nil, fmt.Errorf("codex auto-review command action is invalid: %w", err)
		}
		source, err := mappedSpelling(codexGuardianCommandSource, action.Source, "command source")
		if err != nil {
			return nil, err
		}
		return codexGuardianCommand{codexGuardianActionType: codexGuardianActionType{Type: "command"}, Source: source, Command: action.Command, Cwd: action.Cwd}, nil
	case contracts.CodexAutoReviewActionExecve:
		var action struct {
			Source  string   `json:"source"`
			Program string   `json:"program"`
			Argv    []string `json:"argv"`
			Cwd     string   `json:"cwd"`
		}
		if err := json.Unmarshal(raw, &action); err != nil {
			return nil, fmt.Errorf("codex auto-review execve action is invalid: %w", err)
		}
		source, err := mappedSpelling(codexGuardianCommandSource, action.Source, "command source")
		if err != nil {
			return nil, err
		}
		argv := action.Argv
		if argv == nil {
			argv = []string{}
		}
		return codexGuardianExecve{codexGuardianActionType: codexGuardianActionType{Type: "execve"}, Source: source, Program: action.Program, Argv: argv, Cwd: action.Cwd}, nil
	case contracts.CodexAutoReviewActionWriteStdin:
		var action struct {
			ApprovalID string `json:"approvalId"`
			ProcessID  string `json:"processId"`
			Stdin      string `json:"stdin"`
			Cwd        string `json:"cwd"`
		}
		if err := json.Unmarshal(raw, &action); err != nil {
			return nil, fmt.Errorf("codex auto-review writeStdin action is invalid: %w", err)
		}
		// The core field is a URI, not a path. Codex parses it as a URL and refuses a
		// plain path.
		return codexGuardianStdin{codexGuardianActionType: codexGuardianActionType{Type: "write_stdin"}, ApprovalID: action.ApprovalID, ProcessID: action.ProcessID, Stdin: action.Stdin, Cwd: codexFileURI(action.Cwd)}, nil
	case contracts.CodexAutoReviewActionApplyPatch:
		var action struct {
			Cwd   string   `json:"cwd"`
			Files []string `json:"files"`
		}
		if err := json.Unmarshal(raw, &action); err != nil {
			return nil, fmt.Errorf("codex auto-review applyPatch action is invalid: %w", err)
		}
		files := action.Files
		if files == nil {
			files = []string{}
		}
		return codexGuardianPatch{codexGuardianActionType: codexGuardianActionType{Type: "apply_patch"}, Cwd: action.Cwd, Files: files}, nil
	case contracts.CodexAutoReviewActionNetworkAccess:
		var action struct {
			Target   string `json:"target"`
			Host     string `json:"host"`
			Protocol string `json:"protocol"`
			Port     int    `json:"port"`
		}
		if err := json.Unmarshal(raw, &action); err != nil {
			return nil, fmt.Errorf("codex auto-review networkAccess action is invalid: %w", err)
		}
		protocol, err := mappedSpelling(codexGuardianNetworkProtocol, action.Protocol, "network protocol")
		if err != nil {
			return nil, err
		}
		return codexGuardianNetwork{codexGuardianActionType: codexGuardianActionType{Type: "network_access"}, Target: action.Target, Host: action.Host, Protocol: protocol, Port: action.Port}, nil
	case contracts.CodexAutoReviewActionMcpToolCall:
		var action struct {
			Server        string  `json:"server"`
			ToolName      string  `json:"toolName"`
			ConnectorID   *string `json:"connectorId"`
			ConnectorName *string `json:"connectorName"`
			ToolTitle     *string `json:"toolTitle"`
		}
		if err := json.Unmarshal(raw, &action); err != nil {
			return nil, fmt.Errorf("codex auto-review mcpToolCall action is invalid: %w", err)
		}
		return codexGuardianMcp{codexGuardianActionType: codexGuardianActionType{Type: "mcp_tool_call"}, Server: action.Server, ToolName: action.ToolName, ConnectorID: action.ConnectorID, ConnectorName: action.ConnectorName, ToolTitle: action.ToolTitle}, nil
	case contracts.CodexAutoReviewActionRequestPermissions:
		var action struct {
			Reason      *string `json:"reason"`
			Permissions struct {
				Network    json.RawMessage `json:"network"`
				FileSystem json.RawMessage `json:"fileSystem"`
			} `json:"permissions"`
		}
		if err := json.Unmarshal(raw, &action); err != nil {
			return nil, fmt.Errorf("codex auto-review requestPermissions action is invalid: %w", err)
		}
		fileSystem, err := codexGuardianFileSystemPermissions(action.Permissions.FileSystem)
		if err != nil {
			return nil, err
		}
		return codexGuardianPermissions{codexGuardianActionType: codexGuardianActionType{Type: "request_permissions"}, Reason: action.Reason, Permissions: codexGuardianPermissionValues{Network: action.Permissions.Network, FileSystem: fileSystem}}, nil
	}
	return nil, fmt.Errorf("codex auto-review action type %q has no core spelling", head.Type)
}

// codexGuardianFileSystemPermissions converts v2 file-system permissions into the core
// form. Core reads either the legacy read and write roots or the canonical entries,
// never both, so the legacy roots win when the request states them: Codex fills both
// forms from legacy roots.
func codexGuardianFileSystemPermissions(raw json.RawMessage) (codexGuardianFileSystem, error) {
	if isJSONNullOrAbsent(raw) {
		return nil, nil
	}
	var permissions struct {
		Read             []string          `json:"read"`
		Write            []string          `json:"write"`
		GlobScanMaxDepth *int              `json:"globScanMaxDepth"`
		Entries          []json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(raw, &permissions); err != nil {
		return nil, fmt.Errorf("codex auto-review file-system permissions are invalid: %w", err)
	}
	if permissions.Read != nil || permissions.Write != nil {
		legacy := codexGuardianLegacyFileSystem{}
		if permissions.Read != nil {
			legacy.Read = &permissions.Read
		}
		if permissions.Write != nil {
			legacy.Write = &permissions.Write
		}
		return legacy, nil
	}
	entries := permissions.Entries
	if entries == nil {
		entries = []json.RawMessage{}
	}
	return codexGuardianCanonicalFileSystem{Entries: entries, GlobScanMaxDepth: permissions.GlobScanMaxDepth}, nil
}

// mappedSpelling looks a v2 word up in a v2-to-core table and fails for a word that
// a newer Codex added, so the approval states the gap instead of sending a value
// that Codex cannot parse.
func mappedSpelling(table map[string]string, value, what string) (string, error) {
	if mapped, ok := table[value]; ok {
		return mapped, nil
	}
	return "", fmt.Errorf("codex auto-review %s %q has no core spelling", what, value)
}

func isJSONNullOrAbsent(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed == "" || trimmed == "null"
}

// codexFileURI converts an absolute path into a file URI. A value that is already a
// file URI passes through. A Windows drive path gains the leading slash that a file
// URI needs before the drive letter.
func codexFileURI(path string) string {
	return codexFileURIForOS(path, runtime.GOOS)
}

func codexFileURIForOS(path, operatingSystem string) string {
	if strings.HasPrefix(path, "file:") {
		return path
	}
	slashed := path
	if operatingSystem == "windows" {
		slashed = strings.ReplaceAll(path, `\`, "/")
		if strings.HasPrefix(slashed, "//") {
			host, remainder, _ := strings.Cut(strings.TrimPrefix(slashed, "//"), "/")
			return (&url.URL{Scheme: "file", Host: host, Path: "/" + remainder}).String()
		}
	}
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	return (&url.URL{Scheme: "file", Path: slashed}).String()
}
