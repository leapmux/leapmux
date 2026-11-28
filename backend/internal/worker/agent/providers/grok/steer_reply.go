package grok

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

type grokSteerReplies struct {
	mu     sync.Mutex
	jobs   sync.WaitGroup
	closed bool

	home, workingDir string
	active           *grokSteerReply
	handoff          *grokSteerHandoff
	children         map[string]*grokChildReplyState
	live             grokLiveModel
}

type grokLiveModel struct {
	promptID string
	start    int64
	eventSeq uint64
}

// Grok acknowledges enqueue on RPC and reports consumption in a separate
// journal. Retain the streams and their transcript keys until the journal
// can prove which interjections belong to each stream.
type grokSteerReply struct {
	sessionID, promptID, turnID, group string
	predecessorID                      string
	agentInitiated                     bool
	childMessages                      bool
	baseline                           sessionstore.JournalCursor
	inputs                             []grokSteerAdmission
	streams                            []grokSteerStream
	cancelControlProbe                 context.CancelFunc
	controlProbeStart                  int64
	invalid                            bool
}

func (a *Agent) prepareGrokJournal(sessionID string) (string, sessionstore.JournalCursor) {
	if sessionID == "" {
		return "", sessionstore.JournalCursor{}
	}
	replies := &a.steerReplies
	replies.mu.Lock()
	skip := replies.closed || (replies.active != nil && replies.active.sessionID == sessionID)
	replies.mu.Unlock()
	if skip {
		return "", sessionstore.JournalCursor{}
	}
	return replies.findJournal(sessionID)
}

func (a *Agent) admitGrokSteerWithJournal(receipt agent.NativeInputReceipt, payload grokInterjectionPayload, group string, baseline sessionstore.JournalCursor) *grokSteerReply {
	promptID, turnID, agentInitiated := a.grokSteerScope(receipt.SessionID)
	if turnID == "" || promptID == "" {
		return nil
	}

	replies := &a.steerReplies
	replies.mu.Lock()
	if replies.closed {
		replies.mu.Unlock()
		return nil
	}
	if current := replies.active; current != nil {
		defer replies.mu.Unlock()
		if current.turnID != turnID || current.sessionID != receipt.SessionID || current.promptID != promptID {
			return nil
		}
		current.inputs = append(current.inputs, grokSteerAdmission{id: receipt.InputID, payload: payload, afterStream: replies.live.boundaryOf(promptID)})
		return current
	}
	if group != "" {
		current := &grokSteerReply{
			sessionID: receipt.SessionID, turnID: turnID, promptID: promptID, group: group,
			agentInitiated: agentInitiated, baseline: baseline,
			inputs: []grokSteerAdmission{{id: receipt.InputID, payload: payload, afterStream: replies.live.boundaryOf(promptID)}},
		}
		replies.active = current
		replies.mu.Unlock()
		return current
	}
	replies.mu.Unlock()

	group, baseline = replies.findJournal(receipt.SessionID)
	currentPrompt, currentTurn, currentAgent := a.grokSteerScope(receipt.SessionID)
	if group == "" || currentPrompt != promptID || currentTurn != turnID {
		return nil
	}
	replies.mu.Lock()
	defer replies.mu.Unlock()
	if replies.closed {
		return nil
	}
	if current := replies.active; current != nil {
		if current.turnID != turnID || current.sessionID != receipt.SessionID || current.promptID != promptID {
			return nil
		}
		current.inputs = append(current.inputs, grokSteerAdmission{id: receipt.InputID, payload: payload, afterStream: replies.live.boundaryOf(promptID)})
		return current
	}
	current := &grokSteerReply{
		sessionID: receipt.SessionID, turnID: turnID, promptID: promptID, group: group,
		agentInitiated: currentAgent, baseline: baseline,
		inputs: []grokSteerAdmission{{id: receipt.InputID, payload: payload, afterStream: replies.live.boundaryOf(promptID)}},
	}
	replies.active = current
	return current
}

