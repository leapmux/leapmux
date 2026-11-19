//go:build unix

package launch

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/shellquote"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func useBashProfile(t *testing.T, content string) string {
	t.Helper()
	shell, err := exec.LookPath("bash")
	require.NoError(t, err)
	file := filepath.Join(t.TempDir(), "profile")
	require.NoError(t, os.WriteFile(file, []byte(content), 0o600))
	t.Setenv("BASH_ENV", file)
	return shell
}

func TestProfileCommandUsesTheAliasOrFunctionItDiscovers(t *testing.T) {
	const name = "leapmux-profile-command"
	quote := shellquote.POSIX.Quote
	for _, kind := range []string{"alias", "function"} {
		t.Run(kind, func(t *testing.T) {
			witness := filepath.Join(t.TempDir(), "invocations")
			peer := quote(profileCommandExecutable(t)) + " -test.run=^TestProfileCommandPeer$ --"
			profile := "profile_peer() { printf 'ran\\n' >> " + quote(witness) + "; " + peer + " \"$@\"; return $?; }\n"
			if kind == "alias" {
				profile += "shopt -s expand_aliases\nalias " + name + "=profile_peer\n"
			} else {
				profile += name + "() { profile_peer \"$@\"; }\n"
			}
			shell := useBashProfile(t, profile)
			bin := t.TempDir()
			testutil.WriteExecutable(t, filepath.Join(bin, name), "#!/bin/sh\nprintf 'external should not run\\n'\nexit 42\n")
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			t.Setenv(profilePeerEnv, "1")
			require.Equal(t, ProbeYes, CheckBinary(t.Context(), shell, false, name))
			require.NoFileExists(t, witness, "discovery must not invoke the wrapper")

			args := []string{"", "a b", `a"b`, `double\\backslash\`, "*.txt", "!event", "--flag", "한글"}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			spec, err := Binaries(name).Resolve(ctx, shell, false, "Profile command")
			require.NoError(t, err)
			cmd, delimiter, _ := wrapTestCommand(t, ctx, WrapSpec{Shell: shell, Launch: spec, BaseArgs: args})
			assertProfileCommandExchange(t, cmd, delimiter, args)
			data, err := os.ReadFile(witness)
			require.NoError(t, err)
			assert.Equal(t, "ran\n", string(data))
		})
	}
}

func TestProfileCandidateLaunchReadsTheProfileOnce(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "profile-count")
	shell := useBashProfile(t, "printf 'profile\\n' >> "+shellquote.POSIX.Quote(log)+"\n")
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	testutil.WriteExecutable(t, filepath.Join(dir, "leapmux-profile-second"), "#!/bin/sh\nprintf 'second\\n'\n")
	spec, err := Binaries("leapmux-profile-first", "leapmux-profile-second").Resolve(t.Context(), shell, false, "Profile command")
	require.NoError(t, err)
	cmd, delimiter, _ := wrapTestCommand(t, t.Context(), WrapSpec{Shell: shell, Launch: spec, WorkingDir: dir})
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	require.Contains(t, string(output), delimiter+"\nsecond\n")
	data, err := os.ReadFile(log)
	require.NoError(t, err)
	assert.Equal(t, "profile\n", string(data))
}

func TestProfileCandidateFindsCheckoutLocalFallback(t *testing.T) {
	const first = "leapmux-checkout-first"
	const second = "leapmux-checkout-second"
	shell := useBashProfile(t, "export PATH=./bin\n")
	checkout := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(checkout, "bin"), 0o700))
	testutil.WriteExecutable(t, filepath.Join(checkout, "bin", second), "#!/bin/sh\nprintf 'checkout fallback\\n'\n")
	locator := Binaries(first, second)
	require.Equal(t, []Resolution{Missing}, ScanAvailability(t.Context(), shell, false, []Locator{locator}))
	spec, err := locator.Resolve(t.Context(), shell, false, "Checkout command")
	require.NoError(t, err)
	cmd, delimiter, _ := wrapTestCommand(t, t.Context(), WrapSpec{Shell: shell, Launch: spec, WorkingDir: checkout})
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	assert.Contains(t, string(output), delimiter+"\ncheckout fallback\n")

	pinned := t.TempDir()
	witness := filepath.Join(pinned, "preferred-invoked")
	testutil.WriteExecutable(t, filepath.Join(pinned, first), "#!/bin/sh\nprintf 'pinned preferred\\n'\nprintf invoked > "+
		shellquote.POSIX.Quote(witness)+"\n")
	cmd, delimiter, _ = wrapTestCommand(t, t.Context(), WrapSpec{
		Shell: shell, Launch: spec, WorkingDir: checkout, SetEnv: []string{"PATH=" + pinned},
	})
	output, err = cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	_, body, found := strings.Cut(string(output), delimiter+"\n")
	require.True(t, found, "%q", output)
	assert.Equal(t, "pinned preferred\n", body)
	invoked, err := os.ReadFile(witness)
	require.NoError(t, err)
	assert.Equal(t, "invoked", string(invoked))
}

func TestProfileCandidateExhaustionReportsEveryName(t *testing.T) {
	shell := useBashProfile(t, "printf 'profile stderr' >&2\n")
	diagnostic := assertProfileCandidateExhaustion(t, shell, "leapmux-profile-absent-first", "leapmux-profile-absent-second")
	assert.Equal(t, "profile stderr\n", diagnostic)
}

func TestProgramPathFindsTheExternalBehindAProfileFunction(t *testing.T) {
	const name = "leapmux-profile-interpreter"
	witness := filepath.Join(t.TempDir(), "wrapper-called")
	shell := useBashProfile(t, name+"() { printf 'called' > "+shellquote.POSIX.Quote(witness)+"; }\n")
	t.Setenv("PATH", t.TempDir())
	require.Equal(t, ProbeYes, CheckBinary(t.Context(), shell, false, name))
	path, result := ProgramPath(t.Context(), shell, false, name)
	require.Equal(t, ProbeNo, result)
	require.Empty(t, path)
	bin := t.TempDir()
	executable := filepath.Join(bin, name)
	testutil.WriteExecutable(t, executable, "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", bin)
	path, result = ProgramPath(t.Context(), shell, false, name)
	require.Equal(t, ProbeYes, result)
	assert.Equal(t, executable, path)
	assert.NoFileExists(t, witness)
}

func TestCheckBinaryExcludesAnUnshadowedShellBuiltin(t *testing.T) {
	shell := useBashProfile(t, "")
	t.Setenv("PATH", t.TempDir())
	require.Equal(t, ProbeNo, CheckBinary(t.Context(), shell, false, "echo"))

	profile := os.Getenv("BASH_ENV")
	require.NoError(t, os.WriteFile(profile, []byte("echo() { return 0; }\n"), 0o600))
	require.Equal(t, ProbeYes, CheckBinary(t.Context(), shell, false, "echo"))
}

func TestCheckBinaryDiscardsACompletedProbeWithProcessFailure(t *testing.T) {
	const nonce = "probe-process-test"
	const name = "leapmux-probe-process"
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	testutil.WriteExecutable(t, filepath.Join(dir, name), "#!/bin/sh\nexit 0\n")
	names := []string{name}
	script := renderCommandProbe("sh", nonce, names, false)
	results, _ := parseCommandProbe(runShellProbe(t.Context(), "/bin/sh", false, script), nonce, names, false)
	require.Equal(t, ProbeYes, results[name], "the script emits a complete presence result")

	t.Run("the shell exits nonzero after emitting the completion record", func(t *testing.T) {
		output := runShellProbe(t.Context(), "/bin/sh", false, script+"exit 43\n")
		require.Empty(t, output)
	})

	t.Run("a profile helper inherits stdout after the shell exits", func(t *testing.T) {
		release := openProbeFIFO(t, filepath.Join(t.TempDir(), "release"))
		t.Cleanup(func() {
			_, err := release.WriteString("release\n")
			assert.NoError(t, err)
		})
		// The helper holds the inherited stdout until cleanup releases the FIFO.
		script := "(read -r release < " + shellquote.POSIX.Quote(release.Name()) + ") &\n" + script
		output := runShellProbe(t.Context(), "/bin/sh", false, script)
		require.Empty(t, output)
	})

	t.Run("the deadline expires after the shell starts", func(t *testing.T) {
		ready := openProbeFIFO(t, filepath.Join(t.TempDir(), "ready"))
		release := openProbeFIFO(t, filepath.Join(t.TempDir(), "release"))
		t.Cleanup(func() {
			_, err := release.WriteString("release\n")
			assert.NoError(t, err)
		})
		script := script + "printf 'ready\\n' > " + shellquote.POSIX.Quote(ready.Name()) +
			"\nread -r release < " + shellquote.POSIX.Quote(release.Name()) + "\n"
		ctx, cancel := context.WithCancelCause(t.Context())
		defer cancel(context.Canceled)
		outputs := make(chan string, 1)
		go func() { outputs <- runShellProbe(ctx, "/bin/sh", false, script) }()
		type readiness struct {
			line string
			err  error
		}
		started := make(chan readiness, 1)
		go func() {
			line, err := bufio.NewReader(ready).ReadString('\n')
			started <- readiness{line, err}
		}()
		select {
		case result := <-started:
			require.NoError(t, result.err)
			require.Equal(t, "ready\n", result.line)
		case <-testutil.DeadlineContext(t).Done():
			_, _ = ready.WriteString("stop waiting\n")
			t.Fatal("the probe did not start before the failure guard")
		}
		clock := testutil.NewQuartzMock(t)
		clock.AfterFunc(time.Second, func() { cancel(context.DeadlineExceeded) })
		clock.Advance(time.Second).MustWait(testutil.DeadlineContext(t))
		assert.ErrorIs(t, context.Cause(ctx), context.DeadlineExceeded)
		select {
		case output := <-outputs:
			require.Empty(t, output)
		case <-testutil.DeadlineContext(t).Done():
			t.Fatal("the running probe did not stop after its deadline")
		}
	})
}

func openProbeFIFO(t *testing.T, path string) *os.File {
	t.Helper()
	require.NoError(t, unix.Mkfifo(path, 0o600))
	fifo, err := os.OpenFile(path, os.O_RDWR, 0)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, fifo.Close()) })
	return fifo
}

func TestProfileCandidateBashHonorsRunnableHashEntries(t *testing.T) {
	const interpreter = "leapmux-profile-hashed"
	for _, tc := range []struct {
		name       string
		hashExists bool
		pathExists bool
		want       string
	}{
		{name: "live hash outside PATH", hashExists: true, want: "hash"},
		{name: "live hash before PATH", hashExists: true, pathExists: true, want: "hash"},
		{name: "stale hash falls back to PATH", pathExists: true, want: "path"},
		{name: "stale hash alone", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hashed := filepath.Join(t.TempDir(), interpreter)
			onPath := filepath.Join(t.TempDir(), interpreter)
			if tc.hashExists {
				testutil.WriteExecutable(t, hashed, "#!/bin/sh\nprintf 'hash\\n'\n")
			}
			if tc.pathExists {
				testutil.WriteExecutable(t, onPath, "#!/bin/sh\nprintf 'path\\n'\n")
			}
			shell := useBashProfile(t, "hash -p "+shellquote.POSIX.Quote(hashed)+" "+interpreter+"\n")
			t.Setenv("PATH", filepath.Dir(onPath))

			path, result := ProgramPath(t.Context(), shell, false, interpreter)
			cmd, delimiter, _ := wrapTestCommand(t, t.Context(), WrapSpec{Shell: shell, Launch: ShellCommands(interpreter)})
			output, err := cmd.CombinedOutput()
			if tc.want == "" {
				assert.Equal(t, ProbeNo, CheckBinary(t.Context(), shell, false, interpreter))
				assert.Equal(t, ProbeNo, result)
				assert.Empty(t, path)
				var exit *exec.ExitError
				require.ErrorAs(t, err, &exit)
				assert.Equal(t, 127, exit.ExitCode())
				assert.NotContains(t, string(output), delimiter)
				return
			}
			assert.Equal(t, ProbeYes, CheckBinary(t.Context(), shell, false, interpreter))
			assert.Equal(t, ProbeYes, result)
			wantPath := hashed
			if !tc.hashExists {
				wantPath = onPath
			}
			assert.Equal(t, wantPath, path)
			require.NoError(t, err, "%s", output)
			assert.Contains(t, string(output), delimiter+"\n"+tc.want+"\n")
		})
	}
}

func TestProfileCommandBashProtectsFramingFromProfileFunctions(t *testing.T) {
	const name = "leapmux-profile-marker"
	witness := filepath.Join(t.TempDir(), "marker-intercepted")
	shell := useBashProfile(t, "builtin() { if [[ $1 == printf ]]; then echo intercepted >> "+
		shellquote.POSIX.Quote(witness)+"; return 0; fi; command builtin \"$@\"; }\n"+
		"printf() { echo intercepted >> "+shellquote.POSIX.Quote(witness)+"; }\nreadonly -f builtin printf\n"+
		"echo() { command echo intercepted >> "+shellquote.POSIX.Quote(witness)+"; }\n"+
		"command printf 'profile banner'\n"+
		"command() { /bin/echo intercepted-command >> "+shellquote.POSIX.Quote(witness)+"; }\nreadonly -f command\n")
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	testutil.WriteExecutable(t, filepath.Join(dir, name), "#!/bin/sh\nprintf 'provider started\\n'\n")

	assert.Equal(t, ProbeYes, CheckBinary(t.Context(), shell, false, name))
	for _, target := range []Spec{ShellCommands(name), {Program: filepath.Join(dir, name)}} {
		cmd, delimiter, prefix := wrapTestCommand(t, t.Context(), WrapSpec{
			Shell: shell, Launch: target, EnvGated: testEnvGate("optional"),
		})
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", output)
		assert.Contains(t, string(output), "\n"+prefix+"test_gate_open=true\n")
		assert.Contains(t, string(output), "\n"+delimiter+"\nprovider started\n")
	}
	assert.NoFileExists(t, witness)
}

func TestProfileCommandBashUsesNativeLookupWithABuiltinFunction(t *testing.T) {
	const candidate = "leapmux-bash-profile"
	quote := shellquote.POSIX.Quote
	dir := t.TempDir()
	external := filepath.Join(dir, candidate)
	decoy := filepath.Join(dir, "decoy")
	testutil.WriteExecutable(t, external, "#!/bin/sh\nprintf 'external\\n'\n")
	testutil.WriteExecutable(t, decoy, "#!/bin/sh\nprintf 'decoy\\n'\n")
	profile := "builtin() {\n" +
		"case \"$1\" in\n" +
		"type) printf 'builtin\\n';;\n" +
		"hash) printf '%s\\n' " + quote(decoy) + ";;\n" +
		"*) printf 'profile builtin\\n';;\n" +
		"esac\n}\n" +
		"readonly -f builtin\n" +
		"command() { printf 'profile command\\n'; }\nreadonly -f command\n" +
		"eval() { printf 'profile eval\\n'; }\n" +
		"leapmux_wrapper() {\n" +
		"case :$SHELLOPTS: in *:posix:*) printf 'posix\\n';; *) printf 'bash\\n'; eval from-wrapper;; esac\n" +
		"builtin from-wrapper; command from-wrapper; printf '<%s>\\n' \"$@\"; return 37\n}\n"
	for _, test := range []struct {
		name    string
		startup string
		want    string
	}{
		{"function", candidate + "() { leapmux_wrapper \"$@\"; }\n", "bash\nprofile eval\n"},
		{"alias", "alias " + candidate + "=leapmux_wrapper\n", "bash\nprofile eval\n"},
		{"posix alias", "set -o posix\nalias " + candidate + "=leapmux_wrapper\n", "posix\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			shell := useBashProfile(t, profile+test.startup)
			t.Setenv("PATH", dir)
			path, result := ProgramPath(t.Context(), shell, false, candidate)
			assert.Equal(t, ProbeYes, result)
			assert.Equal(t, external, path)
			cmd, delimiter, _ := wrapTestCommand(t, t.Context(), WrapSpec{
				Shell: shell, Launch: ShellCommands(candidate), BaseArgs: []string{"", "a b"},
			})
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			require.ErrorAs(t, err, &exit, "%s", output)
			assert.Equal(t, 37, exit.ExitCode())
			assert.Equal(t, "\n"+delimiter+"\n"+test.want+"profile builtin\nprofile command\n<>\n<a b>\n", string(output))
		})
	}
	t.Run("external", func(t *testing.T) {
		shell := useBashProfile(t, profile)
		t.Setenv("PATH", dir)
		cmd, delimiter, _ := wrapTestCommand(t, t.Context(), WrapSpec{Shell: shell, Launch: ShellCommands(candidate)})
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", output)
		assert.Equal(t, "\n"+delimiter+"\nexternal\n", string(output))
	})
}

func TestProfileCommandRecordsIgnoreProfileShadows(t *testing.T) {
	const present = "leapmux-marker-peer"
	const missing = "leapmux-missing-marker-peer"
	for _, shellName := range []string{"zsh", "sh", "dash", "nu", "xonsh"} {
		t.Run(shellName, func(t *testing.T) {
			shell := requireProfileShell(t, shellName, shellName)
			home := t.TempDir()
			t.Setenv("HOME", home)
			bin, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			executable := filepath.Join(bin, present)
			testutil.WriteExecutable(t, executable, "#!/bin/sh\nprintf 'provider payload\\n'\nexit 37\n")

			profile := filepath.Join(home, ".profile")
			body := "/usr/bin/printf 'profile banner'\nPATH=" + shellquote.POSIX.Quote(bin) + "\nexport PATH\n"
			switch shellName {
			case "zsh":
				t.Setenv("ZDOTDIR", home)
				profile = filepath.Join(home, ".zshrc")
				body += "builtin() { /bin/echo intercepted-builtin; }\nunset() { /bin/echo intercepted-unset; }\n" +
					"unfunction() { /bin/echo intercepted-unfunction; }\nfunction /usr/bin/printf { /bin/echo intercepted-formatter; }\n"
			case "sh", "dash":
				body += "command() { /bin/echo intercepted-command; }\n"
			case "nu":
				t.Setenv("XDG_CONFIG_HOME", home)
				profile = filepath.Join(home, "nushell", "config.nu")
				body = "^/usr/bin/printf 'profile banner'\n$env.PATH = [" + nuQuote(bin) + "]\n" +
					"def print [--stderr (-e), ...args: any] { ^/bin/echo intercepted-print }\n" +
					"def echo [...args: any] { ^/bin/echo intercepted-echo }\n"
			case "xonsh":
				t.Setenv("TERM", "xterm-256color")
				profile = filepath.Join(home, "profile.xsh")
				t.Setenv("XONSHRC", profile)
				body = "import sys\nsys.stdout.write('profile banner')\nsys.stdout.flush()\n$PATH = [" +
					pythonString(bin) + "]\ndef print(*args, **kwargs):\n    return None\n"
			}
			require.NoError(t, os.MkdirAll(filepath.Dir(profile), 0o700))
			require.NoError(t, os.WriteFile(profile, []byte(body), 0o600))

			assert.Equal(t, map[string]ProbeResult{present: ProbeYes, missing: ProbeNo},
				checkBinaries(t.Context(), shell, true, []string{present, missing}))
			path, result := ProgramPath(t.Context(), shell, true, present)
			assert.Equal(t, ProbeYes, result)
			assert.Equal(t, executable, path)
			for _, target := range []Spec{ShellCommands(missing, present), {Program: executable}} {
				cmd, delimiter, prefix := wrapTestCommand(t, t.Context(), WrapSpec{
					Shell: shell, LoginShell: true, Launch: target, EnvGated: testEnvGate("optional"),
				})
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				output, err := cmd.Output()
				var exit *exec.ExitError
				require.ErrorAs(t, err, &exit, "%s", output)
				assert.Equal(t, 37, exit.ExitCode(), "%s", output)
				assert.Contains(t, string(output), "profile banner\n"+prefix+"test_gate_open=true\n"+delimiter+"\nprovider payload\n")
				assert.NotContains(t, string(output), "intercepted-")
				assert.NotContains(t, stderr.String(), "intercepted-")
			}
			prepared, err := Wrap(t.Context(), WrapSpec{Shell: shell, LoginShell: true, Launch: ShellCommands(missing)})
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, prepared.Close()) })
			diagnostics := prepared.NewDiagnostics(1024)
			prepared.Cmd.Stderr = diagnostics
			out, status := prepared.Cmd.Output()
			diagnostics.Finish()
			assert.NotContains(t, string(out), prepared.Delimiter)
			assert.ErrorIs(t, diagnostics.ExitError(status, false), ErrNoCandidate)
		})
	}
}

func TestProfileCandidateXonshUsesTheCurrentPath(t *testing.T) {
	shell := requireProfileShell(t, "xonsh", "xonsh")
	t.Setenv("TERM", "xterm-256color")
	const removed = "leapmux-xonsh-removed"
	const fallback = "leapmux-xonsh-fallback"
	stale := t.TempDir()
	live := t.TempDir()
	for _, candidate := range []string{removed, fallback} {
		testutil.WriteExecutable(t, filepath.Join(stale, candidate), "#!/bin/sh\nprintf 'stale executable selected\\n'\n")
	}
	want := filepath.Join(live, fallback)
	testutil.WriteExecutable(t, want, "#!/bin/sh\nprintf 'live executable selected\\n'\n")
	profile := filepath.Join(t.TempDir(), "profile.xsh")
	t.Setenv("XONSHRC", profile)
	// Prime both names before changing PATH. Assert the primed results in the
	// profile so a fixture that never reaches the cache cannot pass.
	body := "import os\n$PATH = " + pythonArguments([]string{stale, live}) + "\n" +
		"assert os.path.samefile(__xonsh__.commands_cache.locate_binary(" + pythonString(removed) + "), " + pythonString(filepath.Join(stale, removed)) + ")\n" +
		"assert os.path.samefile(__xonsh__.commands_cache.locate_binary(" + pythonString(fallback) + "), " + pythonString(filepath.Join(stale, fallback)) + ")\n"
	for _, test := range []struct {
		name      string
		after     string
		pinnedEnv []string
	}{
		{"profile removes the stale directory", "$PATH = " + pythonArguments([]string{live}) + "\n", nil},
		{"profile reorders the directories", "$PATH = " + pythonArguments([]string{live, stale}) + "\n", nil},
		{"launch pins PATH after the profile", "", []string{"PATH=" + live}},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.NoError(t, os.WriteFile(profile, []byte(body+test.after), 0o600))
			if len(test.pinnedEnv) == 0 {
				path, result := ProgramPath(t.Context(), shell, true, fallback)
				assert.Equal(t, ProbeYes, result)
				assert.Equal(t, want, path)
				if test.name == "profile removes the stale directory" {
					path, result := ProgramPath(t.Context(), shell, true, removed)
					assert.Equal(t, ProbeNo, result)
					assert.Empty(t, path)
				}
			}
			candidates := ShellCommands(removed, fallback)
			if test.name == "profile reorders the directories" {
				candidates = ShellCommands(fallback)
			}
			cmd, delimiter, _ := wrapTestCommand(t, t.Context(), WrapSpec{
				Shell: shell, LoginShell: true, Launch: candidates, SetEnv: test.pinnedEnv,
			})
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", output)
			require.NotContains(t, string(output), "AssertionError", "the real profile must prime the command cache")
			assert.Contains(t, string(output), delimiter+"\nlive executable selected\n")
			assert.NotContains(t, string(output), "stale executable selected")
		})
	}
}

func TestLaunchLocatorResolveDoesNotStartTheShell(t *testing.T) {
	dir := t.TempDir()
	shell := filepath.Join(dir, "shell")
	witness := filepath.Join(dir, "profile-started")
	testutil.WriteExecutable(t, shell, "#!/bin/sh\nprintf started > "+shellquote.POSIX.Quote(witness)+"\nexec /bin/sh \"$@\"\n")
	name := "leapmux-resolve-profile"
	testutil.WriteExecutable(t, filepath.Join(dir, name), "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", dir)
	locator := Binaries("leapmux-resolve-missing", name)
	require.Equal(t, []Resolution{Found}, ScanAvailability(t.Context(), shell, false, []Locator{locator}))
	require.FileExists(t, witness)
	require.NoError(t, os.Remove(witness))
	spec, err := locator.Resolve(t.Context(), shell, false, "Test provider")
	require.NoError(t, err)
	assert.Equal(t, ShellCommands("leapmux-resolve-missing", name), spec)
	assert.NoFileExists(t, witness)
}

func TestProfileCommandCshLookupPreservesTheSelectedWrappersAliases(t *testing.T) {
	const name = "leapmux_profile_csh_alias"
	for _, shellName := range []string{"csh", "tcsh"} {
		t.Run(shellName, func(t *testing.T) {
			shell := requireProfileShell(t, shellName, shellName)
			home := t.TempDir()
			t.Setenv("HOME", home)
			profile := ".cshrc"
			if shellName == "tcsh" {
				profile = ".tcshrc"
			}
			executable := filepath.Join(home, name)
			testutil.WriteExecutable(t, executable, "#!/bin/sh\nprintf 'external\\n'\n")
			content := "set path = (" + cshQuote(home) + ")\n" +
				"alias echo '/bin/echo -n'\n" +
				"alias which '/bin/echo hidden-which'\n" +
				"alias eval '/bin/echo hidden-eval'\n" +
				"alias source '/bin/echo hidden-source'\n" +
				"alias " + name + " 'echo wrapper; which lookup; eval dispatch; source fixture'\n"
			require.NoError(t, os.WriteFile(filepath.Join(home, profile), []byte(content), 0o600))

			assert.Equal(t, ProbeYes, CheckBinary(t.Context(), shell, true, name))
			path, result := ProgramPath(t.Context(), shell, true, name)
			assert.Equal(t, ProbeYes, result)
			assert.Equal(t, executable, path)
			cmd, delimiter, _ := wrapTestCommand(t, t.Context(), WrapSpec{
				Shell: shell, LoginShell: true, Launch: ShellCommands("leapmux_missing_csh_alias", name),
			})
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", output)
			assert.Contains(t, string(output), delimiter+"\nwrapperhidden-which lookup\nhidden-eval dispatch\nhidden-source fixture\n")
		})
	}
}

func TestProfileCandidateStopsWhenPosixEnvironmentSetupFails(t *testing.T) {
	const name = "leapmux_profile_readonly"
	for _, operation := range []string{"strip", "pin"} {
		t.Run(operation, func(t *testing.T) {
			dir := t.TempDir()
			shell := useBashProfile(t, "export LEAPMUX_PROFILE_READONLY=original\nreadonly LEAPMUX_PROFILE_READONLY\n"+
				name+"() { printf 'provider received %s\\n' \"$LEAPMUX_PROFILE_READONLY\"; printf 'called' > "+
				shellquote.POSIX.Quote(filepath.Join(dir, "provider-invoked"))+"; }\n")
			spec := WrapSpec{Shell: shell, Launch: ShellCommands(name)}
			if operation == "strip" {
				spec.StripEnvKeys = []string{"LEAPMUX_PROFILE_READONLY"}
			} else {
				spec.SetEnv = []string{"LEAPMUX_PROFILE_READONLY=replacement"}
			}
			cmd, delimiter, _ := wrapTestCommand(t, t.Context(), spec)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			require.ErrorAs(t, err, &exit, "%s", output)
			assert.NotEqual(t, 127, exit.ExitCode(), "candidate exhaustion has a separate status")
			assert.Contains(t, string(output), "readonly variable")
			assert.NotContains(t, string(output), delimiter)
			assert.NoFileExists(t, filepath.Join(dir, "provider-invoked"))
		})
	}
}

func TestProfileCommandDialectMatrix(t *testing.T) {
	for _, name := range []string{"bash", "zsh", "sh", "ash", "dash", "ksh", "fish", "csh", "tcsh", "nu", "xonsh", "pwsh"} {
		t.Run(name, func(t *testing.T) {
			shell := requireProfileShell(t, name, name)
			profile := t.TempDir()
			witness := filepath.Join(profile, "profile-read")
			program := profileCommandExecutable(t)
			installDialectProfile(t, name, profile, program, witness)
			t.Setenv(profilePeerEnv, "1")
			t.Setenv(profilePeerStripEnv, "strip from child and gate")
			t.Setenv(profilePeerLiteralEnv, "overwrite in child and gate")

			const candidate = "leapmux_profile_matrix"
			locator := Binaries("leapmux_profile_matrix_missing", candidate)
			const login = true
			profileReads := func(t *testing.T) {
				t.Helper()
				reads, err := os.ReadFile(witness)
				require.NoError(t, err)
				require.Equal(t, "profile\n", string(reads))
				require.NoError(t, os.Remove(witness))
			}
			require.Equal(t, []Resolution{Found}, ScanAvailability(t.Context(), shell, login, []Locator{locator}))
			profileReads(t)
			logUnixProfileShell(t, shell, login)
			spec, err := locator.Resolve(t.Context(), shell, login, "Shell matrix")
			require.NoError(t, err)

			for _, tc := range []struct {
				name             string
				gateEnv          string
				wantOptionalArgs bool
			}{
				{name: "stripped value allows optional argv", gateEnv: profilePeerStripEnv, wantOptionalArgs: true},
				{name: "pinned value withholds optional argv", gateEnv: profilePeerLiteralEnv},
			} {
				t.Run(tc.name, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
					defer cancel()
					args := []string{"", "a b", `a"b`, "it's", `two\\slashes\`, "*.txt", "!event", "--flag", "한글"}
					optionalArgs := []string{"--model", `literal # ! \ ' "$HOME" [x] *`}
					literal := `/not a path/'quote'\tail ! $HOME *.txt 한글\`
					cmd, delimiter, _ := wrapTestCommand(t, ctx, WrapSpec{
						Shell: shell, LoginShell: login, Launch: spec, BaseArgs: args,
						StripEnvKeys: []string{profilePeerStripEnv}, SetEnv: []string{profilePeerLiteralEnv + "=" + literal},
						EnvGated: &EnvGatedArgs{EnvVars: []string{tc.gateEnv}, MetaKey: "optional_argv", Args: optionalArgs},
					})
					wantArgs := slices.Clone(args)
					if tc.wantOptionalArgs {
						wantArgs = append(wantArgs, optionalArgs...)
					}
					assertProfileCommandExchangeWithEnvironment(t, cmd, delimiter, wantArgs, &profilePeerEnvironment{Literal: &literal})
					profileReads(t)
				})
			}

			stopCtx, stop := context.WithTimeout(t.Context(), 30*time.Second)
			defer stop()
			stopped, _, _ := wrapTestCommand(t, stopCtx, WrapSpec{Shell: shell, LoginShell: login, Launch: spec})
			assertProfileCommandCancellation(t, stopCtx, stop, stopped, true)
			profileReads(t)
		})
	}
}

func logUnixProfileShell(t *testing.T, shell string, login bool) {
	t.Helper()
	executable, err := filepath.EvalSymlinks(shell)
	require.NoError(t, err)
	version, versionErr := exec.Command(shell, "--version").CombinedOutput()
	if versionErr != nil && filepath.Base(executable) == "busybox" {
		version, versionErr = exec.Command(executable).CombinedOutput()
	}
	identity, _, _ := strings.Cut(strings.TrimSpace(string(version)), "\n")
	// Some sh implementations expose no version flag. Record the hash of the
	// resolved executable and the kernel build where it ran.
	if versionErr != nil && (identity == "" || !strings.HasPrefix(identity, "version ")) {
		binary, err := os.ReadFile(executable)
		require.NoError(t, err)
		build, err := exec.Command("uname", "-v").Output()
		require.NoError(t, err)
		identity = "sha256 " + fmt.Sprintf("%x", sha256.Sum256(binary)) + " on " + strings.TrimSpace(string(build))
	}
	require.NotEmpty(t, identity)
	flags := terminal.CommandArgs(shell, login, "")
	t.Logf("%s/%s %s (%s), %s, argv %q", runtime.GOOS, runtime.GOARCH, shell, executable, identity, flags[:len(flags)-1])
}

func installDialectProfile(t *testing.T, shell, dir, peer, witness string) {
	t.Helper()
	const candidate = "leapmux_profile_matrix"
	const helperArgs = "-test.run=^TestProfileCommandPeer --"
	posix := shellquote.POSIX.Quote
	t.Setenv("HOME", dir)
	t.Setenv("TERM", "dumb")
	file := filepath.Join(dir, ".profile")
	body := "printf 'profile\\n' >> " + posix(witness) + "\n" +
		candidate + "() { " + posix(peer) + " " + helperArgs + " \"$@\"; return $?; }\n"
	switch shell {
	case "bash":
		t.Setenv("BASH_ENV", "")
		file = filepath.Join(dir, ".bash_profile")
	case "zsh":
		t.Setenv("ZDOTDIR", dir)
		file = filepath.Join(dir, ".zprofile")
	case "fish":
		t.Setenv("XDG_CONFIG_HOME", dir)
		file = filepath.Join(dir, "fish", "config.fish")
		body = "builtin printf 'profile\\n' >> " + fishQuote(witness) + "\nfunction " + candidate + "\n" + fishQuote(peer) + " '-test.run=^TestProfileCommandPeer$' -- $argv\nreturn $status\nend\n"
	case "csh", "tcsh":
		file = filepath.Join(dir, ".cshrc")
		if shell == "tcsh" {
			file = filepath.Join(dir, ".tcshrc")
		}
		body = "echo profile >> " + cshQuote(witness) + "\nalias " + candidate + " '" + peer + " " + helperArgs + " \\!*'\n"
	case "nu":
		t.Setenv("XDG_CONFIG_HOME", dir)
		file = filepath.Join(dir, "nushell", "config.nu")
		body = "\"profile\\n\" | save --append " + nuQuote(witness) + "\ndef --wrapped " + candidate + " [...arguments: string] { ^" + nuQuote(peer) + " '-test.run=^TestProfileCommandPeer$' '--' ...$arguments }\n"
	case "xonsh":
		t.Setenv("TERM", "xterm-256color")
		file = filepath.Join(dir, "profile.xsh")
		t.Setenv("XONSHRC", file)
		body = "import subprocess\nwith open(" + pythonString(witness) + ", 'a') as witness:\n    witness.write('profile\\n')\n" +
			"def profile_peer(args):\n    return subprocess.call([" + pythonString(peer) + ", '-test.run=^TestProfileCommandPeer$', '--', *args])\n" +
			"aliases[" + pythonString(candidate) + "] = profile_peer\n"
	case "pwsh":
		t.Setenv("XDG_CONFIG_HOME", dir)
		file = filepath.Join(dir, "powershell", "Microsoft.PowerShell_profile.ps1")
		quote := shellquote.PowerShell.Quote
		body = "[System.IO.File]::AppendAllText(" + quote(witness) + ", \"profile`n\")\n" +
			"$PSNativeCommandArgumentPassing = 'Standard'\n" +
			"function " + candidate + " { & " + quote(peer) + " '-test.run=^TestProfileCommandPeer$' '--' @args }\n"
	default:
		t.Setenv("ENV", "")
	}
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o700))
	require.NoError(t, os.WriteFile(file, []byte(body), 0o600))
}

func TestProfileCommandNushellDirectProgramKeepsStreams(t *testing.T) {
	shell := requireProfileShell(t, "nu", "nu")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(profilePeerEnv, "1")
	args := []string{"", "one argument", "한글"}
	cmd, delimiter, _ := wrapTestCommand(t, t.Context(), WrapSpec{
		Shell:      shell,
		LoginShell: true,
		Launch:     Spec{Program: profileCommandExecutable(t), PrefixArgs: []string{"-test.run=^TestProfileCommandPeer$", "--"}},
		BaseArgs:   args,
	})
	assertProfileCommandExchange(t, cmd, delimiter, args)
}

func TestProfileCandidateFailureDoesNotInvokeTheFallback(t *testing.T) {
	dir := t.TempDir()
	fallback := filepath.Join(dir, "fallback")
	shell := useBashProfile(t, "leapmux_failing_candidate() { printf 'wrapper started\\n'; return 127; }\n")
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	testutil.WriteExecutable(t, filepath.Join(dir, "leapmux_fallback_candidate"),
		"#!/bin/sh\nprintf 'started' > "+shellquote.POSIX.Quote(fallback)+"\n")
	prepared, err := Wrap(t.Context(), WrapSpec{
		Shell: shell, Launch: ShellCommands("leapmux_failing_candidate", "leapmux_fallback_candidate"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, prepared.Close()) })
	diagnostics := prepared.NewDiagnostics(1024)
	prepared.Cmd.Stderr = diagnostics
	output, err := prepared.Cmd.Output()
	diagnostics.Finish()
	require.Contains(t, string(output), prepared.Delimiter+"\nwrapper started\n")
	var nativeErr *exec.ExitError
	require.ErrorAs(t, diagnostics.ExitError(err, true), &nativeErr)
	assert.Equal(t, 127, nativeErr.ExitCode())
	assert.NotErrorIs(t, diagnostics.ExitError(err, true), ErrNoCandidate)
	assert.NoFileExists(t, fallback)
}

func TestProfileCandidateNushellDefersAnUnselectedSignature(t *testing.T) {
	shell := requireProfileShell(t, "nu", "nu")
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	bin := filepath.Join(dir, "bin")
	require.NoError(t, os.Mkdir(bin, 0o700))
	t.Setenv("PATH", bin)
	const preferred = "leapmux-nu-preferred"
	const incompatible = "leapmux-nu-incompatible"
	file := filepath.Join(dir, "nushell", "config.nu")
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o700))
	require.NoError(t, os.WriteFile(file, []byte("def "+incompatible+" [] { exit 48 }\n"), 0o600))
	testutil.WriteExecutable(t, filepath.Join(bin, preferred), "#!/bin/sh\nprintf 'preferred %s\\n' \"$1\"\n")
	cmd, delimiter, _ := wrapTestCommand(t, t.Context(), WrapSpec{
		Shell: shell, LoginShell: true, Launch: ShellCommands(preferred, incompatible), BaseArgs: []string{"--stdio"},
	})
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	assert.Contains(t, string(output), delimiter+"\npreferred --stdio\n")
}

