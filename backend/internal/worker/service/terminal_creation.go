package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"

	"github.com/leapmux/leapmux/internal/util/userid"

	"log/slog"
	"math"

	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"

	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"

	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/leapmux/leapmux/util/validate"
)

func (svc *Service) prepareTerminalCreation(ctx context.Context, caller channel.Caller, r *leapmuxv1.OpenTerminalRequest, sender channel.ResponseWriter, op *creationOperation) {
	if svc.refuseIfShuttingDown(sender) {
		return
	}
	if op != nil && op.record != nil {
		svc.restoreTerminalCreation(op, sender)
		return
	}
	cols := r.GetCols()
	if cols == 0 {
		cols = 80
	}
	rows := r.GetRows()
	if rows == 0 {
		rows = 25
	}
	if err := validateTerminalDimensions(cols, rows); err != nil {
		sendInvalidArgument(sender, err.Error())
		return
	}

	// Resolve the default shell here (not inside terminal.Start) so
	// the startup-panel label reflects the actual binary, e.g.
	// "Starting zsh…" rather than a generic "Starting terminal…"
	// fallback when the client passes shell="".
	shell := r.GetShell()
	if shell == "" {
		shell = terminal.ResolveDefaultShell()
	}
	shellStartDir := expandTilde(r.GetShellStartDir())
	workingDir, err := normalizeWorkingDir(r.GetWorkingDir(), svc.HomeDir, svc.HomeDir)
	if err != nil {
		sendInvalidArgument(sender, err.Error())
		return
	}

	// Validate git-mode options on the sync path so bad input fails
	// the RPC with InvalidArgument before we create any DB row. The
	// actual mutation happens inside runTerminalStartup.
	plan, gmErr := svc.validateGitMode(ctx, workingDir, r.GetGitSetup())
	if gmErr != nil {
		sendValidationError(sender, gmErr)
		return
	}

	// A QUAKE terminal is opened AT a directory; it never CREATES one.
	//
	// This is not a restriction on worktrees. A tab that lives in a
	// worktree already carries that worktree's path as its working
	// dir, so its panel opens there like any other -- the client
	// simply sends that path. What is refused is asking this one call
	// to do both: create or switch a worktree AND be the quake shell
	// of the result.
	//
	// Refused HERE, at the only write point of is_quake, because every
	// later consumer -- the adopt lookup, the unique index, the
	// reconciler's liveness question, closeQuakeTerminalIfUnused --
	// reads working_dir and assumes it is the address the CLIENT asked
	// for. A worktree created mid-startup stores plan.PlannedWorkingDir
	// instead, so the panel would key on the pre-worktree path, never
	// adopt the shell it just started, and leak a fresh one per toggle.
	isQuake := r.GetQuake() != nil
	if isQuake && plan.Mode != gitModeUseCurrent {
		sendInvalidArgument(sender, "a quake terminal opens at a directory and cannot create or switch a worktree; open it with the worktree path as working_dir")
		return
	}
	// The RAW field, before normalizeWorkingDir above resolved it. That
	// call turns an empty path into the worker's home directory, which
	// for a quake terminal is not a default but a different shell: the
	// directory IS the address, so an unset field would silently claim
	// the singleton row of $HOME and hold it against the real one.
	// ListTerminals drops an empty quake directory for the same reason.
	if isQuake && strings.TrimSpace(r.GetWorkingDir()) == "" {
		sendInvalidArgument(sender, "a quake terminal needs a working_dir: it is the address of the shell, not a default")
		return
	}

	// A QUAKE terminal is at most one per directory, so a second
	// OpenTerminal that specifies the same directory hands back the first
	// one. This is what lets two devices -- or two agent tabs that
	// work in one directory, or one device toggling the panel twice
	// -- share a single shell instead of racing to spawn two.
	// Answered before the id is minted, so the duplicate call
	// creates no row, no startup entry and no goroutine.
	if svc.adoptExistingQuakeTerminal(sender, isQuake, workingDir) {
		return
	}

	terminalID := id.Generate()
	if op != nil {
		terminalID = r.GetCreation().GetTabId()
	}

	// Register the startup in the registry BEFORE the row becomes
	// visible, with a cancel ctx so CloseTab during phase 0 aborts
	// executeGitMode. A reader that finds the row of a terminal with no
	// entry and no PTY takes it for a dead shell. A second quake open
	// finds the row by its directory, and quakeShellIsLive then answers
	// from the registry: with no entry it closes the row of a shell that
	// is about to start. OpenAgent registers first for the same reason.
	//
	// From here, every return that does not hand the claim to
	// runTerminalStartup must release it with abandon. A claim that
	// nobody releases reports STARTING for ever and holds Shutdown's
	// drainForShutdown until the shutdown deadline.
	var startupCtx context.Context
	var startupHandle *startupEntry
	if op == nil {
		startupCtx, startupHandle = svc.beginTerminalStartup(terminalID, shell)
	} else {
		var claimErr error
		startupCtx, startupHandle, claimErr = op.acquireStartupAdmission(&svc.TerminalStartup.startupCore, terminalStartingLabel(shell))
		if claimErr != nil {
			svc.sendStartupRefused(sender, claimErr.Error())
			return
		}
	}
	if startupHandle == nil {
		// The registry claim authorizes startup and coordinates shutdown.
		// Report refused admission before starting the process.
		svc.sendStartupRefused(sender, "a startup for this terminal is already in progress")
		return
	}

	releaseWorktree, admissionErr := svc.worktreeLifecycle.admit(plan.PlannedWorkingDir)
	if admissionErr != nil {
		if op == nil {
			svc.TerminalStartup.abandon(startupHandle)
		}
		sendFailedPrecondition(sender, admissionErr.Error())
		return
	}
	releaseWorktree = sync.OnceFunc(releaseWorktree)
	defer releaseWorktree()

	// Persist the initial terminal record using the planned working
	// dir, so tab sync and post-refresh reads see the eventual path
	// even before git-mode execution creates the worktree.
	//
	// A client-set title is CLEANED, never refused, with the rule that
	// validate.CleanName documents. RenameTerminal, RenameAgent
	// and OpenAgent apply the same rule, so every writer of a `title`
	// column enforces one rule and none of them fails the RPC over a
	// title the user can neither see nor correct.
	terminalTitle := validate.CleanName(r.GetTitle())
	// Empty title means "you pick one" -- the client sent none, or
	// cleaning removed every character of the one it sent. Default to
	// a random "Terminal <Name>" from the shared pool so every spawn
	// path (UI, CLI, ChangeBranchDialog) gets a name from one pool,
	// picked one place. The response echoes whatever lands here, so a
	// client that sent a title renders the STORED form, not its input.
	if terminalTitle == "" {
		terminalTitle = pickTerminalTitle()
	}
	params := db.UpsertTerminalParams{
		ID:            terminalID,
		WorkingDir:    plan.PlannedWorkingDir,
		ShellStartDir: shellStartDir,
		Shell:         shell,
		Title:         terminalTitle,
		Cols:          int64(cols),
		Rows:          int64(rows),
		Screen:        []byte{},
		IsQuake:       isQuake,
	}
	runtime := &terminalCreationRuntime{Startup: creationStartupIntent{Plan: plan}, Options: terminal.Options{ID: terminalID, Shell: shell, WorkingDir: plan.PlannedWorkingDir, ShellStartDir: shellStartDir, Cols: uint16(cols), Rows: uint16(rows)}, Title: terminalTitle}
	var persistErr error
	if op == nil {
		persistErr = svc.Queries.UpsertTerminal(bgCtx(), params)
	} else {
		intent, intentErr := svc.prepareCreationStartup(ctx, plan)
		if intentErr != nil {
			if op == nil {
				svc.TerminalStartup.abandon(startupHandle)
			}
			sendFailedPrecondition(sender, intentErr.Error())
			return
		}
		runtime.Startup = intent
		persistErr = op.persist(runtime, func(ctx context.Context, q *db.Queries) error {
			return q.CreateTerminal(ctx, db.CreateTerminalParams(params))
		})
	}
	if upsertErr := persistErr; upsertErr != nil {
		// The row does not exist, so no startup will ever run for it.
		if op == nil {
			svc.TerminalStartup.abandon(startupHandle)
		}
		// Losing the quake race lands here: the check above found no
		// quake terminal for this directory, another client inserted
		// one in the window, and the unique partial index refused this
		// row. Re-read and answer with the winner's terminal, so both
		// callers attach to one PTY. The index is what makes this
		// decidable at all -- without it both inserts would succeed and
		// the two clients would type into different shells.
		//
		// A lookup that still finds nothing means the failure was not
		// the race, so report it. Deliberately not keyed on a SQLite
		// error code: the question "does a quake terminal exist for
		// this directory now?" is the one that decides what to do, and
		// it answers itself.
		if svc.adoptExistingQuakeTerminal(sender, isQuake, workingDir) {
			return
		}
		slog.Error("failed to persist terminal record", "terminal_id", terminalID, "error", upsertErr)
		sendInternalError(sender, "failed to persist terminal")
		return
	}
	// Same window as the agent path: the row is durable and reapable, but
	// its cleanup is only registered once spawnControlIPC runs. See
	// cleanupRegistry.claim.
	svc.terminalCleanups.claim(terminalID)

	// Broadcast STARTING with the provider label. Phase 0 will
	// overwrite the message with a mode-specific label (e.g.
	// `Creating worktree "feature/x"…`) before mutation begins. The
	// git status is nil here because the post-mutation working dir
	// isn't known yet; phase 1 re-broadcasts with the real value.
	svc.announceTerminalStarting(terminalID, shell)

	releaseWorktree()
	svc.finishTerminalCreation(op, caller, sender, runtime, startupCtx, startupHandle, isQuake)
}

