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

	"github.com/leapmux/leapmux/util/procutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const profilePeerIdentityEnv = "LEAPMUX_TEST_PROFILE_COMMAND_IDENTITY"

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
		if err := json.NewEncoder(os.Stdout).Encode(os.Args[index+1:]); err != nil {
			os.Exit(2)
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

func assertProfileCommandExchange(t *testing.T, cmd *exec.Cmd, delimiter string, args []string) {
	t.Helper()
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

func assertProfileCommandCancellation(t *testing.T, ctx context.Context, cancel context.CancelFunc, cmd *exec.Cmd, childMustDiffer bool) {
	t.Helper()
	cmd.Env = append(cmd.Environ(), profilePeerIdentityEnv+"=1")
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	defer func() { _ = stdin.Close() }()
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	owner := procutil.PrepareProcess(cmd)
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
	assert.False(t, child.Runs())
}
