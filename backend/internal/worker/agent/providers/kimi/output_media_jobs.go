package kimi

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

const (
	kimiMediaConcurrency    = 4
	kimiMediaMaxJobs        = 32
	kimiMediaMaxQueuedBytes = 64 << 20
)

type kimiMediaJob struct {
	run           *kimiRun
	sessionID     string
	agentID       string
	itemIDs       []string
	reads         []kimiMediaSupplementRead
	batch         *agent.CapturedTranscriptBatch
	operations    []agent.CapturedTranscriptOperation
	prepared      atomic.Pointer[[]agent.CapturedTranscriptOperation]
	ctx           context.Context
	cancel        context.CancelFunc
	retainedBytes int
}

type kimiMediaSupplementRead struct {
	operationIndex int
	itemID         string
	read           func(context.Context, chan struct{}) ([]byte, error)
}

type kimiMediaService struct {
	mu            sync.Mutex
	closed        bool
	jobs          map[*kimiMediaJob]struct{}
	retainedBytes int
	queue         chan *kimiMediaJob
	permits       chan struct{}
	workers       sync.WaitGroup
}

func (a *Agent) mediaService() *kimiMediaService {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if a.media == nil {
		a.media = &kimiMediaService{jobs: make(map[*kimiMediaJob]struct{}), queue: make(chan *kimiMediaJob, kimiMediaMaxJobs), permits: make(chan struct{}, kimiMediaConcurrency)}
		for range kimiMediaConcurrency {
			a.media.workers.Go(func() {
				for job := range a.media.queue {
					a.completeMediaJob(a.media, job)
				}
			})
		}
	}
	return a.media
}

// retainMediaBatch accepts only already captured operations. Quota refusal
// leaves the caller's original representation available without recapturing it.
func (a *Agent) retainMediaBatch(run *kimiRun, sink agent.ProviderServices, sessionID, agentID string, batch *agent.CapturedTranscriptBatch, operations []agent.CapturedTranscriptOperation, reads []kimiMediaSupplementRead) bool {
	if len(reads) == 0 || a.IntentionalStopRequested() || a.IsStopped() || a.Context().Err() != nil {
		return false
	}
	retainedBytes := 0
	for _, operation := range operations {
		content := operation.FrozenContent()
		if len(content.Original) > kimiMediaMaxQueuedBytes-retainedBytes {
			return false
		}
		retainedBytes += len(content.Original)
		if len(content.Supplemental) > kimiMediaMaxQueuedBytes-retainedBytes {
			return false
		}
		retainedBytes += len(content.Supplemental)
	}
	service := a.mediaService()
	service.mu.Lock()
	if service.closed || a.IntentionalStopRequested() || len(service.jobs) >= kimiMediaMaxJobs || retainedBytes > kimiMediaMaxQueuedBytes-service.retainedBytes {
		service.mu.Unlock()
		slog.Warn("retain Kimi tool result without media because the media queue cannot accept it", "agent_id", a.AgentID())
		return false
	}
	ctx, cancel := a.requestContext()
	job := &kimiMediaJob{run: run, sessionID: sessionID, agentID: agentID, batch: batch, operations: slices.Clone(operations), reads: slices.Clone(reads), ctx: ctx, cancel: cancel, retainedBytes: retainedBytes}
	for _, read := range reads {
		job.itemIDs = append(job.itemIDs, read.itemID)
	}
	service.jobs[job] = struct{}{}
	service.retainedBytes += job.retainedBytes
	// A prepared result can remain behind another row or a failed storage write.
	// Release its allowance only after that output position completes.
	agent.OutputQueueFor(sink).Append(agent.OutputWrite{
		Owner: agent.NewOutputOwner(), Label: "release Kimi media retention",
		Run: func() error { service.release(job); return nil },
	})
	service.queue <- job
	service.mu.Unlock()
	if err := job.prepare(operations[:reads[0].operationIndex]); !agent.OutputRetained(err) {
		slog.Warn("prepare the ordered Kimi media output", "error", err)
	}
	return true
}