func TestProfileCandidatePowerShellSkipsUnresolvableAliases(t *testing.T) {
	shell, home := testutil.RequirePowerShellArgumentPassing(t, "Standard", "LEAPMUX_PS_PROFILE_MODE")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	appendPowerShellProfile(t, home, "Set-Alias -Name leapmux-dangling -Value leapmux-missing-test-target\n"+
		"Set-Alias -Name leapmux-cycle-a -Value leapmux-cycle-b\n"+
		"Set-Alias -Name leapmux-cycle-b -Value leapmux-cycle-a\n")
	const fallback = "leapmux-successful"
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	require.NoError(t, os.Symlink(profileCommandExecutable(t), filepath.Join(bin, fallback)))
	t.Setenv(profilePeerEnv, "1")
	for _, earlier := range []string{"leapmux-dangling", "leapmux-cycle-a"} {
		t.Run(earlier, func(t *testing.T) {
			require.Equal(t, ProbeNo, CheckBinary(t.Context(), shell, false, earlier))
			locator := Binaries(earlier, fallback)
			require.Equal(t, []Resolution{Found}, ScanAvailability(t.Context(), shell, false, []Locator{locator}))
			spec, err := locator.Resolve(t.Context(), shell, false, "PowerShell aliases")
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			args := []string{"", "--stdio", "literal fallback argv"}
			prefix := []string{"-test.run=^TestProfileCommandPeer$", "--"}
			cmd, delimiter, _ := wrapTestCommand(t, ctx, WrapSpec{
				Shell: shell, Launch: spec, BaseArgs: append(prefix, args...),
			})
			assertProfileCommandExchange(t, cmd, delimiter, args)
		})
	}
}

