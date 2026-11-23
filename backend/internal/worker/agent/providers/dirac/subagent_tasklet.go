package dirac

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

const diracOutcomeMismatchGrace = 5 * time.Second

type diracOutcomeMismatchState struct {
	firstSeen     time.Time
	lifecycleID   string
	nativeOutcome json.RawMessage
}

type diracTaskletWatch struct {
	sessionID  string
	stop       <-chan struct{}
	observed   map[string]string
	writers    map[string]*diracTaskletTranscript
	mismatches map[string]diracOutcomeMismatchState
	// childMu protects observed cards and the retirement check. A new native
	// update cannot disappear while a completed poll closes its last row.
	cards   map[string]struct{}
	changed bool
}

func (a *Agent) observeTaskletCard(updateType string, raw json.RawMessage) {
	if updateType != contracts.ACPUpdateToolCall && updateType != contracts.ACPUpdateToolCallUpdate {
		return
	}
	var call struct {
		Name string `json:"name"`
		ID   string `json:"toolCallId"`
	}
	profile, _ := a.archiveProfile()
	if json.Unmarshal(raw, &call) != nil || call.ID == "" || profile == "" || call.Name != "" && call.Name != contracts.DiracToolSubagents {
		return
	}
	sessionID := a.CurrentSessionID()
	if sessionID == "" {
		return
	}
	a.childMu.Lock()
	if a.taskletWatch != nil && a.taskletWatch.sessionID == sessionID {
		watch := a.taskletWatch
		if call.Name == "" {
			if _, known := watch.cards[call.ID]; !known {
				a.childMu.Unlock()
				return
			}
		}
		watch.cards[call.ID] = struct{}{}
		watch.changed = true
		a.childMu.Unlock()
		return
	}
	if call.Name != contracts.DiracToolSubagents {
		a.childMu.Unlock()
		return
	}
	if a.childStop == nil {
		a.childStop = make(chan struct{})
	}
	watch := &diracTaskletWatch{sessionID: sessionID, stop: a.childStop, observed: make(map[string]string), writers: make(map[string]*diracTaskletTranscript), cards: map[string]struct{}{call.ID: {}}}
	a.taskletWatch = watch
	a.archiveWG.Add(1)
	a.childMu.Unlock()
	go a.watchDiracTasklets(watch)
}

