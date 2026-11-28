package terminal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
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

const (
	initialCommandLaunchRoot   = "terminal-command-handoffs"
	initialCommandLaunchPrefix = "launch-"

	initialCommandEnvPrefix         = "LEAPMUX_INITIAL_COMMAND_"
	initialCommandEnvParent         = initialCommandEnvPrefix + "PARENT"
	initialCommandEnvEnvFile        = initialCommandEnvPrefix + "ENV_FILE"
	initialCommandEnvOriginalEnv    = initialCommandEnvPrefix + "ORIGINAL_ENV"
	initialCommandEnvHasEnv         = initialCommandEnvPrefix + "HAS_ENV"
	initialCommandEnvOriginalZdot   = initialCommandEnvPrefix + "ORIGINAL_ZDOTDIR"
	initialCommandEnvHasZdot        = initialCommandEnvPrefix + "HAS_ZDOTDIR"
	initialCommandEnvManagedZdot    = initialCommandEnvPrefix + "MANAGED_ZDOTDIR"
	initialCommandEnvKshLogin       = initialCommandEnvPrefix + "KSH_LOGIN"
	initialCommandEnvKshInteractive = initialCommandEnvPrefix + "KSH_INTERACTIVE"
	initialCommandEnvPoshHome       = initialCommandEnvPrefix + "POSH_HOME"
)

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
	case "bash", "zsh", "fish", "elvish", "xonsh", "yash", nushellExecutable:
		return name
	case "sh", "dash", "ash":
		return "posix"
	case "posh":
		return "posh"
	case "csh", "tcsh":
		return "csh"
	case "ksh", "ksh93", "mksh":
		return "korn"
	default:
		return ""
	}
}

type initialCommandBootstrap struct {
	dir           string
	nonce         string
	nushellPipe   string
	sourceWriter  *os.File
	sourceCommand func(string) string
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
	var cleanupErr error
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), initialCommandLaunchPrefix) {
			cleanupErr = errors.Join(cleanupErr, removeInitialCommandLaunch(filepath.Join(root, entry.Name())))
		}
	}
	return cleanupErr
}

func removeInitialCommandLaunch(dir string) error {
	linkErr := removeElvishStartupLink(dir)
	return errors.Join(linkErr, os.RemoveAll(dir))
}

func (b *initialCommandBootstrap) close() {
	if b.sourceWriter != nil {
		_ = b.sourceWriter.Close()
	}
	if err := removeInitialCommandLaunch(b.dir); err != nil {
		slog.Warn("Remove a terminal command handoff", "error", err)
	}
}

func (b *initialCommandBootstrap) commandPath() string { return filepath.Join(b.dir, "command") }
func (b *initialCommandBootstrap) ackPath() string     { return filepath.Join(b.dir, "acknowledged") }
func (b *initialCommandBootstrap) requestPath() string { return filepath.Join(b.dir, "ready") }

func (b *initialCommandBootstrap) writeFile(name, content, label string) (string, error) {
	path := filepath.Join(b.dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", fmt.Errorf("write the %s: %w", label, err)
	}
	return path, nil
}

func (b *initialCommandBootstrap) launch(ctx context.Context, shell, workingDir string, args, env []string) (string, []string, []string, error) {
	dialect := initialCommandDialect(shell)
	envPrefix := initialCommandEnvPrefix
	if dialect == "csh" {
		envPrefix = cshInitialCommandEnvPrefix
	}
	env = append(env,
		envPrefix+"FILE="+b.commandPath(),
		envPrefix+"ACK="+b.ackPath(),
		envPrefix+"READY="+b.requestPath(),
		envPrefix+"NONCE="+b.nonce,
	)
	if dialect == "powershell" {
		return shell, append(args, "-NoExit", "-Command", powerShellInitialCommand), env, nil
	}
	env, args, err := b.unixStartup(ctx, shell, dialect, workingDir, env, args)
	if err != nil {
		return "", nil, nil, err
	}
	// The launcher uses exec, so the native shell retains the exact root
	// process identity that the PTY owner observes.
	pidVariable := envPrefix + "PID"
	launcher := []string{"-c", pidVariable + `=$$; export ` + pidVariable + `; exec "$@"`, "leapmux-terminal-start", shell}
	return "/bin/sh", append(launcher, args...), env, nil
}

