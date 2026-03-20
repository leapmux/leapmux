package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// CodexAgent manages a single Codex app-server process.
type CodexAgent struct {
	processBase // shared process lifecycle (Stop, Wait, Stderr, etc.)

	model      string
	effort     string
	workingDir string
	sink       OutputSink

	// Codex-specific state.
	threadID        string       // from thread/start response
	turnID          string       // currently active turn ID
	nextReqID       atomic.Int64 // JSON-RPC request ID counter
	pendingReqs     sync.Map     // reqID (int64) -> chan json.RawMessage
	approvalPolicy  string       // Codex approval policy (stored as-is from DB)
	sandboxPolicy   string       // Codex sandbox policy (e.g. "workspace-write")
	networkAccess   string       // Codex network access ("restricted" or "enabled")
	availableModels []*leapmuxv1.AvailableModel
}

// StartCodex starts a Codex agent process and performs the JSON-RPC handshake.
func StartCodex(ctx context.Context, opts Options, sink OutputSink) (Provider, error) {
	ctx, cancel := context.WithCancel(ctx)

	// Codex doesn't have third-party provider detection or model/effort
	// conditional args, so we pass empty modelEffortArgs for a simple command.
	cmd, preambleDelimiter, metaPrefix := buildShellWrappedCommand(
		ctx, opts.Shell, opts.LoginShell, "codex", []string{"app-server"}, nil, opts.WorkingDir,
	)

	cmd.Env = filterEnv(cmd.Environ(), "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT")
	cmd.Env = append(cmd.Env, "LEAPMUX_WORKER=1")
	if opts.LoginShell {
		cmd.Env = append(cmd.Env, "CLAUDECODE=1")
	}

	cmd.Cancel = func() error {
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 5 * time.Second

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	a := &CodexAgent{
		processBase: processBase{
			agentID:            opts.AgentID,
			cmd:                cmd,
			stdin:              stdin,
			ctx:                ctx,
			cancel:             cancel,
			stderrDone:         make(chan struct{}),
			processDone:        make(chan struct{}),
			preambleDelimiter:  preambleDelimiter,
			preambleMetaPrefix: metaPrefix,
			preambleMeta:       make(map[string]string),
		},
		model:      opts.Model,
		effort:     opts.Effort,
		workingDir: opts.WorkingDir,
		sink:       sink,
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start codex: %w", err)
	}

	// Drain stderr in background.
	a.drainStderr(stderrPipe)

	// Read stdout JSONL in background.
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	go a.readOutput(scanner)

	cleanup := func() {
		a.Stop()
		_ = a.Wait()
	}

	timeout := opts.startupTimeout()

	// 1. Send "initialize" request.
	if _, err := a.sendRequest("initialize", json.RawMessage(`{
		"clientInfo": {"name": "leapmux", "version": "1.0.0"},
		"capabilities": {"experimentalApi": true}
	}`), timeout); err != nil {
		cleanup()
		return nil, a.formatStartupError("initialize", err)
	}

	// 2. Send "initialized" notification.
	if err := a.sendNotification("initialized", nil); err != nil {
		cleanup()
		return nil, a.formatStartupError("initialized notification", err)
	}

	// 3. Use the permission mode directly as the Codex approval policy.
	// The DB stores provider-native values (e.g. "never", "on-request", "untrusted" for Codex).
	approvalPolicy := opts.PermissionMode
	if approvalPolicy == "" {
		approvalPolicy = "on-request"
	}
	a.approvalPolicy = approvalPolicy

	sandboxPolicy := opts.CodexSandboxPolicy
	if sandboxPolicy == "" {
		sandboxPolicy = "workspace-write"
	}
	a.sandboxPolicy = sandboxPolicy

	networkAccess := opts.CodexNetworkAccess
	if networkAccess == "" {
		networkAccess = "restricted"
	}
	a.networkAccess = networkAccess

	// 4. Send "thread/start" or "thread/resume" request.
	threadParams := map[string]interface{}{
		"model":          opts.Model,
		"cwd":            opts.WorkingDir,
		"approvalPolicy": approvalPolicy,
		"sandbox":        sandboxPolicy,
	}

	threadMethod := "thread/start"
	if opts.ResumeSessionID != "" {
		threadMethod = "thread/resume"
		threadParams["threadId"] = opts.ResumeSessionID
	}

	threadParamsJSON, _ := json.Marshal(threadParams)
	threadResp, err := a.sendRequest(threadMethod, threadParamsJSON, timeout)
	if err != nil {
		// If thread/resume fails, fall back to thread/start.
		if threadMethod == "thread/resume" {
			slog.Warn("codex thread/resume failed, falling back to thread/start",
				"agent_id", opts.AgentID, "error", err)
			delete(threadParams, "threadId")
			threadParamsJSON, _ = json.Marshal(threadParams)
			threadResp, err = a.sendRequest("thread/start", threadParamsJSON, timeout)
		}
		if err != nil {
			cleanup()
			return nil, a.formatStartupError(threadMethod, err)
		}
	}

	// Extract thread ID from response.
	var threadResult struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(threadResp, &threadResult); err != nil {
		// If thread/resume response is unparseable, fall back to thread/start.
		if threadMethod == "thread/resume" {
			slog.Warn("codex thread/resume: failed to parse response, falling back to thread/start",
				"agent_id", opts.AgentID, "error", err, "response", string(threadResp))
			delete(threadParams, "threadId")
			threadParamsJSON, _ = json.Marshal(threadParams)
			threadResp, err = a.sendRequest("thread/start", threadParamsJSON, timeout)
			if err != nil {
				cleanup()
				return nil, a.formatStartupError("thread/start", err)
			}
			if err := json.Unmarshal(threadResp, &threadResult); err != nil {
				cleanup()
				return nil, fmt.Errorf("codex thread/start: failed to parse response: %w", err)
			}
		} else {
			cleanup()
			return nil, fmt.Errorf("codex %s: failed to parse response: %w", threadMethod, err)
		}
	}
	if threadResult.Thread.ID == "" {
		// If thread/resume returned an empty thread ID, fall back to thread/start.
		if threadMethod == "thread/resume" {
			slog.Warn("codex thread/resume: response had empty thread ID, falling back to thread/start",
				"agent_id", opts.AgentID, "response", string(threadResp))
			delete(threadParams, "threadId")
			threadParamsJSON, _ = json.Marshal(threadParams)
			var err error
			threadResp, err = a.sendRequest("thread/start", threadParamsJSON, timeout)
			if err != nil {
				cleanup()
				return nil, a.formatStartupError("thread/start", err)
			}
			if err := json.Unmarshal(threadResp, &threadResult); err != nil {
				cleanup()
				return nil, fmt.Errorf("codex thread/start: failed to parse response: %w", err)
			}
		}
		if threadResult.Thread.ID == "" {
			cleanup()
			return nil, fmt.Errorf("codex %s: response did not contain a thread ID", threadMethod)
		}
	}
	a.threadID = threadResult.Thread.ID
	sink.UpdateSessionID(a.threadID)
	sink.BroadcastStatusActive(a.threadID)

	// 5. Query available models (best-effort; don't fail startup if this fails).
	a.availableModels = a.queryAvailableModels(timeout)

	return a, nil
}

// SendInput writes a user message to the agent. If a turn is already in
// progress it uses turn/steer; otherwise it starts a new turn via turn/start
// with the current model, effort, approval policy and sandbox policy.
func (a *CodexAgent) SendInput(content string) error {
	// Read shared state under lock, then release before the blocking RPC.
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return fmt.Errorf("agent is stopped")
	}
	threadID := a.threadID
	turnID := a.turnID
	model := a.model
	effort := a.effort
	approvalPolicy := a.approvalPolicy
	sandboxPolicy := a.sandboxPolicy
	networkAccess := a.networkAccess
	a.mu.Unlock()

	if threadID == "" {
		return fmt.Errorf("codex agent has no active thread")
	}

	input := []map[string]interface{}{
		{"type": "text", "text": content},
	}

	// If a turn is active, steer it instead of starting a new one.
	if turnID != "" {
		return a.sendTurnSteer(threadID, turnID, input)
	}

	return a.sendTurnStart(threadID, input, model, effort, approvalPolicy, sandboxPolicy, networkAccess)
}

