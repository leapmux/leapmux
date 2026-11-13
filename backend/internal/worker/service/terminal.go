package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"

	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/leapmux/leapmux/util/validate"
)

// pendingResizeWaitCap limits how long runTerminalStartup blocks waiting
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
// cancellable background context, and seeds the "Starting <shell>…" message on
// the registry. It broadcasts nothing: announceTerminalStarting does that. The
// two are separate because OpenTerminal registers BEFORE it writes the row, and
// announces only after the row exists. Returns the ctx AND the startup handle
// the caller passes into runTerminalStartup / runTerminalRestart.
//
// It returns a nil handle when another startup holds the id. The caller then
// started nothing.
func (svc *Service) beginTerminalStartup(terminalID, shell string) (context.Context, *startupEntry) {
	startupCtx, cancel := context.WithCancel(context.Background())
	h := svc.TerminalStartup.begin(terminalID, cancel)
	if h == nil {
		// A startup for this terminal is already in flight. Every caller checks
		// the registry first, but that check and this claim straddle no lock and
		// each RPC runs on its own goroutine, so the claim is what actually
		// decides. Release the context we will not use.
		cancel()
		return nil, nil
	}
	svc.TerminalStartup.setMessage(terminalID, terminalStartingLabel(shell))
	return startupCtx, h
}

// announceTerminalStarting broadcasts STARTING to the watchers of terminalID. It
// carries no git status: the async goroutine fetches git status and re-broadcasts
// with the branch and origin once it lands, and the frontend keeps its existing
// git fields when STARTING arrives without them, so a first broadcast with no
// status does not clobber them.
func (svc *Service) announceTerminalStarting(terminalID, shell string) {
	svc.broadcastTerminalStarting(terminalID, terminalStartingLabel(shell), nil)
}

// terminalProcessScanTimeout caps one InspectTerminalProcesses call. The scan is
// a few thousand cheap syscalls on a busy machine, but it reads a table the
// Worker does not own, so it gets a deadline rather than the caller's patience.
const terminalProcessScanTimeout = 5 * time.Second

// validateTerminalDimensions rejects values that cannot reach the PTY without
// narrowing. Zero remains available for each caller's existing default policy.
func validateTerminalDimensions(cols, rows uint32) error {
	if cols > math.MaxUint16 || rows > math.MaxUint16 {
		return fmt.Errorf("columns and rows must be at most %d", math.MaxUint16)
	}
	return nil
}

