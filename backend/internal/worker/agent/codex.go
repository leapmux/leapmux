package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/util/version"
)

// Codex default option values.
const (
	CodexDefaultApprovalPolicy    = "on-request"
	CodexDefaultSandboxPolicy     = "workspace-write"
	CodexDefaultNetworkAccess     = "restricted"
	CodexDefaultCollaborationMode = "default"
	CodexDefaultServiceTier       = "default"
)

const (
	CodexExtraSandboxPolicy     = "sandbox_policy"
	CodexExtraNetworkAccess     = "network_access"
	CodexExtraCollaborationMode = "collaboration_mode"
	CodexExtraServiceTier       = "service_tier"
)

// Codex sandbox policy values.
const (
	CodexSandboxDangerFullAccess = "danger-full-access"
	CodexSandboxWorkspaceWrite   = "workspace-write"
	CodexSandboxReadOnly         = "read-only"
)

// Codex network access values.
const (
	CodexNetworkRestricted = "restricted"
	CodexNetworkEnabled    = "enabled"
)

// Codex collaboration mode values.
const (
	CodexCollaborationDefault = "default"
	CodexCollaborationPlan    = "plan"
)

// Codex service tier values.
const (
	CodexServiceTierFast = "fast"
)

// StringOrDefault returns value if non-empty, otherwise fallback.
func StringOrDefault(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

// CodexAgent manages a single Codex app-server process.
type CodexAgent struct {
	jsonrpcBase // shared process lifecycle + JSON-RPC plumbing

	model      string
	effort     string
	workingDir string
	sink       OutputSink

	// Codex-specific state.
	threadID          string // from thread/start response
	turnID            string // currently active turn ID
	approvalPolicy    string // Codex approval policy (stored as-is from DB)
	sandboxPolicy     string // Codex sandbox policy (e.g. "workspace-write")
	networkAccess     string // Codex network access ("restricted" or "enabled")
	collaborationMode string // Codex collaboration mode ("default" or "plan")
	serviceTier       string // Codex service tier ("default" or "fast")
	turnSawPlan       bool   // whether the current turn produced a plan item
	turnPlanText      string // final text of the current turn's plan item
	turnAssistantText string // final assistant message text for the current turn
	streamingPlan     bool   // whether we've sent streamingType session info for the current plan stream
	availableModels   []*leapmuxv1.AvailableModel
	collabThreadSpans map[string]string // child thread ID -> owning spawnAgent span ID
	collabSpanThreads map[string]int    // spawnAgent span ID -> active child thread count
}

// StartCodex starts a Codex agent process and performs the JSON-RPC handshake.
func StartCodex(ctx context.Context, opts Options, sink OutputSink) (Provider, error) {
	ctx, cancel := context.WithCancel(ctx)

	// Codex doesn't have third-party provider detection or model/effort
	// conditional args, so we pass empty modelEffortArgs for a simple command.
	cmd, preambleDelimiter, metaPrefix := buildShellWrappedCommand(
		ctx, opts.Shell, opts.LoginShell, "codex", []string{"CODEX_CI"}, []string{"app-server"}, nil, opts.WorkingDir,
	)

	cmd.Env = filterEnv(cmd.Environ(), "CODEX_CI", "CODEX_THREAD_ID")
	cmd.Env = append(cmd.Env, "LEAPMUX_WORKER=1")
	if opts.LoginShell {
		cmd.Env = append(cmd.Env, "CODEX_CI=1")
	}

	stdin, stdout, stderrPipe, err := setupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}

	a := &CodexAgent{
		jsonrpcBase: jsonrpcBase{processBase: processBase{
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
			apiTimeout:         opts.apiTimeout(),
		}},
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
	go a.readOutputLoop(scanner, a.handleOutput)

	cleanup := func() {
		a.Stop()
		_ = a.Wait()
	}

	timeout := opts.startupTimeout()

	// 1. Send "initialize" request.
	initParams, err := json.Marshal(map[string]interface{}{
		"clientInfo": map[string]string{"name": "leapmux", "title": "LeapMux", "version": version.Value},
		"capabilities": map[string]interface{}{
			"experimentalApi":           true,
			"optOutNotificationMethods": []string{"turn/diff/updated"},
		},
	})
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("marshal initialize params: %w", err)
	}
	if _, err := a.sendRequest("initialize", json.RawMessage(initParams), timeout); err != nil {
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
	a.approvalPolicy = StringOrDefault(opts.PermissionMode, CodexDefaultApprovalPolicy)
	a.sandboxPolicy = StringOrDefault(opts.ExtraSettings[CodexExtraSandboxPolicy], CodexDefaultSandboxPolicy)
	a.networkAccess = StringOrDefault(opts.ExtraSettings[CodexExtraNetworkAccess], CodexDefaultNetworkAccess)
	a.collaborationMode = StringOrDefault(opts.ExtraSettings[CodexExtraCollaborationMode], CodexDefaultCollaborationMode)
	a.serviceTier = StringOrDefault(opts.ExtraSettings[CodexExtraServiceTier], CodexDefaultServiceTier)

	// 4. Send "thread/start" or "thread/resume" request.
	threadParams := map[string]interface{}{
		"model":          opts.Model,
		"cwd":            opts.WorkingDir,
		"approvalPolicy": a.approvalPolicy,
		"sandbox":        a.sandboxPolicy,
	}
	if st := codexServiceTierValue(a.serviceTier); st != nil {
		threadParams["serviceTier"] = *st
	}

	threadMethod := "thread/start"
	if opts.ResumeSessionID != "" {
		threadMethod = "thread/resume"
		threadParams["threadId"] = opts.ResumeSessionID
	}

	threadID, err := a.startOrResumeThread(threadParams, threadMethod, opts.AgentID, timeout)
	if err != nil {
		cleanup()
		return nil, a.formatStartupError(threadMethod, err)
	}
	a.threadID = threadID
	sink.UpdateSessionID(a.threadID)
	sink.BroadcastStatusActive(a.threadID)

	// 5. Query available models (best-effort; don't fail startup if this fails).
	a.availableModels = a.queryAvailableModels(timeout)

	return a, nil
}

// startOrResumeThread sends thread/start (or thread/resume when resuming). If
// thread/resume fails for any reason (RPC error, unparseable response, empty
// thread ID), it falls back to thread/start automatically.
func (a *CodexAgent) startOrResumeThread(
	threadParams map[string]interface{}, method, agentID string, timeout time.Duration,
) (string, error) {
	threadID, fallback, err := a.tryThreadRequest(threadParams, method, agentID, timeout)
	if err != nil && !fallback {
		return "", err
	}
	if threadID != "" {
		return threadID, nil
	}

	// Fall back to thread/start.
	delete(threadParams, "threadId")
	threadID, _, err = a.tryThreadRequest(threadParams, "thread/start", agentID, timeout)
	if err != nil {
		return "", err
	}
	if threadID == "" {
		return "", fmt.Errorf("codex thread/start: response did not contain a thread ID")
	}
	return threadID, nil
}

// tryThreadRequest sends a thread request and returns the thread ID. If the
// request was a thread/resume that can be retried as thread/start, it returns
// fallback=true with an empty threadID.
func (a *CodexAgent) tryThreadRequest(
	threadParams map[string]interface{}, method, agentID string, timeout time.Duration,
) (threadID string, fallback bool, err error) {
	canFallback := method == "thread/resume"

	paramsJSON, err := json.Marshal(threadParams)
	if err != nil {
		return "", false, fmt.Errorf("marshal %s params: %w", method, err)
	}
	resp, err := a.sendRequest(method, paramsJSON, timeout)
	if err != nil {
		if canFallback {
			slog.Warn("codex thread/resume failed, falling back to thread/start",
				"agent_id", agentID, "error", err)
			return "", true, err
		}
		return "", false, err
	}

	var result struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		if canFallback {
			slog.Warn("codex thread/resume: failed to parse response, falling back to thread/start",
				"agent_id", agentID, "error", err, "response", string(resp))
			return "", true, nil
		}
		return "", false, fmt.Errorf("codex %s: failed to parse response: %w", method, err)
	}

	if result.Thread.ID == "" && canFallback {
		slog.Warn("codex thread/resume: response had empty thread ID, falling back to thread/start",
			"agent_id", agentID, "response", string(resp))
		return "", true, nil
	}

	return result.Thread.ID, false, nil
}

