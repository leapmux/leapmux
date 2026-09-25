package acp

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// This file holds what the base does with a session that it stops serving. A
// context clear replaces the session on the running process, and the outgoing
// session must then do nothing that LeapMux does not show:
//
//   - Before the base opens the new session, it answers each control request
//     that the outgoing session holds open, and it cancels the turn that runs.
//   - After the swap, it closes each registry row that the outgoing session
//     opened, forgets the subagent sessions of that session, and asks the agent
//     to end the session and the work that the session left running.
//   - From then on, a control request of a session that the base does not serve
//     never reaches the reader. The base refuses it at once.

// MethodSessionClose ends a session and the work that it left running. An agent
// that offers it advertises `agentCapabilities.sessionCapabilities.close`.
const MethodSessionClose = "session/close"

// acpStaleSessionRequestError is the JSON-RPC code of the refusal of a control
// request that carries no cancel answer of its own: the request states a
// session that this client no longer serves.
const acpStaleSessionRequestError = -32602

// acpOpenRows holds the key of each registry row that this agent opened and did
// not close yet. A context clear closes what it holds, because no later update
// of the outgoing session reaches the base.
//
// It is a copy of what the registry states, and it stays safe for that reason:
// the data flows one way (each write goes to the registry first), and a missed
// entry costs one row that the clear leaves running, which is the state before
// this copy existed. It has its own lock, because the reader goroutine opens
// rows while a worker goroutine clears the context.
type acpOpenRows struct {
	mu   sync.Mutex
	keys map[string]struct{}
}

// opened records a row that is open now.
func (r *acpOpenRows) opened(rowKey string) {
	if rowKey == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.keys == nil {
		r.keys = make(map[string]struct{})
	}
	r.keys[rowKey] = struct{}{}
}

// closed forgets a row that has a final status now.
func (r *acpOpenRows) closed(rowKey string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.keys, rowKey)
}

// renamed moves a row that the registry re-keyed.
func (r *acpOpenRows) renamed(from, to string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, open := r.keys[from]; !open || to == "" {
		return
	}
	delete(r.keys, from)
	r.keys[to] = struct{}{}
}

// takeAll returns and forgets every open row.
func (r *acpOpenRows) takeAll() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	keys := make([]string, 0, len(r.keys))
	for key := range r.keys {
		keys = append(keys, key)
	}
	r.keys = nil
	return keys
}

// ServesSession reports whether sessionID is a session whose traffic this agent
// shows: the current session, or the session of a subagent that a registry row
// routes. An empty sessionID states no session, so it counts as the current one.
//
// It takes no sessionMu, for the reason IsCurrentSession states: the reader
// goroutine calls it.
func (b *Base) ServesSession(sessionID string) bool {
	return sessionID == "" || b.IsCurrentSession(sessionID) || b.childSessionRow(sessionID) != ""
}

// PublishSessionControlRequest publishes one control request that a session of
// the agent raised. The base publishes its permission request and its MCP
// elicitation through it, and a provider publishes each dialog of its own
// through it too, never through PublishControlRequest or
// PublishControlRequestInSession directly, so that no dialog of a retired
// session reaches the reader.
//
// A request of a session that the agent does not serve never reaches the
// reader. That session's updates reach no transcript, so a card for it would
// let the reader approve work that LeapMux never shows. The base refuses such a
// request at once (see refuseControlRequest), because its turn waits on the
// answer and no card could ever give one.
//
// A request that passes is stored under the session that owns it (see
// agentSessionOfControlRequest), because the worker accepts an answer only
// while the agent is in that session.
//
// The provider's ControlRequestObserver reads each request that passes, BEFORE
// the publication, so a withdrawal that the provider reads right after the
// publication finds the request recorded.
func (b *Base) PublishSessionControlRequest(line *providerkit.ParsedLine, cancelAnswer any) {
	sessionID := controlRequestSessionID(line.Params)
	if !b.ServesSession(sessionID) {
		b.refuseControlRequest(line, sessionID, cancelAnswer)
		return
	}
	if b.hooks.ControlRequestObserver != nil {
		b.hooks.ControlRequestObserver(line)
	}
	b.PublishControlRequestInSession(b.sink, b.agentSessionOfControlRequest(sessionID), line.Raw, cancelAnswer)
}

// agentSessionOfControlRequest returns the provider session to store with a
// control request that states sessionID, or "" to make the sink store the
// session that it holds.
//
//   - A request of the main session belongs to that session. So does a request
//     that arrives before the base knows its main session: the agent can raise
//     one while it opens the session, before its answer to session/new gives
//     the id (Grok Build asks for folder trust there). The sink learns a new
//     main session only after the base does. Until then it holds no session at
//     the start, and the replaced session at a context clear. A request stored
//     under that session is one whose every answer the worker refuses.
//   - A request of a subagent session, and a request that states no session,
//     return "". The request does not state which main session owns it, so the
//     session that the sink holds is the best record.
func (b *Base) agentSessionOfControlRequest(sessionID string) string {
	if sessionID != "" && b.IsCurrentSession(sessionID) {
		return sessionID
	}
	return ""
}

// controlRequestSessionID reads the session that a control request states. It
// returns "" for a request that states none.
func controlRequestSessionID(params json.RawMessage) string {
	var request struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(params, &request) != nil {
		return ""
	}
	return request.SessionID
}