func (replies *grokSteerReplies) findJournal(sessionID string) (group string, baseline sessionstore.JournalCursor) {
	for _, dir := range grokSessionGroups(filepath.Join(replies.home, "sessions"), replies.workingDir) {
		b, err := readGrokJournalBaseline(replies.home, filepath.Base(dir), sessionID)
		if err == nil {
			if group != "" {
				return "", sessionstore.JournalCursor{}
			}
			group, baseline = filepath.Base(dir), b
		}
	}
	return group, baseline
}

func (a *Agent) grokActivePrompt(sessionID string) (promptID string, agentInitiated bool) {
	if sessionID == "" || !a.IsCurrentSession(sessionID) {
		return "", false
	}
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	promptID = a.turns.running
	return promptID, promptID != "" && promptID == a.turns.agentTurn
}

func (a *Agent) grokSteerScope(sessionID string) (promptID, turnID string, agentInitiated bool) {
	promptID, agentInitiated = a.grokActivePrompt(sessionID)
	if agentInitiated {
		return promptID, promptID, true
	}
	return promptID, a.RootReplyTurnID(sessionID), false
}

// Grok's prompt ID owns the controls of agent-initiated and child turns. The
// journal separately proves which inputs those controls block.
func (a *Agent) grokSteerControlOwner(sessionID string, _ *providerkit.ParsedLine) (string, []string) {
	if promptID, agentInitiated := a.grokActivePrompt(sessionID); agentInitiated {
		return promptID, nil
	}
	replies := &a.steerReplies
	replies.mu.Lock()
	defer replies.mu.Unlock()
	if child := replies.activeChildTurn(sessionID); child != nil {
		return child.active.turnID, nil
	}
	return "", nil
}

func (model grokLiveModel) boundaryOf(promptID string) int64 {
	if model.promptID == promptID {
		return model.start
	}
	return 0
}

func (a *Agent) forgetGrokSteer(nativeID string) {
	replies := &a.steerReplies
	replies.mu.Lock()
	defer replies.mu.Unlock()
	if tracked := replies.active; tracked != nil {
		tracked.inputs = slices.DeleteFunc(tracked.inputs, func(input grokSteerAdmission) bool { return input.id == nativeID })
		if len(tracked.inputs) == 0 {
			replies.active = nil
			if tracked.cancelControlProbe != nil {
				tracked.cancelControlProbe()
			}
		}
	}
	if replies.handoff != nil && replies.handoff.input.id == nativeID {
		replies.takeHandoff()
	}
}

func (a *Agent) observeGrokInterjection(sessionID string, params json.RawMessage) {
	nativeID := providerkit.StringField(params, "interjectionId")
	if nativeID == "" || !a.IsCurrentSession(sessionID) {
		return
	}
	replies := &a.steerReplies
	replies.mu.Lock()
	if handoff := replies.handoff; handoff != nil && handoff.sessionID == sessionID {
		if handoff.input.id == nativeID {
			handoff.input.echoed = true
			replies.mu.Unlock()
			return
		}
		unproved := replies.takeHandoff()
		replies.mu.Unlock()
		a.retireGrokSteer(unproved.receipt())
		replies.mu.Lock()
	}
	defer replies.mu.Unlock()
	current := replies.active
	if current == nil || current.sessionID != sessionID {
		return
	}
	for index, input := range current.inputs {
		if input.id == nativeID {
			if !input.echoed {
				current.inputs[index].echoed = true
				current.inputs[index].afterStream = max(input.afterStream, replies.live.boundaryOf(current.promptID))
			}
			return
		}
	}
	// An interjection outside the recorded batch can contribute a journal row.
	current.invalid = true
}

func readGrokReplyModel(sessionID, updateType string, metadata map[string]json.RawMessage) (grokLiveModel, bool) {
	switch updateType {
	case contracts.ACPUpdateAgentMessageChunk, contracts.ACPUpdateAgentThoughtChunk, contracts.ACPUpdateToolCall:
	default:
		return grokLiveModel{}, false
	}
	promptID, _ := providerkit.RawString(metadata[grokPromptIDKey])
	eventID, _ := providerkit.RawString(metadata["eventId"])
	start, ok := providerkit.RawInt64(metadata[grokStreamStartMeta])
	seq, seqOK := grokEventSequence(sessionID, eventID)
	if !ok || start <= 0 || !seqOK || promptID == "" {
		return grokLiveModel{}, false
	}
	return grokLiveModel{promptID: promptID, start: start, eventSeq: seq}, true
}

