package launch

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveDialectMapsEveryShellBasename(t *testing.T) {
	t.Parallel()
	cases := []struct {
		shell string
		want  shellDialect
	}{
		{"sh", posixDialect{shell: "sh"}},
		{"bash", posixDialect{shell: "bash"}},
		{"zsh", posixDialect{shell: "zsh"}},
		{"ksh", posixDialect{shell: "ksh"}},
		{"dash", posixDialect{shell: "dash"}},
		{"pwsh", pwshDialect{}},
		{"powershell", pwshDialect{}},
		{"pwsh-preview", pwshDialect{}},
		{"powershell-preview", pwshDialect{}},
		{"nu", nuDialect{}},
		{"fish", fishDialect{}},
		{"elvish", elvishDialect{}},
		{"xonsh", xonshDialect{}},
		{"csh", cshDialect{}},
		{"tcsh", cshDialect{}},
		{"profile-shim", posixDialect{shell: "profile-shim"}},
	}
	for _, tc := range cases {
		t.Run(tc.shell, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, resolveDialect(tc.shell))
		})
	}
}

func TestDialectRenderProbeMatchesRenderCommandProbe(t *testing.T) {
	t.Parallel()
	const nonce = "__TEST_NONCE__"
	candidates := []string{"alpha", "beta"}
	shells := []string{
		"sh", "bash", "zsh", "ksh", "dash",
		"pwsh", "powershell", "nu", "fish", "elvish", "xonsh", "csh", "tcsh",
	}
	for _, shell := range shells {
		for _, nativeOnly := range []bool{false, true} {
			for _, loginShell := range []bool{false, true} {
				got := renderCommandProbe(shell, nonce, candidates, nativeOnly, loginShell)
				want := resolveDialect(shell).renderProbe(nonce, candidates, nativeOnly, loginShell)
				assert.Equal(t, want, got, "shell=%s nativeOnly=%t loginShell=%t", shell, nativeOnly, loginShell)
				assert.Contains(t, got, nonce+":start")
				assert.Contains(t, got, nonce+":0:yes")
				assert.Contains(t, got, nonce+":0:no")
				assert.Contains(t, got, nonce+":1:yes")
				assert.Contains(t, got, nonce+":1:no")
				assert.Contains(t, got, nonce+":complete")
			}
		}
	}

	// Bash uses its batched array path for command probes and the standard path for native probes.
	bashCommand := renderCommandProbe("bash", nonce, candidates, false, false)
	assert.Contains(t, bashCommand, "__lm_probe_records=")
	bashNative := renderCommandProbe("bash", nonce, candidates, true, false)
	assert.NotContains(t, bashNative, "__lm_probe_records=")
	assert.Contains(t, bashNative, "__leapmux_lookup 'alpha' path")

	// Zsh normalizes relative paths with $PWD instead of running pwd -P.
	zshNative := renderCommandProbe("zsh", nonce, candidates, true, false)
	assert.Contains(t, zshNative, `__lm_path="$PWD/$__lm_path"`)
	shNative := renderCommandProbe("sh", nonce, candidates, true, false)
	assert.Contains(t, shNative, `__lm_path="$(__leapmux_builtin pwd -P)/$__lm_path"`)

	// PowerShell emits __leapmux_native only for native probes.
	pwshCommand := renderCommandProbe("pwsh", nonce, candidates, false, false)
	assert.NotContains(t, pwshCommand, "function __leapmux_native")
	pwshNative := renderCommandProbe("pwsh", nonce, candidates, true, false)
	assert.Contains(t, pwshNative, "function __leapmux_native")
	assert.Equal(t, pwshNative, renderCommandProbe("powershell", nonce, candidates, true, false))

	// Elvish loads rc.elv only when loginShell is true.
	elvishNonLogin := renderCommandProbe("elvish", nonce, candidates, false, false)
	assert.NotContains(t, elvishNonLogin, "leapmux-profile/../../rc")
	elvishLogin := renderCommandProbe("elvish", nonce, candidates, false, true)
	assert.Contains(t, elvishLogin, "leapmux-profile/../../rc")

	// Csh and tcsh share the same unalias setup and lookup script.
	assert.Equal(t,
		renderCommandProbe("csh", nonce, candidates, false, false),
		renderCommandProbe("tcsh", nonce, candidates, false, false),
	)
}

