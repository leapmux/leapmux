package launch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPinnedEnvironmentNeverAppearsInCommandArguments(t *testing.T) {
	t.Parallel()
	const secret = "private-server-test-credential-12345"
	for _, shell := range []string{"/bin/sh", "/bin/zsh", "fish", "nu", "pwsh", "csh", "xonsh"} {
		t.Run(shell, func(t *testing.T) {
			t.Parallel()
			for _, launch := range []Spec{{Program: "provider"}, ShellCommands("provider")} {
				cmd, _, _ := wrapTestCommand(t, t.Context(), WrapSpec{Shell: shell, Launch: launch, SetEnv: []string{"PRIVATE_TOKEN=" + secret}})
				assert.NotContains(t, strings.Join(cmd.Args, " "), secret)
				assert.NotContains(t, renderedWrapScript(t, cmd), secret)
			}
		})
	}
}

func TestReusableEnvironmentDropsOnlyTheCurrentLaunchPins(t *testing.T) {
	t.Parallel()
	prepared, err := Wrap(t.Context(), WrapSpec{
		Shell: "/bin/sh", Launch: ShellCommands("provider"),
		Environment: []string{"__LEAPMUX_PIN_inherited=preserved", "SHELL_VALUE=initial"},
		SetEnv:      []string{"PRIVATE_TOKEN=temporary-value"},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, prepared.Close()) })
	require.Len(t, prepared.pinSources, 1)
	transport := prepared.pinSources[0] + "=temporary-value"
	assert.Contains(t, prepared.Cmd.Environ(), transport)
	snapshot := prepared.ReusableEnvironment()
	assert.NotContains(t, snapshot, transport)
	assert.Contains(t, snapshot, "__LEAPMUX_PIN_inherited=preserved")
	assert.Contains(t, snapshot, "SHELL_VALUE=initial")
}

func TestPinnedEnvironmentSurvivesAProfileWithoutLeakingTransportVariables(t *testing.T) {
	t.Parallel()
	shell, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh is not installed")
	}
	binary, err := os.Executable()
	require.NoError(t, err)
	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, ".zshenv"), []byte("export PRIVATE_TOKEN=profile-value\n"), 0o600))
	const secret = "private-token-'quoted'\n$HOME $(printf injected)"
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd, _, _ := wrapTestCommand(t, ctx, WrapSpec{Shell: shell, LoginShell: true, Launch: Spec{Program: binary}, BaseArgs: []string{"-test.run=^TestPinnedEnvironmentChild$"}, SetEnv: []string{"PRIVATE_TOKEN=" + secret}, StripEnvKeys: []string{"PRIVATE_TOKEN"}, WorkingDir: home})
	cmd.Env = envutil.PinEnv(cmd.Environ(), "HOME="+home, "ZDOTDIR="+home, "LEAPMUX_PIN_TEST_HELPER=1")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	result := readPinResult(t, output)
	assert.Equal(t, secret, result.Value)
	assert.Empty(t, result.Transport)
}

type pinResult struct {
	Value           string
	Transport       []string
	Args            []string
	StrippedPresent bool
	EmptyPresent    bool
	EmptyValue      string
}

func readPinResult(t *testing.T, output []byte) pinResult {
	t.Helper()
	var records []string
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "PIN_RESULT:") {
			records = append(records, strings.TrimPrefix(line, "PIN_RESULT:"))
		}
	}
	require.Len(t, records, 1, "%s", output)
	var result pinResult
	require.NoError(t, json.Unmarshal([]byte(records[0]), &result))
	return result
}

func TestPinnedEnvironmentChild(t *testing.T) {
	if os.Getenv("LEAPMUX_PIN_TEST_HELPER") != "1" {
		return
	}
	_, stripped := os.LookupEnv("LEAPMUX_TEST_STRIPPED")
	emptyValue, emptyPresent := os.LookupEnv("LEAPMUX_TEST_EMPTY")
	body, err := json.Marshal(pinResult{Value: os.Getenv("PRIVATE_TOKEN"), Transport: testPinVariableNames(), Args: os.Args[1:], StrippedPresent: stripped, EmptyPresent: emptyPresent, EmptyValue: emptyValue})
	if err != nil {
		os.Exit(2)
	}
	fmt.Printf("PIN_RESULT:%s\n", body)
	os.Exit(0)
}

func testPinVariableNames() []string {
	var names []string
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "__LEAPMUX_PIN_") {
			name, _, _ := strings.Cut(entry, "=")
			names = append(names, name)
		}
	}
	return names
}

