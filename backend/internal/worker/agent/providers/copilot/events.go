package copilot

import (
	"cmp"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

const (
	// copilotMethodSessionEvent carries every frame the transcript reads.
	copilotMethodSessionEvent = contracts.CopilotMethodSessionEvent
	// copilotMethodSessionLifecycle carries the runtime's own session metadata: the
	// start time and the last modified time. One trivial turn sends ten of them.
	copilotMethodSessionLifecycle = "session.lifecycle"
	// copilotMethodHostEvent carries the runtime's feature-flag map, which is some
	// four kilobytes of switches that describe the runtime rather than the work.
	copilotMethodHostEvent = "host.event"
)

// copilotNewInboxMessage announces native inbox state that LeapMux does not display.
const copilotNewInboxMessage = "new_inbox_message"

// copilotPDFMIMEType is the one document type that LeapMux sends to Copilot.
const copilotPDFMIMEType = "application/pdf"

// copilotAttachmentBlob is the attachment type of the inline bytes that LeapMux
// sends. A `file` attachment states a path, which LeapMux never sends.
const copilotAttachmentBlob = "blob"

// copilotUserMessageDocuments is the part of a root `user.message` echo that
// states how Copilot gave the message's documents to the model.
type copilotUserMessageDocuments struct {
	Attachments []struct {
		Type        string `json:"type"`
		DisplayName string `json:"displayName"`
		MimeType    string `json:"mimeType"`
	} `json:"attachments"`
	// SupportedNativeDocumentMimeTypes lists the document types that Copilot
	// sent to the model as files. Copilot sends a document of any other type as
	// its file name alone. Nil means that the echo does not state the list.
	SupportedNativeDocumentMimeTypes *[]string `json:"supportedNativeDocumentMimeTypes"`
}

// reportUnreadDocuments tells the reader which PDFs of their message reached the
// model as a file name alone.
//
// The model API decides it, and Copilot states no model's API before a send:
// the Responses and the Messages APIs take a PDF as a file, and Chat Completions
// does not. The echo of the message states the outcome, so the notice follows
// the message instead of refusing the PDF. An echo that does not state the list
// adds no notice. The caller holds outputMu.
func (a *Agent) reportUnreadDocuments(data json.RawMessage) {
	var documents copilotUserMessageDocuments
	if err := json.Unmarshal(data, &documents); err != nil || documents.SupportedNativeDocumentMimeTypes == nil {
		return
	}
	for _, mimeType := range *documents.SupportedNativeDocumentMimeTypes {
		if copilotIsPDF(mimeType) {
			return
		}
	}
	var names []string
	for _, attachment := range documents.Attachments {
		if attachment.Type != copilotAttachmentBlob || !copilotIsPDF(attachment.MimeType) {
			continue
		}
		names = append(names, cmp.Or(strings.TrimSpace(attachment.DisplayName), "a PDF file"))
	}
	if len(names) == 0 {
		return
	}
	a.sink.PersistLeapMuxNotification(map[string]interface{}{
		contracts.NotificationFieldType: contracts.NotificationTypeAgentStatus,
		contracts.NotificationFieldText: copilotUnreadDocumentsNotice(names),
	})
}

// copilotIsPDF reports whether a MIME type is the PDF type, whatever its case.
func copilotIsPDF(mimeType string) bool {
	return strings.EqualFold(strings.TrimSpace(mimeType), copilotPDFMIMEType)
}

// copilotUnreadDocumentsNotice states which PDFs reached the model as their
// file names alone, and the remedy.
func copilotUnreadDocumentsNotice(names []string) string {
	if len(names) == 1 {
		return fmt.Sprintf("Copilot sent only the file name of %s to the model, because the selected model does not read PDF files. "+
			"Choose another model to send the file itself.", names[0])
	}
	return fmt.Sprintf("Copilot sent only the file names of %s to the model, because the selected model does not read PDF files. "+
		"Choose another model to send the files themselves.", strings.Join(names, ", "))
}

// copilotAgentIdle announces that a background subagent finished a turn and
// waits for its next message. Copilot sends `subagent.completed` after the
// first turn alone, so this notice is what states each later idle.
const copilotAgentIdle = "agent_idle"

// copilotRetry holds one native agent's retry state under outputMu.
type copilotRetry struct {
	resolution providerkit.RetryResolution
	count      int
	lastError  string
}

// copilotDroppedEvents contains frames whose state the Worker consumes without a row.
// The runtime neither persists nor replays live shell output chunks.
var copilotDroppedEvents = map[string]struct{}{
	contracts.CopilotEventSessionStart:                           {},
	contracts.CopilotEventSessionResume:                          {},
	contracts.CopilotEventSessionShutdown:                        {},
	contracts.CopilotEventSessionTitleChanged:                    {},
	contracts.CopilotEventSessionTodosChanged:                    {},
	contracts.CopilotEventSessionPlanChanged:                     {},
	contracts.CopilotEventSessionUsageInfo:                       {},
	contracts.CopilotEventSessionLimitsChanged:                   {},
	contracts.CopilotEventSessionModeChanged:                     {},
	contracts.CopilotEventSessionPermissionsChanged:              {},
	contracts.CopilotEventSessionModelChange:                     {},
	contracts.CopilotEventSessionAutopilotObjectiveChanged:       {},
	contracts.CopilotEventAssistantTurnStart:                     {},
	contracts.CopilotEventAssistantTurnEnd:                       {},
	contracts.CopilotEventAssistantUsage:                         {},
	contracts.CopilotEventAssistantMessageDelta:                  {},
	contracts.CopilotEventAssistantStreamingDelta:                {},
	contracts.CopilotEventAssistantToolCallDelta:                 {},
	contracts.CopilotEventAssistantReasoningDelta:                {},
	contracts.CopilotEventToolProgress:                           {},
	contracts.CopilotEventToolPartialResult:                      {},
	contracts.CopilotEventToolShellOutput:                        {},
	contracts.CopilotEventSubagentStarted:                        {},
	contracts.CopilotEventSubagentConfigured:                     {},
	contracts.CopilotEventSessionTaskComplete:                    {},
	contracts.CopilotEventAbort:                                  {},
	contracts.CopilotEventUserInputCompleted:                     {},
	contracts.CopilotEventExitPlanModeCompleted:                  {},
	contracts.CopilotEventElicitationCompleted:                   {},
	contracts.CopilotEventUserMessage:                            {},
	contracts.CopilotEventAssistantMessageStart:                  {},
	contracts.CopilotEventAssistantIdle:                          {},
	contracts.CopilotEventAssistantIntent:                        {},
	contracts.CopilotEventAssistantServerToolProgress:            {},
	contracts.CopilotEventMcpAppToolCallComplete:                 {},
	contracts.CopilotEventCapabilitiesChanged:                    {},
	contracts.CopilotEventCommandsChanged:                        {},
	contracts.CopilotEventSessionToolsUpdated:                    {},
	contracts.CopilotEventSessionSkillsLoaded:                    {},
	contracts.CopilotEventSessionExtensionsLoaded:                {},
	contracts.CopilotEventSessionCustomAgentsUpdated:             {},
	contracts.CopilotEventSessionExtensionsAttachmentsPushed:     {},
	contracts.CopilotEventSessionMcpServersLoaded:                {},
	contracts.CopilotEventSessionMcpServerToolsLoaded:            {},
	contracts.CopilotEventSessionMcpServerStatusChanged:          {},
	contracts.CopilotEventSessionMcpServerRemoved:                {},
	contracts.CopilotEventSessionMcpServerNeedsReconnect:         {},
	contracts.CopilotEventMcpToolsListChanged:                    {},
	contracts.CopilotEventMcpResourcesListChanged:                {},
	contracts.CopilotEventMcpPromptsListChanged:                  {},
	contracts.CopilotEventSessionBackgroundTasksChanged:          {},
	contracts.CopilotEventPendingMessagesModified:                {},
	contracts.CopilotEventSubagentSelected:                       {},
	contracts.CopilotEventSubagentDeselected:                     {},
	contracts.CopilotEventToolSearchActivated:                    {},
	contracts.CopilotEventSessionUsageCheckpoint:                 {},
	contracts.CopilotEventSessionContextChanged:                  {},
	contracts.CopilotEventSessionCompletionReceipt:               {},
	contracts.CopilotEventSessionManagedSettingsResolved:         {},
	contracts.CopilotEventSessionManagedSettingsEnforced:         {},
	contracts.CopilotEventSessionAutoModeResolved:                {},
	contracts.CopilotEventSessionRemoteSteerableChanged:          {},
	contracts.CopilotEventSessionModeNoticeDelivered:             {},
	contracts.CopilotEventSessionBinaryAsset:                     {},
	contracts.CopilotEventSessionSnapshotRewind:                  {},
	contracts.CopilotEventSessionWorkspaceFileChanged:            {},
	contracts.CopilotEventSessionHandoff:                         {},
	contracts.CopilotEventUiEphemeralQuery:                       {},
	contracts.CopilotEventInstructionUpdate:                      {},
	contracts.CopilotEventSamplingCompleted:                      {},
	contracts.CopilotEventExternalToolCompleted:                  {},
	contracts.CopilotEventCommandQueued:                          {},
	contracts.CopilotEventCommandCompleted:                       {},
	contracts.CopilotEventAutoModeSwitchCompleted:                {},
	contracts.CopilotEventSessionLimitsExhaustedCompleted:        {},
	contracts.CopilotEventSystemMessage:                          {},
	contracts.CopilotEventPermissionAssentDetected:               {},
	contracts.CopilotEventPermissionContextualAuthorization:      {},
	contracts.CopilotEventPermissionMessageAuthorization:         {},
	contracts.CopilotEventPermissionMessageAuthorizationDegraded: {},
	contracts.CopilotEventPermissionMessageAuthorizationRead:     {},
	contracts.CopilotEventSessionPermissionRecovery:              {},
	contracts.CopilotEventSkillContextDelivered:                  {},
	contracts.CopilotEventSkillContextDeliveredRef:               {},
}

// copilotStoredEphemeralEvents contains notices that the browser can display.
var copilotStoredEphemeralEvents = map[string]struct{}{
	contracts.CopilotEventSessionCustomNotification:     {},
	contracts.CopilotEventSessionAutoTierRecommendation: {},
	contracts.CopilotEventSessionAutoTierSwitchFailed:   {},
	contracts.CopilotEventMcpOauthRequired:              {},
	contracts.CopilotEventMcpOauthCompleted:             {},
	contracts.CopilotEventMcpHeadersRefreshRequired:     {},
	contracts.CopilotEventMcpHeadersRefreshCompleted:    {},
}

var copilotKnownEvents = func() map[string]struct{} {
	events := make(map[string]struct{}, len(contracts.CopilotEventKeys))
	for _, event := range contracts.CopilotEventKeys {
		events[event] = struct{}{}
	}
	return events
}()

// A session can run for hours. Keep only recent reasoning IDs for duplicate checks.
const maxCopilotReasoningIDs = 256

// copilotMethodIsTelemetry reports whether a method describes the RUNTIME rather
// than the work.
//
// Neither of these carries conversation, and neither feeds a counter LeapMux shows,
// so the transcript is better without them. Both remain in the runtime's own session
// store, which is where a reader who wants them looks.
func copilotMethodIsTelemetry(method string) bool {
	return method == copilotMethodSessionLifecycle || method == copilotMethodHostEvent
}

// copilotEventIsRuntimeTrace tests the shared runtime-trace prefixes.
// The Worker and browser both read every member of contracts.CopilotEventPrefixKeys.
// A partial Worker list would store rows that the browser always hides.
//
// The model family repeats model input and output already present in assistant and tool events.
// A single probed turn sent 71 kilobytes of this trace data.
// The hook family records internal hook execution. A hook that changes a tool call has that call's own rows.
// The remaining families describe native experiments that no renderer displays.
// The dispatch reads model failures and retries before it applies this trace rule.
func copilotEventIsRuntimeTrace(eventType string) bool {
	for _, prefix := range contracts.CopilotEventPrefixKeys {
		if strings.HasPrefix(eventType, prefix) {
			return true
		}
	}
	return false
}

// copilotStreamedText is one accumulating assistant segment and the transcript it
// belongs to. The identity is the runtime's own message or reasoning id.
type copilotStreamedText struct {
	agentID string
	scope   string
	sink    agent.ProviderServices
}

// copilotOpenTool remembers where one running tool call belongs.
//
// The completion event repeats the tool-call ID and nothing else that identifies
// the transcript, so the start event is the only place that link can be made.
// That start event is also the frame a turn end stores when the runtime never
// finishes the call, so its bytes and its arrival order are kept here too.
type copilotOpenTool struct {
	child      *copilotNativeChild
	name       string
	arguments  json.RawMessage
	spawns     bool
	startFrame []byte
	order      uint64
	// sawOutput records that this call already produced real output, so a later
	// progress SENTENCE does not replace it. See reportNativeToolOutput.
	sawOutput bool
}

// copilotToolStart is the part of `tool.execution_start` that LeapMux reads.
type copilotToolStart struct {
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Arguments  json.RawMessage `json:"arguments"`
}

// handleNativeEvent routes one decoded session event. It returns with the event
// persisted exactly once, in the transcript that owns it.
//
// The caller holds outputMu, which is also what guards every map below. Each
// branch that needs a round trip to the runtime starts a goroutine, because the
// response for such a request arrives on THIS goroutine and a direct call would
// wait for itself.
func (a *Agent) handleNativeEvent(raw []byte, event copilotEvent) {
	if retry := a.retries[event.AgentID]; retry != nil {
		switch event.Type {
		case contracts.CopilotEventAssistantMessageDelta, contracts.CopilotEventAssistantStreamingDelta,
			contracts.CopilotEventAssistantMessage, contracts.CopilotEventAssistantReasoningDelta,
			contracts.CopilotEventAssistantReasoning, contracts.CopilotEventToolStarted:
			retry.resolution.Resolve(a.sinkFor(a.childForEvent(event)))
		}
	}
	if a.handleNativeControlEvent(raw, event) {
		return
	}
	if event.Type == contracts.CopilotEventSystemNotification {
		var notice struct {
			Kind struct {
				Type    string `json:"type"`
				AgentID string `json:"agentId"`
			} `json:"kind"`
		}
		if json.Unmarshal(event.Data, &notice) == nil {
			switch notice.Kind.Type {
			case copilotNewInboxMessage:
				return
			case copilotAgentIdle:
				// The notice still reaches the parent's transcript below.
				a.markNativeChildIdle(a.children[notice.Kind.AgentID])
			}
		}
	}
	switch event.Type {
	case contracts.CopilotEventSubagentStarted:
		a.startNativeSubagent(raw, event)
		return
	case contracts.CopilotEventSubagentCompleted, contracts.CopilotEventSubagentFailed:
		a.finishNativeSubagent(raw, event)
		return
	case contracts.CopilotEventToolStarted:
		a.startNativeTool(raw, event)
		return
	case contracts.CopilotEventToolCompleted:
		a.completeNativeTool(raw, event)
		return
	}
	child := a.childForEvent(event)
	sink := a.sinkFor(child)
	switch event.Type {
	case contracts.CopilotEventUserMessage:
		if event.AgentID == "" {
			var message struct {
				Content json.RawMessage `json:"content"`
			}
			if json.Unmarshal(event.Data, &message) == nil {
				if text, ok := providerkit.RawString(message.Content); ok && a.steers.EchoKey(text) {
					a.steers.Settle(a.sink)
				}
			}
			a.reportUnreadDocuments(event.Data)
		}
		return
	case contracts.CopilotEventModelCallFailure:
		errorText := strings.TrimSpace(providerkit.StringField(event.Data, "errorMessage"))
		if message := providerkit.StringField([]byte(errorText), "message"); message != "" {
			errorText = strings.TrimSpace(message)
		}
		a.nativeRetry(event.AgentID).lastError = errorText
		return
	case contracts.CopilotEventModelTurnRetry, contracts.CopilotEventAssistantTurnRetry:
		a.retryNativeTurn(event.AgentID, sink)
		return
	case contracts.CopilotEventAssistantTurnStart:
		if child == nil && !a.closing {
			a.setNativeTurnActive(true)
		}
		// A background subagent that a message woke starts its next turn.
		a.resumeNativeChild(child)
	case contracts.CopilotEventSessionIdle:
		if child == nil {
			a.endNativeTurn(raw, copilotTurnWasAborted(event.Data))
			return
		}
		a.resetNativeRetry(event.AgentID)
	case contracts.CopilotEventSessionCompactionStart:
		providerkit.PublishCompaction(sink, providerkit.StringField(event.Data, "trigger"))
		return
	case contracts.CopilotEventSessionCompactionComplete:
		a.persistNativeFrameTo(sink, raw, agent.SpanInfo{})
		providerkit.EndCompaction(sink)
		return
	case contracts.CopilotEventSessionTodosChanged:
		a.offReader("sql-todos", a.refreshSqlTodos)
		return
	case contracts.CopilotEventSessionUsageInfo:
		a.reportNativeContextUsage(sink, event.Data)
	case contracts.CopilotEventAssistantUsage:
		// Quota snapshots describe account state and do not create transcript rows.
		a.reportNativeQuota(sink, event.Data)
		return
	case contracts.CopilotEventSessionUsageCheckpoint:
		// Credit totals and model cache state. LeapMux surfaces neither, and the
		// runtime does not mark the event ephemeral, so the transcript would hold one
		// row of counters for every turn.
		return
	case contracts.CopilotEventAssistantMessageDelta, contracts.CopilotEventAssistantStreamingDelta,
		contracts.CopilotEventAssistantReasoningDelta:
		a.reportNativeTextProgress(sink, event)
	case contracts.CopilotEventAssistantReasoning:
		if !a.shouldPersistNativeReasoning(event.Data) {
			return
		}
		a.reportNativeTextProgress(sink, event)
	case contracts.CopilotEventAssistantMessage:
		a.persistEmbeddedReasoning(sink, event)
		a.reportNativeTextProgress(sink, event)
	case contracts.CopilotEventSessionModeChanged, contracts.CopilotEventSessionPermissionsChanged,
		contracts.CopilotEventSessionModelChange:
		// No request here can remove the tools of a mode that ended. Copilot CLI 1.0.103
		// and later keep the tool list and the system message of a model request
		// append-only. A switch to Plan adds `exit_plan_mode` and `task_complete`, and a
		// switch back keeps both and appends a notice that Plan ended. The runtime refuses
		// a call to a kept tool outside its mode. session.tools.initializeAndValidate
		// rebuilds the session's tool list, not the tool list of the model request.
		a.refreshNativeSettingsInBackground()
	case contracts.CopilotEventSessionAutopilotObjectiveChanged:
		a.refreshNativeGoalInBackground()
	case contracts.CopilotEventToolProgress, contracts.CopilotEventToolPartialResult,
		contracts.CopilotEventToolShellOutput:
		// The only text a running call reports. It was dropped UNREAD before, so the
		// row stayed blank for the whole call.
		//
		// The drop is LeapMux's own decision rather than the runtime's `ephemeral`
		// mark: the frame carries a window on output the finished row states in
		// full, so a transcript row for one would repeat the result in pieces. The
		// mark is set today, and a build that stopped setting it would otherwise
		// write a row per progress tick.
		//
		// `tool.shell_output` is the shape Copilot CLI 1.0.103 streams a running
		// shell call in: one chunk of `text` per stream and sequence.
		a.reportNativeToolOutput(sink, event.Data)
		return
	}
	if _, dropped := copilotDroppedEvents[event.Type]; dropped || copilotEventIsRuntimeTrace(event.Type) {
		return
	}
	if _, stored := copilotStoredEphemeralEvents[event.Type]; !stored && event.Ephemeral {
		if _, known := copilotKnownEvents[event.Type]; known {
			return
		}
	}

	a.persistNativeFrameTo(sink, raw, agent.SpanInfo{})
}

// reportNativeToolOutput carries the live text of a running call to its own row.
//
// partialOutput contains new tool output, and so does the `text` of one
// `tool.shell_output` chunk. progressMessage contains a runtime status sentence.
// None of these fields is cumulative.
// The runtime can interleave both fields. Once output arrives, it takes priority over status sentences.
// Before any output arrives, the row shows the status sentence.
//
// The caller holds outputMu, which guards openTools.
func (a *Agent) reportNativeToolOutput(sink agent.ProviderServices, data json.RawMessage) {
	var frame struct {
		ToolCallID      string `json:"toolCallId"`
		ProgressMessage string `json:"progressMessage"`
		PartialOutput   string `json:"partialOutput"`
		Text            string `json:"text"`
	}
	if err := json.Unmarshal(data, &frame); err != nil || frame.ToolCallID == "" {
		return
	}
	if frame.PartialOutput == "" {
		frame.PartialOutput = frame.Text
	}
	tool := a.openTools[frame.ToolCallID]
	if frame.PartialOutput != "" {
		if tool != nil {
			tool.sawOutput = true
		}
		// A partial output is a WINDOW on more by definition.
		sink.ReportProgress(agent.OutputTailProgress(frame.ToolCallID, frame.PartialOutput, true))
		return
	}
	// A call whose start this process never saw has no record to read, so its
	// sentence still shows: it is the only thing there is.
	if frame.ProgressMessage == "" || (tool != nil && tool.sawOutput) {
		return
	}
	// A whole sentence the runtime wrote, so it states no loss.
	sink.ReportProgress(agent.OutputTailProgress(frame.ToolCallID, frame.ProgressMessage, false))
}

// copilotTurnWasAborted reports whether the reader stopped the turn that is ending.
// The runtime states it on the idle event, and the browser reads the same field.
func copilotTurnWasAborted(data json.RawMessage) bool {
	var idle struct {
		Aborted bool `json:"aborted"`
	}
	return json.Unmarshal(data, &idle) == nil && idle.Aborted
}

func (a *Agent) endNativeTurn(raw []byte, aborted bool) {
	a.resetNativeRetry("")
	if !a.setNativeTurnActive(false) {
		// Idle before a turn or after its end supplies no new transcript boundary.
		return
	}
	// A segment the runtime never finished ends with the turn. It reads as interrupted
	// only when the turn was: a turn that ended by itself and left a segment open
	// produced all the text it was going to.
	completion := agent.MessageCompletionComplete
	if aborted {
		completion = agent.MessageCompletionInterrupted
	}
	a.closeStreamedNativeText(completion)
	a.closeOpenNativeTools()
	a.sink.ReportProgress(agent.ResetProgress())
	// The runtime states no count, so the Worker states its own, zero included. The
	// browser rings the completion sound for a turn that states no count, and an
	// explicit zero keeps a turn without tools quiet.
	content := agent.MessageContent{Original: raw, AgentSessionID: a.currentNativeSessionID()}
	uses := a.turnToolUses
	a.turnToolUses = 0
	if err := a.sink.PersistTurnEnd(agent.WithToolUseCount(content, uses), agent.SpanInfo{}); err != nil {
		slog.Error("Persist Copilot turn end", "agent_id", a.AgentID(), "error", err)
	}
}

// closeOpenNativeTools ends every call the runtime started and never finished.
//
// An interrupted turn can omit the completion frame. Without a closing row, the card keeps its running badge.
// The closing row contains the native start frame. LeapMux's completion column states that the call did not finish.
// The rows keep the order in which the calls opened.
func (a *Agent) closeOpenNativeTools() {
	open := a.openTools
	a.openTools = nil
	ids := make([]string, 0, len(open))
	for id := range open {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(left, right int) bool {
		if open[ids[left]].order != open[ids[right]].order {
			return open[ids[left]].order < open[ids[right]].order
		}
		return ids[left] < ids[right]
	})
	for _, id := range ids {
		tool := open[id]
		sink := a.sinkFor(tool.child)
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{
			Original:       tool.startFrame,
			AgentSessionID: a.currentNativeSessionID(),
			Completion:     agent.MessageCompletionInterrupted,
		}, agent.SpanInfo{SpanID: id, SpanType: tool.name, Closing: true}); err != nil {
			slog.Error("Persist Copilot interrupted tool", "agent_id", a.AgentID(), "tool_call_id", id, "error", err)
		}
		if !tool.spawns {
			sink.CloseSpan(id)
		}
	}
}

