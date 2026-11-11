package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/leapmux/leapmux/internal/util/userid"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
	"github.com/leapmux/leapmux/util/pathutil"
)

// creationRuntime is the closed set of resource-specific recovery inputs.
type creationRuntime interface{ startupIntent() *creationStartupIntent }

type payloadCreationRuntime struct{}

func (*payloadCreationRuntime) startupIntent() *creationStartupIntent    { return nil }
func (r *agentCreationRuntime) startupIntent() *creationStartupIntent    { return &r.Startup }
func (r *terminalCreationRuntime) startupIntent() *creationStartupIntent { return &r.Startup }

// creationStartupIntent keeps one validated plan and its durable Git checkpoint.
type creationStartupIntent struct {
	Plan           gitModePlan
	ExpectedCommit string
	OriginalHead   string
	OriginalBranch string
	Started        bool
	Result         *gitModeResult
}

func (svc *Service) prepareCreationStartup(ctx context.Context, plan gitModePlan) (creationStartupIntent, error) {
	intent := creationStartupIntent{Plan: plan}
	switch plan.Mode {
	case gitModeCreateWorktree, gitModeCreateBranch, gitModeCheckoutBranch:
		info, err := queryGitPathInfo(ctx, plan.WorkingDir)
		if err != nil {
			return intent, err
		}
		intent.Plan.RepoRoot = info.RepoRoot
		intent.OriginalHead = info.HeadSHA
		intent.OriginalBranch = info.BranchName
		reference := "HEAD"
		switch plan.Mode {
		case gitModeCreateWorktree:
			reference = plan.StartPoint
		case gitModeCreateBranch:
			if plan.BaseBranch != "" {
				reference = plan.BaseBranch
			}
		case gitModeCheckoutBranch:
			reference = plan.CheckoutTarget
		default:
			return intent, errors.New("unsupported Git mutation for creation")
		}
		commit, err := gitutil.Output(ctx, plan.WorkingDir, "rev-parse", "--verify", reference+"^{commit}")
		if err != nil {
			return intent, fmt.Errorf("resolve the creation's Git commit: %w", err)
		}
		intent.ExpectedCommit = strings.TrimSpace(commit)
		if intent.ExpectedCommit == "" {
			return intent, errors.New("the creation's Git commit is empty")
		}
		if plan.Mode == gitModeCreateWorktree {
			intent.Plan.StartPoint = intent.ExpectedCommit
		}
		if plan.Mode == gitModeCreateBranch {
			intent.Plan.BaseBranch = intent.ExpectedCommit
		}
	default:
	}
	return intent, nil
}

func (op *creationOperation) saveRuntime(ctx context.Context) error {
	validatedOwner, ownerOK := userid.OwnerFilter(op.caller.UserID)
	if !ownerOK {
		return errCreationOwnerMissing
	}
	queryKey := op.dbKey()
	queryKey.UserID = validatedOwner
	payload, err := encodeCreationRecoveryInputs(op.runtime, op.caller.Scopes)
	if err != nil {
		return err
	}
	unlock := op.coordinator.lock(op.key())
	defer unlock()
	changed, err := op.coordinator.svc.Queries.SaveTabCreationRuntime(ctx, db.SaveTabCreationRuntimeParams{RuntimePayload: payload, UserID: validatedOwner, WorkerID: queryKey.WorkerID, TabID: op.key().tabID, State: creationPublished})
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("tab startup no longer owns its creation")
	}
	return nil
}

// runCreationPhase0 recovers a checkpoint without repeating a completed mutation.
func (op *creationOperation) runCreationPhase0(ctx context.Context) (gitModeResult, error) {
	intent := op.runtime.startupIntent()
	if intent == nil {
		return gitModeResult{}, errors.New("tab creation has no startup intent")
	}
	if intent.Result != nil {
		result := *intent.Result
		result.Rollback = gitModeRollback{}
		if err := op.validateSavedCheckpoint(ctx, intent, result); err != nil {
			return result, err
		}
		return result, nil
	}
	var result gitModeResult
	var err error
	recovered := false
	if intent.Started {
		result, recovered, err = op.recoverCreationGit(ctx, intent)
		if err != nil {
			return result, err
		}
	}
	if !recovered {
		intent.Started = true
		if err := op.saveRuntime(ctx); err != nil {
			return gitModeResult{}, err
		}
		result, err = op.coordinator.svc.executeGitMode(ctx, intent.Plan)
		if err != nil {
			return result, err
		}
	}
	intent.Result = &result
	if err := op.checkpointPhase0(ctx, result); err != nil {
		intent.Result = nil
		return result, err
	}
	return result, nil
}

