//go:build unix

package launch

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

// xonshReportEnv makes the test binary print an xonshReport and exit.
const xonshReportEnv = "LEAPMUX_TEST_XONSH_REPORT"

// xonshReportVarsEnv lists, separated by commas, the variables whose values
// the report holds.
const xonshReportVarsEnv = "LEAPMUX_TEST_XONSH_REPORT_VARS"

// xonshReportExitEnv gives the exit status of the report helper. It is 0 when
// the variable is unset.
const xonshReportExitEnv = "LEAPMUX_TEST_XONSH_REPORT_EXIT"

// xonshReportRecord starts the line that holds the report.
const xonshReportRecord = "XONSH_REPORT:"

// xonshReportPrefixArgs make the test binary run TestXonshReportHelper. The
// helper reports each argument after "--".
var xonshReportPrefixArgs = []string{"-test.run=^TestXonshReportHelper$", "--"}

// xonshReportCandidate is the name of the report helper on PATH, for a launch
// that selects a candidate.
const xonshReportCandidate = "leapmux-xonsh-report"

// xonshOddName holds each kind of character that a Python string literal or
// xonsh can change, and that a file name and a native probe name can hold.
// It holds the character U+0080, which differs from the byte 0x80.
const xonshOddName = "tab\t ctrl\x01 del\x7f quote\" apostrophe' $HOME ${HOME} {a,b} @(x) !(y) $(z) `q` ~ * 한글 \u0080"

// xonshInvalidUTF8 holds bytes that are not valid UTF-8: two stray bytes, an
// encoded surrogate and a truncated sequence.
const xonshInvalidUTF8 = "\x80\xff\xed\xa0\x80\xe2\x82"

// xonshOddArgs are arguments that must reach the program byte for byte.
var xonshOddArgs = []string{
	"", xonshOddName, xonshInvalidUTF8, "a" + xonshInvalidUTF8 + "한", `back\slash`,
	`\x80`, `\U0000dc80`, "1\x012", "-flag", ">", "2>&1", "~/x",
}

// xonshReport is what TestXonshReportHelper prints. Each value is
// hex-encoded, because JSON replaces a byte that is not valid UTF-8.
type xonshReport struct {
	// Program is the program path that the helper received as argv[0].
	Program string
	// Args are the arguments after "--".
	Args []string
	// Env holds the value of each variable that xonshReportVarsEnv lists, and
	// nil for an unset one.
	Env map[string]*string
	// Transport lists the private pin variables that the program received.
	Transport []string
}

func TestXonshReportHelper(t *testing.T) {
	if os.Getenv(xonshReportEnv) != "1" {
		return
	}
	report := xonshReport{
		Program: hex.EncodeToString([]byte(os.Args[0])),
		Args:    []string{}, Env: map[string]*string{}, Transport: testPinVariableNames(),
	}
	if index := slices.Index(os.Args, "--"); index >= 0 {
		for _, arg := range os.Args[index+1:] {
			report.Args = append(report.Args, hex.EncodeToString([]byte(arg)))
		}
	}
	for name := range strings.SplitSeq(os.Getenv(xonshReportVarsEnv), ",") {
		if name == "" {
			continue
		}
		report.Env[name] = nil
		if value, set := os.LookupEnv(name); set {
			encoded := hex.EncodeToString([]byte(value))
			report.Env[name] = &encoded
		}
	}
	body, err := json.Marshal(report)
	if err != nil {
		os.Exit(2)
	}
	if _, err := fmt.Printf("%s%s\n", xonshReportRecord, body); err != nil {
		os.Exit(2)
	}
	status, _ := strconv.Atoi(os.Getenv(xonshReportExitEnv))
	os.Exit(status)
}

// program returns the program path that the helper received.
func (r xonshReport) program(t *testing.T) string {
	t.Helper()
	decoded, err := hex.DecodeString(r.Program)
	require.NoError(t, err)
	return string(decoded)
}

