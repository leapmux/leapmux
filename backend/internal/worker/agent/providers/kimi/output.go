package kimi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Kimi Code events identify their native agent: main or a subagent such as agent-1.
// The same handlers serve both. Each run keeps its own output state and writes to its own transcript.
// subagent.go links native subagents to their child transcripts.
//
// Native tool frames open and close spans. A finished native turn supplies the turn divider.
// The notification contract identifies stored notices. Compaction starts use live session state, and only their ends become rows.
//
// The Worker retains public item deltas until a tool, control, or turn boundary needs the text.
// Profile warnings read through REST use the native warning-event shape.

// kimiLiveOutputLimit caps the running command output the worker holds for the
// live tail. The sink caps the tail again before it broadcasts; this cap keeps
// the joined string from growing without limit for a command that prints for
// minutes.
const kimiLiveOutputLimit = 8192

// kimiRun is the output state of one agent of the session.
type kimiRun struct {
	// Native parent identity remains stable while physical child lifetimes renew.
	childParentAgentID string
	childHost          *kimiChildHost
	childLifetime      *kimiChildLifetime
	childGraph         *kimiChildLifetimeGraph
	childInvocations   map[kimiInvocationIdentity]*kimiChildInvocation
	// dispatchMu protects readiness callbacks that await idle input admission.
	readyPromptSubmissions []*kimiPromptSubmission
	conversation           *kimiConversationOutput
	conversationRecovery   atomic.Pointer[kimiConversationRecovery]
	// agentID is the server's id for the agent: `main`, or `agent-N`.
	agentID string
	// turnActive is true while the agent runs a turn.
	turnActive bool
	// Mu protects the native compaction state used by input admission.
	compacting bool
	// userTurn distinguishes a user's child turn, which owns its completion,
	// from a non-user turn whose coordinator outcome remains independent.
	userTurn    bool
	pendingTodo *kimiTodoSnapshot
}

// kimiSpawn is the span of one Agent or AgentSwarm call, and what its input
// states about the subagents it starts.
type kimiSpawn struct {
	spanID string
	name   string
	// label is an AgentSwarm call's description, which heads its workflow group.
	label string
}

// runLocked returns the run of agentID, creating it on first use. The caller
// holds a.Mu.
func (a *Agent) runLocked(agentID string) *kimiRun {
	if a.runs == nil {
		a.runs = make(map[string]*kimiRun)
	}
	run := a.runs[agentID]
	if run == nil {
		run = &kimiRun{agentID: agentID}
		a.runs[agentID] = run
	}
	return run
}

func (a *Agent) run(agentID string) *kimiRun {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.runLocked(agentID)
}

// runSink returns the transcript a run's rows go to: this agent's own for the
// main agent, a child transcript for a linked subagent, or nil for a subagent no
// spawn linked yet.
func (a *Agent) runSink(run *kimiRun) agent.ProviderServices {
	if run.agentID == kimiMainAgentID {
		return a.sink
	}
	if _, ok := a.children.childOf(run.agentID); ok {
		if run.childLifetime != nil {
			return run.childLifetime.writer
		}
	}
	return nil
}

// kimiWithContextUsage adds the last context readout to a turn end's metadata,
// so a reconnecting client reads it off the persisted row.
func kimiWithContextUsage(content agent.MessageContent, usage map[string]any) agent.MessageContent {
	if len(usage) == 0 {
		return content
	}
	var fields map[string]json.RawMessage
	if len(content.Metadata) > 0 && json.Unmarshal(content.Metadata, &fields) != nil {
		return content
	}
	if fields == nil {
		fields = make(map[string]json.RawMessage)
	}
	encoded, err := json.Marshal(usage)
	if err != nil {
		slog.Warn("kimi encode context usage metadata", "error", err)
		return content
	}
	fields[contracts.SessionInfoKeyContextUsage] = encoded
	metadata, err := json.Marshal(fields)
	if err != nil {
		slog.Warn("kimi encode turn end metadata", "error", err)
		return content
	}
	content.Metadata = metadata
	return content
}

