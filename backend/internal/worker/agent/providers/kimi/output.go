package kimi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Kimi Code events identify their native agent: main or a subagent such as agent-1.
// The same handlers serve both. Each run keeps its own output state and writes to its own transcript.
// subagent.go links native subagents to their child transcripts.
//
// Native tool frames open and close spans. A finished native turn supplies the turn divider.
// The notification contract identifies stored notices. Compaction starts use live session state, and only their ends become rows.
//
// The Worker buffers native text and thoughts until their step finishes.
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
	childInvocations   map[kimiTranscriptToolIdentity]*kimiChildInvocation
	// transcript keeps native v2 identities and text until each step finishes.
	transcript kimiTranscript
	// agentID is the server's id for the agent: `main`, or `agent-N`.
	agentID string
	// generation holds the text and thinking the agent streamed since the last
	// boundary.
	generation providerkit.GenerationBuffer
	retry      providerkit.RetryResolution
	// dispatchMu protects the live compaction flag.
	compactionLive bool
	// tools holds every tool call the agent opened and nothing closed yet,
	// keyed by the server's tool call id.
	tools map[string]*kimiTool
	// nextOrder orders the tool calls for a turn-end sweep.
	nextOrder uint64
	// turnActive is true while the agent runs a turn.
	turnActive bool
	// userTurn distinguishes a user's child turn, which owns its completion,
	// from a non-user turn whose coordinator outcome remains independent.
	userTurn bool
	// started is true once the subagent ran a turn, so a later turn's prompt is a
	// message delivered in the middle of the transcript.
	started                bool
	pendingNative          []kimiFrame
	pendingTodo            *kimiTodoSnapshot
	pendingNativeRecovery  bool
	pendingNativeBytes     int64
	nativeRecoveryRevision uint64
}

// kimiTool is one open tool call.
type kimiTool struct {
	spanID string
	name   string
	order  uint64
	// lastFrame is the latest native tool record. A turn that ends while the
	// call runs stores it unchanged, with the completion column stating how
	// the turn ended and any collected output in its scoped supplement.
	lastFrame []byte
	// outputBytes and tail are the running output, for the live counter.
	outputBytes int64
	outputExact bool
	tail        string
	tailLost    bool
}

// kimiSpawn is the span of one Agent or AgentSwarm call, and what its input
// states about the subagents it starts.
type kimiSpawn struct {
	spanID string
	name   string
	// prompt is an Agent call's prompt, which opens its child transcript.
	prompt string
	// label is an AgentSwarm call's description, which heads its workflow group.
	label string
}

// kimiMaxPendingEvents caps events held before a child reference arrives.
// Native transcript overflow requires a snapshot after adoption; it does not identify a side agent.
const kimiMaxPendingEvents = 256

