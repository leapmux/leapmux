package terminal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/util/atomicfile"
	"github.com/leapmux/leapmux/util/useronly"
)

const (
	initialCommandWait        = 15 * time.Second
	initialCommandPoll        = 20 * time.Millisecond
	initialCommandDeadlineTag = "terminal-initial-command-handoff"
	initialCommandPollTag     = "terminal-initial-command-probe"
)

const initialCommandLaunchRoot = "terminal-command-handoffs"
const initialCommandLaunchPrefix = "launch-"

var ErrInitialCommandHandoff = errors.New("the original shell did not acknowledge the initial command")
var errInitialCommandUnproved = errors.New("the Worker cannot verify whether the released initial command ran")

// CheckInitialCommand checks the interpreter and the command before creating
// a terminal that needs a shell-owned execution point.
func CheckInitialCommand(shell, command string) error {
	if command == "" {
		return nil
	}
	if strings.IndexByte(command, 0) >= 0 {
		return errors.New("the initial command cannot contain a null byte")
	}
	if initialCommandDialect(shell) == "" {
		return fmt.Errorf("shell %q does not provide a verified initial command handoff", ShellBaseName(shell))
	}
	return nil
}

func initialCommandDialect(shell string) string {
	name := ShellBaseName(shell)
	if IsPwsh(name) {
		return "powershell"
	}
	if runtime.GOOS == "windows" {
		return ""
	}
	switch name {
	case "bash", "zsh", "fish":
		return name
	case "sh", "dash":
		return "posix"
	case "nu", "nushell":
		return "nu"
	default:
		return ""
	}
}

type initialCommandBootstrap struct {
	dir          string
	nonce        string
	nushellPipe  string
	sourceWriter *os.File
}

func newInitialCommandBootstrap(parent string) (*initialCommandBootstrap, error) {
	if parent != "" {
		parent = filepath.Join(parent, initialCommandLaunchRoot)
		if err := useronly.Mkdir(parent); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create the initial command root: %w", err)
		}
		if err := useronly.VerifyDirectory(parent); err != nil {
			return nil, fmt.Errorf("verify the initial command root: %w", err)
		}
	}
	dir, err := useronly.MkdirTemp(parent, initialCommandLaunchPrefix)
	if err != nil {
		return nil, fmt.Errorf("create the initial command directory: %w", err)
	}
	// The terminal can start in a different working directory from the Worker.
	absDir, err := filepath.Abs(dir)
	if err != nil {
		_ = os.Remove(dir)
		return nil, fmt.Errorf("resolve the initial command directory: %w", err)
	}
	return &initialCommandBootstrap{dir: absDir, nonce: id.Generate()}, nil
}

// ReapInitialCommandLaunches removes files from a Worker stopped during a
// handoff. Call it only under the Worker's exclusive data directory ownership.
func ReapInitialCommandLaunches(dataDir string) error {
	if dataDir == "" {
		return nil
	}
	root := filepath.Join(dataDir, initialCommandLaunchRoot)
	if err := useronly.VerifyDirectory(root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), initialCommandLaunchPrefix) {
			if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *initialCommandBootstrap) close() {
	if b.sourceWriter != nil {
		_ = b.sourceWriter.Close()
	}
	_ = os.RemoveAll(b.dir)
}

func (b *initialCommandBootstrap) commandPath() string { return filepath.Join(b.dir, "command") }
func (b *initialCommandBootstrap) ackPath() string     { return filepath.Join(b.dir, "acknowledged") }
func (b *initialCommandBootstrap) requestPath() string { return filepath.Join(b.dir, "ready") }

func (b *initialCommandBootstrap) launch(shell string, args, env []string) (string, []string, []string, error) {
	env = append(env,
		"LEAPMUX_INITIAL_COMMAND_FILE="+b.commandPath(),
		"LEAPMUX_INITIAL_COMMAND_ACK="+b.ackPath(),
		"LEAPMUX_INITIAL_COMMAND_READY="+b.requestPath(),
		"LEAPMUX_INITIAL_COMMAND_NONCE="+b.nonce,
		"LEAPMUX_INITIAL_COMMAND_PARENT="+filepath.Join(b.dir, "parent"),
	)
	if initialCommandDialect(shell) == "powershell" {
		return shell, append(args, "-NoExit", "-Command", powerShellInitialCommand), env, nil
	}
	env, args, err := b.unixStartup(shell, env, args)
	if err != nil {
		return "", nil, nil, err
	}
	// The launcher uses exec, so the native shell retains the exact root
	// process identity that the PTY owner observes.
	launcher := []string{"-c", `LEAPMUX_INITIAL_COMMAND_PID=$$; export LEAPMUX_INITIAL_COMMAND_PID; exec "$@"`, "leapmux-terminal-start", shell}
	return "/bin/sh", append(launcher, args...), env, nil
}