// refuseControlRequest answers a control request of a session that the agent
// does not serve, with the cancel answer of the request. A request that carries
// no cancel answer takes a JSON-RPC error: the protocol defines no outcome for
// it, and an error releases the agent without a decision that the reader never
// made.
//
// The write does not wait. The reader goroutine calls this, and it must not
// wait for a stdin write (see SendResponseDetached).
func (b *Base) refuseControlRequest(line *providerkit.ParsedLine, sessionID string, cancelAnswer any) {
	slog.Info("acp refused a control request of a session that it does not serve",
		"provider", b.ProviderName(), "agent_id", b.AgentID(), "method", line.Method, "session_id", sessionID)
	if !line.HasID() {
		return
	}
	if cancelAnswer == nil {
		b.SendErrorResponseDetached(line.ID, acpStaleSessionRequestError,
			fmt.Sprintf("session %s is no longer active in this client", sessionID), "refuse "+line.Method)
		return
	}
	b.SendResponseDetached(line.ID, cancelAnswer, "refuse "+line.Method)
}

// withdrawTurnControls answers and retires the control requests that the
// running turn owns.
//
// By default that is every open request. A provider whose answerless requests
// ask about something other than the turn keeps those open, because a stop or a
// context clear leaves the question as valid as it was (see
// Hooks.AnswerlessControlsOutliveTurns).
func (b *Base) withdrawTurnControls() {
	if b.hooks.AnswerlessControlsOutliveTurns {
		b.AnswerOutstandingControlRequests(b.sink)
		return
	}
	b.WithdrawAllControlRequests(b.sink)
}

// releaseOutgoingSession ends what the outgoing session of a context clear
// waits on, before the clear opens the new session. The caller holds sessionMu.
//
//   - It answers each control request that the session holds open, idle or
//     not: a subagent of the session can wait on one while the main turn is
//     idle, and nothing could answer it after the clear.
//   - It cancels the turn that runs, with session/cancel. A session that runs
//     no turn takes no cancel, because session/cancel ends a prompt turn and
//     there is none.
//
// The answers go FIRST, because the agent blocks on them, and a cancel that
// arrives while one is open stops nothing until the block is released. The stop
// is noted BEFORE the cancel goes out, so a result that the agent sends the
// instant it receives one is already known to belong to a stop.
func (b *Base) releaseOutgoingSession(sessionID string) error {
	b.noteACPInterruptRequested()
	b.withdrawTurnControls()
	if !b.PromptActive() {
		return nil
	}
	return b.sendSessionCancel(sessionID)
}

// sendSessionCancel sends the session/cancel notification for sessionID. The
// caller chooses the session: Interrupt through WithSessionID, and a context
// clear under the sessionMu that it already holds.
func (b *Base) sendSessionCancel(sessionID string) error {
	params, err := json.Marshal(map[string]any{"sessionId": sessionID})
	if err != nil {
		return fmt.Errorf("marshal cancel params: %w", err)
	}
	return b.SendNotification(MethodSessionCancel, params)
}

// retireSession ends what the outgoing session of a context clear left behind.
// ClearContext calls it after the swap, when sessionID is no longer current.
//
//   - Each registry row that the base opened and did not close ends as
//     stopped, with the transcript of its child. No later update of the old
//     session reaches the base, so nothing else would ever close it.
//   - The routes to the subagent sessions go, so a late update of one reaches
//     no transcript and a late control request of one is refused.
//   - An agent that advertises session/close receives it, which ends the
//     subagents and the background work of the session. It goes out
//     detached: the new session is ready now, and nothing waits on the answer.
//   - The provider's own RetireSession hook runs last, for an agent that
//     offers another route to that work.
func (b *Base) retireSession(sessionID string) {
	for _, rowKey := range b.openRows.takeAll() {
		b.finishChildConversation(rowKey, bgtask.StatusStopped)
		if err := b.sink.CloseBackgroundTask(rowKey, bgtask.StatusStopped); err != nil {
			slog.Warn("acp close a row of a retired session", "provider", b.ProviderName(), "agent_id", b.AgentID(), "row_key", rowKey, "error", err)
		}
		b.cleanupChildAgent(rowKey)
		b.forgetChild(rowKey)
	}
	// A child conversation that no open row owns (one that the registry
	// resolved after a worker restart) ends with the session too.
	b.finishAllChildConversations()
	b.forgetChildSessions()

	b.Mu.Lock()
	closes := b.closesSessions
	b.Mu.Unlock()
	if closes {
		b.sendSessionClose(sessionID)
	}
	if b.hooks.RetireSession != nil {
		b.hooks.RetireSession(sessionID)
	}
}

// sendSessionClose sends session/close for sessionID without waiting for the
// answer. A failure is logged: the session is already out of LeapMux's view.
func (b *Base) sendSessionClose(sessionID string) {
	params, err := json.Marshal(map[string]any{"sessionId": sessionID})
	if err != nil {
		slog.Warn("acp marshal session/close", "provider", b.ProviderName(), "agent_id", b.AgentID(), "error", err)
		return
	}
	logFailure := func(_ json.RawMessage, err error) {
		if err != nil {
			slog.Warn("acp session/close failed", "provider", b.ProviderName(), "agent_id", b.AgentID(), "session_id", sessionID, "error", err)
		}
	}
	if err := b.SendDetachedRequest(MethodSessionClose, params, logFailure); err != nil {
		logFailure(nil, err)
	}
}

// advertisesSessionClose reports whether an initialize response advertises
// session/close, as `agentCapabilities.sessionCapabilities.close`. The
// capability is an object, so an absent member, `null` and `false` all mean no.
func advertisesSessionClose(initializeResponse []byte) bool {
	var response struct {
		AgentCapabilities struct {
			SessionCapabilities struct {
				Close json.RawMessage `json:"close"`
			} `json:"sessionCapabilities"`
		} `json:"agentCapabilities"`
	}
	if json.Unmarshal(initializeResponse, &response) != nil {
		return false
	}
	switch capability := string(response.AgentCapabilities.SessionCapabilities.Close); capability {
	case "", "null", "false":
		return false
	default:
		return true
	}
}
