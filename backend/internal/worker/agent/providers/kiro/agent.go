package kiro

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// kiroSteerMethod steers a running turn. Kiro does not advertise it in its
// initialize response, so the provider states it.
const kiroSteerMethod = "_session/steer"

// Agent manages a single Kiro process.
type Agent struct {
	acp.Base

	// stateMu guards every field below. The reader goroutine writes most of
	// them and the worker's goroutines read them, and none is held across a
	// call into the base or the sink.
	stateMu    sync.Mutex
	policy     policyState
	turns      turnState
	children   childState
	workflows  workflowState
	goal       goalState
	controls   controlIndex
	output     toolOutputState
	compaction compactionState

	// goalRestoreMu orders the restore of a recovered goal run against the
	// retirement of the session that the run belongs to. restoreGoalRun holds
	// it across its session check, its write and its report to the sink, and
	// retireSession holds it across its reset. So a restore that passed the
	// check reports its goal before the retirement cancels the run, and the
	// context clear then removes the goal card. It is taken before stateMu,
	// never after.
	goalRestoreMu sync.Mutex
}

// This assertion makes a missing Agent method a compile error.
var _ agent.Agent = (*Agent)(nil)

// Agent steers. Manager.SupportsSteering answers false, with no build error,
// for a provider that stops satisfying InputSteerer, so this assertion makes
// that regression a compile error.
var _ agent.InputSteerer = (*Agent)(nil)

// Agent compacts through Kiro's own request.
var _ agent.ContextCompactor = (*Agent)(nil)

// kiroSteerReply is Kiro's answer to a steer.
type kiroSteerReply struct {
	Queued    bool   `json:"queued"`
	MessageID string `json:"messageId"`
	Dropped   string `json:"dropped"`
}

// SteerInput sends text into the running turn. Kiro queues it and injects it
// before its next model call. A steer that the turn cannot read any more --
// the turn ended as it arrived -- Kiro drops, and the worker then sends the
// text as the next message.
//
// Kiro's steer carries text alone, so a steer with an attachment is refused as
// unsupported, and the worker keeps it for the next turn, attachment and all.
func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	if len(attachments) > 0 {
		return fmt.Errorf("%w: Kiro steers a running turn with text alone", agent.ErrSteeringUnsupported)
	}
	if !a.PromptActive() {
		return agent.ErrNoActiveTurn
	}
	return a.WithSessionID(func(sessionID string) error {
		params, err := json.Marshal(map[string]string{"sessionId": sessionID, "message": content})
		if err != nil {
			return fmt.Errorf("marshal the Kiro steer: %w", err)
		}
		raw, err := a.SendRequest(kiroSteerMethod, params, a.APITimeout())
		if err != nil {
			return providerkit.ClassifyJSONRPCDeliveryError(kiroSteerMethod, err)
		}
		var reply kiroSteerReply
		if err := json.Unmarshal(raw, &reply); err != nil {
			return fmt.Errorf("read the Kiro steer reply: %w", err)
		}
		if !reply.Queued {
			// Kiro drops a steer only when the turn it was for is over.
			return fmt.Errorf("%w: Kiro dropped the steer (%s)", agent.ErrNoActiveTurn, reply.Dropped)
		}
		return nil
	})
}

// kiroCompactMethod compacts the context of a session. Kiro summarizes the
// conversation in a model call of its own, outside any turn, and answers when
// the summary is in place.
const kiroCompactMethod = "_kiro/session/compact"

// compactionState follows a context compaction that LeapMux asked for.
// Guarded by Agent.stateMu.
type compactionState struct {
	// id identifies the compaction that Kiro did not answer yet, and it is 0
	// when none runs.
	id uint64
	// lastID is the id of the last compaction that started. Ids never repeat,
	// so the late answer of a compaction that an interrupt or a context clear
	// dropped finds another id, or none.
	lastID uint64
	// summarized records that Kiro reported the result of the running
	// compaction, so its answer states nothing more.
	summarized bool
}