// The reservation publishes records that precede the first image result while
// the downloader prepares the rest. It still blocks every later queue entry.
func (job *kimiMediaJob) prepare(prefix []agent.CapturedTranscriptOperation) error {
	index := 0
	return job.batch.PrepareStream(func() (agent.CapturedTranscriptOperation, bool, error) {
		if index < len(prefix) {
			operation := prefix[index]
			index++
			return operation, false, nil
		}
		prepared := job.prepared.Load()
		if prepared == nil {
			return agent.CapturedTranscriptOperation{}, false, agent.ErrOutputInFlight
		}
		if index == len(*prepared) {
			return agent.CapturedTranscriptOperation{}, true, nil
		}
		operation := (*prepared)[index]
		index++
		return operation, false, nil
	})
}

func (a *Agent) completeMediaJob(service *kimiMediaService, job *kimiMediaJob) {
	defer job.cancel()
	supplements := make([][]byte, len(job.reads))
	for index, read := range job.reads {
		supplement, err := read.read(job.ctx, service.permits)
		if err != nil {
			slog.Warn("recover Kimi tool images", "agent_id", a.AgentID(), "item_id", read.itemID, "error", err)
			continue
		}
		supplements[index] = supplement
	}
	a.dispatchMu.Lock()
	a.Mu.Lock()
	// Admission fixes the representation while the original session and run still own it.
	// A later session switch cannot move this captured write to the new session.
	includeMedia := job.ctx.Err() == nil && !a.StoppedLocked() && !a.IntentionalStopRequested() &&
		a.sessionID == job.sessionID && a.runs[job.agentID] == job.run
	a.Mu.Unlock()
	a.finishDispatch()
	operations := slices.Clone(job.operations)
	if includeMedia {
		for index, supplement := range supplements {
			if len(supplement) == 0 || !service.retainSupplement(job, len(supplement)) {
				continue
			}
			at := job.reads[index].operationIndex
			if enriched, enrichErr := operations[at].WithSupplementalContent(supplement); enrichErr == nil {
				operations[at] = enriched
			} else {
				service.releaseSupplement(job, len(supplement))
				slog.Warn("retain Kimi tool result without rejected media", "agent_id", a.AgentID(), "error", enrichErr)
			}
		}
	}
	service.mu.Lock()
	job.run, job.reads, job.operations = nil, nil, nil
	service.mu.Unlock()
	job.prepared.Store(&operations)
	// Waking the stream can invoke persistence callbacks. Keep it outside provider locks.
	if err := job.batch.FlushAfterReady(); !agent.OutputRetained(err) {
		slog.Error("persist Kimi media result", "agent_id", a.AgentID(), "error", err)
	}
}

func (service *kimiMediaService) retainSupplement(job *kimiMediaJob, size int) bool {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.closed || size > kimiMediaMaxQueuedBytes-service.retainedBytes {
		return false
	}
	job.retainedBytes += size
	service.retainedBytes += size
	return true
}

func (service *kimiMediaService) releaseSupplement(job *kimiMediaJob, size int) {
	service.mu.Lock()
	defer service.mu.Unlock()
	job.retainedBytes -= size
	service.retainedBytes -= size
}

func (service *kimiMediaService) release(job *kimiMediaJob) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if _, retained := service.jobs[job]; retained {
		delete(service.jobs, job)
		service.retainedBytes -= job.retainedBytes
	}
}

func (a *Agent) cancelMediaSession(sessionID string) {
	a.Mu.Lock()
	service := a.media
	a.Mu.Unlock()
	if service == nil {
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	for job := range service.jobs {
		if job.sessionID == sessionID {
			job.cancel()
		}
	}
}

func (a *Agent) closeMedia() {
	a.Mu.Lock()
	service := a.media
	a.Mu.Unlock()
	if service == nil {
		return
	}
	service.mu.Lock()
	if !service.closed {
		service.closed = true
		for job := range service.jobs {
			job.cancel()
		}
		close(service.queue)
	}
	service.mu.Unlock()
	service.workers.Wait()
}
