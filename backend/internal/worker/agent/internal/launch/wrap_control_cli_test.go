package launch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

// pathReportEnv makes the test binary print the PATH that it received and
// exit, so a wrapped launch reports the PATH of its program on every operating
// system.
const pathReportEnv = "LEAPMUX_TEST_PATH_REPORT"

// The report states an unset PATH apart from an empty one.
const (
	pathReportSet   = "set:"
	pathReportUnset = "unset"
)

func TestControlCLIPathReport(t *testing.T) {
	if os.Getenv(pathReportEnv) != "1" {
		return
	}
	if path, set := os.LookupEnv("PATH"); set {
		fmt.Print(pathReportSet + path)
	} else {
		fmt.Print(pathReportUnset)
	}
	os.Exit(0)
}

// programPath is the PATH that a program receives, or that a shell starts
// with: unset, or set to Value.
type programPath struct {
	Set   bool
	Value string
}

func setPath(value string) programPath { return programPath{Set: true, Value: value} }

// parsePathReport reads what TestControlCLIPathReport printed.
func parsePathReport(t *testing.T, report string) programPath {
	t.Helper()
	if value, found := strings.CutPrefix(report, pathReportSet); found {
		return setPath(value)
	}
	require.Equal(t, pathReportUnset, report, "the report states the PATH")
	return programPath{}
}

// homeEnv points every startup file of every shell at home, and keeps the
// telemetry of PowerShell off.
//
// xonsh reads the run control files that XONSHRC and XONSHRC_DIR list, and it
// exports both variables to each program that it starts. A test that runs
// inside a xonsh session would otherwise read the user's own files. The values
// here are the defaults of xonsh for this HOME and XDG_CONFIG_HOME, without the
// system file /etc/xonsh/xonshrc. The dummy history keeps an interactive xonsh
// from writing to the history file of that session.
func homeEnv(home string) []string {
	return []string{
		"HOME=" + home, "ZDOTDIR=" + home, "XDG_CONFIG_HOME=" + home,
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"), "XDG_STATE_HOME=" + filepath.Join(home, ".state"),
		"POWERSHELL_TELEMETRY_OPTOUT=1", "DOTNET_CLI_TELEMETRY_OPTOUT=1",
		"XONSHRC=" + filepath.Join(home, "xonsh", "rc.xsh") + string(os.PathListSeparator) + filepath.Join(home, ".xonshrc"),
		"XONSHRC_DIR=" + filepath.Join(home, "xonsh", "rc.d"),
		"XONSH_HISTORY_BACKEND=dummy",
	}
}

// Every dialect runs the step that puts the Worker's CLI first on PATH once.
// It runs inside the command, which the shell runs after its profile, and
// before each statement that strips or pins a variable, checks the env gate,
// selects a candidate, prints the delimiter or starts the program:
//
//   - A PATH that a provider pins through SetEnv reaches the program unchanged.
//   - The candidate lookup sees the Worker's CLI first.
//
// Only setup comes before the step, such as the definitions of functions and
// the statements that protect the builtins from the profile. The csh command
// and the Git Bash command run from an invocation file, which
// renderedWrapScript reads.
func TestWrapRunsTheControlCLIStepBeforeTheLaunchInEveryDialect(t *testing.T) {
	posix := func(shell string) string { return posixReprependControlCLI(posixBuiltinPrefix(shell)) }
	const program, absent = "leapmux-test-program", "leapmux-test-absent"
	for _, tc := range []struct {
		shell string
		step  string
	}{
		{"/bin/sh", posix("sh")},
		{"/bin/bash", posix("bash")},
		{"/bin/zsh", posix("zsh")},
		{"/bin/dash", posix("dash")},
		{"/bin/ksh", posix("ksh")},
		{"/usr/bin/profile-shim", posix("profile-shim")},
		{"/usr/bin/fish", fishReprependControlCLI},
		{"/usr/bin/nu", nuReprependControlCLI},
		{"/usr/bin/elvish", elvishReprependControlCLI},
		{"/bin/tcsh", cshReprependControlCLI},
		{"/bin/csh", cshReprependControlCLI},
		{"/usr/bin/pwsh", pwshReprependControlCLI},
		{"/usr/bin/powershell", pwshReprependControlCLI},
		{"/usr/bin/xonsh", xonshReprependControlCLI},
	} {
		for _, launch := range []struct {
			name  string
			spec  Spec
			first string // the first name that the selection or the launch reads
		}{
			{"program", Spec{Program: program}, program},
			{"candidates", ShellCommands(absent, program), absent},
		} {
			for _, login := range []bool{false, true} {
				for _, gate := range []*EnvGatedArgs{nil, testEnvGate("--gated")} {
					t.Run(fmt.Sprintf("%s/%s/login=%t/gated=%t", tc.shell, launch.name, login, gate != nil), func(t *testing.T) {
						cmd, delimiter, _ := wrapTestCommand(t, t.Context(), WrapSpec{
							Shell: tc.shell, LoginShell: login, Launch: launch.spec,
							StripEnvKeys: []string{"LEAPMUX_TEST_STRIP"}, SetEnv: []string{"LEAPMUX_TEST_PIN=1"},
							EnvGated: gate, WorkingDir: "/tmp",
						})
						script := renderedWrapScript(t, cmd)
						require.Equal(t, 1, strings.Count(script, tc.step), "the step runs once: %q", script)
						end := strings.Index(script, tc.step) + len(tc.step)
						later := []string{"LEAPMUX_TEST_STRIP", "LEAPMUX_TEST_PIN", launch.first, delimiter}
						if gate != nil {
							later = append(later, gate.EnvVars[0])
						}
						for _, text := range later {
							index := strings.Index(script, text)
							require.NotEqual(t, -1, index, "the script holds %q: %q", text, script)
							assert.GreaterOrEqual(t, index, end, "the step runs before %q: %q", text, script)
						}
					})
				}
			}
		}
	}
}