// ClearContext sends a new thread/start on the running Codex process,
// replacing the current thread with a fresh one.
func (a *CodexAgent) ClearContext() (string, bool) {
	a.mu.Lock()
	approvalPolicy := a.approvalPolicy
	sandboxPolicy := a.sandboxPolicy
	serviceTier := a.serviceTier
	model := a.model
	workingDir := a.workingDir
	a.mu.Unlock()

	threadParams := map[string]interface{}{
		"model":          model,
		"cwd":            workingDir,
		"approvalPolicy": approvalPolicy,
		"sandbox":        sandboxPolicy,
	}
	if st := codexServiceTierValue(serviceTier); st != nil {
		threadParams["serviceTier"] = *st
	}

	threadID, _, err := a.tryThreadRequest(threadParams, "thread/start", a.agentID, a.APITimeout())
	if err != nil || threadID == "" {
		slog.Error("codex ClearContext: thread/start failed", "agent_id", a.agentID, "error", err)
		return "", false
	}

	a.mu.Lock()
	a.threadID = threadID
	a.turnID = ""
	a.turnSawPlan = false
	a.turnPlanText = ""
	a.turnAssistantText = ""
	a.streamingPlan = false
	a.mu.Unlock()

	a.sink.UpdateSessionID(threadID)
	return threadID, true
}