func (op *creationOperation) checkpointPhase0(ctx context.Context, result gitModeResult) error {
	validatedOwner, ownerOK := userid.OwnerFilter(op.caller.UserID)
	if !ownerOK {
		return errCreationOwnerMissing
	}
	payload, err := encodeCreationRecoveryInputs(op.runtime, op.caller.Scopes)
	if err != nil {
		return err
	}
	svc := op.coordinator.svc
	unlock := op.coordinator.lock(op.key())
	defer unlock()
	tx, err := svc.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := svc.Queries.WithTx(tx)
	live, err := op.resourceLive(ctx, q)
	if err != nil {
		return err
	}
	if !live {
		return errors.New("the tab closed or was archived during Git startup")
	}
	changed, err := q.SaveTabCreationRuntime(ctx, db.SaveTabCreationRuntimeParams{RuntimePayload: payload, UserID: validatedOwner, WorkerID: svc.WorkerID, TabID: op.key().tabID, State: creationPublished})
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("tab creation was canceled during Git startup")
	}
	if result.WorktreeID != "" {
		if err := q.AddWorktreeTab(ctx, db.AddWorktreeTabParams{WorktreeID: result.WorktreeID, TabType: op.publication.GetTabType(), TabID: op.key().tabID, UserID: worktreeTabUserID(op.publication.GetTabType(), op.key().owner)}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (op *creationOperation) recoverCreationGit(ctx context.Context, intent *creationStartupIntent) (gitModeResult, bool, error) {
	svc := op.coordinator.svc
	plan := intent.Plan
	unlock := svc.lockGitIndex(plan.RepoRoot)
	defer unlock()
	adoptCurrent := func() (gitModeResult, bool, error) {
		result, err := svc.executeUseCurrent(ctx, plan)
		return result, true, err
	}
	matchesBase := func(directory, head string) bool {
		return intent.ExpectedCommit != "" && head != "" && gitutil.Run(ctx, directory, "merge-base", "--is-ancestor", intent.ExpectedCommit, head) == nil
	}
	switch plan.Mode {
	case gitModeCreateWorktree:
		info, probeErr := queryGitPathInfo(ctx, plan.WorktreePath)
		if probeErr == nil {
			if !info.IsWorktree || pathutil.Canonicalize(info.TopLevel) != pathutil.Canonicalize(plan.WorktreePath) || pathutil.Canonicalize(info.RepoRoot) != pathutil.Canonicalize(plan.RepoRoot) || info.BranchName != plan.BranchName || !matchesBase(plan.WorktreePath, info.HeadSHA) {
				return gitModeResult{}, false, errors.New("the interrupted creation's worktree conflicts with current Git state; existing files were kept")
			}
			worktreeID, err := svc.ensureTrackedWorktreeWith(ctx, plan.WorktreePath, plan.RepoRoot, plan.BranchName)
			return gitModeResult{WorkingDir: plan.WorktreePath, WorktreeID: worktreeID, RepoRoot: plan.RepoRoot}, true, err
		}
		if _, statErr := os.Stat(plan.WorktreePath); !errors.Is(statErr, os.ErrNotExist) {
			return gitModeResult{}, false, errors.New("the interrupted creation's path is unavailable or already occupied; existing files were kept")
		}
		branchCommit, branchErr := gitutil.Output(ctx, plan.RepoRoot, "rev-parse", "--verify", "refs/heads/"+plan.BranchName+"^{commit}")
		if branchErr != nil {
			return gitModeResult{}, false, nil
		}
		if strings.TrimSpace(branchCommit) != intent.ExpectedCommit {
			return gitModeResult{}, false, errors.New("the interrupted creation's branch differs from its expected commit; existing files were kept")
		}
		if err := gitutil.Run(ctx, plan.RepoRoot, "worktree", "add", plan.WorktreePath, plan.BranchName); err != nil {
			return gitModeResult{}, false, err
		}
		worktreeID, err := svc.ensureTrackedWorktreeWith(ctx, plan.WorktreePath, plan.RepoRoot, plan.BranchName)
		return gitModeResult{WorkingDir: plan.WorktreePath, WorktreeID: worktreeID, RepoRoot: plan.RepoRoot}, true, err
	case gitModeCreateBranch, gitModeCheckoutBranch:
		info, err := queryGitPathInfo(ctx, plan.WorkingDir)
		if err != nil {
			return gitModeResult{}, false, err
		}
		target := plan.BranchName
		if plan.Mode == gitModeCheckoutBranch {
			target = plan.CheckoutTarget
		}
		if info.BranchName == target && matchesBase(plan.WorkingDir, info.HeadSHA) {
			return adoptCurrent()
		}
		if info.BranchName != intent.OriginalBranch || info.HeadSHA != intent.OriginalHead {
			return gitModeResult{}, false, errors.New("the checkout changed after startup was interrupted; existing Git state was kept")
		}
		if plan.Mode == gitModeCreateBranch {
			if _, err := gitutil.Output(ctx, plan.WorkingDir, "rev-parse", "--verify", "refs/heads/"+target); err == nil {
				return gitModeResult{}, false, errors.New("the interrupted creation's branch exists on another checkout; existing Git state was kept")
			}
		}
		return gitModeResult{}, false, nil
	default:
		return gitModeResult{}, false, nil
	}
}

type observedPublicationLifecycle struct {
	state   *leapmuxv1.ResolveTabPublicationResponse
	archive *archiveObservation
}

// readPublicationLifecycle captures archive ordering before each authoritative read.
// It does not change resources or creation state, so completed replay can use it.
func (op *creationOperation) readPublicationLifecycle() (*observedPublicationLifecycle, error) {
	c := op.coordinator
	for {
		observation := c.svc.beginArchiveObservation()
		ctx, cancel := context.WithTimeout(c.ctx, c.svc.creationAttemptTimeout())
		result, err := c.svc.ResolveTabPublication(ctx, op.publication)
		cancel()
		valid := err == nil && result.GetReceipt().GetRejected() != nil
		if err == nil && op.matchesPublication(result.GetReceipt().GetPublished()) {
			switch result.GetPresence() {
			case leapmuxv1.TabPublicationPresence_TAB_PUBLICATION_PRESENCE_HOSTED:
				valid = result.GetArchiveState() == leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE || result.GetArchiveState() == leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED
			case leapmuxv1.TabPublicationPresence_TAB_PUBLICATION_PRESENCE_ABSENT, leapmuxv1.TabPublicationPresence_TAB_PUBLICATION_PRESENCE_REASSIGNED:
				valid = true
			default:
			}
		}
		if valid {
			return &observedPublicationLifecycle{state: result, archive: observation}, nil
		}
		observation.done()
		if err := c.waitRetry(); err != nil {
			return nil, err
		}
	}
}

func (op *creationOperation) resolveStartupLifecycle() (*observedPublicationLifecycle, error) {
	observed, err := op.readPublicationLifecycle()
	if err != nil {
		return nil, err
	}
	if observed.state.GetReceipt().GetRejected() != nil {
		observed.archive.done()
		return nil, op.finishUnavailablePublication("the Hub canceled this tab creation")
	}
	return observed, nil
}

// finishUnavailablePublication ends restoration without removing the worktree
// link. A REMOVE close can still be in transit after its Hub tombstone commits.
func (op *creationOperation) finishUnavailablePublication(message string) error {
	validatedOwner, ownerOK := userid.OwnerFilter(op.caller.UserID)
	if !ownerOK {
		return errCreationOwnerMissing
	}
	queryKey := op.dbKey()
	queryKey.UserID = validatedOwner
	c := op.coordinator
	unlock := c.lock(op.key())
	defer unlock()
	row, err := c.svc.Queries.GetTabCreation(c.ctx, queryKey)
	if err != nil {
		return err
	}
	if row.State == creationPending || row.State == creationPublished {
		changed, err := c.svc.Queries.RejectTabCreation(c.ctx, db.RejectTabCreationParams{State: creationCanceled, FailureMessage: message, UserID: validatedOwner, WorkerID: c.svc.WorkerID, TabID: op.key().tabID, State_2: row.State})
		if err != nil {
			return err
		}
		if changed != 1 {
			return errors.New("the creation changed before its unavailable outcome was saved")
		}
	}
	return &creationAbortedError{message: message}
}

// applyStartupLifecycle orders flags while creation still owns admission.
// Archive teardown joins the claim only after handoff to a process goroutine.
func (op *creationOperation) applyStartupLifecycle(observed *observedPublicationLifecycle) (bool, error) {
	defer observed.archive.done()
	result := observed.state
	svc := op.coordinator.svc
	if result.GetPresence() != leapmuxv1.TabPublicationPresence_TAB_PUBLICATION_PRESENCE_HOSTED {
		return false, op.finishUnavailablePublication("the Hub no longer hosts this tab on this Worker")
	}
	flag, err := workspaceArchiveFlag(result.GetArchiveState())
	if err != nil {
		return false, err
	}
	tabs, err := archiveTabIDs(op.caller.UserID, []*leapmuxv1.TabIdent{{TabType: op.publication.GetTabType(), TabId: op.key().tabID}})
	if err != nil {
		return false, err
	}
	unlock := svc.lockArchiveTabs(tabs)
	defer unlock()
	if err := observed.archive.accept(tabs.lockKeys()); err != nil {
		return false, err
	}
	_, err = svc.persistArchiveFlags(op.coordinator.ctx, flag, tabs)
	return !flag, err
}

type creationStartupOutcomeKind uint8

const (
	creationStartupIncomplete creationStartupOutcomeKind = iota
	creationStartupSucceeded
	creationStartupFailed
	creationStartupInterrupted
)

type creationStartupOutcome struct {
	kind    creationStartupOutcomeKind
	message string
}

// finishCreationStartup keeps the claim until its outcome is durable. Archive
// locks cannot be taken here because archival holds them while joining this claim.
func (svc *Service) finishCreationStartup(op *creationOperation, outcome creationStartupOutcome) {
	if op == nil || svc.shuttingDown.Load() {
		return
	}
	for {
		unlock := op.coordinator.lock(op.key())
		err := svc.checkpointCreationOutcome(op, outcome)
		unlock()
		if err == nil {
			return
		}
		if err := op.coordinator.waitRetry(); err != nil {
			return
		}
	}
}
func (svc *Service) checkpointCreationOutcome(op *creationOperation, outcome creationStartupOutcome) error {
	validatedOwner, ownerOK := userid.OwnerFilter(op.caller.UserID)
	if !ownerOK {
		return errCreationOwnerMissing
	}
	queryKey := op.dbKey()
	queryKey.UserID = validatedOwner
	ctx := op.coordinator.ctx
	tx, err := svc.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := svc.Queries.WithTx(tx)
	record, err := q.GetTabCreation(ctx, queryKey)
	if err != nil {
		return err
	}
	if record.State != creationPublished {
		return nil
	}
	closed, archived := false, false
	switch op.publication.GetTabType() {
	case leapmuxv1.TabType_TAB_TYPE_AGENT:
		resource, readErr := q.GetAgentByID(ctx, record.TabID)
		if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
			return readErr
		}
		closed = errors.Is(readErr, sql.ErrNoRows) || resource.ClosedAt.Valid
		archived = resource.WorkspaceArchived
	case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
		resource, readErr := q.GetTerminal(ctx, record.TabID)
		if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
			return readErr
		}
		closed = errors.Is(readErr, sql.ErrNoRows) || resource.ClosedAt.Valid
		archived = resource.WorkspaceArchived
	default:
		return errors.New("this tab kind has no startup outcome")
	}
	finalState := creationCommitted
	if closed {
		finalState = creationCanceled
	} else {
		if archived || outcome.kind == creationStartupIncomplete || outcome.kind == creationStartupInterrupted {
			return nil
		}
		message := ""
		switch outcome.kind {
		case creationStartupSucceeded:
		case creationStartupFailed:
			message = outcome.message
			if message == "" {
				return errors.New("a failed startup has no explanation")
			}
		default:
			return errors.New("the startup outcome is invalid")
		}
		switch op.publication.GetTabType() {
		case leapmuxv1.TabType_TAB_TYPE_AGENT:
			err = q.SetAgentStartupError(ctx, db.SetAgentStartupErrorParams{ID: record.TabID, StartupError: message})
		case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
			err = q.SetTerminalStartupError(ctx, db.SetTerminalStartupErrorParams{ID: record.TabID, StartupError: message})
		default:
			return errors.New("this tab kind has no startup outcome")
		}
		if err != nil {
			return err
		}
	}
	changed, err := q.CompletePublishedTabCreation(ctx, db.CompletePublishedTabCreationParams{State: finalState, UserID: validatedOwner, WorkerID: record.WorkerID, TabID: record.TabID, State_2: creationPublished})
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("tab creation changed during its outcome checkpoint")
	}
	return tx.Commit()
}

