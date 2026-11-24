package kimi

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type kimiOpenLiveSpan struct {
	spanID       string
	parentSpanID string
	spanType     string
}

// The dispatcher owns the document and the publication of each native turn.
// A correction to an older turn keeps its original publication authority.
type kimiConversationOutput struct {
	document      *kimiConversationState
	turns         map[string]*kimiConversationTurnOutput
	activeTurnID  string
	latestOrdinal int64
	snapshotReady bool
	// TODO(https://github.com/leapmux/leapmux/issues/699): Seed live span geometry in TranscriptScopeFor so Fast-Agent and Kimi keep cross-turn colors without shadow open-span slices.
	openLiveSpans []kimiOpenLiveSpan
}

func (output *kimiConversationOutput) recordOpenLiveSpan(spanID, parentSpanID, spanType string) {
	for i := range output.openLiveSpans {
		if output.openLiveSpans[i].spanID == spanID {
			output.openLiveSpans[i].parentSpanID = parentSpanID
			if spanType != "" {
				output.openLiveSpans[i].spanType = spanType
			}
			return
		}
	}
	output.openLiveSpans = append(output.openLiveSpans, kimiOpenLiveSpan{
		spanID:       spanID,
		parentSpanID: parentSpanID,
		spanType:     spanType,
	})
}

func (output *kimiConversationOutput) removeOpenLiveSpan(spanID string) {
	output.openLiveSpans = slices.DeleteFunc(output.openLiveSpans, func(span kimiOpenLiveSpan) bool {
		return span.spanID == spanID
	})
}

type kimiConversationTurnOutput struct {
	writer     *kimiConversationRecordWriter
	scope      agent.TranscriptScope
	progress   agent.TranscriptPublication
	requests   map[string]struct{}
	results    map[string]struct{}
	completed  bool
	historical bool
	steerable  bool
	userTurn   bool
	usageStep  int64
	retries    map[int64]*kimiConversationRetryOutput
	// Native execution starts after model generation completes. Text from that
	// step keeps its own completed outcome if a later tool is interrupted.
	executionSteps map[int64]struct{}
	textRecords    map[int64]map[string]struct{}
}

func (a *Agent) conversationOutput(run *kimiRun, sessionID string) *kimiConversationOutput {
	if run.conversation == nil || run.conversation.document.sessionID != sessionID {
		run.conversation = &kimiConversationOutput{
			document: newKimiConversationState(sessionID, run.agentID),
			turns:    make(map[string]*kimiConversationTurnOutput), latestOrdinal: -1,
		}
	}
	return run.conversation
}

func (a *Agent) conversationTurnOutput(run *kimiRun, turn *kimiConversationTurnState) (*kimiConversationTurnOutput, error) {
	output := run.conversation
	if publication := output.turns[turn.header.TurnID]; publication != nil {
		return publication, nil
	}
	if run.agentID != kimiMainAgentID && (!a.activateChildLifetime(run) || !a.reviveNativeChildRow(run)) {
		return nil, fmt.Errorf("the Kimi child turn has no current transcript lifetime")
	}
	sink := a.runSink(run)
	if sink == nil {
		return nil, fmt.Errorf("the Kimi conversation has no adopted transcript")
	}
	writer, err := newKimiConversationRecordWriter(sink)
	if err != nil {
		return nil, err
	}
	anchor := agent.CaptureTranscript(writer, agent.MessageContent{AgentSessionID: output.document.sessionID}, agent.SpanInfo{})
	historical := turn.header.Status == contracts.KimiConversationStatusEnded || *turn.header.Ordinal < output.latestOrdinal
	var scope agent.TranscriptScope
	if historical {
		scope, err = writer.HistoricalTranscriptScopeFor(anchor)
	} else {
		scope, err = writer.TranscriptScopeFor(anchor)
	}
	if err != nil {
		return nil, err
	}
	if historical {
		anchor, err = scope.CaptureTranscript(agent.MessageContent{}, agent.SpanInfo{})
		if err != nil {
			return nil, err
		}
	} else {
		for _, live := range output.openLiveSpans {
			scope.OpenSpan(live.spanID, live.parentSpanID)
			if live.spanType != "" {
				scope.SetSpanType(live.spanID, live.spanType)
			}
		}
	}
	publication := &kimiConversationTurnOutput{writer: writer, scope: scope, progress: anchor.FrozenContent().Publication,
		requests: make(map[string]struct{}), results: make(map[string]struct{}), historical: historical,
		executionSteps: make(map[int64]struct{}), textRecords: make(map[int64]map[string]struct{})}
	output.turns[turn.header.TurnID] = publication
	return publication, nil
}

