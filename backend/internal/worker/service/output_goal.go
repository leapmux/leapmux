package service

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// The session-goal applier.
//
// Shaped after updatePlan, not after the background-task registry, because a
// goal is the same KIND of state as the plan: exactly one per agent, stored as
// columns on the agents row, and announced with one neutral notification when
// the user-visible part changes. A registry would bring renames, eviction pools
// and per-kind capacity to a record that can never have a second row.
//
// The one rule that shapes everything here: the provider reports the goal far
// more often than the goal CHANGES. Codex sends a full report after every
// completed tool call. So this file splits each report in two.
//
//   - The durable half -- objective, status, status detail, identity -- goes to
//     the agents row and to the panel, and ONLY when it differs from what is
//     already stored.
//   - The volatile half -- tokens, seconds, iterations -- goes to the ephemeral
//     session-info broadcast, which dedups by encoded value and never persists.
//
// Without that split a 200-tool turn costs 200 database writes and 200
// broadcasts to store numbers nobody keeps.
//
// The TRANSCRIPT takes a third, narrower slice: only a change to the goal
// itself, never one to the provider's status word beside it. See the two tests
// in applyGoalUpdate for why the status detail is stored but never announced.

// goalMutex returns the per-agent mutex that serializes the read-modify-write
// on the goal columns.
//
// Three writers race here: the provider's output-read loop, the UpdateAgentGoal
// RPC dispatcher, and a cold-load read. Each of them compares the stored goal
// against a new one and writes the difference, so without this lock two reports
// that arrive together can both read the old row and both decide they are the
// transition -- which writes two transcript rows for one change. Mirrors
// notifMutex, and is a separate map because a goal write must not wait behind
// notification threading for an unrelated message.
func (h *OutputHandler) goalMutex(agentID string) *sync.Mutex {
	v, _ := h.goalMu.LoadOrStore(agentID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// applyGoalUpdate records one provider report of the session goal.
func (h *OutputHandler) applyGoalUpdate(agentID string, provider leapmuxv1.AgentProvider, update agent.GoalUpdate) {
	update = update.Clean()

	mu := h.goalMutex(agentID)
	mu.Lock()
	defer mu.Unlock()

	row, err := h.queries.GetAgentByID(bgCtx(), agentID)
	if err != nil {
		slog.Warn("failed to fetch agent for goal update", "agent_id", agentID, "error", err)
		return
	}

	// TWO tests, over different fields, because the row and the transcript
	// answer different questions.
	//
	// A TRANSITION is a change to the goal itself: what it aims at, what state
	// it is in, or which goal it is. `created_at` is in it because Codex puts no
	// goal id on the wire -- a user who restarts the SAME objective gets a fresh
	// createdAt and nothing else, so a test over (objective, status) alone would
	// read a restart as no change and never announce it.
	statusWire := agent.GoalStatusWire(update.Status)
	sameIdentity := sameGoalCreatedAt(row.GoalCreatedAt, update)
	transition := row.GoalObjective != update.Objective ||
		row.GoalStatus != statusWire ||
		!sameIdentity

	// The status DETAIL is deliberately not part of that test. It is the
	// provider's own word, and two providers move it every turn: Claude Code
	// puts the goal evaluator's reason for the last "not yet" there, and
	// Reasonix streams a lastReason on a cadence of its own. Announcing those
	// would rebuild the exact transcript flood this whole applier exists to
	// stop -- a row per turn, each saying the same goal in different words. So a
	// detail change is STORED and BROADCAST, which is what the card reads, and
	// never announced.
	detailChanged := row.GoalStatusDetail != update.StatusDetail

	if !transition && !detailChanged {
		return
	}

	now := h.now()
	createdAt := row.GoalCreatedAt
	if !update.CreatedAt.IsZero() {
		createdAt = sqltime.SQLiteNullTime{Time: update.CreatedAt, Valid: true}
	} else if !createdAt.Valid {
		// A provider that reports no creation time still needs an identity, or
		// every later report of the same goal would look like a replacement.
		createdAt = sqltime.SQLiteNullTime{Time: now, Valid: true}
	}

	// One spelling, used by both the row and the broadcast. The browser orders
	// its answers by this value, so a stamp the broadcast invented separately
	// from the stored one would order against something no row holds.
	updatedAt := sqltime.SQLiteNullTime{Time: now, Valid: true}
	if err := h.queries.UpdateAgentGoal(bgCtx(), db.UpdateAgentGoalParams{
		GoalObjective:    update.Objective,
		GoalStatus:       statusWire,
		GoalStatusDetail: update.StatusDetail,
		GoalCreatedAt:    createdAt,
		GoalUpdatedAt:    updatedAt,
		ID:               agentID,
	}); err != nil {
		slog.Warn("failed to update agent goal", "agent_id", agentID, "error", err)
		return
	}

	goal := h.goalProto(update.Objective, statusWire, update.StatusDetail, createdAt)
	h.broadcastGoal(agentID, goal, goalStamp(updatedAt))

	// Only a transition reaches the transcript. A detail-only change already
	// updated the row and the card above, and has nothing to announce.
	if !transition {
		return
	}
	// A SNAPSHOT restates the goal instead of announcing a change, so it
	// updates the row and the panel above but writes nothing to the transcript.
	// Codex pushes one on every thread/resume; persisting it would print
	// "Goal set: X" at restart time for a goal set an hour ago.
	if update.Snapshot {
		return
	}
	// The same lie, reached a different way, and it needs its own rule because
	// only two providers mark a restatement for themselves.
	//
	// ClearGoalStatusesAtBoot blanks goal_status for a goal that outlived the
	// worker, so the provider's first report after a restart RE-ARMS a status
	// nothing was pursuing. That is a transition by the test above, and
	// announcing it would print "Goal set: X" at restart time for a goal set
	// before the restart -- exactly what Codex's snapshot flag prevents for
	// Codex, and what nothing prevents for Claude Code or Reasonix.
	//
	// A blank status beside a non-empty objective is a state ONLY that sweep
	// produces: a clear wipes the objective too, and GoalUpdate.Clean refuses to
	// store an objective with no status. Requiring the objective and the
	// identity to match as well is what keeps a genuinely NEW goal announced.
	if row.GoalStatus == "" && row.GoalObjective != "" &&
		row.GoalObjective == update.Objective && sameIdentity {
		return
	}
	// An objective nobody can read has nothing to announce. This is reachable:
	// StripUnreadable empties a string made only of control characters.
	if update.Objective == "" {
		return
	}
	h.PersistLeapMuxNotification(agentID, provider, map[string]interface{}{
		"type":          agent.NotificationTypeGoalUpdated,
		"objective":     update.Objective,
		"goal_status":   statusWire,
		"status_detail": update.StatusDetail,
	})
}

// clearGoal removes the session goal.
func (h *OutputHandler) clearGoal(agentID string, provider leapmuxv1.AgentProvider) {
	mu := h.goalMutex(agentID)
	mu.Lock()
	defer mu.Unlock()

	row, err := h.queries.GetAgentByID(bgCtx(), agentID)
	if err != nil {
		slog.Warn("failed to fetch agent for goal clear", "agent_id", agentID, "error", err)
		return
	}
	// The DELETE is issued from what the DATABASE holds, never from an
	// in-memory copy. Codex sends thread/goal/cleared on resume to mean "this
	// thread has no goal", and that arrives exactly when the worker's copy is
	// cold and the row still holds a goal from the previous process.
	hadGoal := row.GoalObjective != "" || row.GoalStatus != "" || row.GoalCreatedAt.Valid

	// Captured, not read twice: the stamp that goes in the row must be the one
	// the broadcast carries, or the browser orders its answers against a value
	// the worker never stored.
	clearedAt := h.now()
	if err := h.queries.ClearAgentGoal(bgCtx(), db.ClearAgentGoalParams{
		GoalUpdatedAt: sqltime.SQLiteNullTime{Time: clearedAt, Valid: true},
		ID:            agentID,
	}); err != nil {
		slog.Warn("failed to clear agent goal", "agent_id", agentID, "error", err)
		return
	}
	if !hadGoal {
		// Nothing was there. The write above still ran, so the row is
		// unambiguously empty, but there is no change to announce and no panel
		// to update.
		return
	}
	h.broadcastGoal(agentID, nil, goalStamp(sqltime.SQLiteNullTime{Time: clearedAt, Valid: true}))
	h.PersistLeapMuxNotification(agentID, provider, map[string]interface{}{
		"type":      agent.NotificationTypeGoalCleared,
		"objective": row.GoalObjective,
	})
}

// sameGoalCreatedAt reports whether the stored creation time already matches
// this report's. A report that states no creation time cannot contradict the
// stored one, so it compares equal and the identity is left alone.
func sameGoalCreatedAt(stored sqltime.SQLiteNullTime, update agent.GoalUpdate) bool {
	if update.CreatedAt.IsZero() {
		return true
	}
	return stored.Valid && stored.Time.Equal(update.CreatedAt)
}

// goalProgressInfo builds the volatile half of a report, or nil when the
// provider reported no counter at all.
//
// Every field is omitted when the provider did not report it. Absent and zero
// are different answers: Codex sends no iteration count and ZCode sends no
// token usage, so rendering "0 tokens used" for a provider that never mentioned
// tokens would state something false.
func goalProgressInfo(update agent.GoalUpdate) map[string]interface{} {
	progress := map[string]interface{}{}
	if update.TokensUsed != nil {
		progress[contracts.GoalProgressFieldTokensUsed] = *update.TokensUsed
	}
	if update.TokenBudget != nil {
		progress[contracts.GoalProgressFieldTokenBudget] = *update.TokenBudget
	}
	if update.TimeUsedSeconds != nil {
		progress[contracts.GoalProgressFieldTimeUsedSeconds] = *update.TimeUsedSeconds
	}
	if update.Iterations != nil {
		progress[contracts.GoalProgressFieldIterations] = *update.Iterations
	}
	if len(progress) == 0 {
		return nil
	}
	return progress
}

// GoalChangedEvent builds the goal event. A nil goal means the agent has none,
// and updatedAt orders that answer against the other paths that answer the same
// question.
//
// One builder, because THREE paths send this same message -- the applier's
// broadcast, the capability re-publish, and the WatchEvents replay -- and each
// must carry every field. A second hand-built copy drops a field the day one is
// added, silently, because a missing proto field is a zero value and not an
// error.
func (h *OutputHandler) GoalChangedEvent(agentID string, goal *leapmuxv1.AgentGoal, updatedAt string) *leapmuxv1.AgentEvent {
	return &leapmuxv1.AgentEvent{
		AgentId: agentID,
		Event: &leapmuxv1.AgentEvent_GoalChanged{
			GoalChanged: &leapmuxv1.AgentGoalChanged{
				AgentId:          agentID,
				Goal:             goal,
				SupportedActions: h.SupportedGoalActions(agentID),
				GoalUpdatedAt:    updatedAt,
			},
		},
	}
}

// broadcastGoal fans the new durable goal out to live watchers.
func (h *OutputHandler) broadcastGoal(agentID string, goal *leapmuxv1.AgentGoal, updatedAt string) {
	h.watcher.BroadcastAgentEvent(agentID, h.GoalChangedEvent(agentID, goal, updatedAt))
}

// wireGoalString returns s with every invalid UTF-8 byte removed, so a stored
// value can never fail proto.Marshal. It drops the byte rather than writing
// U+FFFD, the same answer validate.StripUnreadable gives, so the two agree.
func wireGoalString(s string) string {
	return strings.ToValidUTF8(s, "")
}

// goalStamp formats the ordering stamp, or "" when the agent never had a goal.
// The layout is fixed-width UTC, so the recipient orders two stamps with a
// plain string compare and never parses a date.
func goalStamp(updatedAt sqltime.SQLiteNullTime) string {
	if !updatedAt.Valid {
		return ""
	}
	return timefmt.Format(updatedAt.Time)
}

// goalProto builds the wire message, or nil when the agent has no goal.
//
// Neither the supported ACTIONS nor the ordering stamp is here, for one reason:
// both must exist when a goal does not. "This agent can set a goal" is what the
// empty state needs to know, and the stamp orders the very write that removes
// the goal. Both ride AgentGoalChanged and the cold-load response instead.
func (h *OutputHandler) goalProto(objective, statusWire, statusDetail string, createdAt sqltime.SQLiteNullTime) *leapmuxv1.AgentGoal {
	if objective == "" && statusWire == "" {
		return nil
	}
	goal := &leapmuxv1.AgentGoal{
		// Repaired HERE, on the projection, exactly as bgtask.Item.ToProto
		// repairs its labels, and for the reason that file gives: sqlite stores
		// a bad byte verbatim, so a value that reached the column before this
		// build's GoalUpdate.Clean existed is read back on every boot.
		//
		// Clean already strips these on the write path, so this is the READ path
		// speaking for itself rather than trusting every past writer. The cost of
		// being wrong is not a missing goal: proto.Marshal fails the WHOLE
		// message for one bad byte, and this projection feeds
		// ListAgentMessagesResponse -- so a single byte would fail an entire page
		// of chat history, with nothing on screen to say why.
		Objective:    wireGoalString(objective),
		Status:       agent.GoalStatusToProto(agent.GoalStatusFromWire(statusWire)),
		StatusDetail: wireGoalString(statusDetail),
	}
	if createdAt.Valid {
		goal.CreatedAt = timefmt.Format(createdAt.Time)
	}
	return goal
}

// SupportedGoalActions asks the RUNNING agent what it can do. An agent that is
// not running, or whose provider implements no goal control (Reasonix reports a
// goal but cannot honestly change one), answers with an empty list, and the
// browser disables every control.
func (h *OutputHandler) SupportedGoalActions(agentID string) []leapmuxv1.AgentGoalAction {
	if h.agents == nil {
		return nil
	}
	actions := h.agents.SupportedGoalActions(agentID)
	if len(actions) == 0 {
		return nil
	}
	out := make([]leapmuxv1.AgentGoalAction, 0, len(actions))
	for _, a := range actions {
		out = append(out, agent.GoalActionToProto(a))
	}
	return out
}

// publishGoalCapabilities re-broadcasts the agent's goal together with what the
// now-running process can do with it.
//
// The goal itself is unchanged; this exists for the ACTIONS beside it. They are
// a property of the live process, so they cannot be answered before it
// registers -- and both earlier opportunities to send them (the cold-load
// response and the WatchEvents replay) can run first.
//
// A child agent has no goal, and no capability either.
func (h *OutputHandler) publishGoalCapabilities(agentID string) {
	snapshot, err := h.LoadGoal(bgCtx(), agentID)
	if err != nil {
		slog.Warn("failed to read agent goal for capability broadcast", "agent_id", agentID, "error", err)
		return
	}
	h.broadcastGoal(agentID, snapshot.Goal, snapshot.UpdatedAt)
}

// GoalSnapshot is one answer about an agent's goal: the goal, and the stamp
// that orders that answer against the other paths which answer it.
//
// The two travel together because the stamp is needed exactly when the Goal is
// nil -- a cleared goal is an answer, and it is the one a stale reply
// resurrects. Returning them separately would let a call site carry the goal
// and drop the stamp, which fails silently.
type GoalSnapshot struct {
	Goal      *leapmuxv1.AgentGoal
	UpdatedAt string
}

// LoadGoal returns the agent's stored goal for a cold start, or a nil Goal when
// it has none. A CHILD agent always answers nil: a child never owns a goal, and
// the Codex handler drops a child thread's goal rather than overwriting its
// root's.
func (h *OutputHandler) LoadGoal(ctx context.Context, agentID string) (GoalSnapshot, error) {
	row, err := h.queries.GetAgentByID(ctx, agentID)
	if err != nil {
		return GoalSnapshot{}, err
	}
	if row.ParentAgentID.Valid {
		return GoalSnapshot{}, nil
	}
	return GoalSnapshot{
		Goal:      h.goalProto(row.GoalObjective, row.GoalStatus, row.GoalStatusDetail, row.GoalCreatedAt),
		UpdatedAt: goalStamp(row.GoalUpdatedAt),
	}, nil
}

// ClearGoalStatusesAtBoot blanks every stored goal status once, at worker start.
//
// A goal that survived a restart is not being pursued: no process holds it, and
// the provider has not re-reported it. Leaving the status set would draw a
// panel with live Pause and Clear buttons for a goal nothing is running, and
// pressing one would either error or act on a process that never knew about it.
//
// The OBJECTIVE text stays, so the panel can still say what was being
// attempted. The provider's own snapshot re-arms the status when the session
// resumes -- Codex pushes exactly that on thread/resume.
func (h *OutputHandler) ClearGoalStatusesAtBoot(ctx context.Context) error {
	return h.queries.ClearAllAgentGoalStatuses(ctx)
}

// --- sink methods ---

func (s *agentOutputSink) UpsertGoal(update agent.GoalUpdate) {
	if !s.ownsGoal("UpsertGoal") {
		return
	}
	update = update.Clean()
	// The volatile counters ride this sink's own session-info channel, which
	// dedups by encoded value and never persists. Routing them through the sink
	// rather than the handler is what earns that dedup -- the cache lives here.
	if progress := goalProgressInfo(update); progress != nil {
		s.BroadcastSessionInfo(map[string]interface{}{
			contracts.SessionInfoKeyGoalProgress: progress,
		})
	}
	s.h.applyGoalUpdate(s.agentID, s.agentProvider, update)
}

func (s *agentOutputSink) PublishGoalCapabilities() {
	if !s.ownsGoal("PublishGoalCapabilities") {
		return
	}
	s.h.publishGoalCapabilities(s.agentID)
}

func (s *agentOutputSink) ClearGoal() {
	if !s.ownsGoal("ClearGoal") {
		return
	}
	s.h.clearGoal(s.agentID, s.agentProvider)
}

// ownsGoal reports whether this sink may write a goal, which only a ROOT sink
// may.
//
// A goal is session state, and a child transcript is not a session: Codex's
// collab children ARE threads and can carry a goal of their own, so a child
// sink that wrote one would overwrite its root's objective with a subagent's.
// The Codex handler already drops a child thread's goal before it gets here, so
// reaching this guard is a provider bug -- it is logged rather than silently
// redirected, because redirecting would store a goal under an agent that never
// had one.
func (s *agentOutputSink) ownsGoal(op string) bool {
	if s.agentID == s.rootAgentID {
		return true
	}
	slog.Warn("refusing session-goal write from a child sink",
		"op", op, "agent_id", s.agentID, "root_agent_id", s.rootAgentID)
	return false
}
