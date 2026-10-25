package zcode

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"log/slog"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// The desktop protocol's view of a session.
//
// ZCode's stdio app-server also serves the conversation subscription of its
// desktop protocol (`v4/conversation/subscribe`). Its frames carry two things
// that the session events do not: a pending hook review (workspace_trust.go),
// and the actors and nodes of a workflow run (workflow.go). One subscription
// serves both. It stays open only while one of them needs it, because each frame
// also repeats the conversation that the session events already carry.
//
// The subscription asks for workflow-run deltas, so its first frame is a
// snapshot of the session with its pending interactions and its workflow runs,
// and the frames that follow carry what changed.

// zcodeConversationTopicPrefix starts the topic of a session's conversation.
const zcodeConversationTopicPrefix = "conversation/"

// zcodeConversationClientMode is the client mode of LeapMux's subscription. A
// replayable client takes the snapshot and the deltas that follow it.
const zcodeConversationClientMode = "web-remote-replayable"

// zcodeConversationSnapshot is the payload kind of a frame that states the
// whole session, which the subscription sends first.
const zcodeConversationSnapshot = "snapshot"

// The kinds of a conversation wire frame.
const (
	zcodeWireComplete = "complete"
	zcodeWireFragment = "fragment"
)

// The limits of the fragmented frames, as ZCode's own assembler sets them: the
// fragments of one logical frame, its bytes, and the bytes of every frame that
// waits for more fragments.
const (
	zcodeFrameMaxFragments   = 1024
	zcodeFrameMaxBytes       = 16 * 1024 * 1024
	zcodeFrameMaxStagedBytes = 32 * 1024 * 1024
)

// zcodeFrameChecksumAlgorithm is the checksum of a fragmented frame.
const zcodeFrameChecksumAlgorithm = "crc32"

// zcodeConversation is the state of the subscription. Guarded by a.Mu.
type zcodeConversation struct {
	// topic is the topic of the open subscription, or "" while none is open. It
	// is set before the subscribe request, because ZCode sends the first frame
	// right after its reply.
	topic          string
	subscriptionID string
	// pendingInteractions is the newest list of the session's pending
	// interactions. A frame that changes nothing in the list carries none.
	pendingInteractions json.RawMessage
	fragments           map[string]*zcodeFrameAssembly
	// staged counts the bytes of every fragment that waits for its frame.
	staged int
}

// zcodeFrameAssembly collects the fragments of one logical frame.
type zcodeFrameAssembly struct {
	count    int
	bytes    int
	checksum string
	parts    map[int][]byte
	// staged counts the bytes of the fragments that arrived.
	staged int
}

// zcodeConversationNeededLocked reports whether a consumer needs the
// subscription: a hook trust that waits for its review, or a workflow run whose
// end the frames have not reported. The caller holds a.Mu.
func (a *Agent) zcodeConversationNeededLocked() bool {
	return a.hookReview != nil || a.workflows.active()
}

// reconcileZCodeConversation opens or closes the subscription, so that it is
// open for the current session exactly while a consumer needs it. It sends
// requests, so it never runs on the read loop, which delivers their replies.
// conversationMu keeps one reconcile at a time.
func (a *Agent) reconcileZCodeConversation() error {
	a.conversationMu.Lock()
	defer a.conversationMu.Unlock()
	a.Mu.Lock()
	want := ""
	if a.sessionID != "" && !a.StoppedLocked() && a.zcodeConversationNeededLocked() {
		want = zcodeConversationTopicPrefix + a.sessionID
	}
	have, subscriptionID := a.conversation.topic, a.conversation.subscriptionID
	if want != have {
		a.conversation = zcodeConversation{topic: want, fragments: map[string]*zcodeFrameAssembly{}}
	}
	a.Mu.Unlock()
	if want == have {
		return nil
	}
	if have != "" && subscriptionID != "" {
		a.unsubscribeZCodeConversation(have, subscriptionID)
	}
	if want == "" {
		return nil
	}
	subscriptionID, err := a.subscribeZCodeConversation(want)
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if a.conversation.topic != want {
		return nil
	}
	if err != nil {
		a.conversation = zcodeConversation{}
		return err
	}
	a.conversation.subscriptionID = subscriptionID
	return nil
}

// reconcileZCodeConversationInBackground runs reconcileZCodeConversation off
// the read loop, for a caller on it.
func (a *Agent) reconcileZCodeConversationInBackground() {
	go func() {
		if err := a.reconcileZCodeConversation(); err != nil {
			slog.Warn("zcode conversation subscription", "agent_id", a.AgentID(), "error", err)
		}
	}()
}

// forgetZCodeConversation drops the subscription's state without a request,
// for a process that ended: its subscription ended with it. Its active workflow
// rows take the process exit's outcome.
func (a *Agent) forgetZCodeConversation() {
	status := agent.IncompleteTaskStatus(a.ProcessExitCompletion())
	a.Mu.Lock()
	a.conversation = zcodeConversation{}
	upserts := a.workflows.clear(status)
	a.Mu.Unlock()
	a.upsertZCodeWorkflowRows(upserts)
}