// startNativeTool opens the tool call's span in the transcript that owns it.
func (a *Agent) startNativeTool(raw []byte, event copilotEvent) {
	var start copilotToolStart
	if err := json.Unmarshal(event.Data, &start); err != nil || start.ToolCallID == "" || start.ToolName == "" {
		slog.Warn("Read Copilot tool start", "agent_id", a.AgentID(), "error", err)
		a.persistNativeFrameTo(a.sinkFor(a.childForEvent(event)), raw, agent.SpanInfo{})
		return
	}
	child := a.childForEvent(event)
	tool := &copilotOpenTool{
		child: child, name: start.ToolName,
		arguments:  append(json.RawMessage(nil), start.Arguments...),
		spawns:     start.ToolName == contracts.CopilotToolTask,
		startFrame: append([]byte(nil), raw...),
		order:      a.nextNativeToolOrder,
	}
	a.nextNativeToolOrder++
	if a.openTools == nil {
		a.openTools = make(map[string]*copilotOpenTool)
	}
	a.openTools[start.ToolCallID] = tool
	// A subagent's calls belong to its own transcript. The Task call that started the
	// subagent is one call of this turn.
	if child == nil {
		a.turnToolUses++
	}
	content := agent.MessageContent{Original: raw, AgentSessionID: a.currentNativeSessionID()}
	if err := providerkit.OpenToolSpan(a.sinkFor(child), content, start.ToolCallID, start.ToolName, tool.spawns); err != nil {
		slog.Error("Persist Copilot tool call", "agent_id", a.AgentID(), "tool_call_id", start.ToolCallID, "error", err)
	}
}

