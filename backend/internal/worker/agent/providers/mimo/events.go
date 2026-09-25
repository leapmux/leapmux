package mimo

import (
	"encoding/json"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
)

// Event types that only the worker reads. The browser never receives them: the
// worker turns each one into a transcript row of another shape, a registry row,
// a control request, or nothing. The types the worker persists verbatim, which
// the browser reads too, are in contracts/mimo-protocol.json. The question
// events are MiMo's copy of OpenCode's, and come from opencode-protocol.json.
const (
	eventServerConnected        = "server.connected"
	eventServerHeartbeat        = "server.heartbeat"
	eventMessageUpdated         = "message.updated"
	eventMessagePartDelta       = "message.part.delta"
	eventSessionCompacted       = "session.compacted"
	eventSessionGoal            = "session.goal"
	eventPermissionReplied      = "permission.replied"
	eventBashInteractiveAsked   = "bash.interactive.asked"
	eventBashInteractiveReplied = "bash.interactive.replied"
	eventActorRegistered        = "actor.registered"
	eventActorStatus            = "actor.status"
	eventWorkflowStarted        = "workflow.started"
	eventWorkflowPhase          = "workflow.phase"
	eventWorkflowFinished       = "workflow.finished"
)

// mimoIgnoredEvents are the event types the worker reads and acts on nowhere.
// Each is here for a stated reason, so a type the server adds later reaches the
// default branch of dispatchEvent and is logged instead of vanishing silently.
var mimoIgnoredEvents = map[string]string{
	"session.idle":              "a deprecated repeat of session.status idle",
	"session.created":           "the create route already returned the session",
	"session.updated":           "LeapMux titles an agent itself; a title the model wrote would fight that",
	"session.deleted":           "LeapMux deletes no MiMo session",
	"session.diff":              "the transcript already holds each edit",
	"session.cwd":               "the working directory is LeapMux's to choose",
	"session.retry.attempt":     "session.status retry carries the same attempt, and it is what LeapMux persists",
	"session.try_best.detected": "a TUI hint about the model's effort",
	"message.removed":           "LeapMux offers no revert, so it never asks for a removal",
	"message.part.removed":      "LeapMux offers no revert, so it never asks for a removal",
	"command.executed":          "the goal and compaction routes report their own effect",
	"inbox.arrived":             "actor.status states the same completion, and the parent turn it starts reports itself",
	"actor.stuck":               "legacy; the server never emits it",
	"actor.stalled":             "a watchdog hint; actor.status states the lifecycle",
	"writer.cache_perf":         "checkpoint-writer telemetry",
	"workflow.log":              "the workflow tool's own result carries its transcript",
	"workflow.agent_failed":     "the failed actor's own actor.status ends its row",
	"workflow.child_failed":     "workflow.finished ends the parent run's row",
	"task.created":              "the task tool's own result row carries the to-do change",
	"task.updated":              "the task tool's own result row carries the to-do change",
	"todo.updated":              "nothing publishes it in production",
	"metrics.model_call":        "telemetry",
	"metrics.tool_call":         "telemetry",
	"metrics.agent_request":     "telemetry",
	"metrics.try_best_detected": "telemetry",
	"hook.executed":             "plugin telemetry",
	"hook.react.max_reached":    "plugin telemetry",
	"hook.react.reentered":      "plugin telemetry",
	"file.edited":               "the edit tool's own row states the edit",
	"file.watcher.updated":      "the worker watches the working tree itself",
	"tui.toast.show":            "a TUI notice; the events it summarizes reach the transcript themselves",
	"tui.command.execute":       "a TUI command",
	"tui.prompt.append":         "a TUI command",
	"tui.session.select":        "a TUI command",
	"tui.instructions.loaded":   "a TUI notice",
	"lsp.updated":               "language-server state",
	"lsp.client.diagnostics":    "language-server state",
	"vcs.branch.updated":        "the worker reads the branch itself",
	"project.updated":           "project metadata",
	"installation.updated":      "the update check runs in the TUI only",
	"ide.installed":             "an IDE integration",
	"mcp.tools.changed":         "the next tool call states the tool it runs",
	"mcp.browser.open.failed":   "an OAuth flow LeapMux does not run",
	"pty.created":               "a TUI terminal",
	"pty.updated":               "a TUI terminal",
	"pty.exited":                "a TUI terminal",
	"pty.deleted":               "a TUI terminal",
	"team.created":              "the actor events state each member",
	"team.member.joined":        "the actor events state each member",
	"worktree.ready":            "isolated worktrees are an orchestrator feature LeapMux does not drive",
	"worktree.failed":           "isolated worktrees are an orchestrator feature LeapMux does not drive",
	"workspace.ready":           "a control-plane workspace",
	"workspace.failed":          "a control-plane workspace",
	"workspace.status":          "a control-plane workspace",
	"workspace.restore":         "a control-plane workspace",
	"global.disposed":           "the process is ending, and the stream ends with it",
	"server.instance.disposed":  "the process is ending, and the stream ends with it",
}

