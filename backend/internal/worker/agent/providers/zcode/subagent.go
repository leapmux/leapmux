package zcode

import (
	"encoding/json"
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
	childAgentID string
	title        string
	toolCallIDs  map[string]struct{}
	background   bool
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
	if record.toolCallIDs == nil {
		record.toolCallIDs = make(map[string]struct{})
	}
	record.toolCallIDs[toolCallID] = struct{}{}
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
		delete(record.toolCallIDs, toolCallID)
	}
}

// takeChild returns a spawn's transcript and drops it, when the spawn ends.
func (i *zcodeChildIndex) takeChild(spawnToolCallID string) (string, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	record := i.spawns[spawnToolCallID]
	if record == nil {
		return "", false
	}
	delete(i.spawns, spawnToolCallID)
	for toolCallID := range record.toolCallIDs {
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

func (i *zcodeChildIndex) clear() {
	i.mu.Lock()
	defer i.mu.Unlock()
	clear(i.spawns)
	clear(i.toolSpawns)
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
	rowKey   string
	title    string
	prompt   string
	status   bgtask.Status
	final    bool
	reportID string
	report   agent.SubagentReport
	failure  string
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
		status, final = bgtask.StatusCompleted, true
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
		rowKey:   payload.ParentToolCallID,
		title:    title,
		prompt:   payload.Prompt,
		status:   status,
		final:    final,
		reportID: reportID,
		report:   agent.SubagentReport{Text: report},
		failure:  strings.TrimSpace(payload.Error),
	}, true
}

func (a *Agent) handleZCodeSubagentLifecycle(event zcodeEventEnvelope) {
	transition, ok := decodeZCodeSubagentLifecycleTransition(event)
	if !ok {
		return
	}
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
	transition.report.Label = childTitle
	if transition.report.Text != "" {
		providerkit.PersistChildSubagentReport(a.sink, agent.ChildSubagentReportWrite{
			RowKey: transition.rowKey,
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
		if transition.failure != "" {
			a.persistZCodeChildText(childID, transition.failure, agent.MessageCompletionError)
		}
		providerkit.LogRegistryRefusal("zcode", "close", a.sink.CloseBackgroundTask(transition.rowKey, transition.status))
		a.children.takeChild(transition.rowKey)
		a.children.forgetTitle(transition.rowKey)
		a.sink.CleanupChildAgent(childID)
		return
	}
	a.upsertZCodeSubagent(transition.rowKey, childID, childTitle)
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
		RowKey: rowKey, Kind: bgtask.KindSubagent, Title: title,
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
// Returns ("", false) when the update gives no parent tool call: without it there is
// nothing to attach the transcript to, and the caller keeps the row in the parent
// rather than dropping it.
func (a *Agent) zcodeSubagentChild(payload zcodeToolUpdated) (string, bool) {
	rowKey := payload.ParentToolCallID
	if rowKey == "" {
		return "", false
	}
	if childID, ok := a.children.child(rowKey); ok {
		a.children.rememberChild(rowKey, payload.ToolCallID, childID)
		return childID, true
	}

	childID, title, ok := a.ensureZCodeSubagentTranscript(rowKey, rowKey, zcodeSubagentTitle(payload))
	if !ok {
		return "", false
	}
	// The subagent's OWN tool call is aliased to the same transcript, so its closing row
	// and any batch summary that lists it land where its opening row went.
	a.children.rememberChild(rowKey, payload.ToolCallID, childID)
	a.upsertZCodeSubagent(rowKey, childID, title)
	return childID, true
}

// ensureZCodeSubagentTranscript is the ONE creator of a subagent's child transcript.
//
// Both events that speak about a subagent reach it -- the `tool.updated` its own tool
// calls arrive on, and the background-task `session.updated` -- so the transcript, the
// index entry and the spawn prompt are written in one place. Two creators produced two
// transcripts for one subagent, and the one the background path made was invisible to
// `zcodeSinkForToolCall`, `takeChild` and the spawn's own teardown.
//
// `rowKey` is the registry row key AND the child key: EnsureChildAgent creates its row
// under exactly that argument, so a different value means two rows for one subagent and
// the one carrying the child linkage is not the one the spawn's result closes.
// `spawnSpanID` is the span the child tab hangs off, which is the spawn's tool call.
//
// Returns the transcript's id and the title it carries, so the caller's registry row
// states the same label the tab does. The SPAWN's label wins over `fallbackTitle`: the
// spawn gave the task its title ("file census"), while the subagent's own updates each
// describe a COMMAND it runs and the background-task event says only "Agent".
func (a *Agent) ensureZCodeSubagentTranscript(rowKey, spawnSpanID, fallbackTitle string) (childAgentID, title string, ok bool) {
	title = a.children.title(rowKey)
	if title == "" {
		title = fallbackTitle
	}
	if childID, found := a.children.child(rowKey); found {
		return childID, title, true
	}
	childID, err := a.sink.EnsureChildAgent(spawnSpanID, rowKey, title)
	if err != nil {
		slog.Warn("zcode subagent ensure child failed", "agent_id", a.AgentID(), "row_key", rowKey, "error", err)
		return "", "", false
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
func (a *Agent) openZCodeSubagentToolCall(event zcodeEventEnvelope, payload zcodeToolUpdated) bool {
	childID, ok := a.zcodeSubagentChild(payload)
	if !ok {
		return false
	}
	a.openZCodeToolCallInto(a.sink.ChildSink(childID), event, payload)
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
	childID, ok := a.children.takeChild(payload.ToolCallID)
	if !ok {
		return
	}
	a.sink.CleanupChildAgent(childID)
}

// openZCodeToolCallInto persists a tool call's opening row into one transcript and
// opens its span there.
//
// The input is recovered from the model stream when the update omits it, which is the
// COMMON case: the app-server sets `inputOmitted: true, inputRef: "model_stream"` and
// sends no input of its own, so the stream is the only copy that ever existed.
func (a *Agent) openZCodeToolCallInto(sink providerkit.ToolSpanServices, event zcodeEventEnvelope, payload zcodeToolUpdated) {
	if payload.ToolCallID == "" {
		return
	}
	toolName := payload.ToolName
	a.Mu.Lock()
	tc := a.zcodeToolCallLocked(payload.ToolCallID)
	if toolName == "" {
		toolName = tc.name
	} else {
		if tc.name == "" {
			tc.order = a.nextToolOrder
			a.nextToolOrder++
		}
		tc.name = toolName
	}
	input := payload.Input
	if zcodeInputIsAbsent(input) {
		input = tc.input
	}
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
			a.toolCallPrompts.Remember(payload.ToolCallID, prompt)
		}
		title := zcodeSpawnTitle(input, payload)
		a.children.rememberTitle(payload.ToolCallID, title)
		// The spawn already identifies one conversation. Create its transcript now,
		// so a child that answers without any tool call still has a prompt and report.
		if childID, childTitle, ok := a.ensureZCodeSubagentTranscript(payload.ToolCallID, payload.ToolCallID, title); ok {
			a.upsertZCodeSubagent(payload.ToolCallID, childID, childTitle)
		}
	}
	supplemental, err := zcodeToolInputSupplement(payload, input)
	if err != nil {
		slog.Warn("Encode supplemental ZCode tool input", "agent_id", a.AgentID(), "error", err)
	}
	if err := providerkit.OpenToolSpan(sink, agent.MessageContent{Original: content, Supplemental: supplemental}, payload.ToolCallID, toolName, spawns); err != nil {
		slog.Error("zcode persist tool scheduled", "agent_id", a.AgentID(), "error", err)
	}
}

// closeZCodeToolCallInto persists a tool call's final row into one transcript and
// closes its span there.
//
// The per-call side tables, the turn tool count and the running-tool indicator are
// the AGENT's rather than the transcript's, so they are updated here whichever sink
// the row went to. A subagent's calls count toward the turn deliberately: they are
// part of the work that turn did.
func (a *Agent) closeZCodeToolCallInto(sink providerkit.ToolLifecycleServices, event zcodeEventEnvelope, payload zcodeToolUpdated, recovered *zcodeRecoveredClose) {
	if payload.ToolCallID == "" {
		return
	}
	toolName := payload.ToolName
	if toolName == "" {
		toolName = a.zcodeToolCallName(payload.ToolCallID)
	}
	// The resolved name travels on, because the bookkeeping below asks whether this
	// call is a spawn and a result payload states no tool name of its own.
	payload.ToolName = toolName

	a.Mu.Lock()
	if !a.backgroundTurn {
		a.TurnToolUses++
	}
	// The mark outlives the close, so the record stays; only what the open consumed is
	// released. ONE record to update, which is what stops the next teardown forgetting
	// a table.
	tc := a.zcodeToolCallLocked(payload.ToolCallID)
	tc.final = true
	tc.name = ""
	tc.input = nil
	lastFrame := tc.lastFrame
	tc.lastFrame = nil
	a.Mu.Unlock()
	a.ClearCumulativeOutput(payload.ToolCallID)
	sink.ReportProgress(agent.CompleteOutputProgress(payload.ToolCallID))

	// A recovered close stores the call's OWN last frame. The event that triggered it
	// says nothing about this one call -- a batch summary is addressed to a list --
	// so what LeapMux read out of it travels in the outcome note instead.
	content, metadata := event.persistBytes(), []byte(nil)
	if recovered != nil {
		content, metadata = lastFrame, recovered.outcome
	}
	if content != nil {
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: content, Metadata: metadata}, agent.SpanInfo{
			SpanID:   payload.ToolCallID,
			SpanType: toolName,
			Closing:  true,
		}); err != nil {
			slog.Error("zcode persist tool result", "agent_id", a.AgentID(), "error", err)
		}
	}
	sink.CloseSpan(payload.ToolCallID)

	backgroundLaunch := zcodeSubagentLaunchedInBackground(payload)
	if backgroundLaunch {
		a.children.markBackground(payload.ToolCallID)
	}
	if !backgroundLaunch {
		a.persistZCodeSubagentReport(payload)
		a.applyZCodeSubagentEnd(payload, recovered)
		a.closeZCodeSubagentChild(payload)
	}
	a.children.forgetTool(payload.ToolCallID)
	if !backgroundLaunch {
		a.children.forgetTitle(payload.ToolCallID)
	}
	a.toolCallPrompts.Take(payload.ToolCallID)
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
		RowKey: payload.ToolCallID,
		Write: agent.SubagentReportWrite{
			ReportID: zcodeFinalReportID(payload.ToolCallID),
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

// zcodeSubagentRowKey is the registry row key for one subagent.
//
// The TOOL-CALL id is the key, because it is the one identifier both events that
// speak about a subagent carry: the tool.updated that runs it, and the
// background-task session.updated that gives it a transcript. Keying the two paths
// differently produced a second row for the same subagent, one of them permanently
// Running.
//
// It is also the key EnsureChildAgent is given, because the registry row it creates
// is keyed by that same argument -- so the row the child linkage lands on and the
// row the lifecycle updates land on are one row.
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
	rowKey := payload.ToolCallID
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
	status := bgtask.StatusCompleted
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