// completeNativeTool closes the span that startNativeTool opened.
//
// A completion whose start this process never saw still reaches its transcript:
// the span type is absent, so the row carries the tool-call ID alone and the
// renderer shows the result without a request.
func (a *Agent) completeNativeTool(raw []byte, event copilotEvent) {
	var complete copilotToolCompletionData
	if err := json.Unmarshal(event.Data, &complete); err != nil || complete.ToolCallID == "" {
		slog.Warn("Read Copilot tool completion", "agent_id", a.AgentID(), "error", err)
		a.persistNativeFrameTo(a.sinkFor(a.childForEvent(event)), raw, agent.SpanInfo{})
		return
	}
	tool := a.openTools[complete.ToolCallID]
	delete(a.openTools, complete.ToolCallID)
	child := a.childForEvent(event)
	spanType := ""
	spawns := false
	if tool != nil {
		child, spanType, spawns = tool.child, tool.name, tool.spawns
	}
	sink := a.sinkFor(child)
	content := agent.MessageContent{Original: raw, AgentSessionID: a.currentNativeSessionID(), Completion: complete.completion()}
	if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{
		SpanID: complete.ToolCallID, SpanType: spanType, Closing: true,
	}); err != nil {
		slog.Error("Persist Copilot tool result", "agent_id", a.AgentID(), "tool_call_id", complete.ToolCallID, "error", err)
	}
	if !spawns {
		sink.CloseSpan(complete.ToolCallID)
	}
}