// sendTurnStart sends a turn/start request with all current settings.
func (a *CodexAgent) sendTurnStart(
	threadID string,
	input []map[string]interface{},
	model, effort, approvalPolicy, sandboxPolicy, networkAccess string,
) error {
	params := map[string]interface{}{
		"threadId": threadID,
		"input":    input,
	}
	if model != "" {
		params["model"] = model
	}
	if effort != "" {
		params["effort"] = effort
	}
	if approvalPolicy != "" {
		params["approvalPolicy"] = approvalPolicy
	}
	if sp := codexSandboxPolicyObject(sandboxPolicy, networkAccess); sp != nil {
		params["sandboxPolicy"] = sp
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal turn/start params: %w", err)
	}

	resp, err := a.sendRequest("turn/start", paramsJSON, 30*time.Second)
	if err != nil {
		return fmt.Errorf("turn/start: %w", err)
	}

	// Extract turn ID from response.
	var turnResult struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(resp, &turnResult); err == nil && turnResult.Turn.ID != "" {
		a.mu.Lock()
		a.turnID = turnResult.Turn.ID
		a.mu.Unlock()
	}

	return nil
}

// sendTurnSteer steers the active turn with additional user input.
func (a *CodexAgent) sendTurnSteer(threadID, turnID string, input []map[string]interface{}) error {
	params := map[string]interface{}{
		"threadId":       threadID,
		"expectedTurnId": turnID,
		"input":          input,
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal turn/steer params: %w", err)
	}

	_, err = a.sendRequest("turn/steer", paramsJSON, 30*time.Second)
	if err != nil {
		return fmt.Errorf("turn/steer: %w", err)
	}
	return nil
}