type terminalCreationRuntime struct {
	Startup creationStartupIntent
	Options terminal.Options
	Title   string
}

func (svc *Service) handleOpenTerminal(ctx context.Context, caller channel.Caller, r *leapmuxv1.OpenTerminalRequest, sender channel.ResponseWriter) {
	if r.GetQuake() != nil {
		svc.prepareTerminalCreation(ctx, caller, r, sender, nil)
		return
	}
	svc.coordinateCreation(ctx, caller, r, r.GetCreation(), leapmuxv1.TabType_TAB_TYPE_TERMINAL, sender, func(op *creationOperation, writer channel.ResponseWriter) {
		svc.prepareTerminalCreation(op.coordinator.ctx, caller, op.request.(*leapmuxv1.OpenTerminalRequest), writer, op)
	})
}
func (svc *Service) restoreTerminalCreation(op *creationOperation, sender channel.ResponseWriter) {
	var runtime terminalCreationRuntime
	if err := json.Unmarshal(op.record.RuntimePayload, &runtime); err != nil {
		sendInternalError(sender, err.Error())
		return
	}
	row, err := svc.Queries.GetTerminal(op.coordinator.ctx, op.key().tabID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && row.ClosedAt.Valid {
		if closeErr := op.finishClosedResource(); closeErr != nil {
			sendInternalError(sender, closeErr.Error())
		} else {
			sendCreationError(sender, &creationAbortedError{message: "the created resource is closed"})
		}
		return
	}
	if err != nil {
		sendInternalError(sender, err.Error())
		return
	}
	if row.StartupError != "" {
		svc.finishCreationStartup(op, creationStartupOutcome{kind: creationStartupFailed, message: row.StartupError})
		svc.replyCommittedCreation(op, sender)
		return
	}
	op.runtime = &runtime
	ctx, handle, claimErr := op.acquireStartupAdmission(&svc.TerminalStartup.startupCore, terminalStartingLabel(row.Shell))
	if claimErr != nil {
		sendFailedPrecondition(sender, claimErr.Error())
		return
	}
	if handle == nil {
		sendFailedPrecondition(sender, "a terminal startup already owns this creation")
		return
	}
	svc.terminalCleanups.claim(row.ID)
	svc.finishTerminalCreation(op, op.caller, sender, &runtime, ctx, handle, false)
}
func (svc *Service) finishTerminalCreation(op *creationOperation, caller channel.Caller, sender channel.ResponseWriter, runtime *terminalCreationRuntime, startupCtx context.Context, handle *startupEntry, isQuake bool) {
	handedOff := false
	defer func() {
		if !handedOff {
			svc.terminalCleanups.retire(runtime.Options.ID)
			if op == nil && handle != nil {
				svc.TerminalStartup.abandon(handle)
			}
		}
	}()
	var publication *leapmuxv1.PublishedTab
	if op != nil {
		var err error
		publication, err = op.publish()
		if err != nil {
			sendCreationError(sender, err)
			return
		}
	}
	if op != nil {
		current, err := op.resolveStartupLifecycle()
		if err != nil {
			sendCreationError(sender, err)
			return
		}
		allowed, err := op.applyStartupLifecycle(current)
		if err != nil {
			sendCreationError(sender, err)
			return
		}
		if !allowed {
			op.releaseStartupAdmission()
			sendProtoResponse(sender, &leapmuxv1.OpenTerminalResponse{TerminalId: runtime.Options.ID, Title: runtime.Title, Publication: publication})
			return
		}
		if err := svc.refreshCreationTerminalDimensions(op.coordinator.ctx, runtime, handle); err != nil {
			sendInternalError(sender, err.Error())
			return
		}
	}
	spawnInfo := TerminalSpawnInfo{UserID: caller.UserID, WorkerID: svc.WorkerID, TabID: runtime.Options.ID, IsQuake: isQuake, WorkingDir: runtime.Startup.Plan.PlannedWorkingDir}
	start := func() {
		go svc.runTerminalStartup(startupCtx, runtime.Options, spawnInfo, runtime.Startup.Plan, svc.makeTerminalOutputFn(runtime.Options.ID), svc.makeTerminalExitFn(), handle)
	}
	if op == nil {
		start()
	} else if err := op.handoff(handle, start); err != nil {
		sendCreationError(sender, err)
		return
	}
	handedOff = true
	sendProtoResponse(sender, &leapmuxv1.OpenTerminalResponse{TerminalId: runtime.Options.ID, Title: runtime.Title, Publication: publication})
}

func (svc *Service) saveCreationTerminalResize(ctx context.Context, id string, cols, rows uint16) (bool, error) {
	validatedOwner, ownerOK := userid.OwnerFilter(svc.RegisteredBy())
	if !ownerOK {
		return false, errCreationOwnerMissing
	}
	changed, err := svc.Queries.SaveCreationTerminalDimensions(ctx, db.SaveCreationTerminalDimensionsParams{Cols: int64(cols), Rows: int64(rows), TerminalID: id, UserID: validatedOwner, WorkerID: svc.WorkerID, TabType: int64(leapmuxv1.TabType_TAB_TYPE_TERMINAL), PendingState: creationPending, PublishedState: creationPublished})
	return changed != 0, err
}

func (svc *Service) refreshCreationTerminalDimensions(ctx context.Context, runtime *terminalCreationRuntime, handle *startupEntry) error {
	return svc.TerminalStartup.withTerminalResize(ctx, runtime.Options.ID, func(current *startupEntry) error {
		if current != handle {
			return terminal.ErrTerminalInstanceChanged
		}
		row, err := svc.Queries.GetTerminal(ctx, runtime.Options.ID)
		if err != nil {
			return err
		}
		if row.Cols <= 0 || row.Rows <= 0 || row.Cols > math.MaxUint16 || row.Rows > math.MaxUint16 {
			return errors.New("the saved terminal dimensions are invalid")
		}
		changed := runtime.Options.Cols != uint16(row.Cols) || runtime.Options.Rows != uint16(row.Rows)
		runtime.Options.Cols = uint16(row.Cols)
		runtime.Options.Rows = uint16(row.Rows)
		if changed && !svc.TerminalStartup.setPendingResize(handle, runtime.Options.Cols, runtime.Options.Rows) {
			return terminal.ErrTerminalInstanceChanged
		}
		return nil
	})
}