// copilotToolCompletionData holds the fields of a `tool.execution_complete` that the
// worker reads.
type copilotToolCompletionData struct {
	ToolCallID string `json:"toolCallId"`
	Success    *bool  `json:"success"`
	Error      *struct {
		Code string `json:"code"`
	} `json:"error"`
}

// completion is LeapMux's own statement about how the call ended, for the completion
// column of its result row.
//
// The browser draws that statement as the row's header, in place of the outcome the
// call states itself. So the statement must agree with the runtime's structured
// `error.code`, never with the `success` flag alone:
//
//   - A call that succeeded, or that states no success value, takes no completion.
//   - An `interrupted` call takes the interrupted completion: an aborted turn cut it
//     short.
//   - A `rejected` or `denied` call takes no completion. A permission decision refused
//     it, so it never ran, and an error completion headed the declined row with
//     `Error`. The browser reads the refusal from the frame itself.
//   - Every other failure takes the error completion.
func (c copilotToolCompletionData) completion() agent.MessageCompletion {
	if c.Success == nil || *c.Success {
		return ""
	}
	code := ""
	if c.Error != nil {
		code = c.Error.Code
	}
	switch code {
	case contracts.CopilotToolErrorCodeInterrupted:
		return agent.MessageCompletionInterrupted
	case contracts.CopilotToolErrorCodeRejected, contracts.CopilotToolErrorCodeDenied:
		return ""
	default:
		return agent.MessageCompletionError
	}
}