// SendInput writes a user message to the agent. If a turn is already in
// progress it uses turn/steer; otherwise it starts a new turn via turn/start
// with the current model, effort, approval policy and sandbox policy.
func (a *CodexAgent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
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
	collaborationMode := a.collaborationMode
	serviceTier := a.serviceTier
	a.mu.Unlock()

	if threadID == "" {
		return fmt.Errorf("codex agent has no active thread")
	}

	input := buildCodexInputBlocks(content, classifyAttachments(attachments))

	// If a turn is active, steer it instead of starting a new one.
	if turnID != "" {
		return a.sendTurnSteer(threadID, turnID, input)
	}

	return a.sendTurnStart(threadID, input, turnSettings{
		model:             model,
		effort:            effort,
		approvalPolicy:    approvalPolicy,
		sandboxPolicy:     sandboxPolicy,
		networkAccess:     networkAccess,
		collaborationMode: collaborationMode,
		serviceTier:       serviceTier,
	})
}

// buildCodexInputBlocks converts text + classified attachments into Codex's
// input format. Images use data URI format; text attachments are inlined.
func buildCodexInputBlocks(content string, classified []classifiedAttachment) []map[string]interface{} {
	var input []map[string]interface{}
	if content != "" {
		input = append(input, map[string]interface{}{"type": "text", "text": content})
	}
	for _, attachment := range classified {
		switch attachment.kind {
		case attachmentKindText:
			input = append(input, map[string]interface{}{
				"type": "text",
				"text": buildInlineTextAttachmentBlock(attachment),
			})
		case attachmentKindImage:
			input = append(input, map[string]interface{}{
				"type": "image",
				"url":  encodeDataURI(attachment.mimeType, attachment.data),
			})
		}
	}
	return input
}

// turnSettings groups the per-turn settings snapshotted from agent state.
type turnSettings struct {
	model             string
	effort            string
	approvalPolicy    string
	sandboxPolicy     string
	networkAccess     string
	collaborationMode string
	serviceTier       string
}

