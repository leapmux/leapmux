//go:build unix

package procutil

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/shirou/gopsutil/v4/process"
	"github.com/stretchr/testify/require"
)

const rootExitRole = "LEAPMUX_TEST_OWNER_ROOT_EXIT_ROLE"
const rootExitAddress = "LEAPMUX_TEST_OWNER_ROOT_EXIT_ADDRESS"

type rootExitReceipt struct {
	Type string `json:"type"`
	PID  int    `json:"pid"`
}

// The relay exits after EOF. Its detached engine waits for a control request.
func TestHelperProcessOwnerRootExit(t *testing.T) {
	role := os.Getenv(rootExitRole)
	if role != "relay" && role != "engine" && role != "leaf" {
		return
	}
	if role == "engine" || role == "leaf" {
		connection, err := net.Dial("tcp", os.Getenv(rootExitAddress))
		if err != nil {
			os.Exit(2)
		}
		defer func() { _ = connection.Close() }()
		send := func(kind string) {
			if err := json.NewEncoder(connection).Encode(rootExitReceipt{Type: kind, PID: os.Getpid()}); err != nil {
				os.Exit(3)
			}
		}
		send("ready")
		commands := bufio.NewScanner(connection)
		for commands.Scan() {
			switch commands.Text() {
			case "ping":
				send("pong")
			case "release":
				os.Exit(0)
			case "spawn":
				child := exec.Command(os.Args[0], "-test.run=^TestHelperProcessOwnerRootExit$")
				child.Env = append(os.Environ(), rootExitRole+"=leaf")
				DetachFromTerminal(child)
				child.Stdout, child.Stderr = io.Discard, io.Discard
				if err := child.Start(); err != nil {
					os.Exit(6)
				}
				if err := json.NewEncoder(connection).Encode(rootExitReceipt{Type: "child-started", PID: child.Process.Pid}); err != nil {
					os.Exit(7)
				}
			default:
				os.Exit(4)
			}
		}
		os.Exit(0)
	}
	engine := exec.Command(os.Args[0], "-test.run=^TestHelperProcessOwnerRootExit$")
	engine.Env = append(os.Environ(), rootExitRole+"=engine")
	DetachFromTerminal(engine)
	engine.Stdout, engine.Stderr = io.Discard, io.Discard
	if err := engine.Start(); err != nil {
		os.Exit(5)
	}
	fmt.Printf("root-child:%d\n", engine.Process.Pid)
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

type rootExitFixture struct {
	cmd      *exec.Cmd
	owner    *ProcessOwner
	stdin    io.WriteCloser
	engine   ProcessIdentity
	control  net.Conn
	reader   *json.Decoder
	listener net.Listener
}

func startRootExitFixture(t *testing.T, home string) *rootExitFixture {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperProcessOwnerRootExit$")
	cmd.Env = append(os.Environ(), "HOME="+home, rootExitRole+"=relay", rootExitAddress+"="+listener.Addr().String())
	DetachFromTerminal(cmd)
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	cmd.Stderr = io.Discard
	fixture := &rootExitFixture{cmd: cmd, owner: PrepareProcess(cmd), stdin: stdin, listener: listener}
	t.Cleanup(func() {
		_, _ = fixture.engine.Kill()
		if fixture.control != nil {
			_ = fixture.control.Close()
		}
		_ = fixture.stdin.Close()
		_ = fixture.owner.Close()
		cancel()
		if cmd.ProcessState == nil {
			_ = cmd.Wait()
		}
	})
	require.NoError(t, fixture.owner.Start())
	childPID := make(chan int, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err != nil {
			return
		}
		text, found := strings.CutPrefix(strings.TrimSpace(line), "root-child:")
		if !found {
			return
		}
		pid, err := strconv.Atoi(text)
		if err == nil {
			childPID <- pid
		}
	}()
	accepted := make(chan net.Conn, 1)
	acceptError := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			acceptError <- err
			return
		}
		accepted <- connection
	}()
	select {
	case fixture.control = <-accepted:
	case err := <-acceptError:
		require.NoError(t, err)
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the root's exact child did not connect")
	}
	require.NoError(t, fixture.control.SetDeadline(time.Now().Add(30*time.Second)))
	fixture.reader = json.NewDecoder(fixture.control)
	var receipt rootExitReceipt
	require.NoError(t, fixture.reader.Decode(&receipt))
	require.Equal(t, "ready", receipt.Type)
	select {
	case pid := <-childPID:
		require.Equal(t, pid, receipt.PID, "the child socket must match the relay's child PID")
		var identified bool
		fixture.engine, identified = IdentifyProcess(pid)
		require.True(t, identified)
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the root did not identify its child")
	}
	return fixture
}

func (f *rootExitFixture) requirePong(t *testing.T) {
	t.Helper()
	require.NoError(t, f.control.SetDeadline(time.Now().Add(30*time.Second)))
	_, err := io.WriteString(f.control, "ping\n")
	require.NoError(t, err)
	var receipt rootExitReceipt
	require.NoError(t, f.reader.Decode(&receipt))
	require.Equal(t, rootExitReceipt{Type: "pong", PID: f.engine.PID}, receipt)
}

