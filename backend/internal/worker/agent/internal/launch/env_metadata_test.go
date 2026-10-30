package launch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/envprobe"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnvironmentMetadataUsesTheNativeLaunchEnvironment(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	data, err := os.ReadFile(executable)
	require.NoError(t, err)
	helper := filepath.Join(t.TempDir(), "worker [probe] ' 日本語.exe")
	require.NoError(t, os.WriteFile(helper, data, 0o700))
	const name = "LEAPMUX_CAPTURED_VALUE"
	const value = "日本語=é\nsecond\r\n%$'"
	args, err := (envprobe.Request{Variables: []string{name}}).Arguments()
	require.NoError(t, err)
	for _, shellName := range []string{"sh", "bash", "zsh", "fish", "tcsh", "pwsh", "powershell", "nu"} {
		t.Run(shellName, func(t *testing.T) {
			shell, err := exec.LookPath(shellName)
			if err != nil {
				t.Skipf("%s is not installed", shellName)
			}
			home := t.TempDir()
			cmd, delimiter, prefix := Wrap(testutil.DeadlineContext(t), WrapSpec{
				Shell: shell, WorkingDir: home, Launch: Spec{Program: helper}, BaseArgs: args,
				StripEnvKeys: []string{name}, SetEnv: []string{name + "=" + value},
				EnvMetadata: &EnvMetadata{Program: helper, Variables: []string{name}},
			})
			cmd.Env = append(cmd.Environ(), "HOME="+home, "USERPROFILE="+home, "ZDOTDIR="+home, "XDG_CONFIG_HOME="+home, name+"=inherited")
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "%q", output)
			before, after, found := strings.Cut(string(output), delimiter+"\n")
			require.True(t, found, "%q", output)
			encoded := strings.TrimSuffix(strings.TrimPrefix(before, prefix+envprobe.MetadataKey+"="), "\n")
			metadata, err := envprobe.DecodeMetadata(encoded)
			require.NoError(t, err, "%q", before)
			actual, err := envprobe.Decode([]byte(after))
			require.NoError(t, err)
			assert.Equal(t, value, metadata.Values[name])
			assert.True(t, metadata.Present[name])
			assert.Equal(t, metadata, actual, "the preamble and program must observe the same native environment and directory")
		})
	}
}

func TestEnvironmentMetadataFailureDoesNotPreventTheProviderLaunch(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	args, err := (envprobe.Request{}).Arguments()
	require.NoError(t, err)
	for _, name := range []string{"sh", "tcsh", "pwsh", "powershell", "nu"} {
		t.Run(name, func(t *testing.T) {
			shell, err := exec.LookPath(name)
			if err != nil {
				t.Skipf("%s is not installed", name)
			}
			home := t.TempDir()
			cmd, delimiter, _ := Wrap(testutil.DeadlineContext(t), WrapSpec{
				Shell: shell, WorkingDir: home, Launch: Spec{Program: executable}, BaseArgs: args,
				EnvMetadata: &EnvMetadata{Program: filepath.Join(home, "missing-helper"), Variables: []string{"TZ"}},
			})
			cmd.Env = append(cmd.Environ(), "HOME="+home, "USERPROFILE="+home, "ZDOTDIR="+home, "XDG_CONFIG_HOME="+home)
			output, err := cmd.CombinedOutput()
			require.NoError(t, err)
			_, after, found := strings.Cut(string(output), delimiter+"\n")
			require.True(t, found, "%q", output)
			_, err = envprobe.Decode([]byte(after))
			require.NoError(t, err)
		})
	}
}

func TestEnvironmentMetadataAndConditionsShareThePreamble(t *testing.T) {
	for _, shell := range []string{"/bin/sh", "/bin/tcsh", "/usr/bin/nu", "/usr/bin/pwsh"} {
		t.Run(shell, func(t *testing.T) {
			helper := filepath.Join(t.TempDir(), "worker")
			cmd, delimiter, prefix := Wrap(context.Background(), WrapSpec{
				Shell: shell, Launch: Spec{Program: "provider"}, EnvGated: testEnvGate(),
				EnvMetadata: &EnvMetadata{Program: helper, Variables: []string{"TZ"}},
			})
			args, err := (envprobe.Request{Variables: []string{"TZ"}, MetadataPrefix: prefix}).Arguments()
			require.NoError(t, err)
			inner := cmd.Args[len(cmd.Args)-1]
			assert.Contains(t, inner, args[1])
			assert.Contains(t, inner, "test_gate_open")
			assert.Less(t, strings.Index(inner, args[1]), strings.Index(inner, delimiter))
		})
	}
}

