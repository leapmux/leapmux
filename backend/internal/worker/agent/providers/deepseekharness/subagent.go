package deepseekharness

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

type childNameKey struct {
	parentSessionID string
	name            string
}

type nativeChildDescriptor struct {
	ID     string `json:"childId"`
	Mode   string `json:"mode"`
	Label  string `json:"label"`
	Name   string `json:"name"`
	Parent string `json:"-"`
}
type nativeChild struct {
	descriptor      nativeChildDescriptor
	agentID         string
	spawnID         string
	active          bool
	nativeTurnEpoch uint64
	sendMu          sync.Mutex
	steerMu         sync.Mutex
	// Mu protects pendingSteers.
	pendingSteers []*pendingSteer
}

func (a *Agent) beginChild(stream *sessionStream, raw []byte) error {
	var descriptor nativeChildDescriptor
	if err := json.Unmarshal(raw, &descriptor); err != nil || descriptor.ID == "" {
		return fmt.Errorf("DeepSeek Harness child catalog has no identity")
	}
	return a.beginOwnedChild(stream, descriptor, false)
}

func (a *Agent) beginOwnedChild(stream *sessionStream, descriptor nativeChildDescriptor, fromSnapshot bool) error {
	switch descriptor.Mode {
	case "continuable", "one-shot":
	case "external", "unknown":
		slog.Debug("DeepSeek Harness ignoring non-owned subagent mode", "agent_id", a.AgentID(), "child_id", descriptor.ID, "mode", descriptor.Mode)
		return nil
	default:
		return fmt.Errorf("DeepSeek Harness child catalog has an invalid mode")
	}
	descriptor.Parent = stream.sessionID
	a.Mu.Lock()
	old, exists := a.childCatalog[descriptor.ID]
	pendingWorkflow, hasPendingWorkflow := a.pendingWorkflowChildren[descriptor.ID]
	a.Mu.Unlock()
	if exists {
		if old != descriptor {
			return fmt.Errorf("DeepSeek Harness child catalog changes an owned identity")
		}
		return nil
	}
	sink := a.streamSink(stream)
	spawnID := ""
	title := descriptor.Label
	if hasPendingWorkflow && pendingWorkflow.Label != "" {
		title = pendingWorkflow.Label
	}
	virtualID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole: bgtask.RoleChildTask,
		SpawnSpanID:  spawnID, ProviderChildKey: descriptor.ID, AgentSessionID: descriptor.ID, Title: title, Options: optionmap.Map{contracts.DeepseekHarnessOptionChildMode: descriptor.Mode}})
	if err != nil {
		return err
	}
	childSink := sink.ChildSink(virtualID)
	childSink.UpdateSessionID(descriptor.ID)
	child := &nativeChild{descriptor: descriptor, agentID: virtualID, spawnID: spawnID}
	upsert := bgtask.Upsert{RowKey: descriptor.ID, Kind: bgtask.KindSubagent, ChildAgentID: virtualID, Title: title, Status: bgtask.StatusRunning}
	if hasPendingWorkflow {
		upsert.GroupKey = pendingWorkflow.GroupKey
		upsert.GroupLabel = pendingWorkflow.GroupLabel
	}
	if err := sink.UpsertBackgroundTask(upsert); err != nil {
		return err
	}
	if err := a.followSessionWithMode(sessionAddress{Kind: "subagent", ParentSessionID: descriptor.Parent, ChildSessionID: descriptor.ID, Mode: descriptor.Mode}, virtualID, fromSnapshot); err != nil {
		return err
	}
	a.Mu.Lock()
	a.registerCatalogChildLocked(descriptor)
	a.children[descriptor.ID] = child
	delete(a.pendingWorkflowChildren, descriptor.ID)
	a.Mu.Unlock()
	return nil
}

