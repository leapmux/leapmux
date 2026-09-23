package copilot

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Agent owns one native connection and its current session.
type Agent struct {
	*copilotConnection
	sink agent.ProviderServices
	opts agent.Options

	sessionMu        sync.RWMutex
	stateMu          sync.Mutex
	sessionID        string
	options          optionmap.Map
	models           []*agent.ModelInfo
	active           bool
	turnOrder        providerkit.TurnSeq
	activityRevision uint64

	// outputMu serializes the reader goroutine against every caller that replaces
	// or ends the session. It also guards the three maps below, which only those
	// two paths touch.
	outputMu  sync.Mutex
	closing   bool
	children  map[string]*copilotNativeChild
	openTools map[string]*copilotOpenTool
	// nextNativeToolOrder numbers the open calls, so a turn that ends with several
	// of them closes each one in the order the runtime opened it.
	nextNativeToolOrder uint64
	// nativeText accumulates the assistant text the runtime STREAMS.
	//
	// The runtime marks every delta ephemeral and sends the assistant message only
	// once that message is complete, so a turn cut short stored nothing and the answer
	// the reader watched vanished. See closeStreamedNativeText and CP-015.
	nativeText providerkit.GenerationBuffer
	// nativeTextSegments identifies the transcript each accumulating segment belongs to, in
	// the order the runtime opened them, so a turn end writes its rows the way the
	// agent produced them.
	nativeTextSegments []copilotStreamedText

	controlMu sync.Mutex
	controls  map[string]*copilotPendingControl

	// interestMu guards the event-log subscription handles of the CURRENT session.
	// The runtime returns a distinct handle for each registration and keeps the
	// subscription alive until its handle is released, so a replacement that
	// forgot them would leave the old session's interests registered for the life
	// of the process. See CP-009.
	interestMu sync.Mutex
	interests  []string

	goalMu sync.Mutex
	goal   copilotGoalSnapshot

	// backgroundReads coalesces the reads offReader starts. It carries its OWN
	// mutex: its bookkeeping has nothing to do with the session snapshot, the
	// option map or the model catalog that stateMu guards, and riding that lock
	// only widened what a reader must hold in mind to order six of them.
	backgroundReads coalescingRunner
}

var _ agent.Agent = (*Agent)(nil)
var _ agent.InputSteerer = (*Agent)(nil)

// The keys of the background reads that offReader keeps apart.
const (
	copilotReadGoal     = "goal"
	copilotReadSettings = "settings"
)

// offReader runs work on its own goroutine, and keeps ONE run of each key in flight.
//
// A dispatch branch that needs a round trip cannot make it inline: the response
// arrives on the reader goroutine that runs the branch, so a direct call would wait
// for itself. The runtime also states that an axis MOVED without stating what it
// became, so a burst of changes would otherwise start a request for each one -- all
// reading the same answer, and finishing in an order the last write cannot be trusted
// to respect.
//
// A request that arrives while a run is in flight therefore does not start a second
// run. It marks the key instead, and one more run follows the current one. That last
// run reads a state no earlier request can precede, so a change the in-flight run
// already passed is never lost.
//
// Each run holds sessionMu for reading, so a session replacement cannot start under
// it, and it does nothing once the process is stopped.
func (a *Agent) offReader(key string, work func()) {
	a.backgroundReads.run(key, func() { a.runUnderNativeSession(work) })
}

// coalescingRunner runs one goroutine for each key, and collapses every request
// that arrives while that key's run is in flight into ONE follow-up run.
//
// It is a general mechanism, so it owns its own mutex rather than riding a lock
// that guards unrelated state. The follow-up is what makes the last run read a
// state no pending request can precede.
type coalescingRunner struct {
	mu sync.Mutex
	// pending maps a key with a run in flight to whether another request arrived
	// during it. Absence means no run is in flight for that key.
	pending map[string]bool
}

// idle reports that no run is in flight for any key.
func (r *coalescingRunner) idle() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pending) == 0
}

// inFlight reports the run of each key, and whether one more request arrived
// during it. It copies the map, so a caller never reads the runner's own state
// without the lock.
func (r *coalescingRunner) inFlight() map[string]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]bool, len(r.pending))
	for key, queued := range r.pending {
		out[key] = queued
	}
	return out
}