// arguments returns the arguments that the helper received.
func (r xonshReport) arguments(t *testing.T) []string {
	t.Helper()
	args := make([]string, len(r.Args))
	for index, arg := range r.Args {
		decoded, err := hex.DecodeString(arg)
		require.NoError(t, err)
		args[index] = string(decoded)
	}
	return args
}

// value returns the value of name that the helper received, and whether the
// variable was set.
func (r xonshReport) value(t *testing.T, name string) (string, bool) {
	t.Helper()
	encoded, listed := r.Env[name]
	require.True(t, listed, "the report lists %s", name)
	if encoded == nil {
		return "", false
	}
	decoded, err := hex.DecodeString(*encoded)
	require.NoError(t, err)
	return string(decoded), true
}

// xonshRun is what a wrapped xonsh launch of the report helper produced.
type xonshRun struct {
	preamble string // stdout before the delimiter
	meta     string // the prefix of each metadata line
	program  string // stdout after the delimiter
	stderr   string
	status   int
	report   *xonshReport // nil when the program printed no report
}

// runXonsh runs spec with env pinned over the inherited environment. A
// non-empty cli applies the control context of that CLI after Wrap, as
// providerkit.FinalizeAgentEnv applies it to an agent.
func runXonsh(t *testing.T, spec WrapSpec, cli string, env ...string) xonshRun {
	t.Helper()
	ctx := testutil.DeadlineContext(t)
	cmd, delimiter, meta := wrapTestCommand(t, ctx, spec)
	var control []string
	if cli != "" {
		control = []string{controlenv.CLIVariable + "=" + cli}
	}
	cmd.Env = controlenv.Apply(envutil.PinEnv(cmd.Environ(), env...), control)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	run := xonshRun{meta: meta, stderr: stderr.String()}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		require.NoError(t, err, "stdout: %q\nstderr: %q", stdout.String(), stderr.String())
	} else {
		run.status = exit.ExitCode()
	}
	preamble, program, found := strings.Cut(stdout.String(), delimiter+"\n")
	require.True(t, found, "the wrapper prints its delimiter: %q\nstderr: %q", stdout.String(), stderr.String())
	run.preamble, run.program = preamble, program
	for line := range strings.SplitSeq(program, "\n") {
		if body, isReport := strings.CutPrefix(line, xonshReportRecord); isReport {
			var report xonshReport
			require.NoError(t, json.Unmarshal([]byte(body), &report))
			run.report = &report
		}
	}
	return run
}

// requireReport returns the report of run, and fails the test when the
// program printed none.
func (run xonshRun) requireReport(t *testing.T) xonshReport {
	t.Helper()
	require.NotNil(t, run.report, "the program reports\nstdout after the delimiter: %q\nstderr: %q", run.program, run.stderr)
	return *run.report
}

// xonshReportEnvironment is the environment of a launch of the report helper
// that reports vars. Each startup file of xonsh is in home (see homeEnv).
func xonshReportEnvironment(home string, vars ...string) []string {
	return append(homeEnv(home), "TERM=xterm-256color", xonshReportEnv+"=1", xonshReportVarsEnv+"="+strings.Join(vars, ","))
}

// writeXonshRC writes body into the rc.xsh that homeEnv lists in XONSHRC.
func writeXonshRC(t *testing.T, home, body string) {
	t.Helper()
	path := filepath.Join(home, "xonsh", "rc.xsh")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
}

// testExecutable returns the absolute path of the test binary.
func testExecutable(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	return executable
}

// reportCandidateDirectory makes a directory that holds the report helper as
// xonshReportCandidate.
func reportCandidateDirectory(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.Symlink(testExecutable(t), filepath.Join(dir, xonshReportCandidate)))
	return dir
}

