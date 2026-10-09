package service

import (
	"context"
	"log/slog"
	"slices"
	"sync"

	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/util/validate"
)

// Each agent stores one session goal in its agents row.
// Durable changes update the card. Goal transitions also produce a neutral notification.
// Progress counters remain ephemeral and use the sink's existing session-info cache.

// goalMutex serializes each goal comparison, storage write, and publication admission.
// It does not surround provider work, notification persistence, logging, or watcher delivery.
func (h *OutputHandler) goalMutex(agentID string) *sync.Mutex {
	v, _ := h.goalMu.LoadOrStore(agentID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// goalPresent supplies the same presence rule for storage and projection.
// An identity without a readable objective does not represent a goal.
func goalPresent(objective string) bool { return objective != "" }

// goalChange carries either a complete report or a conditional status change.
// A conditional change reads its objective and identity from the actual stored row.
type goalChange struct {
	update         agent.GoalUpdate
	expectedStatus *agent.GoalStatus
}

func (change goalChange) readFailure() string {
	if change.expectedStatus != nil {
		return "failed to read agent goal for status update"
	}
	if !goalPresent(change.update.Objective) {
		return "failed to fetch agent for goal clear"
	}
	return "failed to fetch agent for goal update"
}

// applyGoalUpdate preserves one original source and notification context across preparation attempts.
// Only a concurrent change to the stored inputs repeats preparation. Storage errors return immediately.
func (h *OutputHandler) applyGoalUpdate(operation goalOperation, change goalChange) {
	for {
		if !operation.admission.isCurrent() {
			return
		}
		actions := h.SupportedGoalActions(operation.admission.agentID)
		row, current, err := operation.admission.readGoal()
		if err != nil {
			logGoalFailure(operation.admission.agentID, goalFailure{message: change.readFailure(), err: err})
			return
		}
		if !current {
			return
		}
		update := change.update
		matches := change.expectedStatus == nil ||
			(goalPresent(row.GoalObjective) && agent.GoalStatus(row.GoalStatus) == *change.expectedStatus)
		if change.expectedStatus != nil && matches {
			update.NativeID = row.GoalNativeID
			update.Objective = row.GoalObjective
			if row.GoalCreatedAt.Valid {
				update.CreatedAt = row.GoalCreatedAt.Time
			}
		}
		prepared := goalPreparation{previous: row, next: row}
		if matches {
			var failure goalFailure
			prepared, failure = h.applyGoalUpdateFromRow(operation, row, update, actions)
			if failure.err != nil {
				logGoalFailure(operation.admission.agentID, failure)
				return
			}
		}
		result, failure := h.commitGoalPreparation(operation.admission, prepared, change.readFailure())
		if failure.err != nil {
			logGoalFailure(operation.admission.agentID, failure)
			return
		}
		if result == goalPrepareAgain {
			continue
		}
		if result == goalCommitted {
			h.drainGoalPublications(operation.admission.agentID)
		}
		return
	}
}

// applyGoalUpdateFromRow prepares immutable data outside the goal and root mutation locks.
// A detail change updates the card but does not announce another goal transition.
func (h *OutputHandler) applyGoalUpdateFromRow(operation goalOperation, row db.GetAgentGoalRow, update agent.GoalUpdate, actions []leapmuxv1.AgentGoalAction) (goalPreparation, goalFailure) {
	if !goalPresent(update.Objective) {
		return h.clearGoal(operation, row, update.Snapshot, actions)
	}
	sameIdentity := sameGoalIdentity(row, update)
	replaced := row.GoalObjective != update.Objective || !sameIdentity
	transition := replaced || row.GoalStatus != leapmuxv1.AgentGoalStatus(update.Status)
	detailChanged := row.GoalStatusDetail != update.StatusDetail
	nativeID := update.NativeID
	if nativeID == "" && !replaced {
		nativeID = row.GoalNativeID
	}
	identityAdded := nativeID != row.GoalNativeID
	prepared := goalPreparation{previous: row, next: row, clearProgress: replaced}
	if transition || detailChanged || identityAdded {
		now := sqltime.FloorMillis(h.now())
		createdAt := row.GoalCreatedAt
		if !update.CreatedAt.IsZero() {
			createdAt = sqltime.SQLiteNullTime{Time: sqltime.FloorMillis(update.CreatedAt), Valid: true}
		} else if !createdAt.Valid || replaced {
			// An absent native creation time still requires a stable stored identity.
			createdAt = sqltime.SQLiteNullTime{Time: now, Valid: true}
		}
		prepared.write = true
		prepared.next.GoalNativeID = nativeID
		prepared.next.GoalObjective = update.Objective
		prepared.next.GoalStatus = leapmuxv1.AgentGoalStatus(update.Status)
		prepared.next.GoalStatusDetail = update.StatusDetail
		prepared.next.GoalCreatedAt = createdAt
		// Storage and publication use this same timestamp.
		prepared.next.GoalUpdatedAt = sqltime.SQLiteNullTime{Time: now, Valid: true}
		prepared.publication.goal = GoalChangedEvent(operation.admission.agentID, GoalSnapshot{
			Goal:      goalProto(goalColumnsOfRow(prepared.next)),
			UpdatedAt: goalStamp(prepared.next.GoalUpdatedAt),
		}, actions)
		if transition && !update.Snapshot {
			notification, err := operation.captureNotification(map[string]interface{}{
				contracts.NotificationFieldType:           contracts.NotificationTypeGoalUpdated,
				contracts.NotificationFieldObjective:      update.Objective,
				contracts.NotificationFieldGoalStatus:     agent.GoalStatusWire(update.Status),
				contracts.NotificationFieldStatusDetail:   update.StatusDetail,
				contracts.NotificationFieldGoalTransition: goalTransitionKind(row, update),
			})
			if err != nil {
				return goalPreparation{}, goalFailure{message: "marshal notification content", err: err}
			}
			prepared.publication.notification = &notification
		}
	}
	prepared.publication.admission = operation.admission
	prepared.publication.identity = goalColumnsOfRow(prepared.next)
	if err := prepared.publication.prepareProgress(operation.admission.agentID, update); err != nil {
		return goalPreparation{}, goalFailure{message: "The Worker failed to prepare goal progress.", err: err}
	}
	return prepared, goalFailure{}
}

// clearGoal always prepares the complete empty columns, including the clear stamp.
// An already absent goal advances storage but produces no event or notification.
func (h *OutputHandler) clearGoal(operation goalOperation, row db.GetAgentGoalRow, snapshot bool, actions []leapmuxv1.AgentGoalAction) (goalPreparation, goalFailure) {
	prepared := goalPreparation{previous: row, next: row, write: true, clear: true, clearProgress: true}
	prepared.next.GoalNativeID = ""
	prepared.next.GoalObjective = ""
	prepared.next.GoalStatus = leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_UNSPECIFIED
	prepared.next.GoalStatusDetail = ""
	prepared.next.GoalCreatedAt = sqltime.SQLiteNullTime{}
	prepared.next.GoalUpdatedAt = sqltime.SQLiteNullTime{Time: sqltime.FloorMillis(h.now()), Valid: true}
	if !goalPresent(row.GoalObjective) {
		return prepared, goalFailure{}
	}
	prepared.publication.admission = operation.admission
	prepared.publication.goal = GoalChangedEvent(operation.admission.agentID, GoalSnapshot{
		UpdatedAt: goalStamp(prepared.next.GoalUpdatedAt),
	}, actions)
	if !snapshot {
		notification, err := operation.captureNotification(map[string]interface{}{
			contracts.NotificationFieldType:      contracts.NotificationTypeGoalCleared,
			contracts.NotificationFieldObjective: row.GoalObjective,
		})
		if err != nil {
			return goalPreparation{}, goalFailure{message: "marshal notification content", err: err}
		}
		prepared.publication.notification = &notification
	}
	return prepared, goalFailure{}
}

// goalTransitionKind compares the previous stored row with the current report.
// The notification carries this result because its resulting status alone cannot identify a replacement.
func goalTransitionKind(row db.GetAgentGoalRow, update agent.GoalUpdate) string {
	switch {
	case !goalPresent(row.GoalObjective):
		return contracts.GoalTransitionSet
	case row.GoalObjective != update.Objective || !sameGoalIdentity(row, update):
		return contracts.GoalTransitionReplaced
	case update.Status == agent.GoalStatusActive:
		return contracts.GoalTransitionResumed
	case update.Status == agent.GoalStatusPaused:
		return contracts.GoalTransitionPaused
	case update.Status == agent.GoalStatusDone:
		return contracts.GoalTransitionAchieved
	case update.Status == agent.GoalStatusUnknown:
		return contracts.GoalTransitionUpdated
	default:
		return contracts.GoalTransitionBlocked
	}
}

// Native identities are authoritative. Creation time distinguishes providers without an ID.
func sameGoalIdentity(stored db.GetAgentGoalRow, update agent.GoalUpdate) bool {
	if stored.GoalNativeID != "" && update.NativeID != "" {
		return stored.GoalNativeID == update.NativeID
	}
	if update.CreatedAt.IsZero() {
		return true
	}
	return stored.GoalCreatedAt.Valid && stored.GoalCreatedAt.Time.Equal(sqltime.FloorMillis(update.CreatedAt))
}

// goalProgressInfo copies each present counter and preserves the distinction between absent and zero.
// It returns nil when the provider reports no counter.
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

// GoalChangedEvent builds one independent snapshot from supplied data.
// It reads no provider or stored state. A nil goal retains its clear stamp.
func GoalChangedEvent(agentID string, snapshot GoalSnapshot, supportedActions []leapmuxv1.AgentGoalAction) *leapmuxv1.AgentEvent {
	var goal *leapmuxv1.AgentGoal
	if snapshot.Goal != nil {
		goal = proto.Clone(snapshot.Goal).(*leapmuxv1.AgentGoal)
	}
	return &leapmuxv1.AgentEvent{
		AgentId: agentID,
		Event: &leapmuxv1.AgentEvent_GoalChanged{
			GoalChanged: &leapmuxv1.AgentGoalChanged{
				AgentId:          agentID,
				Goal:             goal,
				SupportedActions: slices.Clone(supportedActions),
				GoalUpdatedAt:    snapshot.UpdatedAt,
			},
		},
	}
}

// goalStamp supplies a fixed-width UTC timestamp, or an empty string when no stamp exists.
// The browser compares these strings directly.
func goalStamp(updatedAt sqltime.SQLiteNullTime) string {
	if !updatedAt.Valid {
		return ""
	}
	return timefmt.Format(updatedAt.Time)
}

// goalProto builds the goal, or nil when the objective is absent.
// The event carries supported actions and the stamp even when the goal is nil.
func goalProto(columns GoalColumns) *leapmuxv1.AgentGoal {
	if !goalPresent(columns.Objective) {
		return nil
	}
	goal := &leapmuxv1.AgentGoal{
		// SQLite retains invalid bytes from old writes. Repair them before protobuf serialization.
		// One invalid string prevents serialization of the complete response.
		NativeId:     validate.WireString(columns.NativeID),
		Objective:    validate.WireString(columns.Objective),
		Status:       agent.GoalStatusToProto(columns.Status),
		StatusDetail: validate.WireString(columns.StatusDetail),
	}
	if columns.CreatedAt.Valid {
		goal.CreatedAt = timefmt.Format(columns.CreatedAt.Time)
	}
	return goal
}

// SupportedGoalActions reads the running provider outside mutation and publication locks.
// An absent process or a provider without goal controls returns no actions.
func (h *OutputHandler) SupportedGoalActions(agentID string) []leapmuxv1.AgentGoalAction {
	actions := h.agents.SupportedGoalActions(agentID)
	if len(actions) == 0 {
		return nil
	}
	out := make([]leapmuxv1.AgentGoalAction, 0, len(actions))
	for _, action := range actions {
		out = append(out, agent.GoalActionToProto(action))
	}
	return out
}

// publishGoalCapabilities preserves the direct process-exit projection path.
// It writes no goal columns, notification, progress value, or stamp.
func (h *OutputHandler) publishGoalCapabilities(agentID string) {
	h.publishGoalCapabilitiesFor(h.captureGoalProjection(agentID))
}

func (h *OutputHandler) publishGoalCapabilitiesFor(operation goalOperation) {
	const readFailure = "failed to read agent goal for capability broadcast"
	for {
		if !operation.admission.isCurrent() {
			return
		}
		actions := h.SupportedGoalActions(operation.admission.agentID)
		row, current, err := operation.admission.readGoal()
		if err != nil {
			logGoalFailure(operation.admission.agentID, goalFailure{message: readFailure, err: err})
			return
		}
		if !current {
			return
		}
		prepared := goalPreparation{previous: row, next: row, capabilities: &actions}
		prepared.publication.admission = operation.admission
		result, failure := h.commitGoalPreparation(operation.admission, prepared, readFailure)
		if failure.err != nil {
			logGoalFailure(operation.admission.agentID, failure)
			return
		}
		if result == goalPrepareAgain {
			continue
		}
		if result == goalCommitted {
			h.drainGoalPublications(operation.admission.agentID)
		}
		return
	}
}

// GoalSnapshot keeps the optional goal and its ordering stamp together.
// A cleared goal requires its stamp to reject an older answer.
type GoalSnapshot struct {
	Goal      *leapmuxv1.AgentGoal
	UpdatedAt string
}

// LoadGoal projects the stored goal. A child agent owns no goal.
func (h *OutputHandler) LoadGoal(ctx context.Context, agentID string) (GoalSnapshot, error) {
	row, err := h.queries.GetAgentGoal(ctx, agentID)
	if err != nil {
		return GoalSnapshot{}, err
	}
	return h.GoalSnapshotFrom(goalColumnsOfRow(row)), nil
}

// GoalSnapshotFrom projects the row that the caller already holds.
// It reads process liveness but changes no stored status or stamp.
func (h *OutputHandler) GoalSnapshotFrom(cols GoalColumns) GoalSnapshot {
	if cols.IsChild {
		return GoalSnapshot{}
	}
	if goalPresent(cols.Objective) && !h.agentAlive(cols.AgentID) {
		// Dormant is derived from the running-agent map. The last reported status remains stored.
		// The stored detail describes work that no process now performs.
		cols.Status, cols.StatusDetail = agent.GoalStatusDormant, ""
	}
	return GoalSnapshot{Goal: goalProto(cols), UpdatedAt: goalStamp(cols.UpdatedAt)}
}

// agentAlive reports whether a live process serves the agent.
func (h *OutputHandler) agentAlive(agentID string) bool { return h.agents.AgentAlive(agentID) }

// GoalColumns keeps the goal columns together without interchangeable positional arguments.
// Each adapter retains the row's agent identity for the liveness projection.
type GoalColumns struct {
	NativeID     string
	AgentID      string
	IsChild      bool
	Objective    string
	Status       agent.GoalStatus
	StatusDetail string
	CreatedAt    sqltime.SQLiteNullTime
	UpdatedAt    sqltime.SQLiteNullTime
}

// GoalColumnsOfAgent reads the goal columns from the complete agents row.
func GoalColumnsOfAgent(row db.Agent) GoalColumns {
	return GoalColumns{
		NativeID:     row.GoalNativeID,
		AgentID:      row.ID,
		IsChild:      row.ParentAgentID.Valid,
		Objective:    row.GoalObjective,
		Status:       agent.GoalStatus(row.GoalStatus),
		StatusDetail: row.GoalStatusDetail,
		CreatedAt:    row.GoalCreatedAt,
		UpdatedAt:    row.GoalUpdatedAt,
	}
}

// goalColumnsOfRow reads the goal columns from the narrow goal query.
func goalColumnsOfRow(row db.GetAgentGoalRow) GoalColumns {
	return GoalColumns{
		NativeID:     row.GoalNativeID,
		AgentID:      row.ID,
		IsChild:      row.ParentAgentID.Valid,
		Objective:    row.GoalObjective,
		Status:       agent.GoalStatus(row.GoalStatus),
		StatusDetail: row.GoalStatusDetail,
		CreatedAt:    row.GoalCreatedAt,
		UpdatedAt:    row.GoalUpdatedAt,
	}
}

func (s *agentOutputSink) UpsertGoal(update agent.GoalUpdate) {
	if !s.ownsGoal("UpsertGoal") {
		return
	}
	// Copy counters before provider work or watcher delivery can change the supplied pointers.
	update = cloneGoalUpdate(update).Clean()
	s.h.applyGoalUpdate(s.captureGoalOperation(), goalChange{update: update})
}

func (s *agentOutputSink) PublishGoalCapabilities() {
	if !s.ownsGoal("PublishGoalCapabilities") {
		return
	}
	s.h.publishGoalCapabilitiesFor(s.captureGoalOperation())
}

func (s *agentOutputSink) UpdateGoalStatus(expected, status agent.GoalStatus) {
	if !s.ownsGoal("UpdateGoalStatus") {
		return
	}
	switch status {
	case agent.GoalStatusActive, agent.GoalStatusPaused, agent.GoalStatusBlocked, agent.GoalStatusDone, agent.GoalStatusUnknown:
	default:
		slog.Warn("refusing invalid goal status update", "agent_id", s.agentID, "status", status)
		return
	}
	s.h.applyGoalUpdate(s.captureGoalOperation(), goalChange{
		update:         agent.GoalUpdate{Status: status},
		expectedStatus: &expected,
	})
}

func (s *agentOutputSink) ClearGoal(snapshot bool) {
	if !s.ownsGoal("ClearGoal") {
		return
	}
	s.h.applyGoalUpdate(s.captureGoalOperation(), goalChange{update: agent.GoalUpdate{Snapshot: snapshot}})
}

// ownsGoal refuses a child before any provider read or state preparation.
// A child transcript must not replace its root's session goal.
func (s *agentOutputSink) ownsGoal(op string) bool {
	if s.agentID == s.rootAgentID {
		return true
	}
	slog.Warn("refusing session-goal write from a child sink",
		"op", op, "agent_id", s.agentID, "root_agent_id", s.rootAgentID)
	return false
}

func cloneGoalUpdate(update agent.GoalUpdate) agent.GoalUpdate {
	update.TokensUsed = cloneGoalCounter(update.TokensUsed)
	update.TokenBudget = cloneGoalCounter(update.TokenBudget)
	update.TimeUsedSeconds = cloneGoalCounter(update.TimeUsedSeconds)
	update.Iterations = cloneGoalCounter(update.Iterations)
	return update
}

func cloneGoalCounter[T int64 | int32](counter *T) *T {
	if counter == nil {
		return nil
	}
	value := *counter
	return &value
}