func TestPinnedEnvironmentRunsInEveryInstalledShell(t *testing.T) {
	t.Parallel()
	binary, err := os.Executable()
	require.NoError(t, err)
	for _, name := range []string{"sh", "bash", "zsh", "fish", "nu", "pwsh", "csh", "tcsh", "xonsh"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			shell, err := exec.LookPath(name)
			if err != nil {
				t.Skipf("%s is not installed", name)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			const value = "pin-value-'quoted'\n$HOME $(printf injected)"
			cmd, _, _ := wrapTestCommand(t, ctx, WrapSpec{Shell: shell, Launch: Spec{Program: binary}, BaseArgs: []string{"-test.run=^TestPinnedEnvironmentChild$"}, SetEnv: []string{"PRIVATE_TOKEN=" + value}})
			home := t.TempDir()
			cmd.Env = envutil.PinEnv(cmd.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"), "XDG_CACHE_HOME="+filepath.Join(home, ".cache"), "XDG_STATE_HOME="+filepath.Join(home, ".state"), "ZDOTDIR="+home, "LEAPMUX_PIN_TEST_HELPER=1", "POWERSHELL_TELEMETRY_OPTOUT=1", "DOTNET_CLI_TELEMETRY_OPTOUT=1")
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", output)
			result := readPinResult(t, output)
			assert.Equal(t, value, result.Value)
			assert.Empty(t, result.Transport)
		})
	}
}

func TestEnvironmentPinNamesAvoidEveryEnvironmentNamespace(t *testing.T) {
	t.Parallel()
	prepared := prepareShellLaunch(WrapSpec{
		SetEnv:       []string{"PRIVATE_TOKEN=secret", "__leapmux_pin_TEST_1=target"},
		StripEnvKeys: []string{"__LEAPMUX_PIN_test_0_"},
	}, "test", []string{"__LEAPMUX_PIN_test_0=existing"})
	require.Len(t, prepared.pins, 2)
	assert.Equal(t, "__LEAPMUX_PIN_test_0__", prepared.pins[0].source)
	assert.Equal(t, "__LEAPMUX_PIN_test_1_", prepared.pins[1].source)
	assert.Equal(t, []string{"original=value", "__LEAPMUX_PIN_test_0__=secret", "__LEAPMUX_PIN_test_1_=target"}, prepared.pinEnvironment([]string{"original=value"}))
}

func TestFishPinnedEnvironmentAndConditionalArguments(t *testing.T) {
	t.Parallel()
	shell, err := exec.LookPath("fish")
	if err != nil {
		t.Skip("fish is not installed")
	}
	binary, err := os.Executable()
	require.NoError(t, err)
	for _, token := range []string{"", "present"} {
		t.Run("token="+token, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			cmd, _, meta := wrapTestCommand(t, ctx, WrapSpec{Shell: shell, Launch: Spec{Program: binary}, BaseArgs: []string{"-test.run=^TestPinnedEnvironmentChild$"}, SetEnv: []string{"PRIVATE_TOKEN=protected", "LEAPMUX_TEST_EMPTY="}, StripEnvKeys: []string{"LEAPMUX_TEST_STRIPPED"}, EnvGated: &EnvGatedArgs{EnvVars: []string{"LEAPMUX_TEST_AUTH"}, MetaKey: "default_args", Args: []string{"gated-argument"}}})
			cmd.Env = envutil.PinEnv(cmd.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"), "LEAPMUX_PIN_TEST_HELPER=1", "LEAPMUX_TEST_STRIPPED=inherited", "LEAPMUX_TEST_AUTH="+token)
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", output)
			result := readPinResult(t, output)
			assert.False(t, result.StrippedPresent)
			assert.True(t, result.EmptyPresent)
			assert.Empty(t, result.EmptyValue)
			assert.Empty(t, result.Transport)
			assert.Equal(t, "protected", result.Value)
			if token == "" {
				assert.Contains(t, result.Args, "gated-argument")
				assert.Contains(t, string(output), meta+"default_args=true")
			} else {
				assert.NotContains(t, result.Args, "gated-argument")
				assert.Contains(t, string(output), meta+"default_args=false")
			}
		})
	}
}

func TestWrapKeepsAnExplicitlyEmptyEnvironment(t *testing.T) {
	t.Parallel()
	cmd, _, _ := wrapTestCommand(t, t.Context(), WrapSpec{Shell: "/bin/sh", Launch: Spec{Program: "provider"}, Environment: []string{}})
	require.NotNil(t, cmd.Env, "nil would inherit the host environment")
	assert.Empty(t, cmd.Env)
}
