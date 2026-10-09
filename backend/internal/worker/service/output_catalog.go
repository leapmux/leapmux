package service

import (
	"database/sql"
	"errors"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
	"google.golang.org/protobuf/proto"
)

// The marker prevents distinct live comparison pointers from sharing a zero-size address.
type catalogComparisonIdentity struct{ marker byte }

type catalogSource struct {
	sink      *agentOutputSink
	publisher *agentOutputSink
	session   *nativeSessionFact
}

type catalogOperation struct {
	source     catalogSource
	comparison *catalogComparisonIdentity
}

// lockCatalogSource follows the same root lease order as NewSink.
// The caller acquires catalogMu before these leases and releases all locks before callbacks.
func (source catalogSource) lockCatalogSource() func() {
	mutation := source.sink.h.transcriptMutationMutex(source.sink.rootAgentID)
	mutation.RLock()
	source.sink.h.rootSinkMu.RLock()
	return func() {
		source.sink.h.rootSinkMu.RUnlock()
		mutation.RUnlock()
	}
}

func (source catalogSource) currentLocked() bool {
	return source.sink.currentSettingsSource() && source.sink.registeredCurrent() && source.sink.turnPublisherSink() == source.publisher &&
		source.sink.currentMessageSessionFact() == source.session
}

func (sink *agentOutputSink) beginCatalogOperation() *catalogOperation {
	if !sink.isCurrentSettingsSource() {
		return nil
	}
	sink.catalogMu.Lock()
	defer sink.catalogMu.Unlock()
	source := catalogSource{sink: sink, publisher: sink.turnPublisherSink()}
	release := source.lockCatalogSource()
	defer release()
	source.session = sink.currentMessageSessionFact()
	if !source.currentLocked() {
		return nil
	}
	comparison := &catalogComparisonIdentity{marker: 1}
	sink.catalogComparison = comparison
	return &catalogOperation{source: source, comparison: comparison}
}

func (operation *catalogOperation) restart() bool {
	sink := operation.source.sink
	sink.catalogMu.Lock()
	defer sink.catalogMu.Unlock()
	release := operation.source.lockCatalogSource()
	defer release()
	if !operation.source.currentLocked() {
		return false
	}
	operation.comparison = &catalogComparisonIdentity{marker: 1}
	sink.catalogComparison = operation.comparison
	return true
}

func (operation *catalogOperation) currentLocked() bool {
	return operation.source.currentLocked() && operation.source.sink.catalogComparison == operation.comparison
}

func (operation *catalogOperation) state() catalogAdmissionResult {
	sink := operation.source.sink
	sink.catalogMu.Lock()
	defer sink.catalogMu.Unlock()
	release := operation.source.lockCatalogSource()
	defer release()
	if !operation.source.currentLocked() {
		return catalogExpired
	}
	if !operation.currentLocked() {
		return catalogPreparationChanged
	}
	return catalogAdmitted
}

type catalogFailure struct {
	message string
	level   slog.Level
	err     error
}

func (failure catalogFailure) report(agentID string) {
	if failure.err == nil {
		return
	}
	if failure.level == slog.LevelWarn {
		slog.Warn(failure.message, "agent_id", agentID, "error", failure.err)
	} else {
		slog.Error(failure.message, "agent_id", agentID, "error", failure.err)
	}
}

type catalogRequestKind uint8

const (
	catalogActive catalogRequestKind = iota
	catalogConfirmation
	catalogExplicit
)

type catalogRequest struct {
	kind          catalogRequestKind
	sessionID     string
	groups        []*leapmuxv1.AvailableOptionGroup
	startup       bool
	allowClosed   bool
	useRowSession bool
	refresh       optionmap.Map
}

func (request catalogRequest) readFailure() string {
	switch request.kind {
	case catalogActive:
		return "failed to fetch agent for status broadcast"
	case catalogConfirmation:
		return "The Worker failed to read the agent for the catalog broadcast."
	default:
		return "failed to fetch agent for catalog persist"
	}
}

