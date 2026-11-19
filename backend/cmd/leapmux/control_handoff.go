package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

// controlCommand is the name of the command group that speaks to a Worker.
const controlCommand = "control"

// controlHandoffMarker marks a process that a hand-off started, so that the
// process never hands the command off again. A target that leads back to a
// `leapmux` with the hand-off, such as a script that runs `leapmux` from PATH,
// would otherwise start one process after another. The prefix makes
// controlenv.Apply strip the marker from each process that a Worker spawns.
const controlHandoffMarker = controlenv.VariablePrefix + "CLI_HANDOFF"

// controlHandoff hands a `leapmux control` command that runs inside a LeapMux
// tab to the CLI of the Worker that started the tab.
//
// That CLI speaks the Worker's protocol, and another `leapmux` may not: the
// two come from separate releases when the user installed a `leapmux` of
// another version. The Worker puts the directory of its own CLI first on PATH,
// but a login shell's profile can move another `leapmux` before it. macOS
// path_helper does so for /usr/local/bin in every zsh login shell. The hand-off
// makes a `leapmux control` command reach the Worker's CLI anyway.
//
// It hands off only a command that acts on the control group: `control ...`
// in any spelling that the CLI accepts, `help control ...`, and a shell
// completion of `control ...`. Every other command describes or acts on the
// binary that the user ran, and must stay with it. `leapmux worker run` inside
// a tab starts the Worker that the user chose, `leapmux version` reports the
// binary that runs, and `leapmux commands` lists its commands.
type controlHandoff struct {
	// environ is the environment of this process. A target inherits it.
	environ []string
	// executable returns the path of the running executable. When it fails,
	// the hand-off cannot tell the two executables apart, so it hands the
	// command off, and the marker stops a second hand-off.
	executable func() (string, error)
	// exec runs target with argv and env in place of this process. It returns
	// only when target does not take over the process: on Unix after a failure
	// to run it, and on Windows with the exit code of target or a failure to
	// start it.
	exec func(target string, argv, env []string) (int, error)
	// stderr receives the warning of a hand-off that cannot run.
	stderr io.Writer
}

// handOffControlCommand runs the hand-off of args with the environment and
// the executable of this process. It returns the exit code of the process and
// true when the Worker's CLI ran the command. Otherwise it returns false, and
// this process runs the command.
func handOffControlCommand(args []string) (int, bool) {
	code, handled := controlHandoff{environ: os.Environ(), executable: os.Executable, exec: execControlCLI, stderr: os.Stderr}.run(args)
	// The marker served this process. A process that this one starts, such as
	// a command that `leapmux control` runs, decides for itself.
	if err := os.Unsetenv(controlHandoffMarker); err != nil {
		(&cli.Printer{Err: os.Stderr}).Notice("warning: cannot clear %s: %v", controlHandoffMarker, err)
	}
	return code, handled
}

// run hands args off and returns the exit code and true, or returns false
// when this process must run the command itself. It reads the variables
// first, so a command outside a tab builds no command tree and reads no file.
func (h controlHandoff) run(args []string) (int, bool) {
	if h.getenv(controlHandoffMarker) != "" || h.getenv(controlenv.SocketVariable) == "" || h.getenv(controlenv.TokenVariable) == "" {
		return 0, false
	}
	target := h.getenv(controlenv.CLIVariable)
	if target == "" || !actsOnControl(args) {
		return 0, false
	}
	info, err := statControlCLI(target)
	if err != nil {
		h.warn(err)
		return 0, false
	}
	if h.isSelf(info) {
		return 0, false
	}
	argv := append([]string{target}, args...)
	// PinEnv, not append: the target reads the first entry of a name, and an
	// empty marker that this process inherited would come first.
	env := envutil.PinEnv(h.environ, controlHandoffMarker+"=1")
	code, err := h.exec(target, argv, env)
	if err != nil {
		h.warn(fmt.Errorf("the Worker's CLI %s did not start: %w", target, err))
		return 0, false
	}
	return code, true
}

// getenv returns the value of name in h.environ, as os.Getenv and the C
// library read an environment: the first entry wins, and Windows ignores the
// case of the name.
func (h controlHandoff) getenv(name string) string {
	for _, entry := range h.environ {
		key, value, ok := strings.Cut(entry, "=")
		if ok && (key == name || runtime.GOOS == "windows" && strings.EqualFold(key, name)) {
			return value
		}
	}
	return ""
}

// isSelf reports whether target, whose information is info, is the running
// executable. The comparison reads file identity, so the path itself, a hard
// link and a symbolic link all count as the running executable. It answers
// false when it cannot read the running executable.
func (h controlHandoff) isSelf(info fs.FileInfo) bool {
	path, err := h.executable()
	if err != nil {
		return false
	}
	self, err := os.Stat(path)
	return err == nil && os.SameFile(self, info)
}

// warn states on stderr why the hand-off cannot run, and that this process
// runs the command instead.
func (h controlHandoff) warn(err error) {
	(&cli.Printer{Err: h.stderr}).Notice("warning: %v. This leapmux runs the command, and its version can differ from the Worker's.", err)
}

// actsOnControl reports whether args act on the control group (see
// controlHandoff).
func actsOnControl(args []string) bool {
	top := cli.TopLevelCommand(newRootCommand(defaultCLIRunners()), args)
	return top != nil && top.Name() == controlCommand
}

// statControlCLI returns the information of target, or an error that states
// why target cannot run the command.
func statControlCLI(target string) (fs.FileInfo, error) {
	if !filepath.IsAbs(target) {
		return nil, fmt.Errorf("%s holds %q, which is not an absolute path", controlenv.CLIVariable, target)
	}
	info, err := os.Stat(target)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("the Worker's CLI %s does not exist", target)
	case err != nil:
		return nil, fmt.Errorf("cannot read the Worker's CLI: %w", err)
	case !info.Mode().IsRegular():
		return nil, fmt.Errorf("the Worker's CLI %s is not a file", target)
	case runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0:
		// Windows has no execute permission bit; it runs a file by its
		// extension.
		return nil, fmt.Errorf("the Worker's CLI %s is not executable", target)
	}
	return info, nil
}
