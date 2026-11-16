package kimi

import (
	"context"
	"log/slog"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

const (
	kimiMediaConcurrency    = 4
	kimiMediaMaxJobs        = 32
	kimiMediaMaxQueuedBytes = 64 << 20
)

type kimiMediaJob struct {
	run           *kimiRun
	media         *kimiTranscriptMedia
	batch         *agent.CapturedTranscriptBatch
	operation     agent.CapturedTranscriptOperation
	ctx           context.Context
	cancel        context.CancelFunc
	retainedBytes int
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

// retainMediaResult reserves the complete closing row before dispatch proceeds.
// A refused reservation leaves the native tool retryable.
func (a *Agent) retainMediaResult(run *kimiRun, sink agent.ProviderServices, media *kimiTranscriptMedia, original []byte, span agent.SpanInfo) (bool, error) {
	if a.IntentionalStopRequested() || a.IsStopped() || a.Context().Err() != nil {
		return false, nil
	}
	service := a.mediaService()
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.closed || a.IntentionalStopRequested() || len(service.jobs) >= kimiMediaMaxJobs || len(original) > kimiMediaMaxQueuedBytes-service.retainedBytes {
		slog.Warn("retain Kimi tool result without media because the media queue cannot accept it", "agent_id", a.AgentID())
		return false, nil
	}
	batch, err := agent.ReserveCapturedTranscriptBatch(sink)
	if err != nil {
		return false, err
	}
	operation := agent.CaptureTranscript(sink, agent.MessageContent{Original: original}, span).MessageOperation(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
	ctx, cancel := a.requestContext()
	job := &kimiMediaJob{run: run, media: media, batch: batch, operation: operation, ctx: ctx, cancel: cancel, retainedBytes: len(original)}
	service.jobs[job] = struct{}{}
	service.retainedBytes += job.retainedBytes
	// A prepared result can remain behind another row or a failed storage write.
	// Release its allowance only after that output position completes.
	agent.OutputQueueFor(sink).Append(agent.OutputWrite{
		Owner: agent.NewOutputOwner(), Label: "release Kimi media retention",
		Run: func() error { service.release(job); return nil },
	})
	service.queue <- job
	return true, nil
}

func (a *Agent) completeMediaJob(service *kimiMediaService, job *kimiMediaJob) {
	defer job.cancel()
	supplement, err := a.readTranscriptMedia(job.ctx, job.media, service.permits)
	if err != nil {
		slog.Warn("recover Kimi tool images", "agent_id", a.AgentID(), "error", err)
	}
	a.dispatchMu.Lock()
	a.Mu.Lock()
	// Admission fixes the representation while the original session and run still own it.
	// A later session switch cannot move this captured write to the new session.
	includeMedia := err == nil && job.ctx.Err() == nil && !a.StoppedLocked() && !a.IntentionalStopRequested() &&
		a.sessionID == job.media.identity.sessionID && a.runs[job.media.identity.agentID] == job.run
	a.Mu.Unlock()
	a.finishDispatch()
	operation := job.operation
	if includeMedia && service.retainSupplement(job, len(supplement)) {
		if enriched, enrichErr := operation.WithSupplementalContent(supplement); enrichErr == nil {
			operation = enriched
		} else {
			service.releaseSupplement(job, len(supplement))
			slog.Warn("retain Kimi tool result without rejected media", "agent_id", a.AgentID(), "error", enrichErr)
		}
	}
	// Preparation can invoke persistence callbacks. Keep it outside provider locks.
	if err := job.batch.Prepare(func() []agent.CapturedTranscriptOperation { return []agent.CapturedTranscriptOperation{operation} }); err != nil && !agent.OutputRetained(err) {
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
		if job.media.identity.sessionID == sessionID {
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