func (a *Agent) grokUpdateMessageID(info acp.UpdateMessageInfo) string {
	if info.RowKey != "" {
		if info.SessionID == "" {
			return ""
		}
		return a.observeGrokChildUpdate(info.SessionID, info.RowKey, info.UpdateType, info.NotificationMeta)
	}
	sessionID := info.SessionID
	if sessionID == "" {
		sessionID = a.CurrentSessionID()
	}
	model, ok := readGrokReplyModel(sessionID, info.UpdateType, info.NotificationMeta)
	replies := &a.steerReplies
	replies.mu.Lock()
	defer replies.mu.Unlock()
	if !ok {
		if replies.active != nil && info.UpdateType == contracts.ACPUpdateAgentMessageChunk {
			replies.active.invalid = true
		}
		return ""
	}
	replies.live = model
	current := replies.active
	if current == nil || current.sessionID != sessionID || current.promptID != model.promptID {
		return ""
	}
	current.recordModel(model)
	if info.UpdateType == contracts.ACPUpdateAgentMessageChunk {
		return grokStreamMessageID(sessionID, model.promptID, model.start)
	}
	return ""
}

func (current *grokSteerReply) recordModel(model grokLiveModel) {
	if n := len(current.streams); n == 0 || current.streams[n-1].start != model.start {
		current.streams = append(current.streams, grokSteerStream{start: model.start, eventSeq: model.eventSeq})
	}
}

const grokStreamMessagePrefix = "grok-stream:"

func grokStreamMessageID(sessionID, promptID string, start int64) string {
	return grokStreamMessagePrefix + sessionID + ":" + promptID + ":" + strconv.FormatInt(start, 10)
}

func (a *Agent) grokAssistantTextKey(messageID string) string {
	message, ok := strings.CutPrefix(messageID, grokStreamMessagePrefix)
	sessionID, _, found := strings.Cut(message, ":")
	if !ok || !found {
		return ""
	}
	replies := &a.steerReplies
	replies.mu.Lock()
	defer replies.mu.Unlock()
	current := replies.active
	if child := replies.children[sessionID]; child != nil {
		current = child.active
	}
	if current == nil || current.sessionID != sessionID {
		return ""
	}
	for index := len(current.streams) - 1; index >= 0; index-- {
		stream := &current.streams[index]
		if grokStreamMessageID(sessionID, current.promptID, stream.start) == messageID {
			key := "grok-reply:" + id.Generate()
			stream.keys = append(stream.keys, key)
			return key
		}
	}
	return ""
}

// Prove the cohort that preceded the stream asking for approval. A late
// journal append can bind the inputs while the current request is still open.
func (a *Agent) observeGrokSteerControl(sessionID string) {
	promptID, turnID, _ := a.grokSteerScope(sessionID)
	sink := a.Sink()
	replies := &a.steerReplies
	replies.mu.Lock()
	current := replies.active
	if child := replies.activeChildTurn(sessionID); child != nil {
		current, sink = child.active, child.sink
		promptID, turnID = child.live.promptID, child.active.turnID
	}
	if replies.closed || current == nil || current.invalid || current.sessionID != sessionID ||
		current.promptID != promptID || current.turnID != turnID || len(current.streams) == 0 {
		replies.mu.Unlock()
		return
	}
	latest := current.streams[len(current.streams)-1].start
	if latest == current.controlProbeStart {
		replies.mu.Unlock()
		return
	}
	if current.cancelControlProbe != nil {
		current.cancelControlProbe()
	}
	ctx, cancel := context.WithCancel(a.Context())
	current.cancelControlProbe, current.controlProbeStart = cancel, latest
	proof := current.snapshot()
	replies.jobs.Add(1)
	replies.mu.Unlock()
	go func() {
		defer replies.jobs.Done()
		defer cancel()
		matched := a.awaitGrokSteerProof(ctx, proof)
		for _, input := range proof.inputs {
			if _, ok := matched[input.id]; !ok || ctx.Err() != nil {
				continue
			}
			receipt := agent.NativeInputReceipt{SessionID: proof.sessionID, InputID: input.id}
			if err := sink.BindNativeInputTurn(receipt, proof.turnID); !agent.OutputRetained(err) {
				slog.Warn("bind the Grok steer to its blocking turn", "agent_id", a.AgentID(), "error", err)
			}
		}
	}()
}