func TestProfileCandidateCanReplaceThePosixShellWithAnExecutable(t *testing.T) {
	for _, name := range []string{"bash", "zsh", "sh", "ash", "dash", "ksh"} {
		t.Run(name, func(t *testing.T) {
			shell := requireProfileShell(t, name, name)
			dir := t.TempDir()
			t.Setenv("PATH", dir)
			t.Setenv("ZDOTDIR", dir)
			t.Setenv("BASH_ENV", "")
			t.Setenv("ENV", "")
			const candidate = "leapmux_test_external"
			testutil.WriteExecutable(t, filepath.Join(dir, candidate), "#!/bin/sh\nprintf 'external started\\n'\nexit 37\n")
			spec, err := Binaries("leapmux_other_external", candidate).Resolve(t.Context(), shell, false, "Profile command")
			require.NoError(t, err)
			cmd, delimiter, _ := wrapTestCommand(t, t.Context(), WrapSpec{Shell: shell, Launch: spec})
			output, err := cmd.CombinedOutput()
			var exitErr *exec.ExitError
			require.ErrorAs(t, err, &exitErr)
			assert.Equal(t, 37, exitErr.ExitCode(), "%s", output)
			assert.Contains(t, string(output), delimiter+"\nexternal started\n")
		})
	}
}

func TestProfileCommandZshFindsAnAliasWithoutAnExternalPrintf(t *testing.T) {
	shell := requireProfileShell(t, "zsh", "zsh")
	const name = "leapmux-zsh-alias"
	for _, test := range []struct {
		name, profile, want string
	}{
		{"ordinary alias", "", "chosen"},
		{"preserves builtin function", "builtin() { /bin/echo wrapper builtin; }\n", "wrapper builtin"},
		{"bypasses eval function", "eval() { /bin/echo intercepted-eval; }\n", "chosen"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("ZDOTDIR", dir)
			t.Setenv("PATH", dir)
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".zshenv"),
				[]byte(test.profile+"alias "+name+"='builtin print -r -- chosen'\n"), 0o600))
			require.Equal(t, ProbeYes, CheckBinary(t.Context(), shell, false, name))
			cmd, delimiter, _ := wrapTestCommand(t, t.Context(), WrapSpec{Shell: shell, Launch: ShellCommands(name)})
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", output)
			assert.Contains(t, string(output), delimiter+"\n"+test.want+"\n")
		})
	}
}