// run starts work for key, or marks the in-flight run to repeat once more.
func (r *coalescingRunner) run(key string, work func()) {
	r.mu.Lock()
	if _, running := r.pending[key]; running {
		r.pending[key] = true
		r.mu.Unlock()
		return
	}
	if r.pending == nil {
		r.pending = make(map[string]bool)
	}
	r.pending[key] = false
	r.mu.Unlock()
	go func() {
		for {
			work()
			r.mu.Lock()
			if !r.pending[key] {
				delete(r.pending, key)
				r.mu.Unlock()
				return
			}
			r.pending[key] = false
			r.mu.Unlock()
		}
	}()
}

// stopNativeConnection ends the process and drops the state of the session it served.
//
// A path that disposed of its session and could not open another one has nothing to
// roll back to. An agent that kept its process alive there would accept input that can
// never arrive anywhere.
func (a *Agent) stopNativeConnection() {
	a.outputMu.Lock()
	a.closing = true
	a.clearNativeChildren()
	a.clearNativeControls()
	a.outputMu.Unlock()
	a.forgetNativeControlEvents()
	a.copilotConnection.Stop()
}

// PublishTurnActive republishes the Worker-visible turn state from a.active.
//
// Every active turn is STEERABLE: Copilot's session.send accepts
// mode:"immediate" while a turn runs (see SteerInput), so the input queue may
// offer Steer for any active turn.
func (a *Agent) PublishTurnActive() agent.TurnState {
	a.stateMu.Lock()
	active := a.active
	sequence := a.turnOrder.NextTurnSeq()
	a.stateMu.Unlock()
	return providerkit.PublishTurnStateTo(a.sink, agent.TurnState{Active: active, Steerable: active}, sequence)
}

func (a *Agent) setNativeTurnActive(active bool) bool {
	a.stateMu.Lock()
	previous := a.active
	a.active = active
	a.activityRevision++
	sequence := a.turnOrder.NextTurnSeq()
	a.stateMu.Unlock()
	providerkit.PublishTurnStateTo(a.sink, agent.TurnState{Active: active, Steerable: active}, sequence)
	return previous
}

func (a *Agent) rejectNativeInput(revision uint64) {
	a.stateMu.Lock()
	if revision != a.activityRevision {
		a.stateMu.Unlock()
		return
	}
	a.active = false
	sequence := a.turnOrder.NextTurnSeq()
	a.stateMu.Unlock()
	providerkit.PublishTurnStateTo(a.sink, agent.TurnState{}, sequence)
}

func (a *Agent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInputForSession(nil, content, attachments)
}

func (a *Agent) sendInputForSession(expected *string, content string, attachments []*leapmuxv1.Attachment) error {
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	if a.IsStopped() {
		return fmt.Errorf("the Copilot process is stopped")
	}
	a.stateMu.Lock()
	if err := providerkit.CheckInputSession(expected, a.sessionID); err != nil {
		a.stateMu.Unlock()
		return err
	}
	if a.active {
		a.stateMu.Unlock()
		return agent.ErrAgentBusy
	}
	if a.sessionID == "" {
		a.stateMu.Unlock()
		return fmt.Errorf("the Copilot session is unavailable")
	}
	a.active = true
	a.activityRevision++
	revision := a.activityRevision
	a.stateMu.Unlock()
	a.PublishTurnActive()
	values := map[string]any{"prompt": content}
	if len(attachments) > 0 {
		blobs := make([]map[string]string, 0, len(attachments))
		for _, attachment := range agent.ClassifyAttachments(attachments) {
			blobs = append(blobs, map[string]string{
				"type": "blob", "displayName": attachment.Filename, "mimeType": attachment.MIMEType,
				"data": base64.StdEncoding.EncodeToString(attachment.Data),
			})
		}
		values["attachments"] = blobs
	}
	raw, err := a.requestNativeSession("send", values)
	if err != nil {
		var rejection *providerkit.JSONRPCResponseError
		if errors.As(err, &rejection) {
			a.rejectNativeInput(revision)
		}
		return providerkit.ClassifyJSONRPCDeliveryError("session.send", err)
	}
	var response struct {
		MessageID string `json:"messageId"`
	}
	if json.Unmarshal(raw, &response) != nil || response.MessageID == "" {
		return fmt.Errorf("%w: Copilot omitted the delivered message ID", agent.ErrDeliveryUncertain)
	}
	return nil
}

