package service

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

const generationProgressInterval = 250 * time.Millisecond

var generationProgressRevision atomic.Uint64

type progressEmission struct {
	info map[string]interface{}
}

type generationProgressPublisher struct {
	mu       sync.Mutex
	counter  agent.ProgressCounter
	timer    *time.Timer
	closed   bool
	seen     bool
	revision uint64
	pending  *progressEmission
	wake     chan struct{}
	publish  func(map[string]interface{})
}

func newGenerationProgressPublisher(publish func(map[string]interface{})) *generationProgressPublisher {
	publisher := &generationProgressPublisher{
		publish: publish,
		wake:    make(chan struct{}, 1),
	}
	go publisher.run()
	return publisher
}

func (p *generationProgressPublisher) report(update agent.ProgressUpdate) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	snapshot, changed := p.counter.Apply(update)
	if !changed {
		p.mu.Unlock()
		return
	}
	p.seen = true
	p.revision = generationProgressRevision.Add(1)
	urgent := update.Operation == agent.ProgressReset ||
		update.Operation == agent.ProgressModelReset ||
		update.Operation == agent.ProgressModelComplete ||
		update.Operation == agent.ProgressOutputComplete ||
		update.Operation == agent.ProgressOutputReset
	if urgent {
		if p.timer != nil {
			p.timer.Stop()
			p.timer = nil
		}
		p.queueLocked(snapshot)
		p.mu.Unlock()
		return
	}
	if p.timer == nil {
		p.timer = time.AfterFunc(generationProgressInterval, p.flush)
	}
	p.mu.Unlock()
}

func (p *generationProgressPublisher) flush() {
	p.mu.Lock()
	if p.closed {
		p.timer = nil
		p.mu.Unlock()
		return
	}
	p.timer = nil
	p.queueLocked(p.counter.Snapshot())
	p.mu.Unlock()
}

func (p *generationProgressPublisher) queueLocked(snapshot agent.ProgressSnapshot) {
	p.pending = &progressEmission{info: progressInfo(snapshot, p.revision)}
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *generationProgressPublisher) run() {
	for range p.wake {
		for {
			p.mu.Lock()
			emission := p.pending
			p.pending = nil
			closed := p.closed
			p.mu.Unlock()
			if emission == nil {
				if closed {
					return
				}
				break
			}
			p.publish(emission.info)
		}
	}
}

func (p *generationProgressPublisher) snapshotInfo() map[string]interface{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || !p.seen {
		return nil
	}
	return progressInfo(p.counter.Snapshot(), p.revision)
}

func (p *generationProgressPublisher) close() {
	p.mu.Lock()
	p.closed = true
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	p.counter.Apply(agent.ResetProgress())
	p.pending = nil
	select {
	case p.wake <- struct{}{}:
	default:
	}
	p.mu.Unlock()
}

func progressInfo(snapshot agent.ProgressSnapshot, revision uint64) map[string]interface{} {
	info := map[string]interface{}{
		contracts.SessionInfoKeyThinkingTokens:             snapshot.ThinkingTokens,
		contracts.SessionInfoKeyOutputBytes:                snapshot.OutputBytes,
		contracts.SessionInfoKeyGenerationProgressRevision: revision,
	}
	if snapshot.OutputBytes > 0 {
		info[contracts.SessionInfoKeyOutputBytesMinimum] = snapshot.OutputBytesMinimum
	}
	return info
}
