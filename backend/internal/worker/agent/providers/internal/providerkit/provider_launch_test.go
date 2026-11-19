package providerkit

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/shellquote"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A launch reads the provider and locator from the same registration.
func TestResolveProviderLaunch(t *testing.T) {
	registration := agent.Registration{Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE}
	resolved := launch.Spec{Program: "zcode", PrefixArgs: []string{"--stdio"}}
	called := false
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registration.Locator = launch.Custom(func(received context.Context, shell string, loginShell bool) (launch.Spec, launch.Resolution) {
		called = true
		_, hasDeadline := received.Deadline()
		assert.True(t, hasDeadline)
		assert.NoError(t, received.Err())
		assert.Equal(t, "/bin/sh", shell)
		assert.True(t, loginShell)
		return resolved, launch.Found
	})
	_, err := ResolveLaunch(ctx, agent.Options{Shell: "/bin/sh", LoginShell: true}, registration)
	require.NoError(t, err)
	assert.True(t, called)

	t.Run("registered binary", func(t *testing.T) {
		shell := testutil.TestShell()
		suffix := ""
		if runtime.GOOS == "windows" {
			var err error
			shell, err = exec.LookPath("powershell")
			require.NoError(t, err)
			suffix = ".cmd"
		}
		first, second := "leapmux-test-first-program", "leapmux-test-second-program"
		registration.Locator = launch.Binaries(first+suffix, second+suffix)

		for _, selected := range []string{first, second} {
			t.Run(selected, func(t *testing.T) {
				dir := t.TempDir()
				writeProgram := func(name string) {
					script := "#!/bin/sh\nprintf '%s\\n' " + shellquote.POSIX.Quote(name) + "\n"
					if runtime.GOOS == "windows" {
						script = "@echo off\r\necho " + name + "\r\n"
					}
					testutil.WriteExecutable(t, filepath.Join(dir, name+suffix), script)
				}
				writeProgram(second)
				if selected == first {
					writeProgram(first)
				}
				t.Setenv("PATH", dir)
				spec, err := ResolveLaunch(t.Context(), agent.Options{Shell: shell}, registration)
				require.NoError(t, err)
				assert.Empty(t, spec.PrefixArgs)
				assert.Empty(t, spec.Env)
				prepared, err := launch.Wrap(t.Context(), launch.WrapSpec{Shell: shell, Launch: spec})
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, prepared.Close()) })
				if runtime.GOOS == "windows" {
					// Saved account PATH entries can change the Worker-supplied PATH.
					cmd := prepared.Cmd
					cmd.Args = slices.Insert(cmd.Args, 1, "-NoProfile")
					last := len(cmd.Args) - 1
					cmd.Args[last] = "$env:PATH = " + shellquote.PowerShell.Quote(dir) + "\n" + cmd.Args[last]
				}
				output, err := prepared.Cmd.Output()
				require.NoError(t, err, "%s", output)
				_, payload, ready := strings.Cut(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n"+prepared.Delimiter+"\n")
				require.True(t, ready, "%q", output)
				assert.Equal(t, selected+"\n", payload)
			})
		}
	})

	t.Run("empty locator", func(t *testing.T) {
		registration.Locator = launch.Locator{}
		_, err := ResolveLaunch(context.Background(), agent.Options{Shell: "/bin/sh"}, registration)
		require.EqualError(t, err, "no valid provider commands registered for ZCode")
	})

	for _, tc := range []struct {
		name    string
		res     launch.Resolution
		message string
	}{
		{"missing", launch.Missing, "ZCode is not installed"},
		{"inconclusive", launch.Unknown, "could not determine how to launch ZCode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registration.Locator = agenttest.AnsweringLocator(launch.Spec{}, tc.res)
			_, err := ResolveLaunch(context.Background(), agent.Options{Shell: "/bin/sh"}, registration)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.message)
		})
	}
}

func TestResolveLaunchHonorsStartupDeadline(t *testing.T) {
	registration := agent.Registration{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
		Locator: launch.Custom(func(ctx context.Context, _ string, _ bool) (launch.Spec, launch.Resolution) {
			if _, present := ctx.Deadline(); assert.True(t, present, "resolution needs a deadline") {
				<-ctx.Done()
			}
			return launch.Spec{Program: "fallback"}, launch.Found
		}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	spec, err := ResolveLaunch(ctx, agent.Options{StartupTimeout: time.Nanosecond}, registration)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Empty(t, spec.Program)
	assert.NoError(t, ctx.Err(), "the running agent still needs its parent context")
}