// kimiToolSpawns reports whether a tool call starts subagents. A spawn owns no
// span: its subagents' work lands in child transcripts, and a rail held open for
// the whole run would push every concurrent call one column right.
func kimiToolSpawns(name string) bool {
	return name == contracts.KimiToolAgent || name == contracts.KimiToolAgentSwarm
}

// kimiSpawnFromArgs reads what a spawn's input states about its subagents.
func kimiSpawnFromArgs(spanID, name string, args json.RawMessage) kimiSpawn {
	var input struct {
		Description string `json:"description"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &input); err != nil {
			slog.Debug("kimi spawn input does not decode", "tool", name, "error", err)
		}
	}
	return kimiSpawn{spanID: spanID, name: name, label: strings.TrimSpace(input.Description)}
}

var kimiNotificationEvents = func() map[string]bool {
	members := make(map[string]bool, len(contracts.KimiNotificationEventMembers))
	for _, kind := range contracts.KimiNotificationEventMembers {
		members[kind] = true
	}
	return members
}()

// persistEvent returns whether the output queue accepted or already retained the event.
// Known notifications use one contract table. Unknown events remain separate message rows.
func (a *Agent) persistEvent(sink agent.ProviderServices, event kimiEvent) bool {
	if sink == nil || a.IsDiscardingOutput() {
		return false
	}
	warningKey := ""
	// Profile warnings belong to the main agent. A child's warning must not suppress the main agent's profile notice.
	if event.Type == contracts.KimiEventWarning && event.AgentID == kimiMainAgentID {
		var warning kimiSessionWarning
		if json.Unmarshal(event.Raw, &warning) == nil && strings.TrimSpace(warning.Message) != "" {
			a.Mu.Lock()
			sessionID := a.sessionID
			a.Mu.Unlock()
			warningKey = fmt.Sprintf("%d:%s%d:%s%s", len(sessionID), sessionID, len(warning.Code), warning.Code, warning.Message)
			if _, stored := a.storedSessionWarnings[warningKey]; stored {
				return true
			}
		}
	}
	content := agent.MessageContent{Original: event.Raw}
	var err error
	if kimiNotificationEvents[event.Type] {
		_, err = sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content)
	} else {
		err = sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{})
	}
	if err != nil {
		slog.Error("persist a Kimi event", "agent_id", a.AgentID(), "type", event.Type, "error", err)
	}
	if warningKey != "" && agent.OutputRetained(err) {
		if a.storedSessionWarnings == nil {
			a.storedSessionWarnings = make(map[string]struct{})
		}
		a.storedSessionWarnings[warningKey] = struct{}{}
	}
	return agent.OutputRetained(err)
}

// kimiSessionWarning is one warning from the session profile or its live warning event.
type kimiSessionWarning struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
}

// persistSessionWarnings restores warnings that the profile emitted before the subscription started.
func (a *Agent) persistSessionWarnings(ctx context.Context, sessionID string) {
	if err := kimiCheckID("session", sessionID); err != nil {
		slog.Debug("read Kimi session warnings", "agent_id", a.AgentID(), "error", err)
		return
	}
	var response struct {
		Warnings []json.RawMessage `json:"warnings"`
	}
	if err := a.api.get(ctx, kimiSessionPath(sessionID, "/warnings"), &response); err != nil {
		slog.Debug("read Kimi session warnings", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.dispatchMu.Lock()
	defer a.finishDispatch()
	a.Mu.Lock()
	current := a.sessionID
	a.Mu.Unlock()
	if current != sessionID {
		return
	}
	for _, raw := range response.Warnings {
		var warning kimiSessionWarning
		if json.Unmarshal(raw, &warning) != nil || strings.TrimSpace(warning.Message) == "" {
			continue
		}
		content, err := json.Marshal(map[string]any{
			"type": contracts.KimiEventWarning, "agentId": kimiMainAgentID, "sessionId": sessionID,
			"code": warning.Code, "message": warning.Message,
		})
		if err != nil {
			slog.Error("encode a Kimi session warning", "agent_id", a.AgentID(), "error", err)
			continue
		}
		a.persistEvent(a.sink, kimiEvent{Type: contracts.KimiEventWarning, AgentID: kimiMainAgentID, Raw: content})
	}
}