type catalogPreparation struct {
	row       db.Agent
	groups    []*leapmuxv1.AvailableOptionGroup
	marshaled string
	write     bool
	status    *leapmuxv1.AgentStatusChange
	event     *leapmuxv1.AgentEvent
}

// buildStatusChange samples every configured callback without retained service locks.
func (sink *agentOutputSink) buildStatusChange(row db.Agent, status leapmuxv1.AgentStatus, sessionID string) *leapmuxv1.AgentStatusChange {
	// A close can stop the process before it stores ClosedAt.
	// The root's close admission covers that interval and prevents a late active status.
	if status == leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE &&
		(row.ClosedAt.Valid || sink.h.agentClosing != nil && sink.h.agentClosing(sink.rootAgentID)) {
		status = leapmuxv1.AgentStatus_AGENT_STATUS_INACTIVE
	}
	steering, preemption := false, false
	if sink.h.supportsSteering != nil {
		steering = sink.h.supportsSteering(sink.agentID)
	}
	if sink.h.supportsPreemption != nil {
		preemption = sink.h.supportsPreemption(sink.agentID)
	}
	return &leapmuxv1.AgentStatusChange{
		AgentId: sink.agentID, Status: status, AgentSessionId: sessionID, WorkerOnline: true,
		GitStatus: gitutil.GetGitStatus(bgCtx(), row.WorkingDir), AgentProvider: sink.agentProvider,
		OptionGroups:     optionGroupsView(sink.h.agents, &row, nil),
		SupportsSteering: steering, SupportsPreemption: preemption,
	}
}

func cloneCatalogGroups(groups []*leapmuxv1.AvailableOptionGroup) []*leapmuxv1.AvailableOptionGroup {
	if groups == nil {
		return nil
	}
	cloned := make([]*leapmuxv1.AvailableOptionGroup, len(groups))
	for index, group := range groups {
		if group != nil {
			cloned[index] = proto.Clone(group).(*leapmuxv1.AvailableOptionGroup)
		}
	}
	return cloned
}

// prepareCatalog fixes the complete row input and publication before admission.
func (sink *agentOutputSink) prepareCatalog(request catalogRequest, row db.Agent) (catalogPreparation, catalogFailure) {
	prepared := catalogPreparation{row: row}
	if row.ClosedAt.Valid && request.kind != catalogExplicit && !request.allowClosed {
		return prepared, catalogFailure{}
	}
	switch request.kind {
	case catalogActive:
		payloadRow := row
		if request.startup {
			// The status advertises launch values without replacing the user's pending stored choice.
			payloadRow.Options = marshalOptions(mergeOptions(parseOptions(row.Options), request.refresh))
		}
		sessionID := request.sessionID
		if request.useRowSession {
			sessionID = row.AgentSessionID
		}
		prepared.status = sink.buildStatusChange(payloadRow, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, sessionID)
		prepared.status = proto.Clone(prepared.status).(*leapmuxv1.AgentStatusChange)
		prepared.groups = prepared.status.OptionGroups
	case catalogConfirmation:
		current := resolveProviderDefaults(sink.h.agents.Registry(), parseOptions(row.Options), row.AgentProvider)
		prepared.groups = cloneCatalogGroups(overlayOptionGroupCurrents(sink.h.agents.LiveOptionGroups(sink.agentID, row.AgentProvider), current))
	case catalogExplicit:
		prepared.groups = cloneCatalogGroups(request.groups)
	}
	prepared.write = !request.startup && len(prepared.groups) > 0 && !optionGroupsEqual(parseOptionGroups(row.OptionGroups), prepared.groups)
	if request.kind == catalogConfirmation && prepared.write {
		prepared.status = &leapmuxv1.AgentStatusChange{
			AgentId: sink.agentID, AgentProvider: sink.agentProvider, WorkerOnline: true, OptionGroups: prepared.groups,
		}
	}
	if prepared.write {
		marshaled, err := marshalOptionGroups(prepared.groups)
		if err != nil {
			return prepared, catalogFailure{message: "skipping discovered option-group catalog persist; marshal failed", level: slog.LevelWarn, err: err}
		}
		prepared.marshaled = marshaled
	}
	if prepared.status != nil {
		prepared.event = &leapmuxv1.AgentEvent{AgentId: sink.agentID, Event: &leapmuxv1.AgentEvent_StatusChange{StatusChange: prepared.status}}
		if _, err := proto.Marshal(prepared.event); err != nil {
			prepared.event = nil
			return prepared, catalogFailure{message: "skipping discovered option-group catalog persist; marshal failed", level: slog.LevelWarn, err: err}
		}
	}
	return prepared, catalogFailure{}
}