// registerTerminalHandlers registers all terminal-related RPC handlers.
func registerTerminalHandlers(d ownerOnlyRegistrar, svc *Service) {
	// InspectTerminalProcesses lists what runs inside each terminal, so a
	// close guard can state the reason before it tears the tab down. Batched:
	// closing a tile asks about every terminal it holds at once.
	//
	// registerOwnerGuarded rather than registerTerminalGuardedByID: the request
	// carries a LIST, and a gate keyed to one id cannot answer for a list. The
	// owner gate is the whole access decision here -- a Worker's terminals all
	// belong to its one owner -- and an id that names no live PTY simply drops
	// out of the response, which is the same answer an idle terminal gets.
	registerOwnerGuarded(d, leapmuxv1connect.WorkerTerminalServiceInspectTerminalProcessesProcedure, dispatchPlain,
		func(ctx context.Context, _ channel.Caller, r *leapmuxv1.InspectTerminalProcessesRequest, sender channel.ResponseWriter) {
			ctx, cancel := context.WithTimeout(ctx, terminalProcessScanTimeout)
			defer cancel()
			ids := make([]string, 0, len(r.GetTerminalIds()))
			for _, terminalID := range r.GetTerminalIds() {
				if terminalID != "" {
					ids = append(ids, terminalID)
				}
			}
			// One process-table scan for the whole batch, and the manager omits
			// every terminal that is running nothing.
			running, err := svc.Terminals.DescendantProcesses(ctx, ids)
			if err != nil {
				// A scan the OS refused answers "nothing running" rather than an
				// error. A guard that cannot answer has to let the close through,
				// and an error here would instead make the whole close fail.
				slog.Warn("inspect terminal processes", "terminal_ids", len(ids), "error", err)
			}
			out := make([]*leapmuxv1.TerminalProcesses, 0, len(running))
			for _, t := range running {
				wire := make([]*leapmuxv1.TerminalProcess, 0, len(t.Processes))
				for _, proc := range t.Processes {
					wire = append(wire, &leapmuxv1.TerminalProcess{Pid: proc.PID, Name: proc.Name})
				}
				out = append(out, &leapmuxv1.TerminalProcesses{
					TerminalId: t.TerminalID,
					Processes:  wire,
					TotalCount: int32(t.Total),
				})
			}
			sendProtoResponse(sender, &leapmuxv1.InspectTerminalProcessesResponse{Terminals: out})
		})

	// OpenTerminal starts a new PTY terminal session.
	registerOwnerGuarded(d, leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure, dispatchTracked,
		svc.handleOpenTerminal)

	// RestartTerminal respawns the shell process for a terminal whose
	// previous PTY has exited. Reuses the tab's working_dir / shell /
	// shell_start_dir and mints fresh LEAPMUX_CONTROL_* env vars. The
	// existing screen buffer (including the "[Terminal process exited
	// (N) - Press Enter to restart]" notice) is preserved so the new
	// shell's prompt lands directly below the notice.
	registerTerminalForRestartGuarded(d, leapmuxv1connect.WorkerTerminalServiceRestartTerminalProcedure,
		func(_ context.Context, caller channel.Caller, r *leapmuxv1.RestartTerminalRequest, dbTerm db.GetTerminalForRestartRow, sender channel.ResponseWriter) {
			terminalID := r.GetTerminalId()
			creationCtx, cancelCreationWait := context.WithTimeout(bgCtx(), svc.creationAttemptTimeout())
			creationErr := svc.awaitCreationStartup(creationCtx, leapmuxv1.TabType_TAB_TYPE_TERMINAL, terminalID)
			cancelCreationWait()
			if creationErr != nil {
				sendFailedPrecondition(sender, creationErr.Error())
				return
			}
			latest, readErr := svc.Queries.GetTerminalForRestart(bgCtx(), terminalID)
			if readErr != nil {
				sendInternalError(sender, readErr.Error())
				return
			}
			dbTerm = latest
			if refuseArchivedWrite(sender, leapmuxv1.Scope_SCOPE_TERMINAL_WRITE, dbTerm.WorkspaceArchived, "terminal") {
				return
			}

			if svc.refuseIfShuttingDown(sender) {
				return
			}
			// A QUAKE terminal has no restart contract: its shell exiting ends
			// it, and the next open of the panel spawns a fresh one. Refused
			// here rather than only in the browser, so the rule holds for the
			// Control CLI and for a second device too. Asked BEFORE the
			// liveness checks, because "does this terminal restart at all?" is
			// prior to "is it running right now". Without it a respawn
			// resurrects a row the exit path already closed, and the unique
			// index then refuses every replacement.
			if dbTerm.IsQuake {
				sendFailedPrecondition(sender, "a quake terminal does not restart; open the panel again for a fresh shell")
				return
			}
			// Reject overlapping restarts: a previous startup hasn't broadcast
			// READY/FAILED yet (could be the original OpenTerminal still in
			// flight, or a back-to-back restart).
			if svc.TerminalStartup.hasUnfinishedClaim(terminalID) {
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
			if err := validateTerminalDimensions(cols, rows); err != nil {
				sendInvalidArgument(sender, err.Error())
				return
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
			startupCtx, startupHandle := svc.beginTerminalStartup(terminalID, shell)
			if startupHandle == nil {
				// Two RestartTerminal RPCs for one terminal. The in-flight check
				// above and this claim straddle no lock, and the dispatcher gives
				// every request its own goroutine, so the claim is what decides.
				// The loser must not proceed: its spawn would race the winner's
				// for one PTY and one control socket. During a shutdown the
				// claim is refused by the latch, and the answer states that.
				svc.sendStartupRefused(sender, "a restart for this terminal is already in progress")
				return
			}
			if !svc.Terminals.ExitHandlerFinished(terminalID) {
				svc.TerminalStartup.abandon(startupHandle)
				sendFailedPrecondition(sender, "terminal exit cleanup is still in progress; try again")
				return
			}
			svc.announceTerminalStarting(terminalID, shell)

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
			// IsQuake stays false: the refusal above means only a terminal TAB
			// reaches this line, and a tab is its own ambient tab.
			spawnInfo := TerminalSpawnInfo{
				UserID:     caller.UserID,
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

	// SendInput sends input data to a terminal.
	registerTerminalGuardedByID(d, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, dispatchPlain,
		func(_ context.Context, _ channel.Caller, r *leapmuxv1.SendInputRequest, sender channel.ResponseWriter) {
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
	registerTerminalGuardedByID(d, leapmuxv1connect.WorkerTerminalServiceResizeTerminalProcedure, dispatchPlain,
		func(ctx context.Context, _ channel.Caller, r *leapmuxv1.ResizeTerminalRequest, sender channel.ResponseWriter) {
			terminalID := r.GetTerminalId()

			cols := r.GetCols()
			rows := r.GetRows()
			if cols == 0 || rows == 0 {
				sendInvalidArgument(sender, "cols and rows must be > 0")
				return
			}
			if err := validateTerminalDimensions(cols, rows); err != nil {
				sendInvalidArgument(sender, err.Error())
				return
			}

			err := svc.TerminalStartup.withTerminalResize(ctx, terminalID, func(h *startupEntry) error {
				resizeErr := svc.Terminals.Resize(terminalID, uint16(cols), uint16(rows))
				switch {
				case resizeErr == nil:
					svc.TerminalStartup.clearPendingResize(h)
					return nil
				case errors.Is(resizeErr, terminal.ErrTerminalInstanceChanged):
					// The request belongs to an obsolete physical instance.
					return nil
				case errors.Is(resizeErr, terminal.ErrTerminalNotFound):
					saved, saveErr := svc.saveCreationTerminalResize(ctx, terminalID, uint16(cols), uint16(rows))
					if saveErr != nil {
						return saveErr
					}
					if saved {
						svc.TerminalStartup.setPendingResize(h, uint16(cols), uint16(rows))
						return nil
					}
					// Startup owns these dimensions until its physical terminal is ready.
					if svc.TerminalStartup.setPendingResize(h, uint16(cols), uint16(rows)) {
						return nil
					}
				}
				return resizeErr
			})
			if err != nil {
				slog.Error("failed to resize terminal", "terminal_id", terminalID, "error", err)
				sendInternalError(sender, fmt.Sprintf("resize: %v", err))
				return
			}

			sendProtoResponse(sender, &leapmuxv1.ResizeTerminalResponse{})
		})

	// RenameTerminal updates a terminal's title in both the in-memory
	// manager and the database. It is the only writer of a USER-SUPPLIED
	// title; OpenTerminal writes the worker-assigned name at creation and
	// persistTerminalOnExit writes TerminalMeta.Title on shell exit.
	//
	// The worker never persists a PTY-driven OSC title. It broadcasts that
	// title as a live overlay instead -- see the SignalTitle case in this
	// file for the reason.
	registerTerminalGuarded(d, leapmuxv1connect.WorkerTerminalServiceRenameTerminalProcedure,
		func(_ context.Context, caller channel.Caller, r *leapmuxv1.RenameTerminalRequest, dbTerm db.Terminal, sender channel.ResponseWriter) {
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
				sendProtoResponse(sender, &leapmuxv1.RenameTerminalResponse{
					Title: svc.effectiveTerminalTitle(dbTerm),
				})
				return
			}

			svc.Terminals.UpdateTitle(terminalID, title)
			// Update only the title. A concurrent close owns closed_at.
			if err := svc.Queries.UpdateTerminalTitle(bgCtx(), db.UpdateTerminalTitleParams{
				ID: dbTerm.ID, Title: title,
			}); err != nil {
				slog.Error("failed to update terminal title", "terminal_id", terminalID, "error", err)
				sendInternalError(sender, "failed to update terminal title")
				return
			}

			if svc.PrivateEvents != nil {
				svc.PrivateEvents.PublishTabRenamed(
					caller.UserID, terminalID, leapmuxv1.TabType_TAB_TYPE_TERMINAL,
					title, sender.ChannelID(),
				)
			}

			sendProtoResponse(sender, &leapmuxv1.RenameTerminalResponse{Title: title})
		})

	// ListTerminals resolves the records behind a set of terminal tab ids.
	// Uses the in-memory terminal manager for running terminals and falls
	// back to saved terminal records for terminals that have already exited
	// and been removed from the manager.
	registerOwnerGuarded(d, leapmuxv1connect.WorkerTerminalServiceListTerminalsProcedure, dispatchPlain, func(ctx context.Context, caller channel.Caller, r *leapmuxv1.ListTerminalsRequest, sender channel.ResponseWriter) {
		// Verdicts answer for the ids the caller NAMED, so the requested set is
		// captured before the quake lookup widens it. A directory with no quake
		// terminal is an absence, not a failed hydration -- the client asks
		// precisely to find out whether one exists -- so it gets no verdict.
		requestedTabIDs := r.GetTabIds()
		tabIDs := requestedTabIDs

		// Normalized with the SAME rule OpenTerminal applies to the directory it
		// stores, because a lookup that spelled the path differently would miss
		// the row it means. An entry that will not normalize is DROPPED rather
		// than failing the call: this list is a lookup, and the rest of the
		// request still has an answer.
		//
		// An EMPTY directory is dropped BEFORE that, and the order matters:
		// normalizeWorkingDir resolves an empty path to the worker's home, so
		// normalizing first would silently turn "I have no directory" into a
		// probe of the home directory's panel.
		//
		// file:read IS required, on top of this handler's terminal:read.
		//
		// The owner_agent_id form this replaced needed agent:read, because the
		// reply would have been an agent-id oracle. The directory form is not
		// that, but it is a new REACH: a quake terminal has no CRDT tab, so its
		// id never appears in the hub's tab list, and no worker RPC enumerates
		// terminals -- so before this, a `terminal:read` caller could not name
		// one at all. Keyed on a path it can simply guess, it can now ask
		// whether any absolute directory has a live shell, and read that
		// shell's screen.
		//
		// file:read is the scope that names a filesystem path, and
		// WatchWorkerPrivateEvents already requires it to deliver this very
		// working_dir in a QuakePanelCommand. Requiring it here is what stops
		// the two surfaces answering the same question at different prices. The
		// browser holds it already, so the first-party client loses nothing.
		var quakeWorkingDirs []string
		mayReadPaths := caller.Allows(leapmuxv1.Scope_SCOPE_FILE_READ)
		for _, raw := range r.GetQuakeWorkingDirs() {
			if raw == "" || !mayReadPaths {
				continue
			}
			workingDir, dirErr := normalizeWorkingDir(raw, svc.HomeDir, svc.HomeDir)
			if dirErr != nil {
				continue
			}
			quakeWorkingDirs = append(quakeWorkingDirs, workingDir)
		}
		if len(tabIDs) == 0 && len(quakeWorkingDirs) == 0 {
			sendProtoResponse(sender, &leapmuxv1.ListTerminalsResponse{})
			return
		}

		// Resolve quake terminals to their terminal ids and fold them into the
		// same id set the rest of this handler already walks. Widening the ids
		// rather than building a second result list is what keeps them on the
		// live path: the manager pass below owns the in-memory ring, and a
		// quake terminal resolved straight from its DB row would hand the
		// client a stale screen for a shell that is still running.
		if len(quakeWorkingDirs) > 0 {
			quakeRows, quakeErr := svc.Queries.ListOpenQuakeTerminalsByWorkingDirs(ctx, quakeWorkingDirs)
			if quakeErr != nil {
				slog.Error("failed to list quake terminals from DB", "working_dirs", quakeWorkingDirs, "error", quakeErr)
				sendInternalError(sender, "failed to list terminals")
				return
			}
			widened := slices.Clone(tabIDs)
			known := make(map[string]struct{}, len(widened))
			for _, tabID := range widened {
				known[tabID] = struct{}{}
			}
			for _, row := range quakeRows {
				if _, dup := known[row.ID]; dup {
					continue
				}
				// The SAME liveness rule adoptExistingQuakeTerminal applies,
				// and it has to be here too: the browser LISTS before it opens,
				// so a row this fold hands back is adopted without ever
				// reaching the open path that would have reaped it. A dead row
				// there gives the user a panel that paints nothing and swallows
				// every keystroke, and it never recovers -- a quake terminal
				// refuses the Enter that restarts a terminal tab.
				if !svc.quakeShellIsLive(row.ID) {
					svc.CloseTabForReconcile(leapmuxv1.TabType_TAB_TYPE_TERMINAL, "", row.ID)
					continue
				}
				known[row.ID] = struct{}{}
				widened = append(widened, row.ID)
			}
			tabIDs = widened
		}
		if len(tabIDs) == 0 {
			sendProtoResponse(sender, &leapmuxv1.ListTerminalsResponse{})
			return
		}

		// The DB pass is the ONE source of every terminal's is_quake flag and
		// persisted startup_error, and it runs first so both passes below can
		// read from it. The manager holds the live ring but neither fact, so a
		// map built beside it needed a third loop afterwards to stamp the
		// entries it had already produced -- three writers for one derived
		// fact.
		//
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
		rowsByID := make(map[string]*db.Terminal, len(dbTerminals))
		for i := range dbTerminals {
			rowsByID[dbTerminals[i].ID] = &dbTerminals[i]
		}

		// Run git FIRST. It is the one slow step of this call: a large working
		// tree takes seconds. The reply leaves the Worker after the last step, and
		// the client takes it as the state of each terminal. A state that the call
		// read before git is older than every event that the Worker broadcast
		// while git ran. A client that applied such an event, such as the READY at
		// the end of a startup or the close of a shell, then takes the older reply
		// over it, and nothing sends the event again. The call therefore reads the
		// registry and the Manager after git, with no slow step between the read
		// and the reply.
		//
		// The row of a terminal holds the directory that the terminal finally runs
		// in (see gitModePlan.PlannedWorkingDir), and the Manager takes the same
		// directory. Git therefore needs no state that the call reads later.
		gitStatuses := svc.terminalGitStatuses(ctx, tabIDs, dbTerminals)

		// Collect from the in-memory manager and DB-only rows. This is the read of
		// the runtime state: see the note above the call to git.
		//
		// Both passes take the status from deriveTerminalStatus, the one
		// statement of the rule, which also feeds the WatchEvents replay. It
		// reads the registry first and the Manager last, so a startup that ends
		// in between reads READY and running, and never READY and exited. A
		// terminal can register in the Manager after the read of ListByIDs, and
		// the DB-only pass covers it the same way.
		//
		// The manager pass does not read the Exited flag of its entry, because
		// that flag can disagree with the Manager when the status is read.
		// ListByIDs took the flag BEFORE the registry read. A restart that ends
		// in between pairs the flag of the old, exited shell with a registry that
		// no longer holds the startup, so the reply would say EXITED over a shell
		// that runs. A shell that exits in between disagrees the other way.
		// IsRunning, read last, states the terminal closest to the reply.
		entries := svc.Terminals.ListByIDs(tabIDs)
		seen := make(map[string]bool, len(entries))
		var terminals []*leapmuxv1.TerminalInfo
		for _, e := range entries {
			seen[e.ID] = true
			row := rowsByID[e.ID]
			if row == nil {
				// The Manager can hold a terminal that the DB read did not
				// return, such as one whose close is in progress: the query
				// returns no closed row. The reply then has no persisted
				// startup_error or is_quake flag to read for it.
				row = &db.Terminal{ID: e.ID}
			}
			status, startupError, startupMessage := svc.deriveTerminalStatus(row)
			ti := &leapmuxv1.TerminalInfo{
				TerminalId:      e.ID,
				Cols:            e.Meta.Cols,
				Rows:            e.Meta.Rows,
				Screen:          e.Screen,
				ScreenEndOffset: e.ScreenEndOffset,
				WorkingDir:      e.Meta.WorkingDir,
				ShellStartDir:   e.Meta.ShellStartDir,
				Title:           e.Meta.Title,
				Status:          status,
				StartupError:    startupError,
				StartupMessage:  startupMessage,
				Quake:           row.IsQuake,
				GitStatus:       gitStatuses[e.ID],
			}
			terminals = append(terminals, ti)
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
				WorkingDir:      ts.WorkingDir,
				ShellStartDir:   ts.ShellStartDir,
				Title:           ts.Title,
				Status:          status,
				StartupError:    startupError,
				StartupMessage:  startupMessage,
				Quake:           ts.IsQuake,
				GitStatus:       gitStatuses[ts.ID],
			}
			terminals = append(terminals, ti)
		}

		sendProtoResponse(sender, &leapmuxv1.ListTerminalsResponse{
			Terminals: terminals,
			Verdicts:  tabHydrationVerdicts(requestedTabIDs, seen),
		})
	})

	// SetQuakePanel relays a show/hide request for one WORKING DIRECTORY's quake
	// panel to every frontend the caller has open on this worker.
	//
	// It writes nothing. The panel's open state is client-local -- the same line
	// the active tab in a tile is on -- so this is a remote keystroke, not a
	// setting. The Control CLI is the only caller; a frontend toggles its own
	// panel in-process.
	//
	// Registered beside ListTerminals rather than with the agent handlers: the
	// request carries a directory and no agent id, and its CLI verb is
	// `terminal quake`.
	//
	// terminal:write and nothing else, because the request specifies a DIRECTORY
	// and what it ultimately does is put a shell in front of the user. It used
	// to take agent:read too, when the panel was addressed by agent id and the
	// reply would have been an agent-id oracle; there is no agent id on this
	// wire any more, and privateEventVisible drops the matching check for the
	// same reason.
	//
	// It specifies no tab, so there is no row to load and no per-tab archive
	// refusal to apply. The frontend refuses a cold open in an archived
	// workspace on its own (see the quake store's isWorkspaceMutatable), which
	// is the check that matters: this RPC stores nothing that an archived
	// workspace could be harmed by.
	registerOwnerGuarded(d, leapmuxv1connect.WorkerPrivateServiceSetQuakePanelProcedure, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, r *leapmuxv1.SetQuakePanelRequest, sender channel.ResponseWriter) {
			if r.GetAction() == leapmuxv1.QuakePanelAction_QUAKE_PANEL_ACTION_UNSPECIFIED {
				sendInvalidArgument(sender, "action must be open, close or toggle")
				return
			}
			// Refused BEFORE the normalize below, and the order matters for the
			// reason ListTerminals states at its own quake lookup:
			// normalizeWorkingDir resolves an empty path to the worker's home
			// directory, so normalizing first would silently turn "I have no
			// directory" into a command against the home directory's panel --
			// broadcast to every frontend the caller has open.
			if strings.TrimSpace(r.GetWorkingDir()) == "" {
				sendInvalidArgument(sender, "working_dir is required: it is the address of the panel")
				return
			}
			// Normalized with the SAME rule OpenTerminal applies to the
			// directory it stores, because the two must produce the identical
			// string: the frontend matches this event against the working dir
			// of the tab it has focused, which came from a worker row. A `~`
			// or a relative path that reached a frontend unexpanded would
			// address a panel that can never exist.
			workingDir, err := normalizeWorkingDir(r.GetWorkingDir(), svc.HomeDir, svc.HomeDir)
			if err != nil {
				sendInvalidArgument(sender, err.Error())
				return
			}
			// An OPENING command for a directory no tab works in is refused,
			// because no client could act on it. Every frontend resolves the
			// event back to a tab (`findTabInWorkingDir`) and needs one to
			// decide the workspace a cold open is refused in -- so with no tab
			// anywhere, this RPC answered success for a command that did
			// nothing, and a typo'd --working-dir was indistinguishable from a
			// working invocation.
			//
			// CLOSE is deliberately exempt. It needs only the key, and it is
			// the one action that can act on a directory whose tabs the calling
			// client cannot see -- on a phone it is the only way to dismiss a
			// panel that covers the whole centre area.
			//
			// A read FAILURE publishes rather than refuses: a busy database
			// must not cost the user a command they can otherwise act on.
			if r.GetAction() != leapmuxv1.QuakePanelAction_QUAKE_PANEL_ACTION_CLOSE {
				tabs, tabsErr := openTabsInWorkingDirs(ctx, svc.Queries, []string{workingDir})
				if tabsErr != nil {
					slog.Warn("failed to check the tabs of a quake panel directory",
						"working_dir", workingDir, "error", tabsErr)
				} else if len(tabs) == 0 {
					sendFailedPrecondition(sender, "no tab works in that directory on this worker")
					return
				}
			}
			if svc.PrivateEvents != nil {
				svc.PrivateEvents.PublishQuakePanelCommand(caller.UserID, workingDir, r.GetAction())
			}
			sendProtoResponse(sender, &leapmuxv1.SetQuakePanelResponse{})
		})

	// ListAvailableShells returns the shells installed on this worker.
	// Owner-only: like sysinfo, it discloses machine-scoped state (which
	// shell binaries are present on the host), so a non-owner channel --
	// notably a delegation bearer minted for a different user -- must not
	// reach it. The worker owner's own agents (via the local-IPC remote CLI,
	// which dispatches with the owner's user id) still pass this gate.
	registerOwnerGuarded(d, leapmuxv1connect.WorkerTerminalServiceListAvailableShellsProcedure, dispatchPlain, func(ctx context.Context, caller channel.Caller, _ *leapmuxv1.ListAvailableShellsRequest, sender channel.ResponseWriter) {
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
	defer svc.TerminalStartup.finishEntry(h)
	defer func() { svc.finishCreationStartup(h.creation, svc.TerminalStartup.creationOutcome(h)) }()

	// Mint the remote-IPC token before phase 0 so the cleanup is in the
	// map by the time any concurrent CloseTab calls terminalCleanups.run.
	// Ownership lives with this goroutine until we broadcast READY; until
	// then, the deferred cleanup retires the token on every error path so
	// a close that lost the register-vs-cleanup race doesn't leak it.
	// When no token was minted (RemoteIPC disabled or factory failed),
	// nothing was registered, so the defer skips the mutex roundtrip.
	remoteEnvs, ownsIPCToken, ipcErr := svc.spawnControlIPC(string(tabKindTerminal), terminalID, "open", svc.terminalCleanups.register, func() ([]string, func(), error) {
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
	// Ownership is what the registry STORED, which spawnControlIPC reports. It
	// is NOT `opts.ExtraEnv != nil`: the env vars and the cleanup are separate
	// values, so a factory result with one and not the other would register a
	// cleanup this deferred retire never runs, leaking the listening socket and
	// its unrevoked delegation bearer for the tab's whole life.
	defer func() {
		if ownsIPCToken {
			svc.terminalCleanups.retire(terminalID)
		}
	}()

	// Phase 0: execute git-mode mutation (worktree add, branch create,
	// checkout). Validation already ran synchronously.
	gm, gmErr := svc.runTerminalPhase0(ctx, terminalID, plan, h)
	if gmErr != nil {
		svc.failTerminalStartup(terminalID, gm, gmErr, h)
		return
	}
	// Link the tab to its worktree unless a CloseTab already landed during
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

	// Post-spawn fetch: closed_at detects a CloseTab that landed
	// during the PTY spawn (must neither broadcast READY nor leave a
	// running PTY behind), and title absorbs the value the frontend may
	// have persisted between OpenTerminal returning and StartTerminal
	// registering in-memory metadata. Single narrow query — avoids
	// re-reading the screen BLOB the handler entry already fetched.
	postSpawn, postSpawnErr := svc.Queries.GetTerminalForReady(bgCtx(), terminalID)
	switch svc.TerminalStartup.interruptionOf(h, postSpawnErr == nil && postSpawn.ClosedAt.Valid, postSpawnErr == nil && postSpawn.WorkspaceArchived) {
	case interruptionClosed:
		if startErr == nil {
			svc.Terminals.RemoveTerminal(terminalID)
			svc.clearTerminalBellCoalesce(terminalID)
		}
		svc.finishStartupAfterClose(&svc.TerminalStartup.startupCore, h, terminalID, gm)
		return
	case interruptionArchived:
		if startErr == nil {
			svc.Terminals.StopTerminal(terminalID)
			svc.Terminals.WaitForExit(terminalID)
		}
		// succeed, not fail: see the matching branch in runAgentStartup.
		if startErr != nil {
			slog.Warn("terminal startup stopped by workspace archival", "terminal_id", terminalID, "error", startErr)
		}
		svc.TerminalStartup.succeed(terminalID, h)
		return
	case interruptionNone:
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
	if err := svc.TerminalStartup.withTerminalResizeEntry(ctx, h, func() error {
		if cols, rows, ok := svc.TerminalStartup.takePendingResize(h); ok {
			return svc.Terminals.Resize(terminalID, cols, rows)
		}
		return nil
	}); err != nil && !errors.Is(err, terminal.ErrTerminalInstanceChanged) && !errors.Is(err, errStartupEntryChanged) && !errors.Is(err, errStartupClaimsClosed) && !errors.Is(err, context.Canceled) {
		slog.Warn("apply pending resize after startup", "terminal_id", terminalID, "error", err)
	}

	// Spawn succeeded and no close-race; hand cleanup ownership to the
	// eventual CloseTab handler.
	ownsIPCToken = false
	svc.succeedTerminalStartup(terminalID, h)
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
	defer svc.TerminalStartup.finishEntry(h)

	// Swap the previous spawn's LEAPMUX_CONTROL_* token for a fresh one.
	// remintControlIPC owns the ORDER -- retire, then mint -- because both
	// tokens are keyed by terminalID and both listen on the same per-tab socket
	// path; see its doc for what minting first cost.
	//
	// The previous spawn's token is retired on every exit from here, including
	// the fatal-identity path. The old PTY has already exited by the time this
	// runs -- RestartTerminal refuses synchronously while IsRunning -- so there
	// is no live shell whose remote control needs preserving, and leaving the
	// old cleanup registered would strand a listening unix socket plus an
	// unrevoked per-user delegation bearer for a process that is gone.
	remoteEnvs, ownsIPCToken, ipcErr := svc.remintControlIPC(tabKindTerminal, terminalID, "restart", spawnInfo.UserID,
		func() ([]string, func(), error) {
			return svc.ControlIPC.TerminalSpawning(spawnInfo)
		})
	if ipcErr != nil {
		// See the open path: a spawn that cannot name its user fails the
		// restart rather than silently relaunching without remote control.
		svc.failTerminalStartup(terminalID, gitModeResult{}, ipcErr, h)
		return
	}
	opts.ExtraEnv = remoteEnvs
	defer func() {
		if ownsIPCToken {
			svc.terminalCleanups.retire(terminalID)
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

	// Detect a CloseTab that landed during the PTY spawn: if
	// closed_at is set we must neither broadcast READY nor leave a
	// freshly-spawned PTY behind. Re-fetching is best-effort — a
	// CloseTab whose DB write hasn't committed yet will be caught
	// by its own RemoveTerminal call instead. The title column from the
	// same row is unused on the restart path (titles don't change
	// across restart), so it's read-and-discarded.
	postSpawn, fetchErr := svc.Queries.GetTerminalForReady(bgCtx(), terminalID)
	switch svc.TerminalStartup.interruptionOf(h, fetchErr == nil && postSpawn.ClosedAt.Valid, fetchErr == nil && postSpawn.WorkspaceArchived) {
	case interruptionClosed:
		if startErr == nil {
			svc.Terminals.RemoveTerminal(terminalID)
			svc.clearTerminalBellCoalesce(terminalID)
		}
		svc.TerminalStartup.succeed(terminalID, h)
		return
	case interruptionArchived:
		if startErr == nil {
			svc.Terminals.StopTerminal(terminalID)
			svc.Terminals.WaitForExit(terminalID)
		}
		// succeed, not fail: see the matching branch in runAgentStartup.
		if startErr != nil {
			slog.Warn("terminal restart stopped by workspace archival", "terminal_id", terminalID, "error", startErr)
		}
		svc.TerminalStartup.succeed(terminalID, h)
		return
	case interruptionNone:
	}
	if startErr != nil {
		slog.Error("failed to restart terminal", "terminal_id", terminalID, "error", startErr)
		// No git-mode mutation in the restart path — pass a zero result so
		// failTerminalStartup skips the rollback branch.
		svc.failTerminalStartup(terminalID, gitModeResult{}, startErr, h)
		return
	}

	ownsIPCToken = false
	svc.succeedTerminalStartup(terminalID, h)
}

// succeedTerminalStartup is the shared tail of a successful start for
// runTerminalStartup and runTerminalRestart: clear the persisted startup_error,
// retire the registry entry, and broadcast the status last.
//
// The registry comes before the broadcast for the reason failStartup states:
// ListTerminals lets the registry override the READY of a live terminal, so a
// client that receives READY and then lists a registry that still says
// STARTING keeps STARTING, and no later event corrects it.
//
// A restart can claim the terminal between the two steps. The broadcast then
// states that restart's status, because it derives the status at its own step
// (see broadcastTerminalStatusAfterStartup).
func (svc *Service) succeedTerminalStartup(terminalID string, h *startupEntry) {
	svc.persistTerminalStartupError(terminalID, "")
	svc.TerminalStartup.succeed(terminalID, h)
	svc.broadcastTerminalStatusAfterStartup(terminalID)
}

// runTerminalPhase0 broadcasts the per-mode label and executes the
// git-mode mutation.
func (svc *Service) runTerminalPhase0(ctx context.Context, terminalID string, plan gitModePlan, h *startupEntry) (gitModeResult, error) {
	var result gitModeResult
	var err error
	if h.creation != nil {
		h.creation.announcePhase0(svc.terminalStartupCallbacks(terminalID, h))
		result, err = h.creation.runCreationPhase0(ctx)
	} else {
		result, err = svc.runStartupPhase0(ctx, plan, svc.terminalStartupCallbacks(terminalID, h))
	}
	result.bindCreatedWorktreeStartup(worktreeRollbackStartup{key: newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_TERMINAL, terminalID, ""), registry: &svc.TerminalStartup.startupCore, entry: h})
	return result, err
}

// failTerminalStartup is the common tail for every failure after the sync
// prologue: it rolls back any partial git-mode mutation, persists the
// error, marks the registry failed, and broadcasts STARTUP_FAILED. The
// shared `failStartup` states and enforces that order.
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
func buildTerminalStartingStatus(message string, gs *leapmuxv1.GitRepoStatus) *leapmuxv1.TerminalStatusChange {
	sc := &leapmuxv1.TerminalStatusChange{
		Status:         leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING,
		StartupMessage: message,
	}
	if gs != nil {
		sc.GitStatus = gs
	}
	return sc
}

// buildTerminalFailedStatus builds a STARTUP_FAILED TerminalStatusChange
// carrying the error message.
func buildTerminalFailedStatus(errMsg string) *leapmuxv1.TerminalStatusChange {
	return &leapmuxv1.TerminalStatusChange{
		Status:       leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED,
		StartupError: errMsg,
	}
}

// terminalGitStatuses runs git for the terminals in ids and returns the status
// of each terminal by its id. A terminal in a directory that is no repository
// has no entry in the result.
//
// A terminal gives its git directory through the Manager when its shell is
// registered there, and through its row otherwise. The Manager wins when both
// exist, because the shell started in the directory that its metadata states.
// ResolveGitDir states which of the start directory and the working directory
// counts. One call to BatchGetGitStatus runs git once for each distinct
// directory, so terminals that share a repository share one git process.
func (svc *Service) terminalGitStatuses(ctx context.Context, ids []string, rows []db.Terminal) map[string]*leapmuxv1.GitRepoStatus {
	dirByID := make(map[string]string, len(rows))
	for i := range rows {
		dirByID[rows[i].ID] = gitutil.ResolveGitDir(rows[i].ShellStartDir, rows[i].WorkingDir)
	}
	for _, id := range ids {
		if meta, ok := svc.Terminals.GetMeta(id); ok {
			dirByID[id] = gitutil.ResolveGitDir(meta.ShellStartDir, meta.WorkingDir)
		}
	}

	terminalIDs := make([]string, 0, len(dirByID))
	dirs := make([]string, 0, len(dirByID))
	for id, dir := range dirByID {
		terminalIDs = append(terminalIDs, id)
		dirs = append(dirs, dir)
	}
	statuses := gitutil.BatchGetGitStatus(ctx, dirs)

	byID := make(map[string]*leapmuxv1.GitRepoStatus, len(statuses))
	for i, gs := range statuses {
		if gs != nil {
			byID[terminalIDs[i]] = gs
		}
	}
	return byID
}

// deriveTerminalStatus computes (status, startupError, startupMessage)
// for a terminal. It is the ONE statement of the terminal status rule:
// ListTerminals, the WatchEvents replay and the status after a startup all read
// it, so they cannot report different states for one terminal. The priority
// order:
//  1. in-memory startup registry — STARTING / STARTUP_FAILED while a
//     startup is in flight or has just failed. The current phase
//     message is surfaced so a WatchEvents subscriber that arrived
//     after the initial STARTING broadcast still sees the right label.
//  2. READY while the shell runs. A running shell proves that the last
//     startup succeeded: every failure path leaves no new shell, and
//     RestartTerminal refuses a terminal whose shell runs. A persisted
//     startup_error beside a running shell is therefore stale, such as a
//     row read before the success cleared the column. deriveAgentStatus
//     puts a running agent before its persisted error for the same reason.
//     "Runs" is IsRunning, not HasTerminal: the manager keeps an exited
//     terminal until RestartTerminal or RemoveTerminal, so holding the
//     terminal does not mean its shell still runs.
//  3. persisted startup_error column — surfaces a prior failure across
//     worker restarts (the in-memory registry is wiped on restart) and after
//     the registry evicts a failure (failedEntryTTL). A failed restart keeps
//     the stopped previous shell in the manager, so this case covers a
//     terminal that the manager holds too.
//  4. EXITED: no startup owns the status, no shell runs, and no failure is
//     persisted.
//
// startupError is empty unless the status is STARTUP_FAILED, and
// startupMessage is empty unless it is STARTING. derivedTerminalStatusEvent and
// ListTerminals copy all three into the reply and rely on that.
func (svc *Service) deriveTerminalStatus(t *db.Terminal) (status leapmuxv1.TerminalStatus, startupError, startupMessage string) {
	if sup, errStr, msg, ok := svc.TerminalStartup.status(t.ID); ok {
		return sup, errStr, msg
	}
	if svc.Terminals.IsRunning(t.ID) {
		return leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY, "", ""
	}
	if t.StartupError != "" {
		return leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED, t.StartupError, ""
	}
	return leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED, "", ""
}

// derivedTerminalStatusEvent builds the status change that deriveTerminalStatus
// states for t. deriveTerminalStatus sets startupError only for STARTUP_FAILED
// and startupMessage only for STARTING, so one status change carries all three
// as they are, and a new status needs no case here.
func (svc *Service) derivedTerminalStatusEvent(t *db.Terminal) *leapmuxv1.TerminalEvent {
	status, startupError, startupMessage := svc.deriveTerminalStatus(t)
	return &leapmuxv1.TerminalEvent{
		TerminalId: t.ID,
		Event: &leapmuxv1.TerminalEvent_StatusChange{StatusChange: &leapmuxv1.TerminalStatusChange{
			Status:         status,
			StartupError:   startupError,
			StartupMessage: startupMessage,
		}},
	}
}

// effectiveTerminalTitle returns the title a terminal tab shows right now.
// The in-memory manager holds it while the terminal is live and the persisted
// row holds it after the shell exits and the manager drops the entry -- the
// same precedence ListTerminals applies when it builds a TerminalInfo, so the
// two RPCs cannot report different titles for the same terminal.
//
// RenameTerminal calls it on the path that stores nothing (the request's
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
func (svc *Service) broadcastTerminalStarting(terminalID, message string, gs *leapmuxv1.GitRepoStatus) {
	svc.Watchers.PublishTerminalStatus(terminalID, &leapmuxv1.TerminalEvent{
		TerminalId: terminalID,
		Event: &leapmuxv1.TerminalEvent_StatusChange{
			StatusChange: buildTerminalStartingStatus(message, gs),
		},
	})
}

// broadcastTerminalFailed fans out a STARTUP_FAILED TerminalStatusChange.
func (svc *Service) broadcastTerminalFailed(terminalID, errMsg string) {
	svc.Watchers.PublishTerminalStatus(terminalID, &leapmuxv1.TerminalEvent{
		TerminalId: terminalID,
		Event: &leapmuxv1.TerminalEvent_StatusChange{
			StatusChange: buildTerminalFailedStatus(errMsg),
		},
	})
}

// broadcastTerminalStatusAfterStartup fans out the status of a terminal whose
// startup succeeded and left the registry: READY while the shell runs, and EXITED
// when it already ended.
//
// A shell can exit between its spawn and this call. A watch session that
// registered after the exit handler published closed never receives closed, and
// its replay can read the STARTING of the registry. So the queue entry states the
// truth instead of nothing: this status is the only one that corrects that
// session, and the client's EXITED case takes the same path as closed.
//
// READY never lands behind closed. IsExited turns true before the exit handler
// runs, and the status is derived inside the queue's lock, so a closed event that
// is already in the queue implies a derive that sees the exit.
//
// The derive also reads the registry, because a restart can claim the terminal
// before this call. A bare EXITED would then land behind that restart's STARTING,
// and the client would drop the restart's READY and show a dead tab over a live
// shell.
//
// The row holds only the id because succeedTerminalStartup cleared the persisted
// error, the one other field that deriveTerminalStatus reads.
func (svc *Service) broadcastTerminalStatusAfterStartup(terminalID string) {
	row := db.Terminal{ID: terminalID}
	svc.Watchers.PublishDerivedTerminalStatus(terminalID, func() *leapmuxv1.TerminalEvent {
		return svc.derivedTerminalStatusEvent(&row)
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
		// TerminalData is classContent, so BroadcastTerminalEvent restricts itself:
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
		title := validate.CleanName(sig.Title)
		body := validate.StripUnreadable(sig.Body, notificationBodyByteLimit)
		// A notification that carries no text says only what the BELL says, and
		// the bell has the coalesce window this path does not -- a `cat` of a
		// hostile file can emit maxNotificationsPerChunk of these per read.
		//
		// Both fields are tested AFTER the clean, rather than testing whether the
		// clean emptied them: `dispatchOSC9` already emits an empty title for a
		// bare `ESC ] 9 ; ST`, with no cleaning involved, so a "did the clean
		// empty it?" guard would refuse the invisible-character form and admit
		// the empty one. This drops the tab badge for such a signal too, which is
		// the cost: an empty notification is not something the reader can act on.
		if title == "" && body == "" {
			return
		}
		svc.Watchers.BroadcastTerminalEvent(terminalID, &leapmuxv1.TerminalEvent{
			TerminalId: terminalID,
			Event: &leapmuxv1.TerminalEvent_Notification{
				Notification: &leapmuxv1.TerminalNotification{
					Title: title,
					Body:  body,
				},
			},
		})
	case terminal.SignalTitle:
		// Broadcast the live OSC title only — do NOT write Meta.Title. That
		// field is owned by the user-rename path (RenameTerminal) and by
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
		// CleanName runs here for the reason RenameTerminal runs it above,
		// and this is the writer that needs it most: the bytes came from
		// whatever process holds the PTY, which includes the REMOTE host of an
		// `ssh` session and any command an agent ran. The browser renders the
		// result as the tab label and the tab tooltip, so an OSC title of
		// "‮txt.exe" reordered what the tab strip showed, and an OSC
		// title could reach oscBufCap (2048 bytes), 16 times the limit every
		// other writer of a tab title obeys.
		//
		// A title that cleans to nothing is a no-op, not a clear: the same
		// answer RenameTerminal gives, and the answer the browser's own
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
		// A successful shutdown notice stays authoritative only when the
		// hang-up request preceded the observed exit. Save the latest retained
		// screen without another notice or a TerminalClosed event. A natural
		// exit observed before that request keeps its own exit code.
		if notices := svc.shutdownNotifiedTerminals.Load(); notices != nil {
			if _, notified := (*notices)[tid]; notified && svc.Terminals.HangUpRequested(tid) {
				if snapshot, ok := svc.captureTerminalExit(tid); ok {
					svc.saveTerminalExit(tid, exitCodeUnknown, snapshot)
				}
				svc.clearTerminalBellCoalesce(tid)
				return
			}
		}

		svc.persistTerminalOnExit(tid, exitCode)
		svc.clearTerminalBellCoalesce(tid)
		svc.Watchers.PublishTerminalStatus(tid, &leapmuxv1.TerminalEvent{
			TerminalId: tid,
			Event: &leapmuxv1.TerminalEvent_Closed{
				Closed: &leapmuxv1.TerminalClosed{
					ExitCode: int32(exitCode),
				},
			},
		})
		svc.closeQuakeTerminalOnShellExit(tid)
	}
}

// closeQuakeTerminalOnShellExit ends a QUAKE terminal whose shell exited.
//
// A terminal TAB survives its shell: the row stays open, the exit notice sits in
// the screen, and Enter respawns it. A quake terminal has no such contract --
// there is no pane to leave behind, and the panel retracts -- so its exit IS
// its end.
//
// Without this the row would stay open with a dead PTY, and the unique index
// over live quake rows would then refuse a new one: the next toggle would adopt
// the corpse instead of starting a shell. It also stops the reconciler having to
// reap a row nothing will ever use.
//
// On its own goroutine, because the caller is the terminal's own reader: the
// shared teardown calls RemoveTerminal, which stops the terminal this handler is
// running inside.
//
// The read is NARROW for the reason GetTerminalExitCode gives: GetTerminal
// answers the same question and reads the 100KB screen blob, and this runs on
// every terminal exit rather than on a quake terminal's alone.
func (svc *Service) closeQuakeTerminalOnShellExit(terminalID string) {
	row, err := svc.Queries.GetTerminalQuakeAndClosed(bgCtx(), terminalID)
	if err != nil {
		// A failure here is NOT "this is not a quake terminal". It leaves the
		// row open with a dead PTY, which the unique index then reads as the
		// directory's live shell and which no later pass reclaims while a tab
		// still works there -- so it must be visible rather than silent. The
		// worker boot sweep is the backstop that eventually closes it.
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Error("failed to read terminal is_quake on shell exit", "terminal_id", terminalID, "error", err)
		}
		return
	}
	if !row.IsQuake || row.ClosedAt.Valid {
		return
	}
	// Counted as service cleanup, so Shutdown joins the close before the
	// database closes. A shutdown that already began admits nothing: the row
	// stays open here,
	// and the boot sweep (CloseOrphanedQuakeTerminals) closes it.
	if !svc.beginCleanup() {
		return
	}
	go func() {
		defer svc.Cleanup.Done()
		svc.CloseTabForReconcile(leapmuxv1.TabType_TAB_TYPE_TERMINAL, "", terminalID)
	}()
}

// quakeShellIsLive answers whether a QUAKE row still has a shell that this
// process hosts.
//
// A quake row is valid only while its PTY lives here, and three states break
// that link with the row left open. The worker restarted and lost every PTY.
// The shell exited and the close is still on its goroutine. The spawn failed,
// which persists a startup error and never stamps closed_at. A row in any of
// them is dead, and handing one back gives the user a panel that paints nothing
// and swallows every keystroke: a quake terminal refuses the Enter that restarts
// a terminal TAB, so there is no way out but to close every tab in the
// directory.
func (svc *Service) quakeShellIsLive(terminalID string) bool {
	return svc.shellIsLive(terminalID, func() bool { return svc.Terminals.IsRunning(terminalID) })
}

// shellIsLive reports whether the shell of terminalID runs or is about to run:
// a startup that is in flight and has not failed says that it is about to, and
// shellRuns says that it runs. It calls shellRuns at most once.
//
// The order of the two reads is fixed. A startup ends in this order: the Manager
// registers the PTY, then the registry drops the entry. The answer reads the
// registry first and the shell second, so it finds the entry, or the shell, or
// both. Read in the other order, it can find neither, and the caller then closes
// the row of a shell that runs. See agentLiveness for the same rule.
func (svc *Service) shellIsLive(terminalID string, shellRuns func() bool) bool {
	status, _, _, inFlight := svc.TerminalStartup.status(terminalID)
	if inFlight && status != leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED {
		return true
	}
	return shellRuns()
}

// adoptExistingQuakeTerminal answers the caller with the directory's existing
// quake terminal when that terminal still has a live shell, and reports whether
// it answered.
//
// Shared by OpenTerminal's pre-mint check and its lost-race retry, so the two
// cannot answer with different fields.
//
// A row whose shell is gone is CLOSED here rather than handed back. The unique
// partial index counts it as the directory's one live quake terminal, so
// leaving it in place refuses every replacement for as long as a tab works
// there.
func (svc *Service) adoptExistingQuakeTerminal(sender channel.ResponseWriter, isQuake bool, workingDir string) bool {
	if !isQuake {
		return false
	}
	existing, err := svc.Queries.GetOpenQuakeTerminalByWorkingDir(bgCtx(), workingDir)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Error("failed to look up the existing quake terminal", "working_dir", workingDir, "error", err)
		}
		return false
	}
	if !svc.quakeShellIsLive(existing.ID) {
		svc.CloseTabForReconcile(leapmuxv1.TabType_TAB_TYPE_TERMINAL, "", existing.ID)
		return false
	}
	sendProtoResponse(sender, &leapmuxv1.OpenTerminalResponse{
		TerminalId: existing.ID,
		Title:      existing.Title,
	})
	return true
}

// CloseOrphanedQuakeTerminals closes every open QUAKE row whose shell this
// process does not host. The worker runs it once at boot.
//
// A quake terminal is valid only while this process hosts its PTY, and a
// restart breaks that link with the row still open. No other pass reclaims one:
// the orphan reconciler measures a quake terminal by whether any open tab still
// works in its DIRECTORY, so one live tab there keeps the dead row open for as
// long as it exists. The next open would then adopt a terminal with no PTY.
func (svc *Service) CloseOrphanedQuakeTerminals(ctx context.Context) {
	ids, err := svc.Queries.ListOpenQuakeTerminalIDs(ctx)
	if err != nil {
		slog.Warn("failed to list quake terminals at startup", "error", err)
		return
	}
	for _, terminalID := range ids {
		if svc.quakeShellIsLive(terminalID) {
			continue
		}
		svc.CloseTabForReconcile(leapmuxv1.TabType_TAB_TYPE_TERMINAL, "", terminalID)
		slog.Info("closed a quake terminal that did not survive the restart", "terminal_id", terminalID)
	}
}

// clearTerminalBellCoalesce drops the per-terminal bell timer so closed
// terminals cannot leak lastBellAt entries for the life of the worker.
func (svc *Service) clearTerminalBellCoalesce(terminalID string) {
	svc.terminalBellMu.Lock()
	delete(svc.lastBellAt, terminalID)
	svc.terminalBellMu.Unlock()
}