// registerCatalogChildLocked records descriptor in childCatalog and its
// (parentSessionID, name) secondary index. The caller holds Mu.
func (a *Agent) registerCatalogChildLocked(descriptor nativeChildDescriptor) {
	if a.childCatalog == nil {
		a.childCatalog = map[string]nativeChildDescriptor{}
	}
	a.childCatalog[descriptor.ID] = descriptor
	if descriptor.Name != "" {
		if a.childCatalogByName == nil {
			a.childCatalogByName = map[childNameKey]string{}
		}
		a.childCatalogByName[childNameKey{parentSessionID: descriptor.Parent, name: descriptor.Name}] = descriptor.ID
	}
}

// A continuable result identifies the exact child created by this exact native call.
func (a *Agent) bindChildResult(stream *sessionStream, callID string, raw []byte) error {
	sink := a.streamSink(stream)
	name := sink.GetSpanType(callID)
	if name != contracts.DeepseekHarnessToolSubagent {
		return nil
	}
	var result struct {
		Message struct {
			CallID  string `json:"toolCallId"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	if result.Message.IsError {
		return nil
	}
	if result.Message.CallID != callID {
		return fmt.Errorf("DeepSeek Harness child result changes its call identity")
	}
	if len(result.Message.Content) != 1 || result.Message.Content[0].Type != contracts.DeepseekHarnessContentTypeText {
		return nil
	}
	const prefix = "started subagent "
	text := result.Message.Content[0].Text
	if !strings.HasPrefix(text, prefix) {
		return nil
	}
	target := strings.TrimSpace(strings.TrimPrefix(text, prefix))
	a.Mu.Lock()
	childID, descriptor, known := a.resolveCatalogChildLocked(stream.sessionID, target)
	child := a.children[childID]
	if !known && target != "" {
		// v3 subagent results can report "started subagent <name>" where <name>
		// is the child's label rather than its childId. Match a unique unbound
		// continuable child owned by this stream.
		var matchID string
		var matchDesc nativeChildDescriptor
		var matchChild *nativeChild
		matches := 0
		for id, candidate := range a.childCatalog {
			if candidate.Parent != stream.sessionID || candidate.Mode != "continuable" || candidate.Label != target {
				continue
			}
			c := a.children[id]
			if c == nil || c.descriptor != candidate {
				continue
			}
			if c.spawnID == callID {
				matchID = id
				matchDesc = candidate
				matchChild = c
				matches = 1
				break
			}
			if c.spawnID != "" {
				continue
			}
			matchID = id
			matchDesc = candidate
			matchChild = c
			matches++
		}
		if matches == 1 {
			childID = matchID
			descriptor = matchDesc
			child = matchChild
			known = true
		}
	}
	a.Mu.Unlock()
	if !known || descriptor.Parent != stream.sessionID || descriptor.Mode != "continuable" || child == nil || child.descriptor != descriptor {
		slog.Warn("DeepSeek Harness child result did not match a continuable catalog entry", "agent_id", a.AgentID(), "call_id", callID, "target", target)
		return nil
	}
	if child.spawnID != "" && child.spawnID != callID {
		return fmt.Errorf("DeepSeek Harness child result changes its first spawn span")
	}
	virtualID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole: bgtask.RoleChildTask,
		SpawnSpanID:  callID, ProviderChildKey: childID, Title: descriptor.Label, Options: optionmap.Map{contracts.DeepseekHarnessOptionChildMode: descriptor.Mode}})
	if err != nil {
		return err
	}
	if virtualID != child.agentID {
		return fmt.Errorf("DeepSeek Harness child result changes its stored child identity")
	}
	a.Mu.Lock()
	child.spawnID = callID
	a.Mu.Unlock()
	return nil
}

func (a *Agent) setChildActive(id string, active bool) {
	a.Mu.Lock()
	if child := a.children[id]; child != nil {
		if active {
			child.nativeTurnEpoch++
		}
		child.active = active
	}
	a.Mu.Unlock()
}

// resolveCatalogChildLocked resolves a `started subagent <target>` result to
// its parent-owned catalog entry. dsh 0.2.0–0.2.1 returns the child session ID;
// dsh 0.2.2+ returns the child's path-qualified name (e.g. "agent-1" or
// "reviewer/checker") whose final segment matches the sibling-unique catalog name.
func (a *Agent) resolveCatalogChildLocked(parentSessionID, target string) (string, nativeChildDescriptor, bool) {
	if descriptor, known := a.childCatalog[target]; known {
		return target, descriptor, true
	}
	if target == "" {
		return "", nativeChildDescriptor{}, false
	}
	leafName := target
	if slash := strings.LastIndexByte(target, '/'); slash >= 0 {
		leafName = target[slash+1:]
	}
	if leafName == "" {
		return "", nativeChildDescriptor{}, false
	}
	if id, ok := a.childCatalogByName[childNameKey{parentSessionID: parentSessionID, name: leafName}]; ok {
		if descriptor, known := a.childCatalog[id]; known && descriptor.Parent == parentSessionID && descriptor.Name == leafName {
			return id, descriptor, true
		}
	}
	return "", nativeChildDescriptor{}, false
}
func (a *Agent) childTurnState(id string, active bool, completion agent.MessageCompletion) error {
	a.Mu.Lock()
	child := a.children[id]
	if child == nil {
		a.Mu.Unlock()
		return nil
	}
	status := bgtask.StatusRunning
	if !active {
		switch completion {
		case agent.MessageCompletionComplete:
			status = bgtask.StatusSucceeded
		case agent.MessageCompletionError:
			status = bgtask.StatusFailed
		case agent.MessageCompletionInterrupted:
			status = bgtask.StatusInterrupted
		case agent.MessageCompletionFinished:
			status = bgtask.StatusEndedWithUnknownOutcome
		default:
			a.Mu.Unlock()
			return fmt.Errorf("DeepSeek Harness has an invalid child completion %q", completion)
		}
	}
	if active {
		child.nativeTurnEpoch++
	}
	child.active = active
	childAgentID := child.agentID
	continuable := child.descriptor.Mode == "continuable"
	var turnSeq uint64
	if continuable && childAgentID != "" {
		turnSeq = a.NextTurnSeq()
	}
	a.Mu.Unlock()
	var err error
	if a.sink != nil {
		if active {
			if err = a.sink.ReviveBackgroundTask(id); err == nil {
				err = a.sink.UpdateBackgroundTaskStatus(id, bgtask.StatusRunning, "")
			}
		} else {
			err = a.sink.CloseBackgroundTask(id, status)
		}
		if continuable && childAgentID != "" {
			state := agent.TurnState{}
			if active {
				state = agent.TurnState{Active: true, Steerable: true}
			}
			providerkit.PublishTurnStateTo(a.sink.ChildSink(childAgentID), state, turnSeq)
		}
	}
	return err
}

func (a *Agent) SendChildInput(childKey string, input agent.ChildInput) (agent.ChildInputResult, error) {
	receipt, err := a.sendChild(childKey, input.Content, input.Attachments, nil, true)
	return agent.ChildInputResult{StartsTurn: true, NativeReceipt: receipt}, err
}

func (a *Agent) SteerChildInput(childKey string, request agent.SteerRequest) (agent.NativeInputReceipt, error) {
	_, err := a.sendChild(childKey, request.Content, request.Attachments, &request, request.AttemptID != "")
	return agent.NativeInputReceipt{}, err
}

// ChildSteerPolicy holds the steer row until echo and keeps it pending when
// the child finishes its running turn while delivery is in flight: DeepSeek
// Harness retains the message in the child's inbox for the next turn.
func (*Agent) ChildSteerPolicy() agent.SteerPolicy {
	return agent.SteerPolicy{RowAtEcho: true, SurvivesTurnEnd: true}
}

func (a *Agent) sendChild(childKey, content string, attachments []*leapmuxv1.Attachment, steer *agent.SteerRequest, trackReply bool) (agent.NativeInputReceipt, error) {
	var inputReceipt agent.NativeInputReceipt
	delivery := "queue"
	if steer != nil {
		delivery = "steer"
	}
	a.Mu.Lock()
	if a.streamFailure != nil || a.StoppedLocked() {
		a.Mu.Unlock()
		return inputReceipt, fmt.Errorf("DeepSeek Harness cannot send input after its native process stops")
	}
	child := a.children[childKey]
	if child == nil || child.descriptor.Mode != "continuable" {
		a.Mu.Unlock()
		return inputReceipt, agent.ErrChildOperationUnsupported
	}
	a.Mu.Unlock()

	child.sendMu.Lock()
	defer child.sendMu.Unlock()
	if delivery == "steer" {
		child.steerMu.Lock()
		defer child.steerMu.Unlock()
	}

	a.Mu.Lock()
	if a.streamFailure != nil || a.StoppedLocked() {
		a.Mu.Unlock()
		return inputReceipt, fmt.Errorf("DeepSeek Harness cannot send input after its native process stops")
	}
	active := child.active
	parentSessionID := child.descriptor.Parent
	childAgentID := child.agentID
	a.Mu.Unlock()
	if delivery == "queue" && active {
		return inputReceipt, agent.ErrAgentBusy
	}
	if delivery == "steer" && !active {
		return inputReceipt, agent.ErrNoActiveTurn
	}
	parts := []map[string]any{{"type": "text", "text": content}}
	for _, attachment := range agent.ClassifyAttachments(attachments) {
		if attachment.Kind != contracts.AttachmentMediaKindImage {
			return inputReceipt, fmt.Errorf("DeepSeek Harness child input accepts text and images")
		}
		parts = append(parts, map[string]any{"type": "image", "mediaType": attachment.MIMEType, "data": base64.StdEncoding.EncodeToString(attachment.Data), "name": attachment.Filename})
	}
	childSink := a.sink.ChildSink(childAgentID)
	requestID := uuid.NewString()
	var submission *harnessReplySubmission
	if trackReply {
		inputReceipt = agent.NativeInputReceipt{SessionID: childKey, InputID: requestID}
		submission = a.registerReplySubmission(childSink, inputReceipt, steer)
	}
	var startEpoch uint64
	if delivery == "queue" {
		a.Mu.Lock()
		if a.streamFailure != nil || a.StoppedLocked() {
			a.Mu.Unlock()
			if submission != nil {
				a.takeReplySubmissionEchoed(submission)
			}
			return agent.NativeInputReceipt{}, fmt.Errorf("DeepSeek Harness cannot send input after its native process stops")
		}
		if child.active {
			a.Mu.Unlock()
			if submission != nil {
				a.takeReplySubmissionEchoed(submission)
			}
			return agent.NativeInputReceipt{}, agent.ErrAgentBusy
		}
		child.active = true
		startEpoch = child.nativeTurnEpoch
		seq := a.NextTurnSeq()
		a.Mu.Unlock()
		if childAgentID != "" {
			childSink.SetTurnState(agent.TurnState{Active: true, Steerable: true}, seq)
		}
	}
	var pending *pendingSteer
	if steer != nil {
		pending = newPendingSteer(requestID, *steer)
		a.Mu.Lock()
		child.pendingSteers = append(child.pendingSteers, pending)
		a.Mu.Unlock()
	}
	var receipt struct {
		ID string `json:"messageId"`
	}
	err := a.rpc.request(a.Context(), "subagents/prompt", map[string]any{"parentSessionId": parentSessionID, "childSessionId": childKey, "mode": "continuable", "delivery": delivery, "requestId": requestID, "content": parts}, &receipt)
	if err == nil && receipt.ID == "" {
		err = fmt.Errorf("DeepSeek Harness did not confirm the child prompt")
	}
	if err == nil {
		if pending != nil {
			a.Mu.Lock()
			if pending.itemID == "" {
				pending.itemID = receipt.ID
			}
			a.Mu.Unlock()
		}
		return inputReceipt, nil
	}
	if submission != nil {
		if a.takeReplySubmissionEchoed(submission) {
			if pending != nil {
				a.forgetPendingSteer(steerTarget{sessionID: childKey, isChild: true}, pending)
			}
			return inputReceipt, nil
		}
		inputReceipt = agent.NativeInputReceipt{}
	}
	if pending != nil {
		a.Mu.Lock()
		delivered := pending.delivered
		child.pendingSteers = slices.DeleteFunc(child.pendingSteers, func(candidate *pendingSteer) bool { return candidate == pending })
		a.Mu.Unlock()
		if delivered {
			return inputReceipt, nil
		}
	}
	if delivery == "queue" {
		a.clearOptimisticChildTurnState(child, childSink, startEpoch)
	}
	if isDefinitePromptRefusal(err) {
		return inputReceipt, err
	}
	return inputReceipt, fmt.Errorf("%w: DeepSeek Harness did not confirm the child prompt: %w", agent.ErrDeliveryUncertain, err)
}

func (a *Agent) clearOptimisticChildTurnState(child *nativeChild, childSink agent.ProviderServices, startEpoch uint64) {
	a.Mu.Lock()
	if !child.active || child.nativeTurnEpoch != startEpoch {
		a.Mu.Unlock()
		return
	}
	child.active = false
	seq := a.NextTurnSeq()
	childAgentID := child.agentID
	a.Mu.Unlock()
	if childAgentID != "" && childSink != nil {
		childSink.SetTurnState(agent.TurnState{}, seq)
	}
}

func (a *Agent) ActiveChildTurnState(childKey string) agent.TurnState {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	child := a.children[childKey]
	if child == nil {
		return agent.TurnState{}
	}
	return agent.TurnState{Active: child.active, Steerable: child.active && child.descriptor.Mode == "continuable"}
}

func (a *Agent) InterruptChild(childKey string, stop agent.StopContext) error {
	a.Mu.Lock()
	child := a.children[childKey]
	var mode string
	if child != nil {
		mode = child.descriptor.Mode
	}
	a.Mu.Unlock()
	if child == nil || mode != "continuable" {
		return agent.ErrChildOperationUnsupported
	}

	child.steerMu.Lock()
	defer child.steerMu.Unlock()

	a.Mu.Lock()
	active := child.active
	parent := child.descriptor.Parent
	childAgentID := child.agentID
	steers := slices.Clone(child.pendingSteers)
	a.Mu.Unlock()

	if active {
		err := a.rpc.call(a.Context(), "subagents/interruptByUser", map[string]any{"childSessionId": childKey}, nil)
		if providerkit.IsHTTPStatus(err, http.StatusNotFound) {
			// DeepSeek Harness 0.2.0–0.2.1 exposed subagents/interruptByParent before renaming it to subagents/interruptByUser.
			err = a.rpc.call(a.Context(), "subagents/interruptByParent", map[string]any{"childSessionId": childKey, "parentSessionId": parent, "mode": "continuable"}, nil)
		}
		if err != nil {
			return err
		}
	}

	var failures error
	var childSink agent.ProviderServices
	if a.sink != nil && childAgentID != "" {
		childSink = a.sink.ChildSink(childAgentID)
	}
	childTarget := steerTarget{sessionID: childKey, isChild: true, sink: childSink}
	for _, steer := range steers {
		failures = errors.Join(failures, a.removePendingSteer(childTarget, steer))
	}

	if !active {
		a.dispatchMu.Lock()
		a.Mu.Lock()
		current := a.children[childKey]
		stillIdle := current != nil && !current.active && !a.StoppedLocked()
		var childStreams []*sessionStream
		if stillIdle {
			childAgentID = current.agentID
			for _, stream := range a.sortedStreamsLocked() {
				if stream.sessionID == childKey && stream.childAgentID != "" {
					childStreams = append(childStreams, stream)
				}
			}
		}
		a.Mu.Unlock()
		if stillIdle {
			for _, stream := range childStreams {
				failures = errors.Join(failures, a.finishPendingTools(stream, agent.MessageCompletionInterrupted))
				providerkit.ResetSpansAndProgress(a.streamSink(stream))
			}
			if len(childStreams) == 0 && childAgentID != "" && a.sink != nil {
				providerkit.ResetSpansAndProgress(a.sink.ChildSink(childAgentID))
			}
		}
		a.dispatchMu.Unlock()
	}
	return failures
}