type catalogAdmissionResult uint8

const (
	catalogExpired catalogAdmissionResult = iota
	catalogPreparationChanged
	catalogAdmitted
)

func sameCatalogRowInputs(first, second db.Agent) bool {
	return first.ID == second.ID && first.ParentAgentID == second.ParentAgentID && first.AgentProvider == second.AgentProvider &&
		first.Options == second.Options && first.OptionGroups == second.OptionGroups && first.WorkingDir == second.WorkingDir &&
		first.AgentSessionID == second.AgentSessionID && first.ClosedAt.Valid == second.ClosedAt.Valid &&
		(!first.ClosedAt.Valid || first.ClosedAt.Time.Equal(second.ClosedAt.Time))
}

// admitCatalog performs no callback, marshal, or transport work.
func (sink *agentOutputSink) admitCatalog(operation *catalogOperation, prepared catalogPreparation, request catalogRequest, publishAfterFailure bool) (result catalogAdmissionResult, failure catalogFailure) {
	sink.catalogMu.Lock()
	defer sink.catalogMu.Unlock()
	release := operation.source.lockCatalogSource()
	defer release()
	if !operation.currentLocked() {
		if operation.source.currentLocked() {
			return catalogPreparationChanged, catalogFailure{}
		}
		return catalogExpired, catalogFailure{}
	}
	transaction, err := sink.h.db.BeginTx(bgCtx(), nil)
	if err != nil {
		return catalogExpired, catalogFailure{message: "failed to persist discovered option-group catalog", level: slog.LevelWarn, err: err}
	}
	committed := false
	defer func() {
		if !committed {
			rollbackError := transaction.Rollback()
			if rollbackError != nil && !errors.Is(rollbackError, sql.ErrTxDone) {
				failure.err = errors.Join(failure.err, rollbackError)
				if failure.message == "" {
					failure.message = "failed to persist discovered option-group catalog"
					failure.level = slog.LevelWarn
				}
			}
		}
	}()
	queries := db.New(transaction)
	actual, err := queries.GetAgentByID(bgCtx(), sink.agentID)
	if err != nil {
		return catalogExpired, catalogFailure{message: request.readFailure(), level: slog.LevelError, err: err}
	}
	if !sameCatalogRowInputs(actual, prepared.row) {
		return catalogPreparationChanged, catalogFailure{}
	}
	if prepared.write && !publishAfterFailure {
		if err := queries.SetAgentOptionGroups(bgCtx(), db.SetAgentOptionGroupsParams{ID: sink.agentID, OptionGroups: prepared.marshaled}); err != nil {
			return catalogExpired, catalogFailure{message: "failed to persist discovered option-group catalog", level: slog.LevelWarn, err: err}
		}
	}
	if err := transaction.Commit(); err != nil {
		return catalogExpired, catalogFailure{message: "failed to persist discovered option-group catalog", level: slog.LevelWarn, err: err}
	}
	committed = true
	if prepared.event != nil {
		sink.h.watcher.EnqueueAgentEvent(sink.agentID, prepared.event)
	}
	return catalogAdmitted, catalogFailure{}
}