// copilotResponseSizeScope identifies the counter for a stream that reports a size and no
// identity. The size is a RUNNING TOTAL for the whole response, so every report has
// to land on one scope: a scope for each event would add the totals instead of
// replacing one, and an eight-byte answer would count as thirty-six.
const copilotResponseSizeScope = "copilot:response"

// copilotAssistantText holds every field that an assistant text event carries.
//
// The runtime does not spell them alike: Copilot 1.0.24 streams both its answer and
// its reasoning as `deltaContent`, and sends the finished message as `content`.
// Reading each known spelling means a build that fills a different one needs no
// change here.
type copilotAssistantText struct {
	Content         string `json:"content"`
	Text            string `json:"text"`
	Delta           string `json:"delta"`
	DeltaContent    string `json:"deltaContent"`
	MessageID       string `json:"messageId"`
	ReasoningID     string `json:"reasoningId"`
	ReasoningText   string `json:"reasoningText"`
	ReasoningOpaque string `json:"reasoningOpaque"`
	ReasoningBlocks struct {
		Blocks []struct {
			ID      string `json:"id"`
			Summary []struct {
				Text string `json:"text"`
			} `json:"summary"`
		} `json:"blocks"`
	} `json:"reasoningBlocks"`
	// TotalResponseSizeBytes is the running size of the answer, which is all the
	// answer's own stream carries today.
	TotalResponseSizeBytes int64 `json:"totalResponseSizeBytes"`
}

