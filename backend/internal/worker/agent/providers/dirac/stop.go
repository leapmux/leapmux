package dirac

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

// Dirac exits on stdin EOF without flushing its debounced history index. The
// pre-stop hook confirms a usable native checkpoint while the process still
// runs, so an orderly shutdown does not lose the resume link. The check makes
// no claim about fresh writes from later prompts.

const (
	// diracStopBudget bounds the whole pre-stop confirmation. It starts before
	// any file observation so even the first read is inside the budget.
	diracStopBudget = 2 * time.Second
	// diracStopPollInterval is the gap between two file reads. A confirmed
	// checkpoint returns at once and does not wait for the next tick.
	diracStopPollInterval = 50 * time.Millisecond

	diracStopBudgetTimerTag = "dirac-stop-budget"
	diracStopPollTimerTag   = "dirac-stop-poll"
)

// diracCheckpointProbe is the result of one file observation.
type diracCheckpointProbe struct {
	// ready reports a usable native checkpoint for the session.
	ready bool
	// task reports that a native task is linked to the session, even when the
	// checkpoint is not yet usable. It separates "active work worth waiting for"
	// from an idle session with nothing to flush.
	task bool
}

// waitForDiracResumeCheckpoint is the ACP PrepareStop hook. It runs once after
// the base closes prompt admission and before pipe EOF. It sends at most one
// cancellation to make Dirac flush its index, then waits for the checkpoint,
// the process exit, a cancellation or the two-second budget -- whichever comes
// first.
func (a *Agent) waitForDiracResumeCheckpoint(snapshot acp.ACPStopSnapshot) {
	waitForDiracCheckpoint(diracCheckpointWait{
		agentID:      a.AgentID(),
		profile:      a.root,
		sessionID:    snapshot.SessionID,
		promptActive: snapshot.PromptActive,
		cancel:       snapshot.Cancel,
		processDone:  a.Process.ProcessDone(),
		ctx:          a.Process.Context(),
		clock:        a.archiveClock(),
	})
}

// diracCheckpointWait carries the inputs of one pre-stop checkpoint wait.
type diracCheckpointWait struct {
	agentID      string
	profile      string
	sessionID    string
	promptActive bool
	cancel       func()
	processDone  <-chan struct{}
	ctx          context.Context
	clock        quartz.Clock
}

// waitForDiracCheckpoint polls for a usable checkpoint through one reader at a
// time. Each read owns and closes its file handles and touches no process or
// sink state, so a read that outlives the wait disposes its handles and its
// late result is dropped. Missing or changing files are pending; a nonrecoverable
// state keeps the wait open until the budget expires, when the hook logs that
// the index could not be confirmed and hands control back to normal stop.
func waitForDiracCheckpoint(w diracCheckpointWait) {
	if w.profile == "" || !safeDiracComponent(w.sessionID) {
		return
	}
	budget := w.clock.NewTimer(diracStopBudget, diracStopBudgetTimerTag)
	defer budget.Stop()

	results := make(chan diracCheckpointProbe, 1)
	startReader := func() {
		go func() {
			ready, task := probeDiracCheckpoint(w.profile, w.sessionID)
			select {
			case results <- diracCheckpointProbe{ready: ready, task: task}:
			default:
				// The wait returned first; drop the late result.
			}
		}()
	}

	startReader()
	sentCancel := false
	for {
		select {
		case probe := <-results:
			if probe.ready {
				return
			}
			if !sentCancel {
				if !w.promptActive && !probe.task {
					return // idle: nothing to checkpoint.
				}
				if w.cancel != nil {
					w.cancel()
				}
				sentCancel = true
			}
			tick := w.clock.NewTimer(diracStopPollInterval, diracStopPollTimerTag)
			select {
			case <-tick.C:
				startReader()
			case <-w.processDone:
				tick.Stop()
				return
			case <-w.ctx.Done():
				tick.Stop()
				return
			case <-budget.C:
				tick.Stop()
				slog.Warn("the Dirac resume checkpoint could not be confirmed before shutdown", "agent_id", w.agentID, "session_id", w.sessionID)
				return
			}
		case <-w.processDone:
			return
		case <-w.ctx.Done():
			return
		case <-budget.C:
			slog.Warn("the Dirac resume checkpoint could not be confirmed before shutdown", "agent_id", w.agentID, "session_id", w.sessionID)
			return
		}
	}
}

