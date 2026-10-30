package zcode

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// ZCode's subagents.
//
// The `Agent` tool starts one, and the app-server then reports the subagent's OWN
// tool calls as ordinary `tool.updated` events on the parent session's stream. Each
// one is marked: `source: "subagent"`, plus `agentId`, `agentType`,
// `childSessionId`, and -- the field that makes the linkage possible --
// `parentToolCallId`, which is the tool-call id of the `Agent` call that started it.
//
// LeapMux gives those events a transcript of their own. Left in the parent they
// bury the conversation: one `Explore` subagent produced eight tool cards for a
// question the user asked in one sentence, and the answer it reported is already in
// the `Agent` tool's own result. So the parent keeps the spawn card and the result,
// and the subagent's work goes to a child transcript that opens from it.
//
// Current builds can send the report through a subagent lifecycle event before the
// `Agent` result repeats it. The child transcript holds the prompt, tool cards, and
// one durable report identity across both paths.

// zcodeSpawnRecord is what zcodeChildIndex keeps for one spawn: the child
// transcript, its title, the tool calls that belong to it, and whether the child
// runs in the background.
type zcodeSpawnRecord struct {
	childAgentID      string
	childSessionID    string
	nativeOnly        bool
	title             string
	toolInvocationIDs map[string]struct{}
	background        bool
	// taskID is the background task that runs the subagent, which addresses
	// session/cancelBackgroundTask. A foreground subagent has none.
	taskID string
	// interrupted is true once LeapMux's own Interrupt cancelled the task, so the
	// cancellation closes the row as interrupted rather than stopped.
	interrupted bool
	// lifecycle is true once a lifecycle event of the subagent arrived. Such a
	// subagent also ends with a lifecycle event, which ZCode sends AFTER the
	// background task's final status, and which carries the report. So that
	// event, not the task's, ends the transcript.
	lifecycle bool
}

// zcodeChildIndex remembers which transcript a subagent's rows belong to.
//
// It answers two lookups, and both are needed because the two halves of a tool call
// state the linkage differently. A `scheduled` update gives its `parentToolCallId`,
// so the SPAWN keys the transcript. A `result` -- and above all a `batch` summary,
// which LeapMux synthesizes from a list of ids and nothing else -- gives only the
// tool call, so that id has to be enough on its own.
//
// The zero value is usable: every map is created on first write, under the lock.
type zcodeChildIndex struct {
	mu sync.Mutex
	// spawns owns every fact whose lifetime is one Agent call.
	spawns map[string]*zcodeSpawnRecord
	// toolSpawns resolves a child tool call back to its owning spawn record.
	toolSpawns map[string]string
	// sessionSpawns preserves the child route after an individual tool call ends.
	sessionSpawns map[string]string
	// ended holds the spawns whose subagent ended. A later event of such a spawn
	// creates no transcript again: a transcript that the end tore down and an
	// event re-created would stay open, and the tab would show the subagent
	// working for good.
	ended map[string]struct{}
}