// CompactContext asks Kiro to compact the context.
//
// Kiro compacts outside any turn: the request starts no turn marker, and its
// response is the end. The input queue records the dispatch as a turn, so the
// turn opens here and closes when the response arrives, and a "compacting"
// notice shows meanwhile. Kiro states the summary itself, and the response
// states a compaction that it refused or found nothing for.
//
// It returns once the request is written: a compaction takes as long as a
// model call.
func (a *Agent) CompactContext() error {
	if a.PromptActive() {
		return agent.ErrAgentBusy
	}
	a.stateMu.Lock()
	if a.compaction.id != 0 {
		a.stateMu.Unlock()
		return fmt.Errorf("a Kiro context compaction is already pending")
	}
	id := a.compaction.lastID + 1
	a.compaction = compactionState{id: id, lastID: id}
	a.stateMu.Unlock()
	if !a.BeginAgentTurn() {
		a.dropCompaction(id)
		return agent.ErrAgentBusy
	}
	a.persistCompacting()
	err := a.WithSessionID(func(sessionID string) error {
		params, err := json.Marshal(map[string]string{"sessionId": sessionID})
		if err != nil {
			return fmt.Errorf("marshal the Kiro compaction: %w", err)
		}
		return a.SendDetachedRequest(kiroCompactMethod, params, func(response json.RawMessage, err error) {
			a.finishCompaction(id, response, err)
		})
	})
	if err != nil {
		a.dropCompaction(id)
		a.EndAgentTurnWithoutRow()
		return err
	}
	return nil
}

// Interrupt stops the running turn.
//
// A compaction that LeapMux asked for runs outside every turn of Kiro, so
// session/cancel does not end it, and Kiro can take a model call's time to
// answer, or never answer. So the interrupt also drops the compaction: the
// turn that it held ends with no divider, and its late answer changes
// nothing. Kiro can still finish the summary, and then states it in its own
// report.
func (a *Agent) Interrupt() error {
	err := a.Base.Interrupt()
	a.stateMu.Lock()
	running := a.compaction.id
	a.stateMu.Unlock()
	if a.dropCompaction(running) {
		a.persistStatus("Stopped waiting for the context compaction")
		a.EndAgentTurnWithoutRow()
	}
	return err
}

// dropCompaction forgets the compaction id, and reports whether it was the
// running one.
func (a *Agent) dropCompaction(id uint64) bool {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if id == 0 || a.compaction.id != id {
		return false
	}
	a.compaction = compactionState{lastID: a.compaction.lastID}
	return true
}

// kiroCompactReply is Kiro's answer to a compaction.
type kiroCompactReply struct {
	Success bool `json:"success"`
}

// finishCompaction states what the answer of a compaction adds to Kiro's own
// report, and closes the turn that the compaction held.
//
// The answer of a compaction that an interrupt or a context clear dropped
// changes nothing: its turn already ended, and a later turn is not its own.
func (a *Agent) finishCompaction(id uint64, response json.RawMessage, err error) {
	a.stateMu.Lock()
	if a.compaction.id != id {
		a.stateMu.Unlock()
		slog.Debug("kiro answered a compaction that LeapMux dropped", "agent_id", a.AgentID(), "error", err)
		return
	}
	summarized := a.compaction.summarized
	a.compaction = compactionState{lastID: a.compaction.lastID}
	a.stateMu.Unlock()
	defer a.EndAgentTurnWithoutRow()
	if a.IsStopped() {
		return
	}
	if err != nil {
		a.persistAgentError(fmt.Sprintf("Kiro could not compact the context: %v", err))
		return
	}
	var reply kiroCompactReply
	if jsonErr := json.Unmarshal(response, &reply); jsonErr != nil {
		a.persistAgentError(fmt.Sprintf("Kiro answered the compaction with an unreadable reply: %v", jsonErr))
		return
	}
	switch {
	case !reply.Success:
		// Kiro refuses while a turn or another compaction runs, and when the
		// model call fails.
		a.persistStatus("Kiro did not compact the context")
	case !summarized:
		// Kiro succeeds with no report when the conversation holds nothing to
		// summarize.
		a.persistStatus("The conversation had nothing to compact")
	}
}

// noteCompactionSummarized records that Kiro reported the result of the
// running compaction.
func (a *Agent) noteCompactionSummarized() {
	a.stateMu.Lock()
	if a.compaction.id != 0 {
		a.compaction.summarized = true
	}
	a.stateMu.Unlock()
}