func (a *Agent) handleConversationFrame(source kimiFrame) {
	if !a.ownsNativeSession(source.SessionID) || a.IsDiscardingOutput() {
		return
	}
	frame, err := decodeKimiConversationFrame(source.Raw)
	if err != nil {
		slog.Warn("decode the Kimi conversation event", "agent_id", a.AgentID(), "error", err)
		return
	}
	// Kimi sends a child's first conversation page after its own subscription.
	// That subscription starts after the Worker adopts the child transcript.
	if frame.AgentID != kimiMainAgentID {
		if _, linked := a.children.get(frame.AgentID); !linked {
			return
		}
	}
	run := a.run(frame.AgentID)
	if frame.Type == "notice" {
		a.publishConversationNotice(run, frame)
		return
	}
	output := a.conversationOutput(run, frame.SessionID)
	if frame.Type == contracts.KimiFrameTurnsFailed {
		// The native subscription survives this failure and can send a later
		// replacement snapshot. Retire older page work, but retain admission.
		a.awaitConversationSnapshot(run)
		a.publishConversationFailure(run, frame, source.Raw)
		return
	}
	if frame.Type == "turns" {
		if *frame.HasMore {
			a.startConversationRecovery(run, frame, int64(source.wireSize))
			return
		}
		if recovery := run.conversationRecovery.Load(); recovery != nil {
			// Keep admission held while the complete replacement is projected.
			a.invalidateConversationRecovery(recovery)
			if err := a.applyConversationFrame(run, output, frame); err != nil {
				a.requestConversationResubscription(run, recovery)
				slog.Warn("apply the replacement Kimi snapshot", "agent_id", a.AgentID(), "error", err)
				return
			}
			a.replaceConversationRecovery(run, nil)
			a.publishConversationRecoveryState(run)
			return
		}
	} else if recovery := run.conversationRecovery.Load(); recovery != nil {
		a.retainConversationRecoveryFrame(run, recovery, frame, int64(source.wireSize))
		return
	}
	if err := a.applyConversationFrame(run, output, frame); err != nil {
		if errors.Is(err, errKimiConversationNeedsSnapshot) {
			recovery := a.awaitConversationSnapshot(run)
			a.invalidateConversationRecovery(recovery)
			a.requestConversationResubscription(run, recovery)
		}
		slog.Warn("apply the Kimi conversation event", "agent_id", a.AgentID(), "error", err)
	}
}

func (a *Agent) applyConversationFrame(run *kimiRun, output *kimiConversationOutput, frame kimiConversationFrame) error {
	previous := conversationTurnsBeforeRemoval(output.document, frame)
	changed, err := output.document.apply(frame)
	if err != nil {
		return err
	}
	if !changed && !conversationProjectionPending(output.document, frame) {
		return nil
	}
	a.closeWithdrawnConversationSpans(run, output, frame, previous)
	switch frame.Type {
	case "turns":
		output.snapshotReady = true
		a.withdrawAbsentConversationInvocations(run, output.document)
		turns := slices.Collect(maps.Values(output.document.turns))
		slices.SortFunc(turns, func(left, right *kimiConversationTurnState) int {
			if *left.header.Ordinal < *right.header.Ordinal {
				return -1
			}
			if *left.header.Ordinal > *right.header.Ordinal {
				return 1
			}
			return 0
		})
		for _, turn := range turns {
			a.projectConversationTurn(run, turn, frame)
		}
		if output.activeTurnID != "" && output.document.turns[output.activeTurnID] == nil {
			a.finishConversationActivity(run, output.activeTurnID)
		}
	case "turn.started", "turn.ended":
		a.projectConversationTurn(run, output.document.turns[frame.Turn.TurnID], frame)
	case "item.removed":
		identity := kimiInvocationIdentity{sessionID: frame.SessionID, agentID: run.agentID, turnID: frame.TurnID, toolCallID: frame.ItemID}
		a.withdrawConversationInvocation(run, run.childInvocations[identity])
		// A retry reuses its step index but removes the failed attempt's items.
		// Their stored outcomes must not follow the replacement attempt.
		if publication := output.turns[frame.TurnID]; publication != nil {
			for _, records := range publication.textRecords {
				delete(records, frame.ItemID)
			}
		}
	case "item.started", "item.ended", "item.updated", "item.delta", "turn.updated":
		turn := output.document.turns[frame.TurnID]
		if turn == nil {
			return nil
		}
		itemID := frame.ItemID
		if frame.Item != nil {
			itemID = frame.Item.ItemID
		}
		itemState := turn.items[itemID]
		publication, err := a.conversationTurnOutput(run, turn)
		if err != nil {
			if itemState != nil && frame.Type != contracts.KimiFrameItemDelta {
				itemState.projectionPending = true
			}
			slog.Warn("capture the Kimi conversation turn", "error", err)
			return nil
		}
		if frame.Type == "turn.updated" {
			a.observeConversationRetry(run, turn, publication, frame.Retry, frame.EventCreatedAt)
			if frame.Step != nil {
				a.publishConversationStepUsage(run, turn, publication, slices.Values([]kimiConversationStep{*frame.Step}))
				a.correctConversationStepCompletion(publication, frame, turn.header.TurnID, *frame.Step)
				if frame.Step.Outcome == contracts.KimiConversationOutcomeCompleted {
					a.resolveConversationRetry(publication, frame.Step.Index)
				}
			}
			return nil
		}
		if itemState == nil {
			return nil
		}
		a.resolveConversationRetryFromOutput(publication, turn, itemState, frame)
		if frame.Type == "item.delta" {
			publication.reportDelta(frame, itemState)
			return nil
		}
		item := itemState.snapshot()
		if item == nil {
			return nil
		}
		if changed {
			publication.reportItem(frame, *item, itemState.output)
		}
		if item.Type == contracts.KimiConversationItemKindUserMessage {
			a.observeConversationPrompt(run, turn, publication, *item)
			if kimiConversationPromptRecordRole(run.agentID, *item) != "" {
				itemState.projectionPending = !a.persistConversationItems(run, turn, publication, frame, kimiConversationSelection{items: []string{itemID}})
			}
			return nil
		}
		if item.Type == contracts.KimiConversationItemKindToolCall || item.Status == contracts.KimiConversationStatusEnded {
			selection := kimiConversationSelection{items: []string{itemID}}
			if item.Type == contracts.KimiConversationItemKindToolCall && item.Stage != "input" {
				if _, opened := publication.requests[itemID]; !opened {
					for index, candidate := range turn.order {
						if candidate == itemID {
							selection.items, selection.flushText = turn.order[:index+1], true
							break
						}
					}
				}
			}
			itemState.projectionPending = !a.persistConversationItems(run, turn, publication, frame, selection)
		}
	case "turns.removed":
		a.withdrawAbsentConversationInvocations(run, output.document)
		if slices.Contains(frame.TurnIDs, output.activeTurnID) {
			a.finishConversationActivity(run, output.activeTurnID)
		}
	}
	return nil
}

