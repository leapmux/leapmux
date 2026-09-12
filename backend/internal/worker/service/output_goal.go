package service

import (
	"context"
	"log/slog"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/util/validate"
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

// goalPresent reports whether a row holds a goal.
//
// ONE rule, because three sites ask the question and they used to answer it
// three ways: the projection tested the objective, the clear path also counted
// a stored identity, and Clean forced an empty objective to mean no goal. A row
// could therefore be "no goal" to the card and "had a goal" to the clear, which
// wrote a "Goal cleared" row with an empty objective for a card that had
// already emptied itself.
//
// The OBJECTIVE decides. A goal with no text is one the user cannot read, and
// the identity column alone is bookkeeping, not a goal.
func goalPresent(objective string) bool { return objective != "" }

// applyGoalUpdate records one provider report of the session goal.
//
// The report arrives already cleaned: agentOutputSink.UpsertGoal runs Clean at
// the sink boundary, which is where GoalUpdate.Clean's own doc says it belongs.
// Cleaning again here would re-scan the whole objective on a path Codex drives
// after every completed tool call.
func (h *OutputHandler) applyGoalUpdate(agentID string, provider leapmuxv1.AgentProvider, update agent.GoalUpdate) {
	// A report with no readable objective is a REMOVAL, and clearGoal is the
	// one path that performs one. Reaching the code below with it half-wiped
	// the row -- objective, status and detail blanked, the identity left behind
	// -- broadcast an empty card, and wrote nothing to the transcript, so the
	// goal vanished mid-session with no record. Three routes produce it:
	// Reasonix sends an absent objective as "", ZCode returns early only when
	// BOTH halves are empty, and Clean empties an objective made of control
	// characters.
	if !goalPresent(update.Objective) {
		h.clearGoal(agentID, provider, update.Snapshot)
		return
	}
	mu := h.goalMutex(agentID)
	mu.Lock()
	defer mu.Unlock()

	row, err := h.queries.GetAgentGoal(bgCtx(), agentID)
	if err != nil {
		slog.Warn("failed to fetch agent for goal update", "agent_id", agentID, "error", err)
		return
	}
	h.applyGoalUpdateFromRow(agentID, provider, row, update)
}

// applyGoalUpdateFromRow applies a report under the per-agent goal mutex.
func (h *OutputHandler) applyGoalUpdateFromRow(agentID string, provider leapmuxv1.AgentProvider, row db.GetAgentGoalRow, update agent.GoalUpdate) {
	// TWO tests, over different fields, because the row and the transcript
	// answer different questions.
	//
	// A TRANSITION is a change to the goal itself: what it aims at, what state
	// it is in, or which goal it is. `created_at` is in it because Codex puts no
	// goal id on the wire -- a user who restarts the SAME objective gets a fresh
	// createdAt and nothing else, so a test over (objective, status) alone would
	// read a restart as no change and never announce it.
	//
	// The first report after a worker restart needs no rule of its own. The row
	// holds the last status the provider reported, dormancy is derived rather
	// than stored, so a restatement of the same goal matches on all three parts
	// and announces nothing. A goal that finished or blocked while the worker
	// was down still differs in status, and is still announced.
	statusWire := agent.GoalStatusWire(update.Status)
	sameIdentity := sameGoalIdentity(row, update)
	replaced := row.GoalObjective != update.Objective || !sameIdentity
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
	nativeID := update.NativeID
	if nativeID == "" && !replaced {
		nativeID = row.GoalNativeID
	}
	identityAdded := nativeID != row.GoalNativeID

	if !transition && !detailChanged && !identityAdded {
		return
	}

	now := h.now()
	createdAt := row.GoalCreatedAt
	if !update.CreatedAt.IsZero() {
		createdAt = sqltime.SQLiteNullTime{Time: update.CreatedAt, Valid: true}
	} else if !createdAt.Valid || replaced {
		// A provider that reports no creation time still needs an identity, or
		// every later report of the same goal would look like a replacement.
		createdAt = sqltime.SQLiteNullTime{Time: now, Valid: true}
	}

	// One spelling, used by both the row and the broadcast. The browser orders
	// its answers by this value, so a stamp the broadcast invented separately
	// from the stored one would order against something no row holds.
	updatedAt := sqltime.SQLiteNullTime{Time: now, Valid: true}
	if err := h.queries.UpdateAgentGoal(bgCtx(), db.UpdateAgentGoalParams{
		GoalNativeID:     nativeID,
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

	goal := goalProto(GoalColumns{NativeID: nativeID, Objective: update.Objective, StatusWire: statusWire, StatusDetail: update.StatusDetail, CreatedAt: createdAt})
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
	h.PersistLeapMuxNotification(agentID, provider, map[string]interface{}{
		"type":          contracts.NotificationTypeGoalUpdated,
		"objective":     update.Objective,
		"goal_status":   statusWire,
		"status_detail": update.StatusDetail,
		// The transition KIND, so the transcript can say what happened rather
		// than guess it from the resulting status. Without it a resume -- which
		// changes the status to `active` and nothing else -- reads as "Goal
		// set: X" two rows under "Goal paused: X", announcing a new goal for an
		// objective nobody replaced.
		"goal_transition": goalTransitionKind(row, update),
	})
}

// goalTransitionKind identifies the transition from the row as it stood
// before the write. Only the applier holds both sides, so only the applier can
// answer: the persisted payload carries the resulting state alone, and several
// different changes land on the same one.
func goalTransitionKind(row db.GetAgentGoalRow, update agent.GoalUpdate) string {
	// Derived HERE rather than taken as a parameter. The caller computed it from
	// these same two values, and a signature that accepted it would let a second
	// caller pass an identity answer that disagrees with the update -- which
	// names the wrong verb in the transcript and nothing catches it.
	sameIdentity := sameGoalIdentity(row, update)
	// Reaching a status case below means the STATUS is what moved: the caller
	// only asks after a transition, and the two cases above take the objective
	// and the identity.
	switch {
	case !goalPresent(row.GoalObjective):
		return contracts.GoalTransitionSet
	case row.GoalObjective != update.Objective || !sameIdentity:
		return contracts.GoalTransitionReplaced
	case update.Status == agent.GoalStatusActive:
		return contracts.GoalTransitionResumed
	case update.Status == agent.GoalStatusPaused:
		return contracts.GoalTransitionPaused
	case update.Status == agent.GoalStatusDone:
		return contracts.GoalTransitionAchieved
	default:
		return contracts.GoalTransitionBlocked
	}
}

// clearGoal removes the session goal.
//
// `snapshot` marks a clear that RESTATES the absence rather than announcing a
// removal, exactly as GoalUpdate.Snapshot does for the upsert half. Codex
// pushes thread/goal/cleared on every thread/resume for a thread that has none,
// and that arrives when the worker's copy is cold and the row still holds a
// goal from the previous process -- so without the flag the user opens the chat
// after a restart and reads "Goal cleared: ship the tests" for a clear nobody
// performed. The write and the broadcast still run; only the transcript row is
// suppressed.
func (h *OutputHandler) clearGoal(agentID string, provider leapmuxv1.AgentProvider, snapshot bool) {
	mu := h.goalMutex(agentID)
	mu.Lock()
	defer mu.Unlock()

	row, err := h.queries.GetAgentGoal(bgCtx(), agentID)
	if err != nil {
		slog.Warn("failed to fetch agent for goal clear", "agent_id", agentID, "error", err)
		return
	}
	// The DELETE is issued from what the DATABASE holds, never from an
	// in-memory copy. Codex sends thread/goal/cleared on resume to mean "this
	// thread has no goal", and that arrives exactly when the worker's copy is
	// cold and the row still holds a goal from the previous process.
	hadGoal := goalPresent(row.GoalObjective)

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
	if snapshot {
		return
	}
	h.PersistLeapMuxNotification(agentID, provider, map[string]interface{}{
		"type":      contracts.NotificationTypeGoalCleared,
		"objective": row.GoalObjective,
	})
}

// Native identities are authoritative. Creation time distinguishes providers without an ID.
func sameGoalIdentity(stored db.GetAgentGoalRow, update agent.GoalUpdate) bool {
	if stored.GoalNativeID != "" && update.NativeID != "" {
		return stored.GoalNativeID == update.NativeID
	}
	if update.CreatedAt.IsZero() {
		return true
	}
	return stored.GoalCreatedAt.Valid && stored.GoalCreatedAt.Time.Equal(update.CreatedAt)
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
func goalProto(columns GoalColumns) *leapmuxv1.AgentGoal {
	// The one presence rule, so the projection cannot disagree with the clear
	// path about the same row. See goalPresent.
	if !goalPresent(columns.Objective) {
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
		NativeId:     validate.WireString(columns.NativeID),
		Objective:    validate.WireString(columns.Objective),
		Status:       agent.GoalStatusToProto(agent.GoalStatusFromWire(columns.StatusWire)),
		StatusDetail: validate.WireString(columns.StatusDetail),
	}
	if columns.CreatedAt.Valid {
		goal.CreatedAt = timefmt.Format(columns.CreatedAt.Time)
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
	// Under the goal mutex, so the read and the broadcast cannot straddle a
	// concurrent applyGoalUpdate. Without it this can read stamp T1, lose the
	// race to a write that broadcasts T2, and then broadcast T1 -- which the
	// browser drops as stale, together with the capability list that is the
	// whole reason this broadcast exists.
	mu := h.goalMutex(agentID)
	mu.Lock()
	defer mu.Unlock()

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
	row, err := h.queries.GetAgentGoal(ctx, agentID)
	if err != nil {
		return GoalSnapshot{}, err
	}
	return h.GoalSnapshotFrom(goalColumnsOfRow(row)), nil
}

// GoalSnapshotFrom projects an agents row that the caller ALREADY holds.
//
// The RPC handlers reach the goal columns through a row the dispatcher loaded
// for them, so re-reading the row inside LoadGoal would run a second query for
// data already in hand -- once per cold chat load and once per resubscribe. The
// registry code reads its own column from that row for the same reason.
func (h *OutputHandler) GoalSnapshotFrom(cols GoalColumns) GoalSnapshot {
	if cols.IsChild {
		return GoalSnapshot{}
	}
	statusWire, statusDetail := cols.StatusWire, cols.StatusDetail
	// DORMANT is derived here and stored nowhere.
	//
	// It means "the objective is stored and no live process pursues it", which
	// is the running-agent map's answer, not a fact about the row. Writing it
	// into the column took two sweeps -- one at boot, one per process exit --
	// and every exit those two missed left a card with a live Active dot and
	// working Pause and Clear buttons for a process that was gone. Reading it
	// from the map cannot miss one.
	//
	// The provider's own word goes with it. It described the running state
	// ("verifying", "two suites still fail"), and beside a dormant goal it
	// states a check that nothing performs.
	if goalPresent(cols.Objective) && !h.agentAlive(cols.AgentID) {
		statusWire, statusDetail = contracts.GoalStatusTokenDormant, ""
	}
	cols.StatusWire, cols.StatusDetail = statusWire, statusDetail
	return GoalSnapshot{
		Goal:      goalProto(cols),
		UpdatedAt: goalStamp(cols.UpdatedAt),
	}
}

// agentAlive reports whether a live process serves the agent.
//
// An absent Manager answers TRUE, which is the conservative answer: it means
// "do not claim dormant". A worker with no agent manager cannot observe an exit
// either, so inventing the state it would have recorded is worse than leaving
// the provider's last word alone.
func (h *OutputHandler) agentAlive(agentID string) bool {
	return h.agents == nil || h.agents.AgentAlive(agentID)
}

// GoalColumns is the goal half of an agents row, named.
//
// A struct rather than six positional parameters, because three of those were
// interchangeable strings in a row and two were interchangeable nullable times:
// a transposed pair compiled cleanly and rendered the provider's own word as
// the neutral status, or ordered every answer by the wrong stamp. Two adapters
// build it, one per sqlc row type, so no call site spells the field order.
type GoalColumns struct {
	NativeID string
	// AgentID, because the projection asks the running-agent map whether a
	// process still serves this row. It comes off the row itself, so no call
	// site pairs a set of columns with somebody else's id.
	AgentID      string
	IsChild      bool
	Objective    string
	StatusWire   string
	StatusDetail string
	CreatedAt    sqltime.SQLiteNullTime
	UpdatedAt    sqltime.SQLiteNullTime
}

// GoalColumnsOfAgent reads them from the full agents row the RPC dispatcher
// already loaded.
func GoalColumnsOfAgent(row db.Agent) GoalColumns {
	return GoalColumns{
		NativeID:     row.GoalNativeID,
		AgentID:      row.ID,
		IsChild:      row.ParentAgentID.Valid,
		Objective:    row.GoalObjective,
		StatusWire:   row.GoalStatus,
		StatusDetail: row.GoalStatusDetail,
		CreatedAt:    row.GoalCreatedAt,
		UpdatedAt:    row.GoalUpdatedAt,
	}
}

// goalColumnsOfRow reads them from the narrow query the goal paths use.
func goalColumnsOfRow(row db.GetAgentGoalRow) GoalColumns {
	return GoalColumns{
		NativeID:     row.GoalNativeID,
		AgentID:      row.ID,
		IsChild:      row.ParentAgentID.Valid,
		Objective:    row.GoalObjective,
		StatusWire:   row.GoalStatus,
		StatusDetail: row.GoalStatusDetail,
		CreatedAt:    row.GoalCreatedAt,
		UpdatedAt:    row.GoalUpdatedAt,
	}
}

// --- sink methods ---

func (s *agentOutputSink) UpsertGoal(update agent.GoalUpdate) {
	if !s.ownsGoal("UpsertGoal") {
		return
	}
	update = update.Clean()
	// The GOAL first, then the counters, and the order is load bearing.
	//
	// Both ride the same ordered channel to the browser, and the browser drops
	// the counters whenever the goal they measured is replaced -- which includes
	// the FIRST goal it ever sees, because it has no previous one to compare
	// against. Broadcasting the counters first therefore delivered them, and the
	// goal event landing second threw them away, so a resumed session showed a
	// card with no progress row at all until the next report moved a number.
	s.h.applyGoalUpdate(s.agentID, s.agentProvider, update)
	// The volatile counters ride this sink's own session-info channel, which
	// dedups by encoded value and never persists. Routing them through the sink
	// rather than the handler is what earns that dedup -- the cache lives here.
	if progress := goalProgressInfo(update); progress != nil {
		s.BroadcastSessionInfo(map[string]interface{}{
			contracts.SessionInfoKeyGoalProgress: progress,
		})
	}
}

func (s *agentOutputSink) PublishGoalCapabilities() {
	if !s.ownsGoal("PublishGoalCapabilities") {
		return
	}
	s.h.publishGoalCapabilities(s.agentID)
}

func (s *agentOutputSink) UpdateGoalStatus(expected, status agent.GoalStatus) {
	if !s.ownsGoal("UpdateGoalStatus") {
		return
	}
	switch status {
	case agent.GoalStatusActive, agent.GoalStatusPaused, agent.GoalStatusBlocked, agent.GoalStatusDone:
	default:
		slog.Warn("refusing invalid goal status update", "agent_id", s.agentID, "status", status)
		return
	}
	mu := s.h.goalMutex(s.agentID)
	mu.Lock()
	defer mu.Unlock()
	row, err := s.h.queries.GetAgentGoal(bgCtx(), s.agentID)
	if err != nil {
		slog.Warn("failed to read agent goal for status update", "agent_id", s.agentID, "error", err)
		return
	}
	if !goalPresent(row.GoalObjective) || agent.GoalStatusFromWire(row.GoalStatus) != expected {
		return
	}
	update := agent.GoalUpdate{
		NativeID:  row.GoalNativeID,
		Objective: row.GoalObjective,
		Status:    status,
	}
	if row.GoalCreatedAt.Valid {
		update.CreatedAt = row.GoalCreatedAt.Time
	}
	s.h.applyGoalUpdateFromRow(s.agentID, s.agentProvider, row, update)
}

func (s *agentOutputSink) ClearGoal(snapshot bool) {
	if !s.ownsGoal("ClearGoal") {
		return
	}
	s.h.clearGoal(s.agentID, s.agentProvider, snapshot)
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