// codexSandboxPolicyObject converts a simple sandbox policy string
// (e.g. "danger-full-access") to the tagged object format expected by
// turn/start's sandboxPolicy field (e.g. {"type": "dangerFullAccess"}).
// networkAccess is included as a boolean for workspaceWrite/readOnly or
// as a string ("restricted"/"enabled") for dangerFullAccess.
// Returns nil if the policy is empty or unrecognized.
func codexSandboxPolicyObject(policy, networkAccess string) map[string]interface{} {
	var obj map[string]interface{}
	switch policy {
	case "danger-full-access":
		obj = map[string]interface{}{"type": "dangerFullAccess"}
	case "workspace-write":
		obj = map[string]interface{}{"type": "workspaceWrite"}
	case "read-only":
		obj = map[string]interface{}{"type": "readOnly"}
	default:
		return nil
	}
	obj["networkAccess"] = networkAccess == "enabled"
	return obj
}

// CurrentSettings returns the current settings for this agent.
func (a *CodexAgent) CurrentSettings() *leapmuxv1.AgentSettings {
	a.mu.Lock()
	defer a.mu.Unlock()
	return &leapmuxv1.AgentSettings{
		Model:              a.model,
		Effort:             a.effort,
		PermissionMode:     a.approvalPolicy,
		CodexSandboxPolicy: a.sandboxPolicy,
		CodexNetworkAccess: a.networkAccess,
	}
}

// AvailableModels returns the models reported by the Codex process.
func (a *CodexAgent) AvailableModels() []*leapmuxv1.AvailableModel {
	return a.availableModels
}

// UpdateSettings stores new settings so the next turn/start picks them up.
func (a *CodexAgent) UpdateSettings(s *leapmuxv1.AgentSettings) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s.GetModel() != "" {
		a.model = s.GetModel()
	}
	if s.GetEffort() != "" {
		a.effort = s.GetEffort()
	}
	if s.GetPermissionMode() != "" {
		a.approvalPolicy = s.GetPermissionMode()
	}
	if s.GetCodexSandboxPolicy() != "" {
		a.sandboxPolicy = s.GetCodexSandboxPolicy()
	}
	if s.GetCodexNetworkAccess() != "" {
		a.networkAccess = s.GetCodexNetworkAccess()
	}
	return true
}