// xonshOddFileName returns a file name that holds xonshOddName and, on a file
// system that accepts them, the bytes of xonshInvalidUTF8. APFS refuses a
// name that is not valid UTF-8 with EILSEQ.
func xonshOddFileName(t *testing.T) string {
	t.Helper()
	name := "odd " + xonshOddName + " " + xonshInvalidUTF8
	err := os.Mkdir(filepath.Join(t.TempDir(), name), 0o700)
	if errors.Is(err, syscall.EILSEQ) {
		t.Logf("the file system refuses a name that is not valid UTF-8: %v", err)
		return "odd " + xonshOddName
	}
	require.NoError(t, err)
	return name
}

// `xonsh -c` prints the value of each expression statement outside a
// function, also inside a loop, and pop returns the value that it removes. A
// stripped value, such as a credential, must not reach the output. The test
// compares the whole preamble, so a launch statement that prints anything
// else fails it too.
func TestWrapXonshStripsAVariableWithoutPrintingIt(t *testing.T) {
	t.Parallel()
	shell := requireProfileShell(t, "xonsh", "xonsh")
	const secret = "leapmux-stripped-secret-5e1f"
	for _, login := range []bool{false, true} {
		t.Run("login="+strconv.FormatBool(login), func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			run := runXonsh(t, WrapSpec{
				Shell: shell, LoginShell: login,
				Launch:       Spec{Program: testExecutable(t), PrefixArgs: xonshReportPrefixArgs},
				StripEnvKeys: []string{"LEAPMUX_TEST_SECRET", "LEAPMUX_TEST_ABSENT"},
				SetEnv:       []string{"LEAPMUX_TEST_PIN=pinned"},
				EnvGated:     testEnvGate("gated"),
				WorkingDir:   home,
			}, "", append(xonshReportEnvironment(home, "LEAPMUX_TEST_SECRET", "LEAPMUX_TEST_PIN"),
				"LEAPMUX_TEST_SECRET="+secret, "LEAPMUX_TEST_GATE_A=", "LEAPMUX_TEST_GATE_B=")...)

			assert.NotContains(t, run.preamble, secret)
			assert.NotContains(t, run.stderr, secret)
			assert.Equal(t, "\n"+run.meta+"test_gate_open=true\n", run.preamble, "no launch statement prints")
			report := run.requireReport(t)
			_, stripped := report.value(t, "LEAPMUX_TEST_SECRET")
			assert.False(t, stripped, "the launch strips the variable")
			pinned, set := report.value(t, "LEAPMUX_TEST_PIN")
			assert.True(t, set)
			assert.Equal(t, "pinned", pinned)
			assert.Equal(t, []string{"gated"}, report.arguments(t))
		})
	}
}

// Each argument and the program path reach the program byte for byte through
// xonsh: on both launch paths, through the env gate, and for a byte that is
// not valid UTF-8, which Python reads from a surrogate escape.
func TestWrapXonshPassesArgumentsAndTheProgramPathExactly(t *testing.T) {
	t.Parallel()
	shell := requireProfileShell(t, "xonsh", "xonsh")
	odd := filepath.Join(t.TempDir(), xonshOddFileName(t))
	require.NoError(t, os.Mkdir(odd, 0o700))
	program := filepath.Join(odd, xonshOddFileName(t))
	require.NoError(t, os.Symlink(testExecutable(t), program))
	candidates := reportCandidateDirectory(t)
	for _, tc := range []struct {
		name    string
		launch  Spec
		base    []string
		program string // the argv[0] that the program receives
	}{
		{"program", Spec{Program: program, PrefixArgs: xonshReportPrefixArgs}, xonshOddArgs, program},
		{"candidates", ShellCommands("leapmux-xonsh-missing", xonshReportCandidate), append(slices.Clone(xonshReportPrefixArgs), xonshOddArgs...),
			filepath.Join(candidates, xonshReportCandidate)},
	} {
		for _, gate := range []string{"", "set"} {
			t.Run(tc.name+"/gate="+gate, func(t *testing.T) {
				t.Parallel()
				home := t.TempDir()
				run := runXonsh(t, WrapSpec{
					Shell: shell, LoginShell: true, Launch: tc.launch, BaseArgs: tc.base,
					EnvGated: testEnvGate(xonshOddArgs...), WorkingDir: home,
				}, "", append(xonshReportEnvironment(home),
					"PATH="+candidates+":/usr/bin:/bin", "LEAPMUX_TEST_GATE_A="+gate, "LEAPMUX_TEST_GATE_B=")...)

				want := slices.Clone(xonshOddArgs)
				if gate == "" {
					want = append(want, xonshOddArgs...)
				}
				report := run.requireReport(t)
				assert.Equal(t, tc.program, report.program(t))
				assert.Equal(t, want, report.arguments(t))
			})
		}
	}
}