// finishClosedResource resolves a known closed row without replaying creation.
func (op *creationOperation) finishClosedResource() error {
	validatedOwner, ownerOK := userid.OwnerFilter(op.caller.UserID)
	if !ownerOK {
		return errCreationOwnerMissing
	}
	queryKey := op.dbKey()
	queryKey.UserID = validatedOwner
	c := op.coordinator
	svc := c.svc
	unlock := c.lock(op.key())
	row, err := svc.Queries.GetTabCreation(c.ctx, queryKey)
	if err != nil {
		unlock()
		return err
	}
	if row.State == creationPublished {
		_, err = svc.Queries.CompletePublishedTabCreation(c.ctx, db.CompletePublishedTabCreationParams{State: creationCanceled, UserID: validatedOwner, WorkerID: row.WorkerID, TabID: row.TabID, State_2: creationPublished})
		unlock()
		return err
	}
	if row.State == creationPending {
		changed, writeErr := svc.Queries.RequestTabCreationCancel(c.ctx, db.RequestTabCreationCancelParams{State: creationCancelRequested, CancelAction: sql.NullInt64{Int64: int64(leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP), Valid: true}, UserID: validatedOwner, WorkerID: row.WorkerID, TabID: row.TabID, State_2: creationPending})
		if writeErr != nil {
			unlock()
			return writeErr
		}
		if changed == 1 {
			row.State = creationCancelRequested
			row.CancelAction = sql.NullInt64{Int64: int64(leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP), Valid: true}
		}
	}
	unlock()
	if row.State == creationCancelRequested {
		_, err = svc.finishPendingCreationCancellation(c.ctx, row)
		return err
	}
	return nil
}

