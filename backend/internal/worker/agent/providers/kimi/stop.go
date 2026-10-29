package kimi

import (
	"context"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Kimi Code's stop.
//
// Kimi runs every tool command in a process group of its own (`detached: true`
// on every platform but Windows), so the signal that ends the server's process
// group does not reach them -- and neither does its own graceful shutdown: a
// running command and a background task both outlive `POST /shutdown` and
// SIGTERM (measured on 2.0.2).
//
// Stop captures verified descendants before each control request, because an
// abort or shutdown can end the root. The shared owner retains those identities
// after root exit and terminates the remaining commands. Windows also retains
// descendants in the job object assigned to the server.
//
// Protocol requests stop once native exit is recorded. Local cleanup still
// closes the stream and endpoint and completes Process.Stop.

// kimiStopStepTimeout limits each native stop step.
const kimiStopStepTimeout = 2 * time.Second

// Interrupt aborts the main agent's running turn. A turn that is not running
// needs nothing.
func (a *Agent) Interrupt(stop agent.StopContext) error {
	a.Mu.Lock()
	sessionID, active := a.sessionID, a.turnActive
	a.Mu.Unlock()
	if !active {
		return nil
	}
	if err := kimiCheckID("session", sessionID); err != nil {
		return err
	}
	ctx, cancel := a.requestContext()
	defer cancel()
	return a.api.post(ctx, kimiSessionPath(sessionID, kimiActionAbort), nil, nil)
}

// Stop ends the server and its captured descendants. It is safe to call more
// than once.
func (a *Agent) Stop() {
	if a.IsStopped() {
		return
	}
	a.NoteIntentionalStop()
	a.shutdownServer()
	// Close the stream before Process.Stop so it cannot redial during local
	// shutdown. Wait joins its goroutines.
	if a.stream != nil {
		a.stream.close()
	}
	a.Process.Stop()
	if a.endpoint != nil {
		a.endpoint.Close()
	}
}

// captureServerOwnership retains descendants before a control request can end
// their parent. A recorded exit prevents control of a later server at the same
// endpoint. The generic stop still performs local cleanup after this returns.
func (a *Agent) captureServerOwnership() bool {
	if a.ProcessExited() {
		return false
	}
	if cmd := a.Cmd(); cmd != nil && cmd.Process != nil {
		ctx, cancel := context.WithTimeout(context.Background(), kimiStopStepTimeout)
		if err := a.BindDescendants(ctx); err != nil {
			slog.Debug("kimi process ownership capture failed", "agent_id", a.AgentID(), "error", err)
		}
		cancel()
	}
	return !a.ProcessExited()
}

// shutdownServer runs the native protocol steps while this instance remains
// live. Descendants can change during abort, so shutdown captures them again.
func (a *Agent) shutdownServer() {
	if a.api == nil {
		return
	}
	a.Mu.Lock()
	sessionID, active := a.sessionID, a.turnActive
	a.Mu.Unlock()
	if active && kimiCheckID("session", sessionID) == nil {
		if !a.captureServerOwnership() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), kimiStopStepTimeout)
		if err := a.api.post(ctx, kimiSessionPath(sessionID, kimiActionAbort), nil, nil); err != nil {
			slog.Debug("kimi abort on stop", "agent_id", a.AgentID(), "error", err)
		}
		cancel()
	}
	if !a.captureServerOwnership() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), kimiStopStepTimeout)
	defer cancel()
	if err := a.api.post(ctx, kimiRouteShutdown, nil, nil); err != nil {
		slog.Debug("kimi shutdown request", "agent_id", a.AgentID(), "error", err)
	}
}

// Wait waits for the server to exit. It then ends the event stream, waits until
// the stream's goroutines return, and settles what the process left open.
//
// This is the one path that every exit takes, a crash included. A stream that
// outlived the server would redial the server's port with the bearer token
// every few seconds for the life of the worker, and it would keep the agent
// reachable. Every `kimi web` of the same home accepts that token, so a later
// server of that home on the same port would accept the subscribe, and its
// events would reach an agent that no longer runs.
//
// The manager can call Wait more than once, and each step is safe to repeat.
func (a *Agent) Wait() error {
	err := a.Process.Wait()
	a.closeStream()
	a.settleAfterExit(a.ProcessExitCompletion())
	return err
}

// closeStream ends the event stream and waits until its goroutines return, so
// no event is dispatched after the settle that follows. It then closes the idle
// connections of the endpoint.
func (a *Agent) closeStream() {
	if a.stream != nil {
		a.stream.close()
		a.stream.wait()
	}
	if a.endpoint != nil {
		a.endpoint.Close()
	}
}

// settleAfterExit ends what the process left open: the text each agent
// streamed, the tool calls that no result closed, and the turn flags.
// completion states how the process ended.
//
// A pending request needs nothing here: the worker withdraws every request of
// an agent whose process exited.
func (a *Agent) settleAfterExit(completion agent.MessageCompletion) {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	a.Mu.Lock()
	runs := make([]*kimiRun, 0, len(a.runs))
	for _, run := range a.runs {
		runs = append(runs, run)
	}
	a.turnActive = false
	a.turnSteerable = false
	a.manualCompactionActive = false
	a.Mu.Unlock()

	// Each run writes to its own transcript, so the order of the runs does not
	// matter.
	for _, run := range runs {
		sink := a.runSink(run)
		if sink == nil {
			continue
		}
		a.flushRun(run, sink, completion)
		a.closeOpenTools(run, sink, completion)
		a.Mu.Lock()
		childTurn := run.agentID != kimiMainAgentID && run.turnActive
		run.turnActive = false
		a.Mu.Unlock()
		if childTurn {
			if childID, linked := a.children.childOf(run.agentID); linked {
				a.publishChildTurn(childID, false)
			}
		}
	}
	a.ResetCumulativeOutput()
	a.PublishTurnActive()
	a.sink.ReportProgress(agent.ResetProgress())
}
