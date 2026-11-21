//go:build unix

package launch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/util/shellquote"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

// controlCLIFixture is a Worker's link directory and a decoy directory. Each
// holds a `leapmux` that prints which one it is.
type controlCLIFixture struct {
	// linkDir holds the Worker's own CLI, as controlipc.CLIDirectory does.
	linkDir string
	// decoyDir holds another `leapmux`, as /usr/local/bin can.
	decoyDir string
	// cli is the value of LEAPMUX_CONTROL_CLI.
	cli string
}

func newControlCLIFixture(t *testing.T) controlCLIFixture {
	t.Helper()
	root := t.TempDir()
	fixture := controlCLIFixture{
		linkDir:  filepath.Join(root, "cli-1"),
		decoyDir: filepath.Join(root, "decoy"),
	}
	fixture.cli = filepath.Join(fixture.linkDir, "leapmux")
	for dir, name := range map[string]string{fixture.linkDir: "worker", fixture.decoyDir: "decoy"} {
		require.NoError(t, os.Mkdir(dir, 0o700))
		testutil.WriteExecutable(t, filepath.Join(dir, "leapmux"), "#!/bin/sh\necho "+name+"\n")
	}
	return fixture
}

// shellProfiles holds the body of the login profile of each dialect.
type shellProfiles struct {
	posix, csh, fish, nu, elvish, pwsh, xonsh string
}

// writeShellProfiles writes, into home, the login profile of each shell that
// the tests start. The caller passes homeEnv(home), so a shell reads these
// files and none of the user's.
func writeShellProfiles(t *testing.T, home string, profiles shellProfiles) {
	t.Helper()
	files := map[string]string{
		// sh, dash, ksh and bash without a .bash_profile read .profile.
		".profile": profiles.posix,
		// zsh reads .zprofile in a login shell, after /etc/zprofile.
		".zprofile": profiles.posix,
		// tcsh and csh start as `-ic`, so they read the interactive startup
		// file and no login file: tcsh reads .tcshrc, and BSD csh .cshrc.
		".tcshrc":          profiles.csh,
		".cshrc":           profiles.csh,
		"fish/config.fish": profiles.fish,
		"nushell/login.nu": profiles.nu,
		"elvish/rc.elv":    profiles.elvish,
		"powershell/Microsoft.PowerShell_profile.ps1": profiles.pwsh,
		// xonsh reads rc.xsh with and without the login flags. homeEnv lists it
		// in XONSHRC.
		"xonsh/rc.xsh": profiles.xonsh,
	}
	for name, body := range files {
		path := filepath.Join(home, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	}
}

// decoyProfiles puts decoyDir first on PATH in each dialect.
func decoyProfiles(decoyDir string) shellProfiles {
	return shellProfiles{
		posix:  "PATH=" + shellquote.POSIX.Quote(decoyDir) + ":\"$PATH\"; export PATH\n",
		csh:    "setenv PATH " + shellquote.POSIX.Quote(decoyDir) + "\":$PATH\"\n",
		fish:   "set --global --export PATH " + shellquote.POSIX.Quote(decoyDir) + " $PATH\n",
		nu:     "$env.PATH = ($env.PATH | split row (char esep) | prepend " + nuQuote(decoyDir) + ")\n",
		elvish: "set E:PATH = " + elvishQuote(decoyDir+":") + "$E:PATH\n",
		pwsh:   "$env:PATH = " + shellquote.PowerShell.Quote(decoyDir+":") + " + $env:PATH\n",
		xonsh:  "$PATH.insert(0, " + pythonString(decoyDir) + ")\n",
	}
}

// runWithControlCLI runs spec with the control context of cli applied after
// Wrap, as providerkit.FinalizeAgentEnv applies it to an agent, and returns
// what the program printed after the delimiter.
func runWithControlCLI(t *testing.T, spec WrapSpec, cli string, env ...string) string {
	t.Helper()
	return runWrappedWithControlCLI(t, spec, cli, env...).program
}

// wrappedOutput is what a wrapped launch printed.
type wrappedOutput struct {
	preamble string // stdout before the delimiter
	program  string // stdout after the delimiter
	stderr   string
}

// runWrappedWithControlCLI is runWithControlCLI, and it returns each part of
// the output.
func runWrappedWithControlCLI(t *testing.T, spec WrapSpec, cli string, env ...string) wrappedOutput {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd, delimiter, _ := wrapTestCommand(t, ctx, spec)
	var control []string
	if cli != "" {
		control = []string{controlenv.CLIVariable + "=" + cli}
	}
	cmd.Env = controlenv.Apply(envutil.PinEnv(cmd.Environ(), env...), control)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	require.NoError(t, err, "output: %q\nstderr: %q", output, stderr.String())
	preamble, program, found := strings.Cut(string(output), delimiter+"\n")
	require.True(t, found, "the wrapper prints its delimiter: %q\nstderr: %q", output, stderr.String())
	return wrappedOutput{preamble: preamble, program: program, stderr: stderr.String()}
}

// pathReportSpec is a launch of the test binary that reports its PATH (see
// TestControlCLIPathReport).
func pathReportSpec(t *testing.T, shell string, login bool, home string) WrapSpec {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	return WrapSpec{
		Shell: shell, LoginShell: login,
		Launch:     Spec{Program: executable},
		BaseArgs:   []string{"-test.run=^TestControlCLIPathReport$"},
		WorkingDir: home,
	}
}

// firstPathEntry returns the first entry of a PATH that the program received.
func firstPathEntry(t *testing.T, path programPath) string {
	t.Helper()
	require.True(t, path.Set, "the program receives PATH")
	entries := filepath.SplitList(path.Value)
	require.NotEmpty(t, entries, "PATH holds an entry")
	return entries[0]
}

// The step prints nothing in any dialect, to stdout or to stderr. xonsh
// prints the value of each expression statement, and PowerShell reports the
// error of a statement that fails before $ErrorActionPreference is set. A
// launch where the step runs therefore prints what a launch without the
// variable prints.
func TestWrapRunsTheControlCLIStepSilently(t *testing.T) {
	t.Parallel()
	for _, name := range controlCLIShells {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			shell := requireProfileShell(t, name, name)
			fixture := newControlCLIFixture(t)
			for _, login := range []bool{false, true} {
				t.Run("login="+strconv.FormatBool(login), func(t *testing.T) {
					home := t.TempDir()
					writeShellProfiles(t, home, decoyProfiles(fixture.decoyDir))
					spec := pathReportSpec(t, shell, login, home)
					env := append(homeEnv(home), pathReportEnv+"=1")

					without := runWrappedWithControlCLI(t, spec, "", env...)
					with := runWrappedWithControlCLI(t, spec, fixture.cli, env...)

					assert.Equal(t, without.preamble, with.preamble)
					assert.Equal(t, without.stderr, with.stderr)
					assert.Equal(t, fixture.linkDir, firstPathEntry(t, parsePathReport(t, with.program)), "the directory comes first")
				})
			}
		})
	}
}