func TestEnvironmentMetadataRejectsInvalidProducerSpecifications(t *testing.T) {
	for _, metadata := range []*EnvMetadata{{}, {Program: "relative"}, {Program: filepath.Join(t.TempDir(), "worker"), Variables: []string{"TZ;exit"}}} {
		assert.Panics(t, func() {
			Wrap(context.Background(), WrapSpec{Shell: "/bin/sh", Launch: Spec{Program: "provider"}, EnvMetadata: metadata})
		})
	}
	assert.Panics(t, func() {
		Wrap(context.Background(), WrapSpec{Shell: "/bin/sh", Launch: Spec{Program: "provider"}, EnvMetadata: &EnvMetadata{Program: filepath.Join(t.TempDir(), "worker")}, EnvGated: &EnvGatedArgs{EnvVars: []string{"TZ"}, MetaKey: envprobe.MetadataKey}})
	})
}

func TestCshArgumentsKeepLiteralLineBreaks(t *testing.T) {
	requireShell(t, "/bin/tcsh")
	const value = "日本語=é\nsecond\r\n%$'"
	_, output := runWrapped(t, WrapSpec{
		Shell: "/bin/tcsh", WorkingDir: t.TempDir(), Launch: Spec{Program: "/bin/sh"},
		BaseArgs: []string{"-c", `printf '%s' "$1"`, "probe", value},
	})
	assert.Equal(t, value, output)
}

const launchFixtureArgument = "--leapmux-test-launch-program"

// This fixture uses the test executable so every shell starts the same native
// program, including paths with spaces and Unicode. It never starts an agent.
func init() {
	if len(os.Args) < 5 || os.Args[1] != launchFixtureArgument {
		return
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(91)
	}
	code, err := strconv.Atoi(os.Args[3])
	if err != nil {
		os.Exit(92)
	}

	cwd, err := syscall.Getwd()
	if err != nil {
		os.Exit(93)
	}
	err = json.NewEncoder(os.Stdout).Encode(map[string]any{"input": string(input), "value": os.Getenv("LEAPMUX_LAUNCH_FIXTURE_VALUE"), "cwd": cwd, "arguments": os.Args[5:], "identityPresent": nativeFixtureIdentityPresent(), "pinned": os.Getenv("LEAPMUX_NATIVE_PINNED_PATH"), "executable": os.Args[0], "valuePresent": nativeFixtureValuePresent()})
	if err != nil {
		os.Exit(94)
	}
	if _, err := fmt.Fprintln(os.Stderr, "native stderr after startup"); err != nil {
		os.Exit(96)
	}
	marker, err := os.OpenFile(os.Args[4], os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(95)
	}
	_, writeErr := marker.WriteString("executed")
	closeErr := marker.Close()
	if writeErr != nil || closeErr != nil {
		os.Exit(95)
	}
	os.Exit(code)
}

func nativeFixtureValuePresent() bool {
	_, present := os.LookupEnv("LEAPMUX_LAUNCH_FIXTURE_VALUE")
	return present
}

func nativeFixtureIdentityPresent() bool {
	_, present := os.LookupEnv("LEAPMUX_NATIVE_IDENTITY")
	return present
}

func nativeFixtureExecutable(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	data, err := os.ReadFile(executable)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "worker [fixture] ' 日本語.exe")
	require.NoError(t, os.WriteFile(path, data, 0o700))
	return path
}

func nativeFixtureContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func nativeFixtureOutput(output, delimiter string) (string, bool) {
	_, after, found := strings.Cut(output, delimiter)
	if !found {
		return "", false
	}
	if result, ok := strings.CutPrefix(after, "\r\n"); ok {
		return result, true
	}
	return strings.CutPrefix(after, "\n")
}