// A duplicate native frame can still own a projection the output queue refused.
// The dispatcher retries that projection without applying the native mutation twice.
func conversationProjectionPending(document *kimiConversationState, frame kimiConversationFrame) bool {
	switch frame.Type {
	case contracts.KimiFrameTurnStarted, contracts.KimiFrameTurnEnded:
		if turn := document.turns[frame.Turn.TurnID]; turn != nil {
			return turn.projectionPending
		}
	case contracts.KimiFrameItemStarted, contracts.KimiFrameItemEnded:
		if turn := document.turns[frame.TurnID]; turn != nil {
			item := turn.items[frame.Item.ItemID]
			return item != nil && item.projectionPending
		}
	}
	return false
}

func (a *Agent) projectConversationTurn(run *kimiRun, turn *kimiConversationTurnState, frame kimiConversationFrame) {
	if turn == nil {
		return
	}
	turn.projectionPending = true
	output := run.conversation
	if run.agentID != kimiMainAgentID && output.turns[turn.header.TurnID] == nil && (!a.activateChildLifetime(run) || !a.reviveNativeChildRow(run)) {
		return
	}
	if turn.header.Status == contracts.KimiConversationStatusRunning && *turn.header.Ordinal >= output.latestOrdinal {
		output.latestOrdinal = *turn.header.Ordinal
		if output.activeTurnID != turn.header.TurnID {
			if output.activeTurnID != "" {
				a.finishConversationActivity(run, output.activeTurnID)
			}
			output.activeTurnID = turn.header.TurnID
			a.Mu.Lock()
			run.turnActive = true
			if run.agentID == kimiMainAgentID {
				a.turnActive, a.turnSteerable = true, false
			}
			a.Mu.Unlock()
			if run.agentID == kimiMainAgentID {
				a.PublishTurnActive()
			} else {
				a.publishChildInputState(run)
			}
		}
	}
	// Activity publication creates the new live scope. Capture only afterward.
	publication, err := a.conversationTurnOutput(run, turn)
	if err != nil {
		slog.Warn("capture the Kimi conversation turn", "error", err)
		return
	}
	a.observeConversationRetry(run, turn, publication, turn.header.Retry, frame.EventCreatedAt)
	a.publishConversationStepUsage(run, turn, publication, maps.Values(turn.steps))
	for index, step := range turn.steps {
		if step.Outcome == contracts.KimiConversationOutcomeCompleted {
			a.resolveConversationRetry(publication, index)
		}
	}
	for _, itemID := range turn.order {
		if item := turn.items[itemID].snapshot(); item != nil && item.Type == contracts.KimiConversationItemKindUserMessage {
			a.observeConversationPrompt(run, turn, publication, *item)
		}
	}
	ended := turn.header.Status == contracts.KimiConversationStatusEnded
	turn.projectionPending = !a.persistConversationItems(run, turn, publication, frame, kimiConversationSelection{items: turn.order, endTurn: ended, flushText: ended})
}

