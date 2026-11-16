package kimi

import (
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// kimiChildHost preserves descendant dependencies across this child's lease renewals.
// Physical cleanup recursively retires descendants, so it must wait for their output.
type kimiChildHost struct {
	mu       sync.Mutex
	children map[*kimiChildLifetime]struct{}
	waiting  map[*kimiChildLifetime]struct{}
}

// The Worker owns physical process-exit finalization. Session retirement releases
// cleanup dependencies without removing still-running background rows or activity.
type kimiChildLifetimeGraph struct {
	mu      sync.Mutex
	retired atomic.Bool
	nodes   map[*kimiChildLifetime]struct{}
}

func (g *kimiChildLifetimeGraph) abandon() {
	if g == nil || g.retired.Swap(true) {
		return
	}
	g.mu.Lock()
	nodes := make([]*kimiChildLifetime, 0, len(g.nodes))
	for node := range g.nodes {
		nodes = append(nodes, node)
	}
	g.nodes = nil
	g.mu.Unlock()
	for _, node := range nodes {
		node.release()
		node.wake()
	}
}

type kimiChildLifetime struct {
	parent           agent.ProviderServices
	writer           agent.ProviderServices
	lease            agent.ChildSinkLease
	queue            *agent.OutputQueue
	owner            *agent.OutputOwner
	host, parentHost *kimiChildHost
	graph            *kimiChildLifetimeGraph
	superseded       atomic.Bool
	releaseOnce      sync.Once
	// dispatchMu protects admission of the retirement handoff.
	retiring bool
}

func (l *kimiChildLifetime) wake() {
	if err := l.queue.FlushOwnerAfterReady(l.owner); err != nil && !errors.Is(err, agent.ErrOutputInFlight) {
		slog.Debug("retain Kimi child cleanup behind output", "error", err)
	}
}

func (l *kimiChildLifetime) retire() error {
	l.host.mu.Lock()
	if !l.graph.retired.Load() && !l.superseded.Load() && len(l.host.children) != 0 {
		if l.host.waiting == nil {
			l.host.waiting = make(map[*kimiChildLifetime]struct{})
		}
		l.host.waiting[l] = struct{}{}
		l.host.mu.Unlock()
		return agent.ErrOutputInFlight
	}
	delete(l.host.waiting, l)
	l.host.mu.Unlock()
	// Cleanup checks the captured physical lease again. A replacement makes it a no-op.
	if !l.graph.retired.Load() {
		l.lease.Cleanup()
	}
	l.release()
	return nil
}

func (l *kimiChildLifetime) release() {
	l.releaseOnce.Do(func() {
		l.graph.mu.Lock()
		delete(l.graph.nodes, l)
		l.graph.mu.Unlock()
		if l.parentHost == nil {
			return
		}
		l.parentHost.mu.Lock()
		delete(l.parentHost.children, l)
		waiting := make([]*kimiChildLifetime, 0, len(l.parentHost.waiting))
		for parent := range l.parentHost.waiting {
			waiting = append(waiting, parent)
		}
		l.parentHost.mu.Unlock()
		for _, parent := range waiting {
			parent.wake()
		}
	})
}

type kimiLeasedChildServices struct {
	agent.ProviderServices
	capture func(agent.MessageContent, agent.SpanInfo) agent.MessageContent
}

func (s *kimiLeasedChildServices) CaptureMessage(content agent.MessageContent, span agent.SpanInfo) agent.MessageContent {
	return s.capture(content, span)
}

// installChildLifetime installs the writer before activation can publish progress.
// The caller holds dispatchMu.
func (a *Agent) installChildLifetime(run, parentRun *kimiRun, parent agent.ProviderServices, lease agent.ChildSinkLease) error {
	if a.ProcessExited() || parentRun.childGraph != nil && parentRun.childGraph.retired.Load() {
		return errors.New("the Kimi child cannot join a retired session")
	}
	if lease.Sink == nil || lease.CaptureMessage == nil || lease.Activate == nil || lease.Cleanup == nil {
		return errors.New("the Kimi child services supplied an incomplete lifetime")
	}
	direct, valid := agent.DirectTranscript(lease.Sink).(agent.ProviderServices)
	if !valid {
		return errors.New("the Kimi child writer does not preserve its provider services")
	}
	writer := agent.NewOrderedProviderServices(agent.OutputQueueFor(lease.Sink), &kimiLeasedChildServices{
		ProviderServices: direct, capture: lease.CaptureMessage,
	})
	queue := agent.OutputQueueFor(writer)
	if queue == nil {
		return errors.New("the Kimi child writer has no output queue")
	}
	if run.childHost == nil {
		run.childHost = &kimiChildHost{children: make(map[*kimiChildLifetime]struct{})}
	}
	if parentRun.childHost == nil {
		parentRun.childHost = &kimiChildHost{children: make(map[*kimiChildLifetime]struct{})}
	}
	if parentRun.childGraph == nil {
		parentRun.childGraph = &kimiChildLifetimeGraph{nodes: make(map[*kimiChildLifetime]struct{})}
	}
	run.childGraph = parentRun.childGraph
	lifetime := &kimiChildLifetime{parent: parent, writer: agent.NewModelProgressResetSink(writer), lease: lease,
		queue: queue, owner: agent.NewOutputOwner(), host: run.childHost, parentHost: parentRun.childHost, graph: run.childGraph}
	lifetime.graph.mu.Lock()
	lifetime.graph.nodes[lifetime] = struct{}{}
	lifetime.graph.mu.Unlock()
	if lifetime.parentHost != nil {
		lifetime.parentHost.mu.Lock()
		lifetime.parentHost.children[lifetime] = struct{}{}
		lifetime.parentHost.mu.Unlock()
	}
	previous := run.childLifetime
	run.childParentAgentID, run.childLifetime = parentRun.agentID, lifetime
	if previous != nil {
		previous.superseded.Store(true)
		previous.wake()
	}
	return nil
}

func (a *Agent) activateChildLifetime(run *kimiRun) bool {
	if !a.ensureChildLifetime(run) {
		return false
	}
	run.childLifetime.lease.Activate()
	return true
}

func (a *Agent) ensureChildLifetime(run *kimiRun) bool {
	if a.ProcessExited() || run.childGraph != nil && run.childGraph.retired.Load() {
		return false
	}
	if run.childLifetime != nil && !run.childLifetime.retiring {
		return true
	}
	child, linked := a.children.get(run.agentID)
	if !linked || run.childParentAgentID == "" {
		return false
	}
	parentRun := a.run(run.childParentAgentID)
	if parentRun.agentID != kimiMainAgentID && !a.ensureChildLifetime(parentRun) {
		return false
	}
	parent := a.runSink(parentRun)
	if parent == nil {
		return false
	}
	if err := a.installChildLifetime(run, parentRun, parent, parent.AcquireChildStateSink(child.childAgentID)); err != nil {
		slog.Warn("acquire Kimi child lifetime", "agent_id", a.AgentID(), "child", run.agentID, "error", err)
		return false
	}
	return true
}

// queueChildCleanup follows the physical registry close and the child's admitted output.
// Captured cleanup cannot retire a newer child lifetime.
func (a *Agent) queueChildCleanup(run *kimiRun) {
	// A native turn can finish while an unrelated input still awaits admission.
	// Keep that input's original source current through readiness publication.
	if child, linked := a.children.get(run.agentID); linked && child.pendingPrompt != nil {
		return
	}
	lifetime := run.childLifetime
	if lifetime == nil || lifetime.retiring {
		return
	}
	lifetime.retiring = true
	err := a.queueRegistryWrite(a.sink, "retire Kimi child after its registry close", func(agent.BackgroundTaskServices) error {
		lifetime.queue.Append(agent.OutputWrite{Owner: lifetime.owner, Label: "retire Kimi child output lifetime", Run: lifetime.retire})
		lifetime.wake()
		return nil
	})
	if !agent.OutputRetained(err) {
		lifetime.retiring = false
		slog.Warn("retain Kimi child cleanup", "agent_id", a.AgentID(), "child", run.agentID, "error", err)
	}
}