func (sink *agentOutputSink) runCatalogOperation(operation *catalogOperation, request catalogRequest, suppliedRow *db.Agent) *leapmuxv1.AgentStatusChange {
	if operation == nil {
		return nil
	}
	for {
		state := operation.state()
		if state == catalogExpired {
			return nil
		}
		if state == catalogPreparationChanged && !operation.restart() {
			return nil
		}
		var row db.Agent
		var err error
		if suppliedRow != nil {
			row = *suppliedRow
			suppliedRow = nil
		} else {
			row, err = sink.h.queries.GetAgentByID(bgCtx(), sink.agentID)
		}
		if err != nil {
			(catalogFailure{message: request.readFailure(), level: slog.LevelError, err: err}).report(sink.agentID)
			return nil
		}
		prepared, failure := sink.prepareCatalog(request, row)
		if failure.err != nil {
			failure.report(sink.agentID)
			return nil
		}
		result, failure := sink.admitCatalog(operation, prepared, request, false)
		if failure.err != nil {
			failure.report(sink.agentID)
			if prepared.event != nil {
				// A logger can replace the source or start a nested comparison.
				// Admit publication again without repeating the failed catalog write.
				accepted, publicationFailure := sink.admitCatalog(operation, prepared, request, true)
				publicationFailure.report(sink.agentID)
				if accepted == catalogAdmitted {
					return prepared.status
				}
			}
			return nil
		}
		if result == catalogPreparationChanged {
			if !operation.restart() {
				return nil
			}
			continue
		}
		if result == catalogAdmitted {
			return prepared.status
		}
		return nil
	}
}

func (sink *agentOutputSink) persistCatalogAndBuildStatus(sessionID string) *leapmuxv1.AgentStatusChange {
	return sink.runCatalogOperation(sink.beginCatalogOperation(), catalogRequest{kind: catalogActive, sessionID: sessionID}, nil)
}

func (sink *agentOutputSink) buildChangedCatalogStatus() *leapmuxv1.AgentStatusChange {
	if sink.h.agents == nil {
		return nil
	}
	return sink.runCatalogOperation(sink.beginCatalogOperation(), catalogRequest{kind: catalogConfirmation}, nil)
}

func (sink *agentOutputSink) persistLiveCatalog(groups []*leapmuxv1.AvailableOptionGroup) {
	sink.runCatalogOperation(sink.beginCatalogOperation(), catalogRequest{kind: catalogExplicit, groups: cloneCatalogGroups(groups)}, nil)
}

func (sink *agentOutputSink) persistCatalogIfChanged(existing db.Agent, groups []*leapmuxv1.AvailableOptionGroup) {
	sink.runCatalogOperation(sink.beginCatalogOperation(), catalogRequest{kind: catalogExplicit, groups: cloneCatalogGroups(groups)}, &existing)
}

func (sink *agentOutputSink) broadcastChangedCatalog() {
	if sink.buildChangedCatalogStatus() != nil {
		sink.h.watcher.DrainAgentEvents(sink.agentID)
	}
}

func (sink *agentOutputSink) BroadcastStatusActive(sessionID string) {
	if sink.persistCatalogAndBuildStatus(sessionID) != nil {
		sink.h.watcher.DrainAgentEvents(sink.agentID)
	}
}

type settingsRefreshSettlement struct {
	row            db.Agent
	startupWindow  bool
	optionsChanged bool
	operation      *catalogOperation
}

type settingsRefreshPreparation struct {
	row     db.Agent
	refresh optionmap.Map
	startup bool
	options string
	changed bool
}

func (sink *agentOutputSink) PersistSettingsRefresh(refresh optionmap.Map) {
	settlement, accepted := sink.settleSettingsRefresh(refresh)
	if !accepted {
		return
	}
	if !settlement.optionsChanged {
		if !settlement.startupWindow && sink.h.agents != nil {
			if sink.runCatalogOperation(settlement.operation, catalogRequest{kind: catalogConfirmation}, nil) != nil {
				sink.h.watcher.DrainAgentEvents(sink.agentID)
			}
		}
		return
	}
	request := catalogRequest{kind: catalogActive, sessionID: settlement.row.AgentSessionID, startup: settlement.startupWindow, allowClosed: true, useRowSession: true, refresh: refresh.Clone()}
	if sink.runCatalogOperation(settlement.operation, request, nil) != nil {
		sink.h.watcher.DrainAgentEvents(sink.agentID)
	}
}