func (a *Agent) observeConversationPrompt(run *kimiRun, turn *kimiConversationTurnState, publication *kimiConversationTurnOutput, item kimiConversationItem) {
	for _, promptID := range item.PromptIDs {
		if submission := a.ownedPrompt(run, run.conversation.document.sessionID, promptID); submission != nil {
			submission.observed = true
			a.releaseChildPromptSubmission(submission)
		}
	}
	if item.Placement != "opener" {
		return
	}
	origin, err := decodeKimiConversationOrigin(item.Origin)
	if err != nil {
		return
	}
	if run.agentID != kimiMainAgentID {
		publication.userTurn = origin.Kind == contracts.KimiConversationOriginUser
		if run.conversation.activeTurnID == turn.header.TurnID || run.conversation.activeTurnID == "" && *turn.header.Ordinal >= run.conversation.latestOrdinal {
			a.Mu.Lock()
			run.userTurn = publication.userTurn
			a.Mu.Unlock()
		}
	}
	publication.steerable = origin.Kind == contracts.KimiConversationOriginUser || origin.Kind == contracts.KimiConversationOriginSkill || origin.Kind == contracts.KimiConversationOriginPluginCommand
	if run.agentID == kimiMainAgentID && run.conversation.activeTurnID == turn.header.TurnID {
		a.Mu.Lock()
		a.turnSteerable = publication.steerable
		a.Mu.Unlock()
		a.PublishTurnActive()
	}
}

func kimiConversationPromptRecordRole(agentID string, item kimiConversationItem) string {
	if agentID == kimiMainAgentID {
		if item.hasOriginNotice() {
			return contracts.KimiConversationRecordRoleOrigin
		}
		return ""
	}
	origin, err := decodeKimiConversationOrigin(item.Origin)
	if err == nil && origin.Kind != contracts.KimiConversationOriginUser {
		return contracts.KimiConversationRecordRoleItem
	}
	return ""
}

func kimiConversationCompletion(outcome string) agent.MessageCompletion {
	switch outcome {
	case contracts.KimiConversationOutcomeCompleted:
		return agent.MessageCompletionComplete
	case contracts.KimiConversationOutcomeInterrupted:
		return agent.MessageCompletionInterrupted
	default:
		return agent.MessageCompletionError
	}
}

// Request records contain only the tool declaration. Later output and approval
// results cannot enter the immutable request, including during snapshot recovery.
func kimiConversationRequestItem(item kimiConversationItem) kimiConversationItem {
	return kimiConversationItem{Type: item.Type, ItemID: item.ItemID, StepIndex: item.StepIndex,
		Status: item.Status, CreatedAt: item.CreatedAt, StartedAt: item.StartedAt,
		Name: item.Name, Input: item.Input, InputText: item.InputText}
}

type kimiPlanProposal struct {
	Kind string `json:"kind"`
	Plan string `json:"plan"`
}

// A native approval item can declare its plan before it has tool input.
// The plan text is fixed at review. The display's review state can change.
func (item kimiConversationItem) planProposal() *kimiPlanProposal {
	if item.Name == nil || *item.Name != contracts.KimiToolExitPlanMode || len(item.Display) == 0 {
		return nil
	}
	var display kimiPlanProposal
	if err := json.Unmarshal(item.Display, &display); err != nil || display.Kind != contracts.KimiDisplayPlanReview || strings.TrimSpace(display.Plan) == "" {
		return nil
	}
	return &display
}

type kimiConversationProjection struct {
	record        contracts.KimiConversationRecord
	span          agent.SpanInfo
	completion    agent.MessageCompletion
	source        leapmuxv1.MessageSource
	alreadyClosed bool
}

type kimiConversationSelection struct {
	items           []string
	endTurn         bool
	flushText       bool
	localCompletion agent.MessageCompletion
}

func (a *Agent) flushConversationControlText(run *kimiRun) bool {
	output := run.conversation
	if output == nil || output.activeTurnID == "" {
		return false
	}
	turn := output.document.turns[output.activeTurnID]
	publication := output.turns[output.activeTurnID]
	if turn == nil || publication == nil || publication.completed || publication.historical {
		return false
	}
	var items []string
	for _, id := range turn.order {
		state := turn.items[id]
		if state == nil || state.data == nil || state.data.Status != contracts.KimiConversationStatusRunning || state.text == nil || state.text.Len() == 0 {
			continue
		}
		switch state.identity.kind {
		case contracts.KimiConversationItemKindAssistantMessage, contracts.KimiConversationItemKindReasoning:
			items = append(items, id)
		}
	}
	if len(items) == 0 {
		return false
	}
	frame := kimiConversationFrame{SessionID: output.document.sessionID, AgentID: run.agentID, TurnID: turn.header.TurnID}
	return a.persistConversationItems(run, turn, publication, frame, kimiConversationSelection{items: items, flushText: true})
}

