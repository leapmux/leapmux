package launch

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/shellquote"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/leapmux/leapmux/util/procutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const profilePeerIdentityEnv = "LEAPMUX_TEST_PROFILE_COMMAND_IDENTITY"
const profilePeerEnvironmentEnv = "LEAPMUX_TEST_PROFILE_COMMAND_ENVIRONMENT"
const profilePeerStripEnv = "LEAPMUX_TEST_PROFILE_COMMAND_STRIP"
const profilePeerLiteralEnv = "LEAPMUX_TEST_PROFILE_COMMAND_LITERAL"

type profilePeerEnvironment struct {
	Stripped  *string
	Literal   *string
	Transport []string
}

func profilePeerEnvironmentValue(name string) *string {
	value, present := os.LookupEnv(name)
	if !present {
		return nil
	}
	return &value
}

const profilePeerEnv = "LEAPMUX_TEST_PROFILE_COMMAND_PEER"
const profilePeerPayload = `{"message":"한글"}`

func TestProfileCommandPeer(t *testing.T) {
	if os.Getenv(profilePeerEnv) != "1" {
		return
	}
	if os.Getenv(profilePeerIdentityEnv) == "1" {
		if _, err := fmt.Fprintf(os.Stdout, "peer-pid:%d\n", os.Getpid()); err != nil {
			os.Exit(2)
		}
	}
	for index, arg := range os.Args {
		if arg != "--" {
			continue
		}
		encoder := json.NewEncoder(os.Stdout)
		if err := encoder.Encode(os.Args[index+1:]); err != nil {
			os.Exit(2)
		}
		if os.Getenv(profilePeerEnvironmentEnv) == "1" {
			environment := profilePeerEnvironment{
				Stripped:  profilePeerEnvironmentValue(profilePeerStripEnv),
				Literal:   profilePeerEnvironmentValue(profilePeerLiteralEnv),
				Transport: testPinVariableNames(),
			}
			if err := encoder.Encode(environment); err != nil {
				os.Exit(2)
			}
		}
		if _, err := fmt.Fprintf(os.Stdout, "Content-Length: %d\r\n\r\n%s", len(profilePeerPayload), profilePeerPayload); err != nil {
			os.Exit(2)
		}
		input := bufio.NewScanner(os.Stdin)
		for range 2 {
			if !input.Scan() {
				os.Exit(3)
			}
			if _, err := fmt.Fprintln(os.Stdout, "reply:"+input.Text()); err != nil {
				os.Exit(2)
			}
		}
		fmt.Fprintln(os.Stderr, "provider diagnostic")
		os.Exit(37)
	}
	os.Exit(4)
}

func profileCommandExecutable(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	path, err := filepath.Abs(executable)
	require.NoError(t, err)
	return path
}

// wrapTestCommand keeps the preamble available to existing renderer tests.
func wrapTestCommand(t *testing.T, ctx context.Context, spec WrapSpec) (*exec.Cmd, string, string) {
	t.Helper()
	prepared, err := Wrap(ctx, spec)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, prepared.Close()) })
	return prepared.Cmd, prepared.Delimiter, prepared.MetaPrefix
}

func bashInvocationPath(t *testing.T, command string) (string, bool) {
	t.Helper()
	quoted, found := strings.CutPrefix(command, bashInvocationPrefix)
	if !found {
		return "", false
	}
	require.GreaterOrEqual(t, len(quoted), 2)
	require.True(t, strings.HasPrefix(quoted, "'") && strings.HasSuffix(quoted, "'"))
	return filepath.FromSlash(strings.ReplaceAll(quoted[1:len(quoted)-1], `'\''`, "'")), true
}

func renderedWrapScript(t *testing.T, cmd *exec.Cmd) string {
	t.Helper()
	script := cmd.Args[len(cmd.Args)-1]
	path, found := bashInvocationPath(t, script)
	if !found {
		return script
	}
	file, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(file)
}