// controlCLIShells lists every Unix shell that the real-shell tests of the
// control CLI step start. requireProfileShell skips each shell that PATH does
// not hold, unless LEAPMUX_REQUIRE_PROFILE_SHELLS lists it.
var controlCLIShells = []string{"sh", "bash", "ash", "dash", "ksh", "zsh", "fish", "nu", "elvish", "tcsh", "csh", "pwsh", "xonsh"}

// conformanceShells lists the shells whose step the conformance test runs. A
// POSIX shell on Windows, such as Git Bash, reads a Windows path that it
// cannot put on its own PATH, so its step leaves PATH alone there.
func conformanceShells() []string {
	if runtime.GOOS == "windows" {
		return []string{"pwsh", "powershell"}
	}
	return controlCLIShells
}

// pathCase is one PATH that a shell can start with, with the value of
// LEAPMUX_CONTROL_CLI.
type pathCase struct {
	name string
	path programPath
	cli  string
}

// pathCases returns the PATH values that the conformance test starts each
// shell with. dir is an absolute directory that the cases put on PATH.
func pathCases(dir string) []pathCase {
	sep := string(os.PathListSeparator)
	join := func(entries ...string) programPath { return setPath(strings.Join(entries, sep)) }
	// Real system directories keep a shell that needs one of them working.
	system, bin := "/usr/bin", "/bin"
	// Windows refuses * and ? in a file name, and .NET Framework refuses them
	// in a path.
	odd := filepath.Join(dir, "a b*?[c]$x'q")
	if runtime.GOOS == "windows" {
		system, bin = filepath.Join(os.Getenv("SystemRoot"), "System32"), os.Getenv("SystemRoot")
		odd = filepath.Join(dir, "a b[c]$x'q")
	}
	root := filepath.VolumeName(dir) + string(filepath.Separator)
	cli := filepath.Join(dir, "leapmux")
	cases := []pathCase{
		{"puts an absent directory first", join(system, bin), cli},
		{"moves the directory from the end", join(system, bin, dir), cli},
		{"keeps one entry of a directory that PATH holds twice", join(dir, system, dir), cli},
		{"keeps the entries that only start like the directory", join(dir+"x", filepath.Join(dir, "sub"), bin), cli},
		{"reads the directory literally", join(system, odd, bin), filepath.Join(odd, "leapmux")},
		// xonsh expands `$NAME`, `${NAME}`, and a `~` after `=`, in an entry
		// of a PATH that it passes on. homeEnv sets HOME.
		{"reads a directory that holds a set variable literally", join(system, bin), filepath.Join(dir, "a$HOME ${HOME} b", "leapmux")},
		{"reads a directory that holds a tilde after an equals sign literally", join(system, bin), filepath.Join(dir, "x=~", "leapmux")},
		{"puts the root directory first", join(system, root, bin), filepath.Join(root, "leapmux")},
		{"leaves PATH alone for a directory that holds the separator", join(system, bin), filepath.Join(dir, "a"+sep+"b", "leapmux")},
		{"leaves PATH alone for a relative executable", join(system, bin), filepath.Join("cli-1", "leapmux")},
		{"leaves PATH alone without the variable", join(system, bin), ""},
	}
	if runtime.GOOS == "windows" {
		cases = append(cases, pathCase{"leaves PATH alone for a drive-relative path", join(system, bin), `/foo/leapmux`})
	} else {
		cases = append(cases, pathCase{"leaves PATH alone for a Windows path on Unix", join(system, bin), `C:\foo\leapmux`})
	}
	if runtime.GOOS != "windows" {
		// An empty entry stands for the working directory, and it keeps its
		// place. Windows holds no variable with an empty value: PowerShell
		// removes one that it sets to "".
		//
		// A shell that starts without PATH gives the program no PATH, or a
		// default PATH of its own, as fish and xonsh do. The step acts only
		// on a PATH that the program receives. On Windows, runWrappedPath
		// starts PowerShell with the environment of the host, which holds
		// Path.
		cases = append(cases,
			pathCase{"keeps the empty entry of an empty PATH", join(""), cli},
			pathCase{"keeps an empty entry before the directory", join("", dir), cli},
			pathCase{"keeps an empty entry after the directory", join(dir, ""), cli},
			pathCase{"keeps an empty entry between other entries", join(system, "", dir, bin, dir), cli},
			pathCase{"starts the shell without PATH", programPath{}, cli},
		)
	}
	return cases
}

