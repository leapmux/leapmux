//go:build unix

package gitutil

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/util/procutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	gitPipeGuard      = 30 * time.Second
	gitPipeRoleEnv    = "LEAPMUX_TEST_GIT_PIPE_ROLE"
	gitPipeAddressEnv = "LEAPMUX_TEST_GIT_PIPE_ADDRESS"
	gitPipeOnceEnv    = "LEAPMUX_TEST_GIT_PIPE_ONCE"
)

// The first filter waits for commands. Later invocations preserve the input.
// A separate holder can keep stderr open after the filter and Git exit.
func TestGitPipeHelperProcess(t *testing.T) {
	role := os.Getenv(gitPipeRoleEnv)
	if role != "filter" && role != "holder" {
		return
	}
	if err := runGitPipeHelper(role); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func runGitPipeHelper(role string) error {
	if role == "filter" {
		marker, err := os.OpenFile(os.Getenv(gitPipeOnceEnv), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			_, copyErr := io.Copy(os.Stdout, os.Stdin)
			return copyErr
		}
		if err != nil {
			return err
		}
		if err := marker.Close(); err != nil {
			return err
		}
	}
	connection, err := net.DialTimeout("tcp", os.Getenv(gitPipeAddressEnv), gitPipeGuard)
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()
	if _, err := fmt.Fprintf(connection, "%s %d\n", role, os.Getpid()); err != nil {
		return err
	}
	commands := bufio.NewScanner(connection)
	for commands.Scan() {
		switch commands.Text() {
		case "ping":
			if _, err := fmt.Fprintln(connection, "pong"); err != nil {
				return err
			}
		case "spawn":
			if role != "filter" {
				return errors.New("only the filter can start a holder")
			}
			child := exec.Command(os.Args[0], "-test.run=^TestGitPipeHelperProcess$")
			child.Env = envutil.PinEnv(os.Environ(), gitPipeRoleEnv+"=holder")
			child.Stdout = io.Discard
			child.Stderr = os.Stderr
			if err := child.Start(); err != nil {
				return err
			}
			if err := child.Process.Release(); err != nil {
				return err
			}
			if _, err := fmt.Fprintln(connection, "spawned"); err != nil {
				return err
			}
		case "finish":
			if role != "filter" {
				return errors.New("only the filter can return input")
			}
			_, err := io.Copy(os.Stdout, os.Stdin)
			return err
		case "release":
			return nil
		default:
			return errors.New("unknown pipe helper command")
		}
	}
	return commands.Err()
}

type gitPipeControl struct {
	connection net.Conn
	reader     *bufio.Reader
	identity   procutil.ProcessIdentity
	owner      *procutil.ProcessOwner
}

type gitPipeFixture struct {
	listener  *net.TCPListener
	cancel    context.CancelFunc
	result    chan error
	controls  []*gitPipeControl
	completed bool
	started   bool
}

func startGitPipeFixture(t *testing.T) *gitPipeFixture {
	t.Helper()
	repo := initRepo(t)
	data := filepath.Join(repo, "tracked.txt")
	require.NoError(t, os.WriteFile(data, []byte("before\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("tracked.txt filter=leapmux-pipe\n"), 0o600))
	require.NoError(t, Run(t.Context(), repo, "add", ".gitattributes", "tracked.txt"))
	initial, err := os.Stat(data)
	require.NoError(t, err)

	helperDir := t.TempDir()
	address, err := net.ResolveTCPAddr("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	listener, err := net.ListenTCP("tcp", address)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	fixture := &gitPipeFixture{listener: listener, cancel: cancel, result: make(chan error, 1)}
	t.Cleanup(func() {
		cancel()
		_ = listener.Close()
		for _, control := range fixture.controls {
			_ = control.connection.SetWriteDeadline(time.Now().Add(gitPipeGuard))
			_, _ = fmt.Fprintln(control.connection, "release")
		}
		for i := len(fixture.controls) - 1; i >= 0; i-- {
			control := fixture.controls[i]
			_ = control.connection.Close()
			if control.owner != nil {
				assert.NoError(t, control.owner.Close(), "stop the exact pipe helper identity")
			} else {
				_, err := control.identity.Kill()
				assert.NoError(t, err)
			}
		}
		if fixture.started && !fixture.completed {
			guard, stop := context.WithTimeout(context.Background(), gitPipeGuard)
			defer stop()
			select {
			case cleanupErr := <-fixture.result:
				t.Logf("Git completed during cleanup: %v", cleanupErr)
			case <-guard.Done():
				t.Error("Git did not finish after the fixture released its pipes")
			}
		}
	})

	binary, err := os.Executable()
	require.NoError(t, err)
	program := filepath.Join(helperDir, "clean-filter")
	testutil.WriteExecutable(t, program, "#!/bin/sh\nexec \"$LEAPMUX_TEST_GIT_PIPE_BINARY\" -test.run='^TestGitPipeHelperProcess$'\n")
	t.Setenv("LEAPMUX_TEST_GIT_PIPE_BINARY", binary)
	t.Setenv("LEAPMUX_TEST_GIT_PIPE_PROGRAM", program)
	t.Setenv(gitPipeRoleEnv, "filter")
	t.Setenv(gitPipeAddressEnv, listener.Addr().String())
	t.Setenv(gitPipeOnceEnv, filepath.Join(helperDir, "first-filter"))
	require.NoError(t, Run(t.Context(), repo, "config", "filter.leapmux-pipe.clean", `"$LEAPMUX_TEST_GIT_PIPE_PROGRAM"`))
	require.NoError(t, Run(t.Context(), repo, "config", "filter.leapmux-pipe.required", "true"))

	// A different mtime with the same size requires Git to compare content.
	// A different size can establish a change without invoking the filter.
	require.NoError(t, os.WriteFile(data, []byte("after!\n"), 0o600))
	require.NoError(t, os.Chtimes(data, initial.ModTime(), initial.ModTime().Add(2*time.Second)))
	fixture.started = true
	go func() {
		_, err := Output(ctx, repo, "status", "--porcelain")
		fixture.result <- err
	}()
	return fixture
}

func (fixture *gitPipeFixture) acceptHelper(t *testing.T, role string) *gitPipeControl {
	t.Helper()
	require.NoError(t, fixture.listener.SetDeadline(time.Now().Add(gitPipeGuard)))
	connection, err := fixture.listener.Accept()
	require.NoError(t, err, "wait for the real Git filter or inherited-pipe holder")
	control := &gitPipeControl{connection: connection, reader: bufio.NewReader(connection)}
	fixture.controls = append(fixture.controls, control)
	require.NoError(t, connection.SetDeadline(time.Now().Add(gitPipeGuard)))
	line, err := control.reader.ReadString('\n')
	require.NoError(t, err)
	fields := strings.Fields(line)
	require.Len(t, fields, 2)
	require.Equal(t, role, fields[0])
	pid, err := strconv.Atoi(fields[1])
	require.NoError(t, err)
	identity, identified := procutil.IdentifyProcess(pid)
	require.True(t, identified, "the helper must remain alive until release")
	control.identity = identity
	control.owner, err = procutil.OwnStartedProcess(identity)
	require.NoError(t, err)
	// The reply after identification confirms that the helper still owns PID.
	control.command(t, "ping")
	reply, err := control.reader.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "pong\n", reply)
	return control
}

func (control *gitPipeControl) command(t *testing.T, command string) {
	t.Helper()
	require.NoError(t, control.connection.SetWriteDeadline(time.Now().Add(gitPipeGuard)))
	_, err := fmt.Fprintln(control.connection, command)
	require.NoError(t, err)
}

func (fixture *gitPipeFixture) wait(t *testing.T) error {
	t.Helper()
	guard, cancel := context.WithTimeout(t.Context(), gitPipeGuard)
	defer cancel()
	select {
	case err := <-fixture.result:
		fixture.completed = true
		return err
	case <-guard.Done():
		t.Fatal("Git waited for an inherited pipe before the helper was released")
		return nil
	}
}

func TestGitOutputReturnsAfterCancellationWithInheritedStderr(t *testing.T) {
	fixture := startGitPipeFixture(t)
	filter := fixture.acceptHelper(t, "filter")
	require.True(t, filter.identity.Runs(), "the filter must hold stderr before cancellation")
	fixture.cancel()

	err := fixture.wait(t)
	require.Error(t, err)
	var exitError *exec.ExitError
	require.True(t, errors.As(err, &exitError) || errors.Is(err, context.Canceled), "retain the native exit failure or cancellation cause: %v", err)
}

func TestGitOutputReportsWaitDelayAfterSuccessfulRootExit(t *testing.T) {
	fixture := startGitPipeFixture(t)
	filter := fixture.acceptHelper(t, "filter")
	filter.command(t, "spawn")
	holder := fixture.acceptHelper(t, "holder")
	reply, err := filter.reader.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "spawned\n", reply)
	require.True(t, holder.identity.Runs(), "the holder must retain stderr before the filter exits")
	filter.command(t, "finish")

	err = fixture.wait(t)
	require.ErrorIs(t, err, exec.ErrWaitDelay)
}

func TestGitOutputSucceedsAfterReleasedFilter(t *testing.T) {
	fixture := startGitPipeFixture(t)
	filter := fixture.acceptHelper(t, "filter")
	filter.command(t, "finish")
	require.NoError(t, fixture.wait(t))
}