// mimoEvent is one event of the stream, with its payload still raw.
type mimoEvent struct {
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`
	// raw is the whole event as the server sent it. A row that stores an event
	// verbatim stores these bytes.
	raw []byte
}

// parseEvent decodes one event. ok is false for a payload that is not an event.
func parseEvent(data []byte) (mimoEvent, bool) {
	var event mimoEvent
	if err := json.Unmarshal(data, &event); err != nil || event.Type == "" {
		return mimoEvent{}, false
	}
	event.raw = append([]byte(nil), data...)
	return event, true
}

// mimoStatus is the `status` of session.status, and one value of the status map
// that GET /session/status returns.
type mimoStatus struct {
	Type    string `json:"type"`
	Attempt int    `json:"attempt"`
	Message string `json:"message"`
	// Next is when the next attempt runs, in epoch milliseconds.
	Next int64 `json:"next"`
}

type mimoStatusEvent struct {
	SessionID string     `json:"sessionID"`
	Status    mimoStatus `json:"status"`
}

// mimoTokens is the token count of an assistant message and of a step.
type mimoTokens struct {
	Total     int64 `json:"total"`
	Input     int64 `json:"input"`
	Output    int64 `json:"output"`
	Reasoning int64 `json:"reasoning"`
	Cache     struct {
		Read  int64 `json:"read"`
		Write int64 `json:"write"`
	} `json:"cache"`
}

// mimoMessageInfo is the `info` of message.updated.
type mimoMessageInfo struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionID"`
	Role      string `json:"role"`
	// AgentID is the actor the message belongs to: "main" for the main agent,
	// or a subagent's actor id. The first update of a user message can omit it.
	AgentID string `json:"agentID"`
	Agent   string `json:"agent"`
	// Summary is `true` on the assistant message that a compaction writes, and an
	// object on a user message, so it stays raw.
	Summary json.RawMessage `json:"summary"`
	// Error is the failure that ended an assistant message.
	Error      *mimoError  `json:"error"`
	Cost       float64     `json:"cost"`
	Tokens     *mimoTokens `json:"tokens"`
	ModelID    string      `json:"modelID"`
	ProviderID string      `json:"providerID"`
	Variant    string      `json:"variant"`
	Time       struct {
		Created   int64 `json:"created"`
		Completed int64 `json:"completed"`
	} `json:"time"`
}

// isCompactionSummary reports whether the message is the summary that a
// compaction wrote. Its text is the new context, which the compaction row
// already states, so the worker persists none of its parts.
func (m mimoMessageInfo) isCompactionSummary() bool {
	return string(m.Summary) == "true"
}

type mimoMessageEvent struct {
	SessionID string          `json:"sessionID"`
	Info      mimoMessageInfo `json:"info"`
}

// mimoMessageWithParts is one entry of GET /session/:id/message.
type mimoMessageWithParts struct {
	Info  mimoMessageInfo `json:"info"`
	Parts []mimoPart      `json:"parts"`
}

// Part types that only the worker reads: the worker assembles the text and the
// reasoning into its own rows, and reads the tokens off a finished step.
const (
	partTypeText       = "text"
	partTypeReasoning  = "reasoning"
	partTypeStepFinish = "step-finish"
)

// mimoPart is the `part` of message.part.updated, across every part type.
type mimoPart struct {
	ID        string `json:"id"`
	MessageID string `json:"messageID"`
	SessionID string `json:"sessionID"`
	Type      string `json:"type"`

	// text and reasoning
	Text      string `json:"text"`
	Synthetic bool   `json:"synthetic"`
	Ignored   bool   `json:"ignored"`
	Time      *struct {
		Start int64 `json:"start"`
		End   int64 `json:"end"`
	} `json:"time"`

	// tool
	Tool   string         `json:"tool"`
	CallID string         `json:"callID"`
	State  *mimoToolState `json:"state"`

	// compaction
	Projection json.RawMessage `json:"projection"`

	// step-finish
	Tokens *mimoTokens `json:"tokens"`
}