// SupportsSteering always reports true. Copilot's session.send takes a SendMode,
// and mode:"immediate" interjects the message during an in-progress turn, so the
// capability needs no handshake discovery.
func (a *Agent) SupportsSteering() bool { return true }

// SteerInput injects a user message into the RUNNING turn with Copilot's
// SendMode "immediate".
//
// The turn's activity bookkeeping is NOT touched: the turn this steers is the
// one already recorded active, and a rejected steer must not read as the turn
// ending. Contrast sendInputForSession, which owns the idle->active transition
// and therefore owns the revision it can roll back.
func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	if a.IsStopped() {
		return fmt.Errorf("agent is stopped")
	}
	a.stateMu.Lock()
	if a.sessionID == "" {
		a.stateMu.Unlock()
		return fmt.Errorf("the Copilot session is unavailable")
	}
	active := a.active
	a.stateMu.Unlock()
	if !active {
		return agent.ErrNoActiveTurn
	}
	values := map[string]any{"prompt": content, "mode": "immediate"}
	if len(attachments) > 0 {
		blobs := make([]map[string]string, 0, len(attachments))
		for _, attachment := range agent.ClassifyAttachments(attachments) {
			blobs = append(blobs, map[string]string{
				"type": "blob", "displayName": attachment.Filename, "mimeType": attachment.MIMEType,
				"data": base64.StdEncoding.EncodeToString(attachment.Data),
			})
		}
		values["attachments"] = blobs
	}
	raw, err := a.requestNativeSession("send", values)
	if err != nil {
		return providerkit.ClassifyJSONRPCDeliveryError("session.send", err)
	}
	var response struct {
		MessageID string `json:"messageId"`
	}
	if json.Unmarshal(raw, &response) != nil || response.MessageID == "" {
		return fmt.Errorf("%w: Copilot omitted the delivered message ID", agent.ErrDeliveryUncertain)
	}
	return nil
}

// Interrupt aborts the running turn with Copilot's own `session.abort`.
//
// A stopped agent REFUSES, the way every other provider in the roster refuses.
// The worker's InterruptAgent handler reads that refusal as "not running" and
// says so; a nil here reported a stop that never happened, and the handler then
// withdrew the prompts of a process that had already gone.
func (a *Agent) Interrupt() error {
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	if a.IsStopped() {
		return fmt.Errorf("agent is stopped")
	}
	_, err := a.requestNativeSession("abort", nil)
	return err
}

func (a *Agent) Stop() {
	a.NoteIntentionalStop()
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()
	a.outputMu.Lock()
	a.closing = true
	a.outputMu.Unlock()
	if !a.IsStopped() && a.currentNativeSessionID() != "" {
		// Release the subscriptions before the suspend, while the session still
		// accepts session work. A suspended session accepts a release as well
		// (CP-009), but the order that needs no exception is the simpler rule.
		a.releaseNativeControlEvents()
		params, err := json.Marshal(map[string]string{"sessionId": a.currentNativeSessionID()})
		if err == nil {
			_, err = a.SendRequest("session.suspend", params, 2*time.Second)
		}
		if err != nil {
			slog.Debug("Suspend Copilot before process exit", "agent_id", a.AgentID(), "error", err)
		}
	}
	a.copilotConnection.Stop()
	a.outputMu.Lock()
	a.clearNativeChildren()
	a.clearNativeControls()
	a.outputMu.Unlock()
	a.setNativeTurnActive(false)
}

func (a *Agent) Wait() error {
	err := a.copilotConnection.Wait()
	a.outputMu.Lock()
	a.closing = true
	a.clearNativeChildren()
	a.clearNativeControls()
	a.outputMu.Unlock()
	a.forgetNativeControlEvents()
	a.setNativeTurnActive(false)
	return err
}

func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInputForSession(&sessionID, content, attachments)
}