func (a *Agent) persistConversationItems(run *kimiRun, turn *kimiConversationTurnState, publication *kimiConversationTurnOutput, frame kimiConversationFrame, selection kimiConversationSelection) bool {
	var projections []kimiConversationProjection
	var released []*kimiConversationItemState
	var requested, completed []string
	var invocations []kimiConversationItem
	textRecords := make(map[int64][]string)
	turnContext, err := json.Marshal(contracts.KimiConversationTurnContext{TurnID: turn.header.TurnID, Kind: turn.header.Kind, Status: turn.header.Status, Outcome: turn.header.Outcome, EndReason: turn.header.EndReason})
	if err != nil {
		slog.Warn("encode the Kimi item turn context", "error", err)
		return false
	}
	// A full snapshot already contains the step and its native tool declarations.
	// Find those declarations before calculating any text completion.
	for _, itemID := range selection.items {
		state := turn.items[itemID]
		if state == nil || state.data == nil || state.identity.stepIndex == nil {
			continue
		}
		if state.identity.kind == contracts.KimiConversationItemKindToolCall && len(state.data.Input) != 0 && state.data.Stage != "input" {
			publication.executionSteps[*state.identity.stepIndex] = struct{}{}
		}
	}
	for _, itemID := range selection.items {
		state := turn.items[itemID]
		if state == nil {
			continue
		}
		item := state.snapshot()
		if item == nil {
			continue
		}
		record := contracts.KimiConversationRecord{Type: contracts.KimiConversationRecordKindItem,
			SessionID: frame.SessionID, AgentID: run.agentID, TurnID: turn.header.TurnID,
			Role: contracts.KimiConversationRecordRoleItem, EventCreatedAt: frame.EventCreatedAt}
		span := agent.SpanInfo{}
		source := leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT
		if item.Type == contracts.KimiConversationItemKindUserMessage {
			record.Role = kimiConversationPromptRecordRole(run.agentID, *item)
			if record.Role == "" {
				continue
			}
			if record.Role == contracts.KimiConversationRecordRoleItem {
				source = leapmuxv1.MessageSource_MESSAGE_SOURCE_USER
				span.MarkType = leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE
			}
		}
		completion := agent.MessageCompletionComplete
		if item.Status == contracts.KimiConversationStatusEnded {
			completion = kimiConversationCompletion(item.Outcome)
		} else if selection.localCompletion != "" {
			completion = selection.localCompletion
		} else if selection.endTurn {
			completion = kimiConversationCompletion(turn.header.Outcome)
		}
		if (item.Type == contracts.KimiConversationItemKindAssistantMessage || item.Type == contracts.KimiConversationItemKindReasoning) && state.identity.stepIndex != nil {
			index := *state.identity.stepIndex
			if _, generated := publication.executionSteps[index]; generated {
				if selection.localCompletion != "" {
					completion = agent.MessageCompletionComplete
				}
			} else {
				if step, exists := turn.steps[index]; exists {
					completion = kimiConversationCompletion(step.Outcome)
				}
			}
			textRecords[index] = append(textRecords[index], itemID)
		}
		alreadyClosed := false
		if item.Type == contracts.KimiConversationItemKindToolCall {
			_, alreadyClosed = publication.results[itemID]
			if alreadyClosed && item.Status != contracts.KimiConversationStatusEnded {
				continue
			}
			if item.Name == nil || strings.TrimSpace(*item.Name) == "" || item.Stage == "input" && item.Status != contracts.KimiConversationStatusEnded {
				continue
			}
			plan := item.planProposal()
			if *item.Name == contracts.KimiToolExitPlanMode && plan == nil && item.Status == contracts.KimiConversationStatusRunning && !selection.endTurn && selection.localCompletion == "" {
				// The native plan display can arrive after the tool starts.
				continue
			}
			// Approval stubs carry a name before native execution supplies input.
			// A complete plan display also declares an exact review request.
			hasDeclaration := len(item.Input) != 0 || plan != nil
			if item.Status == contracts.KimiConversationStatusRunning && !hasDeclaration {
				continue
			}
			record.Role = contracts.KimiConversationRecordRoleRequest
			request := kimiConversationRequestItem(*item)
			if plan != nil {
				request.Display, err = json.Marshal(plan)
				if err != nil {
					slog.Warn("encode the Kimi plan proposal", "error", err)
					return false
				}
			}
			requestRaw, err := json.Marshal(request)
			if err != nil {
				slog.Warn("encode the Kimi tool declaration", "error", err)
				return false
			}
			record.Item = requestRaw
			view := kimiConversationRecordView{record: record, item: &request}
			span = agent.SpanInfo{SpanID: view.key(), SpanType: *item.Name, NoSpan: kimiToolSpawns(*item.Name)}
			if selection.localCompletion == "" && len(item.Input) != 0 && (kimiToolSpawns(*item.Name) || *item.Name == contracts.KimiToolBash) {
				invocations = append(invocations, *item)
			}
			if _, opened := publication.requests[itemID]; !opened && hasDeclaration {
				projections = append(projections, kimiConversationProjection{record: record, span: span, completion: agent.MessageCompletionComplete, source: source})
				requested = append(requested, itemID)
			}
			interruptedTurnEnd := selection.endTurn && turn.header.Outcome == contracts.KimiConversationOutcomeInterrupted
			if item.Status != contracts.KimiConversationStatusEnded && !interruptedTurnEnd && selection.localCompletion == "" {
				continue
			}
			record.Role = contracts.KimiConversationRecordRoleResult
			record.OutputTruncated = state.output.truncated()
			span.Closing = true
			completed = append(completed, itemID)
		} else if item.Status != contracts.KimiConversationStatusEnded && (!selection.flushText ||
			item.Type != contracts.KimiConversationItemKindAssistantMessage && item.Type != contracts.KimiConversationItemKindReasoning) {
			continue
		}
		itemRaw, err := json.Marshal(item)
		if err != nil {
			slog.Warn("encode the Kimi conversation item", "error", err)
			return false
		}
		record.Item = itemRaw
		record.TurnContext = turnContext
		projections = append(projections, kimiConversationProjection{record: record, span: span, completion: completion, source: source, alreadyClosed: alreadyClosed})
		if item.Status == contracts.KimiConversationStatusEnded || selection.localCompletion != "" {
			released = append(released, state)
		}
	}
	if selection.endTurn {
		header := turn.headerSnapshot()
		raw, err := json.Marshal(header)
		if err != nil {
			slog.Warn("encode the Kimi turn header", "error", err)
			return false
		}
		projections = append(projections, kimiConversationProjection{record: contracts.KimiConversationRecord{
			Type: contracts.KimiConversationRecordKindTurn, SessionID: frame.SessionID, AgentID: run.agentID,
			TurnID: header.TurnID, Turn: raw, EventCreatedAt: frame.EventCreatedAt}, completion: kimiConversationCompletion(header.Outcome)})
	}
	if len(projections) == 0 {
		a.observeConversationInvocations(run, turn, publication, frame, invocations)
		return true
	}
	// Validate the whole group before reserving or capturing any of its rows.
	contents := make([]agent.MessageContent, len(projections))
	var turnEndPayload []byte
	for index, projection := range projections {
		raw, err := encodeKimiConversationRecord(projection.record)
		if err != nil {
			slog.Warn("prepare the Kimi conversation record", "error", err)
			return false
		}
		view, err := decodeKimiConversationRecord(raw)
		if err != nil {
			slog.Warn("validate the Kimi conversation record", "error", err)
			return false
		}
		contents[index] = agent.MessageContent{Original: raw, AgentSessionID: frame.SessionID, IdempotencyKey: view.key(), Completion: projection.completion}
		if projection.record.Type == contracts.KimiConversationRecordKindTurn {
			turnEndPayload = raw
			toolUses := maps.Clone(publication.results)
			for _, id := range completed {
				toolUses[id] = struct{}{}
			}
			contents[index] = agent.WithToolUseCount(contents[index], len(toolUses))
			a.Mu.Lock()
			usage := maps.Clone(a.contextUsage)
			a.Mu.Unlock()
			contents[index] = kimiWithContextUsage(contents[index], usage)
		}
	}
	if selection.endTurn && !publication.completed && !publication.historical && turn.header.Outcome == contracts.KimiConversationOutcomeInterrupted {
		if !a.closeOpenConversationToolsAcrossTurnsExcluding(run, turn.header.TurnID, agent.MessageCompletionInterrupted, false) {
			return false
		}
	}
	batch, err := agent.ReserveCapturedTranscriptBatch(publication.writer)
	if err != nil {
		slog.Warn("reserve the Kimi conversation records", "error", err)
		return false
	}
	operations := make([]agent.CapturedTranscriptOperation, 0, len(projections))
	var mediaReads []kimiMediaSupplementRead
	type kimiScopeSpanChange struct {
		spanID        string
		spanType      string
		prevSpanType  string
		closing       bool
		noSpan        bool
		alreadyClosed bool
	}
	var spanChanges []kimiScopeSpanChange
	rollbackScope := func() {
		for i := len(spanChanges) - 1; i >= 0; i-- {
			change := spanChanges[i]
			if change.noSpan || change.alreadyClosed {
				continue
			}
			if change.closing {
				publication.scope.OpenSpan(change.spanID, "")
				if change.prevSpanType != "" {
					publication.scope.SetSpanType(change.spanID, change.prevSpanType)
				}
			} else {
				publication.scope.CloseSpan(change.spanID)
			}
		}
	}
	for index, projection := range projections {
		if a.api != nil && selection.localCompletion == "" {
			plan, mediaErr := prepareKimiConversationMedia(projection.record, len(contents[index].Original))
			if mediaErr != nil {
				slog.Warn("prepare the Kimi conversation image", "error", mediaErr)
			}
			if plan != nil {
				api := a.api
				mediaReads = append(mediaReads, kimiMediaSupplementRead{operationIndex: index, itemID: plan.receipt.ItemID,
					read: func(ctx context.Context, permits chan struct{}) ([]byte, error) { return plan.read(ctx, api, permits) }})
			}
		}
		span := projection.span
		if span.SpanID != "" && !span.NoSpan {
			span.SpanColor = publication.scope.ReserveSpanColor(span.SpanID, "")
		}
		capture, captureErr := publication.scope.CaptureTranscript(contents[index], span)
		if captureErr != nil {
			rollbackScope()
			batch.Discard()
			slog.Warn("capture the Kimi conversation record", "error", captureErr)
			return false
		}
		if projection.record.Type == contracts.KimiConversationRecordKindTurn {
			operations = append(operations, capture.TurnEndOperation())
		} else {
			operations = append(operations, capture.MessageOperation(projection.source))
		}
		if span.SpanID != "" {
			change := kimiScopeSpanChange{
				spanID:        span.SpanID,
				spanType:      span.SpanType,
				closing:       span.Closing,
				noSpan:        span.NoSpan,
				alreadyClosed: projection.alreadyClosed,
			}
			if !span.NoSpan {
				if span.Closing {
					if !projection.alreadyClosed {
						change.prevSpanType = publication.scope.GetSpanType(span.SpanID)
						publication.scope.CloseSpan(span.SpanID)
					}
				} else {
					publication.scope.OpenSpan(span.SpanID, "")
					publication.scope.SetSpanType(span.SpanID, span.SpanType)
				}
			}
			spanChanges = append(spanChanges, change)
		}
	}
	if !a.retainMediaBatch(run, publication.writer, frame.SessionID, run.agentID, batch, operations, mediaReads) {
		err = batch.Prepare(func() []agent.CapturedTranscriptOperation { return operations })
		if !agent.OutputRetained(err) {
			rollbackScope()
			slog.Warn("retain the Kimi conversation records", "error", err)
			return false
		}
	}
	if !publication.historical {
		for _, change := range spanChanges {
			if change.closing {
				if change.alreadyClosed {
					continue
				}
				if !change.noSpan {
					run.conversation.removeOpenLiveSpan(change.spanID)
					if run.agentID == kimiMainAgentID && publication.sessionCurrent() {
						publication.writer.CloseSpan(change.spanID)
					}
				}
				if publication.progress != nil {
					publication.reportProgressUpdate(agent.CompleteOutputProgress(change.spanID))
					publication.reportProgressUpdate(agent.CompleteModelProgress(change.spanID))
				}
			} else if !change.noSpan {
				run.conversation.recordOpenLiveSpan(change.spanID, "", change.spanType)
				if run.agentID == kimiMainAgentID && publication.sessionCurrent() {
					publication.writer.OpenSpan(change.spanID, "")
					publication.writer.SetSpanType(change.spanID, change.spanType)
				}
			}
		}
	}
	for _, id := range requested {
		publication.requests[id] = struct{}{}
	}
	for _, id := range completed {
		publication.results[id] = struct{}{}
	}
	a.observeConversationInvocations(run, turn, publication, frame, invocations)
	for index, ids := range textRecords {
		if publication.textRecords[index] == nil {
			publication.textRecords[index] = make(map[string]struct{})
		}
		for _, id := range ids {
			publication.textRecords[index][id] = struct{}{}
		}
	}
	if selection.endTurn {
		for id, state := range turn.items {
			if state.identity.kind == contracts.KimiConversationItemKindToolCall {
				_, opened := publication.requests[id]
				_, closed := publication.results[id]
				if opened && !closed {
					continue
				}
			}
			state.releaseBody()
		}
	} else {
		for _, state := range released {
			// Text without a native step can precede a hook's final turn outcome.
			// A patched item also needs its base until a full item or turn end arrives.
			if selection.localCompletion != "" || state.fullDigest != nil && (turn.header.Status == contracts.KimiConversationStatusEnded ||
				state.identity.stepIndex != nil || state.identity.kind != contracts.KimiConversationItemKindAssistantMessage &&
				state.identity.kind != contracts.KimiConversationItemKindReasoning) {
				state.releaseBody()
			}
		}
	}
	if selection.endTurn && !publication.completed {
		for _, index := range slices.Sorted(maps.Keys(turn.steps)) {
			a.correctConversationStepCompletion(publication, frame, turn.header.TurnID, turn.steps[index])
		}
		publication.completed = true
		if !publication.historical && turn.header.Outcome == contracts.KimiConversationOutcomeInterrupted {
			a.closeOpenConversationToolsAcrossTurns(run, agent.MessageCompletionInterrupted, true)
		}
		a.finishChildInvocationTurn(run, turn.header.TurnID)
		if run.agentID == kimiMainAgentID {
			if run.conversation.activeTurnID == turn.header.TurnID && !publication.historical && turn.header.Kind == contracts.KimiConversationTurnKindAgent {
				a.settleConversationServerErrorRetry(publication.writer.CaptureAutoRetry(), turn.header, turnEndPayload)
			}
			a.finishConversationActivity(run, turn.header.TurnID)
		} else {
			a.finishConversationChildTurn(run, turn, publication)
		}
	}
	for _, id := range selection.items {
		if item := turn.items[id]; item != nil {
			item.projectionPending = false
		}
	}
	return true
}

