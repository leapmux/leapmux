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

	"github.com/leapmux/leapmux/internal/util/atomicfile"
	"github.com/leapmux/leapmux/internal/util/id"
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
	name := ShellBaseName(shell)
	if IsPwsh(name) {
		return nil
	}
	if runtime.GOOS != "windows" {
		switch name {
		case "bash", "zsh", "fish", "sh", "dash":
			return nil
		}
	}
	return fmt.Errorf("shell %q does not provide a verified initial command handoff", name)
}

type initialCommandBootstrap struct {
	dir   string
	nonce string
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
	return &initialCommandBootstrap{dir: dir, nonce: id.Generate()}, nil
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

func (b *initialCommandBootstrap) close() { _ = os.RemoveAll(b.dir) }

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
	name := ShellBaseName(shell)
	if IsPwsh(name) {
		return shell, append(args, "-NoExit", "-Command", powerShellInitialCommand), env, nil
	}
	env, args, err := b.posixStartup(shell, env, args)
	if err != nil {
		return "", nil, nil, err
	}
	// The launcher uses exec, so the native shell retains the exact root
	// process identity that the PTY owner observes.
	launcher := []string{"-c", `LEAPMUX_INITIAL_COMMAND_PID=$$; export LEAPMUX_INITIAL_COMMAND_PID; exec "$@"`, "leapmux-terminal-start", shell}
	return "/bin/sh", append(launcher, args...), env, nil
}

func (b *initialCommandBootstrap) posixStartup(shell string, env, args []string) ([]string, []string, error) {
	name := ShellBaseName(shell)
	switch name {
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
	case "sh", "dash":
		source := filepath.Join(b.dir, "environment")
		if err := os.WriteFile(source, []byte(posixInitialStartup), 0o600); err != nil {
			return nil, nil, fmt.Errorf("write the shell startup hook: %w", err)
		}
		env = rememberStartupEnvironment(env, source)
	default:
		return nil, nil, fmt.Errorf("shell %q has no initial command hook", name)
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
	deadline := clock.NewTimer(initialCommandWait, initialCommandDeadlineTag)
	defer deadline.Stop(initialCommandDeadlineTag)
	poll := clock.NewTicker(initialCommandPoll, initialCommandPollTag)
	defer poll.Stop(initialCommandPollTag)
	for {
		if ctx.Err() == nil && b.markerMatches(path, expected) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %w", unconfirmed, ctx.Err())
		case <-term.exitCh:
			if b.markerMatches(path, expected) {
				return nil
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
		return atomicfile.WriteFile(b.commandPath(), []byte(opts.InitialCommand), 0o600)
	}
	if opts.ReleaseInitialCommand != nil {
		return opts.ReleaseInitialCommand(ctx, expose)
	}
	return expose()
}
