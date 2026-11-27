package cmd

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

// The `terminal` group mirrors the `agent` group: a target resolves to the
// terminal's (worker, tab) pair, and each verb calls the Worker over the
// E2EE channel or the tab's socket. Inside a quake panel, "self" is the
// quake terminal, which LEAPMUX_CONTROL_TERMINAL_ID gives.
func newTerminalCommand() *cobra.Command {
	return cli.SetAliases(cli.Group("terminal", "Open, type into, read and resize terminals",
		cli.NewLeaf(cli.Leaf{
			Use:     "list",
			Aliases: []string{"ls"},
			Short:   "List your terminals",
			Examples: []cli.Example{
				{Command: "leapmux terminal list"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("workspace", "", `list only the terminals of this workspace (an id, a prefix, "title:<title>" or "self")`)
				fs.String("worker", "", `list only the terminals hosted by this worker (an id, a prefix, "name:<name>" or "self")`)
				fs.String("state", "", "list only the terminals in this status")
				cli.SetEnum(fs, "state", enumTokens(leapmuxv1.TerminalStatus(0))...)
			},
			Run: runTerminalList,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:   "get [terminal]",
			Short: "Show a terminal's status, size and directory (default: this terminal)",
			Examples: []cli.Example{
				{Command: "leapmux terminal get"},
				{Command: "leapmux terminal get 4cbb3f34"},
			},
			Flags:    addTargetWorkspaceFlag,
			Complete: completeTargets,
			Run:      runTerminalGet,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "open",
			Mutates: true,
			Short:   "Open a new terminal in a new tab",
			Long:    terminalOpenLong,
			Examples: []cli.Example{
				{Comment: "Open a terminal beside this tab and keep its id.", Command: "T=$(leapmux terminal open -q)"},
				{Comment: "Open a terminal in a new git worktree.", Command: "leapmux terminal open --worktree try-fix --dir ~/src/app"},
				{Comment: "Run the tests in a new terminal and wait until its shell exits.", Command: `leapmux terminal open --command 'make test; exit' --wait --exit`},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("command", "", `evaluate this command in the original shell after startup ("-" reads stdin)`)
				fs.String("shell", "", "shell to start (default: the worker's default shell)")
				fs.String("start-dir", "", "directory the shell starts in (default: --dir)")
				fs.String("title", "", "tab title (default: the worker picks one)")
				for _, name := range []string{"command", "shell", "start-dir", "title"} {
					cli.SetFlagGroup(fs, name, "Terminal")
				}
				fs.Bool("wait", false, "wait after opening until --exit, --text, --regex, or --change holds")
				addTerminalWaitConditionFlags(fs, false)
				for _, name := range []string{"wait", "exit", "text", "regex", "change", "scrollback", "timeout"} {
					cli.SetFlagGroup(fs, name, "Wait")
				}
				addWorkingDirFlag(fs, "working directory of the terminal")
				cli.SetFlagGroup(fs, "dir", "Git & Directory")
				addGitSetupFlags(fs)
				addPlacementFlags(fs)
			},
			FlagComplete: mergeFlagCompletions(
				map[string]cobra.CompletionFunc{"shell": completeShellFlag},
				gitSetupFlagCompletions(),
				tabPositionFlagCompletions(),
			),
			Run: runTerminalOpen,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "send <terminal> [text]",
			Short:   "Type text into a terminal and press keys, with no Enter added",
			Mutates: true,
			Long:    terminalSendLong(),
			Examples: []cli.Example{
				{Command: `leapmux terminal send 4cbb3f34 $'make test\n'`},
				{Comment: "Stop the program in the foreground.", Command: "leapmux terminal send 4cbb3f34 --key ctrl-c"},
				{Comment: "Choose the second entry of a menu.", Command: "leapmux terminal send 4cbb3f34 --key down --key enter"},
				{Comment: "Paste a file into the program, as the browser pastes it.", Command: "leapmux terminal send 4cbb3f34 --paste --file snippet.py"},
				{Comment: "Type the bytes of a file, such as escape sequences.", Command: "leapmux terminal send 4cbb3f34 --file keys.bin"},
			},
			Flags: func(fs *pflag.FlagSet) {
				addTerminalSendFlags(fs)
				addTargetWorkspaceFlag(fs)
			},
			Complete: completeTargets,
			Run:      runTerminalSend,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "run <terminal> [command]",
			Short:   "Run a command in a terminal: paste the command line and press Enter",
			Mutates: true,
			Long:    terminalRunLong,
			Args: map[string]string{
				"command": `The command line, or "-" to read it from standard input.`,
			},
			Examples: []cli.Example{
				{Command: "leapmux terminal run 4cbb3f34 'make test'"},
				{Comment: "Run the command line that stdin holds.", Command: "echo 'go test ./...' | leapmux terminal run 4cbb3f34 -"},
				{Comment: "Run a command and wait for a sentinel line.", Command: "leapmux terminal run 4cbb3f34 'make test; echo DONE' --wait --regex '^DONE$' --timeout 10m"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("file", "", `read the command line from this file ("-" reads stdin)`)
				addRequestIDFlag(fs)
				for _, name := range []string{"file", requestIDFlag} {
					cli.SetFlagGroup(fs, name, "Command")
				}
				fs.Bool("wait", false, "wait after running until --exit, --text, --regex, or --change holds")
				addTerminalWaitConditionFlags(fs, true)
				for _, name := range []string{"wait", "exit", "text", "regex", "change", "after-offset", "scrollback", "timeout"} {
					cli.SetFlagGroup(fs, name, "Wait")
				}
				addTargetWorkspaceFlag(fs)
				cli.SetFlagGroup(fs, "workspace", "Scope")
			},
			Complete: completeTargets,
			Run:      runTerminalRun,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "read [terminal]",
			Aliases: []string{"cat"},
			Short:   "Print what a terminal shows, as text (default: this terminal)",
			Long:    terminalReadLong,
			Examples: []cli.Example{
				{Command: "leapmux terminal read 4cbb3f34"},
				{Comment: "Print the last 200 lines, the scrollback included.", Command: "leapmux terminal read 4cbb3f34 --scrollback --lines 200"},
				{Comment: "Page through the retained bytes with their colors.", Command: "leapmux terminal read 4cbb3f34 --raw | less -R"},
				{Comment: "Follow live terminal output until Ctrl-C or shell exit.", Command: "leapmux terminal read 4cbb3f34 --follow"},
			},
			Flags: func(fs *pflag.FlagSet) {
				addTerminalReadFlags(fs)
				addTargetWorkspaceFlag(fs)
			},
			Complete: completeTargets,
			Run:      runTerminalRead,
		}),
		newTerminalAttachLeaf(),
		newTerminalPSLeaf(),
		newTerminalWaitLeaf(),
		cli.NewLeaf(cli.Leaf{
			Use:     "resize <terminal> <cols> <rows>",
			Short:   "Resize a terminal",
			Mutates: true,
			Examples: []cli.Example{
				{Command: "leapmux terminal resize 4cbb3f34 120 40"},
			},
			Flags:    addTargetWorkspaceFlag,
			Complete: completeTargets,
			Run:      runTerminalResize,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "restart <terminal>",
			Short:   "Restart a terminal's shell",
			Mutates: true,
			Examples: []cli.Example{
				{Command: "leapmux terminal restart 4cbb3f34"},
				{Command: "leapmux terminal restart 4cbb3f34 --replace --command 'npm run dev'"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.Bool("replace", false, "stop the running shell before starting a new one in the same tab")
				fs.String("command", "", "run this command in the new shell after its startup files, or read it from stdin with -")
				fs.Bool(allowSelfFlag, false, "replace this tab's shell even though it ends this command's own process")
				addTargetWorkspaceFlag(fs)
			},
			Complete: completeTargets,
			Run:      runTerminalRestart,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:   "shells",
			Short: "List the shells that a worker offers",
			Examples: []cli.Example{
				{Command: "leapmux terminal shells"},
			},
			Flags: addWorkerFlag,
			Run:   runTerminalShells,
		}),
		cli.Group("quake", "Show, hide, inspect, read, and drive the quake terminal of a directory",
			newQuakeLeaf("open", "Show the quake terminal", leapmuxv1.QuakePanelAction_QUAKE_PANEL_ACTION_OPEN),
			newQuakeLeaf("close", "Hide the quake terminal", leapmuxv1.QuakePanelAction_QUAKE_PANEL_ACTION_CLOSE),
			newQuakeLeaf("toggle", "Show the quake terminal if it is hidden, and hide it otherwise", leapmuxv1.QuakePanelAction_QUAKE_PANEL_ACTION_TOGGLE),
			newQuakeGetLeaf(),
			newQuakeReadLeaf(),
			newQuakeRunLeaf(),
			newQuakeSendLeaf(),
		),
		newTabCloseLeafFor(resolve.KindTerminal),
		newTabRenameLeafFor(resolve.KindTerminal),
		newTabRevealLeafFor(resolve.KindTerminal),
	), "term")
}

// terminalOpenLong is the help of `terminal open`.
const terminalOpenLong = `Open a new terminal in a new tab. Without --workspace and --tile, the new
tab opens beside the tab that runs leapmux.

--command runs a command in the original interactive shell after its startup
files run. On Unix, use Bash, zsh, fish, sh, Dash, Elvish, Nushell, Xonsh,
or Yash. PowerShell also works.
If Elvish has a startup file, its directory must allow the Worker to create
a temporary link. The Worker reports a startup failure if the shell cannot
acknowledge the handoff.
A restart of the terminal does not run it again. If the Worker stops after
claiming the command, recovery reports an unverified outcome without running
it again. The command is the value of --command, or "-" to read it from stdin.
Read its output with "terminal read", or wait for it with "terminal wait".`

// terminalListRow is one terminal in `terminal list`.
type terminalListRow struct {
	ID          string `json:"id" table:"ID,short"`
	Title       string `json:"title,omitempty" table:"TITLE"`
	Status      string `json:"status,omitempty" table:"STATUS,state"`
	Size        string `json:"size,omitempty" table:"SIZE"`
	WorkingDir  string `json:"working_dir,omitempty" table:"DIR"`
	Worker      string `json:"worker,omitempty" table:"WORKER"`
	WorkspaceID string `json:"workspace_id" table:"WORKSPACE,short"`
	ExitCode    *int32 `json:"exit_code,omitempty" table:"EXIT"`
	Cols        uint32 `json:"cols,omitempty"`
	Rows        uint32 `json:"rows,omitempty"`
	WorkerID    string `json:"worker_id"`
	TileID      string `json:"tile_id"`
	Position    string `json:"position"`
}

type terminalList struct {
	Terminals []terminalListRow `json:"terminals"`
}

// runTerminalList lists the terminal tabs with their titles, status, size,
// directory, and exit code on a single Noise_NK channel per Worker.
func runTerminalList(inv *cli.Invocation) (any, error) {
	stateFilter := strings.TrimSpace(stringFlag(inv, "state"))
	if stateFilter != "" && !slices.Contains(enumTokens(leapmuxv1.TerminalStatus(0)), stateFilter) {
		return nil, cli.Usagef("--state must be one of %s; got %q", strings.Join(enumTokens(leapmuxv1.TerminalStatus(0)), ", "), stateFilter)
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	tabs, err := s.listWorkspaceTabs(leapmuxv1.TabType_TAB_TYPE_TERMINAL)
	if err != nil {
		return nil, err
	}
	if inv.Changed("worker") {
		w, err := s.resolveOne(resolve.KindWorker, stringFlag(inv, "worker"))
		if err != nil {
			return nil, err
		}
		tabs = filterTabsByWorker(tabs, w.ID)
	}
	details, names := fetchTabDetailsAndWorkers(s.ctx, s.c, tabs, true)
	rows := make([]terminalListRow, 0, len(tabs))
	for _, t := range tabs {
		d := details[t.GetTab().GetTabId()]
		if stateFilter != "" && !strings.EqualFold(d.State, stateFilter) {
			continue
		}
		row := terminalListRow{
			ID:          t.GetTab().GetTabId(),
			Title:       d.Title,
			Status:      d.State,
			Worker:      names[t.GetWorkerId()],
			WorkerID:    t.GetWorkerId(),
			WorkspaceID: t.GetWorkspaceId(),
			TileID:      t.GetTileId(),
			Position:    t.GetPosition(),
		}
		if info := d.Terminal; info != nil {
			row.Cols = info.GetCols()
			row.Rows = info.GetRows()
			if row.Cols > 0 && row.Rows > 0 {
				row.Size = fmt.Sprintf("%dx%d", row.Cols, row.Rows)
			}
			row.WorkingDir = info.GetWorkingDir()
			row.ExitCode = exitCodeOf(info)
		}
		rows = append(rows, row)
	}
	return terminalList{Terminals: rows}, nil
}

// terminalRow is a terminal in a result.
type terminalRow struct {
	ID              string     `json:"id"`
	Title           string     `json:"title"`
	Status          string     `json:"status"`
	Quake           bool       `json:"quake,omitempty"`
	Cols            uint32     `json:"cols"`
	Rows            uint32     `json:"rows"`
	WorkingDir      string     `json:"working_dir,omitempty"`
	ShellStartDir   string     `json:"shell_start_dir,omitempty"`
	ScreenEndOffset int64      `json:"screen_end_offset"`
	StartupError    string     `json:"startup_error,omitempty"`
	StartupMessage  string     `json:"startup_message,omitempty"`
	ExitCode        *int32     `json:"exit_code,omitempty"`
	Git             *gitStatus `json:"git,omitempty"`
}

func terminalRowOf(t *leapmuxv1.TerminalInfo) terminalRow {
	return terminalRow{
		ID:              t.GetTerminalId(),
		Title:           t.GetTitle(),
		Status:          enumToken(t.GetStatus()),
		Quake:           t.GetQuake(),
		Cols:            t.GetCols(),
		Rows:            t.GetRows(),
		WorkingDir:      t.GetWorkingDir(),
		ShellStartDir:   t.GetShellStartDir(),
		ScreenEndOffset: t.GetScreenEndOffset(),
		StartupError:    t.GetStartupError(),
		StartupMessage:  t.GetStartupMessage(),
		ExitCode:        exitCodeOf(t),
		Git:             gitStatusOf(t.GetGitStatus()),
	}
}

// exitCodeOf returns the exit code that a record of a terminal states, or nil
// when it states none: the shell has not ended, or the Worker holds no record
// of the exit. The result is a copy, so it shares no memory with the record.
func exitCodeOf(t *leapmuxv1.TerminalInfo) *int32 {
	if t == nil || t.ExitCode == nil {
		return nil
	}
	code := t.GetExitCode()
	return &code
}

// terminalInfoOn reads the Worker's record of terminalID over an open channel.
// It reuses ListTerminals with a single tab id rather than a GetTerminal RPC,
// since the Worker already filters by id.
func terminalInfoOn(ctx context.Context, w workerCall, terminalID string) (*leapmuxv1.TerminalInfo, error) {
	var resp leapmuxv1.ListTerminalsResponse
	if err := w.Call(ctx, leapmuxv1connect.WorkerTerminalServiceListTerminalsProcedure, &leapmuxv1.ListTerminalsRequest{TabIds: []string{terminalID}}, &resp); err != nil {
		return nil, err
	}
	for _, info := range resp.GetTerminals() {
		if info.GetTerminalId() == terminalID {
			return info, nil
		}
	}
	return nil, cli.Newf(cli.CodeNotFound, "the worker has no terminal %s", terminalID)
}

// terminalInfo reads the Worker's record of the terminal t.
func (s *session) terminalInfo(t tabTarget) (*leapmuxv1.TerminalInfo, error) {
	if err := maybePreflightWorker(s.ctx, s.c, t.WorkerID); err != nil {
		return nil, err
	}
	var info *leapmuxv1.TerminalInfo
	if err := withWorkerChannel(s.ctx, s.c, t.WorkerID, func(w workerCall) error {
		var err error
		info, err = terminalInfoOn(s.ctx, w, t.ID)
		return err
	}); err != nil {
		return nil, err
	}
	return info, nil
}

func runTerminalGet(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	got, err := s.resolveTab(resolve.KindTerminal, inv.Arg("terminal"), resolve.Need{WorkerID: true})
	if err != nil {
		return nil, err
	}
	info, err := s.terminalInfo(got)
	if err != nil {
		return nil, err
	}
	return terminalRowOf(info), nil
}

// terminalSizeResult is the result of `terminal resize` and `terminal
// restart`.
type terminalSizeResult struct {
	ID   string `json:"id"`
	Cols uint32 `json:"cols,omitempty"`
	Rows uint32 `json:"rows,omitempty"`
}

// maxTerminalDimension caps a size that a caller can ask for. A PTY wider or
// taller than this is a typo, not a terminal.
const maxTerminalDimension = 10000

func parseDimension(name, raw string) (uint32, error) {
	n, err := strconv.ParseUint(raw, 10, 32)
	if err != nil || n == 0 || n > maxTerminalDimension {
		return 0, cli.Usagef("<%s> must be a whole number from 1 to %d; got %q", name, maxTerminalDimension, raw)
	}
	return uint32(n), nil
}

func runTerminalResize(inv *cli.Invocation) (any, error) {
	cols, err := parseDimension("cols", inv.Arg("cols"))
	if err != nil {
		return nil, err
	}
	rows, err := parseDimension("rows", inv.Arg("rows"))
	if err != nil {
		return nil, err
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	got, err := s.resolveTab(resolve.KindTerminal, inv.Arg("terminal"), resolve.Need{WorkerID: true})
	if err != nil {
		return nil, err
	}
	if err := maybePreflightWorker(s.ctx, s.c, got.WorkerID); err != nil {
		return nil, err
	}
	if err := callInnerRPC(s.ctx, s.c, got.WorkerID, leapmuxv1connect.WorkerTerminalServiceResizeTerminalProcedure, &leapmuxv1.ResizeTerminalRequest{TerminalId: got.ID, Cols: cols, Rows: rows}, nil); err != nil {
		return nil, err
	}
	return terminalSizeResult{ID: got.ID, Cols: cols, Rows: rows}, nil
}

// runTerminalRestart starts the terminal's shell again at its current size.
func runTerminalRestart(inv *cli.Invocation) (any, error) {
	command, err := initialCommandOf(inv)
	if err != nil {
		return nil, err
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	got, err := s.resolveTab(resolve.KindTerminal, inv.Arg("terminal"), resolve.Need{WorkerID: true})
	if err != nil {
		return nil, err
	}
	replace := boolFlag(inv, "replace")
	isSelfTerminal := got.Self || got.ID == s.listing().SelfTabID(resolve.KindTerminal) ||
		(strings.TrimSpace(os.Getenv(controlenv.TerminalIDVariable)) == "" && got.ID == callingTabID())
	if replace && isSelfTerminal && !boolFlag(inv, allowSelfFlag) {
		return nil, errSelfTargetRefused("refusing to replace the shell of the calling tab " + got.ID)
	}
	if err := maybePreflightWorker(s.ctx, s.c, got.WorkerID); err != nil {
		return nil, err
	}
	var req *leapmuxv1.RestartTerminalRequest
	if err := withWorkerChannel(s.ctx, s.c, got.WorkerID, func(w workerCall) error {
		info, err := terminalInfoOn(s.ctx, w, got.ID)
		if err != nil {
			return err
		}
		req = &leapmuxv1.RestartTerminalRequest{
			TerminalId: got.ID, Cols: info.GetCols(), Rows: info.GetRows(), InitialCommand: command,
		}
		if replace {
			req.ReplaceShellInstanceId = info.GetShellInstanceId()
			if req.GetReplaceShellInstanceId() == "" {
				return cli.Newf(cli.CodeConflict, "terminal %s has no running shell to replace", got.ID)
			}
		}
		return w.Call(s.ctx, leapmuxv1connect.WorkerTerminalServiceRestartTerminalProcedure, req, nil)
	}); err != nil {
		return nil, err
	}
	return terminalSizeResult{ID: got.ID, Cols: req.GetCols(), Rows: req.GetRows()}, nil
}

// shellList is the result of `terminal shells`.
type shellList struct {
	Shells       []string `json:"shells"`
	DefaultShell string   `json:"default_shell"`
}

func (l shellList) QuietLines() []string { return l.Shells }

// runTerminalShells lists the shells installed on a worker, with the
// worker's $SHELL default, so a caller can render a picker.
func runTerminalShells(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	workerID, err := s.workerScope()
	if err != nil {
		return nil, err
	}
	if err := maybePreflightWorker(s.ctx, s.c, workerID); err != nil {
		return nil, err
	}
	var resp leapmuxv1.ListAvailableShellsResponse
	if err := callInnerRPC(s.ctx, s.c, workerID, leapmuxv1connect.WorkerTerminalServiceListAvailableShellsProcedure, &leapmuxv1.ListAvailableShellsRequest{}, &resp); err != nil {
		return nil, err
	}
	shells := resp.GetShells()
	if shells == nil {
		shells = []string{}
	}
	return shellList{Shells: shells, DefaultShell: resp.GetDefaultShell()}, nil
}

// newQuakeLeaf builds one verb of `terminal quake`, which shows, hides or
// flips the quake panel of one WORKING DIRECTORY on every frontend the
// caller has open.
//
// They are the one place the Control CLI reaches into the UI, and they do it
// without introducing shared UI state: SetQuakePanel stores nothing and the
// worker relays the request as a transient event. A frontend that does not run
// simply never hears it, which is the correct outcome for a keystroke.
//
// A quake terminal is addressed by (worker, working directory), so this needs
// no tab at all. --dir defaults to $LEAPMUX_CONTROL_WORKING_DIR, which every
// spawn exports, so the bare command works from inside the panel it toggles,
// from an agent's own shell, and from a terminal tab alike -- and in every one
// of those the ambient directory is the panel the user means.
func newQuakeLeaf(verb, short string, action leapmuxv1.QuakePanelAction) *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:     verb,
		Mutates: true,
		Short:   short,
		Examples: []cli.Example{
			{Command: "leapmux terminal quake " + verb},
			{Command: "leapmux terminal quake " + verb + " --dir ~/src/app --worker name:build-box"},
		},
		Flags: func(fs *pflag.FlagSet) {
			addWorkingDirFlag(fs, "the directory whose quake terminal to act on")
			addWorkerFlag(fs)
		},
		Run: func(inv *cli.Invocation) (any, error) {
			dir, err := dirOf(inv)
			if err != nil {
				return nil, err
			}
			s, err := openSession(inv)
			if err != nil {
				return nil, err
			}
			defer s.Close()
			workerID, err := s.workerScope()
			if err != nil {
				return nil, err
			}
			if err := maybePreflightWorker(s.ctx, s.c, workerID); err != nil {
				return nil, err
			}
			req := &leapmuxv1.SetQuakePanelRequest{WorkingDir: dir, Action: action}
			if err := callInnerRPC(s.ctx, s.c, workerID, leapmuxv1connect.WorkerPrivateServiceSetQuakePanelProcedure, req, nil); err != nil {
				return nil, err
			}
			return quakeResult{WorkingDir: dir, Action: enumToken(action)}, nil
		},
	})
}

// quakeResult is the result of a `terminal quake` verb.
type quakeResult struct {
	WorkingDir string `json:"working_dir"`
	Action     string `json:"action"`
}

func (r quakeResult) QuietLines() []string { return []string{r.WorkingDir} }