func (i *zcodeChildIndex) rememberSession(spawnToolCallID, childSessionID string) {
	if spawnToolCallID == "" || childSessionID == "" {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.sessionSpawns == nil {
		i.sessionSpawns = make(map[string]string)
	}
	i.sessionSpawns[childSessionID] = spawnToolCallID
	i.spawnLocked(spawnToolCallID).childSessionID = childSessionID
}

func (i *zcodeChildIndex) sessionChild(childSessionID string) (string, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	record := i.spawns[i.sessionSpawns[childSessionID]]
	if record == nil || record.childAgentID == "" {
		return "", false
	}
	return record.childAgentID, true
}

func (i *zcodeChildIndex) spawnLocked(spawnToolCallID string) *zcodeSpawnRecord {
	if i.spawns == nil {
		i.spawns = make(map[string]*zcodeSpawnRecord)
	}
	record := i.spawns[spawnToolCallID]
	if record == nil {
		record = &zcodeSpawnRecord{}
		i.spawns[spawnToolCallID] = record
	}
	return record
}

func (i *zcodeChildIndex) child(spawnToolCallID string) (string, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	record := i.spawns[spawnToolCallID]
	if record == nil || record.childAgentID == "" {
		return "", false
	}
	return record.childAgentID, true
}

// rememberChild records the transcript of a spawn, and of one tool call inside it.
func (i *zcodeChildIndex) rememberChild(spawnToolCallID, toolCallID, childAgentID string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	record := i.spawnLocked(spawnToolCallID)
	record.childAgentID = childAgentID
	if toolCallID == "" {
		return
	}
	if record.toolInvocationIDs == nil {
		record.toolInvocationIDs = make(map[string]struct{})
	}
	record.toolInvocationIDs[toolCallID] = struct{}{}
	if i.toolSpawns == nil {
		i.toolSpawns = make(map[string]string)
	}
	i.toolSpawns[toolCallID] = spawnToolCallID
}

// toolChild returns the transcript one of a subagent's tool calls belongs to.
func (i *zcodeChildIndex) toolChild(toolCallID string) (string, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	spawnToolCallID := i.toolSpawns[toolCallID]
	record := i.spawns[spawnToolCallID]
	if record == nil || record.childAgentID == "" {
		return "", false
	}
	return record.childAgentID, true
}

// forgetTool drops a finished tool call. The transcript stays: the subagent is still
// running, and its next call belongs to the same one.
func (i *zcodeChildIndex) forgetTool(toolCallID string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	spawnToolCallID := i.toolSpawns[toolCallID]
	delete(i.toolSpawns, toolCallID)
	if record := i.spawns[spawnToolCallID]; record != nil {
		delete(record.toolInvocationIDs, toolCallID)
	}
}

// takeChild returns a spawn's transcript and drops it, when the spawn ends.
func (i *zcodeChildIndex) takeChild(spawnToolCallID string) (string, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.ended == nil {
		i.ended = make(map[string]struct{})
	}
	i.ended[spawnToolCallID] = struct{}{}
	record := i.spawns[spawnToolCallID]
	if record == nil {
		return "", false
	}
	delete(i.spawns, spawnToolCallID)
	for sessionID, owner := range i.sessionSpawns {
		if owner == spawnToolCallID {
			delete(i.sessionSpawns, sessionID)
		}
	}
	for toolCallID := range record.toolInvocationIDs {
		delete(i.toolSpawns, toolCallID)
	}
	return record.childAgentID, record.childAgentID != ""
}

func (i *zcodeChildIndex) rememberTitle(spawnToolCallID, title string) {
	if spawnToolCallID == "" || title == "" {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	record := i.spawnLocked(spawnToolCallID)
	if record.title == "" {
		record.title = title
	}
}

func (i *zcodeChildIndex) title(spawnToolCallID string) string {
	i.mu.Lock()
	defer i.mu.Unlock()
	if record := i.spawns[spawnToolCallID]; record != nil {
		return record.title
	}
	return ""
}

// forgetTitle drops a spawn's label when the spawn ends.
func (i *zcodeChildIndex) forgetTitle(spawnToolCallID string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if record := i.spawns[spawnToolCallID]; record != nil {
		record.title = ""
	}
}

func (i *zcodeChildIndex) markBackground(spawnToolCallID string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.spawnLocked(spawnToolCallID).background = true
}

func (i *zcodeChildIndex) isBackground(spawnToolCallID string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	record := i.spawns[spawnToolCallID]
	return record != nil && record.background
}

// hasEnded reports whether a spawn's subagent ended.
func (i *zcodeChildIndex) hasEnded(spawnToolCallID string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	_, ended := i.ended[spawnToolCallID]
	return ended
}

// rememberLifecycle records that a spawn's subagent reports lifecycle events.
func (i *zcodeChildIndex) rememberLifecycle(spawnToolCallID string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, ended := i.ended[spawnToolCallID]; !ended {
		i.spawnLocked(spawnToolCallID).lifecycle = true
	}
}

// reportsLifecycle reports whether a spawn's subagent ends with a lifecycle event.
func (i *zcodeChildIndex) reportsLifecycle(spawnToolCallID string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	record := i.spawns[spawnToolCallID]
	return record != nil && record.lifecycle
}

// rememberTask records the background task that runs a spawn's subagent.
func (i *zcodeChildIndex) rememberTask(spawnToolCallID, taskID string) {
	if spawnToolCallID == "" || taskID == "" {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, ended := i.ended[spawnToolCallID]; !ended {
		i.spawnLocked(spawnToolCallID).taskID = taskID
	}
}

// task returns the background task of a spawn's subagent. known is false for a
// spawn the index does not hold.
func (i *zcodeChildIndex) task(spawnToolCallID string) (taskID string, known bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	record := i.spawns[spawnToolCallID]
	if record == nil {
		return "", false
	}
	return record.taskID, true
}

// setInterrupted marks or clears LeapMux's own interrupt of a spawn's subagent.
func (i *zcodeChildIndex) setInterrupted(spawnToolCallID string, interrupted bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if record := i.spawns[spawnToolCallID]; record != nil {
		record.interrupted = interrupted
	}
}

// cancelledStatus is the final status of a spawn's cancelled subagent:
// interrupted when LeapMux's own Interrupt cancelled it, and stopped otherwise,
// such as when the model stopped the task.
func (i *zcodeChildIndex) cancelledStatus(spawnToolCallID string) bgtask.Status {
	i.mu.Lock()
	defer i.mu.Unlock()
	if record := i.spawns[spawnToolCallID]; record != nil && record.interrupted {
		return bgtask.StatusInterrupted
	}
	return bgtask.StatusStopped
}

func (i *zcodeChildIndex) clear() {
	i.mu.Lock()
	defer i.mu.Unlock()
	clear(i.spawns)
	clear(i.toolSpawns)
	clear(i.sessionSpawns)
	clear(i.ended)
}

// zcodeToolSpawnsSubagent reports whether a tool call STARTS a subagent.
//
// The `Agent` tool is the only one that does. The subagent-marking fields
// (`source`, `agentId`, `childSessionId`) say the opposite -- that the update was
// produced BY a subagent -- so they must not be read here; see
// zcodeToolFromSubagent.
func zcodeToolSpawnsSubagent(payload zcodeToolUpdated) bool {
	return payload.ToolName == contracts.ZCodeToolNameAgent
}

// zcodeToolFromSubagent reports whether a tool call was made BY a subagent.
//
// Either marker is enough. `source` is the app-server's own discriminator, and
// `parentToolCallId` rides on every such update as well -- it is read too so a build
// that drops the discriminator still routes the update to the right transcript
// rather than into the parent's.
func zcodeToolFromSubagent(payload zcodeToolUpdated) bool {
	return payload.Source == ToolSourceSubagent || payload.ParentToolCallID != ""
}

type zcodeSubagentLifecycle struct {
	AgentID          string `json:"agentId"`
	AgentType        string `json:"agentType"`
	ChildSessionID   string `json:"childSessionId"`
	ParentToolCallID string `json:"parentToolCallId"`
	Description      string `json:"description"`
	Prompt           string `json:"prompt"`
	Status           string `json:"status"`
	SummaryText      string `json:"summaryText"`
	Text             string `json:"text"`
	Message          string `json:"message"`
	Error            string `json:"error"`
}

type zcodeSubagentLifecycleTransition struct {
	agentID        string
	rowKey         string
	childSessionID string
	title          string
	prompt         string
	status         bgtask.Status
	final          bool
	reportID       string
	report         agent.SubagentReport
	failure        string
}

func decodeZCodeSubagentLifecycleTransition(event zcodeEventEnvelope) (zcodeSubagentLifecycleTransition, bool) {
	var payload zcodeSubagentLifecycle
	if json.Unmarshal(event.Payload, &payload) != nil || payload.ParentToolCallID == "" {
		return zcodeSubagentLifecycleTransition{}, false
	}
	if payload.Description == "" && payload.Prompt == "" && payload.Status == "" &&
		payload.SummaryText == "" && payload.Text == "" && payload.Message == "" && payload.Error == "" {
		return zcodeSubagentLifecycleTransition{}, false
	}
	title := strings.TrimSpace(payload.Description)
	if title == "" {
		title = strings.TrimSpace(payload.AgentType)
	}
	if title == "" {
		title = "ZCode subagent"
	}
	status := bgtask.StatusRunning
	final := false
	switch payload.Status {
	case "completed", "success":
		status, final = bgtask.StatusSucceeded, true
	case "failed":
		status, final = bgtask.StatusFailed, true
	case "cancelled", "stopped":
		status, final = bgtask.StatusStopped, true
	}

	report := strings.TrimSpace(payload.Message)
	if report == "" {
		report = strings.TrimSpace(payload.Text)
	}
	if report == "" {
		report = strings.TrimSpace(payload.SummaryText)
	}
	reportID := ""
	if report != "" {
		if final {
			reportID = zcodeFinalReportID(payload.ParentToolCallID)
		} else {
			reportID = strings.TrimSpace(event.EventID)
		}
		if reportID == "" {
			reportID = providerkit.SubagentReportContentID("zcode", payload.ParentToolCallID, report)
		}
	}
	return zcodeSubagentLifecycleTransition{
		rowKey:         payload.ParentToolCallID,
		agentID:        payload.AgentID,
		childSessionID: payload.ChildSessionID,
		title:          title,
		prompt:         payload.Prompt,
		status:         status,
		final:          final,
		reportID:       reportID,
		report:         agent.SubagentReport{Text: report},
		failure:        strings.TrimSpace(payload.Error),
	}, true
}

func (a *Agent) handleZCodeSubagentLifecycle(event zcodeEventEnvelope) {
	transition, ok := decodeZCodeSubagentLifecycleTransition(event)
	if !ok {
		return
	}
	nativeParentID := transition.rowKey
	transition.rowKey = a.resolveZCodeSpawn(event, nativeParentID, transition.childSessionID, transition.agentID)
	if transition.rowKey == "" {
		return
	}
	if transition.final && transition.report.Text != "" {
		transition.reportID = zcodeFinalReportID(transition.rowKey)
	}
	a.children.rememberSession(transition.rowKey, transition.childSessionID)

	if transition.prompt != "" {
		a.toolCallPrompts.Remember(transition.rowKey, transition.prompt)
	}
	a.children.rememberTitle(transition.rowKey, transition.title)
	childID, childTitle, ok := a.ensureZCodeSubagentTranscript(
		transition.rowKey,
		transition.rowKey,
		transition.title,
	)
	if !ok {
		return
	}
	a.children.rememberLifecycle(transition.rowKey)
	a.upsertZCodeSubagent(transition.rowKey, childID, childTitle)
	transition.report.Label = childTitle
	if transition.report.Text != "" {
		providerkit.PersistChildSubagentReport(a.sink, agent.ChildSubagentReportWrite{
			RowKey: a.zcodeSubagentRegistryKey(transition.rowKey),
			Write: agent.SubagentReportWrite{
				ReportID: transition.reportID,
				Report:   transition.report,
			},
		})
		if transition.final && a.children.isBackground(transition.rowKey) {
			providerkit.PersistSubagentReport(a.sink, agent.SubagentReportWrite{
				ReportID: transition.reportID,
				Report:   transition.report,
			})
		}
	}
	if transition.final {
		if transition.status == bgtask.StatusStopped {
			transition.status = a.children.cancelledStatus(transition.rowKey)
		}
		if transition.failure != "" {
			completion := agent.MessageCompletionError
			if transition.status == bgtask.StatusInterrupted {
				// The reader's own Interrupt stopped it: no error ended it.
				completion = agent.MessageCompletionInterrupted
			}
			a.persistZCodeChildText(childID, transition.failure, completion)
		}
		a.persistIncompleteZCodeToolsForChild(transition.rowKey, zcodeIncompleteChildCompletion(transition.status))
		providerkit.LogRegistryRefusal("zcode", "close", a.sink.CloseBackgroundTask(a.zcodeSubagentRegistryKey(transition.rowKey), transition.status))
		// Native foreground completion carries counters before the Agent result
		// returns its report. Keep that call's route and lease until the result.
		if transition.status == bgtask.StatusSucceeded && a.zcodeForegroundSpawnResultPending(transition.rowKey) {
			return
		}
		a.children.takeChild(transition.rowKey)
		a.children.forgetTitle(transition.rowKey)
		a.sink.CleanupChildAgent(childID)
		a.reapZCodeSettledTurns()
		return
	}
}

func (a *Agent) zcodeForegroundSpawnResultPending(spanID string) bool {
	a.Mu.Lock()
	invocation := a.toolInvocations[spanID]
	pending := invocation != nil && !invocation.final && invocation.name == contracts.ZCodeToolNameAgent
	var input json.RawMessage
	if pending {
		input = append(json.RawMessage(nil), invocation.input...)
	}
	a.Mu.Unlock()
	if !pending || a.children.isBackground(spanID) {
		return false
	}
	var options struct {
		Background bool `json:"run_in_background"`
	}
	if len(input) > 0 {
		_ = json.Unmarshal(input, &options)
	}
	return !options.Background
}

func (a *Agent) persistZCodeChildText(childID, text string, completion agent.MessageCompletion) {
	raw, err := agent.MarshalAssembledMessage(agent.AssembledMessageKindText, text, completion)
	if err != nil {
		slog.Warn("zcode subagent text marshal failed", "agent_id", a.AgentID(), "child_agent_id", childID, "error", err)
		return
	}
	if err := a.sink.PersistChildMessage(childID, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, raw, agent.SpanInfo{}); err != nil {
		slog.Warn("zcode subagent text persist failed", "agent_id", a.AgentID(), "child_agent_id", childID, "error", err)
	}
}

func (a *Agent) upsertZCodeSubagent(rowKey, childID, title string) {
	providerkit.LogRegistryRefusal("zcode", "upsert", a.sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: a.zcodeSubagentRegistryKey(rowKey), Kind: bgtask.KindSubagent, Title: title,
		Status: bgtask.StatusRunning, ChildAgentID: childID,
	}))
}

// zcodeSinkForToolCall returns the transcript a tool call's rows belong to: the
// child transcript of the subagent that made it, or this agent's own.
//
// Every row of one tool call goes to ONE transcript. The closing row and the batch
// summary state no subagent linkage of their own, so without this they would close a
// span in a transcript that never opened it.
func (a *Agent) zcodeSinkForToolCall(toolCallID string) agent.ProviderServices {
	if childID, ok := a.children.toolChild(toolCallID); ok {
		return a.sink.ChildSink(childID)
	}
	return a.sink
}

// zcodeSubagentChild returns the child transcript for a subagent's tool update,
// creating it and its registry row on first sight.
//
// Returns ("", false) when neither the parent invocation nor the native child
// identity identifies a transcript. The caller then keeps the row in the parent.
func (a *Agent) zcodeSubagentChild(payload zcodeToolUpdated) (string, bool) {
	rowKey := a.resolveZCodeSpawn(zcodeEventEnvelope{TurnID: payload.invocation.owner.id}, payload.ParentToolCallID, payload.ChildSessionID, payload.AgentID)
	if rowKey == "" {
		return "", false
	}
	a.children.rememberSession(rowKey, payload.ChildSessionID)

	childID, title, ok := a.ensureZCodeSubagentTranscript(rowKey, rowKey, zcodeSubagentTitle(payload))
	if !ok {
		return "", false
	}
	// The subagent's OWN tool call is aliased to the same transcript, so its closing row
	// and any batch summary that lists it land where its opening row went.
	a.children.rememberChild(rowKey, payload.spanID(), childID)
	a.children.rememberSession(rowKey, payload.ChildSessionID)
	a.upsertZCodeSubagent(rowKey, childID, title)
	return childID, true
}

// ensureZCodeSubagentTranscript creates or adopts a subagent's child transcript.
//
// Both the child's tool updates and background-task updates reach it. It records
// their shared child identity and writes the spawn prompt once.
//
// rowKey identifies the internal spawn record. After adoption, the native child
// session identifies the registry row and provider child. spawnSpanID links the
// child tab to its parent invocation.
//
// Returns the transcript's id and the title it carries, so the caller's registry row
// states the same label the tab does. The SPAWN's label wins over `fallbackTitle`: the
// spawn gave the task its title ("file census"), while the subagent's own updates each
// describe a COMMAND it runs and the background-task event says only "Agent".
func (a *Agent) ensureZCodeSubagentTranscript(rowKey, spawnSpanID, fallbackTitle string) (childAgentID, title string, ok bool) {
	if a.children.hasEnded(rowKey) {
		return "", "", false
	}
	title = a.children.title(rowKey)
	if title == "" {
		title = fallbackTitle
	}
	a.children.mu.Lock()
	if record := a.children.spawns[rowKey]; record != nil && record.nativeOnly {
		spawnSpanID = ""
	}
	a.children.mu.Unlock()
	nativeKey := a.zcodeSubagentNativeKey(rowKey)
	previous, found := a.children.child(rowKey)
	if found && nativeKey != "" {
		a.sink.ChildSink(previous).UpdateSessionID(nativeKey)
		if err := a.sink.RenameBackgroundTask(rowKey, nativeKey); err != nil {
			slog.Warn("zcode child registry key adoption failed", "error", err)
			return "", "", false
		}
	}
	childID, err := a.sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: spawnSpanID, ProviderChildKey: nativeKey, Title: title})
	if err != nil {
		slog.Warn("zcode subagent ensure child failed", "agent_id", a.AgentID(), "error", err)
		return "", "", false
	}
	if nativeKey != "" {
		a.sink.ChildSink(childID).UpdateSessionID(nativeKey)
	}
	a.children.rememberChild(rowKey, "", childID)

	// The spawn prompt is the child transcript's first message, and it can only be
	// written once the transcript exists.
	if prompt := a.toolCallPrompts.Take(rowKey); prompt != "" {
		if err := a.sink.PersistChildPrompt(childID, prompt); err != nil {
			slog.Warn("zcode subagent persist prompt failed", "agent_id", a.AgentID(), "child", childID, "error", err)
		}
	}
	return childID, title, true
}

