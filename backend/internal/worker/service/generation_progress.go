package service

import (
	"sync"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

const generationProgressInterval = 250 * time.Millisecond

type generationProgressPublisher struct {
	mu      sync.Mutex
	counter agent.ProgressCounter
	timer   *time.Timer
	closed  bool
	publish func(map[string]interface{})
}

func newGenerationProgressPublisher(publish func(map[string]interface{})) *generationProgressPublisher {
	return &generationProgressPublisher{publish: publish}
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
	urgent := update.Operation == agent.ProgressReset ||
		update.Operation == agent.ProgressModelReset ||
		update.Operation == agent.ProgressModelComplete ||
		update.Operation == agent.ProgressOutputComplete
	if urgent {
		if p.timer != nil {
			p.timer.Stop()
			p.timer = nil
		}
		info := progressInfo(snapshot)
		// Keep the lock through the send. A reset must publish after this value,
		// or a delayed send can restore a counter that the reset cleared.
		p.publish(info)
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
	info := progressInfo(p.counter.Snapshot())
	// See the immediate-send path. The lock makes publication order match
	// counter mutation order.
	p.publish(info)
	p.mu.Unlock()
}

func (p *generationProgressPublisher) snapshotInfo() map[string]interface{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	return positiveProgressInfo(p.counter.Snapshot())
}

func (p *generationProgressPublisher) close() {
	p.mu.Lock()
	p.closed = true
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	p.counter.Apply(agent.ResetProgress())
	p.mu.Unlock()
}

func progressInfo(snapshot agent.ProgressSnapshot) map[string]interface{} {
	info := map[string]interface{}{
		contracts.SessionInfoKeyThinkingTokens: snapshot.ThinkingTokens,
		contracts.SessionInfoKeyOutputBytes:    snapshot.OutputBytes,
	}
	if snapshot.OutputBytes > 0 {
		info[contracts.SessionInfoKeyOutputBytesMinimum] = snapshot.OutputBytesMinimum
	}
	return info
}

func positiveProgressInfo(snapshot agent.ProgressSnapshot) map[string]interface{} {
	info := make(map[string]interface{}, 3)
	if snapshot.ThinkingTokens > 0 {
		info[contracts.SessionInfoKeyThinkingTokens] = snapshot.ThinkingTokens
	}
	if snapshot.OutputBytes > 0 {
		info[contracts.SessionInfoKeyOutputBytes] = snapshot.OutputBytes
		info[contracts.SessionInfoKeyOutputBytesMinimum] = snapshot.OutputBytesMinimum
	}
	if len(info) == 0 {
		return nil
	}
	return info
}