// The case that an E2E run on macOS found: /etc/zprofile runs path_helper in
// every zsh login shell, and a profile can put any directory first. Either one
// moves the Worker's directory behind a `leapmux` of another version. The
// wrapper puts the directory first again after the profile runs, so the agent
// that it starts finds the Worker's own CLI.
func TestWrapPutsTheControlCLIFirstAfterAZshLoginProfile(t *testing.T) {
	t.Parallel()
	requireShell(t, "/bin/zsh")
	fixture := newControlCLIFixture(t)
	home := t.TempDir()
	writeShellProfiles(t, home, decoyProfiles(fixture.decoyDir))

	// The profile alone puts the decoy first, so the assertion below holds
	// only because of the wrapper.
	profileOnly := exec.Command("/bin/zsh", "-i", "-l", "-c", "leapmux")
	profileOnly.Env = controlenv.Apply(envutil.PinEnv(os.Environ(), homeEnv(home)...), []string{controlenv.CLIVariable + "=" + fixture.cli})
	profileOnly.Dir = home
	output, err := profileOnly.Output()
	require.NoError(t, err, "output: %q", output)
	require.Equal(t, "decoy\n", string(output), "the profile must move the decoy first")

	got := runWithControlCLI(t, WrapSpec{
		Shell: "/bin/zsh", LoginShell: true,
		Launch: Spec{Program: "/bin/sh"}, BaseArgs: []string{"-c", "leapmux"},
		WorkingDir: home,
	}, fixture.cli, homeEnv(home)...)

	assert.Equal(t, "worker\n", got)
}

