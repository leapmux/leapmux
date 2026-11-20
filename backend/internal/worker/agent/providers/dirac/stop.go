package dirac

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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
	err  error
}

// waitForDiracResumeCheckpoint is the ACP PrepareStop hook. It runs once after
// the base closes prompt admission and before pipe EOF. It sends at most one
// cancellation to make Dirac flush its index, then waits for the checkpoint,
// the process exit, a cancellation or the two-second budget -- whichever comes
// first.
func (a *Agent) waitForDiracResumeCheckpoint(snapshot acp.ACPStopSnapshot) {
	if snapshot.SessionID == a.resumedSession || a.checkpointProfile == "" {
		return
	}
	waitForDiracCheckpoint(diracCheckpointWait{
		agentID:      a.AgentID(),
		profile:      a.checkpointProfile,
		sessionID:    snapshot.SessionID,
		promptActive: snapshot.PromptActive,
		cancel:       snapshot.Cancel,
		processDone:  a.ProcessDone(),
		ctx:          a.Context(),
		clock:        a.archiveClock(),
		openArchive:  a.openCheckpointArchive,
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
	openArchive  func(string) (sessionstore.ArchiveRoot, error)
}

// waitForDiracCheckpoint polls through one reader at a time. A reader owns
// the archive handles and only retains the path, session ID and result channel
// if the deadline, process exit or context cancellation ends the wait first.
func waitForDiracCheckpoint(w diracCheckpointWait) {
	if w.profile == "" || !safeDiracComponent(w.sessionID) {
		return
	}
	deadline := w.clock.Now().Add(diracStopBudget)
	budget := w.clock.NewTimer(diracStopBudget, diracStopBudgetTimerTag)
	defer budget.Stop()

	profile, sessionID, openArchive := w.profile, w.sessionID, w.openArchive
	if openArchive == nil {
		openArchive = sessionstore.OpenArchiveRoot
	}
	results := make(chan diracCheckpointProbe, 1)
	startReader := func() {
		go func() {
			results <- probeDiracCheckpoint(profile, sessionID, openArchive)
		}()
	}
	warn := func(err error) {
		slog.Warn("the Dirac resume checkpoint could not be confirmed before shutdown",
			"agent_id", w.agentID, "session_id", w.sessionID, "error", err)
	}
	var poll *quartz.Timer
	var nextPoll <-chan time.Time
	defer func() {
		if poll != nil {
			poll.Stop()
		}
	}()

	startReader()
	cancelConsidered := false
	for {
		select {
		case probe := <-results:
			select {
			case <-w.processDone:
				return
			case <-w.ctx.Done():
				return
			default:
			}
			if !w.clock.Now().Before(deadline) {
				warn(context.DeadlineExceeded)
				return
			}
			if probe.err != nil {
				warn(probe.err)
				return
			}
			if probe.ready || !w.promptActive && !probe.task {
				return
			}
			if !cancelConsidered && w.cancel != nil {
				w.cancel()
			}
			cancelConsidered = true
			poll = w.clock.NewTimer(diracStopPollInterval, diracStopPollTimerTag)
			nextPoll = poll.C
		case <-nextPoll:
			poll, nextPoll = nil, nil
			if !w.clock.Now().Before(deadline) {
				warn(context.DeadlineExceeded)
				return
			}
			startReader()
		case <-w.processDone:
			return
		case <-w.ctx.Done():
			return
		case <-budget.C:
			warn(context.DeadlineExceeded)
			return
		}
	}
}

// probeDiracCheckpoint opens one native archive and closes it before the
// result reaches the waiting stop call.
func probeDiracCheckpoint(profile, sessionID string, openArchive func(string) (sessionstore.ArchiveRoot, error)) (result diracCheckpointProbe) {
	if profile == "" || !safeDiracComponent(sessionID) {
		return result
	}
	root, err := openArchive(profile)
	if err != nil {
		return diracCheckpointProbe{err: checkpointReadFailure(err)}
	}
	defer func() {
		if err := root.Close(); err != nil {
			result.ready = false
			result.err = errors.Join(result.err, err)
		}
	}()
	result = evaluateDiracCheckpoint(root, sessionID)
	if result.ready {
		opened, err := root.Stat(".")
		if err != nil {
			result.ready, result.err = false, checkpointReadFailure(err)
			return result
		}
		current, err := os.Stat(profile)
		if err != nil {
			result.ready, result.err = false, checkpointReadFailure(err)
			return result
		}
		result.ready = os.SameFile(opened, current)
	}
	return result
}

// evaluateDiracCheckpoint requires the observed profile to link the exact
// ACP session to its task. The same archive observation must contain one
// matching index record and a recoverable conversation for that task.
func evaluateDiracCheckpoint(root sessionstore.ArchiveRoot, sessionID string) (result diracCheckpointProbe) {
	files := &diracCheckpointFiles{identities: make(map[string]os.FileInfo)}
	archive := diracCheckpointArchive{ArchiveRoot: root, files: files}
	link, err := readDiracRuntimeLink(archive, sessionID)
	if failure := checkpointReadFailure(err); failure != nil {
		result.err = failure
		return result
	}
	records, historyErr := readDiracHistory(archive)
	if err != nil {
		for _, row := range records {
			if row.ULID == sessionID {
				result.task = true
				break
			}
		}
		result.err = checkpointReadFailure(historyErr)
		return result
	}
	result.task = true
	if historyErr != nil {
		result.err = checkpointReadFailure(historyErr)
		return result
	}
	if link.Mode != contracts.DiracModePlan && link.Mode != contracts.DiracModeAct ||
		!diracHistoryMatch(records, link.TaskID, sessionID) {
		return result
	}
	chain, err := sessionstore.OpenCheckedArchiveDirectoryChain(archive, "data", "tasks", link.TaskID)
	if err != nil {
		result.err = checkpointReadFailure(err)
		return result
	}
	defer func() {
		if err := chain.Close(); err != nil {
			result.ready = false
			result.err = errors.Join(result.err, err)
		}
	}()
	recoverable, err := diracConversationRecoverable(chain.Root())
	if err != nil {
		result.err = checkpointReadFailure(err)
		return result
	}
	if recoverable {
		result.ready, result.err = files.unchanged(root)
	}
	return result
}

func checkpointReadFailure(err error) error {
	if errors.Is(err, os.ErrPermission) || errors.Is(err, sessionstore.ErrArchiveFileModeOrSize) {
		return err
	}
	return nil
}

func diracConversationRecoverable(taskRoot sessionstore.ArchiveRoot) (bool, error) {
	baseline, err := readOptionalDiracFile(taskRoot, "api_conversation_history.baseline.jsonl")
	if err != nil {
		return false, err
	}
	var snapshot []byte
	if baseline == nil {
		snapshot, err = readOptionalDiracFile(taskRoot, "api_conversation_history.json")
		if err != nil {
			return false, err
		}
	}
	operations, err := readOptionalDiracFile(taskRoot, "api_conversation_history.jsonl")
	if err != nil {
		return false, err
	}
	if len(operations) > 0 && bytes.IndexByte(operations, '\n') < 0 {
		return false, nil
	}
	messages, err := decodeDiracAPIHistory(snapshot, baseline, operations)
	return err == nil && len(messages) > 0, nil
}

func readDiracHistory(root sessionstore.ArchiveRoot) ([]diracHistoryRecord, error) {
	raw, err := sessionstore.ReadRegularFileWithoutSymlinkAncestors(root, diracTaskHistoryLimit, "data", "state", "taskHistory.json")
	if err != nil {
		return nil, err
	}
	var records []diracHistoryRecord
	if err := json.Unmarshal(raw, &records); err != nil {
		return nil, err
	}
	return records, nil
}

// diracCheckpointArchive records each path checked by the private reader
// across all participating files, including paths checked for absence.
type diracCheckpointArchive struct {
	sessionstore.ArchiveRoot
	prefix string
	files  *diracCheckpointFiles
}

type diracCheckpointFiles struct {
	identities map[string]os.FileInfo
	changed    bool
}

func (r diracCheckpointArchive) OpenChild(name string) (sessionstore.ArchiveRoot, error) {
	child, err := r.ArchiveRoot.OpenChild(name)
	if err != nil {
		return nil, err
	}
	return diracCheckpointArchive{ArchiveRoot: child, prefix: filepath.Join(r.prefix, name), files: r.files}, nil
}

func (r diracCheckpointArchive) Lstat(name string) (os.FileInfo, error) {
	info, err := r.ArchiveRoot.Lstat(name)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	path := filepath.Join(r.prefix, name)
	if previous, seen := r.files.identities[path]; seen && !sameCheckpointPath(previous, info) {
		r.files.changed = true
	}
	r.files.identities[path] = info
	return info, err
}

func (f *diracCheckpointFiles) unchanged(root sessionstore.ArchiveRoot) (bool, error) {
	if f.changed {
		return false, nil
	}
	for path, observed := range f.identities {
		current, err := root.Lstat(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, checkpointReadFailure(fmt.Errorf("recheck the Dirac checkpoint path: %w", err))
		}
		if !sameCheckpointPath(observed, current) {
			return false, nil
		}
	}
	return true, nil
}

func sameCheckpointPath(before, after os.FileInfo) bool {
	if before == nil || after == nil {
		return before == nil && after == nil
	}
	if !os.SameFile(before, after) || before.Mode() != after.Mode() {
		return false
	}
	return before.IsDir() || before.Size() == after.Size() && before.ModTime().Equal(after.ModTime())
}
