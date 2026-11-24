package cmd

import (
	"time"

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
			fs.Int("lines", 0, "print only the last N lines")
			fs.Bool("scrollback", false, "also print the lines above the screen")
			fs.Bool("raw", false, "print the retained bytes as they are, with their escape sequences")
			fs.BoolP("follow", "f", false, "stream live output until Ctrl-C or shell exit")
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

func (s *session) resolveQuakeTerminal(dir string, createIfMissing bool) (string, *leapmuxv1.TerminalInfo, error) {
	workerID, info, _, err := s.resolveQuakeTerminalWithInit(dir, createIfMissing, "")
	return workerID, info, err
}

func (s *session) resolveQuakeTerminalWithInit(dir string, createIfMissing bool, initialCmd string) (string, *leapmuxv1.TerminalInfo, bool, error) {
	workerID, err := s.workerScope()
	if err != nil {
		return "", nil, false, err
	}
	if err := maybePreflightWorker(s.ctx, s.c, workerID); err != nil {
		return "", nil, false, err
	}
	var resp leapmuxv1.ListTerminalsResponse
	if err := callInnerRPC(s.ctx, s.c, workerID, leapmuxv1connect.WorkerTerminalServiceListTerminalsProcedure, &leapmuxv1.ListTerminalsRequest{
		QuakeWorkingDirs: []string{dir},
	}, &resp); err != nil {
		return "", nil, false, err
	}
	for _, info := range resp.GetTerminals() {
		if info.GetQuake() {
			return workerID, info, false, nil
		}
	}
	if !createIfMissing {
		return "", nil, false, cli.Newf(cli.CodeNotFound, "no quake terminal is open for %s on worker %s", dir, cli.ShortID(workerID)).
			WithHint(`start it with "leapmux terminal quake open --dir %s" or "leapmux terminal quake run <command>"`, dir)
	}
	var openResp leapmuxv1.OpenTerminalResponse
	if err := callInnerRPC(s.ctx, s.c, workerID, leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure, &leapmuxv1.OpenTerminalRequest{
		WorkingDir:     dir,
		InitialCommand: initialCmd,
		Target:         &leapmuxv1.OpenTerminalRequest_Quake{Quake: &leapmuxv1.QuakeTerminalTarget{}},
	}, &openResp); err != nil {
		return "", nil, false, err
	}
	return workerID, &leapmuxv1.TerminalInfo{
		TerminalId: openResp.GetTerminalId(),
		Title:      openResp.GetTitle(),
		WorkingDir: dir,
		Quake:      true,
	}, true, nil
}

// waitForQuakeTerminalReady polls ListTerminals on workerID until a newly
// opened quake terminal transitions out of STARTING so SendInput does not race
// shell PTY registration.
func (s *session) waitForQuakeTerminalReady(workerID, dir, terminalID string) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		var resp leapmuxv1.ListTerminalsResponse
		if err := callInnerRPC(s.ctx, s.c, workerID, leapmuxv1connect.WorkerTerminalServiceListTerminalsProcedure, &leapmuxv1.ListTerminalsRequest{
			QuakeWorkingDirs: []string{dir},
		}, &resp); err == nil {
			for _, info := range resp.GetTerminals() {
				if info.GetTerminalId() == terminalID {
					switch info.GetStatus() {
					case leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY:
						return nil
					case leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED:
						return cli.Newf(cli.CodeInternal, "quake terminal %s exited before becoming ready", cli.ShortID(terminalID))
					case leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED:
						return cli.Newf(cli.CodeInternal, "quake terminal %s failed to start", cli.ShortID(terminalID))
					case leapmuxv1.TerminalStatus_TERMINAL_STATUS_UNSPECIFIED,
						leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING:
						// Still starting; keep polling until ready or context deadline.
					}
				}
			}
		}
		select {
		case <-s.ctx.Done():
			return s.ctx.Err()
		case <-ticker.C:
		}
	}
}

func runQuakeGet(inv *cli.Invocation) (any, error) {
	dir, err := dirOf(inv)
	if err != nil {
		return nil, err
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	_, info, err := s.resolveQuakeTerminal(dir, false)
	if err != nil {
		return nil, err
	}
	return terminalRowOf(info), nil
}

func runQuakeRead(inv *cli.Invocation) (any, error) {
	dir, err := dirOf(inv)
	if err != nil {
		return nil, err
	}
	opts, err := screenReadOptionsOf(inv)
	if err != nil {
		return nil, err
	}
	follow := boolFlag(inv, "follow")
	if follow {
		s, err := openStreamSession(inv)
		if err != nil {
			return nil, err
		}
		defer s.Close()
		bounded := s.bounded()
		workerID, info, err := bounded.resolveQuakeTerminal(dir, false)
		bounded.Close()
		if err != nil {
			return nil, err
		}
		target := tabTarget{ID: info.GetTerminalId(), Type: leapmuxv1.TabType_TAB_TYPE_TERMINAL, WorkerID: workerID}
		return nil, followTerminalOutput(inv, s, target, info, opts)
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	_, info, err := s.resolveQuakeTerminal(dir, false)
	if err != nil {
		return nil, err
	}
	if opts.raw {
		return rawScreenOf(info), nil
	}
	return renderScreen(info, opts)
}

func runQuakeRun(inv *cli.Invocation) (any, error) {
	dir, err := dirOf(inv)
	if err != nil {
		return nil, err
	}
	command, err := readRunCommand(inv)
	if err != nil {
		return nil, err
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	workerID, info, created, err := s.resolveQuakeTerminalWithInit(dir, true, command)
	if err != nil {
		return nil, err
	}
	if created {
		// InitialCommand already queued the command atomically on startup.
		return terminalRunResult{ID: info.GetTerminalId(), Command: command}, nil
	}
	req := &leapmuxv1.SendInputRequest{
		TerminalId: info.GetTerminalId(),
		Data:       []byte(command),
		Paste:      true,
		Keys:       []string{"enter"},
	}
	if err := callInnerRPC(s.ctx, s.c, workerID, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, req, nil); err != nil {
		return nil, err
	}
	return terminalRunResult{ID: info.GetTerminalId(), Command: command}, nil
}

func runQuakeSend(inv *cli.Invocation) (any, error) {
	dir, err := dirOf(inv)
	if err != nil {
		return nil, err
	}
	text, hasText, err := optionalInputBytes(inv, "text")
	if err != nil {
		return nil, err
	}
	keys, err := terminalKeyNames(inv)
	if err != nil {
		return nil, err
	}
	paste := boolFlag(inv, "paste")
	switch {
	case !hasText && len(keys) == 0:
		return nil, cli.Usagef("missing <text>").
			WithHint(`give the text as an argument, as "-" to read stdin, or with --file, or press a key with --key`)
	case paste && !hasText:
		return nil, cli.Usagef("--paste needs a text")
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	workerID, info, created, err := s.resolveQuakeTerminalWithInit(dir, true, "")
	if err != nil {
		return nil, err
	}
	if created {
		if err := s.waitForQuakeTerminalReady(workerID, dir, info.GetTerminalId()); err != nil {
			return nil, err
		}
	}
	req := &leapmuxv1.SendInputRequest{
		TerminalId: info.GetTerminalId(),
		Data:       text,
		Paste:      paste,
		Keys:       keys,
	}
	if err := callInnerRPC(s.ctx, s.c, workerID, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, req, nil); err != nil {
		return nil, err
	}
	return terminalSendResult{ID: info.GetTerminalId(), TextBytes: len(text), Paste: paste, Keys: keys}, nil
}