func TestDialectPrepareProbeScript(t *testing.T) {
	t.Parallel()
	const sample = "echo hello\n"
	for _, shell := range []string{"sh", "bash", "zsh", "ksh", "dash", "pwsh", "powershell", "nu", "fish", "elvish", "xonsh"} {
		t.Run(shell, func(t *testing.T) {
			t.Parallel()
			wrapped, cleanup, err := resolveDialect(shell).prepareProbeScript(sample)
			require.NoError(t, err)
			assert.Equal(t, sample, wrapped)
			assert.Nil(t, cleanup)
		})
	}

	for _, shell := range []string{"csh", "tcsh"} {
		t.Run(shell, func(t *testing.T) {
			t.Parallel()
			wrapped, cleanup, err := resolveDialect(shell).prepareProbeScript(sample)
			require.NoError(t, err)
			require.NotNil(t, cleanup)
			quotedPath, found := strings.CutPrefix(wrapped, cshInvocationPrefix)
			require.True(t, found)
			require.True(t, strings.HasPrefix(quotedPath, "'") && strings.HasSuffix(quotedPath, "'"))
			path := strings.NewReplacer("'\\''", "'", "\\!", "!").Replace(quotedPath[1 : len(quotedPath)-1])
			data, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			assert.Equal(t, sample+"\n", string(data))
			cleanup()
			_, statErr := os.Stat(path)
			assert.ErrorIs(t, statErr, os.ErrNotExist)
		})
	}
}

func TestDialectPinSource(t *testing.T) {
	t.Parallel()
	const base = environmentPinPrefix + "tok_0"
	used := map[string]bool{strings.ToUpper(base): true}
	for _, shell := range []string{"sh", "bash", "zsh", "ksh", "dash", "pwsh", "nu", "fish", "elvish", "xonsh"} {
		assert.Equal(t, base+"_", resolveDialect(shell).pinSource(base, used), shell)
	}
	for _, shell := range []string{"csh", "tcsh"} {
		source := resolveDialect(shell).pinSource(base, used)
		assert.LessOrEqual(t, len(source), cshVariableNameLimit)
		assert.True(t, strings.HasPrefix(source, environmentPinPrefix))
		assert.NotEqual(t, base, source)
	}
}

func TestDialectWrapSupportsEveryShellBasename(t *testing.T) {
	t.Parallel()
	shells := []string{
		"/bin/sh", "/bin/bash", "/bin/zsh", "/bin/ksh", "/bin/dash",
		"/usr/bin/pwsh", "/usr/bin/powershell", "/usr/bin/nu", "/usr/bin/fish",
		"/usr/bin/elvish", "/usr/bin/xonsh", "/bin/csh", "/bin/tcsh",
	}
	for _, shell := range shells {
		for _, target := range []Spec{{Program: "provider"}, ShellCommands("first-cli", "second-cli")} {
			prepared, err := Wrap(t.Context(), WrapSpec{
				Shell:        shell,
				Launch:       target,
				BaseArgs:     []string{"--stdio"},
				StripEnvKeys: []string{"LEAPMUX_STRIP"},
				SetEnv:       []string{"LEAPMUX_PIN=val"},
				WorkingDir:   t.TempDir(),
			})
			require.NoError(t, err, "shell=%s target=%+v", shell, target)
			script := renderedWrapScript(t, prepared.Cmd)
			assert.Contains(t, script, prepared.Delimiter, "shell=%s", shell)
			assert.Contains(t, script, "LEAPMUX_STRIP", "shell=%s", shell)
			assert.Contains(t, script, "LEAPMUX_PIN", "shell=%s", shell)
			require.NoError(t, prepared.Close())
		}
	}
}