// sendTurnStart sends a turn/start request with all current settings.
func (a *CodexAgent) sendTurnStart(
	threadID string,
	input []map[string]interface{},
	s turnSettings,
) error {
	params := map[string]interface{}{
		"threadId": threadID,
		"input":    input,
	}
	if s.model != "" {
		params["model"] = s.model
	}
	if s.effort != "" {
		params["effort"] = s.effort
	}
	if s.approvalPolicy != "" {
		params["approvalPolicy"] = s.approvalPolicy
	}
	if sp := codexSandboxPolicyObject(s.sandboxPolicy, s.networkAccess); sp != nil {
		params["sandboxPolicy"] = sp
	}
	if cm := codexCollaborationModeObject(s.collaborationMode, s.model, s.effort); cm != nil {
		params["collaborationMode"] = cm
	}
	if st := codexServiceTierValue(s.serviceTier); st != nil {
		params["serviceTier"] = *st
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal turn/start params: %w", err)
	}

	resp, err := a.sendRequest("turn/start", paramsJSON, a.APITimeout())
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

	_, err = a.sendRequest("turn/steer", paramsJSON, a.APITimeout())
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
	case CodexSandboxDangerFullAccess:
		obj = map[string]interface{}{"type": "dangerFullAccess"}
	case CodexSandboxWorkspaceWrite:
		obj = map[string]interface{}{"type": "workspaceWrite"}
	case CodexSandboxReadOnly:
		obj = map[string]interface{}{"type": "readOnly"}
	default:
		return nil
	}
	obj["networkAccess"] = networkAccess == CodexNetworkEnabled
	return obj
}

// codexCollaborationModeObject converts a simple collaboration mode string to
// the object format expected by turn/start's collaborationMode field.
// We send developer_instructions: null so Codex applies its built-in mode
// instructions, matching the native Codex TUI/Desktop behavior.
func codexCollaborationModeObject(mode, model, effort string) map[string]interface{} {
	if mode == "" {
		return nil
	}
	switch mode {
	case CodexCollaborationDefault, CodexCollaborationPlan:
	default:
		return nil
	}
	reasoningEffort := interface{}(nil)
	if effort != "" {
		reasoningEffort = effort
	}
	return map[string]interface{}{
		"mode": mode,
		"settings": map[string]interface{}{
			"model":                  model,
			"reasoning_effort":       reasoningEffort,
			"developer_instructions": nil,
		},
	}
}

// codexServiceTierValue converts a stored service tier to the turn/thread
// wire value. A nil return omits the field and keeps Codex's normal tier.
func codexServiceTierValue(tier string) *string {
	switch tier {
	case "", CodexDefaultServiceTier:
		return nil
	case CodexServiceTierFast:
		v := CodexServiceTierFast
		return &v
	default:
		return nil
	}
}

// CurrentSettings returns the current settings for this agent.
func (a *CodexAgent) CurrentSettings() *leapmuxv1.AgentSettings {
	a.mu.Lock()
	defer a.mu.Unlock()
	return &leapmuxv1.AgentSettings{
		Model:          a.model,
		Effort:         a.effort,
		PermissionMode: a.approvalPolicy,
		ExtraSettings: map[string]string{
			CodexExtraSandboxPolicy:     a.sandboxPolicy,
			CodexExtraNetworkAccess:     a.networkAccess,
			CodexExtraCollaborationMode: a.collaborationMode,
			CodexExtraServiceTier:       a.serviceTier,
		},
	}
}

// AvailableModels returns the models reported by the Codex process.
func (a *CodexAgent) AvailableModels() []*leapmuxv1.AvailableModel {
	return a.availableModels
}

// AvailableOptionGroups returns the static Codex option groups.
func (a *CodexAgent) AvailableOptionGroups() []*leapmuxv1.AvailableOptionGroup {
	return AvailableOptionGroupsForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
}

