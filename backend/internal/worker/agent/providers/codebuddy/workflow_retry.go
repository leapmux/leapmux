package codebuddy

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

const (
	codebuddyArchiveRetryFirst = 100 * time.Millisecond
	codebuddyArchiveRetryMax   = 2 * time.Second
	codebuddyArchiveRetryLimit = 30 * time.Second
	codebuddyArchiveTimerTag   = "codebuddy workflow archive"
)

// codebuddyArchiveJob keeps only the children whose saved files are incomplete.
// A complete child leaves this list, so a later retry cannot route it twice.
type codebuddyArchiveJob struct {
	mu              sync.Mutex
	runID           string
	groupLabel      string
	status          bgtask.Status
	parentSessionID string
	pending         []codebuddyWorkflowChild
	done            bool
}

func (a *Agent) startWorkflowArchiveRetry(runID, groupLabel string, status bgtask.Status, parentSessionID string, children []codebuddyWorkflowChild) {
	job := &codebuddyArchiveJob{runID: runID, groupLabel: groupLabel, status: status, parentSessionID: parentSessionID, pending: children}
	a.archiveMu.Lock()
	if a.archiveStopped {
		a.archiveMu.Unlock()
		for _, child := range children {
			a.reportWorkflowArchiveFailure(child.rowKey, errors.New("the codebuddy process exited before its workflow archive was ready"))
			a.closeWorkflowChild(child, status)
		}
		return
	}
	if a.archiveJobs == nil {
		a.archiveJobs = make(map[string]*codebuddyArchiveJob)
		a.archiveChildJobs = make(map[string]*codebuddyArchiveJob)
	}
	a.archiveJobs[runID] = job
	for _, child := range children {
		a.archiveChildJobs[child.rowKey] = job
	}
	a.archiveRetries.Add(1)
	a.archiveMu.Unlock()
	go func() {
		defer a.archiveRetries.Done()
		a.runWorkflowArchiveRetry(job)
	}()
}

func (a *Agent) stopWorkflowArchiveRetries() {
	a.archiveMu.Lock()
	a.archiveStopped = true
	a.archiveMu.Unlock()
	a.archiveRetries.Wait()
}

// retryPendingWorkflowArchive uses a repeated native notification as an early
// retry signal. The timer also retries when no second notification arrives.
func (a *Agent) retryPendingWorkflowArchive(runID string) bool {
	a.archiveMu.Lock()
	job := a.archiveJobs[runID]
	a.archiveMu.Unlock()
	if job == nil {
		return false
	}
	a.tryWorkflowArchive(job)
	return true
}

// A child can finish while its parent waits for an archive file. Keep that
// native outcome in the pending job; the transcript must arrive before close.
func (a *Agent) recordPendingWorkflowChildEnd(rowKey string, status bgtask.Status) bool {
	a.archiveMu.Lock()
	job := a.archiveChildJobs[rowKey]
	a.archiveMu.Unlock()
	if job == nil {
		return false
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	for index := range job.pending {
		child := &job.pending[index]
		if child.rowKey == rowKey {
			if !child.status.IsFinished() {
				child.status = status
			}
			break
		}
	}
	return true
}

func (a *Agent) runWorkflowArchiveRetry(job *codebuddyArchiveJob) {
	delay := codebuddyArchiveRetryFirst
	elapsed := time.Duration(0)
	for {
		job.mu.Lock()
		done := job.done
		job.mu.Unlock()
		if done {
			return
		}
		if remaining := codebuddyArchiveRetryLimit - elapsed; delay > remaining {
			delay = remaining
		}
		timer := a.Clock().NewTimer(delay, codebuddyArchiveTimerTag)
		select {
		case <-a.Context().Done():
			timer.Stop(codebuddyArchiveTimerTag)
			a.failWorkflowArchive(job, errors.New("the codebuddy process stopped before its workflow archive was ready"))
			return
		case <-a.ProcessDone():
			timer.Stop(codebuddyArchiveTimerTag)
			a.failWorkflowArchive(job, errors.New("the codebuddy process exited before its workflow archive was ready"))
			return
		case <-timer.C:
			timer.Stop(codebuddyArchiveTimerTag)
		}
		elapsed += delay
		if a.tryWorkflowArchive(job) {
			return
		}
		if elapsed >= codebuddyArchiveRetryLimit {
			a.failWorkflowArchive(job, errors.New("the codebuddy workflow archive did not become complete within 30 seconds"))
			return
		}
		delay = min(delay*2, codebuddyArchiveRetryMax)
	}
}

func (a *Agent) tryWorkflowArchive(job *codebuddyArchiveJob) bool {
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.done {
		return true
	}
	remaining := job.pending[:0]
	for _, child := range job.pending {
		err := a.replayWorkflowChild(job.runID, child, job.groupLabel, job.parentSessionID)
		if errors.Is(err, errCodebuddyArchiveIncomplete) {
			remaining = append(remaining, child)
			continue
		}
		if err != nil {
			slog.Warn("codebuddy workflow archive retry refused", "agent_id", a.AgentID(), "child_key", child.rowKey, "error", err)
			if errors.Is(err, errCodebuddyChildRoute) {
				child.status = bgtask.StatusFailed
			}
			a.reportWorkflowArchiveFailure(child.rowKey, err)
		}
		a.closeWorkflowChild(child, job.status)
		a.removePendingWorkflowChild(child.rowKey, job)
	}
	job.pending = remaining
	if len(remaining) > 0 {
		return false
	}
	a.finishWorkflowArchiveJob(job)
	return true
}

func (a *Agent) removePendingWorkflowChild(rowKey string, job *codebuddyArchiveJob) {
	a.archiveMu.Lock()
	if a.archiveChildJobs[rowKey] == job {
		delete(a.archiveChildJobs, rowKey)
	}
	a.archiveMu.Unlock()
}

func (a *Agent) failWorkflowArchive(job *codebuddyArchiveJob, err error) {
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.done {
		return
	}
	for _, child := range job.pending {
		a.reportWorkflowArchiveFailure(child.rowKey, err)
		a.closeWorkflowChild(child, job.status)
		a.removePendingWorkflowChild(child.rowKey, job)
	}
	job.pending = nil
	a.finishWorkflowArchiveJob(job)
}

// The caller holds job.mu, so a timer and a repeated notification finish once.
func (a *Agent) finishWorkflowArchiveJob(job *codebuddyArchiveJob) {
	job.done = true
	a.archiveMu.Lock()
	delete(a.archiveJobs, job.runID)
	for _, child := range job.pending {
		if a.archiveChildJobs[child.rowKey] == job {
			delete(a.archiveChildJobs, child.rowKey)
		}
	}
	a.archiveMu.Unlock()
}

func (a *Agent) reportWorkflowArchiveFailure(rowKey string, err error) {
	a.sink.PersistLeapMuxNotification(map[string]interface{}{
		contracts.NotificationFieldType:  contracts.NotificationTypeAgentError,
		contracts.NotificationFieldError: fmt.Sprintf("workflow child %s transcript unavailable: %v", rowKey, err),
	})
}