// openZCodeSubagentToolCall opens one of a subagent's tool calls in its child
// transcript, and reports whether it did.
//
// Only the OPENING half routes here. Every later row of the same call finds its
// transcript through zcodeSinkForToolCall, which is what a batch summary -- carrying
// nothing but a list of ids -- has to rely on.
func (a *Agent) openZCodeSubagentToolCall(event zcodeEventEnvelope, payload zcodeToolUpdated, owner *zcodeTurnState) bool {
	childID, ok := a.zcodeSubagentChild(payload)
	if !ok {
		return false
	}
	a.openZCodeToolCallInto(a.sink.ChildSink(childID), event, payload, owner)
	return true
}

// closeZCodeSubagentChild ends the child transcript a spawn opened, when its own
// `Agent` call finishes.
//
// The child index is the discriminator, not the tool NAME. Only a spawn's id is ever
// written to `children`, so a hit is proof that this call owns a subagent -- and a name
// check would additionally fail after a worker restart, where the resumed session
// replays no history and a `result` carries no `toolName` of its own.
func (a *Agent) closeZCodeSubagentChild(payload zcodeToolUpdated) {
	if _, ok := a.children.child(payload.spanID()); !ok {
		return
	}
	a.persistIncompleteZCodeToolsForChild(payload.spanID(), agent.MessageCompletionError)
	childID, ok := a.children.takeChild(payload.spanID())
	if !ok {
		return
	}
	a.sink.CleanupChildAgent(childID)
	a.reapZCodeSettledTurns()
}