func assertProfileCandidateExhaustion(t *testing.T, shell string, names ...string) string {
	t.Helper()
	spec, err := Binaries(names...).Resolve(t.Context(), shell, false, "Missing candidate test")
	require.NoError(t, err)
	prepared, err := Wrap(t.Context(), WrapSpec{Shell: shell, Launch: spec})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, prepared.Close()) })
	diagnostics := prepared.NewDiagnostics(1024)
	prepared.Cmd.Stderr = diagnostics
	output, waitErr := prepared.Cmd.Output()
	diagnostics.Finish()
	err = diagnostics.ExitError(waitErr, false)
	require.ErrorIs(t, err, ErrNoCandidate)
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	assert.Equal(t, 127, exit.ExitCode())
	for _, name := range names {
		assert.ErrorContains(t, err, name)
	}
	assert.NotContains(t, string(output), prepared.Delimiter)
	return diagnostics.String()
}

const requiredProfileShellsEnv = "LEAPMUX_REQUIRE_PROFILE_SHELLS"

func requireProfileShell(t *testing.T, row, name string) string {
	t.Helper()
	shell, err := exec.LookPath(name)
	if err == nil {
		return shell
	}
	if slices.Contains(strings.Split(os.Getenv(requiredProfileShellsEnv), ","), row) ||
		runtime.GOOS == "windows" && os.Getenv("GITHUB_ACTIONS") == "true" {
		require.NoError(t, err, "required shell %s", row)
	}
	t.Skipf("%s is unavailable: %v", row, err)
	return ""
}

// Load the fixture in the command's scope without an on-disk script or a policy change.
func prependPowerShellProfile(cmd *exec.Cmd, source string) {
	cmd.Args = slices.Insert(cmd.Args, 1, "-NoProfile")
	last := len(cmd.Args) - 1
	cmd.Args[last] = ". {\n" + source + "\n}\n" + cmd.Args[last]
}

func TestProfileCommandPowerShellRegistrySizedProbe(t *testing.T) {
	interpreters := []string{"pwsh"}
	if runtime.GOOS == "windows" {
		interpreters = append(interpreters, "powershell")
	}
	for _, name := range interpreters {
		t.Run(name, func(t *testing.T) {
			shell := requireProfileShell(t, name, name)
			const count = 36
			const nonce = "__POWERSHELL_REGISTRY_PROBE__"
			const alias = "leapmux-registry-profile-alias"
			const function = "leapmux-registry-profile-function"
			names := make([]string, count)
			descriptors := []string{"cli", "agent-tool", "model-bridge", "language-server", "profile-wrap-application"}
			for index := range names {
				names[index] = fmt.Sprintf("leapmux-%s-%02d", descriptors[index%len(descriptors)], index)
			}
			executable := filepath.Base(shell)
			names[0], names[13], names[count-1] = executable, alias, function
			want := make(map[string]ProbeResult, count)
			for _, candidate := range names {
				want[candidate] = ProbeNo
			}
			want[executable], want[alias], want[function] = ProbeYes, ProbeYes, ProbeYes

			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			script := renderCommandProbe(terminal.ShellBaseName(shell), nonce, names, false)
			cmd, err := newShellCommand(ctx, shell, false, script, t.TempDir(), nil)
			require.NoError(t, err)
			quote := shellquote.PowerShell.Quote
			prependPowerShellProfile(cmd, "$env:PATH = "+quote(filepath.Dir(shell))+"\n"+
				"Set-Alias -Name "+alias+" -Value "+quote(shell)+"\n"+
				"function "+function+" { throw 'probing must not run the function' }\n")
			output, err := cmd.Output()
			require.NoError(t, err, "%s", output)
			require.True(t, strings.HasSuffix(strings.ReplaceAll(string(output), "\r\n", "\n"), nonce+":complete\n"),
				"the batch did not emit an exact completion record: %q", output)
			results, path := parseCommandProbe(string(output), nonce, names, false)
			assert.Equal(t, want, results)
			assert.Empty(t, path)
		})
	}
}

func assertProfileCommandExchange(t *testing.T, cmd *exec.Cmd, delimiter string, args []string) {
	t.Helper()
	assertProfileCommandExchangeWithEnvironment(t, cmd, delimiter, args, nil)
}