// upsertZCodeWorkflowRows writes outside a.Mu, because a sink broadcast can block.
func (a *Agent) upsertZCodeWorkflowRows(upserts []bgtask.Upsert) {
	for _, upsert := range upserts {
		providerkit.LogRegistryRefusal("zcode", "upsert", a.sink.UpsertBackgroundTask(upsert))
	}
}

// subscribeZCodeConversation subscribes to a session's conversation and returns
// the subscription's ID.
func (a *Agent) subscribeZCodeConversation(topic string) (string, error) {
	raw, err := a.sendZCodeRequest(MethodConversationSubscribe, map[string]any{
		"topic":             topic,
		"connectionId":      zcodeCommandClientID,
		"clientMode":        zcodeConversationClientMode,
		"workflowRunDeltas": true,
	}, a.APITimeout())
	if err != nil {
		return "", fmt.Errorf("subscribe to the ZCode conversation: %w", err)
	}
	var reply struct {
		Ack struct {
			SubscriptionID string `json:"subscriptionId"`
		} `json:"ack"`
	}
	if json.Unmarshal(raw, &reply) != nil || reply.Ack.SubscriptionID == "" {
		return "", errors.New("ZCode answered the conversation subscription without a subscription ID")
	}
	return reply.Ack.SubscriptionID, nil
}

// unsubscribeZCodeConversation ends a subscription. A failure leaves only a
// subscription that the end of the process ends, so it is logged.
func (a *Agent) unsubscribeZCodeConversation(topic, subscriptionID string) {
	if _, err := a.sendZCodeRequest(MethodConversationUnsubscribe, map[string]string{
		"topic":          topic,
		"subscriptionId": subscriptionID,
		"connectionId":   zcodeCommandClientID,
	}, a.APITimeout()); err != nil {
		slog.Warn("zcode unsubscribe from the conversation", "agent_id", a.AgentID(), "error", err)
	}
}

// zcodeConversationWire is one conversation frame on the wire: a complete
// logical frame, or one fragment of one.
type zcodeConversationWire struct {
	Kind           string          `json:"kind"`
	Topic          string          `json:"topic"`
	LogicalFrameID string          `json:"logicalFrameId"`
	Frame          json.RawMessage `json:"frame"`
	FragmentIndex  int             `json:"fragmentIndex"`
	FragmentCount  int             `json:"fragmentCount"`
	LogicalBytes   int             `json:"logicalBytes"`
	Checksum       struct {
		Algorithm string `json:"algorithm"`
		Value     string `json:"value"`
	} `json:"checksum"`
	DataBase64 string `json:"dataBase64"`
}

// zcodeConversationFrame is the part of a logical frame that LeapMux reads:
// the pending interactions and the workflow runs, of a snapshot or of deltas.
type zcodeConversationFrame struct {
	Payload struct {
		Kind     string `json:"kind"`
		Snapshot struct {
			PendingInteractions json.RawMessage         `json:"pendingInteractions"`
			WorkflowRuns        *zcodeWorkflowRunsState `json:"workflowRuns"`
		} `json:"snapshot"`
		Deltas []zcodeConversationDelta `json:"deltas"`
	} `json:"payload"`
}

// zcodeConversationDelta is one change of a delta frame.
type zcodeConversationDelta struct {
	Op    string `json:"op"`
	Patch struct {
		PendingInteractions json.RawMessage         `json:"pendingInteractions"`
		WorkflowRuns        *zcodeWorkflowRunsState `json:"workflowRuns"`
	} `json:"patch"`
	// The fields of a workflowRun.updated or workflowRun.removed change.
	RunID        string                    `json:"runId"`
	Run          *zcodeWorkflowRunState    `json:"run"`
	Actors       []zcodeWorkflowActorState `json:"actors"`
	Nodes        []zcodeWorkflowNodeState  `json:"nodes"`
	RemovedNodes []zcodeWorkflowSiteRef    `json:"removedNodes"`
}