// settleSettingsRefresh samples startup outside every source and comparison lock.
// Its original query handle keeps actual compare-and-swap adapters and errors intact.
func (sink *agentOutputSink) settleSettingsRefresh(refresh optionmap.Map) (settlement settingsRefreshSettlement, accepted bool) {
	operation := sink.beginCatalogOperation()
	if operation == nil {
		return settingsRefreshSettlement{}, false
	}
	refresh = refresh.Clone()
	for {
		state := operation.state()
		if state == catalogExpired {
			return settingsRefreshSettlement{}, false
		}
		if state == catalogPreparationChanged && !operation.restart() {
			return settingsRefreshSettlement{}, false
		}
		row, err := sink.h.queries.GetAgentByID(bgCtx(), sink.agentID)
		if err != nil {
			(catalogFailure{message: "failed to fetch agent for settings broadcast", level: slog.LevelError, err: err}).report(sink.agentID)
			return settingsRefreshSettlement{}, false
		}
		startup := sink.h.agentStarting != nil && sink.h.agentStarting(sink.agentID)
		var startupOptions string
		var startupChanged bool
		if startup {
			stored := parseOptions(row.Options)
			startupOptions = marshalOptions(mergeOptions(stored, refresh))
			startupChanged = startupOptions != marshalOptions(stored)
		}
		prepared := settingsRefreshPreparation{row: row, refresh: refresh, startup: startup, options: startupOptions, changed: startupChanged}
		result, settled, failure, changed := sink.admitSettingsRefresh(operation, prepared)
		result.report(sink.agentID)
		if failure.err != nil {
			failure.report(sink.agentID)
			return settingsRefreshSettlement{}, false
		}
		if changed {
			if !operation.restart() {
				return settingsRefreshSettlement{}, false
			}
			continue
		}
		if settled.operation == nil {
			return settingsRefreshSettlement{}, false
		}
		return settled, true
	}
}

func (sink *agentOutputSink) admitSettingsRefresh(operation *catalogOperation, prepared settingsRefreshPreparation) (optionsCASResult, settingsRefreshSettlement, catalogFailure, bool) {
	sink.catalogMu.Lock()
	defer sink.catalogMu.Unlock()
	release := operation.source.lockCatalogSource()
	defer release()
	if !operation.currentLocked() {
		return optionsCASResult{}, settingsRefreshSettlement{}, catalogFailure{}, operation.source.currentLocked()
	}
	actual, err := sink.h.queries.GetAgentByID(bgCtx(), sink.agentID)
	if err != nil {
		return optionsCASResult{}, settingsRefreshSettlement{}, catalogFailure{message: "failed to fetch agent for settings broadcast", level: slog.LevelError, err: err}, false
	}
	if !sameCatalogRowInputs(actual, prepared.row) {
		return optionsCASResult{}, settingsRefreshSettlement{}, catalogFailure{}, true
	}
	settlement := settingsRefreshSettlement{row: prepared.row, startupWindow: prepared.startup, operation: operation}
	if prepared.startup {
		settlement.row.Options = prepared.options
		settlement.optionsChanged = prepared.changed
		return optionsCASResult{}, settlement, catalogFailure{}, false
	}
	result, err := sink.casPersistOptions(prepared.row.Options, prepared.refresh)
	if err != nil {
		return result, settingsRefreshSettlement{}, catalogFailure{message: "failed to persist refreshed settings", level: slog.LevelError, err: err}, false
	}
	settlement.row.Options = result.options
	settlement.optionsChanged = result.wrote
	return result, settlement, catalogFailure{}, false
}