// openZCodeToolCallInto persists a tool call's opening row into one transcript and
// opens its span there.
//
// The input is recovered from the model stream when the update omits it, which is the
// COMMON case: the app-server sets `inputOmitted: true, inputRef: "model_stream"` and
// sends no input of its own, so the stream is the only copy that ever existed.
//
// The open records the owning turn on the call, so a close can count and settle
// against the turn that opened it.
func (a *Agent) openZCodeToolCallInto(sink providerkit.ToolSpanServices, event zcodeEventEnvelope, payload zcodeToolUpdated, owner *zcodeTurnState) {
	if payload.spanID() == "" {
		return
	}
	toolName := payload.ToolName
	a.Mu.Lock()
	tc := payload.invocation
	if tc == nil {
		tc = a.resolveZCodeInvocationLocked(owner, payload.ToolCallID, payload.AssistantMessageID, true)
		payload.invocation = tc
	}
	if tc == nil {
		a.Mu.Unlock()
		return
	}
	if toolName == "" {
		toolName = tc.name
	} else {
		if tc.name == "" {
			tc.order = a.nextToolOrder
			a.nextToolOrder++
		}
		tc.name = toolName
	}
	if tc.owner == nil {
		tc.owner = owner
		if owner != nil {
			owner.toolInvocationIDs[payload.spanID()] = struct{}{}
		}
	}
	tc.native.ToolName = toolName
	input := payload.Input
	if zcodeInputIsAbsent(input) {
		input = tc.input
	}
	metadata := tc.metadata(nil)
	a.Mu.Unlock()

	content := event.persistBytes()
	if content == nil {
		return
	}

	// A subagent spawn owns no span: its work lands in a child transcript, so a rail
	// held open for the whole run would only push every concurrent tool one column
	// right. The decision is made from ZCode's own wire shape and stays here.
	spawns := zcodeToolSpawnsSubagent(payload)
	if spawns {
		if prompt := zcodeSpawnPrompt(input); prompt != "" {
			a.toolCallPrompts.Remember(payload.spanID(), prompt)
		}
		title := zcodeSpawnTitle(input, payload)
		a.children.rememberTitle(payload.spanID(), title)
		// The spawn already identifies one conversation. Create its transcript now,
		// so a child that answers without any tool call still has a prompt and report.
		if childID, childTitle, ok := a.ensureZCodeSubagentTranscript(payload.spanID(), payload.spanID(), title); ok {
			a.upsertZCodeSubagent(payload.spanID(), childID, childTitle)
		}
	}
	supplemental, err := zcodeToolInputSupplement(payload, input)
	if err != nil {
		slog.Warn("Encode supplemental ZCode tool input", "agent_id", a.AgentID(), "error", err)
	}
	if err := providerkit.OpenToolSpan(sink, agent.MessageContent{Original: content, Supplemental: supplemental, Metadata: metadata}, payload.spanID(), toolName, spawns); err != nil {
		slog.Error("zcode persist tool scheduled", "agent_id", a.AgentID(), "error", err)
	}
}