func (b *initialCommandBootstrap) unixStartup(shell string, env, args []string) ([]string, []string, error) {
	switch initialCommandDialect(shell) {
	case "bash":
		source := filepath.Join(b.dir, "environment")
		if err := os.WriteFile(source, []byte(bashInitialStartup), 0o600); err != nil {
			return nil, nil, fmt.Errorf("write the Bash startup file: %w", err)
		}
		env = rememberStartupEnvironment(env, source)
		resolved, _ := filepath.EvalSymlinks(shell)
		if runtime.GOOS == "darwin" && resolved == "/bin/bash" {
			// Apple's Bash 3.2 skips ENV in Bash's own POSIX mode. An
			// interactive rcfile can still load the login profiles.
			args = []string{"--rcfile", source, "-i"}
		} else {
			args = append([]string{"--noprofile", "--posix"}, args...)
		}
	case "zsh":
		original, hasOriginal := lookupLaunchEnv(env, "ZDOTDIR")
		if hasOriginal {
			env = append(env, "LEAPMUX_INITIAL_COMMAND_ORIGINAL_ZDOTDIR="+original, "LEAPMUX_INITIAL_COMMAND_HAS_ZDOTDIR=1")
		}
		for _, profile := range []string{".zshenv", ".zprofile", ".zshrc", ".zlogin"} {
			source := filepath.Join(b.dir, profile)
			if err := os.WriteFile(source, []byte(zshInitialStartup(profile, profile == ".zlogin")), 0o600); err != nil {
				return nil, nil, fmt.Errorf("write the zsh startup file: %w", err)
			}
		}
		env = append(env, "ZDOTDIR="+b.dir, "LEAPMUX_INITIAL_COMMAND_MANAGED_ZDOTDIR="+b.dir)
	case "fish":
		args = append(args, "-C", fishInitialCommand)
	case "posix":
		source := filepath.Join(b.dir, "environment")
		if err := os.WriteFile(source, []byte(posixInitialStartup), 0o600); err != nil {
			return nil, nil, fmt.Errorf("write the shell startup hook: %w", err)
		}
		env = rememberStartupEnvironment(env, source)
	case "nu":
		path := filepath.Join(b.dir, "startup.nu")
		if err := makeInitialCommandPipe(path); err != nil {
			return nil, nil, fmt.Errorf("create the Nushell startup pipe: %w", err)
		}
		b.nushellPipe = path
		// The parser opens this path after the login files finish. It reads
		// a fixed source statement from the pipe, with the payload in a
		// separate file so a failed write cannot expose a partial command.
		quoted := `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(path) + `"`
		args = append(args, "--execute", "source "+quoted)
	default:
		return nil, nil, fmt.Errorf("shell %q has no initial command hook", ShellBaseName(shell))
	}
	return env, args, nil
}

func rememberStartupEnvironment(env []string, source string) []string {
	if previous, ok := lookupLaunchEnv(env, "ENV"); ok {
		env = append(env, "LEAPMUX_INITIAL_COMMAND_ORIGINAL_ENV="+previous, "LEAPMUX_INITIAL_COMMAND_HAS_ENV=1")
	}
	return append(env, "ENV="+source)
}

func lookupLaunchEnv(env []string, name string) (string, bool) {
	for i := len(env) - 1; i >= 0; i-- {
		key, value, _ := strings.Cut(env[i], "=")
		if key == name {
			return value, true
		}
	}
	return "", false
}

func (b *initialCommandBootstrap) await(ctx context.Context, term *Terminal, clock quartz.Clock, path string, unconfirmed error) error {
	expected := fmt.Sprintf("%d %s", term.shellPID, b.nonce)
	return waitForInitialCommand(ctx, term, clock, unconfirmed, func() (bool, error) {
		return b.markerMatches(path, expected), nil
	})
}

func (b *initialCommandBootstrap) awaitReady(ctx context.Context, term *Terminal, clock quartz.Clock) error {
	if b.nushellPipe == "" {
		return b.await(ctx, term, clock, b.requestPath(), ErrInitialCommandHandoff)
	}
	return waitForInitialCommand(ctx, term, clock, ErrInitialCommandHandoff, func() (bool, error) {
		writer, err := openInitialCommandPipe(b.nushellPipe)
		if err != nil {
			return false, fmt.Errorf("open the Nushell startup pipe: %w", err)
		}
		b.sourceWriter = writer
		return writer != nil, nil
	})
}

func waitForInitialCommand(ctx context.Context, term *Terminal, clock quartz.Clock, unconfirmed error, check func() (bool, error)) error {
	deadline := clock.NewTimer(initialCommandWait, initialCommandDeadlineTag)
	defer deadline.Stop(initialCommandDeadlineTag)
	poll := clock.NewTicker(initialCommandPoll, initialCommandPollTag)
	defer poll.Stop(initialCommandPollTag)
	for {
		if ctx.Err() == nil {
			if ready, err := check(); ready || err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %w", unconfirmed, ctx.Err())
		case <-term.exitCh:
			if ready, err := check(); ready || err != nil {
				return err
			}
			return unconfirmed
		case <-deadline.C:
			return unconfirmed
		case <-poll.C:
		}
	}
}

func (b *initialCommandBootstrap) markerMatches(path, expected string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 256 {
		return false
	}
	content, err := os.ReadFile(path)
	return err == nil && strings.TrimSpace(string(content)) == expected
}

func (b *initialCommandBootstrap) release(ctx context.Context, term *Terminal, opts Options) error {
	expose := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if term.IsExited() {
			return ErrInitialCommandHandoff
		}
		command := opts.InitialCommand
		if b.nushellPipe != "" {
			if b.sourceWriter == nil {
				return ErrInitialCommandHandoff
			}
			command = nushellInitialPrelude + command
		}
		if err := atomicfile.WriteFile(b.commandPath(), []byte(command), 0o600); err != nil {
			return err
		}
		if b.sourceWriter != nil {
			if _, err := b.sourceWriter.WriteString(nushellInitialSource); err != nil {
				return fmt.Errorf("release the Nushell startup source: %w", err)
			}
			writer := b.sourceWriter
			b.sourceWriter = nil
			if err := writer.Close(); err != nil {
				return fmt.Errorf("close the Nushell startup pipe: %w", err)
			}
		}
		return nil
	}
	if opts.ReleaseInitialCommand != nil {
		return opts.ReleaseInitialCommand(ctx, expose)
	}
	return expose()
}
