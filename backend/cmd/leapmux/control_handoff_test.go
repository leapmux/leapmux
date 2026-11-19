package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cmdcontrol "github.com/leapmux/leapmux/internal/cli/control/cmd"
	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

// handoffCall is what one call of controlHandoff.exec received.
type handoffCall struct {
	target string
	argv   []string
	env    []string
}

// handoffHarness is a hand-off whose exec records each call, with the
// executables that a test points it at.
type handoffHarness struct {
	// self is the running executable, as os.Executable reports it.
	self string
	// target is the Worker's CLI. It is another file with the same bytes, as
	// is the copy that a Worker makes when it can make no link. Each hand-off
	// in these tests therefore shows that file identity decides, not content.
	target string
	calls  []handoffCall
	stderr bytes.Buffer
	// execCode and execErr are what exec returns.
	execCode int
	execErr  error
}

func newHandoffHarness(t *testing.T) *handoffHarness {
	t.Helper()
	dir := t.TempDir()
	h := &handoffHarness{
		self:     filepath.Join(dir, "installed", "leapmux"),
		target:   filepath.Join(dir, "cli-1", "leapmux"),
		execCode: 7,
	}
	for _, path := range []string{h.self, h.target} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		testutil.WriteExecutable(t, path, "#!/bin/sh\nexit 0\n")
	}
	return h
}

// tabEnv is the environment of a process inside a tab whose Worker linked
// cli. Each entry of overrides replaces the variable that it sets, as in the
// deduplicated environment that os.Environ returns.
func tabEnv(cli string, overrides ...string) []string {
	return envutil.PinEnv([]string{
		"HOME=/home/u",
		controlenv.SocketVariable + "=unix:/run/leapmux/control.sock",
		controlenv.TokenVariable + "=token-1",
		controlenv.CLIVariable + "=" + cli,
	}, overrides...)
}

// run runs the hand-off of args in environ, and returns its answer.
func (h *handoffHarness) run(environ []string, args ...string) (int, bool) {
	return h.runAs(func() (string, error) { return h.self, nil }, environ, args...)
}

func (h *handoffHarness) runAs(executable func() (string, error), environ []string, args ...string) (int, bool) {
	handoff := controlHandoff{
		environ:    environ,
		executable: executable,
		exec: func(target string, argv, env []string) (int, error) {
			h.calls = append(h.calls, handoffCall{target: target, argv: argv, env: env})
			return h.execCode, h.execErr
		},
		stderr: &h.stderr,
	}
	return handoff.run(args)
}

func TestControlHandoffHandsAControlCommandToTheWorkerCLI(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"control", "tab", "list"},
		{"control"},
		{"control", "--help"},
		{"control", "agent", "send", "87a19d5f", "--", "-v"},
		{"control", "a-command-of-a-newer-release", "--a-flag-of-a-newer-release"},
		{"cont", "tab", "list"},
		{"--help", "control", "tab"},
		{"help", "control", "tab"},
		{"__complete", "control", "tab", ""},
		{"__completeNoDesc", "control", "wo"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			h := newHandoffHarness(t)
			environ := tabEnv(h.target)

			code, handled := h.run(environ, args...)

			assert.True(t, handled)
			assert.Equal(t, 7, code, "the exit code is the Worker CLI's")
			require.Len(t, h.calls, 1)
			assert.Equal(t, h.target, h.calls[0].target)
			assert.Equal(t, append([]string{h.target}, args...), h.calls[0].argv,
				"the Worker's CLI receives the arguments as typed, and parses them itself")
			assert.Equal(t, append(environ, controlHandoffMarker+"=1"), h.calls[0].env,
				"the Worker's CLI inherits the environment, with the marker that stops a second hand-off")
			assert.Empty(t, h.stderr.String())
		})
	}
}

// Every other command describes or acts on the binary that the user ran. The
// target does not exist here, and no warning proves that the hand-off never
// looked at it.
func TestControlHandoffLeavesEveryOtherCommandHere(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		nil,
		{"worker", "run"},
		{"solo", "start"},
		{"recover", "user"},
		{"version"},
		{"--version"},
		{"commands", "--json"},
		{"completion", "zsh"},
		{"help"},
		{"help", "worker"},
		{"--help"},
		{"__complete", "control"},
		{"__complete", "worker", ""},
		{"c", "tab", "list"},
		{"controlx", "tab"},
		{"--unknown-flag", "control", "tab"},
		{"--", "control"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			h := newHandoffHarness(t)

			_, handled := h.run(tabEnv(filepath.Join(t.TempDir(), "missing", "leapmux")), args...)

			assert.False(t, handled)
			assert.Empty(t, h.calls)
			assert.Empty(t, h.stderr.String())
		})
	}
}