// body reports the text one assistant event carries, whichever field holds it.
func (t copilotAssistantText) body() string {
	return sessionstore.FirstNonBlank(t.DeltaContent, t.Delta, t.Content, t.Text)
}

// embeddedReasoning takes the summary that the runtime put on a finished answer.
// The summary field is the fallback when the runtime omits reasoningText.
func (t copilotAssistantText) embeddedReasoning() (string, string) {
	id := sessionstore.FirstNonBlank(t.ReasoningID, t.ReasoningOpaque)
	if id == "" && len(t.ReasoningBlocks.Blocks) > 0 {
		id = t.ReasoningBlocks.Blocks[0].ID
	}
	text := t.ReasoningText
	if strings.TrimSpace(text) == "" {
		var parts []string
		for _, block := range t.ReasoningBlocks.Blocks {
			for _, summary := range block.Summary {
				if strings.TrimSpace(summary.Text) != "" {
					parts = append(parts, summary.Text)
				}
			}
		}
		text = strings.Join(parts, "\n")
	}
	if strings.TrimSpace(text) == "" {
		return "", ""
	}
	return id, text
}

// shouldPersistNativeReasoning records the ID of a separate finished reasoning event.
// The caller holds outputMu, which also guards the recent-ID map.
func (a *Agent) shouldPersistNativeReasoning(data json.RawMessage) bool {
	var text copilotAssistantText
	if json.Unmarshal(data, &text) != nil {
		return true
	}
	if strings.TrimSpace(text.body()) == "" {
		return false
	}
	if text.ReasoningID == "" {
		return true
	}
	if a.sawReasoning(text.ReasoningID) {
		return false
	}
	a.rememberReasoning(text.ReasoningID)
	return true
}

// sawReasoning checks one native ID within its session. The caller holds outputMu.
func (a *Agent) sawReasoning(id string) bool {
	_, exists := a.reasoningSeen[a.currentNativeSessionID()+"\x00"+id]
	return exists
}

// rememberReasoning limits duplicate state without discarding IDs from a busy turn.
// The caller holds outputMu.
func (a *Agent) rememberReasoning(id string) {
	key := a.currentNativeSessionID() + "\x00" + id
	if a.reasoningSeen == nil {
		a.reasoningSeen = make(map[string]struct{})
	}
	if _, exists := a.reasoningSeen[key]; exists {
		return
	}
	if len(a.reasoningOrder) >= maxCopilotReasoningIDs {
		delete(a.reasoningSeen, a.reasoningOrder[0])
		a.reasoningOrder[0] = ""
		a.reasoningOrder = a.reasoningOrder[1:]
	}
	a.reasoningSeen[key] = struct{}{}
	a.reasoningOrder = append(a.reasoningOrder, key)
}