// closeZCodeToolCallInto persists a tool call's final row into one transcript and
// closes its span there.
//
// The per-call side tables, the turn tool count and the running-tool indicator are
// the AGENT's rather than the transcript's, so they are updated here whichever sink
// the row went to. A subagent's calls count toward the turn deliberately: they are
// part of the work that turn did. The count and the settlement follow the turn the
// OPENING recorded, so a close never counts against whichever turn happens to be
// active; owner is only the fallback for a call no open ever recorded.
func (a *Agent) closeZCodeToolCallInto(sink providerkit.ToolLifecycleServices, event zcodeEventEnvelope, payload zcodeToolUpdated, recovered *zcodeRecoveredClose, owner *zcodeTurnState) {
	if payload.spanID() == "" {
		return
	}
	toolName := payload.ToolName
	if toolName == "" {
		toolName = a.zcodeToolInvocationName(payload.spanID())
	}
	// The resolved name travels on, because the bookkeeping below asks whether this
	// call is a spawn and a result payload states no tool name of its own.
	payload.ToolName = toolName

	a.Mu.Lock()
	// The mark outlives the close, so the record stays; only what the open consumed is
	// released. ONE record to update, which is what stops the next teardown forgetting
	// a table.
	tc := payload.invocation
	if tc == nil {
		tc = a.resolveZCodeInvocationLocked(owner, payload.ToolCallID, payload.AssistantMessageID, true)
		payload.invocation = tc
	}
	if tc == nil {
		a.Mu.Unlock()
		return
	}
	if tc.owner == nil {
		tc.owner = owner
	}
	if tc.final {
		a.Mu.Unlock()
		return
	}
	tc.name = toolName
	metadata := []byte(nil)
	if recovered != nil {
		metadata = recovered.outcome
	}
	metadata = tc.metadata(metadata)
	owner = tc.owner
	if owner != nil {
		owner.toolInvocationIDs[payload.spanID()] = struct{}{}
		owner.toolUses++
		if a.foregroundTurnID == owner.id {
			a.TurnToolUses = owner.toolUses
		}
	}
	tc.final = true
	tc.name = ""
	tc.input = nil
	tc.progress = zcodeToolUpdated{}
	tc.native.Input, tc.native.Result, tc.native.Error = nil, nil, nil
	lastFrame := tc.lastFrame
	tc.lastFrame = nil
	a.Mu.Unlock()
	a.ClearCumulativeOutput(payload.spanID())
	sink.ReportProgress(agent.CompleteOutputProgress(payload.spanID()))

	// A recovered close stores the call's OWN last frame. The event that triggered it
	// says nothing about this one call -- a batch summary is addressed to a list --
	// so what LeapMux read out of it travels in the outcome note instead.
	content := event.persistBytes()
	if recovered != nil {
		content = lastFrame
	}
	if content != nil {
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: content, Metadata: metadata}, agent.SpanInfo{
			SpanID:   payload.spanID(),
			SpanType: toolName,
			Closing:  true,
		}); err != nil {
			slog.Error("zcode persist tool result", "agent_id", a.AgentID(), "error", err)
		}
	}
	sink.CloseSpan(payload.spanID())

	backgroundLaunch := zcodeSubagentLaunchedInBackground(payload)
	if backgroundLaunch {
		a.children.markBackground(payload.spanID())
	}
	if !backgroundLaunch {
		a.persistZCodeSubagentReport(payload)
		a.applyZCodeSubagentEnd(payload, recovered)
		a.closeZCodeSubagentChild(payload)
	}
	a.children.forgetTool(payload.spanID())
	if !backgroundLaunch {
		a.children.forgetTitle(payload.spanID())
	}
	a.toolCallPrompts.Take(payload.spanID())
	// The last tool result of a settled owner releases its record; a settled
	// owner with calls still open stays, so their late results route to it.
	if owner != nil {
		a.Mu.Lock()
		a.dropZCodeTurnLocked(owner)
		a.Mu.Unlock()
	}
	// No running-tool clear is broadcast here. The `zcode_running_tool` key this
	// site used to clear had no reader; its replacement,
	// contracts.SessionInfoKeyRunningTool, is cleared by the frontend when the
	// result row above lands, so a provider never sends an end message. See
	// recordZCodeToolStarted for what ZCode must report before it broadcasts one.
}