// UpdateSettings stores new settings so the next turn/start picks them up,
// then refreshes from the Codex server to confirm the effective state.
func (a *CodexAgent) UpdateSettings(s *leapmuxv1.AgentSettings) bool {
	a.mu.Lock()
	if s.GetModel() != "" {
		a.model = s.GetModel()
	}
	if s.GetEffort() != "" {
		a.effort = s.GetEffort()
	}
	if s.GetPermissionMode() != "" {
		a.approvalPolicy = s.GetPermissionMode()
	}
	extras := s.GetExtraSettings()
	if v := extras[CodexExtraSandboxPolicy]; v != "" {
		a.sandboxPolicy = v
	}
	if v := extras[CodexExtraNetworkAccess]; v != "" {
		a.networkAccess = v
	}
	if v := extras[CodexExtraCollaborationMode]; v != "" {
		a.collaborationMode = v
	}
	if v := extras[CodexExtraServiceTier]; v != "" {
		a.serviceTier = v
	}
	a.mu.Unlock()

	a.refreshSettingsFromAgent()
	return true
}

// refreshSettingsFromAgent sends a config/read RPC to the Codex server and
// updates internal state with the effective config values.
func (a *CodexAgent) refreshSettingsFromAgent() {
	resp, err := a.sendRequest("config/read", json.RawMessage(`{"includeLayers":false}`), a.APITimeout())
	if err != nil {
		slog.Warn("codex config/read failed", "agent_id", a.agentID, "error", err)
		return
	}

	var result struct {
		Config struct {
			Model                json.RawMessage `json:"model"`
			ModelReasoningEffort json.RawMessage `json:"model_reasoning_effort"`
			ApprovalPolicy       json.RawMessage `json:"approval_policy"`
			SandboxMode          json.RawMessage `json:"sandbox_mode"`
			ServiceTier          json.RawMessage `json:"service_tier"`
		} `json:"config"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		slog.Warn("codex config/read unmarshal failed", "agent_id", a.agentID, "error", err)
		return
	}

	a.mu.Lock()
	// Each field may be null or a string; only update if it's a non-empty string.
	if s := jsonString(result.Config.Model); s != "" {
		a.model = s
	}
	if s := jsonString(result.Config.ModelReasoningEffort); s != "" {
		a.effort = s
	}
	// approval_policy can be a string ("on-request") or an object ({"granular":...}).
	// Only update for the simple string case that LeapMux models.
	if s := jsonString(result.Config.ApprovalPolicy); s != "" {
		a.approvalPolicy = s
	}
	if s := jsonString(result.Config.SandboxMode); s != "" {
		a.sandboxPolicy = s
	}
	if s := jsonString(result.Config.ServiceTier); s != "" {
		a.serviceTier = s
	}
	a.mu.Unlock()

	slog.Info("codex agent settings refreshed",
		"agent_id", a.agentID,
		"model", a.model,
		"effort", a.effort,
		"approvalPolicy", a.approvalPolicy,
		"sandboxPolicy", a.sandboxPolicy,
		"serviceTier", a.serviceTier,
	)

	a.sink.BroadcastSettingsRefreshed(a.model, a.effort, a.approvalPolicy, map[string]string{
		CodexExtraSandboxPolicy:     a.sandboxPolicy,
		CodexExtraNetworkAccess:     a.networkAccess,
		CodexExtraCollaborationMode: a.collaborationMode,
		CodexExtraServiceTier:       a.serviceTier,
	})
}

// jsonString attempts to unmarshal a JSON value as a plain string.
// Returns "" if the value is null, not a string, or empty.
func jsonString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
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

		// Prefer our curated metadata over the API's, which often
		// returns the raw model ID (e.g. "gpt-5.4" instead of "GPT-5.4").
		var displayName string
		var description string
		var contextWindow int64
		if d, ok := defaultsByID[id]; ok {
			displayName = d.DisplayName
			description = d.Description
			contextWindow = d.ContextWindow
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
			ContextWindow:    contextWindow,
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
	{Id: "gpt-5.4", DisplayName: "GPT-5.4", Description: "Latest frontier agentic coding model", IsDefault: true, DefaultEffort: "high", SupportedEfforts: codexDefaultEfforts, ContextWindow: 1_050_000},
	{Id: "gpt-5.4-mini", DisplayName: "GPT-5.4 Mini", Description: "Smaller frontier agentic coding model", DefaultEffort: "high", SupportedEfforts: codexDefaultEfforts, ContextWindow: 400_000},
	{Id: "gpt-5.3-codex", DisplayName: "GPT-5.3 Codex", Description: "Frontier Codex-optimized agentic coding model", DefaultEffort: "high", SupportedEfforts: codexDefaultEfforts, ContextWindow: 400_000},
	{Id: "gpt-5.2-codex", DisplayName: "GPT-5.2 Codex", Description: "Frontier agentic coding model", DefaultEffort: "high", SupportedEfforts: codexDefaultEfforts, ContextWindow: 400_000},
	{Id: "gpt-5.2", DisplayName: "GPT-5.2", Description: "Optimized for professional work and long-running agents", DefaultEffort: "high", SupportedEfforts: codexDefaultEfforts, ContextWindow: 256_000},
	{Id: "gpt-5.1-codex-max", DisplayName: "GPT-5.1 Codex Max", Description: "Codex-optimized model for deep and fast reasoning", DefaultEffort: "high", SupportedEfforts: codexDefaultEfforts, ContextWindow: 400_000},
	{Id: "gpt-5.1-codex-mini", DisplayName: "GPT-5.1 Codex Mini", Description: "Optimized for Codex; cheaper, faster, but less capable", DefaultEffort: "high", SupportedEfforts: codexDefaultEfforts, ContextWindow: 400_000},
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
				Key:   CodexExtraServiceTier,
				Label: "Fast Mode",
				Options: []*leapmuxv1.AvailableOption{
					{Id: CodexServiceTierFast, Name: "On", Description: "Use Codex fast mode for future turns"},
					{Id: CodexDefaultServiceTier, Name: "Off", Description: "Use the normal/default service tier", IsDefault: true},
				},
			},
			{
				Key:   CodexExtraCollaborationMode,
				Label: "Workflow",
				Options: []*leapmuxv1.AvailableOption{
					{Id: CodexCollaborationDefault, Name: "Default", IsDefault: true},
					{Id: CodexCollaborationPlan, Name: "Plan Mode"},
				},
			},
			{
				Key:   OptionGroupKeyPermissionMode,
				Label: "Approval Policy",
				Options: []*leapmuxv1.AvailableOption{
					{Id: "never", Name: "Full Auto"},
					{Id: CodexDefaultApprovalPolicy, Name: "Suggest & Approve", IsDefault: true},
					{Id: "untrusted", Name: "Auto-edit"},
				},
			},
			{
				Key:   CodexExtraSandboxPolicy,
				Label: "Sandbox Policy",
				Options: []*leapmuxv1.AvailableOption{
					{Id: CodexSandboxDangerFullAccess, Name: "Full Access", Description: "No filesystem restrictions"},
					{Id: CodexSandboxWorkspaceWrite, Name: "Workspace Write", Description: "Write only within the working directory", IsDefault: true},
					{Id: CodexSandboxReadOnly, Name: "Read Only", Description: "No write access to the filesystem"},
				},
			},
			{
				Key:   CodexExtraNetworkAccess,
				Label: "Network Access",
				Options: []*leapmuxv1.AvailableOption{
					{Id: CodexNetworkRestricted, Name: "Restricted", Description: "No network access from the sandbox", IsDefault: true},
					{Id: CodexNetworkEnabled, Name: "Enabled", Description: "Allow network access from the sandbox"},
				},
			},
		},
		"LEAPMUX_CODEX_DEFAULT_MODEL",
		"LEAPMUX_CODEX_DEFAULT_EFFORT",
		"codex",
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
			suffixParts[i] = capitalizeFirst(p)
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

func (a *CodexAgent) handleOutput(line *parsedLine) {
	handleCodexOutput(a, line)
}

// HandleOutput processes a single JSONL notification from Codex.
func (a *CodexAgent) HandleOutput(content []byte) {
	handleCodexOutput(a, parseLine(content))
}