func (a *Agent) watchDiracTasklets(watch *diracTaskletWatch) {
	defer a.archiveWG.Done()
	defer func() {
		a.childMu.Lock()
		if a.taskletWatch == watch {
			a.taskletWatch = nil
		}
		a.childMu.Unlock()
	}()
	var lastError string
	delay := diracArchiveRetryInterval
	for {
		a.childMu.Lock()
		watch.changed = false
		a.childMu.Unlock()
		finished, hasMismatch, mismatchDeadline, err := a.pollDiracTaskletsPass(watch, false)
		if !a.diracTaskletWatchCurrent(watch) {
			return
		}
		if err == nil && finished {
			a.childMu.Lock()
			retire := a.taskletWatch == watch && !watch.changed
			if retire {
				a.taskletWatch = nil
			}
			a.childMu.Unlock()
			if retire {
				return
			}
			continue
		}
		if err != nil && lastError != err.Error() {
			lastError = err.Error()
			slog.Warn("read the native Dirac tasklets", "agent_id", a.AgentID(), "error", err)
		}
		interval := diracArchiveRetryInterval
		if err != nil {
			interval = delay
			delay = min(delay*2, diracArchiveRetryMax)
		} else if hasMismatch {
			interval = delay
			delay = min(delay*2, diracArchiveRetryMax)
			if !mismatchDeadline.IsZero() {
				remaining := mismatchDeadline.Sub(a.archiveClock().Now())
				if remaining <= 0 {
					continue
				}
				if remaining < interval {
					interval = remaining
				}
			}
		} else {
			delay = diracArchiveRetryInterval
		}
		timer := a.archiveClock().NewTimer(interval, diracArchiveTimerTag)
		select {
		case <-a.Context().Done():
			timer.Stop()
			// The process can commit its outcome after the last poll. Native
			// committed state must reach storage before generic exit cleanup.
			if _, _, _, err := a.pollDiracTaskletsPass(watch, true); err != nil {
				slog.Warn("read the final native Dirac tasklets", "agent_id", a.AgentID(), "error", err)
			}
			return
		case <-watch.stop:
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (a *Agent) pollDiracTasklets(watch *diracTaskletWatch) (bool, error) {
	finished, _, _, err := a.pollDiracTaskletsPass(watch, false)
	return finished, err
}

func (a *Agent) pollDiracTaskletsPass(watch *diracTaskletWatch, forceSettleMismatch bool) (bool, bool, time.Time, error) {
	if !a.diracTaskletWatchCurrent(watch) {
		return false, false, time.Time{}, nil
	}
	profile, _ := a.archiveProfile()
	metadata, err := readDiracTaskletMetadata(profile, watch.sessionID)
	if err != nil {
		return false, false, time.Time{}, err
	}
	defer a.FlushRegistryObservations()
	a.childMu.Lock()
	finished := len(watch.cards) != 0 || len(metadata.Tasklets) != 0
	for cardID := range watch.cards {
		finished = finished && metadata.Cards[cardID]
	}
	a.childMu.Unlock()
	now := a.archiveClock().Now()
	if watch.mismatches == nil {
		watch.mismatches = make(map[string]diracOutcomeMismatchState)
	}
	seenKeys := make(map[string]struct{}, len(metadata.Tasklets))
	hasMismatch := false
	var earliestMismatchDeadline time.Time
	for _, tasklet := range metadata.Tasklets {
		key := diracTaskletKey(watch.sessionID, tasklet.Record.ID, tasklet.Record.ConversationID)
		seenKeys[key] = struct{}{}
		if tasklet.Record.outcomeMismatch && tasklet.Record.unmatchedOutcome != nil {
			prev, tracked := watch.mismatches[key]
			if !tracked || prev.lifecycleID != tasklet.Record.lifecycle.ID || !sameDiracNativeOutcome(prev.nativeOutcome, tasklet.Record.NativeOutcome) {
				prev = diracOutcomeMismatchState{
					firstSeen:     now,
					lifecycleID:   tasklet.Record.lifecycle.ID,
					nativeOutcome: tasklet.Record.NativeOutcome,
				}
				watch.mismatches[key] = prev
			}
			deadline := prev.firstSeen.Add(diracOutcomeMismatchGrace)
			if forceSettleMismatch || !now.Before(deadline) {
				tasklet.Record.Outcome = tasklet.Record.unmatchedOutcome
				tasklet.Record.outcomeMismatch = false
			} else {
				hasMismatch = true
				if earliestMismatchDeadline.IsZero() || deadline.Before(earliestMismatchDeadline) {
					earliestMismatchDeadline = deadline
				}
			}
		} else {
			delete(watch.mismatches, key)
		}
		finished = finished && tasklet.Record.Outcome != nil
		if err := a.publishDiracTasklet(watch, tasklet); err != nil {
			return false, hasMismatch, earliestMismatchDeadline, err
		}
	}
	for key := range watch.mismatches {
		if _, exists := seenKeys[key]; !exists {
			delete(watch.mismatches, key)
		}
	}
	return finished, hasMismatch, earliestMismatchDeadline, nil
}

func (a *Agent) diracTaskletWatchCurrent(watch *diracTaskletWatch) bool {
	a.childMu.Lock()
	current := a.taskletWatch == watch
	a.childMu.Unlock()
	return current && watch.sessionID != "" && a.CurrentSessionID() == watch.sessionID
}

func (a *Agent) publishDiracTasklet(watch *diracTaskletWatch, tasklet diracStoredTasklet) error {
	if !a.diracTaskletWatchCurrent(watch) {
		return agent.ErrChildRouteNotReady
	}
	record := tasklet.Record
	key := diracTaskletKey(watch.sessionID, record.ID, record.ConversationID)
	fingerprintBytes, err := json.Marshal(struct {
		LifecycleID string
		Record      diracTaskletRecord
	}{record.lifecycle.ID, record})
	if err != nil {
		return err
	}
	digest := sha256.Sum256(fingerprintBytes)
	fingerprint := hex.EncodeToString(digest[:])
	if record.Outcome != nil && watch.observed[key] == fingerprint {
		return nil
	}
	writer := watch.writers[key]
	if writer != nil && writer.lifecycleID != record.lifecycle.ID {
		if record.lifecycle.State != "resumed" && (writer.lifecycleID != "" || record.lifecycle.State != "accepted") {
			return fmt.Errorf("the Dirac tasklet replaced its native lifetime without a resumption")
		}
		writer = nil
	}
	if writer != nil && record.Outcome == nil && watch.observed[key] != "" {
		return agent.ErrChildRouteNotReady
	}
	if writer == nil {
		err := a.WithSessionID(func(sessionID string) error {
			if sessionID != watch.sessionID || !a.diracTaskletWatchCurrent(watch) {
				return agent.ErrChildRouteNotReady
			}
			a.ApplySubagentObservation(&acp.SubagentObservation{
				RowKey: key, ChildAgentKey: key, ChildSpawnSpanID: tasklet.SpawnCardID,
				ChildAgentSessionID: record.ConversationID, Title: *record.Title, Prompt: *record.Prompt,
				Status: bgtask.StatusRunning, Spawns: true,
			})
			return nil
		})
		if err != nil {
			return err
		}
		target := acp.ChildSessionTarget{RootSessionID: watch.sessionID, SessionID: record.ConversationID, RowKey: key}
		if record.lifecycle.State == "resumed" && record.Outcome == nil {
			if err := a.ReviveChildRow(target); err != nil {
				return err
			}
		}
		err = a.WithChildTranscript(target, func(services agent.ProviderServices) error {
			var captureErr error
			writer, captureErr = captureDiracTaskletTranscript(services, record.ConversationID)
			if writer != nil {
				writer.lifecycleID = record.lifecycle.ID
			}
			return captureErr
		})
		if err != nil {
			return err
		}
		watch.writers[key] = writer
		delete(watch.observed, key)
	}
	profile, _ := a.archiveProfile()
	messages, presentation, err := readDiracTaskletHistory(profile, record)
	if err != nil {
		return err
	}
	tools, err := diracTaskletTranscriptRecords(watch.sessionID, record, messages, presentation)
	if err != nil {
		return err
	}
	if err := writer.publish(tools); err != nil {
		return err
	}
	if err := agent.FlushPendingOutput(writer); err != nil {
		return err
	}
	if record.Outcome == nil {
		return nil
	}
	desiredStatus := record.Outcome.status()
	if record.lifecycle.State == "resumed" {
		if sink := a.Sink(); sink != nil {
			_, currentStatus, found, lookupErr := sink.LookupBackgroundTask(key)
			if lookupErr != nil {
				return lookupErr
			}
			if found && currentStatus.IsFinished() && currentStatus != desiredStatus {
				if !a.diracTaskletWatchCurrent(watch) {
					return agent.ErrChildRouteNotReady
				}
				target := acp.ChildSessionTarget{RootSessionID: watch.sessionID, SessionID: record.ConversationID, RowKey: key}
				if err := a.ReviveChildRow(target); err != nil {
					return err
				}
			}
		}
	}
	err = a.WithSessionID(func(sessionID string) error {
		if sessionID != watch.sessionID || !a.diracTaskletWatchCurrent(watch) {
			return agent.ErrChildRouteNotReady
		}
		observation := &acp.SubagentObservation{RowKey: key, Status: desiredStatus, CloseRow: true, Mode: acp.ModeCloseOnly}
		if record.Outcome.Response != nil {
			observation.ReportID = diracTaskletReportID(key, record.lifecycle.ID)
			observation.Report = agent.SubagentReport{Text: *record.Outcome.Response, Status: "completed"}
		}
		a.ApplySubagentObservation(observation)
		return nil
	})
	if err == nil {
		watch.observed[key] = fingerprint
	}
	return err
}

func diracTaskletReportID(rowKey, lifecycleID string) string {
	raw, _ := json.Marshal([]string{rowKey, lifecycleID})
	digest := sha256.Sum256(raw)
	return "dirac-report:" + hex.EncodeToString(digest[:])
}

func diracTaskletKey(sessionID, taskletID, conversationID string) string {
	raw, _ := json.Marshal([]string{sessionID, taskletID, conversationID})
	digest := sha256.Sum256(raw)
	return "dirac-tasklet:" + hex.EncodeToString(digest[:])
}
