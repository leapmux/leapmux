package droid

import (
	"fmt"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// droidChildConnection holds one bound stream JSON-RPC process. The root Agent
// creates the slot before startup, so concurrent sends load one child only.
type droidChildConnection struct {
	ready chan struct{}
	done  chan struct{}
	agent *Agent
	err   error
}

// droidArchiveOnlySink keeps the native JSONL tail authoritative for chat
// rows. The bound process still sends controls, settings, and turn state to the
// child sink through the embedded services.
type droidArchiveOnlySink struct{ agent.ProviderServices }

var _ agent.ProviderServices = droidArchiveOnlySink{}

func (droidArchiveOnlySink) PersistMessage(leapmuxv1.MessageSource, agent.MessageContent, agent.SpanInfo) error {
	return nil
}

func (droidArchiveOnlySink) PersistTurnEnd(agent.MessageContent, agent.SpanInfo) error {
	return nil
}

// registeredChildID rejects a key that the Worker's child registry cannot
// resolve. A guessed native UUID is never enough to open another session.
func (a *Agent) registeredChildID(childKey string) (string, error) {
	if !validDroidSessionID(childKey) {
		return "", agent.ErrChildRouteNotReady
	}
	childAgentID, _, exists, err := a.sink.LookupBackgroundTask(childKey)
	if err != nil {
		return "", fmt.Errorf("look up droid child %q: %w", childKey, err)
	}
	if !exists || childAgentID == "" {
		return "", agent.ErrChildRouteNotReady
	}
	return childAgentID, nil
}

// childConnection loads a registry-linked child in a separate process. The
// root stream adapter cannot select another session.
func (a *Agent) childConnection(childKey, childAgentID string) (*Agent, error) {
	a.childConnMu.Lock()
	if a.childClosing {
		a.childConnMu.Unlock()
		return nil, errAgentStopped
	}
	if a.childConns == nil {
		a.childConns = make(map[string]*droidChildConnection)
	}
	connection := a.childConns[childKey]
	if connection != nil {
		a.childConnMu.Unlock()
		<-connection.ready
		return connection.agent, connection.err
	}
	connection = &droidChildConnection{ready: make(chan struct{}), done: make(chan struct{})}
	a.childConns[childKey] = connection
	a.childConnMu.Unlock()

	childOpts := a.launchOpts
	childOpts.AgentID = childAgentID
	childOpts.ResumeSessionID = childKey
	childSink := droidArchiveOnlySink{ProviderServices: a.sink.ChildSink(childAgentID)}
	started, startErr := startProcess(a.Context(), childOpts, childSink, a.launchSpec)
	var child *Agent
	if startErr == nil {
		child = started.(*Agent)
	}
	a.childConnMu.Lock()
	if a.childClosing && child != nil {
		startErr = errAgentStopped
	}
	connection.agent = child
	connection.err = startErr
	if startErr != nil {
		delete(a.childConns, childKey)
	}
	close(connection.ready)
	a.childConnMu.Unlock()
	if startErr != nil {
		close(connection.done)
		if child != nil {
			child.Stop()
		}
		return nil, startErr
	}
	go a.watchChildConnection(childKey, connection)
	return child, nil
}

func (a *Agent) watchChildConnection(childKey string, connection *droidChildConnection) {
	defer close(connection.done)
	_ = connection.agent.Wait()
	a.childConnMu.Lock()
	current := a.childConns[childKey] == connection
	if current {
		delete(a.childConns, childKey)
	}
	a.childConnMu.Unlock()
	if !current {
		return
	}
	a.finishExitedChild(childKey, connection.agent)
}

// finishExitedChild first reads the archive's final outcome, when present.
// An exit without that outcome ends the registry row as failed or interrupted.
func (a *Agent) finishExitedChild(childKey string, child *Agent) {
	select {
	case <-child.ProcessDone():
	default:
		return
	}
	a.tailMu.Lock()
	tail := a.childTails[childKey]
	a.tailMu.Unlock()
	if tail != nil {
		tail.poll()
	}
	_, status, exists, lookupErr := a.sink.LookupBackgroundTask(childKey)
	if lookupErr != nil {
		slog.Warn("droid: child exit row unavailable", "session_id", childKey, "error", lookupErr)
		return
	}
	if !exists || status.IsFinished() {
		return
	}
	final := bgtask.StatusFailed
	if a.IsStopped() || child.IsStopped() {
		final = bgtask.StatusInterrupted
	}
	providerkit.LogRegistryRefusal("droid", "close exited child", a.sink.CloseBackgroundTask(childKey, final))
}

func (a *Agent) stopChildConnections() {
	a.childStopOnce.Do(func() {
		a.childConnMu.Lock()
		a.childClosing = true
		connections := make([]*droidChildConnection, 0, len(a.childConns))
		for _, connection := range a.childConns {
			connections = append(connections, connection)
		}
		a.childConnMu.Unlock()
		for _, connection := range connections {
			<-connection.ready
			if connection.agent != nil {
				connection.agent.Stop()
			}
		}
	})
}
