package cmd

import (
	"context"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
)

func newQuakeGetLeaf() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:   "get",
		Short: "Show the status and details of the quake terminal for a directory",
		Examples: []cli.Example{
			{Command: "leapmux terminal quake get"},
			{Command: "leapmux terminal quake get --dir ~/src/app --worker name:build-box"},
		},
		Flags: func(fs *pflag.FlagSet) {
			addWorkingDirFlag(fs, "the directory whose quake terminal to inspect")
			addWorkerFlag(fs)
		},
		Run: runQuakeGet,
	})
}

func newQuakeReadLeaf() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:     "read",
		Aliases: []string{"cat"},
		Short:   "Print what the quake terminal of a directory shows",
		Examples: []cli.Example{
			{Command: "leapmux terminal quake read"},
			{Command: "leapmux terminal quake read --dir ~/src/app --lines 50"},
		},
		Flags: func(fs *pflag.FlagSet) {
			addTerminalReadFlags(fs)
			addWorkingDirFlag(fs, "the directory whose quake terminal to read")
			addWorkerFlag(fs)
		},
		Run: runQuakeRead,
	})
}

func newQuakeRunLeaf() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:     "run [command]",
		Short:   "Run a command in the quake terminal of a directory (starts the quake shell if needed)",
		Mutates: true,
		Args: map[string]string{
			"command": `The command line, or "-" to read it from standard input.`,
		},
		Examples: []cli.Example{
			{Command: "leapmux terminal quake run 'go test ./...'"},
			{Command: "leapmux terminal quake run 'make build' --dir ~/src/app --worker name:build-box"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.String("file", "", `read the command line from this file ("-" reads stdin)`)
			addWorkingDirFlag(fs, "the directory whose quake terminal to run in")
			addWorkerFlag(fs)
		},
		Run: runQuakeRun,
	})
}

func newQuakeSendLeaf() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:     "send [text]",
		Short:   "Type text or press keys in the quake terminal of a directory",
		Mutates: true,
		Examples: []cli.Example{
			{Command: "leapmux terminal quake send --key ctrl-c"},
			{Command: `leapmux terminal quake send "ls -la" --key enter`},
		},
		Flags: func(fs *pflag.FlagSet) {
			addTerminalSendFlags(fs)
			addWorkingDirFlag(fs, "the directory whose quake terminal to type into")
			addWorkerFlag(fs)
		},
		Run: runQuakeSend,
	})
}

// lookupQuakeTerminalOn queries the Worker over an open channel for the quake
// terminal of dir. It returns nil without an error when none is open.
func lookupQuakeTerminalOn(ctx context.Context, w workerCall, dir string) (*leapmuxv1.TerminalInfo, error) {
	var resp leapmuxv1.ListTerminalsResponse
	if err := w.Call(ctx, leapmuxv1connect.WorkerTerminalServiceListTerminalsProcedure, &leapmuxv1.ListTerminalsRequest{
		QuakeWorkingDirs: []string{dir},
	}, &resp); err != nil {
		return nil, err
	}
	for _, info := range resp.GetTerminals() {
		if info.GetQuake() {
			return info, nil
		}
	}
	return nil, nil
}

// resolveQuakeTerminal resolves the Worker and working directory, then looks up
// the existing quake terminal for that directory without creating one.
func (s *session) resolveQuakeTerminal() (string, *leapmuxv1.TerminalInfo, error) {
	workerID, err := s.workerScope()
	if err != nil {
		return "", nil, err
	}
	dir, err := s.resolveGitDir(workerID)
	if err != nil {
		return "", nil, err
	}
	if err := maybePreflightWorker(s.ctx, s.c, workerID); err != nil {
		return "", nil, err
	}
	var info *leapmuxv1.TerminalInfo
	if err := withWorkerChannel(s.ctx, s.c, workerID, func(w workerCall) error {
		var lookupErr error
		info, lookupErr = lookupQuakeTerminalOn(s.ctx, w, dir)
		return lookupErr
	}); err != nil {
		return "", nil, err
	}
	if info == nil {
		return "", nil, cli.Newf(cli.CodeNotFound, "no quake terminal is open for %s on worker %s", dir, cli.ShortID(workerID)).
			WithHint(`start it with "leapmux terminal quake open --dir %s" or "leapmux terminal quake run <command>"`, dir)
	}
	return workerID, info, nil
}

