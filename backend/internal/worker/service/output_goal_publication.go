package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// goalPublication retains immutable data for one committed storage decision.
// Its raw identity columns permit the existing stored-goal comparison before progress dedup.
type goalPublication struct {
	admission       *goalAdmission
	goal            *leapmuxv1.AgentEvent
	notification    *agent.CapturedTranscript
	progress        *leapmuxv1.AgentEvent
	encodedProgress []byte
	identity        GoalColumns
}

type goalPublicationStage uint8

const (
	goalPublicationGoal goalPublicationStage = iota
	goalPublicationNotification
	goalPublicationProgress
	goalPublicationComplete
)

type goalPublicationDelivery struct {
	publication goalPublication
	stage       goalPublicationStage
}

// goalPublicationQueue owns delivery bookkeeping, with no live or stored goal authority.
// The handler's queue mutex protects its records and one delivery claim.
type goalPublicationQueue struct {
	records  []*goalPublicationDelivery
	draining bool
}

func (publication *goalPublication) prepareProgress(agentID string, update agent.GoalUpdate) error {
	progress := goalProgressInfo(update)
	if progress == nil {
		return nil
	}
	encoded, err := json.Marshal(progress)
	if err != nil {
		return err
	}
	event := sessionInfoEvent(agentID, map[string]interface{}{
		contracts.SessionInfoKeyGoalProgress: json.RawMessage(encoded),
	})
	if event == nil {
		return errors.New("the Worker could not prepare the goal progress event")
	}
	publication.encodedProgress = encoded
	publication.progress = event
	return nil
}

// enqueueGoalPublication invokes no callback. The caller holds the goal mutex.
func (h *OutputHandler) enqueueGoalPublication(agentID string, publication goalPublication) {
	if publication.goal == nil && publication.notification == nil && publication.progress == nil {
		return
	}
	h.goalPublicationMu.Lock()
	defer h.goalPublicationMu.Unlock()
	if h.goalPublications == nil {
		h.goalPublications = make(map[string]*goalPublicationQueue)
	}
	queue := h.goalPublications[agentID]
	if queue == nil {
		queue = &goalPublicationQueue{}
		h.goalPublications[agentID] = queue
	}
	queue.records = append(queue.records, &goalPublicationDelivery{publication: publication})
}

// drainGoalPublications preserves complete record order outside every retained lock.
// A concurrent or reentrant operation appends its record and leaves delivery to the existing claim.
// A failed stage keeps its exact record for a later explicit drain attempt.
func (h *OutputHandler) drainGoalPublications(agentID string) {
	h.goalPublicationMu.Lock()
	queue := h.goalPublications[agentID]
	if queue == nil || queue.draining {
		h.goalPublicationMu.Unlock()
		return
	}
	queue.draining = true
	h.goalPublicationMu.Unlock()
	defer h.releaseGoalPublicationClaim(agentID, queue)
	for {
		h.goalPublicationMu.Lock()
		if len(queue.records) == 0 {
			// Release and remove the exact queue under the same mutex.
			// An append after this removal creates a new queue with its own drainer.
			queue.draining = false
			if h.goalPublications[agentID] == queue {
				delete(h.goalPublications, agentID)
			}
			h.goalPublicationMu.Unlock()
			return
		}
		delivery := queue.records[0]
		stage := delivery.stage
		h.goalPublicationMu.Unlock()
		publication := &delivery.publication
		switch stage {
		case goalPublicationGoal:
			accepted := publication.goal != nil && publication.admission.enqueueEvent(publication.goal)
			// Advance before transport. A watcher panic must not repeat an accepted event.
			h.advanceGoalPublication(delivery)
			if accepted {
				h.watcher.DrainAgentEvents(agentID)
			}
		case goalPublicationNotification:
			if publication.notification != nil {
				if _, err := publication.notification.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX); err != nil {
					logGoalFailure(agentID, goalFailure{message: "failed to persist notification", err: err})
					return
				}
			}
			h.advanceGoalPublication(delivery)
		case goalPublicationProgress:
			accepted, err := publication.enqueueProgress()
			if err != nil {
				logGoalFailure(agentID, goalFailure{message: "The Worker failed to read the goal before progress publication.", err: err})
				return
			}
			h.advanceGoalPublication(delivery)
			if accepted {
				h.watcher.DrainAgentEvents(agentID)
			}
		case goalPublicationComplete:
			h.goalPublicationMu.Lock()
			queue.records[0] = nil
			queue.records = queue.records[1:]
			h.goalPublicationMu.Unlock()
		}
	}
}

func (h *OutputHandler) advanceGoalPublication(delivery *goalPublicationDelivery) {
	h.goalPublicationMu.Lock()
	delivery.stage++
	h.goalPublicationMu.Unlock()
}

// releaseGoalPublicationClaim also runs after a panic. It preserves unfinished records.
func (h *OutputHandler) releaseGoalPublicationClaim(agentID string, queue *goalPublicationQueue) {
	h.goalPublicationMu.Lock()
	defer h.goalPublicationMu.Unlock()
	if h.goalPublications[agentID] != queue {
		return
	}
	queue.draining = false
	if len(queue.records) == 0 {
		delete(h.goalPublications, agentID)
	}
}

// enqueueProgress checks the actual stored goal before it changes the existing dedup cache.
// An old notification may persist after replacement, but its counters cannot describe the replacement goal.
func (publication *goalPublication) enqueueProgress() (bool, error) {
	if publication.progress == nil {
		return false, nil
	}
	admission := publication.admission
	mutation := admission.mutationMutex()
	mutation.RLock()
	defer mutation.RUnlock()
	if !admission.currentLocked() {
		return false, nil
	}
	goal := admission.h.goalMutex(admission.agentID)
	goal.Lock()
	defer goal.Unlock()
	row, err := admission.h.queries.GetAgentGoal(bgCtx(), admission.agentID)
	if err != nil {
		return false, err
	}
	identity := agent.GoalUpdate{NativeID: publication.identity.NativeID}
	if publication.identity.CreatedAt.Valid {
		identity.CreatedAt = publication.identity.CreatedAt.Time
	}
	if !goalPresent(row.GoalObjective) || row.GoalObjective != publication.identity.Objective || !sameGoalIdentity(row, identity) {
		return false, nil
	}
	sink := admission.sink
	sink.sessionInfoMu.Lock()
	defer sink.sessionInfoMu.Unlock()
	if bytes.Equal(sink.lastSessionInfo[contracts.SessionInfoKeyGoalProgress], publication.encodedProgress) {
		return false, nil
	}
	if sink.lastSessionInfo == nil {
		sink.lastSessionInfo = make(map[string][]byte)
	}
	sink.lastSessionInfo[contracts.SessionInfoKeyGoalProgress] = slices.Clone(publication.encodedProgress)
	admission.h.watcher.EnqueueAgentEvent(admission.agentID, publication.progress)
	return true, nil
}
