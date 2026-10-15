package codex

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
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

// The command and file-change approval decisions that a standing approval may send.
// Each one approves this request alone. Every other decision that Codex offers
// either refuses the request or writes a lasting rule, such as an exec policy
// amendment, and no answer without a human may write a lasting rule.
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

// bypassActive reports whether the standing approval answers permission requests now.
func (a *Agent) bypassActive() bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.bypassActiveLocked()
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
	if a.bypassActive() {
		a.SendResponseDetached(line.ID, answer, "standing approval of "+line.Method)
		return
	}
	a.registerApprovalRequest(line.Raw, cancelAnswer, answer)
}

// registerApprovalRequest publishes a permission request with its approval answer, then
// reads the standing approval again. A switch to Bypass whose sweep ran before this
// registration found no record, so this second read is what answers the request.
func (a *Agent) registerApprovalRequest(raw []byte, cancelAnswer, answer any) {
	requestID := a.PublishControlRequestWithApproval(a.sink, raw, cancelAnswer, answer)
	if requestID != "" && a.bypassActive() {
		a.ApproveOutstandingControlRequest(a.sink, requestID)
	}
}

// approveOutstandingForBypass answers every pending permission request when the
// standing approval applies, and records how many it answered. The reader saw those
// cards, so the transcript states why they left.
func (a *Agent) approveOutstandingForBypass() {
	if !a.bypassActive() {
		return
	}
	count := a.ApproveOutstandingControlRequests(a.sink)
	if count == 0 {
		return
	}
	slog.Info("codex bypass approved pending permission requests", "agent_id", a.AgentID(), "count", count)
	a.sink.PersistLeapMuxNotification(map[string]interface{}{
		contracts.NotificationFieldType:  contracts.NotificationTypePendingRequestsApproved,
		contracts.NotificationFieldCount: count,
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

// ApproveAutoReviewDenial records the user's approval of one denied review, so the
// agent may retry the action once. Codex records the approval in the thread and
// starts no turn, so the next request to the model carries it.
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
	paramsJSON, err := json.Marshal(map[string]any{"threadId": threadID, "event": event})
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

// codexGuardianAssessmentEvent converts a v2 auto-review notification into the core
// GuardianAssessmentEvent that thread/approveGuardianDeniedAction parses. The two
// differ in their casing and in two field types; testdata/codex_guardian_event_conversion.json
// pins every action variant against what Codex accepts.
func codexGuardianAssessmentEvent(review codexAutoReviewCompleted) (map[string]any, error) {
	status, ok := codexGuardianStatus[review.Review.Status]
	if !ok {
		return nil, fmt.Errorf("codex auto-review status %q has no core spelling", review.Review.Status)
	}
	action, err := codexGuardianAction(review.Action)
	if err != nil {
		return nil, err
	}
	event := map[string]any{
		"id":            review.ReviewID,
		"turn_id":       review.TurnID,
		"started_at_ms": review.StartedAtMs,
		"status":        status,
		"action":        action,
	}
	putOptional(event, "target_item_id", review.TargetItemID)
	putOptional(event, "completed_at_ms", review.CompletedAtMs)
	putOptional(event, "risk_level", review.Review.RiskLevel)
	putOptional(event, "user_authorization", review.Review.UserAuthorization)
	putOptional(event, "rationale", review.Review.Rationale)
	putOptional(event, "decision_source", review.DecisionSource)
	return event, nil
}

// putOptional sets key only for a present value, as the core event omits an absent
// optional field.
func putOptional[T any](event map[string]any, key string, value *T) {
	if value != nil {
		event[key] = *value
	}
}

// codexGuardianAction converts one v2 review action into its core spelling.
func codexGuardianAction(raw json.RawMessage) (map[string]any, error) {
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
		return map[string]any{"type": "command", "source": source, "command": action.Command, "cwd": action.Cwd}, nil
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
		return map[string]any{"type": "execve", "source": source, "program": action.Program, "argv": argv, "cwd": action.Cwd}, nil
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
		return map[string]any{"type": "write_stdin", "approval_id": action.ApprovalID, "process_id": action.ProcessID, "stdin": action.Stdin, "cwd": codexFileURI(action.Cwd)}, nil
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
		return map[string]any{"type": "apply_patch", "cwd": action.Cwd, "files": files}, nil
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
		return map[string]any{"type": "network_access", "target": action.Target, "host": action.Host, "protocol": protocol, "port": action.Port}, nil
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
		return map[string]any{"type": "mcp_tool_call", "server": action.Server, "tool_name": action.ToolName, "connector_id": action.ConnectorID, "connector_name": action.ConnectorName, "tool_title": action.ToolTitle}, nil
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
		return map[string]any{"type": "request_permissions", "reason": action.Reason, "permissions": map[string]any{
			"network":     nullableRaw(action.Permissions.Network),
			"file_system": fileSystem,
		}}, nil
	}
	return nil, fmt.Errorf("codex auto-review action type %q has no core spelling", head.Type)
}

// codexGuardianFileSystemPermissions converts v2 file-system permissions into the core
// form. Core reads either the legacy read and write roots or the canonical entries,
// never both, so the legacy roots win when the request states them: Codex fills both
// forms from legacy roots.
func codexGuardianFileSystemPermissions(raw json.RawMessage) (any, error) {
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
		legacy := map[string]any{}
		if permissions.Read != nil {
			legacy["read"] = permissions.Read
		}
		if permissions.Write != nil {
			legacy["write"] = permissions.Write
		}
		return legacy, nil
	}
	entries := permissions.Entries
	if entries == nil {
		entries = []json.RawMessage{}
	}
	canonical := map[string]any{"entries": entries}
	if permissions.GlobScanMaxDepth != nil {
		canonical["glob_scan_max_depth"] = *permissions.GlobScanMaxDepth
	}
	return canonical, nil
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

// nullableRaw returns raw for a present value and nil for an absent or null one, so
// the core event states an explicit null.
func nullableRaw(raw json.RawMessage) any {
	if isJSONNullOrAbsent(raw) {
		return nil
	}
	return raw
}

func isJSONNullOrAbsent(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed == "" || trimmed == "null"
}

// codexFileURI converts an absolute path into a file URI. A value that is already a
// file URI passes through. A Windows drive path gains the leading slash that a file
// URI needs before the drive letter.
func codexFileURI(path string) string {
	if strings.HasPrefix(path, "file:") {
		return path
	}
	slashed := filepath.ToSlash(path)
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	return (&url.URL{Scheme: "file", Path: slashed}).String()
}