func (current *grokSteerReply) snapshot() grokSteerReply {
	snapshot := *current
	snapshot.cancelControlProbe = nil
	snapshot.inputs = slices.Clone(current.inputs)
	snapshot.streams = slices.Clone(current.streams)
	for index := range snapshot.streams {
		snapshot.streams[index].keys = slices.Clone(snapshot.streams[index].keys)
	}
	return snapshot
}

// captureGrokSteerReconciliation captures the active LeapMux prompt's steer
// batch after captureRootSnapshotLocked has allocated the final buffered
// assistant text key, retiring the batch if the prompt ends without a
// turn_completed notification.
func (a *Agent) captureGrokSteerReconciliation() func(agent.MessageCompletion, bool) error {
	replies := &a.steerReplies
	replies.mu.Lock()
	current := replies.active
	if replies.closed || current == nil || current.agentInitiated {
		replies.mu.Unlock()
		return nil
	}
	replies.mu.Unlock()
	return func(_ agent.MessageCompletion, _ bool) error {
		replies.mu.Lock()
		if replies.closed || replies.active != current {
			replies.mu.Unlock()
			return nil
		}
		a.detachAndPublishGrokSteerLocked(current, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED)
		return nil
	}
}

func (a *Agent) finishLingeringPromptSteer() {
	a.stateMu.Lock()
	running := a.turns.running
	a.stateMu.Unlock()
	replies := &a.steerReplies
	replies.mu.Lock()
	current := replies.active
	if replies.closed || current == nil || current.agentInitiated || current.promptID == running {
		replies.mu.Unlock()
		return
	}
	sessionID, promptID := current.sessionID, current.promptID
	replies.mu.Unlock()
	a.finishGrokSteerReply(sessionID, promptID, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED)
}

// detachAndPublishGrokSteerLocked detaches current, unlocks replies.mu, and
// starts asynchronous proof publication if any inputs remain.
func (a *Agent) detachAndPublishGrokSteerLocked(current *grokSteerReply, outcome leapmuxv1.AgentInputTurnOutcome) {
	replies := &a.steerReplies
	replies.active = nil
	if current.cancelControlProbe != nil {
		current.cancelControlProbe()
	}
	proof := current.snapshot()
	if outcome != 0 && replies.handoff == nil && !proof.invalid {
		a.retainGrokSteerHandoffLocked(&proof)
	}
	if len(proof.inputs) == 0 {
		replies.mu.Unlock()
		return
	}
	replies.jobs.Add(1)
	replies.mu.Unlock()
	go func() {
		defer replies.jobs.Done()
		a.publishGrokSteerReplies(proof, outcome)
	}()
}

func (a *Agent) finishGrokSteerReply(sessionID, promptID string, outcome leapmuxv1.AgentInputTurnOutcome) {
	replies := &a.steerReplies
	replies.mu.Lock()
	current := replies.active
	if replies.closed || current == nil || current.sessionID != sessionID || current.promptID != promptID {
		replies.mu.Unlock()
		return
	}
	replies.mu.Unlock()

	a.FlushBufferedOutput(agent.MessageCompletionComplete)
	replies.mu.Lock()
	if replies.closed || replies.active != current {
		replies.mu.Unlock()
		return
	}
	a.detachAndPublishGrokSteerLocked(current, outcome)
}

func (a *Agent) awaitGrokSteerProof(ctx context.Context, proof grokSteerReply) map[string]int {
	if proof.invalid || len(proof.streams) == 0 {
		return nil
	}
	poller := newGrokJournalPoller(a.steerReplies.home, proof.group, proof.baseline, grokJournalScope{
		sessionID: proof.sessionID, predecessorID: proof.predecessorID, promptID: proof.promptID, childMessages: proof.childMessages,
	})
	deadline := a.Clock().NewTimer(a.APITimeout(), "grok-interjection-proof")
	defer deadline.Stop()
	for {
		rows, changed, err := poller.poll()
		if errors.Is(err, errGrokJournalInvalid) {
			return nil
		}
		if err == nil && changed {
			matched, matchErr := matchGrokConsumptions(proof.inputs, proof.streams, rows)
			if matchErr == nil {
				return matched
			}
			if !errors.Is(matchErr, errGrokConsumptionIncomplete) {
				return nil
			}
		}
		if ctx.Err() != nil {
			return nil
		}
		next := a.Clock().NewTimer(grokJournalReadInterval, "grok-interjection-journal")
		select {
		case <-deadline.C:
			next.Stop()
			return nil
		case <-ctx.Done():
			next.Stop()
			return nil
		case <-next.C:
		}
	}
}