// ensureQuakeTerminalOn returns the existing quake terminal for dir or opens a
// new one over the shared channel w. created is true when OpenTerminal started
// a new shell (so initialCmd ran on startup); it is false when the terminal
// already existed or OpenTerminal adopted a shell started concurrently.
func ensureQuakeTerminalOn(ctx context.Context, w workerCall, dir, initialCmd string) (info *leapmuxv1.TerminalInfo, created bool, err error) {
	info, err = lookupQuakeTerminalOn(ctx, w, dir)
	if err != nil {
		return nil, false, err
	}
	if info != nil {
		return info, false, nil
	}
	var openResp leapmuxv1.OpenTerminalResponse
	if err := w.Call(ctx, leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure, &leapmuxv1.OpenTerminalRequest{
		WorkingDir:     dir,
		InitialCommand: initialCmd,
		Target:         &leapmuxv1.OpenTerminalRequest_Quake{Quake: &leapmuxv1.QuakeTerminalTarget{}},
	}, &openResp); err != nil {
		return nil, false, err
	}
	info = &leapmuxv1.TerminalInfo{
		TerminalId: openResp.GetTerminalId(),
		Title:      openResp.GetTitle(),
		WorkingDir: dir,
		Quake:      true,
		Status:     leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING,
	}
	return info, !openResp.GetAdopted(), nil
}

func runQuakeGet(inv *cli.Invocation) (any, error) {
	if err := checkGitDirInput(inv); err != nil {
		return nil, err
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	_, info, err := s.resolveQuakeTerminal()
	if err != nil {
		return nil, err
	}
	return terminalRowOf(info), nil
}

func runQuakeRead(inv *cli.Invocation) (any, error) {
	if err := checkGitDirInput(inv); err != nil {
		return nil, err
	}
	return executeTerminalRead(inv, func(s *session) (tabTarget, *leapmuxv1.TerminalInfo, error) {
		workerID, info, err := s.resolveQuakeTerminal()
		if err != nil {
			return tabTarget{}, nil, err
		}
		target := tabTarget{ID: info.GetTerminalId(), Type: leapmuxv1.TabType_TAB_TYPE_TERMINAL, WorkerID: workerID}
		return target, info, nil
	})
}

func runQuakeRun(inv *cli.Invocation) (any, error) {
	if err := checkGitDirInput(inv); err != nil {
		return nil, err
	}
	command, err := readRunCommand(inv)
	if err != nil {
		return nil, err
	}
	s, workerID, dir, err := openWorkerSessionWithDir(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	var terminalID string
	if err := withWorkerChannel(s.ctx, s.c, workerID, func(w workerCall) error {
		info, created, err := ensureQuakeTerminalOn(s.ctx, w, dir, command)
		if err != nil {
			return err
		}
		terminalID = info.GetTerminalId()
		if created {
			// InitialCommand already queued the command atomically on startup.
			return nil
		}
		req := &leapmuxv1.SendInputRequest{
			TerminalId: terminalID,
			Data:       []byte(command),
			Paste:      true,
			Keys:       []string{"enter"},
		}
		return w.Call(s.ctx, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, req, nil)
	}); err != nil {
		return nil, err
	}
	return terminalRunResult{ID: terminalID, Command: command}, nil
}

func runQuakeSend(inv *cli.Invocation) (any, error) {
	if err := checkGitDirInput(inv); err != nil {
		return nil, err
	}
	text, keys, paste, err := terminalSendPayload(inv)
	if err != nil {
		return nil, err
	}
	s, workerID, dir, err := openWorkerSessionWithDir(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	var terminalID string
	if err := withWorkerChannel(s.ctx, s.c, workerID, func(w workerCall) error {
		info, _, err := ensureQuakeTerminalOn(s.ctx, w, dir, "")
		if err != nil {
			return err
		}
		terminalID = info.GetTerminalId()
		req := &leapmuxv1.SendInputRequest{
			TerminalId: terminalID,
			Data:       text,
			Paste:      paste,
			Keys:       keys,
		}
		return w.Call(s.ctx, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, req, nil)
	}); err != nil {
		return nil, err
	}
	return terminalSendResult{ID: terminalID, TextBytes: len(text), Paste: paste, Keys: keys}, nil
}