func assertProfileCommandExchangeWithEnvironment(t *testing.T, cmd *exec.Cmd, delimiter string, args []string, wantEnvironment *profilePeerEnvironment) {
	t.Helper()
	if wantEnvironment != nil {
		cmd.Env = append(cmd.Environ(), profilePeerEnvironmentEnv+"=1")
	}
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	var diagnostics bytes.Buffer
	cmd.Stderr = &diagnostics
	owner := procutil.PrepareProcess(cmd)
	require.NoError(t, owner.Start())
	t.Cleanup(func() {
		assert.NoError(t, owner.Close())
		_ = owner.Wait()
	})

	reader := bufio.NewReader(stdout)
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			_ = stdin.Close()
			status := owner.Wait()
			t.Fatalf("readiness absent: %v; shell status: %v; stderr: %s", readErr, status, diagnostics.String())
		}
		if strings.TrimRight(line, "\r\n") == delimiter {
			break
		}
	}
	line, err := reader.ReadBytes('\n')
	require.NoError(t, err)
	var got []string
	require.NoError(t, json.Unmarshal(line, &got), "provider output: %q", line)
	assert.Equal(t, args, got)
	if wantEnvironment != nil {
		line, err := reader.ReadBytes('\n')
		require.NoError(t, err)
		var gotEnvironment profilePeerEnvironment
		require.NoError(t, json.Unmarshal(line, &gotEnvironment), "provider environment: %q", line)
		assert.Equal(t, *wantEnvironment, gotEnvironment)
	}
	expected := fmt.Appendf(nil, "Content-Length: %d\r\n\r\n%s", len(profilePeerPayload), profilePeerPayload)
	gotFrame := make([]byte, len(expected))
	_, err = io.ReadFull(reader, gotFrame)
	require.NoError(t, err)
	require.Equal(t, expected, gotFrame)
	for _, request := range []string{"first", "second"} {
		_, err = io.WriteString(stdin, request+"\n")
		require.NoError(t, err)
		reply, err := reader.ReadString('\n')
		require.NoError(t, err)
		assert.Equal(t, "reply:"+request+"\n", reply)
	}
	require.NoError(t, stdin.Close())
	var exit *exec.ExitError
	require.ErrorAs(t, owner.Wait(), &exit)
	assert.Equal(t, 37, exit.ExitCode())
	assert.Contains(t, diagnostics.String(), "provider diagnostic")
}

type observedProfileExits map[procutil.ProcessIdentity]error

func (exits observedProfileExits) ObserveExit(_ context.Context, identity procutil.ProcessIdentity, err error) error {
	exits[identity] = err
	return nil
}

func assertProfileCommandCancellation(t *testing.T, ctx context.Context, cancel context.CancelFunc, cmd *exec.Cmd, childMustDiffer bool) {
	t.Helper()
	cmd.Env = append(cmd.Environ(), profilePeerIdentityEnv+"=1")
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	defer func() { _ = stdin.Close() }()
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	exits := make(observedProfileExits)
	owner := procutil.PrepareProcessWithExitObserver(cmd, exits)
	require.NoError(t, owner.Start())
	t.Cleanup(func() { assert.NoError(t, owner.Close()); _ = owner.Wait() })

	if file, ok := stdout.(*os.File); ok && runtime.GOOS != "windows" {
		require.NoError(t, file.SetReadDeadline(time.Now().Add(15*time.Second)))
	}
	lines := bufio.NewScanner(stdout)
	var childPID int
	for lines.Scan() {
		if pid, ok := strings.CutPrefix(lines.Text(), "peer-pid:"); ok {
			childPID, err = strconv.Atoi(pid)
			require.NoError(t, err)
			break
		}
	}
	require.Positive(t, childPID, "the attached provider must report readiness")
	root, found := procutil.IdentifyProcess(owner.PID())
	require.True(t, found)
	child, found := procutil.IdentifyProcess(childPID)
	require.True(t, found)
	if childMustDiffer {
		require.NotEqual(t, root, child)
	}
	require.NoError(t, owner.Capture(ctx))
	cancel()
	require.Error(t, owner.Wait())
	require.NoError(t, owner.Close())
	assert.False(t, root.Runs())
	// An exited child can keep its PID until its parent reaps it.
	exit, observed := exits[child]
	require.True(t, observed, "the owner must observe the exit of provider process %d", child.PID)
	assert.NoError(t, exit)
}