func (op *creationOperation) resourceLive(ctx context.Context, q *db.Queries) (bool, error) {
	switch op.publication.GetTabType() {
	case leapmuxv1.TabType_TAB_TYPE_AGENT:
		row, err := q.GetAgentByID(ctx, op.key().tabID)
		return err == nil && !row.ClosedAt.Valid && !row.WorkspaceArchived, err
	case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
		row, err := q.GetTerminal(ctx, op.key().tabID)
		return err == nil && !row.ClosedAt.Valid && !row.WorkspaceArchived, err
	default:
		return false, errors.New("this tab kind has no process startup")
	}
}

// creationStartupAdmission belongs to the flight until a process goroutine starts.
type creationStartupAdmission struct {
	registry *startupCore
	entry    *startupEntry
	ctx      context.Context
}

func (op *creationOperation) acquireStartupAdmission(registry *startupCore, message string) (context.Context, *startupEntry, error) {
	if current := op.admission; current != nil && current.registry == registry && registry.ownsCreation(current.entry, op) {
		return current.ctx, current.entry, nil
	}
	op.releaseStartupAdmission()
	ctx, cancel := context.WithCancel(context.Background())
	entry := registry.begin(op.key().tabID, cancel)
	if entry == nil {
		cancel()
		return nil, nil, errors.New("a startup or close already owns this resource")
	}
	if !registry.bindCreation(entry, op) {
		registry.abandon(entry)
		return nil, nil, errors.New("the startup lost its creation admission")
	}
	if message != "" {
		registry.setMessage(op.key().tabID, message)
	}
	op.admission = &creationStartupAdmission{registry: registry, entry: entry, ctx: ctx}
	return ctx, entry, nil
}
func (op *creationOperation) releaseStartupAdmission() {
	admission := op.admission
	op.admission = nil
	if admission != nil && !admission.registry.creationProcessOwns(admission.entry) {
		admission.registry.abandon(admission.entry)
	}
}
func (op *creationOperation) announcePhase0(cb startupCallbacks) {
	intent := op.runtime.startupIntent()
	label := intent.Plan.PhaseLabel()
	if intent.Started || intent.Result != nil {
		label = "Restoring tab startup…"
	}
	announceStartupPhase0(label, cb)
}