func (a *Agent) publishGrokSteerReplies(proof grokSteerReply, outcome leapmuxv1.AgentInputTurnOutcome) {
	a.publishGrokSteerRepliesTo(a.Sink(), proof, outcome)
}

func (a *Agent) publishGrokSteerRepliesTo(sink agent.ProviderServices, proof grokSteerReply, outcome leapmuxv1.AgentInputTurnOutcome) {
	var matched map[string]int
	if outcome != 0 {
		matched = a.awaitGrokSteerProof(a.Context(), proof)
	}
	if len(matched) > 0 {
		turn := grokCanonicalSteerReply(proof, matched, outcome)
		if err := sink.PublishNativeReplyTurn(turn); !agent.OutputRetained(err) {
			slog.Warn("store the Grok steer reply", "agent_id", a.AgentID(), "error", err)
			matched = nil
		}
	}
	for _, input := range proof.inputs {
		if _, ok := matched[input.id]; !ok {
			a.retireGrokSteerFrom(sink, agent.NativeInputReceipt{SessionID: proof.sessionID, InputID: input.id})
		}
	}
}

func grokCanonicalSteerReply(proof grokSteerReply, matched map[string]int, outcome leapmuxv1.AgentInputTurnOutcome) agent.NativeReplyTurn {
	turn := agent.NativeReplyTurn{SessionID: proof.sessionID, TurnID: proof.turnID, Outcome: outcome}
	positions := make([]int64, len(proof.streams))
	ordinal := int64(1)
	for index, stream := range proof.streams {
		positions[index] = ordinal
		for _, key := range stream.keys {
			ordinal++
			turn.Items = append(turn.Items, agent.NativeReplyItem{Ordinal: ordinal, MessageKey: key})
		}
	}
	for _, input := range proof.inputs {
		if index, ok := matched[input.id]; ok {
			turn.Inputs = append(turn.Inputs, agent.NativeReplyBoundary{InputID: input.id, AfterOrdinal: positions[index]})
		}
	}
	return turn
}

func (a *Agent) retireGrokSteer(receipt agent.NativeInputReceipt) {
	a.retireGrokSteerFrom(a.Sink(), receipt)
}

func (a *Agent) retireGrokSteerFrom(sink agent.ProviderServices, receipt agent.NativeInputReceipt) {
	if err := sink.RetireNativeInputTurn(receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED); !agent.OutputRetained(err) {
		slog.Warn("retire the unproved Grok steer", "agent_id", a.AgentID(), "error", err)
	}
}

func (a *Agent) abandonGrokSteerReplies() {
	replies := &a.steerReplies
	replies.mu.Lock()
	current := replies.active
	replies.active = nil
	handoff := replies.takeHandoff()
	children := replies.children
	replies.children = nil
	replies.live = grokLiveModel{}
	if current != nil && current.cancelControlProbe != nil {
		current.cancelControlProbe()
	}
	replies.mu.Unlock()
	if current != nil {
		a.publishGrokSteerReplies(current.snapshot(), leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED)
	}
	if handoff != nil {
		a.retireGrokSteer(handoff.receipt())
	}
	for _, child := range children {
		if child.active != nil {
			a.publishGrokSteerRepliesTo(child.sink, detachGrokChildProof(child), leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED)
		}
	}
}

func (a *Agent) finishGrokSteerJobs() {
	a.steerReplies.mu.Lock()
	a.steerReplies.closed = true
	a.steerReplies.mu.Unlock()
	a.abandonGrokSteerReplies()
	a.steerReplies.jobs.Wait()
}