// persistEmbeddedReasoning restores the thought that Copilot's Responses path
// sends inside assistant.message instead of a separate assistant.reasoning event.
func (a *Agent) persistEmbeddedReasoning(sink agent.ProviderServices, event copilotEvent) {
	var text copilotAssistantText
	if json.Unmarshal(event.Data, &text) != nil {
		return
	}
	id, content := text.embeddedReasoning()
	if content == "" {
		return
	}
	if id == "" {
		id = event.ID + "-reasoning"
	}
	if a.sawReasoning(id) {
		return
	}
	a.rememberReasoning(id)
	data, err := json.Marshal(map[string]string{"reasoningId": id, "content": content})
	if err != nil {
		slog.Error("Encode Copilot reasoning", "agent_id", a.AgentID(), "error", err)
		return
	}
	reasoning := copilotEvent{
		ID: event.ID + "-reasoning", Type: contracts.CopilotEventAssistantReasoning,
		AgentID: event.AgentID, ParentID: event.ParentID, Data: data,
	}
	frame, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": copilotMethodSessionEvent,
		"params": map[string]any{"sessionId": a.currentNativeSessionID(), "event": reasoning},
	})
	if err != nil {
		slog.Error("Encode Copilot reasoning frame", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.reportNativeTextProgress(sink, reasoning)
	a.persistNativeFrameTo(sink, frame, agent.SpanInfo{})
}

// copilotAssistantTextKind reports which assembled segment one assistant event
// contributes to, and whether it is a streaming DELTA rather than the finished
// message.
//
// Copilot 1.0.24 streams the answer's text on `assistant.message_delta` and, in
// parallel, the answer's running SIZE on `assistant.streaming_delta`. Both belong to
// the answer, so both map to the same segment: whichever one a later build fills is
// the one that counts, and a third name costs one contract entry and one case.
func copilotAssistantTextKind(eventType string) (agent.AssembledMessageKind, bool, bool) {
	switch eventType {
	case contracts.CopilotEventAssistantReasoningDelta:
		return agent.AssembledMessageKindReasoning, true, true
	case contracts.CopilotEventAssistantReasoning:
		return agent.AssembledMessageKindReasoning, false, true
	case contracts.CopilotEventAssistantMessageDelta, contracts.CopilotEventAssistantStreamingDelta:
		return agent.AssembledMessageKindText, true, true
	case contracts.CopilotEventAssistantMessage:
		return agent.AssembledMessageKindText, false, true
	}
	return "", false, false
}

// reportNativeTextProgress publishes the live counters for one assistant event, and
// accumulates whatever text that event streams.
//
// The scope identifies one message or reasoning segment.
// The completed message replaces that scope's deltas, so the counter does not count the text twice.
// The runtime sends the authoritative message only when it completes.
// A size-only report uses a separate response scope because it identifies no message and counts the whole response.
func (a *Agent) reportNativeTextProgress(sink agent.ProviderServices, event copilotEvent) {
	kind, isDelta, ok := copilotAssistantTextKind(event.Type)
	if !ok {
		return
	}
	var text copilotAssistantText
	if json.Unmarshal(event.Data, &text) != nil {
		return
	}
	body := text.body()
	if body == "" && text.TotalResponseSizeBytes > 0 {
		sink.ReportProgress(agent.OutputExactTotalProgress(copilotResponseSizeScope, text.TotalResponseSizeBytes))
		return
	}
	// The scope is the runtime's own message or reasoning identity. The FRAME id is
	// not a substitute: it changes for every delta, so it would open one segment for
	// each delta, and the finished message would then discard none of them.
	scope := sessionstore.FirstNonBlank(text.MessageID, text.ReasoningID)
	if scope == "" {
		slog.Debug("Skip a Copilot assistant event that states no message identity",
			"agent_id", a.AgentID(), "event", event.Type, "frame_id", event.ID)
		return
	}
	if body != "" {
		sink.ReportProgress(agent.ModelTextProgress(scope, body))
	}
	if !isDelta {
		// The finished message is the row. Its own deltas are not a second one.
		a.dropStreamedNativeText(scope)
		return
	}
	a.appendStreamedNativeText(sink, event.AgentID, scope, kind, body)
}

// appendStreamedNativeText accumulates one delta under its message identity.
//
// The caller holds outputMu, which guards nativeTextSegments as it guards every
// other field the dispatch touches.
func (a *Agent) appendStreamedNativeText(sink agent.ProviderServices, agentID, scope string, kind agent.AssembledMessageKind, delta string) {
	if delta == "" {
		return
	}
	a.nativeText.Append(scope, kind, delta, providerkit.JoinVerbatim)
	for _, segment := range a.nativeTextSegments {
		if segment.scope == scope {
			return
		}
	}
	a.nativeTextSegments = append(a.nativeTextSegments, copilotStreamedText{agentID: agentID, scope: scope, sink: sink})
}

// dropStreamedNativeText forgets the deltas a finished message replaces.
func (a *Agent) dropStreamedNativeText(scope string) {
	a.nativeText.Discard(scope)
	a.nativeTextSegments = slices.DeleteFunc(a.nativeTextSegments, func(segment copilotStreamedText) bool {
		return segment.scope == scope
	})
}

// closeStreamedNativeText stores every segment the runtime streamed and never
// finished, as LeapMux's own assembled message.
//
// The runtime marks deltas ephemeral and sends a message frame only when the message completes.
// The assembled row retains the text from those deltas. Its completion column states that the segment did not finish.
func (a *Agent) closeStreamedNativeText(completion agent.MessageCompletion) {
	segments := a.nativeTextSegments
	a.nativeTextSegments = nil
	for _, segment := range segments {
		raw, ok, err := a.nativeText.Finish(segment.scope, completion)
		if err != nil {
			slog.Error("Assemble Copilot streamed text", "agent_id", a.AgentID(), "scope", segment.scope, "error", err)
			continue
		}
		if !ok {
			continue
		}
		sink := segment.sink
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{
			Original:       raw,
			AgentSessionID: a.currentNativeSessionID(),
			Completion:     completion,
		}, agent.SpanInfo{}); err != nil {
			slog.Error("Persist Copilot streamed text", "agent_id", a.AgentID(), "error", err)
		}
	}
}

// reportNativeContextUsage publishes the live context counter in the transcript that
// owns the event. A subagent reports its own context, so the root sink is not the
// answer for every caller.
func (a *Agent) reportNativeContextUsage(sink agent.ProviderServices, data json.RawMessage) {
	var usage struct {
		CurrentTokens *int64 `json:"currentTokens"`
		TokenLimit    *int64 `json:"tokenLimit"`
	}
	if json.Unmarshal(data, &usage) != nil || usage.CurrentTokens == nil {
		return
	}
	fields := map[string]interface{}{contracts.ContextUsageFieldContextTokens: *usage.CurrentTokens}
	if usage.TokenLimit != nil && *usage.TokenLimit > 0 {
		fields[contracts.ContextUsageFieldContextWindow] = *usage.TokenLimit
	}
	sink.BroadcastSessionInfo(map[string]interface{}{contracts.SessionInfoKeyContextUsage: fields})
}