func TestControlHandoffNeedsTheContextOfATab(t *testing.T) {
	t.Parallel()
	for name, environ := range map[string]func(cli string) []string{
		"without the socket": func(cli string) []string {
			return []string{controlenv.TokenVariable + "=token-1", controlenv.CLIVariable + "=" + cli}
		},
		"with an empty socket": func(cli string) []string { return tabEnv(cli, controlenv.SocketVariable+"=") },
		"without the token": func(cli string) []string {
			return []string{controlenv.SocketVariable + "=unix:/s", controlenv.CLIVariable + "=" + cli}
		},
		"with an empty token": func(cli string) []string { return tabEnv(cli, controlenv.TokenVariable+"=") },
		// A Worker without a link, such as the desktop app's AppImage, sets
		// no LEAPMUX_CONTROL_CLI.
		"without the Worker's CLI": func(string) []string {
			return []string{controlenv.SocketVariable + "=unix:/s", controlenv.TokenVariable + "=token-1"}
		},
		"with an empty Worker's CLI": func(cli string) []string { return tabEnv(cli, controlenv.CLIVariable+"=") },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHandoffHarness(t)

			_, handled := h.run(environ(h.target), "control", "tab", "list")

			assert.False(t, handled)
			assert.Empty(t, h.calls)
			assert.Empty(t, h.stderr.String())
		})
	}
}

// A process that a hand-off started never hands the command off again, so a
// target that leads back to this `leapmux` cannot start one process after
// another.
func TestControlHandoffHonorsTheMarker(t *testing.T) {
	t.Parallel()
	h := newHandoffHarness(t)

	_, handled := h.run(tabEnv(h.target, controlHandoffMarker+"=1"), "control", "tab", "list")

	assert.False(t, handled)
	assert.Empty(t, h.calls)
	assert.Empty(t, h.stderr.String())

	t.Run("an empty marker is no marker", func(t *testing.T) {
		t.Parallel()
		h := newHandoffHarness(t)

		_, handled := h.run(tabEnv(h.target, controlHandoffMarker+"="), "control", "tab", "list")

		assert.True(t, handled)
		require.Len(t, h.calls, 1)
		// Go's os.Getenv and the C library read the first entry of a name, so
		// the target must find no entry but the marker that the hand-off set.
		assert.Equal(t, []string{"1"}, envutil.ValuesFor(h.calls[0].env, controlHandoffMarker))
	})
}

// The comparison reads file identity: the path itself, a hard link and a
// symbolic link all name the running executable, and running it again would
// only cost a process.
func TestControlHandoffKeepsACommandForTheRunningExecutable(t *testing.T) {
	t.Parallel()
	for name, link := range map[string]func(self, link string) error{
		"the path itself": nil,
		"a hard link":     os.Link,
		"a symbolic link": os.Symlink,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHandoffHarness(t)
			target := h.self
			if link != nil {
				target = filepath.Join(t.TempDir(), "leapmux")
				if err := link(h.self, target); err != nil {
					// Windows refuses a symbolic link without a privilege.
					t.Skipf("cannot create %s: %v", name, err)
				}
			}

			_, handled := h.run(tabEnv(target), "control", "tab", "list")

			assert.False(t, handled)
			assert.Empty(t, h.calls)
			assert.Empty(t, h.stderr.String())
		})
	}
}