func (a *Agent) finishConversationActivity(run *kimiRun, turnID string) {
	if run.conversation.activeTurnID != turnID {
		return
	}
	run.conversation.activeTurnID = ""
	a.Mu.Lock()
	run.turnActive = false
	if run.agentID == kimiMainAgentID {
		a.turnActive, a.turnSteerable = false, false
	}
	a.Mu.Unlock()
	if run.agentID == kimiMainAgentID {
		a.PublishTurnActive()
		a.requestUsageRefresh()
	} else {
		a.publishChildInputState(run)
	}
}

// closeOpenConversationToolsAcrossTurns scans all turns in run.conversation
// (regardless of turn status) for any opened tool call that has not closed yet,
// closes each with completion, completes its progress counters, and resets
// affected turn scopes plus root/child spans and progress.
func (a *Agent) closeOpenConversationToolsAcrossTurns(run *kimiRun, completion agent.MessageCompletion, resetProgress bool) bool {
	return a.closeOpenConversationToolsAcrossTurnsExcluding(run, "", completion, resetProgress)
}

func (a *Agent) closeOpenConversationToolsAcrossTurnsExcluding(run *kimiRun, excludeTurnID string, completion agent.MessageCompletion, resetProgress bool) bool {
	if run == nil || run.conversation == nil {
		return true
	}
	output := run.conversation
	discarding := a.IsDiscardingOutput()
	turns := slices.Collect(maps.Values(output.document.turns))
	slices.SortFunc(turns, func(left, right *kimiConversationTurnState) int {
		return cmp.Compare(*left.header.Ordinal, *right.header.Ordinal)
	})
	allClosed := true
	var closedSpanIDs []string
	for _, turn := range turns {
		if excludeTurnID != "" && turn.header.TurnID == excludeTurnID {
			continue
		}
		publication := output.turns[turn.header.TurnID]
		if publication == nil {
			continue
		}
		frame := kimiConversationFrame{SessionID: output.document.sessionID, AgentID: run.agentID, TurnID: turn.header.TurnID}
		var openIDs []string
		for _, itemID := range turn.order {
			item := turn.items[itemID]
			if item == nil || item.identity.kind != contracts.KimiConversationItemKindToolCall {
				continue
			}
			_, opened := publication.requests[itemID]
			_, closed := publication.results[itemID]
			if !opened || closed {
				continue
			}
			openIDs = append(openIDs, itemID)
		}
		if len(openIDs) > 0 {
			if !discarding {
				if !a.persistConversationItems(run, turn, publication, frame, kimiConversationSelection{items: openIDs, localCompletion: completion}) {
					allClosed = false
					continue
				}
			}
			for _, itemID := range openIDs {
				publication.results[itemID] = struct{}{}
				scopeID := kimiConversationProgressID(frame, itemID, contracts.KimiConversationItemKindToolCall)
				closedSpanIDs = append(closedSpanIDs, scopeID)
				if discarding && publication.progress != nil {
					publication.reportProgressUpdate(agent.CompleteOutputProgress(scopeID))
					publication.reportProgressUpdate(agent.CompleteModelProgress(scopeID))
				}
			}
		}
		publication.scope.ResetSpans()
	}
	if excludeTurnID != "" {
		if excluded := output.turns[excludeTurnID]; excluded != nil {
			for _, spanID := range closedSpanIDs {
				excluded.scope.CloseSpan(spanID)
			}
		}
		return allClosed
	}
	if !allClosed {
		return false
	}
	output.openLiveSpans = nil
	if resetProgress {
		if run.agentID == kimiMainAgentID {
			if a.sink != nil {
				providerkit.ResetSpansAndProgress(a.sink)
			}
		} else if sink := a.runSink(run); sink != nil {
			providerkit.ResetSpansAndProgress(sink)
		}
	}
	return true
}