// Every dialect puts the directory first again after its own login profile,
// on both launch paths:
//
//   - An explicit program receives the directory first on PATH, and a
//     `leapmux` that it runs is the Worker's.
//   - The candidate selection finds the Worker's `leapmux` first, so the step
//     runs before the lookup. Nushell selects through its own builder.
func TestWrapPutsTheControlCLIFirstAfterEveryLoginProfile(t *testing.T) {
	t.Parallel()
	for _, name := range controlCLIShells {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			shell := requireProfileShell(t, name, name)
			fixture := newControlCLIFixture(t)
			home := t.TempDir()
			writeShellProfiles(t, home, decoyProfiles(fixture.decoyDir))

			t.Run("program", func(t *testing.T) {
				got := runWithControlCLI(t, WrapSpec{
					Shell: shell, LoginShell: true,
					Launch:     Spec{Program: "/bin/sh"},
					BaseArgs:   []string{"-c", `printf '%s\n' "$PATH"; leapmux`},
					WorkingDir: home,
				}, fixture.cli, homeEnv(home)...)

				path, resolved, found := strings.Cut(got, "\n")
				require.True(t, found, "output: %q", got)
				entries := filepath.SplitList(path)
				require.NotEmpty(t, entries)
				assert.Equal(t, fixture.linkDir, entries[0], "the directory comes first")
				assert.Contains(t, entries, fixture.decoyDir, "the profile ran, so the step ran after it")
				assert.Equal(t, "worker\n", resolved)
			})

			t.Run("candidates", func(t *testing.T) {
				got := runWithControlCLI(t, WrapSpec{
					Shell: shell, LoginShell: true,
					Launch:     ShellCommands("leapmux-test-absent", "leapmux"),
					WorkingDir: home,
				}, fixture.cli, homeEnv(home)...)

				assert.Equal(t, "worker\n", got)
			})
		})
	}
}

// A profile that removes PATH leaves the program without PATH. The step acts
// only on a PATH that the program receives, so it leaves PATH unset too.
func TestWrapLeavesAPathThatTheProfileUnsetsUnset(t *testing.T) {
	t.Parallel()
	for _, name := range controlCLIShells {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			shell := requireProfileShell(t, name, name)
			fixture := newControlCLIFixture(t)
			home := t.TempDir()
			writeShellProfiles(t, home, shellProfiles{
				posix:  "unset PATH\n",
				csh:    "unsetenv PATH\n",
				fish:   "set --erase PATH\n",
				nu:     "hide-env PATH\n",
				elvish: "unset-env PATH\n",
				pwsh:   "Remove-Item Env:PATH\n",
				xonsh:  "del $PATH\n",
			})

			for _, cli := range []string{"", fixture.cli} {
				got := runWithControlCLI(t, pathReportSpec(t, shell, true, home), cli, append(homeEnv(home), pathReportEnv+"=1")...)
				assert.Equal(t, programPath{}, parsePathReport(t, got), "LEAPMUX_CONTROL_CLI=%q", cli)
			}
		})
	}
}

// A provider that pins PATH through SetEnv states the exact PATH that its
// program needs. The step runs before the pins in every dialect, so the
// pinned PATH reaches the program unchanged. The launch also strips a
// variable, and the statements that strip and pin it still succeed after the
// step acted.
func TestWrapLeavesAPinnedPathAsTheProviderStatesIt(t *testing.T) {
	t.Parallel()
	for _, name := range controlCLIShells {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			shell := requireProfileShell(t, name, name)
			fixture := newControlCLIFixture(t)
			home := t.TempDir()
			const pinned = "/leapmux-test-pinned:/usr/bin:/bin"
			spec := pathReportSpec(t, shell, false, home)
			spec.StripEnvKeys = []string{"LEAPMUX_TEST_STRIP"}
			spec.SetEnv = []string{"PATH=" + pinned}

			got := runWithControlCLI(t, spec, fixture.cli, append(homeEnv(home), pathReportEnv+"=1", "LEAPMUX_TEST_STRIP=1")...)

			assert.Equal(t, setPath(pinned), parsePathReport(t, got))
		})
	}
}

// bash and zsh let a profile function replace `unset`, and zsh lets one
// replace `builtin` too. The step calls each builtin past such a function, as
// the rest of the launch environment does. Under `set -a`, the shell exports
// each variable that it assigns, and the step removes its own variables
// before the program starts.
func TestWrapRunsThePosixStepPastProfileFunctions(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"bash", "zsh"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			shell := requireProfileShell(t, name, name)
			fixture := newControlCLIFixture(t)
			home := t.TempDir()
			writeShellProfiles(t, home, shellProfiles{posix: "set -a\n" +
				"unset() { /usr/bin/printf 'profile-unset\\n'; }\n" +
				"export() { /usr/bin/printf 'profile-export\\n'; }\n" +
				"builtin() { /usr/bin/printf 'profile-builtin\\n'; }\n"})

			got := runWrappedWithControlCLI(t, WrapSpec{
				Shell: shell, LoginShell: true, Launch: Spec{Program: "/usr/bin/env"}, WorkingDir: home,
			}, fixture.cli, homeEnv(home)...)

			assert.NotContains(t, got.preamble, "profile-", "no profile function runs")
			environ := strings.Split(strings.TrimSuffix(got.program, "\n"), "\n")
			paths := envutil.ValuesFor(environ, "PATH")
			require.Len(t, paths, 1, "environment: %q", got.program)
			assert.Equal(t, fixture.linkDir, firstPathEntry(t, setPath(paths[0])), "the directory comes first")
			for _, entry := range environ {
				assert.False(t, strings.HasPrefix(entry, "__lm_cli_"), "the step leaves %q", entry)
			}
		})
	}
}

