package kimi

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

type kimiInvocationRecovery struct {
	sessionID string
	applied   bool
}

// beginInvocationRecovery runs before a reconnect sends a subscription.
// Its returned callback runs through the stream's ordered dispatch barrier.
func (a *Agent) beginInvocationRecovery(_ context.Context, sessionID string) func(bool) {
	a.dispatchMu.Lock()
	defer a.finishDispatch()
	a.Mu.Lock()
	current := a.sessionID == sessionID && !a.ProcessExitedLocked() && !a.StoppedLocked()
	a.Mu.Unlock()
	if !current {
		return nil
	}
	recovery := &kimiInvocationRecovery{sessionID: sessionID}
	a.invocationRecovery = recovery
	return func(accepted bool) {
		a.dispatchMu.Lock()
		defer a.finishDispatch()
		if a.invocationRecovery != recovery {
			return
		}
		if !accepted {
			a.invocationRecovery = nil
			return
		}
		recovery.applied = true
	}
}

func (a *Agent) finishInvocationRecoveryIfReady() {
	recovery := a.invocationRecovery
	if recovery == nil || !recovery.applied {
		return
	}
	a.Mu.Lock()
	if a.sessionID != recovery.sessionID || a.ProcessExitedLocked() || a.StoppedLocked() {
		a.invocationRecovery = nil
		a.Mu.Unlock()
		return
	}
	runs := make([]*kimiRun, 0, len(a.runs))
	for _, run := range a.runs {
		if run.pendingNativeRecovery {
			a.Mu.Unlock()
			return
		}
		runs = append(runs, run)
	}
	a.Mu.Unlock()
	a.invocationRecovery = nil
	for _, run := range runs {
		for _, invocation := range run.childInvocations {
			a.projectChildInvocation(run, invocation)
		}
	}
	for _, run := range runs {
		a.replayNativeTasksOwnedBy(run.agentID)
	}
}

func (a *Agent) scheduleDiscoveredNativeTask(run *kimiRun, target *kimiDiscoveredTask) {
	if target.complete || !target.invocation.eligible || a.invocationRecovery != nil || a.currentDiscoveredTaskOwner(target) == nil {
		return
	}
	a.Mu.Lock()
	local := a.tasks[target.address]
	var state kimiNativeTaskState
	if local != nil {
		state = local.native
	}
	a.Mu.Unlock()
	if local != nil && kimiDiscoveredTaskMatches(target, state) {
		target.complete = true
		a.applyNativeTaskStateBound(target.address.ownerAgentID, state, target.invocation)
		return
	}
	if a.stream == nil {
		return
	}
	key := fmt.Sprintf("discover/%q/%q/%q/%p", target.invocation.identity.sessionID, run.agentID, target.address.taskID, target.invocation)
	a.taskRecoveryWork.Run(key, func() { a.recoverDiscoveredNativeTask(target) })
}

// recoverDiscoveredNativeTask verifies an address without inventing running task state.
func (a *Agent) recoverDiscoveredNativeTask(target *kimiDiscoveredTask) {
	sessionID := target.invocation.identity.sessionID
	ctx, cancel := a.requestContext()
	state, err := a.stream.readTaskState(ctx, sessionID, target.address)
	cancel()
	if err != nil {
		slog.Warn("recover Kimi task identity from native output", "agent_id", a.AgentID(), "task_id", target.address.taskID, "error", err)
		return
	}
	if state.validate() != nil || state.TaskID != target.address.taskID {
		slog.Warn("refuse invalid Kimi task state", "agent_id", a.AgentID(), "task_id", target.address.taskID)
		return
	}
	matchesCreator := kimiDiscoveredTaskMatches(target, state)
	a.dispatchMu.Lock()
	defer a.finishDispatch()
	a.Mu.Lock()
	current := a.sessionID == sessionID && !a.StoppedLocked() && !a.ProcessExitedLocked()
	a.Mu.Unlock()
	if !current {
		return
	}
	if a.observedDiscoveredTaskOwner(target) == nil {
		return
	}
	if !matchesCreator || a.invocationRecovery != nil || a.currentDiscoveredTaskOwner(target) == nil {
		// Exact native state is independent from creator attribution. Recovery or
		// competing claims can refuse the join without discarding its outcome.
		a.applyNativeTaskState(target.address.ownerAgentID, state)
		return
	}
	target.complete = true
	a.applyNativeTaskStateBound(target.address.ownerAgentID, state, target.invocation)
}

