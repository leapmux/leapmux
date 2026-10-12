package qoder

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
	qoderArchiveRetryFirst = 100 * time.Millisecond
	qoderArchiveRetryMax   = 2 * time.Second
	qoderArchiveRetryLimit = 30 * time.Second
	qoderArchiveTimerTag   = "qoder workflow archive"
)

// qoderArchiveJob holds the native outcome after its first archive read fails.
// The output loop releases its workflow map entry before this job starts.
type qoderArchiveJob struct {
	mu     sync.Mutex
	event  qoderTaskEvent
	run    *qoderWorkflowRun
	status bgtask.Status
	done   bool
}

func (a *Agent) startWorkflowArchiveRetry(event qoderTaskEvent, run *qoderWorkflowRun, status bgtask.Status) {
	job := &qoderArchiveJob{event: event, run: run, status: status}
	a.archiveMu.Lock()
	if a.archiveStopped {
		a.archiveMu.Unlock()
		a.closeWorkflowWithoutArchive(run, status, errors.New("the qoder process exited before its workflow archive was ready"))
		return
	}
	if a.archiveJobs == nil {
		a.archiveJobs = make(map[qoderWorkflowKey]*qoderArchiveJob)
	}
	a.archiveJobs[run.key] = job
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
// retry signal. The timer still makes progress when no second frame arrives.
func (a *Agent) retryPendingWorkflowArchive(key qoderWorkflowKey) bool {
	a.archiveMu.Lock()
	job := a.archiveJobs[key]
	a.archiveMu.Unlock()
	if job == nil {
		return false
	}
	a.tryWorkflowArchive(job)
	return true
}

func (a *Agent) runWorkflowArchiveRetry(job *qoderArchiveJob) {
	delay := qoderArchiveRetryFirst
	elapsed := time.Duration(0)
	for {
		job.mu.Lock()
		done := job.done
		job.mu.Unlock()
		if done {
			return
		}
		if remaining := qoderArchiveRetryLimit - elapsed; delay > remaining {
			delay = remaining
		}
		timer := a.Clock().NewTimer(delay, qoderArchiveTimerTag)
		select {
		case <-a.Context().Done():
			timer.Stop(qoderArchiveTimerTag)
			a.failWorkflowArchive(job, errors.New("the qoder process stopped before its workflow archive was ready"))
			return
		case <-a.ProcessDone():
			timer.Stop(qoderArchiveTimerTag)
			a.failWorkflowArchive(job, errors.New("the qoder process exited before its workflow archive was ready"))
			return
		case <-timer.C:
			timer.Stop(qoderArchiveTimerTag)
		}
		elapsed += delay
		if a.tryWorkflowArchive(job) {
			return
		}
		if elapsed >= qoderArchiveRetryLimit {
			a.failWorkflowArchive(job, errors.New("the qoder workflow archive did not become complete within 30 seconds"))
			return
		}
		delay = min(delay*2, qoderArchiveRetryMax)
	}
}

func (a *Agent) tryWorkflowArchive(job *qoderArchiveJob) bool {
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.done {
		return true
	}
	archive, err := readQoderWorkflowArchive(a.opts, job.run.key.sessionID, &job.event, job.run)
	if errors.Is(err, errQoderArchiveIncomplete) {
		return false
	}
	if err != nil {
		slog.Warn("qoder workflow archive retry refused", "agent_id", a.AgentID(), "error", err)
		a.closeWorkflowWithoutArchive(job.run, job.status, err)
	} else {
		a.applyWorkflowArchive(job.run, job.status, archive)
	}
	a.finishWorkflowArchiveJob(job)
	return true
}

func (a *Agent) failWorkflowArchive(job *qoderArchiveJob, err error) {
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.done {
		return
	}
	a.closeWorkflowWithoutArchive(job.run, job.status, err)
	a.finishWorkflowArchiveJob(job)
}

// The caller holds job.mu, so a timer and a repeated notification finish once.
func (a *Agent) finishWorkflowArchiveJob(job *qoderArchiveJob) {
	job.done = true
	a.archiveMu.Lock()
	if a.archiveJobs[job.run.key] == job {
		delete(a.archiveJobs, job.run.key)
	}
	a.archiveMu.Unlock()
}

func (a *Agent) reportWorkflowArchiveFailure(err error) {
	a.sink.PersistLeapMuxNotification(map[string]interface{}{
		contracts.NotificationFieldType:  contracts.NotificationTypeAgentError,
		contracts.NotificationFieldError: fmt.Sprintf("workflow transcript unavailable: %v", err),
	})
}