// persistNativeFrameTo stores one native frame in the transcript that owns it.
func (a *Agent) persistNativeFrameTo(sink agent.ProviderServices, raw []byte, span agent.SpanInfo) {
	// Specialized handlers also store frames through this method after consuming their state.
	// Apply the drop rule here also, so control receipts and subagent starts store no row.
	var header struct {
		Method string `json:"method"`
		Params struct {
			Event struct {
				Type string `json:"type"`
			} `json:"event"`
		} `json:"params"`
	}
	if json.Unmarshal(raw, &header) == nil && header.Method == copilotMethodSessionEvent {
		if _, dropped := copilotDroppedEvents[header.Params.Event.Type]; dropped || copilotEventIsRuntimeTrace(header.Params.Event.Type) {
			return
		}
	}
	if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: raw, AgentSessionID: a.currentNativeSessionID()}, span); err != nil {
		slog.Error("Persist Copilot event", "agent_id", a.AgentID(), "error", err)
	}
}

func (a *Agent) HandleOutput(content []byte) {
	line := &providerkit.ParsedLine{Raw: content}
	if err := json.Unmarshal(content, line); err != nil {
		a.persistNativeFrame(content, agent.SpanInfo{})
		return
	}
	a.handleNativeOutput(line)
}

func (a *Agent) handleNativeOutput(line *providerkit.ParsedLine) {
	a.outputMu.Lock()
	defer a.outputMu.Unlock()
	if line.Method != copilotMethodSessionEvent {
		a.RefuseUnsupportedRequest(line)
		// An unrecognized method still reaches the transcript: a frame that carries
		// conversation is worse lost than shown as raw JSON.
		if !copilotMethodIsTelemetry(line.Method) {
			a.persistNativeFrame(line.Raw, agent.SpanInfo{})
		}
		return
	}
	event, err := decodeCopilotSessionEvent(line.Params, a.currentNativeSessionID())
	if err != nil {
		slog.Debug("Skip Copilot event with an invalid session", "error", err)
		return
	}
	a.handleNativeEvent(line.Raw, event)
}

func (a *Agent) persistNativeFrame(raw []byte, span agent.SpanInfo) {
	a.persistNativeFrameTo(a.sink, raw, span)
}

// nativeRetry returns one agent's state. The caller holds outputMu.
func (a *Agent) nativeRetry(nativeAgentID string) *copilotRetry {
	if a.retries == nil {
		a.retries = make(map[string]*copilotRetry)
	}
	retry := a.retries[nativeAgentID]
	if retry == nil {
		retry = &copilotRetry{}
		a.retries[nativeAgentID] = retry
	}
	return retry
}

// retryNativeTurn discards only the failed agent's open output before it records the retry.
func (a *Agent) retryNativeTurn(nativeAgentID string, sink agent.ProviderServices) {
	var scopes []string
	a.nativeTextSegments = slices.DeleteFunc(a.nativeTextSegments, func(segment copilotStreamedText) bool {
		if segment.agentID != nativeAgentID {
			return false
		}
		scopes = append(scopes, segment.scope)
		return true
	})
	providerkit.DiscardGenerationScopes(&a.nativeText, sink, scopes)
	// Size-only updates have no buffered segment, but belong to the failed attempt also.
	sink.ReportProgress(agent.ResetModelScopeProgress(copilotResponseSizeScope))
	retry := a.nativeRetry(nativeAgentID)
	retry.count++
	notice := map[string]any{
		contracts.NotificationFieldType:  contracts.NotificationTypeApiRetry,
		contracts.NotificationFieldCount: retry.count,
	}
	if retry.lastError != "" {
		notice[contracts.NotificationFieldError] = retry.lastError
	}
	sink.PersistLeapMuxNotification(notice)
	retry.resolution.NoteRetry()
}

// resetNativeRetry forgets a completed agent's retry state. The caller holds outputMu.
func (a *Agent) resetNativeRetry(nativeAgentID string) {
	if retry := a.retries[nativeAgentID]; retry != nil {
		retry.resolution.Reset()
		delete(a.retries, nativeAgentID)
	}
}

// refreshSqlTodos stores changed rows from the native session's SQL to-do table.
// offReader holds sessionMu for reading, so the snapshot keeps its session identity.
func (a *Agent) refreshSqlTodos() {
	raw, err := a.requestNativeSession("plan.readSqlTodos", nil)
	if err != nil {
		slog.Warn("read Copilot SQL to-dos", "agent_id", a.AgentID(), "error", err)
		return
	}
	var result struct {
		Rows json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		slog.Warn("decode Copilot SQL to-dos", "agent_id", a.AgentID(), "error", err)
		return
	}
	if _, ok := decodeCopilotSQLTodos(result.Rows); !ok {
		slog.Warn("Copilot returned invalid SQL to-do rows", "agent_id", a.AgentID())
		return
	}
	content, err := json.Marshal(map[string]any{"type": contracts.CopilotLeapmuxRowTypeSqlTodos, "rows": result.Rows})
	if err != nil {
		slog.Error("encode the SQL to-do snapshot for Copilot", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.outputMu.Lock()
	defer a.outputMu.Unlock()
	if agent.JSONCanonicalEqual(a.sqlTodos, result.Rows) {
		return
	}
	err = a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{
		Original: content, AgentSessionID: a.currentNativeSessionID(),
	}, agent.SpanInfo{})
	if err != nil {
		slog.Error("persist the SQL to-do snapshot for Copilot", "agent_id", a.AgentID(), "error", err)
		if !agent.OutputRetained(err) {
			return
		}
	}
	a.sqlTodos = slices.Clone(result.Rows)
}