func TestFishLaunchPreservesLiteralValuesAndArguments(t *testing.T) {
	shell, err := exec.LookPath("fish")
	if err != nil {
		t.Skip("fish is not installed")
	}
	executable := nativeFixtureExecutable(t)
	for _, value := range []string{"", "apostrophe'", `trailing\`, `two\\slashes`, "日本語\nsecond\r\n%$'", "first::last"} {
		t.Run(value, func(t *testing.T) {
			directory := t.TempDir()
			marker := filepath.Join(directory, "executed")
			cmd, delimiter, _ := Wrap(nativeFixtureContext(t), WrapSpec{
				Shell: shell, WorkingDir: directory, Launch: Spec{Program: executable},
				BaseArgs:    []string{launchFixtureArgument, "echo", "0", marker, value},
				SetEnv:      []string{"LEAPMUX_NATIVE_PINNED_PATH=" + value},
				EnvMetadata: &EnvMetadata{Program: executable, Variables: []string{"LEAPMUX_NATIVE_PINNED_PATH"}},
			})
			cmd.Env = append(cmd.Environ(), "HOME="+directory, "USERPROFILE="+directory, "XDG_CONFIG_HOME="+directory)
			output, err := cmd.Output()
			require.NoError(t, err)
			program, found := nativeFixtureOutput(string(output), delimiter)
			require.True(t, found)
			var result struct {
				Pinned    string   `json:"pinned"`
				Arguments []string `json:"arguments"`
			}
			require.NoError(t, json.Unmarshal([]byte(program), &result))
			assert.Equal(t, value, result.Pinned)
			assert.Equal(t, []string{value}, result.Arguments)
		})
	}
}

func TestFishLaunchRemovesTheExportedIdentity(t *testing.T) {
	shell, err := exec.LookPath("fish")
	if err != nil {
		t.Skip("fish is not installed")
	}
	executable := nativeFixtureExecutable(t)
	directory := t.TempDir()
	cmd, delimiter, _ := Wrap(nativeFixtureContext(t), WrapSpec{
		Shell: shell, WorkingDir: directory, Launch: Spec{Program: executable},
		BaseArgs:     []string{launchFixtureArgument, "echo", "0", filepath.Join(directory, "executed")},
		StripEnvKeys: []string{"LEAPMUX_NATIVE_IDENTITY"},
	})
	cmd.Env = append(cmd.Environ(), "HOME="+directory, "XDG_CONFIG_HOME="+directory, "LEAPMUX_NATIVE_IDENTITY=inherited")
	output, err := cmd.Output()
	require.NoError(t, err)
	program, found := nativeFixtureOutput(string(output), delimiter)
	require.True(t, found)
	var result struct {
		IdentityPresent bool `json:"identityPresent"`
	}
	require.NoError(t, json.Unmarshal([]byte(program), &result))
	assert.False(t, result.IdentityPresent)
}

func TestFishLaunchSelectsOptionalArgumentsFromItsEnvironment(t *testing.T) {
	shell, err := exec.LookPath("fish")
	if err != nil {
		t.Skip("fish is not installed")
	}
	executable := nativeFixtureExecutable(t)
	for _, tc := range []struct {
		name     string
		value    *string
		optional bool
	}{
		{name: "absent", optional: true}, {name: "empty", value: new(string), optional: true}, {name: "present", value: func() *string { value := "native"; return &value }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			spec := WrapSpec{Shell: shell, WorkingDir: directory, Launch: Spec{Program: executable},
				BaseArgs:     []string{launchFixtureArgument, "echo", "0", filepath.Join(directory, "executed")},
				StripEnvKeys: []string{"LEAPMUX_NATIVE_GATE"},
				EnvGated:     &EnvGatedArgs{EnvVars: []string{"LEAPMUX_NATIVE_GATE"}, MetaKey: "optional_arguments", Args: []string{"optional"}},
			}
			if tc.value != nil {
				spec.SetEnv = []string{"LEAPMUX_NATIVE_GATE=" + *tc.value}
			}
			cmd, delimiter, prefix := Wrap(nativeFixtureContext(t), spec)
			cmd.Env = append(cmd.Environ(), "HOME="+directory, "XDG_CONFIG_HOME="+directory)
			output, err := cmd.Output()
			require.NoError(t, err)
			assert.Contains(t, string(output), fmt.Sprintf("%soptional_arguments=%t", prefix, tc.optional))
			program, found := nativeFixtureOutput(string(output), delimiter)
			require.True(t, found)
			var result struct {
				Arguments []string `json:"arguments"`
			}
			require.NoError(t, json.Unmarshal([]byte(program), &result))
			if tc.optional {
				assert.Equal(t, []string{"optional"}, result.Arguments)
			} else {
				assert.Empty(t, result.Arguments)
			}
		})
	}
}

func TestCshHandoffPreservesLiteralArgumentsAcrossLoginAndConditionalModes(t *testing.T) {
	requireShell(t, "/bin/tcsh")
	executable := nativeFixtureExecutable(t)
	values := []string{"", "apostrophe'", `trailing\`, `two\\slashes`, "$literal", "`literal`", "(literal)", "bang!literal", "日本語\nsecond\r\n%$'"}
	for _, login := range []bool{false, true} {
		for _, gated := range []bool{false, true} {
			t.Run(fmt.Sprintf("login=%t/gated=%t", login, gated), func(t *testing.T) {
				directory := t.TempDir()
				arguments := append([]string{launchFixtureArgument, "echo", "0", filepath.Join(directory, "executed")}, values...)
				spec := WrapSpec{Shell: "/bin/tcsh", LoginShell: login, WorkingDir: directory, Launch: Spec{Program: executable}, BaseArgs: arguments,
					EnvMetadata: &EnvMetadata{Program: executable},
				}
				if gated {
					spec.EnvGated = &EnvGatedArgs{EnvVars: []string{"LEAPMUX_NATIVE_CSH_GATE"}, MetaKey: "optional_arguments", Args: []string{"optional"}}
				}
				cmd, delimiter, _ := Wrap(nativeFixtureContext(t), spec)
				cmd.Env = append(cmd.Environ(), "HOME="+directory, "USERPROFILE="+directory, "ZDOTDIR="+directory)
				output, err := cmd.Output()
				require.NoError(t, err)
				program, found := nativeFixtureOutput(string(output), delimiter)
				require.True(t, found)
				var result struct {
					Arguments []string `json:"arguments"`
				}
				require.NoError(t, json.Unmarshal([]byte(program), &result))
				expected := append([]string(nil), values...)
				if gated {
					expected = append(expected, "optional")
				}
				assert.Equal(t, expected, result.Arguments)
			})
		}
	}
}

func TestFishIdentityMaskPreservesExportedUniversalState(t *testing.T) {
	shell, err := exec.LookPath("fish")
	if err != nil {
		t.Skip("fish is not installed")
	}
	executable := nativeFixtureExecutable(t)
	directory := t.TempDir()
	environment := append(os.Environ(), "HOME="+directory, "XDG_CONFIG_HOME="+directory)
	seed := exec.CommandContext(nativeFixtureContext(t), shell, "-c", "builtin set --universal --export LEAPMUX_NATIVE_IDENTITY universal")
	seed.Env = environment
	require.NoError(t, seed.Run())
	path := filepath.Join(directory, "fish", "fish_variables")
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	cmd, delimiter, _ := Wrap(nativeFixtureContext(t), WrapSpec{Shell: shell, WorkingDir: directory, Launch: Spec{Program: executable},
		BaseArgs: []string{launchFixtureArgument, "echo", "0", filepath.Join(directory, "executed")}, StripEnvKeys: []string{"LEAPMUX_NATIVE_IDENTITY"},
	})
	cmd.Env = environment
	output, err := cmd.Output()
	require.NoError(t, err)
	program, found := nativeFixtureOutput(string(output), delimiter)
	require.True(t, found)
	var result struct {
		IdentityPresent bool `json:"identityPresent"`
	}
	require.NoError(t, json.Unmarshal([]byte(program), &result))
	assert.False(t, result.IdentityPresent)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after, "the launch must not erase or rewrite universal identity state")
}

func TestCshHandoffRejectsProfileArgumentCardinalityChanges(t *testing.T) {
	requireShell(t, "/bin/tcsh")
	executable := nativeFixtureExecutable(t)
	for _, tc := range []struct{ name, profile string }{
		{name: "drop", profile: "set argv = ()\n"},
		{name: "append", profile: "set argv = ( $argv:q appended )\n"},
		{name: "prepend", profile: "set argv = ( prepended $argv:q )\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			marker := filepath.Join(directory, "executed")
			require.NoError(t, os.WriteFile(filepath.Join(directory, ".tcshrc"), []byte(tc.profile), 0o600))
			cmd, delimiter, prefix := Wrap(nativeFixtureContext(t), WrapSpec{
				Shell: "/bin/tcsh", LoginShell: true, WorkingDir: directory, Launch: Spec{Program: executable},
				BaseArgs:    []string{launchFixtureArgument, "echo", "0", marker},
				EnvMetadata: &EnvMetadata{Program: executable},
			})
			cmd.Env = append(cmd.Environ(), "HOME="+directory, "USERPROFILE="+directory)
			output, runErr := cmd.Output()
			var exit *exec.ExitError
			require.ErrorAs(t, runErr, &exit)
			assert.Equal(t, shellHandoffRefusedExitCode, exit.ExitCode())
			assert.NotContains(t, string(output), prefix+envprobe.MetadataKey+"=")
			assert.NotContains(t, string(output), delimiter)
			_, err := os.Stat(marker)
			assert.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}