func TestControlHandoffWarnsAndRunsOnWhenTheTargetCannotRun(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		target func(t *testing.T) string
		reason string
	}{
		"a missing file": {
			target: func(t *testing.T) string { return filepath.Join(t.TempDir(), "leapmux") },
			reason: "does not exist",
		},
		"a directory": {
			target: func(t *testing.T) string { return t.TempDir() },
			reason: "is not a file",
		},
		"a relative path": {
			target: func(*testing.T) string { return filepath.Join("cli-1", "leapmux") },
			reason: "is not an absolute path",
		},
	}
	if runtime.GOOS != "windows" {
		cases["a file without the execute permission"] = struct {
			target func(t *testing.T) string
			reason string
		}{
			target: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "leapmux")
				require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"), 0o600))
				return path
			},
			reason: "is not executable",
		}
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHandoffHarness(t)
			target := tc.target(t)

			_, handled := h.run(tabEnv(target), "control", "tab", "list")

			assert.False(t, handled, "this leapmux runs the command")
			assert.Empty(t, h.calls)
			assert.Contains(t, h.stderr.String(), "warning: ")
			assert.Contains(t, h.stderr.String(), target)
			assert.Contains(t, h.stderr.String(), tc.reason)
			assert.Contains(t, h.stderr.String(), "This leapmux runs the command, and its version can differ from the Worker's.")
		})
	}
}

func TestControlHandoffWarnsAndRunsOnWhenTheTargetDoesNotStart(t *testing.T) {
	t.Parallel()
	h := newHandoffHarness(t)
	h.execErr = errors.New("exec format error")

	_, handled := h.run(tabEnv(h.target), "control", "tab", "list")

	assert.False(t, handled)
	assert.Len(t, h.calls, 1)
	assert.Equal(t, "warning: the Worker's CLI "+h.target+" did not start: exec format error. "+
		"This leapmux runs the command, and its version can differ from the Worker's.\n", h.stderr.String())
}

// Without the path of the running executable, the hand-off cannot tell the
// two apart. It hands the command off, and the marker stops a second one.
func TestControlHandoffHandsOffWhenItCannotReadItself(t *testing.T) {
	t.Parallel()
	for name, executable := range map[string]func() (string, error){
		"an error":       func() (string, error) { return "", errors.New("not supported") },
		"a missing path": func() (string, error) { return filepath.Join(t.TempDir(), "gone"), nil },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHandoffHarness(t)

			_, handled := h.runAs(executable, tabEnv(h.target), "control", "tab", "list")

			assert.True(t, handled)
			assert.Len(t, h.calls, 1)
		})
	}
}

// The hand-off reads a variable as os.Getenv and the C library do: the first
// entry wins, and only Windows ignores the case of a name.
func TestControlHandoffReadsTheEnvironmentAsOSGetenvDoes(t *testing.T) {
	t.Parallel()

	t.Run("the first entry wins", func(t *testing.T) {
		t.Parallel()
		h := newHandoffHarness(t)
		environ := append(tabEnv(h.target), controlenv.CLIVariable+"="+filepath.Join(t.TempDir(), "missing"))

		_, handled := h.run(environ, "control", "tab", "list")

		assert.True(t, handled)
		require.Len(t, h.calls, 1)
		assert.Equal(t, h.target, h.calls[0].target)
		assert.Empty(t, h.stderr.String(), "the later entry is never read")
	})

	t.Run("the case of a name", func(t *testing.T) {
		t.Parallel()
		h := newHandoffHarness(t)
		environ := []string{
			strings.ToLower(controlenv.SocketVariable) + "=unix:/s",
			strings.ToLower(controlenv.TokenVariable) + "=token-1",
			strings.ToLower(controlenv.CLIVariable) + "=" + h.target,
		}

		_, handled := h.run(environ, "control", "tab", "list")

		assert.Equal(t, runtime.GOOS == "windows", handled)
	})
}

// The name that the hand-off compares is the name of the control group in the
// command tree.
func TestControlCommandIsTheControlGroup(t *testing.T) {
	t.Parallel()
	assert.Equal(t, cmdcontrol.NewCommand().Name(), controlCommand)
	found, rest, err := newRootCommand(testCLIRunners(t)).Find([]string{controlCommand})
	require.NoError(t, err)
	assert.Empty(t, rest)
	assert.Equal(t, controlCommand, found.Name())
}

// The marker served the process that a hand-off started. A process that it
// starts decides for itself.
//
// Not parallel: the test sets a variable of this process.
func TestHandOffControlCommandClearsTheMarker(t *testing.T) {
	t.Setenv(controlHandoffMarker, "1")

	_, handled := handOffControlCommand([]string{"version"})

	assert.False(t, handled)
	_, present := os.LookupEnv(controlHandoffMarker)
	assert.False(t, present)
}