// runLocked returns the run of agentID, creating it on first use. The caller
// holds a.Mu.
func (a *Agent) runLocked(agentID string) *kimiRun {
	if a.runs == nil {
		a.runs = make(map[string]*kimiRun)
	}
	run := a.runs[agentID]
	if run == nil {
		run = &kimiRun{agentID: agentID, tools: make(map[string]*kimiTool)}
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

// --- turns ---

// kimiOrigin states who started a turn.
type kimiOrigin struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// kimiOriginIsNotable reports whether a turn of this origin needs a row that
// states why it started.
//
// User messages and native task notification frames already explain their turns.
// Other origins retain the cause of a turn. Skill and cron markers separately
// describe activations and input delivery, which do not always start a turn.
func kimiOriginIsNotable(kind string) bool {
	switch kind {
	case contracts.KimiOriginUser, contracts.KimiOriginTask, contracts.KimiOriginBackgroundTask, "":
		return false
	default:
		return true
	}
}

// kimiOriginTakesSteer reports whether a main turn of this origin takes a
// steer.
//
// The server steers into a turn only when a tracked prompt started it
// (loopService.steer). Only the user's own prompt, a skill activation and a
// plugin command submit a tracked prompt to the main agent. A goal
// continuation, a task notification, a cron fire, a retry and a hook submit an
// untracked one. A steer into such a turn fails with PROMPT_NOT_FOUND, and the
// steered text waits in the server's queue behind the turn.
func kimiOriginTakesSteer(kind string) bool {
	switch kind {
	case contracts.KimiOriginUser, contracts.KimiOriginSkillActivation, contracts.KimiOriginPluginCommand:
		return true
	default:
		return false
	}
}

// kimiEventErr holds the diagnostic fields preserved in a native notice marker.
type kimiEventErr struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// kimiTurnCompletion maps a turn's end reason onto LeapMux's completion.
func kimiTurnCompletion(reason string) agent.MessageCompletion {
	switch reason {
	case contracts.KimiTranscriptTurnCompleted:
		return agent.MessageCompletionComplete
	case contracts.KimiTranscriptTurnCancelled:
		return agent.MessageCompletionInterrupted
	case contracts.KimiTranscriptTurnFailed:
		return agent.MessageCompletionError
	default:
		// A reason this build does not know. The turn did not state that it
		// completed.
		return agent.MessageCompletionError
	}
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

// --- text and thinking ---

// flushRun persists a run's buffered thinking and text, in the order they
// streamed. A subagent no spawn linked yet keeps its buffer.
func (a *Agent) flushRun(run *kimiRun, sink agent.ProviderServices, completion agent.MessageCompletion) {
	if sink == nil {
		return
	}
	scopes := run.generation.ScopeIDs()
	discarding := a.IsDiscardingOutput()
	if err := providerkit.PersistGenerationScopes(&run.generation, sink, scopes, completion, discarding); err != nil {
		slog.Error("kimi persist generation", "agent_id", a.AgentID(), "kimi_agent", run.agentID, "error", err)
	}
	if !discarding {
		a.recordFlushedTranscriptScopes(run, scopes)
	}
}

// --- tool calls ---

// kimiToolSpawns reports whether a tool call starts subagents. A spawn owns no
// span: its subagents' work lands in child transcripts, and a rail held open for
// the whole run would push every concurrent call one column right.
func kimiToolSpawns(name string) bool {
	return name == contracts.KimiToolAgent || name == contracts.KimiToolAgentSwarm
}

// kimiSpawnFromArgs reads what a spawn's input states about its subagents.
func kimiSpawnFromArgs(spanID, name string, args json.RawMessage) kimiSpawn {
	var input struct {
		Prompt      string `json:"prompt"`
		Description string `json:"description"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &input); err != nil {
			slog.Debug("kimi spawn input does not decode", "tool", name, "error", err)
		}
	}
	spawn := kimiSpawn{spanID: spanID, name: name, label: strings.TrimSpace(input.Description)}
	if name == contracts.KimiToolAgent {
		spawn.prompt = strings.TrimSpace(input.Prompt)
	}
	return spawn
}

func (a *Agent) recordToolProgress(tool *kimiTool, sink agent.ProviderServices, text string) {
	if tool == nil || text == "" {
		return
	}
	a.Mu.Lock()
	tool.outputBytes = agent.SaturatingAdd(tool.outputBytes, int64(len(text)))
	var clipped bool
	tool.tail, clipped = agent.ClipTailBytes(tool.tail+text, kimiLiveOutputLimit)
	if clipped {
		tool.tailLost = true
	}
	a.Mu.Unlock()
	a.reportToolProgress(tool, sink)
}

func (a *Agent) reportToolProgress(tool *kimiTool, sink agent.ProviderServices) {
	a.Mu.Lock()
	spanID, total, tail, lost, exact := tool.spanID, tool.outputBytes, tool.tail, tool.tailLost, tool.outputExact
	a.Mu.Unlock()
	if exact {
		sink.ReportProgress(agent.OutputExactTotalProgress(spanID, total))
	} else {
		sink.ReportProgress(agent.OutputTotalProgress(spanID, total, true))
	}
	sink.ReportProgress(agent.OutputTailProgress(spanID, tail, lost))
}

// closeOpenTools ends every call a turn left open. The server reports a result
// for a call the user aborted, so one left here means the turn ended in a way
// the server did not report per call. Each call's own opening payload becomes
// its last row, and the completion column states how the turn ended. The output
// the call printed before it stopped is retained with that row.
func (a *Agent) closeOpenTools(run *kimiRun, sink agent.ProviderServices, completion agent.MessageCompletion) {
	if sink == nil {
		return
	}
	open := a.takeOpenToolCalls(run, func(*kimiTool) bool { return true })
	a.closeToolCalls(sink, open, completion)
}

// takeOpenToolCalls removes matching calls while Mu protects their ownership.
func (a *Agent) takeOpenToolCalls(run *kimiRun, matches func(*kimiTool) bool) []*kimiTool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	var calls []*kimiTool
	for key, tool := range run.tools {
		if matches(tool) {
			calls = append(calls, tool)
			delete(run.tools, key)
		}
	}
	return calls
}

func (a *Agent) closeToolCalls(sink agent.ProviderServices, open []*kimiTool, completion agent.MessageCompletion) {
	if sink == nil {
		return
	}
	sort.Slice(open, func(i, j int) bool { return open[i].order < open[j].order })
	for _, tool := range open {
		retained := a.IsDiscardingOutput()
		a.ClearCumulativeOutput(tool.spanID)
		sink.ReportProgress(agent.CompleteOutputProgress(tool.spanID))
		if !a.IsDiscardingOutput() {
			a.Mu.Lock()
			frame, tail, lost := tool.lastFrame, tool.tail, tool.tailLost
			a.Mu.Unlock()
			content, native := kimiRetainedTranscriptToolContent(frame, tail, lost)
			if !native {
				slog.Error("decode retained Kimi transcript tool", "agent_id", a.AgentID(), "span_id", tool.spanID)
				content.Original = frame
			}
			content.Completion = completion
			err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{
				SpanID: tool.spanID, SpanType: tool.name, Closing: true,
			})
			retained = agent.OutputRetained(err)
			if err != nil {
				slog.Error("kimi persist incomplete tool call", "agent_id", a.AgentID(), "span_id", tool.spanID, "error", err)
			}
		}
		if retained {
			a.Mu.Lock()
			tool.lastFrame = nil
			tool.tail = ""
			a.Mu.Unlock()
		}
		sink.CloseSpan(tool.spanID)
	}
}

// --- notices ---

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