// A native probe name reaches the lookup byte for byte, and the probe reports
// the path that it finds.
func TestProgramPathFindsAnOddlyNamedProgramThroughXonsh(t *testing.T) {
	shell := requireProfileShell(t, "xonsh", "xonsh")
	home := t.TempDir()
	for _, entry := range xonshReportEnvironment(home) {
		name, value, _ := strings.Cut(entry, "=")
		t.Setenv(name, value)
	}
	dir := t.TempDir()
	name := xonshOddFileName(t)
	program := filepath.Join(dir, name)
	testutil.WriteExecutable(t, program, "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", dir+":/usr/bin:/bin")

	path, result := ProgramPath(t.Context(), shell, true, name)
	require.Equal(t, ProbeYes, result)
	assert.Equal(t, program, path)
}

// A pin reaches the program exactly as the provider states it, whatever its
// name, also after the control CLI step ran. xonsh reads a variable whose name
// ends in PATH or DIRS as a list of paths, and it expands `~` and `$NAME` in
// each entry when it passes the list on. It converts the value of one of its
// own settings, such as the bool AUTO_CD, and it passes no value of a setting
// that has no detype function, such as PROMPT_FIELDS. A profile alias receives
// the pins through os.environ, which replace_env fills, and a program that the
// alias starts with `![...]` receives them from the environment of xonsh.
func TestWrapXonshSetsEachPinExactly(t *testing.T) {
	t.Parallel()
	shell := requireProfileShell(t, "xonsh", "xonsh")
	executable := testExecutable(t)
	candidates := reportCandidateDirectory(t)
	fixture := newControlCLIFixture(t)
	pins := []string{
		"LEAPMUX_TEST_FOO_PATH=~/a::$HOME/b",
		"LEAPMUX_TEST_X_DIRS=~:$HOME/x:",
		"LEAPMUX_TEST_EMPTY_PATH=",
		"LEAPMUX_TEST_SEPARATOR_DIRS=:",
		"LEAPMUX_TEST_PLAIN=~/c:$HOME",
		"AUTO_CD=yes",
		// The validator of PROMPT_FIELDS accepts any value, so only its
		// missing detype function shows that xonsh would not pass it on.
		"PROMPT_FIELDS=~/fields:$HOME",
		// The candidate lookup reads the pinned PATH, so the helper's
		// directory is on it.
		"PATH=~/leapmux-test-bin::$HOME/bin:" + candidates + ":/usr/bin:/bin",
	}
	var names []string
	for _, pin := range pins {
		name, _, _ := strings.Cut(pin, "=")
		names = append(names, name)
	}
	quotedHelper := pythonArguments(append([]string{executable}, xonshReportPrefixArgs...))
	profile := "import subprocess\n" +
		"def _leapmux_report_environ(args):\n" +
		"    return subprocess.call(" + quotedHelper + " + list(args))\n" +
		"def _leapmux_report_spawn(args):\n" +
		"    return ![@(" + quotedHelper + " + list(args))].returncode\n" +
		"aliases['leapmux-report-environ'] = _leapmux_report_environ\n" +
		"aliases['leapmux-report-spawn'] = _leapmux_report_spawn\n"
	for _, tc := range []struct {
		name   string
		launch Spec
		base   []string
	}{
		{"program", Spec{Program: executable, PrefixArgs: xonshReportPrefixArgs}, nil},
		{"external candidate", ShellCommands("leapmux-xonsh-missing", xonshReportCandidate), xonshReportPrefixArgs},
		{"alias through os.environ", ShellCommands("leapmux-report-environ"), nil},
		{"alias through a xonsh command", ShellCommands("leapmux-report-spawn"), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			writeXonshRC(t, home, profile)
			run := runXonsh(t, WrapSpec{
				Shell: shell, LoginShell: true, Launch: tc.launch, BaseArgs: tc.base, SetEnv: pins, WorkingDir: home,
			}, fixture.cli, append(xonshReportEnvironment(home, names...), "LEAPMUX_TEST_FOO_PATH=inherited", "AUTO_CD=0")...)

			report := run.requireReport(t)
			for _, pin := range pins {
				name, want, _ := strings.Cut(pin, "=")
				got, set := report.value(t, name)
				assert.True(t, set, "the program receives %s", name)
				assert.Equal(t, want, got, "the pin of %s", name)
			}
			assert.Empty(t, report.Transport, "the launch removes each private pin variable")
		})
	}
}