// ended reports whether a text or reasoning part is complete.
func (p mimoPart) ended() bool {
	return p.Time != nil && p.Time.End != 0
}

// mimoToolState is the `state` of a tool part.
type mimoToolState struct {
	Status   string          `json:"status"`
	Input    json.RawMessage `json:"input"`
	Output   string          `json:"output"`
	Error    string          `json:"error"`
	Title    string          `json:"title"`
	Metadata json.RawMessage `json:"metadata"`
}

// final reports whether the call reached a final state.
func (s *mimoToolState) final() bool {
	return s != nil && (s.Status == contracts.MiMoToolStatusCompleted || s.Status == contracts.MiMoToolStatusError)
}

type mimoPartEvent struct {
	SessionID string   `json:"sessionID"`
	Part      mimoPart `json:"part"`
}

type mimoPartDelta struct {
	SessionID string `json:"sessionID"`
	MessageID string `json:"messageID"`
	PartID    string `json:"partID"`
	Field     string `json:"field"`
	Delta     string `json:"delta"`
}

// mimoError is a named error, as session.error and a failed message state it.
type mimoError struct {
	Name string `json:"name"`
	Data struct {
		Message     string `json:"message"`
		IsRetryable bool   `json:"isRetryable"`
	} `json:"data"`
}

// mimoErrorEvent is session.error. The session id is absent for an error that
// no session owns.
type mimoErrorEvent struct {
	SessionID string    `json:"sessionID"`
	Error     mimoError `json:"error"`
}

// Errors that do not fail a turn.
const (
	// errorNameAborted is the error a session reports for a turn that an abort
	// ended. The worker asked for that abort, so it is no failure.
	errorNameAborted = "MessageAbortedError"
	// errorNameContextOverflow reports a context that no longer fits. MiMo
	// compacts it and continues the same turn, and the compaction reports itself.
	errorNameContextOverflow = "ContextOverflowError"
)

// dispatchEvent routes one event of the stream. Only the stream goroutine calls
// it, so two events never race each other.
func (a *Agent) dispatchEvent(data []byte) {
	event, ok := parseEvent(data)
	if !ok {
		slog.Warn("mimo event is not an event", "agent_id", a.AgentID(), "len", len(data))
		return
	}
	if a.IsDiscardingOutput() {
		return
	}
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	switch event.Type {
	case eventServerConnected, eventServerHeartbeat:
		// The stream loop reads the first; the second only keeps the stream open.
	case contracts.MiMoEventSessionStatus:
		a.handleSessionStatus(event)
	case contracts.MiMoEventSessionError:
		a.handleSessionError(event)
	case eventMessageUpdated:
		a.handleMessageUpdated(event)
	case contracts.MiMoEventMessagePartUpdated:
		a.handlePartUpdated(event)
	case eventMessagePartDelta:
		a.handlePartDelta(event)
	case eventSessionCompacted:
		// The compaction part states the same boundary, and it carries the summary.
	case eventSessionGoal:
		a.handleSessionGoal(event)
	case contracts.MiMoEventPermissionAsked:
		a.handlePermissionAsked(event.Properties)
	case eventPermissionReplied:
		a.handlePermissionReplied(event)
	case contracts.OpenCodeEventQuestionAsked:
		a.handleQuestionAsked(event.Properties)
	case contracts.OpenCodeEventQuestionReplied, contracts.OpenCodeEventQuestionRejected:
		a.handleQuestionSettled(event)
	case eventBashInteractiveAsked:
		a.handleBashInteractiveAsked(event.Properties)
	case eventBashInteractiveReplied:
		// The reply LeapMux sent; nothing is left to do.
	case eventActorRegistered:
		a.handleActorRegistered(event)
	case eventActorStatus:
		a.handleActorStatus(event)
	case eventWorkflowStarted, eventWorkflowPhase, eventWorkflowFinished:
		a.handleWorkflowEvent(event)
	default:
		if _, ignored := mimoIgnoredEvents[event.Type]; ignored {
			return
		}
		slog.Debug("mimo unknown event type", "agent_id", a.AgentID(), "type", event.Type)
	}
}