// retainDiscoveredTask keeps one immutable descriptor through repeated frame replay.
func (invocation *kimiChildInvocation) retainDiscoveredTask(target *kimiDiscoveredTask) {
	if previous := invocation.taskTarget; previous != nil && previous.address == target.address && previous.kind == target.kind && previous.childID == target.childID && previous.command == target.command && previous.explicitBackground == target.explicitBackground {
		return
	}
	invocation.taskTarget = target
}

func kimiDiscoveredShellCommandMatches(target *kimiDiscoveredTask, state kimiNativeTaskState) bool {
	parentMatches := state.ParentToolCallID == target.invocation.identity.toolCallID
	if !target.explicitBackground && (!parentMatches || state.Command == "") {
		return false
	}
	if state.Command == "" || state.Command == target.command {
		return true
	}
	// Kimi rewrites Windows NUL redirects before recording the shell task.
	// Require the native parent call too before accepting that alternate command.
	return parentMatches && state.Command == kimiWindowsShellCommand(target.command)
}

// This is Kimi's Windows redirect rewrite, including JavaScript whitespace.
var kimiWindowsNullRedirect = regexp.MustCompile(`(\d?&?>+[[:space:]\x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]*)[Nn][Uu][Ll]`)

func kimiWindowsShellCommand(command string) string {
	var result strings.Builder
	position := 0
	for _, match := range kimiWindowsNullRedirect.FindAllStringSubmatchIndex(command, -1) {
		if match[1] < len(command) {
			next, _ := utf8.DecodeRuneInString(command[match[1]:])
			space := next == '\uFEFF' || next != '\u0085' && unicode.IsSpace(next)
			if !space && !strings.ContainsRune("|&;)", next) {
				continue
			}
		}
		result.WriteString(command[position:match[3]])
		result.WriteString("/dev/null")
		position = match[1]
	}
	if position == 0 {
		return command
	}
	result.WriteString(command[position:])
	return result.String()
}

func kimiDiscoveredTaskMatches(target *kimiDiscoveredTask, state kimiNativeTaskState) bool {
	return state.validate() == nil && state.TaskID == target.address.taskID && state.Kind == target.kind && state.ChildAgentID == target.childID && state.Detached &&
		(state.ParentToolCallID == "" || state.ParentToolCallID == target.invocation.identity.toolCallID) &&
		(target.kind != "shell" || kimiDiscoveredShellCommandMatches(target, state))
}

// The caller holds dispatchMu. Historical creator evidence remains valid after
// withdrawal, but it grants no current parent or child publication rights.
func (a *Agent) observedDiscoveredTaskOwner(target *kimiDiscoveredTask) *kimiRun {
	invocation := target.invocation
	a.Mu.Lock()
	current := a.sessionID == invocation.identity.sessionID && invocation.identity.agentID == target.address.ownerAgentID
	owner := a.runs[target.address.ownerAgentID]
	a.Mu.Unlock()
	if !current || owner == nil || owner.childInvocations[invocation.identity] != invocation || invocation.taskTarget != target {
		return nil
	}
	return owner
}

func (a *Agent) currentDiscoveredTaskOwner(target *kimiDiscoveredTask) *kimiRun {
	owner := a.observedDiscoveredTaskOwner(target)
	if owner == nil {
		return nil
	}
	invocation := target.invocation
	a.Mu.Lock()
	task := a.tasks[target.address]
	competing := task != nil && task.boundInvocation != nil && task.boundInvocation != invocation
	a.Mu.Unlock()
	if competing {
		return nil
	}
	for _, other := range owner.childInvocations {
		if other == invocation {
			continue
		}
		if other.taskTarget != nil && other.taskTarget.address == target.address {
			return nil
		}
		if target.kind == "shell" && !target.explicitBackground && other.identity.toolCallID == invocation.identity.toolCallID {
			return nil
		}
	}
	return owner
}
