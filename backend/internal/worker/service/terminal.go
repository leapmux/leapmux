package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/leapmux/leapmux/util/validate"
)

// pendingResizeWaitCap bounds how long runTerminalStartup blocks waiting
// for the frontend's first ResizeTerminal before spawning the shell. A
// typical wait returns in a few tens of ms; the cap is a safety valve
// for a wedged or unusually slow frontend.
const pendingResizeWaitCap = 500 * time.Millisecond

// terminalStartingLabel returns the "Starting <shell>…" label used for the
// sync prologue broadcast and the phase-1 re-broadcast once git status is
// in hand. Kept in one place so both call sites stay in lockstep.
func terminalStartingLabel(shell string) string {
	return "Starting " + filepath.Base(shell) + "…"
}

// beginTerminalStartup registers a fresh startup for terminalID with a
// cancellable background context, seeds the "Starting <shell>…" message
// on the registry, and broadcasts STARTING to watchers. gs is the git
// status to attach; both current callers pass nil and let the async
// goroutine re-broadcast once git status returns (the frontend keeps
// existing git fields when STARTING arrives without them, so a nil
// first broadcast is non-clobbering). Returns the ctx AND the startup handle
// the caller passes into runTerminalStartup / runTerminalRestart.
func (svc *Service) beginTerminalStartup(terminalID, shell string, gs *leapmuxv1.AgentGitStatus) (context.Context, *startupEntry) {
	startupCtx, cancel := context.WithCancel(context.Background())
	h := svc.TerminalStartup.begin(terminalID, cancel)
	msg := terminalStartingLabel(shell)
	svc.TerminalStartup.setMessage(terminalID, msg)
	svc.broadcastTerminalStarting(terminalID, msg, gs)
	return startupCtx, h
}