func TestProcessOwnerEndsOnlyItsDetachedChildAfterRootExit(t *testing.T) {
	home := t.TempDir()
	first := startRootExitFixture(t, home)
	second := startRootExitFixture(t, home)
	first.requirePong(t)
	second.requirePong(t)
	// Capture while the live parent still proves the exact child ancestry.
	require.NoError(t, first.owner.Capture(t.Context()))
	require.NoError(t, first.stdin.Close())
	require.NoError(t, first.cmd.Wait())
	require.NoError(t, first.owner.Close())
	_, _ = io.WriteString(first.control, "ping\n")
	var receipt rootExitReceipt
	err := first.reader.Decode(&receipt)
	if err == nil {
		t.Fatalf("the exact child still answers after its root exits: %+v", receipt)
	}
	require.True(t, errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET), "the owned child's socket must close: %v", err)
	second.requirePong(t)
}

func TestProcessOwnerDoesNotAdoptAnUnobservedDetachedChildAfterRootExit(t *testing.T) {
	home := t.TempDir()
	first := startRootExitFixture(t, home)
	second := startRootExitFixture(t, home)
	first.requirePong(t)
	second.requirePong(t)
	// No live ancestry capture exists for this owner before its root exits.
	require.NoError(t, first.stdin.Close())
	require.NoError(t, first.cmd.Wait())
	require.NoError(t, first.owner.Close())
	first.requirePong(t)
	second.requirePong(t)
}

func TestProcessOwnerRefreshesKnownEngineDescendantsAfterRelayExit(t *testing.T) {
	home := t.TempDir()
	first := startRootExitFixture(t, home)
	second := startRootExitFixture(t, home)
	require.NoError(t, first.owner.Capture(t.Context()))
	require.NoError(t, first.stdin.Close())
	require.NoError(t, first.cmd.Wait())
	first.requirePong(t)
	_, err := io.WriteString(first.control, "spawn\n")
	require.NoError(t, err)
	var spawn rootExitReceipt
	require.NoError(t, first.reader.Decode(&spawn))
	require.Equal(t, "child-started", spawn.Type)
	child, exists := IdentifyProcess(spawn.PID)
	require.True(t, exists)
	t.Cleanup(func() { _, _ = child.Kill() })
	parent, err := (&process.Process{Pid: int32(child.PID)}).PpidWithContext(t.Context())
	require.NoError(t, err)
	require.Equal(t, int32(first.engine.PID), parent)
	require.True(t, first.engine.Runs())
	type acceptResult struct {
		connection net.Conn
		err        error
	}
	accepted := make(chan acceptResult, 1)
	go func() {
		connection, err := first.listener.Accept()
		accepted <- acceptResult{connection: connection, err: err}
	}()
	var connection net.Conn
	select {
	case result := <-accepted:
		require.NoError(t, result.err)
		connection = result.connection
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the known engine's exact child did not connect")
	}
	t.Cleanup(func() { _ = connection.Close() })
	require.NoError(t, connection.SetDeadline(time.Now().Add(30*time.Second)))
	reader := json.NewDecoder(connection)
	var ready rootExitReceipt
	require.NoError(t, reader.Decode(&ready))
	require.Equal(t, rootExitReceipt{Type: "ready", PID: child.PID}, ready)
	require.NoError(t, first.owner.Close())
	second.requirePong(t)
	_, _ = io.WriteString(connection, "ping\n")
	var response rootExitReceipt
	err = reader.Decode(&response)
	if err == nil {
		t.Fatalf("the known engine's exact child still answers after cleanup: %+v", response)
	}
	require.True(t, errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET), "the exact child socket must close: %v", err)
}

func TestBindDescendantsRequiresTheLiveOriginalRelay(t *testing.T) {
	fixture := startRootExitFixture(t, t.TempDir())
	require.NoError(t, fixture.stdin.Close())
	require.NoError(t, fixture.cmd.Wait())
	require.ErrorContains(t, fixture.owner.BindDescendants(t.Context()), "before its descendants could be bound")
	fixture.requirePong(t)
}

func TestBindDescendantsKeepsItsEngineAfterAnUnexpectedRelayExit(t *testing.T) {
	home := t.TempDir()
	first := startRootExitFixture(t, home)
	second := startRootExitFixture(t, home)
	require.NoError(t, first.owner.BindDescendants(t.Context()))
	require.NoError(t, first.cmd.Process.Kill())
	require.Error(t, first.cmd.Wait())
	require.NoError(t, first.owner.Close())
	second.requirePong(t)
	_, _ = io.WriteString(first.control, "ping\n")
	var response rootExitReceipt
	err := first.reader.Decode(&response)
	require.True(t, errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET), "the exact bound engine must close: %v", err)
}

func TestProcessOwnerConcurrentCaptureCancelAndCloseKeepTheOtherEngine(t *testing.T) {
	home := t.TempDir()
	first := startRootExitFixture(t, home)
	second := startRootExitFixture(t, home)
	require.NoError(t, first.owner.BindDescendants(t.Context()))
	start := make(chan struct{})
	results := make(chan error, 3)
	var workers sync.WaitGroup
	for _, operation := range []func() error{
		func() error { return first.owner.Capture(t.Context()) }, first.owner.Cancel, first.owner.Close,
	} {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			results <- operation()
		}()
	}
	close(start)
	completed := make(chan struct{})
	go func() { workers.Wait(); close(completed) }()
	select {
	case <-completed:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the concurrent owner operations did not finish")
	}
	for range 3 {
		require.NoError(t, <-results)
	}
	second.requirePong(t)
	_, _ = io.WriteString(first.control, "ping\n")
	var response rootExitReceipt
	err := first.reader.Decode(&response)
	require.True(t, errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET), "the bound engine must close: %v", err)
}
