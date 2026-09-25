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
// SIGTERM (measured on 2.0.2). A stop therefore:
//
//  1. aborts a running turn, which the server answers by killing the turn's
//     command the way a user's interrupt does;
//  2. records the process groups of every process the server started, while
//     they are still its descendants;
//  3. asks the server to shut down, and stops the process;
//  4. kills every recorded group that outlived the server.
//
// Windows does not need step 4: the server starts its commands without
// detaching them there, so they stay in the job object the worker assigned the
// server to, and the job's teardown ends them.
//
// Wait, which the manager calls after every exit, ends the event stream and
// settles what the process left open. It does that for a crash too, when
// nobody calls Stop.

// kimiStopStepTimeout limits each request the stop sends. A server that does not
// answer is killed anyway.
const kimiStopStepTimeout = 2 * time.Second

// Interrupt aborts the main agent's running turn. A turn that is not running
// needs nothing.
func (a *Agent) Interrupt() error {
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

// Stop ends the server and every process it started. It is safe to call more
// than once.
func (a *Agent) Stop() {
	if a.IsStopped() {
		return
	}
	a.NoteIntentionalStop()
	a.Mu.Lock()
	sessionID, active := a.sessionID, a.turnActive
	a.Mu.Unlock()

	if active && kimiCheckID("session", sessionID) == nil && a.api != nil {
		ctx, cancel := context.WithTimeout(context.Background(), kimiStopStepTimeout)
		if err := a.api.post(ctx, kimiSessionPath(sessionID, kimiActionAbort), nil, nil); err != nil {
			slog.Debug("kimi abort on stop", "agent_id", a.AgentID(), "error", err)
		}
		cancel()
	}

	var groups []int
	if cmd := a.Cmd(); cmd != nil && cmd.Process != nil && a.descendantGroups != nil {
		groups = a.descendantGroups(cmd.Process.Pid)
	}
	if a.api != nil {
		ctx, cancel := context.WithTimeout(context.Background(), kimiStopStepTimeout)
		if err := a.api.post(ctx, kimiRouteShutdown, nil, nil); err != nil {
			slog.Debug("kimi shutdown request", "agent_id", a.AgentID(), "error", err)
		}
		cancel()
	}
	// The stream closes before the server exits, so the reader does not start to
	// redial a server that goes away on purpose. Wait joins its goroutines.
	if a.stream != nil {
		a.stream.close()
	}
	a.Process.Stop()
	killKimiGroups(groups)
	if a.endpoint != nil {
		a.endpoint.Close()
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