// A profile can make PATH readonly, and an assignment to it then fails. dash
// and zsh end the whole command, and the other shells report the failure on
// stderr. The step leaves a readonly PATH alone: the program starts with the
// PATH of the profile, and the shell reports what it reports without the
// variable.
func TestWrapLeavesAReadonlyPathAlone(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"sh", "bash", "ash", "dash", "ksh", "zsh"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			shell := requireProfileShell(t, name, name)
			fixture := newControlCLIFixture(t)
			home := t.TempDir()
			readonly := fixture.decoyDir + ":/usr/bin:/bin"
			writeShellProfiles(t, home, shellProfiles{
				posix: "PATH=" + shellquote.POSIX.Quote(readonly) + "; export PATH; readonly PATH\n",
			})
			spec := pathReportSpec(t, shell, true, home)
			env := append(homeEnv(home), pathReportEnv+"=1")

			without := runWrappedWithControlCLI(t, spec, "", env...)
			with := runWrappedWithControlCLI(t, spec, fixture.cli, env...)

			assert.Equal(t, setPath(readonly), parsePathReport(t, with.program))
			assert.Equal(t, without.stderr, with.stderr)
		})
	}
}

// xonsh reads rc.xsh also for `-c` without the login flags, where sh reads no
// profile. The step still puts the directory first after that file moved it.
func TestWrapPutsTheControlCLIFirstAfterTheXonshRCWithoutLoginFlags(t *testing.T) {
	t.Parallel()
	shell := requireProfileShell(t, "xonsh", "xonsh")
	fixture := newControlCLIFixture(t)
	home := t.TempDir()
	writeShellProfiles(t, home, decoyProfiles(fixture.decoyDir))

	got := runWithControlCLI(t, WrapSpec{
		Shell: shell, Launch: Spec{Program: "/bin/sh"},
		BaseArgs:   []string{"-c", `printf '%s\n' "$PATH"; leapmux`},
		WorkingDir: home,
	}, fixture.cli, homeEnv(home)...)

	path, resolved, found := strings.Cut(got, "\n")
	require.True(t, found, "output: %q", got)
	entries := filepath.SplitList(path)
	require.NotEmpty(t, entries)
	assert.Equal(t, fixture.linkDir, entries[0], "the directory comes first")
	assert.Contains(t, entries, fixture.decoyDir, "rc.xsh ran, so the step ran after it")
	assert.Equal(t, "worker\n", resolved)
}

// Every dialect checks the env gate after the profile, and each one answers
// the same: a set variable withholds the gated arguments.
func TestEveryInstalledShellRunsTheEnvGate(t *testing.T) {
	t.Parallel()
	for _, name := range controlCLIShells {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			shell := requireProfileShell(t, name, name)
			home := t.TempDir()
			for _, tc := range []struct {
				gate     string
				wantMeta string
				wantArg  string
			}{
				{"on", "test_gate_open=false", ""},
				{"", "test_gate_open=true", "gated"},
			} {
				t.Run("gate="+tc.gate, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
					defer cancel()
					cmd, delimiter, metaPrefix := wrapTestCommand(t, ctx, WrapSpec{
						Shell: shell, Launch: Spec{Program: "/bin/sh"},
						BaseArgs:   []string{"-c", `printf '%s\n' "$1"`, "sh"},
						EnvGated:   testEnvGate("gated"),
						WorkingDir: home,
					})
					cmd.Env = envutil.PinEnv(cmd.Environ(), append(homeEnv(home), "LEAPMUX_TEST_GATE_A="+tc.gate, "LEAPMUX_TEST_GATE_B=")...)
					output, err := cmd.CombinedOutput()
					require.NoError(t, err, "output: %q", output)
					meta, program, found := strings.Cut(string(output), delimiter+"\n")
					require.True(t, found, "the wrapper prints its delimiter: %q", output)
					assert.Contains(t, meta, metaPrefix+tc.wantMeta)
					assert.Equal(t, tc.wantArg+"\n", program)
				})
			}
		})
	}
}