// handleZCodeConversationFrame reads one frame of the subscription. A frame of
// another topic, or one that arrives while no subscription is open, is dropped.
func (a *Agent) handleZCodeConversationFrame(params json.RawMessage) {
	var wire zcodeConversationWire
	if err := json.Unmarshal(params, &wire); err != nil {
		slog.Warn("zcode conversation frame unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.Mu.Lock()
	if a.conversation.topic == "" || wire.Topic != a.conversation.topic {
		a.Mu.Unlock()
		return
	}
	raw := wire.Frame
	switch wire.Kind {
	case zcodeWireComplete:
	case zcodeWireFragment:
		assembled, err := a.conversation.addFragment(wire)
		if err != nil {
			a.Mu.Unlock()
			slog.Warn("zcode conversation fragment dropped", "agent_id", a.AgentID(), "frame", wire.LogicalFrameID, "error", err)
			return
		}
		if assembled == nil {
			a.Mu.Unlock()
			return
		}
		raw = assembled
	default:
		a.Mu.Unlock()
		return
	}
	var frame zcodeConversationFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		a.Mu.Unlock()
		slog.Warn("zcode conversation frame payload unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.conversation.observePendingInteractions(frame)
	if wait := a.hookReview; wait != nil {
		if review, ok := zcodeHookReviewInList(a.conversation.pendingInteractions, wait.bundleDigest); ok {
			select {
			case wait.found <- review:
			default:
			}
		}
	}
	upserts, ended := a.workflows.apply(frame)
	a.Mu.Unlock()
	a.upsertZCodeWorkflowRows(upserts)
	if ended {
		a.reconcileZCodeConversationInBackground()
	}
}

// observePendingInteractions keeps the newest list of pending interactions that
// the frame states: a snapshot's, or the last patch's.
func (c *zcodeConversation) observePendingInteractions(frame zcodeConversationFrame) {
	if list := frame.Payload.Snapshot.PendingInteractions; len(list) > 0 {
		c.pendingInteractions = list
	}
	for _, delta := range frame.Payload.Deltas {
		if list := delta.Patch.PendingInteractions; len(list) > 0 {
			c.pendingInteractions = list
		}
	}
}

// addFragment keeps one fragment and returns the logical frame once every
// fragment arrived. A fragment that contradicts its frame drops the frame.
func (c *zcodeConversation) addFragment(wire zcodeConversationWire) (json.RawMessage, error) {
	if wire.FragmentCount < 1 || wire.FragmentCount > zcodeFrameMaxFragments ||
		wire.FragmentIndex < 0 || wire.FragmentIndex >= wire.FragmentCount ||
		wire.LogicalBytes < 1 || wire.LogicalBytes > zcodeFrameMaxBytes ||
		wire.Checksum.Algorithm != zcodeFrameChecksumAlgorithm {
		c.dropFrame(wire.LogicalFrameID)
		return nil, errors.New("the fragment states an invalid position, size or checksum")
	}
	data, err := base64.StdEncoding.DecodeString(wire.DataBase64)
	if err != nil {
		c.dropFrame(wire.LogicalFrameID)
		return nil, fmt.Errorf("decode the fragment: %w", err)
	}
	if c.fragments == nil {
		c.fragments = map[string]*zcodeFrameAssembly{}
	}
	assembly := c.fragments[wire.LogicalFrameID]
	if assembly == nil {
		assembly = &zcodeFrameAssembly{count: wire.FragmentCount, bytes: wire.LogicalBytes, checksum: wire.Checksum.Value, parts: map[int][]byte{}}
		c.fragments[wire.LogicalFrameID] = assembly
	}
	if assembly.count != wire.FragmentCount || assembly.bytes != wire.LogicalBytes || assembly.checksum != wire.Checksum.Value {
		c.dropFrame(wire.LogicalFrameID)
		return nil, errors.New("the fragment contradicts the earlier fragments of its frame")
	}
	if _, repeated := assembly.parts[wire.FragmentIndex]; repeated {
		c.dropFrame(wire.LogicalFrameID)
		return nil, errors.New("the fragment repeats an earlier fragment of its frame")
	}
	if assembly.staged+len(data) > assembly.bytes || c.staged+len(data) > zcodeFrameMaxStagedBytes {
		c.dropFrame(wire.LogicalFrameID)
		return nil, errors.New("the fragments exceed the bytes that their frame states or that a frame may stage")
	}
	assembly.parts[wire.FragmentIndex] = data
	assembly.staged += len(data)
	c.staged += len(data)
	if len(assembly.parts) < assembly.count {
		return nil, nil
	}
	c.dropFrame(wire.LogicalFrameID)
	var frame bytes.Buffer
	for index := range assembly.count {
		frame.Write(assembly.parts[index])
	}
	if frame.Len() != assembly.bytes {
		return nil, fmt.Errorf("the frame holds %d bytes, not the %d that its fragments state", frame.Len(), assembly.bytes)
	}
	if checksum := fmt.Sprintf("%08x", crc32.ChecksumIEEE(frame.Bytes())); checksum != assembly.checksum {
		return nil, fmt.Errorf("the frame's checksum is %s, not %s", checksum, assembly.checksum)
	}
	return frame.Bytes(), nil
}

// dropFrame forgets the fragments of one logical frame.
func (c *zcodeConversation) dropFrame(logicalFrameID string) {
	if assembly := c.fragments[logicalFrameID]; assembly != nil {
		c.staged -= assembly.staged
		delete(c.fragments, logicalFrameID)
	}
}