func (b *initialCommandBootstrap) unixStartup(ctx context.Context, shell, dialect, workingDir string, env, args []string) ([]string, []string, error) {
	switch dialect {
	case "bash":
		source, err := b.writeFile("environment", bashInitialStartup, "Bash startup file")
		if err != nil {
			return nil, nil, err
		}
		env = rememberStartupEnvironment(env, source)
		useRcfile, err := bashRequiresRcfileFallback(ctx, shell)
		if err != nil {
			return nil, nil, err
		}
		if useRcfile {
			// Bash 3.x skips ENV in Bash's own POSIX mode. An interactive
			// rcfile can still load the login profiles.
			args = []string{"--rcfile", source, "-i"}
		} else {
			args = append([]string{"--noprofile", "--posix"}, args...)
		}
	case "zsh":
		original, hasOriginal := lookupLaunchEnv(env, "ZDOTDIR")
		if hasOriginal {
			env = append(env, initialCommandEnvOriginalZdot+"="+original, initialCommandEnvHasZdot+"=1")
		}
		for _, profile := range []string{".zshenv", ".zprofile", ".zshrc", ".zlogin"} {
			if _, err := b.writeFile(profile, zshInitialStartup(profile, profile == ".zlogin"), "zsh startup file"); err != nil {
				return nil, nil, err
			}
		}
		env = append(env, "ZDOTDIR="+b.dir, initialCommandEnvManagedZdot+"="+b.dir)
	case "fish":
		args = append(args, "-C", fishInitialCommand)
	case "csh":
		var err error
		env, err = b.cshStartup(env)
		if err != nil {
			return nil, nil, err
		}
	case "elvish":
		source, err := b.elvishStartupSource(workingDir, env)
		if err != nil {
			return nil, nil, err
		}
		args = append(args, "-rc", source)
	case "posix":
		source, err := b.writeFile("environment", posixInitialStartup, "shell startup hook")
		if err != nil {
			return nil, nil, err
		}
		env = append(rememberStartupEnvironment(env, source), initialCommandEnvParent+"="+filepath.Join(b.dir, "parent"))
		// Login files can replace ENV before the shell opens it. Read those
		// files from the managed interactive hook in their startup order.
		args = []string{"-i"}
	case "posh":
		var err error
		env, err = b.poshStartup(env)
		if err != nil {
			return nil, nil, err
		}
		env = append(env, initialCommandEnvParent+"="+filepath.Join(b.dir, "parent"))
	case "korn":
		var err error
		env, err = b.kornShellStartup(ctx, shell, env)
		if err != nil {
			return nil, nil, err
		}
		env = append(env, initialCommandEnvParent+"="+filepath.Join(b.dir, "parent"))
		args = []string{"-i"}
	case "yash":
		source, err := b.writeFile("profile", yashInitialStartup, "Yash startup profile")
		if err != nil {
			return nil, nil, err
		}
		env = append(env, initialCommandEnvParent+"="+filepath.Join(b.dir, "parent"))
		args = append(args, "--profile="+source, "--norcfile")
	case "xonsh":
		source, err := b.writeFile("startup.py", xonshInitialStartup, "Xonsh startup file")
		if err != nil {
			return nil, nil, err
		}
		args = append(args, "--no-script-cache", "--rc", source)
	case nushellExecutable:
		path := filepath.Join(b.dir, "startup.nu")
		if err := makeInitialCommandPipe(path); err != nil {
			return nil, nil, fmt.Errorf("create the Nushell startup pipe: %w", err)
		}
		b.nushellPipe = path
		b.sourceCommand = func(command string) string { return nushellInitialPrelude + command }
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

const bashRcfileCacheLimit = 64

var (
	bashRcfileCacheMu sync.RWMutex
	bashRcfileCache   = make(map[shellBinaryKey]bool)
)

// bashRequiresRcfileFallback reports whether shell is a Bash 3.x binary whose
// POSIX mode ignores ENV. Results are cached by executable identity.
func bashRequiresRcfileFallback(ctx context.Context, shell string) (bool, error) {
	key, cacheable := shellBinaryIdentity(shell)
	if cacheable {
		bashRcfileCacheMu.RLock()
		cached, ok := bashRcfileCache[key]
		bashRcfileCacheMu.RUnlock()
		if ok {
			return cached, nil
		}
	}
	// Probe BASH_VERSINFO[0] without passing startup profiles or credentials.
	probe := exec.CommandContext(ctx, shell, "-c", `command printf '%s' "${BASH_VERSINFO[0]-}"`)
	probe.Env = []string{"ENV=", "BASH_ENV="}
	out, err := probe.Output()
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, nil
	}
	major, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || major < 1 {
		return false, nil
	}
	needsRcfile := major < 4
	if cacheable {
		bashRcfileCacheMu.Lock()
		if len(bashRcfileCache) >= bashRcfileCacheLimit {
			clear(bashRcfileCache)
		}
		bashRcfileCache[key] = needsRcfile
		bashRcfileCacheMu.Unlock()
	}
	return needsRcfile, nil
}

func rememberStartupEnvironment(env []string, source string) []string {
	// ENV undergoes shell expansion. Expand a variable containing the path
	// so characters in the path stay data.
	return append(saveOriginalStartupEnvironment(env), initialCommandEnvEnvFile+"="+source, "ENV=${"+initialCommandEnvEnvFile+"}")
}

func saveOriginalStartupEnvironment(env []string) []string {
	if previous, ok := lookupLaunchEnv(env, "ENV"); ok {
		return append(env, initialCommandEnvOriginalEnv+"="+previous, initialCommandEnvHasEnv+"=1")
	}
	return append(env, initialCommandEnvHasEnv+"=0")
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

func startupHomeDirectory(env []string) (string, error) {
	if home, _ := lookupLaunchEnv(env, "HOME"); home != "" {
		return home, nil
	}
	owner, err := user.Current()
	if err != nil {
		return "", err
	}
	return owner.HomeDir, nil
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
waiting:
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
			break waiting
		case <-deadline.C:
			break waiting
		case <-poll.C:
		}
	}
	// The shell can publish its marker while the Worker is between polls.
	// A ready deadline or exit notification can be selected before that poll.
	if ready, err := check(); ready || err != nil {
		return err
	}
	return unconfirmed
}

func (b *initialCommandBootstrap) markerMatches(path, expected string) bool {
	f, err := openInitialCommandMarker(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 256 {
		return false
	}
	content, err := io.ReadAll(io.LimitReader(f, 257))
	return err == nil && len(content) <= 256 && strings.TrimSpace(string(content)) == expected
}

func (b *initialCommandBootstrap) release(ctx context.Context, term *Terminal, opts Options) error {
	expose := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if term.IsExited() {
			return ErrInitialCommandHandoff
		}
		if b.nushellPipe != "" && b.sourceWriter == nil {
			return ErrInitialCommandHandoff
		}
		command := opts.InitialCommand
		if b.sourceCommand != nil {
			command = b.sourceCommand(command)
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