// A profile can define a Python function with the name of a builtin, such as
// any or next, and a bare call in the launch would run it. The launch calls
// each builtin through the builtins module, so the env gate, the candidate
// selection and the exit status do not depend on the profile.
func TestWrapXonshCallsTheBuiltinsPastProfileDefinitions(t *testing.T) {
	t.Parallel()
	shell := requireProfileShell(t, "xonsh", "xonsh")
	candidates := reportCandidateDirectory(t)
	const profile = "def any(*args, **kwargs):\n    return True\n" +
		"def next(*args, **kwargs):\n    return ('external', '/usr/bin/false')\n" +
		"class SystemExit(Exception):\n    pass\n"
	t.Run("launch", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		writeXonshRC(t, home, profile)
		run := runXonsh(t, WrapSpec{
			Shell: shell, LoginShell: true, Launch: ShellCommands("leapmux-xonsh-missing", xonshReportCandidate),
			BaseArgs: xonshReportPrefixArgs, EnvGated: testEnvGate("gated"), WorkingDir: home,
		}, "", append(xonshReportEnvironment(home), "PATH="+candidates+":/usr/bin:/bin",
			xonshReportExitEnv+"=37", "LEAPMUX_TEST_GATE_A=", "LEAPMUX_TEST_GATE_B=")...)

		assert.Equal(t, 37, run.status, "the launch exits with the status of the program\nstderr: %q", run.stderr)
		assert.Equal(t, "\n"+run.meta+"test_gate_open=true\n", run.preamble)
		assert.Equal(t, []string{"gated"}, run.requireReport(t).arguments(t))
	})
	t.Run("exhaustion", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		writeXonshRC(t, home, profile)
		prepared, err := Wrap(testutil.DeadlineContext(t), WrapSpec{Shell: shell, LoginShell: true, Launch: ShellCommands("leapmux-xonsh-missing"), WorkingDir: home})
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, prepared.Close()) })
		prepared.Cmd.Env = envutil.PinEnv(prepared.Cmd.Environ(), append(xonshReportEnvironment(home), "PATH=/usr/bin:/bin")...)
		diagnostics := prepared.NewDiagnostics(1024)
		prepared.Cmd.Stderr = diagnostics
		_, status := prepared.Cmd.Output()
		diagnostics.Finish()
		err = diagnostics.ExitError(status, false)
		require.ErrorIs(t, err, ErrNoCandidate, "stderr: %q", diagnostics.String())
		var exit *exec.ExitError
		require.ErrorAs(t, err, &exit)
		assert.Equal(t, 127, exit.ExitCode())
	})
}