// registerTerminalHandlers registers all terminal-related RPC handlers.
func registerTerminalHandlers(d registrar, svc *Service) {
	// OpenTerminal starts a new PTY terminal session.
	registerOwnerGated(d, "OpenTerminal", dispatchPlain,
		func(ctx context.Context, userID userid.UserID, r *leapmuxv1.OpenTerminalRequest, sender channel.ResponseWriter) {
			if svc.refuseIfShuttingDown(sender) {
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

			// Resolve the default shell here (not inside terminal.Start) so
			// the startup-panel label reflects the actual binary, e.g.
			// "Starting zsh…" rather than a generic "Starting terminal…"
			// fallback when the client passes shell="".
			shell := r.GetShell()
			if shell == "" {
				shell = terminal.ResolveDefaultShell()
			}
			shellStartDir := expandTilde(r.GetShellStartDir())
			workingDir, err := normalizeWorkingDir(r.GetWorkingDir(), svc.HomeDir)
			if err != nil {
				sendInvalidArgument(sender, err.Error())
				return
			}

			// Validate git-mode options on the sync path so bad input fails
			// the RPC with InvalidArgument before we create any DB row. The
			// actual mutation happens inside runTerminalStartup.
			plan, gmErr := svc.validateGitMode(ctx, workingDir, r)
			if gmErr != nil {
				sendValidationError(sender, gmErr)
				return
			}

			terminalID := id.Generate()

			outputFn := svc.makeTerminalOutputFn(terminalID)
			exitFn := svc.makeTerminalExitFn()

			// Persist the initial terminal record using the planned working
			// dir, so tab sync and post-refresh reads see the eventual path
			// even before git-mode execution creates the worktree.
			// Default a random "Terminal <Name>" title here so all spawn
			// paths (UI + CLI) get a name from one pool, picked one place.
			// OpenTerminalRequest has no title field by design — the
			// frontend used to pick client-side and call UpdateTerminalTitle
			// afterward; now it just reads `title` from this response.
			terminalTitle := pickTerminalTitle()
			if upsertErr := svc.Queries.UpsertTerminal(bgCtx(), db.UpsertTerminalParams{
				ID:            terminalID,
				WorkingDir:    plan.PlannedWorkingDir,
				HomeDir:       svc.HomeDir,
				ShellStartDir: shellStartDir,
				Shell:         shell,
				Title:         terminalTitle,
				Cols:          int64(cols),
				Rows:          int64(rows),
				Screen:        []byte{},
			}); upsertErr != nil {
				slog.Error("failed to persist terminal record", "terminal_id", terminalID, "error", upsertErr)
				sendInternalError(sender, "failed to persist terminal")
				return
			}
			// Same window as the agent path: the row is durable and reapable, but
			// its cleanup is only registered once spawnControlIPC runs. See
			// cleanupRegistry.claim.
			svc.terminalCleanups.claim(terminalID)

			// Register the startup in the registry with a cancel ctx so
			// CloseTerminal during phase 0 aborts executeGitMode, and seed
			// the STARTING broadcast with the provider label. Phase 0 will
			// overwrite the message with a mode-specific label (e.g.
			// `Creating worktree "feature/x"…`) before mutation begins. gs
			// is nil here because the post-mutation working dir isn't
			// known yet; phase 1 re-broadcasts with the real value.
			startupCtx, startupHandle := svc.beginTerminalStartup(terminalID, shell, nil)

			sendProtoResponse(sender, &leapmuxv1.OpenTerminalResponse{
				TerminalId: terminalID,
				Title:      terminalTitle,
			})

			// Kick off git-mode execution + PTY spawn in the background.
			// The ControlIPC mint happens inside runTerminalStartup so an
			// unusually slow factory doesn't stretch the synchronous RPC
			// latency the user sees.
			spawnInfo := TerminalSpawnInfo{
				UserID:     userID,
				WorkerID:   svc.WorkerID,
				TabID:      terminalID,
				WorkingDir: plan.PlannedWorkingDir,
			}
			go svc.runTerminalStartup(startupCtx, terminal.Options{
				ID:            terminalID,
				Shell:         shell,
				WorkingDir:    plan.PlannedWorkingDir,
				ShellStartDir: shellStartDir,
				Cols:          uint16(cols),
				Rows:          uint16(rows),
			}, spawnInfo, plan, outputFn, exitFn, startupHandle)
		})

	// RestartTerminal respawns the shell process for a terminal whose
	// previous PTY has exited. Reuses the tab's working_dir / shell /
	// shell_start_dir and mints fresh LEAPMUX_CONTROL_* env vars. The
	// existing screen buffer (including the "[Terminal process exited
	// (N) - Press Enter to restart]" notice) is preserved so the new
	// shell's prompt lands directly below the notice.
	registerTerminalForRestartGated(d, "RestartTerminal",
		func(_ context.Context, userID userid.UserID, r *leapmuxv1.RestartTerminalRequest, dbTerm db.GetTerminalForRestartRow, sender channel.ResponseWriter) {
			terminalID := r.GetTerminalId()

			if svc.refuseIfShuttingDown(sender) {
				return
			}
			// Reject overlapping restarts: a previous startup hasn't broadcast
			// READY/FAILED yet (could be the original OpenTerminal still in
			// flight, or a back-to-back restart).
			if _, _, _, inFlight := svc.TerminalStartup.status(terminalID); inFlight {
				sendFailedPrecondition(sender, "terminal startup in progress")
				return
			}
			// Reject synchronously if the PTY is still alive so the user sees
			// FailedPrecondition rather than waiting for an async
			// STARTUP_FAILED broadcast from the spawn goroutine. The TOCTOU
			// between this check and Manager.RestartTerminal's own check is
			// benign: a PTY that exits between calls yields a false reject
			// that retrying Enter resolves.
			if svc.Terminals.IsRunning(terminalID) {
				sendFailedPrecondition(sender, terminal.ErrTerminalStillRunning.Error())
				return
			}

			cols := r.GetCols()
			if cols == 0 {
				cols = uint32(dbTerm.Cols)
			}
			rows := r.GetRows()
			if rows == 0 {
				rows = uint32(dbTerm.Rows)
			}

			// No default-shell fallback — an empty value here means the
			// OpenTerminal path that wrote the row skipped its own
			// ResolveDefaultShell() call, which is a real bug we'd rather
			// surface as a clear STARTUP_FAILED than mask by silently
			// swapping in a different shell.
			shell := dbTerm.Shell
			if shell == "" {
				sendFailedPrecondition(sender, "terminal has no shell to restart")
				return
			}

			// Seed STARTING without git status — the goroutine fetches and
			// re-broadcasts with branch/origin once it lands. Mirrors
			// runTerminalStartup's phase-1 pattern so the RPC round-trip
			// doesn't block on a slow `git status` against a large worktree.
			startupCtx, startupHandle := svc.beginTerminalStartup(terminalID, shell, nil)

			sendProtoResponse(sender, &leapmuxv1.RestartTerminalResponse{})

			outputFn := svc.makeTerminalOutputFn(terminalID)
			exitFn := svc.makeTerminalExitFn()
			// fallbackOffset seeds the cumulative byte counter only when no
			// in-memory ScreenBuffer is around (post-worker-restart). For the
			// common case it's ignored — Manager.RestartTerminal carries the
			// live buffer's counter through Respawn.
			var fallbackOffset int64
			if dbTerm.ScreenLength.Valid {
				fallbackOffset = dbTerm.ScreenLength.Int64
			}
			spawnInfo := TerminalSpawnInfo{
				UserID:     userID,
				WorkerID:   svc.WorkerID,
				TabID:      terminalID,
				WorkingDir: dbTerm.WorkingDir,
			}
			go svc.runTerminalRestart(startupCtx, terminal.Options{
				ID:            terminalID,
				Shell:         shell,
				WorkingDir:    dbTerm.WorkingDir,
				ShellStartDir: dbTerm.ShellStartDir,
				Cols:          uint16(cols),
				Rows:          uint16(rows),
			}, spawnInfo, fallbackOffset, outputFn, exitFn, startupHandle)
		})

	// CloseTerminal stops and removes a terminal session.
	registerTerminalGatedByID(d, "CloseTerminal", dispatchTracked,
		func(_ context.Context, userID userid.UserID, r *leapmuxv1.CloseTerminalRequest, sender channel.ResponseWriter) {
			terminalID := r.GetTerminalId()

			// Tracked via dispatcher RegisterTracked above so Shutdown
			// drains the close flow (stop → DB close → unregister →
			// optional worktree remove) before tearing down the DB pool.
			// The frontend fires this RPC fire-and-forget after removing
			// the tab from the UI. The TerminalStartup goroutine's
			// trailing rollback work is tracked separately by
			// TerminalStartup.WaitForInFlight and drained in Shutdown.
			result := svc.closeTerminalTabCommon(userID.String(), terminalID, r.GetWorktreeAction(), dropWorktreeLink)
			sendProtoResponse(sender, &leapmuxv1.CloseTerminalResponse{Result: result})
		})

	// SendInput sends input data to a terminal.
	registerTerminalGatedByID(d, "SendInput", dispatchPlain,
		func(_ context.Context, _ userid.UserID, r *leapmuxv1.SendInputRequest, sender channel.ResponseWriter) {
			terminalID := r.GetTerminalId()

			if svc.WakeLock != nil {
				svc.WakeLock.RecordActivity()
			}

			if err := svc.Terminals.SendInput(terminalID, r.GetData()); err != nil {
				slog.Error("failed to send input", "terminal_id", terminalID, "error", err)
				sendInternalError(sender, fmt.Sprintf("send input: %v", err))
				return
			}

			sendProtoResponse(sender, &leapmuxv1.SendInputResponse{})
		})

	// ResizeTerminal changes a terminal's dimensions.
	registerTerminalGatedByID(d, "ResizeTerminal", dispatchPlain,
		func(_ context.Context, _ userid.UserID, r *leapmuxv1.ResizeTerminalRequest, sender channel.ResponseWriter) {
			terminalID := r.GetTerminalId()

			cols := r.GetCols()
			rows := r.GetRows()
			if cols == 0 || rows == 0 {
				sendInvalidArgument(sender, "cols and rows must be > 0")
				return
			}

			err := svc.Terminals.Resize(terminalID, uint16(cols), uint16(rows))
			switch {
			case err == nil:
				// Drop any dims stashed during STARTING — the resize just
				// landed on the real PTY, so the post-startup apply in
				// runTerminalStartup must not overwrite it with older dims.
				svc.TerminalStartup.clearPendingResize(terminalID)
			case errors.Is(err, terminal.ErrTerminalNotFound):
				// Async startup: the tab exists but the PTY isn't in the
				// Manager yet. Stash the latest dims and ack success so the
				// frontend's first fit() isn't silently dropped — vim/nvim
				// would otherwise see the placeholder 80x24 from the
				// OpenTerminal request on its first TIOCGWINSZ query.
				if !svc.TerminalStartup.setPendingResize(terminalID, uint16(cols), uint16(rows)) {
					// Benign TOCTOU: the PTY exited between the frontend's
					// status check and this RPC arriving. The frontend gates
					// EXITED/DISCONNECTED/STARTUP_FAILED, so reaching here
					// means READY at check time and gone now — no PTY to
					// resize, but not actionable either.
					slog.Debug("resize on missing terminal", "terminal_id", terminalID, "error", err)
					sendInternalError(sender, fmt.Sprintf("resize: %v", err))
					return
				}
			default:
				slog.Error("failed to resize terminal", "terminal_id", terminalID, "error", err)
				sendInternalError(sender, fmt.Sprintf("resize: %v", err))
				return
			}

			sendProtoResponse(sender, &leapmuxv1.ResizeTerminalResponse{})
		})

	// UpdateTerminalTitle updates a terminal's title in both the in-memory
	// manager and the database. It is the only writer of a USER-SUPPLIED
	// title; OpenTerminal writes the worker-assigned name at creation and
	// persistTerminalOnExit writes TerminalMeta.Title on shell exit.
	//
	// The worker never persists a PTY-driven OSC title. It broadcasts that
	// title as a live overlay instead -- see the SignalTitle case in this
	// file for the reason.
	registerTerminalGated(d, "UpdateTerminalTitle",
		func(_ context.Context, userID userid.UserID, r *leapmuxv1.UpdateTerminalTitleRequest, dbTerm db.Terminal, sender channel.ResponseWriter) {
			terminalID := r.GetTerminalId()

			// Clean the title, never refuse it -- the same rule OpenAgent,
			// RenameAgent and the plan auto-rename apply. An empty result
			// means the request carried nothing that survives cleaning, so
			// both the manager and the row keep the title they hold: writing
			// "" would leave the tab with no name at all. The handler answers
			// OK because the rename is a no-op, not a failure.
			title := validate.CleanName(r.GetTitle())
			if title == "" {
				slog.Debug("ignoring terminal rename to an empty title", "terminal_id", terminalID)
				// The reply reports the title that is IN FORCE, not the empty
				// string the handler refused to store. "In force" is the
				// manager's title while the terminal is live and the row's
				// title after it exits -- the same precedence ListTerminals
				// applies, so a client polling either RPC reads one answer.
				sendProtoResponse(sender, &leapmuxv1.UpdateTerminalTitleResponse{
					Title: svc.effectiveTerminalTitle(dbTerm),
				})
				return
			}

			svc.Terminals.UpdateTitle(terminalID, title)
			screen := dbTerm.Screen
			if screen == nil {
				screen = []byte{}
			}

			// Persist to DB so it survives restarts.
			if err := svc.Queries.UpsertTerminal(bgCtx(), db.UpsertTerminalParams{
				ID:            dbTerm.ID,
				WorkingDir:    dbTerm.WorkingDir,
				HomeDir:       dbTerm.HomeDir,
				ShellStartDir: dbTerm.ShellStartDir,
				Shell:         dbTerm.Shell,
				Title:         title,
				Cols:          dbTerm.Cols,
				Rows:          dbTerm.Rows,
				Screen:        screen,
				ExitCode:      dbTerm.ExitCode,
				ClosedAt:      dbTerm.ClosedAt,
			}); err != nil {
				slog.Error("failed to update terminal title", "terminal_id", terminalID, "error", err)
				sendInternalError(sender, "failed to update terminal title")
				return
			}

			if svc.PrivateEvents != nil {
				svc.PrivateEvents.PublishTabRenamed(
					userID, terminalID, leapmuxv1.TabType_TAB_TYPE_TERMINAL,
					title, sender.ChannelID(),
				)
			}

			sendProtoResponse(sender, &leapmuxv1.UpdateTerminalTitleResponse{Title: title})
		})

	// ListTerminals resolves the records behind a set of terminal tab ids.
	// Uses the in-memory terminal manager for running terminals and falls
	// back to saved terminal records for terminals that have already exited
	// and been removed from the manager.
	registerOwnerGated(d, "ListTerminals", dispatchPlain, func(ctx context.Context, _ userid.UserID, r *leapmuxv1.ListTerminalsRequest, sender channel.ResponseWriter) {
		tabIDs := r.GetTabIds()
		if len(tabIDs) == 0 {
			sendProtoResponse(sender, &leapmuxv1.ListTerminalsResponse{})
			return
		}

		// Collect from the in-memory manager and DB-only rows, recording
		// each terminal's resolved git directory (see gitutil.ResolveGitDir)
		// so BatchGetGitStatus can dedupe across terminals that share a repo.
		entries := svc.Terminals.ListByIDs(tabIDs)
		seen := make(map[string]bool, len(entries))
		var terminals []*leapmuxv1.TerminalInfo
		var gitDirs []string
		for _, e := range entries {
			seen[e.ID] = true
			ti := &leapmuxv1.TerminalInfo{
				TerminalId:      e.ID,
				Cols:            e.Meta.Cols,
				Rows:            e.Meta.Rows,
				Screen:          e.Screen,
				ScreenEndOffset: e.ScreenEndOffset,
				Exited:          e.Exited,
				WorkingDir:      e.Meta.WorkingDir,
				ShellStartDir:   e.Meta.ShellStartDir,
				Title:           e.Meta.Title,
				Status:          leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY,
			}
			if sup, errStr, msg, ok := svc.TerminalStartup.status(e.ID); ok {
				ti.Status = sup
				ti.StartupError = errStr
				ti.StartupMessage = msg
			}
			terminals = append(terminals, ti)
			gitDirs = append(gitDirs, gitutil.ResolveGitDir(e.Meta.ShellStartDir, e.Meta.WorkingDir))
		}

		// A DB read failure has to fail the WHOLE call. Falling through to
		// the verdict pass would leave `seen` holding only the ids the
		// in-memory manager happens to hold, so every other requested id
		// gets stamped ABSENT -- which the client reads as "no such record,
		// stop asking" (retryableFrom drops ABSENT ids) and retires the tab
		// from hydration for the life of the page. Surfacing the error
		// instead rejects the client's promise and keeps its backoff
		// running, matching ListAgents on the same failure.
		dbTerminals, err := svc.Queries.ListTerminalsByIDs(ctx, tabIDs)
		if err != nil {
			slog.Error("failed to list terminals from DB", "tab_ids", tabIDs, "error", err)
			sendInternalError(sender, "failed to list terminals")
			return
		}
		for _, ts := range dbTerminals {
			if seen[ts.ID] {
				continue
			}
			seen[ts.ID] = true
			status, startupError, startupMessage := svc.deriveTerminalStatus(&ts)
			// DB-persisted screen is just the bytes; the backend has no
			// live ring for this terminal (PTY exited or worker
			// restarted), so the "end offset" equals the screen
			// length. The client's after_offset will be the same
			// value, and WatchEvents will return nothing for a dead
			// terminal — correct, since there are no new bytes.
			ti := &leapmuxv1.TerminalInfo{
				TerminalId:      ts.ID,
				Cols:            uint32(ts.Cols),
				Rows:            uint32(ts.Rows),
				Screen:          ts.Screen,
				ScreenEndOffset: int64(len(ts.Screen)),
				Exited:          !svc.Terminals.HasTerminal(ts.ID),
				WorkingDir:      ts.WorkingDir,
				ShellStartDir:   ts.ShellStartDir,
				Title:           ts.Title,
				Status:          status,
				StartupError:    startupError,
				StartupMessage:  startupMessage,
			}
			terminals = append(terminals, ti)
			gitDirs = append(gitDirs, gitutil.ResolveGitDir(ts.ShellStartDir, ts.WorkingDir))
		}

		gitStatuses := gitutil.BatchGetGitStatus(ctx, gitDirs)
		for i, gs := range gitStatuses {
			if gs != nil {
				terminals[i].GitBranch = gs.Branch
				terminals[i].GitOriginUrl = gs.OriginUrl
				terminals[i].GitToplevel = gs.Toplevel
				terminals[i].GitIsWorktree = gs.IsWorktree
			}
		}

		sendProtoResponse(sender, &leapmuxv1.ListTerminalsResponse{
			Terminals: terminals,
			Verdicts:  tabHydrationVerdicts(tabIDs, seen),
		})
	})

	// ListAvailableShells returns the shells installed on this worker.
	// Owner-only: like sysinfo, it discloses machine-scoped state (which
	// shell binaries are present on the host), so a non-owner channel --
	// notably a delegation bearer minted for a different user -- must not
	// reach it. The worker owner's own agents (via the local-IPC remote CLI,
	// which dispatches with the owner's user id) still pass this gate.
	registerOwnerOnly(d, "ListAvailableShells", func(ctx context.Context, userID userid.UserID, req *leapmuxv1.InnerRpcRequest, sender channel.ResponseWriter) {
		var r leapmuxv1.ListAvailableShellsRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		shells, defaultShell := terminal.ListAvailableShells()
		sendProtoResponse(sender, &leapmuxv1.ListAvailableShellsResponse{
			Shells:       shells,
			DefaultShell: defaultShell,
		})
	})
}

// runTerminalStartup is the async body of OpenTerminal: it executes the
// git-mode plan, spawns the PTY, and reports READY or STARTUP_FAILED to the
// frontend. On failure it rolls back any partial git-mode side effects.
//
// spawnInfo carries the data needed to mint the LEAPMUX_CONTROL_* token.
// The mint runs inside this goroutine (rather than synchronously, before
// sendProtoResponse) so an unusually slow RemoteIPC factory doesn't
// stretch the RPC latency the user sees.
func (svc *Service) runTerminalStartup(ctx context.Context, opts terminal.Options, spawnInfo TerminalSpawnInfo, plan gitModePlan, outputFn terminal.OutputHandler, exitFn terminal.ExitHandler, h *startupEntry) {
	terminalID := opts.ID
	defer svc.TerminalStartup.finish()

	// Mint the remote-IPC token before phase 0 so the cleanup is in the
	// map by the time any concurrent CloseTerminal calls terminalCleanups.run.
	// Ownership lives with this goroutine until we broadcast READY; until
	// then, the deferred cleanup retires the token on every error path so
	// a close that lost the register-vs-cleanup race doesn't leak it.
	// When no token was minted (RemoteIPC disabled or factory failed),
	// nothing was registered, so the defer skips the mutex roundtrip.
	remoteEnvs, ipcErr := svc.spawnControlIPC("terminal", terminalID, "open", svc.terminalCleanups.register, func() ([]string, func(), error) {
		return svc.ControlIPC.TerminalSpawning(spawnInfo)
	})
	if ipcErr != nil {
		// Only a missing identity is fatal here; every other factory failure
		// degrades to "no remote control". Route it through the same tail every
		// other startup failure uses so the frontend gets STARTUP_FAILED rather
		// than a terminal stuck in STARTING.
		svc.failTerminalStartup(terminalID, gitModeResult{}, ipcErr, h)
		return
	}
	opts.ExtraEnv = remoteEnvs
	ownsIPCToken := opts.ExtraEnv != nil
	defer func() {
		if ownsIPCToken {
			svc.terminalCleanups.run(terminalID)
		}
	}()

	// Phase 0: execute git-mode mutation (worktree add, branch create,
	// checkout). Validation already ran synchronously.
	gm, gmErr := svc.runTerminalPhase0(ctx, terminalID, plan, h)
	if gmErr != nil {
		svc.failTerminalStartup(terminalID, gm, gmErr, h)
		return
	}
	// Link the tab to its worktree unless a CloseTerminal already landed during
	// startup and decided the worktree's fate itself (see
	// registerTabForWorktreeAfterClose). Symmetric with the agent startup guard.
	terminalClosedDuringStartup := false
	if latest, fetchErr := svc.Queries.GetTerminalForReady(bgCtx(), terminalID); fetchErr == nil {
		terminalClosedDuringStartup = latest.ClosedAt.Valid
	}
	svc.linkWorktreeAfterPhase0(&svc.TerminalStartup.startupCore, h, gm.WorktreeID,
		leapmuxv1.TabType_TAB_TYPE_TERMINAL, terminalID, terminalClosedDuringStartup)
	if gm.WorkingDir != "" {
		opts.WorkingDir = gm.WorkingDir
	}

	// Phase 1: compute git status from the final working dir (may be a
	// freshly-created worktree). The resulting branch/origin/toplevel travel
	// on the "Starting <shell>…" broadcast so the frontend can populate the
	// tab's gitBranch / gitOriginUrl / gitToplevel without an extra round-trip.
	gs := gitutil.GetGitStatus(ctx, gitutil.ResolveGitDir(opts.ShellStartDir, opts.WorkingDir))
	shellMsg := terminalStartingLabel(opts.Shell)
	svc.TerminalStartup.setMessage(terminalID, shellMsg)
	svc.broadcastTerminalStarting(terminalID, shellMsg, gs)

	// Wait for the frontend's first ResizeTerminal to arrive so the shell
	// is spawned at the final size rather than being SIGWINCH'd to it
	// after StartTerminal returns — some shells emit artifacts on a
	// mid-startup resize. If the cap elapses, the post-spawn apply below
	// still lands the dims on the running PTY.
	if cols, rows, ok := svc.TerminalStartup.waitForPendingResize(terminalID, pendingResizeWaitCap); ok {
		opts.Cols = cols
		opts.Rows = rows
	}

	startErr := svc.startTerminal(ctx, opts, outputFn, exitFn)

	// Post-spawn fetch: closed_at detects a CloseTerminal that landed
	// during the PTY spawn (must neither broadcast READY nor leave a
	// running PTY behind), and title absorbs the value the frontend may
	// have persisted between OpenTerminal returning and StartTerminal
	// registering in-memory metadata. Single narrow query — avoids
	// re-reading the screen BLOB the handler entry already fetched.
	postSpawn, postSpawnErr := svc.Queries.GetTerminalForReady(bgCtx(), terminalID)
	if postSpawnErr == nil && postSpawn.ClosedAt.Valid {
		if startErr == nil {
			svc.Terminals.RemoveTerminal(terminalID)
			svc.clearTerminalBellCoalesce(terminalID)
		}
		svc.finishStartupAfterClose(&svc.TerminalStartup.startupCore, h, terminalID, gm)
		return
	}

	if startErr != nil {
		slog.Error("failed to start terminal", "terminal_id", terminalID, "error", startErr)
		svc.failTerminalStartup(terminalID, gm, startErr, h)
		return
	}

	if postSpawnErr == nil && postSpawn.Title != "" {
		svc.Terminals.UpdateTitle(terminalID, postSpawn.Title)
	}

	// Apply any ResizeTerminal that arrived after the pre-spawn wait
	// above (e.g. the frontend's fit() was unusually slow, or a second
	// resize has since landed). The PTY is already the correct size for
	// the pre-wait case; this handler covers the rare late-arriving dims.
	if cols, rows, ok := svc.TerminalStartup.takePendingResize(terminalID); ok {
		if err := svc.Terminals.Resize(terminalID, cols, rows); err != nil {
			slog.Warn("apply pending resize after startup", "terminal_id", terminalID, "error", err)
		}
	}

	// Spawn succeeded and no close-race; hand cleanup ownership to the
	// eventual CloseTerminal handler.
	ownsIPCToken = false
	svc.succeedTerminalStartup(terminalID)
}

// runTerminalRestart is the async body of RestartTerminal: it spawns a
// new PTY through Manager.RestartTerminal and broadcasts READY or
// STARTUP_FAILED depending on the outcome. The handler seeded STARTING
// without git status; this goroutine re-broadcasts STARTING with the
// branch/origin once `git status` returns. No git-mode rollback path —
// restart never mutates worktrees. spawnInfo + the previous-token
// release run inside this goroutine so a slow RemoteIPC factory
// doesn't stretch the RPC latency the user sees.
func (svc *Service) runTerminalRestart(
	ctx context.Context,
	opts terminal.Options,
	spawnInfo TerminalSpawnInfo,
	fallbackOffset int64,
	outputFn terminal.OutputHandler,
	exitFn terminal.ExitHandler,
	h *startupEntry,
) {
	terminalID := opts.ID
	defer svc.TerminalStartup.finish()

	// Mint the fresh token BEFORE retiring the previous spawn's, parking the
	// new cleanup in a local instead of registering it. Both are keyed by
	// terminalID and register overwrites, so the swap has to be ordered, and
	// minting first shortens the window a concurrent CloseTerminal can slip
	// into: the slow factory call no longer sits inside it.
	//
	// The previous spawn's token is retired on EVERY exit from here, including
	// the fatal-identity path. The old PTY has already exited by the time this
	// runs -- RestartTerminal refuses synchronously while IsRunning -- so there
	// is no live shell whose remote control needs preserving, and leaving the
	// old cleanup registered would strand a listening unix socket plus an
	// unrevoked per-user delegation bearer for a process that is gone.
	var newCleanup func()
	remoteEnvs, ipcErr := svc.spawnControlIPC("terminal", terminalID, "restart", func(_ string, fn func()) {
		newCleanup = fn
	}, func() ([]string, func(), error) {
		return svc.ControlIPC.TerminalSpawning(spawnInfo)
	})
	if ipcErr != nil {
		// See the open path: a spawn that cannot name its user fails the
		// restart rather than silently relaunching without remote control.
		// Retire the dead spawn's token on the way out -- the restart is not
		// happening, so nothing will come along later to retire it.
		svc.terminalCleanups.run(terminalID)
		svc.failTerminalStartup(terminalID, gitModeResult{}, ipcErr, h)
		return
	}
	// The mint succeeded: retire the *old* token and take ownership of the
	// *new* one under the same key. The deferred cleanup below retires the
	// new token if the spawn never reaches READY.
	svc.terminalCleanups.run(terminalID)
	// Ownership is what was REGISTERED, not what the env vars imply. The two
	// were derived from different values here -- register on `newCleanup != nil`,
	// own on `remoteEnvs != nil` -- so any factory result with one and not the
	// other registered a cleanup that the deferred retire would never run,
	// leaking the socket and its delegation bearer for the tab's whole life.
	ownsIPCToken := newCleanup != nil
	if ownsIPCToken {
		svc.terminalCleanups.register(terminalID, newCleanup)
	}
	opts.ExtraEnv = remoteEnvs
	defer func() {
		if ownsIPCToken {
			svc.terminalCleanups.run(terminalID)
		}
	}()

	// Phase 1: fetch git status off the RPC goroutine and re-broadcast
	// STARTING with branch/origin attached. Working dir is stable across
	// restart (no git-mode mutation), so a single re-broadcast is enough.
	gs := gitutil.GetGitStatus(ctx, gitutil.ResolveGitDir(opts.ShellStartDir, opts.WorkingDir))
	shellMsg := terminalStartingLabel(opts.Shell)
	svc.TerminalStartup.setMessage(terminalID, shellMsg)
	svc.broadcastTerminalStarting(terminalID, shellMsg, gs)

	startErr := svc.Terminals.RestartTerminal(ctx, opts, fallbackOffset, outputFn, exitFn)

	// Detect a CloseTerminal that landed during the PTY spawn: if
	// closed_at is set we must neither broadcast READY nor leave a
	// freshly-spawned PTY behind. Re-fetching is best-effort — a
	// CloseTerminal whose DB write hasn't committed yet will be caught
	// by its own RemoveTerminal call instead. The title column from the
	// same row is unused on the restart path (titles don't change
	// across restart), so it's read-and-discarded.
	if postSpawn, fetchErr := svc.Queries.GetTerminalForReady(bgCtx(), terminalID); fetchErr == nil && postSpawn.ClosedAt.Valid {
		if startErr == nil {
			svc.Terminals.RemoveTerminal(terminalID)
			svc.clearTerminalBellCoalesce(terminalID)
		}
		svc.TerminalStartup.succeed(terminalID)
		return
	}

	if startErr != nil {
		slog.Error("failed to restart terminal", "terminal_id", terminalID, "error", startErr)
		// No git-mode mutation in the restart path — pass a zero result so
		// failTerminalStartup skips the rollback branch.
		svc.failTerminalStartup(terminalID, gitModeResult{}, startErr, h)
		return
	}

	ownsIPCToken = false
	svc.succeedTerminalStartup(terminalID)
}

// succeedTerminalStartup is the shared READY tail for runTerminalStartup
// and runTerminalRestart: clear the persisted startup_error, broadcast
// READY, and mark the registry succeeded last so observers see a durable
// terminal state.
func (svc *Service) succeedTerminalStartup(terminalID string) {
	svc.persistTerminalStartupError(terminalID, "")
	svc.broadcastTerminalReady(terminalID)
	svc.TerminalStartup.succeed(terminalID)
}

// runTerminalPhase0 broadcasts the per-mode label and executes the
// git-mode mutation.
func (svc *Service) runTerminalPhase0(ctx context.Context, terminalID string, plan gitModePlan, h *startupEntry) (gitModeResult, error) {
	return svc.runStartupPhase0(ctx, plan, svc.terminalStartupCallbacks(terminalID, h))
}

// failTerminalStartup is the common tail for every failure after the sync
// prologue: rolls back any partial git-mode mutation, persists the
// error, broadcasts STARTUP_FAILED, and marks the registry failed. The
// shared `failStartup` enforces the ordering (DB before broadcast
// before registry) so observers see a durable terminal state.
func (svc *Service) failTerminalStartup(terminalID string, gm gitModeResult, cause error, h *startupEntry) {
	svc.failStartup(gm, cause, svc.terminalStartupCallbacks(terminalID, h))
}

// persistTerminalStartupError writes (or clears when errMsg is "") the
// terminals.startup_error column so the startup panel survives a worker
// restart that wipes the in-memory registry.
func (svc *Service) persistTerminalStartupError(terminalID, errMsg string) {
	if err := svc.Queries.SetTerminalStartupError(bgCtx(), db.SetTerminalStartupErrorParams{
		StartupError: errMsg,
		ID:           terminalID,
	}); err != nil {
		action := "persist"
		if errMsg == "" {
			action = "clear"
		}
		slog.Warn("failed to "+action+" terminal startup error", "terminal_id", terminalID, "error", err)
	}
}

// buildTerminalStartingStatus builds a STARTING TerminalStatusChange
// carrying the current phase label. gs is nil for phases before git
// status has been computed (phase 0 mode labels, rollback labels, the
// seed broadcast from registerTerminalHandlers) and non-nil once
// runTerminalStartup's phase 1 has run `git status` on the final dir.
func buildTerminalStartingStatus(terminalID, message string, gs *leapmuxv1.AgentGitStatus) *leapmuxv1.TerminalStatusChange {
	sc := &leapmuxv1.TerminalStatusChange{
		TerminalId:     terminalID,
		Status:         leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING,
		StartupMessage: message,
	}
	if gs != nil {
		sc.GitBranch = gs.GetBranch()
		sc.GitOriginUrl = gs.GetOriginUrl()
		sc.GitToplevel = gs.GetToplevel()
		sc.GitIsWorktree = gs.GetIsWorktree()
	}
	return sc
}

// buildTerminalFailedStatus builds a STARTUP_FAILED TerminalStatusChange
// carrying the error message.
func buildTerminalFailedStatus(terminalID, errMsg string) *leapmuxv1.TerminalStatusChange {
	return &leapmuxv1.TerminalStatusChange{
		TerminalId:   terminalID,
		Status:       leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED,
		StartupError: errMsg,
	}
}

// buildTerminalReadyStatus builds a READY TerminalStatusChange.
func buildTerminalReadyStatus(terminalID string) *leapmuxv1.TerminalStatusChange {
	return &leapmuxv1.TerminalStatusChange{
		TerminalId: terminalID,
		Status:     leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY,
	}
}

// deriveTerminalStatus computes (status, startupError, startupMessage)
// for a terminal, in priority order:
//  1. in-memory startup registry — STARTING / STARTUP_FAILED while a
//     startup is in flight or has just failed. The current phase
//     message is surfaced so a WatchEvents subscriber that arrived
//     after the initial STARTING broadcast still sees the right label.
//  2. persisted startup_error column — surfaces a prior failure across
//     worker restarts (the in-memory registry is wiped on restart).
//  3. READY otherwise (the caller uses `Exited` to distinguish a
//     running terminal from an exited one).
func (svc *Service) deriveTerminalStatus(t *db.Terminal) (status leapmuxv1.TerminalStatus, startupError, startupMessage string) {
	if sup, errStr, msg, ok := svc.TerminalStartup.status(t.ID); ok {
		return sup, errStr, msg
	}
	if t.StartupError != "" {
		return leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED, t.StartupError, ""
	}
	return leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY, "", ""
}

// effectiveTerminalTitle returns the title a terminal tab shows right now.
// The in-memory manager holds it while the terminal is live and the persisted
// row holds it after the shell exits and the manager drops the entry -- the
// same precedence ListTerminals applies when it builds a TerminalInfo, so the
// two RPCs cannot report different titles for the same terminal.
//
// UpdateTerminalTitle calls it on the path that stores nothing (the request's
// title cleaned to empty), so the reply reports the title in force instead of
// the empty string the handler refused to store.
func (svc *Service) effectiveTerminalTitle(row db.Terminal) string {
	if meta, ok := svc.Terminals.GetMeta(row.ID); ok {
		return meta.Title
	}
	return row.Title
}

// broadcastTerminalStarting fans out a STARTING TerminalStatusChange.
// Used by runTerminalStartup for each phase label transition; gs is
// non-nil only once phase 1 has computed git status.
func (svc *Service) broadcastTerminalStarting(terminalID, message string, gs *leapmuxv1.AgentGitStatus) {
	svc.Watchers.BroadcastTerminalEvent(terminalID, &leapmuxv1.TerminalEvent{
		TerminalId: terminalID,
		Event: &leapmuxv1.TerminalEvent_StatusChange{
			StatusChange: buildTerminalStartingStatus(terminalID, message, gs),
		},
	})
}

// broadcastTerminalFailed fans out a STARTUP_FAILED TerminalStatusChange.
func (svc *Service) broadcastTerminalFailed(terminalID, errMsg string) {
	svc.Watchers.BroadcastTerminalEvent(terminalID, &leapmuxv1.TerminalEvent{
		TerminalId: terminalID,
		Event: &leapmuxv1.TerminalEvent_StatusChange{
			StatusChange: buildTerminalFailedStatus(terminalID, errMsg),
		},
	})
}

// broadcastTerminalReady fans out a READY TerminalStatusChange.
func (svc *Service) broadcastTerminalReady(terminalID string) {
	svc.Watchers.BroadcastTerminalEvent(terminalID, &leapmuxv1.TerminalEvent{
		TerminalId: terminalID,
		Event: &leapmuxv1.TerminalEvent_StatusChange{
			StatusChange: buildTerminalReadyStatus(terminalID),
		},
	})
}

// bellCoalesceWindow limits TerminalBell broadcasts: a program can emit BEL
// per keystroke, so at most one bell event per terminal per window is enough.
const bellCoalesceWindow = 250 * time.Millisecond

// notificationBodyByteLimit caps the OSC notification body that the browser
// hands to the OS notification service.
//
// The body is a message rather than a name, so validate.NameByteLimit is too
// short for it: a build failure or a test summary needs more than one line.
// The cap exists because the process that wrote the OSC chose the length, and
// oscBufCap alone lets it be 2 KB of one word that no notification panel can
// render.
const notificationBodyByteLimit = 512

// makeTerminalOutputFn builds the OutputHandler closure that broadcasts
// data events to subscribers and pings the wake lock.
func (svc *Service) makeTerminalOutputFn(terminalID string) terminal.OutputHandler {
	return func(data []byte, endOffset int64, signals []terminal.Signal) {
		if svc.WakeLock != nil {
			svc.WakeLock.RecordActivity()
		}
		// TerminalData is classContent, so BroadcastTerminalEvent self-gates:
		// when nobody watches this terminal in FULL it short-circuits before
		// the snapshot/marshal, and NOTIFY-only watchers receive just signals.
		svc.Watchers.BroadcastTerminalEvent(terminalID, &leapmuxv1.TerminalEvent{
			TerminalId: terminalID,
			Event: &leapmuxv1.TerminalEvent_Data{
				Data: &leapmuxv1.TerminalData{
					Data:      data,
					EndOffset: endOffset,
				},
			},
		})
		for _, sig := range signals {
			svc.broadcastTerminalSignal(terminalID, sig)
		}
	}
}

func (svc *Service) broadcastTerminalSignal(terminalID string, sig terminal.Signal) {
	switch sig.Kind {
	case terminal.SignalBell:
		now := time.Now()
		svc.terminalBellMu.Lock()
		last := svc.lastBellAt[terminalID]
		if !last.IsZero() && now.Sub(last) < bellCoalesceWindow {
			svc.terminalBellMu.Unlock()
			return
		}
		svc.lastBellAt[terminalID] = now
		svc.terminalBellMu.Unlock()
		svc.Watchers.BroadcastTerminalEvent(terminalID, &leapmuxv1.TerminalEvent{
			TerminalId: terminalID,
			Event:      &leapmuxv1.TerminalEvent_Bell{Bell: &leapmuxv1.TerminalBell{}},
		})
	case terminal.SignalNotification:
		// The PTY wrote this, so ANY process with the terminal open chose the
		// text: the user's shell, an `ssh` session whose prompt the REMOTE
		// host writes, a `cat` of a hostile file, or a command an agent ran.
		// The browser hands both fields to the OS notification service, so
		// clean them here for the reason every other title writer cleans:
		// a right-to-left override reorders what the reader sees, and an
		// invisible run pads text past the width the visible characters fit.
		//
		// The title takes the name rule, because it IS a title. The body is a
		// message, so it takes the strip and the cap WITHOUT the fold and the
		// trim: a run of spaces inside a build summary is the sender's
		// formatting, and nothing here has a reason to rewrite it.
		//
		// The body still loses every character a reader cannot see, the line
		// breaks included. That is one definition of "unreadable" rather than
		// two, and an OS notification renders a body as one block in any case.
		// notificationBodyByteLimit caps it, because the process that wrote
		// the OSC chose the length and the notification panel has no limit of
		// its own.
		svc.Watchers.BroadcastTerminalEvent(terminalID, &leapmuxv1.TerminalEvent{
			TerminalId: terminalID,
			Event: &leapmuxv1.TerminalEvent_Notification{
				Notification: &leapmuxv1.TerminalNotification{
					Title: validate.CleanName(sig.Title),
					Body:  validate.StripUnreadable(sig.Body, notificationBodyByteLimit),
				},
			},
		})
	case terminal.SignalTitle:
		// Broadcast the live OSC title only — do NOT write Meta.Title. That
		// field is owned by the user-rename path (UpdateTerminalTitle) and by
		// persistTerminalOnExit, which persists TerminalMeta.Title into the DB
		// title column on shell exit. Writing the OSC value into Meta.Title
		// would (a) clobber an in-memory user rename on the next ListTerminals
		// hydration (the client maps Title → tab.title, which tabDisplayLabel
		// prefers over the live ptyTitle) and (b) leak the OSC title into the DB
		// title column when the shell exits. The frontend already routes the
		// TerminalEvent_TitleChanged payload into a separate ptyTitle overlay
		// that yields to an explicit rename, so the broadcast alone is the
		// correct worker-side effect.
		//
		// CleanName runs here for the reason UpdateTerminalTitle runs it above,
		// and this is the writer that needs it most: the bytes came from
		// whatever process holds the PTY, which includes the REMOTE host of an
		// `ssh` session and any command an agent ran. The browser renders the
		// result as the tab label and the tab tooltip, so an OSC title of
		// "‮txt.exe" reordered what the tab strip showed, and an OSC
		// title could reach oscBufCap (2048 bytes), 16 times the limit every
		// other writer of a tab title obeys.
		//
		// A title that cleans to nothing is a no-op, not a clear: the same
		// answer UpdateTerminalTitle gives, and the answer the browser's own
		// `if (!value.title) return` already expects.
		title := validate.CleanName(sig.Title)
		if title == "" {
			return
		}
		svc.Watchers.BroadcastTerminalEvent(terminalID, &leapmuxv1.TerminalEvent{
			TerminalId: terminalID,
			Event: &leapmuxv1.TerminalEvent_TitleChanged{
				TitleChanged: &leapmuxv1.TerminalTitleChanged{Title: title},
			},
		})
	case terminal.SignalProgress:
		svc.Watchers.BroadcastTerminalEvent(terminalID, &leapmuxv1.TerminalEvent{
			TerminalId: terminalID,
			Event: &leapmuxv1.TerminalEvent_Progress{
				Progress: &leapmuxv1.TerminalProgress{
					State:   terminalProgressState(sig.State),
					Percent: sig.Percent,
				},
			},
		})
	}
}

func terminalProgressState(state terminal.ProgressState) leapmuxv1.TerminalProgress_State {
	switch state {
	case terminal.ProgressNormal:
		return leapmuxv1.TerminalProgress_STATE_NORMAL
	case terminal.ProgressError:
		return leapmuxv1.TerminalProgress_STATE_ERROR
	case terminal.ProgressIndeterminate:
		return leapmuxv1.TerminalProgress_STATE_INDETERMINATE
	case terminal.ProgressPaused:
		return leapmuxv1.TerminalProgress_STATE_PAUSED
	default:
		return leapmuxv1.TerminalProgress_STATE_UNSPECIFIED
	}
}

// makeTerminalExitFn builds the ExitHandler that runs when the shell
// process exits: append the "Press Enter to restart" notice, persist
// the final screen + metadata to the DB so a worker restart still finds
// an exited row, and broadcast TerminalClosed. Does not set closed_at —
// only explicit user close does that. Clears the bell-coalesce timer so a
// naturally-exited terminal (which stays in the Manager for restart-via-Enter)
// cannot leak its lastBellAt entry for the worker's life — the explicit-close
// paths clear it via clearTerminalBellCoalesce, but the exit path does not flow
// through those.
func (svc *Service) makeTerminalExitFn() terminal.ExitHandler {
	return func(tid string, exitCode int) {
		svc.persistTerminalOnExit(tid, exitCode)
		svc.clearTerminalBellCoalesce(tid)
		svc.Watchers.BroadcastTerminalEvent(tid, &leapmuxv1.TerminalEvent{
			TerminalId: tid,
			Event: &leapmuxv1.TerminalEvent_Closed{
				Closed: &leapmuxv1.TerminalClosed{
					ExitCode: int32(exitCode),
				},
			},
		})
	}
}

// clearTerminalBellCoalesce drops the per-terminal bell timer so closed
// terminals cannot leak lastBellAt entries for the life of the worker.
func (svc *Service) clearTerminalBellCoalesce(terminalID string) {
	svc.terminalBellMu.Lock()
	delete(svc.lastBellAt, terminalID)
	svc.terminalBellMu.Unlock()
}