// probeDiracCheckpoint opens the native archive and evaluates the ready
// condition once. It closes every handle before returning.
func probeDiracCheckpoint(profile, sessionID string) (ready, task bool) {
	if profile == "" || !safeDiracComponent(sessionID) {
		return false, false
	}
	root, err := sessionstore.OpenArchiveRoot(profile)
	if err != nil {
		return false, false
	}
	defer root.Close()
	return evaluateDiracCheckpoint(root, sessionID)
}

// evaluateDiracCheckpoint applies the ready condition under one open archive
// root. A usable checkpoint needs a safe, stable task ID, a runtime mode of
// plan or act, exactly one matching task-and-session history row, and readable
// native conversation data under data/tasks/<taskID>/.
func evaluateDiracCheckpoint(root sessionstore.ArchiveRoot, sessionID string) (ready, task bool) {
	link, err := readDiracRuntimeLink(root, sessionID)
	if err != nil {
		// No runtime link yet. A session that already owns a history row still
		// counts as active work; a started task that wrote nothing does not, and
		// the caller's promptActive flag covers that case.
		return false, diracSessionHistoryPresent(root, sessionID)
	}
	task = true
	if link.Mode != contracts.DiracModePlan && link.Mode != contracts.DiracModeAct {
		return false, true
	}
	records, ok := readDiracHistory(root)
	if !ok || !diracHistoryMatch(records, link.TaskID, sessionID) {
		return false, true
	}
	chain, err := sessionstore.OpenCheckedArchiveDirectoryChain(root, "data", "tasks", link.TaskID)
	if err != nil {
		return false, true
	}
	recoverable := diracConversationRecoverable(chain.Root())
	if closeErr := chain.Close(); closeErr != nil {
		return false, true
	}
	if !recoverable {
		return false, true
	}
	// Recheck the runtime link across the observation: a task ID that moved
	// while the conversation was read is not stable.
	current, err := readDiracRuntimeLink(root, sessionID)
	if err != nil || current != link {
		return false, true
	}
	return true, true
}

// diracConversationRecoverable reports whether the native conversation data
// under a task directory decodes into recoverable history. A framed baseline, a
// legacy snapshot, or an empty history at offset -1 seeds the read, and any
// complete operations are applied. No conversation files, an incomplete first
// record, an invalid baseline or a gapped append gives no checkpoint.
func diracConversationRecoverable(taskRoot sessionstore.ArchiveRoot) bool {
	baseline, err := readOptionalDiracFile(taskRoot, "api_conversation_history.baseline.jsonl")
	if err != nil {
		return false
	}
	var snapshot []byte
	if baseline == nil {
		snapshot, err = readOptionalDiracFile(taskRoot, "api_conversation_history.json")
		if err != nil {
			return false
		}
	}
	operations, err := readOptionalDiracFile(taskRoot, "api_conversation_history.jsonl")
	if err != nil {
		return false
	}
	if baseline == nil && snapshot == nil && operations == nil {
		return false
	}
	messages, err := decodeDiracAPIHistory(snapshot, baseline, operations)
	return err == nil && len(messages) > 0
}

// readDiracHistory reads the task history index, returning false when it is
// absent or malformed.
func readDiracHistory(root sessionstore.ArchiveRoot) ([]diracHistoryRecord, bool) {
	raw, err := sessionstore.ReadRegularFileWithoutSymlinkAncestors(root, diracTaskHistoryLimit, "data", "state", "taskHistory.json")
	if err != nil {
		return nil, false
	}
	var records []diracHistoryRecord
	if json.Unmarshal(raw, &records) != nil {
		return nil, false
	}
	return records, true
}

// diracSessionHistoryPresent reports whether the history index already owns a
// row for the session.
func diracSessionHistoryPresent(root sessionstore.ArchiveRoot, sessionID string) bool {
	records, ok := readDiracHistory(root)
	if !ok {
		return false
	}
	for _, record := range records {
		if record.ULID == sessionID {
			return true
		}
	}
	return false
}