// wantPath is the PATH that a dialect gives the program when the shell gives
// the program baseline without the variable. It is the PATH that
// controlenv.Apply makes of baseline, so the shell code and the Go code keep
// one rule. csh cannot split PATH exactly, so it puts the directory first and
// keeps every other entry (see cshReprependControlCLI).
func wantPath(shellName string, baseline programPath, cli string) programPath {
	if !baseline.Set {
		return baseline
	}
	var control []string
	if cli != "" {
		control = []string{controlenv.CLIVariable + "=" + cli}
	}
	apply := func(path string) string {
		return envutil.ValuesFor(controlenv.Apply([]string{"PATH=" + path}, control), "PATH")[0]
	}
	if shellName == "csh" || shellName == "tcsh" {
		// Apply changes a PATH that cannot hold the directory exactly when it
		// puts the directory on PATH, and only then does the csh step act.
		const unrelated = "/leapmux-test-unrelated"
		if apply(unrelated) == unrelated {
			return baseline
		}
		return setPath(filepath.Dir(cli) + ":" + baseline.Value)
	}
	return setPath(apply(baseline.Value))
}

// Each dialect moves the directory as controlenv.Apply does. A shell can edit
// PATH as it starts: pwsh adds its own directory, fish spells an empty entry as
// ".", xonsh and fish give themselves a default PATH, and the zshenv of Debian
// replaces an empty PATH. The baseline is therefore the PATH that the same
// shell gives the program without the variable, and the step must make of it
// what controlenv.Apply makes.
func TestWrapMovesTheControlCLIDirectoryAsControlenvDoes(t *testing.T) {
	t.Parallel()
	for _, name := range conformanceShells() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			shell := requireProfileShell(t, name, name)
			home := t.TempDir()
			for _, c := range pathCases(t.TempDir()) {
				t.Run(c.name, func(t *testing.T) {
					baseline := runWrappedPath(t, shell, home, c.path, "")
					got := runWrappedPath(t, shell, home, c.path, c.cli)
					assert.Equal(t, wantPath(name, baseline, c.cli), got)
				})
			}
		})
	}
}

// runWrappedPath starts shell without a login profile, with path as PATH and
// cli as LEAPMUX_CONTROL_CLI, and returns the PATH that the program receives.
func runWrappedPath(t *testing.T, shell, home string, path programPath, cli string) programPath {
	t.Helper()
	executable := profileCommandExecutable(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd, delimiter, _ := wrapTestCommand(t, ctx, WrapSpec{
		Shell: shell, Launch: Spec{Program: executable},
		BaseArgs:   []string{"-test.run=^TestControlCLIPathReport$"},
		WorkingDir: home,
	})
	// A Unix shell starts with this environment alone. Windows PowerShell
	// needs the system variables of the host, such as SystemRoot, to start.
	env := append(homeEnv(home), pathReportEnv+"=1")
	if path.Set {
		env = append(env, "PATH="+path.Value)
	}
	if runtime.GOOS == "windows" {
		env = envutil.PinEnv(envutil.StripByPrefix(os.Environ(), controlenv.VariablePrefix), env...)
	}
	if cli != "" {
		env = append(env, controlenv.CLIVariable+"="+cli)
	}
	cmd.Env = env
	output, err := cmd.Output()
	require.NoError(t, err, "output: %q", output)
	_, after, found := strings.Cut(string(output), delimiter+"\n")
	if !found {
		// PowerShell ends a line with CRLF on Windows.
		_, after, found = strings.Cut(string(output), delimiter+"\r\n")
	}
	require.True(t, found, "the wrapper prints its delimiter: %q", output)
	return parsePathReport(t, after)
}