func zcodeSubagentLaunchedInBackground(payload zcodeToolUpdated) bool {
	if !zcodeToolSpawnsSubagent(payload) || len(payload.Result) == 0 {
		return false
	}
	var result struct {
		Content string `json:"content"`
	}
	return json.Unmarshal(payload.Result, &result) == nil &&
		strings.HasPrefix(result.Content, "Async agent launched successfully.\n")
}

func (a *Agent) persistZCodeSubagentReport(payload zcodeToolUpdated) {
	if !zcodeToolSpawnsSubagent(payload) || len(payload.Result) == 0 {
		return
	}
	var result struct {
		Content string `json:"content"`
	}
	if json.Unmarshal(payload.Result, &result) != nil || strings.TrimSpace(result.Content) == "" {
		return
	}
	if zcodeSubagentLaunchedInBackground(payload) {
		return
	}
	providerkit.PersistChildSubagentReport(a.sink, agent.ChildSubagentReportWrite{
		RowKey: a.zcodeSubagentRegistryKey(payload.spanID()),
		Write: agent.SubagentReportWrite{
			ReportID: zcodeFinalReportID(payload.spanID()),
			Report:   agent.SubagentReport{Text: result.Content},
		},
	})
}

func zcodeFinalReportID(spawnToolCallID string) string {
	return "zcode-final:" + strings.TrimSpace(spawnToolCallID)
}