// validateSavedCheckpoint is read-only. A completed mutation never grants new
// rollback rights, even if the original path now contains another repository.
func (op *creationOperation) validateSavedCheckpoint(ctx context.Context, intent *creationStartupIntent, result gitModeResult) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	directory, err := os.Stat(result.WorkingDir)
	if err != nil {
		return fmt.Errorf("the saved startup directory is unavailable: %w", err)
	}
	if !directory.IsDir() {
		return errors.New("the saved startup directory was replaced by a file")
	}
	expectedRepository := result.RepoRoot
	if expectedRepository == "" {
		expectedRepository = intent.Plan.RepoRoot
	}
	if expectedRepository == "" && result.WorktreeID == "" {
		return nil
	}
	info, err := queryGitPathInfo(ctx, result.WorkingDir)
	if err != nil {
		return fmt.Errorf("read the saved startup repository: %w", err)
	}
	if expectedRepository == "" || !pathutil.SamePath(pathutil.Canonicalize(info.RepoRoot), pathutil.Canonicalize(expectedRepository)) {
		return errors.New("the saved startup directory belongs to a different repository; existing files were kept")
	}
	if result.WorktreeID != "" {
		tracked, err := op.coordinator.svc.Queries.GetWorktreeByID(ctx, result.WorktreeID)
		if err != nil {
			return fmt.Errorf("read the saved tracked worktree: %w", err)
		}
		if tracked.DeletedAt.Valid || !info.IsWorktree || !pathutil.SamePath(pathutil.Canonicalize(tracked.WorktreePath), pathutil.Canonicalize(info.TopLevel)) || !pathutil.SamePath(pathutil.Canonicalize(tracked.RepoRoot), pathutil.Canonicalize(expectedRepository)) {
			return errors.New("the saved tracked worktree no longer matches this startup; existing files were kept")
		}
	}
	expectedBranch := ""
	switch intent.Plan.Mode {
	case gitModeCreateWorktree, gitModeCreateBranch:
		expectedBranch = intent.Plan.BranchName
	case gitModeCheckoutBranch:
		expectedBranch = intent.Plan.CheckoutTarget
	default:
	}
	if expectedBranch != "" && info.BranchName != expectedBranch {
		return errors.New("the saved startup branch changed; existing Git state was kept")
	}
	if intent.ExpectedCommit != "" {
		if info.HeadSHA == "" || gitutil.Run(ctx, result.WorkingDir, "merge-base", "--is-ancestor", intent.ExpectedCommit, info.HeadSHA) != nil {
			return errors.New("the saved startup branch no longer contains its expected commit; existing Git state was kept")
		}
	}
	return nil
}