// queryAvailableModels sends a model/list request and converts the response.
func (a *CodexAgent) queryAvailableModels(timeout time.Duration) []*leapmuxv1.AvailableModel {
	resp, err := a.sendRequest("model/list", json.RawMessage(`{}`), timeout)
	if err != nil {
		slog.Warn("codex model/list failed", "agent_id", a.agentID, "error", err)
		return nil
	}

	var result struct {
		Data []struct {
			ID                        string `json:"id"`
			Model                     string `json:"model"`
			DisplayName               string `json:"displayName"`
			IsDefault                 bool   `json:"isDefault"`
			Hidden                    bool   `json:"hidden"`
			DefaultReasoningEffort    string `json:"defaultReasoningEffort"`
			SupportedReasoningEfforts []struct {
				ReasoningEffort string `json:"reasoningEffort"`
				Description     string `json:"description"`
			} `json:"supportedReasoningEfforts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		slog.Warn("codex model/list unmarshal failed", "agent_id", a.agentID, "error", err)
		return nil
	}

	// Build a lookup from default models so we can fill in missing metadata.
	var defaults []*leapmuxv1.AvailableModel
	if reg, ok := providerRegistry[leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX]; ok {
		defaults = reg.defaultModels
	}
	defaultsByID := make(map[string]*leapmuxv1.AvailableModel, len(defaults))
	for _, d := range defaults {
		defaultsByID[d.Id] = d
	}

	var models []*leapmuxv1.AvailableModel
	for _, m := range result.Data {
		if m.Hidden {
			continue
		}
		id := m.Model
		if id == "" {
			id = m.ID
		}
		// Reverse effort order so highest appears first, and split
		// the server description into a short label + tooltip.
		raw := m.SupportedReasoningEfforts
		efforts := make([]*leapmuxv1.AvailableEffort, len(raw))
		for i, e := range raw {
			efforts[len(raw)-1-i] = &leapmuxv1.AvailableEffort{
				Id:          e.ReasoningEffort,
				Name:        codexEffortName(e.ReasoningEffort),
				Description: e.Description,
			}
		}

		// Prefer our curated display name over the API's, which often
		// returns the raw model ID (e.g. "gpt-5.4" instead of "GPT-5.4").
		var displayName string
		var description string
		if d, ok := defaultsByID[id]; ok {
			displayName = d.DisplayName
			description = d.Description
		}
		if displayName == "" {
			displayName = m.DisplayName
		}
		if displayName == "" {
			displayName = codexModelDisplayName(id)
		}

		models = append(models, &leapmuxv1.AvailableModel{
			Id:               id,
			DisplayName:      displayName,
			Description:      description,
			IsDefault:        m.IsDefault,
			DefaultEffort:    m.DefaultReasoningEffort,
			SupportedEfforts: efforts,
		})
	}
	return models
}

// Default Codex efforts (high to low) used as static fallback.
var codexDefaultEfforts = []*leapmuxv1.AvailableEffort{
	{Id: "xhigh", Name: "Extra High"},
	{Id: "high", Name: "High"},
	{Id: "medium", Name: "Medium"},
	{Id: "low", Name: "Low"},
	{Id: "minimal", Name: "Minimal"},
	{Id: "none", Name: "None"},
}

var codexDefaultModels = []*leapmuxv1.AvailableModel{
	{Id: "gpt-5.4", DisplayName: "GPT-5.4", Description: "Latest frontier agentic coding model", IsDefault: true, DefaultEffort: "medium", SupportedEfforts: codexDefaultEfforts},
	{Id: "gpt-5.4-mini", DisplayName: "GPT-5.4 Mini", Description: "Smaller frontier agentic coding model", DefaultEffort: "medium", SupportedEfforts: codexDefaultEfforts},
	{Id: "gpt-5.3-codex", DisplayName: "GPT-5.3 Codex", Description: "Frontier Codex-optimized agentic coding model", DefaultEffort: "medium", SupportedEfforts: codexDefaultEfforts},
	{Id: "gpt-5.2-codex", DisplayName: "GPT-5.2 Codex", Description: "Frontier agentic coding model", DefaultEffort: "medium", SupportedEfforts: codexDefaultEfforts},
	{Id: "gpt-5.2", DisplayName: "GPT-5.2", Description: "Optimized for professional work and long-running agents", DefaultEffort: "medium", SupportedEfforts: codexDefaultEfforts},
	{Id: "gpt-5.1-codex-max", DisplayName: "GPT-5.1 Codex Max", Description: "Codex-optimized model for deep and fast reasoning", DefaultEffort: "medium", SupportedEfforts: codexDefaultEfforts},
	{Id: "gpt-5.1-codex-mini", DisplayName: "GPT-5.1 Codex Mini", Description: "Optimized for Codex; cheaper, faster, but less capable", DefaultEffort: "medium", SupportedEfforts: codexDefaultEfforts},
}

func init() {
	registerProvider(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		func(ctx context.Context, opts Options, sink OutputSink) (Provider, error) {
			return StartCodex(ctx, opts, sink)
		},
		codexDefaultModels,
		[]*leapmuxv1.AvailableOptionGroup{
			{
				Key:   "permissionMode",
				Label: "Approval Policy",
				Options: []*leapmuxv1.AvailableOption{
					{Id: "never", Name: "Full Auto"},
					{Id: "on-request", Name: "Suggest & Approve"},
					{Id: "untrusted", Name: "Auto-edit"},
				},
			},
			{
				Key:   "codexSandboxPolicy",
				Label: "Sandbox Policy",
				Options: []*leapmuxv1.AvailableOption{
					{Id: "danger-full-access", Name: "Full Access", Description: "No filesystem restrictions"},
					{Id: "workspace-write", Name: "Workspace Write", Description: "Write only within the working directory"},
					{Id: "read-only", Name: "Read Only", Description: "No write access to the filesystem"},
				},
			},
			{
				Key:   "codexNetworkAccess",
				Label: "Network Access",
				Options: []*leapmuxv1.AvailableOption{
					{Id: "restricted", Name: "Restricted", Description: "No network access from the sandbox"},
					{Id: "enabled", Name: "Enabled", Description: "Allow network access from the sandbox"},
				},
			},
		},
		"LEAPMUX_CODEX_DEFAULT_MODEL",
		"LEAPMUX_CODEX_DEFAULT_EFFORT",
	)
}

// codexModelDisplayName generates a human-readable display name from a Codex
// model ID (e.g. "gpt-5.4-mini" → "GPT-5.4 Mini", "o4-mini" → "o4-mini").
func codexModelDisplayName(id string) string {
	prefix := ""
	rest := id
	if strings.HasPrefix(id, "gpt-") {
		prefix = "GPT-"
		rest = id[4:]
	}
	// Split remaining by hyphens, capitalize suffix parts.
	parts := strings.SplitN(rest, "-", 2)
	if len(parts) == 1 {
		return prefix + parts[0]
	}
	// Version part stays as-is, suffix parts get title-cased.
	suffixParts := strings.Split(parts[1], "-")
	for i, p := range suffixParts {
		if len(p) > 0 {
			suffixParts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return prefix + parts[0] + " " + strings.Join(suffixParts, " ")
}

// codexEffortName returns a short human-readable label for a Codex effort ID.
func codexEffortName(id string) string {
	for _, e := range codexDefaultEfforts {
		if e.Id == id {
			return e.Name
		}
	}
	return id
}

// HandleOutput processes a single JSONL notification from Codex.
func (a *CodexAgent) HandleOutput(content []byte) {
	handleCodexOutput(a, content)
}

// --- JSON-RPC helpers ---

// sendRequest sends a JSON-RPC request and waits for the response.
func (a *CodexAgent) sendRequest(method string, params json.RawMessage, timeout time.Duration) (json.RawMessage, error) {
	reqID := a.nextReqID.Add(1)

	ch := make(chan json.RawMessage, 1)
	a.pendingReqs.Store(reqID, ch)
	defer a.pendingReqs.Delete(reqID)

	msg := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      reqID,
		"method":  method,
	}
	if params != nil {
		msg["params"] = json.RawMessage(params)
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	data = append(data, '\n')

	if _, err := a.stdin.Write(data); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case resp := <-ch:
		return resp, nil
	case <-a.processDone:
		return nil, a.processExitError()
	case <-a.ctx.Done():
		return nil, a.ctx.Err()
	case <-timer.C:
		return nil, fmt.Errorf("timeout waiting for %s response", method)
	}
}

// sendNotification sends a JSON-RPC notification (no id, no response expected).
func (a *CodexAgent) sendNotification(method string, params json.RawMessage) error {
	msg := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if params != nil {
		msg["params"] = json.RawMessage(params)
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}
	data = append(data, '\n')

	if _, err := a.stdin.Write(data); err != nil {
		return fmt.Errorf("write notification: %w", err)
	}

	return nil
}

// readOutput reads JSONL lines from stdout and dispatches them.
func (a *CodexAgent) readOutput(scanner *bufio.Scanner) {
	a.skipPreamble(scanner)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		lineCopy := make([]byte, len(line))
		copy(lineCopy, line)

		if a.handleJSONRPCResponse(lineCopy) {
			continue
		}

		a.HandleOutput(lineCopy)
	}

	if err := scanner.Err(); err != nil {
		slog.Warn("codex agent stdout read error",
			"agent_id", a.agentID,
			"error", err,
		)
	}

	a.waitErr = a.cmd.Wait()
	close(a.processDone)
}

// handleJSONRPCResponse checks if a line is a JSON-RPC response and routes it.
// JSON-RPC responses have a numeric "id" field. Notifications do not.
// We unmarshal just the top-level id/method fields — notifications bail out
// quickly at the nil-ID check without inspecting the payload.
func (a *CodexAgent) handleJSONRPCResponse(line []byte) bool {
	var envelope struct {
		ID     *json.Number    `json:"id"`
		Method string          `json:"method"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(line, &envelope); err != nil {
		return false
	}

	// Notifications have "method" but no "id"; responses have "id" but no "method".
	if envelope.ID == nil || envelope.Method != "" {
		return false
	}

	reqID, err := envelope.ID.Int64()
	if err != nil {
		return false
	}

	val, ok := a.pendingReqs.Load(reqID)
	if !ok {
		return false
	}

	ch := val.(chan json.RawMessage)
	if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
		// Send error as the response — caller can inspect it.
		ch <- envelope.Error
	} else {
		ch <- envelope.Result
	}

	return true
}

// formatStartupError includes stderr and preamble output for diagnostics.
func (a *CodexAgent) formatStartupError(phase string, err error) error {
	return a.processBase.formatStartupError(phase, err, a.PreambleOutput())
}