// zcodeSpawnTitle labels the subagent an `Agent` call starts: the description the
// model wrote for it, then the subagent type, then whatever the update itself says.
func zcodeSpawnTitle(input json.RawMessage, payload zcodeToolUpdated) string {
	var in struct {
		Description string `json:"description"`
		AgentType   string `json:"agentType"`
		Subagent    string `json:"subagent_type"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			slog.Debug("zcode spawn title unmarshal failed", "error", err)
		}
	}
	for _, candidate := range []string{in.Description, in.AgentType, in.Subagent} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return bgtask.CleanTitleRunes(bgtask.FirstLine(trimmed), 80)
		}
	}
	return zcodeSubagentTitle(payload)
}

// zcodeSpawnPrompt reads the instruction a subagent spawn was given, so the child
// transcript can open on it. ZCode's Agent tool declares it as `prompt`.
func zcodeSpawnPrompt(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var in struct {
		Prompt      string `json:"prompt"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return ""
	}
	if prompt := strings.TrimSpace(in.Prompt); prompt != "" {
		return prompt
	}
	return strings.TrimSpace(in.Description)
}

// zcodeSubagentRowKey selects an initial route before the native child session
// is known. The child session becomes the durable registry key after adoption.
func zcodeSubagentRowKey(toolCallID, childSessionID, taskID string) string {
	for _, candidate := range []string{toolCallID, childSessionID, taskID} {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

// applyZCodeSubagentEnd closes the registry row of a finished subagent.
//
// It UPDATES a row and never creates one. A row is created by the background-task
// path, which is also what mints the child transcript, and a row with no transcript
// opens nothing -- the subagent's result is persisted in this transcript by
// closeZCodeToolCall either way. So an Agent tool call that the app-server never
// reported as a background task correctly leaves the registry alone.
func (a *Agent) applyZCodeSubagentEnd(payload zcodeToolUpdated, recovered *zcodeRecoveredClose) {
	// The only caller, closeZCodeToolCallInto, already returned for an empty tool-call
	// id, so this is the key both subagent creators use.
	rowKey := a.zcodeSubagentRegistryKey(payload.spanID())
	if rowKey == "" {
		return
	}
	childID, _, exists, err := a.sink.LookupBackgroundTask(rowKey)
	if err != nil {
		slog.Warn("zcode subagent end lookup failed", "agent_id", a.AgentID(), "row_key", rowKey, "error", err)
		return
	}
	if !exists {
		return
	}
	// The row's CHILD linkage says whether this tool call owns a subagent, and it
	// survives a worker restart where the tool-name cache does not: a resumed session
	// replays no history, so a spawn's `result` arrives with no `toolName`, and a name
	// check would leave the row Running for good. A background SHELL task is keyed by the
	// same tool-call id and carries no child, so it still needs the name.
	if childID == "" && !zcodeToolSpawnsSubagent(payload) {
		return
	}
	// A recovered close states the status itself: the agent reported no result, so
	// the payload cannot say whether the subagent finished, and reading Completed out
	// of its absence would claim work that never ended.
	status := bgtask.StatusSucceeded
	switch {
	case recovered != nil:
		status = recovered.status
	case payload.Kind == contracts.ZCodeToolKindError || payload.ErrorCount > 0:
		status = bgtask.StatusFailed
	}
	providerkit.LogRegistryRefusal("zcode", "close", a.sink.CloseBackgroundTask(rowKey, status))
}

// zcodeSubagentTitle labels a subagent row: its own description, then its agent type.
func zcodeSubagentTitle(payload zcodeToolUpdated) string {
	if desc := strings.TrimSpace(payload.Description); desc != "" {
		return bgtask.CleanTitleRunes(bgtask.FirstLine(desc), 80)
	}
	if t := strings.TrimSpace(payload.AgentType); t != "" {
		return t
	}
	return payload.ToolName
}

var _ agent.ChildInterrupter = (*Agent)(nil)

// zcodeCancelBackgroundTaskReply is the answer of session/cancelBackgroundTask.
type zcodeCancelBackgroundTaskReply struct {
	Cancelled bool   `json:"cancelled"`
	Reason    string `json:"reason"`
	Status    string `json:"status"`
}

// InterruptChild stops one subagent and leaves its parent running.
//
// A background subagent runs as a background task of the session, and
// session/cancelBackgroundTask stops that task alone. ZCode then reports the task
// cancelled, which closes the row as interrupted, and runs a parent turn for the
// notification. A foreground subagent runs inside its parent's turn: it is no
// background task, and ZCode stops it only with that turn.
//
// The mark precedes the cancel, because ZCode reports the cancellation before it
// answers the request, and a refused cancel clears the mark again.
func (a *Agent) InterruptChild(childKey string, _ agent.StopContext) error {
	a.children.mu.Lock()
	if spanID := a.children.sessionSpawns[childKey]; spanID != "" {
		childKey = spanID
	}
	a.children.mu.Unlock()

	taskID, known := a.children.task(childKey)
	switch {
	case !known:
		return fmt.Errorf("unknown ZCode subagent %q", childKey)
	case taskID == "":
		return fmt.Errorf("%w: this subagent runs inside its parent's turn, and ZCode stops it only with that turn",
			agent.ErrChildOperationUnsupported)
	}
	a.Mu.Lock()
	sessionID := a.sessionID
	a.Mu.Unlock()
	a.children.setInterrupted(childKey, true)
	raw, err := a.sendZCodeRequest(MethodSessionCancelBackgroundTask, map[string]string{"sessionId": sessionID, "taskId": taskID}, a.APITimeout())
	if err != nil {
		a.children.setInterrupted(childKey, false)
		return fmt.Errorf("stop the ZCode subagent: %w", err)
	}
	var reply zcodeCancelBackgroundTaskReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		a.children.setInterrupted(childKey, false)
		return fmt.Errorf("read the ZCode subagent stop: %w", err)
	}
	if reply.Cancelled {
		return nil
	}
	a.children.setInterrupted(childKey, false)
	if _, final := zcodeBackgroundStatus(reply.Status); final {
		// The subagent ended before the cancel arrived, so nothing runs to stop.
		return nil
	}
	return fmt.Errorf("ZCode did not stop the subagent: %s", reply.Reason)
}

func (a *Agent) zcodeSubagentNativeKey(spanID string) string {
	a.children.mu.Lock()
	defer a.children.mu.Unlock()
	if record := a.children.spawns[spanID]; record != nil {
		return record.childSessionID
	}
	return ""
}
func (a *Agent) zcodeSubagentRegistryKey(spanID string) string {
	if native := a.zcodeSubagentNativeKey(spanID); native != "" {
		return native
	}
	return spanID
}

// resolveZCodeSpawn uses native session identity first, then the parent turn.
// A restarted worker recovers the original spawn span from the stored child.
func (a *Agent) resolveZCodeSpawn(event zcodeEventEnvelope, nativeCallID, childSessionID, agentID string) string {
	a.children.mu.Lock()
	known := a.children.sessionSpawns[childSessionID]
	a.children.mu.Unlock()
	if known != "" {
		return known
	}
	a.Mu.Lock()
	owner := a.resolveZCodeTurnLocked(event, nativeCallID)
	existing := a.findZCodeInvocationLocked(owner, nativeCallID, "")
	a.Mu.Unlock()
	if existing != nil {
		return existing.spanID
	}
	var childID, storedSpan string
	if childSessionID != "" {
		var err error
		childID, err = a.sink.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: childSessionID})
		if err != nil {
			return ""
		}
		storedSpan, err = a.sink.ChildSpawnSpan(childID)
		if err != nil {
			return ""
		}
	}
	a.Mu.Lock()
	// Recheck after the child-store read, which can invoke sink callbacks.
	if owner != nil && a.turns[owner.id] != owner {
		a.Mu.Unlock()
		return ""
	}
	invocation := a.resolveZCodeInvocationLocked(owner, nativeCallID, "", true)
	if invocation != nil && storedSpan != "" && invocation.spanID != storedSpan {
		if other := a.toolInvocations[storedSpan]; other != nil && other != invocation {
			a.Mu.Unlock()
			return ""
		}
		delete(a.toolInvocations, invocation.spanID)
		delete(owner.toolInvocationIDs, invocation.spanID)
		invocation.spanID = storedSpan
		a.toolInvocations[storedSpan] = invocation
		owner.toolInvocationIDs[storedSpan] = struct{}{}
	}
	sessionID := a.sessionID
	a.Mu.Unlock()
	spanID := storedSpan
	if invocation != nil {
		spanID = invocation.spanID
	}
	if spanID == "" {
		if childSessionID == "" && agentID == "" {
			return ""
		}
		identity := sessionID + "\x00" + childSessionID + "\x00" + agentID + "\x00" + nativeCallID
		spanID = "zcode-child:" + base64.RawURLEncoding.EncodeToString([]byte(identity))
	}
	if childID != "" {
		a.children.rememberChild(spanID, "", childID)
		a.children.rememberSession(spanID, childSessionID)
		a.children.mu.Lock()
		a.children.spawnLocked(spanID).nativeOnly = invocation == nil && storedSpan == ""
		a.children.mu.Unlock()
	}
	return spanID
}

func zcodeIncompleteChildCompletion(status bgtask.Status) agent.MessageCompletion {
	if status == bgtask.StatusInterrupted || status == bgtask.StatusStopped {
		return agent.MessageCompletionInterrupted
	}
	return agent.MessageCompletionError
}

// liveZCodeBackgroundToolLocked reports whether parent settlement must preserve this tool.
// The caller holds a.Mu, matching the order used by turn cleanup.
func (a *Agent) liveZCodeBackgroundToolLocked(spanID string) bool {
	a.children.mu.Lock()
	defer a.children.mu.Unlock()
	spawn := a.children.spawns[a.children.toolSpawns[spanID]]
	return spawn != nil && spawn.background && spawn.childAgentID != ""
}
