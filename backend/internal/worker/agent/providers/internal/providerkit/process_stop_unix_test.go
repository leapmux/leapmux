//go:build unix

package providerkit

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
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/util/procutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const detachedLifetimeRole = "LEAPMUX_TEST_DETACHED_LIFETIME_ROLE"
const detachedLifetimeAddress = "LEAPMUX_TEST_DETACHED_LIFETIME_ADDRESS"

type detachedLifetimeEvent struct {
	Type string `json:"type"`
	PID  int    `json:"pid"`
}

// The relay waits for its detached engine after EOF. The engine waits for an explicit release.
func TestHelperDetachedProcessLifetime(t *testing.T) {
	role := os.Getenv(detachedLifetimeRole)
	if role != "relay" && role != "engine" {
		return
	}
	if role == "engine" {
		connection, err := net.Dial("tcp", os.Getenv(detachedLifetimeAddress))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		defer func() { _ = connection.Close() }()
		var writeMu sync.Mutex
		send := func(kind string) {
			writeMu.Lock()
			defer writeMu.Unlock()
			if err := json.NewEncoder(connection).Encode(detachedLifetimeEvent{Type: kind, PID: os.Getpid()}); err != nil {
				os.Exit(3)
			}
		}
		send("ready")
		go func() {
			_, _ = io.Copy(io.Discard, os.Stdin)
			send("stdin-eof")
		}()
		commands := bufio.NewScanner(connection)
		for commands.Scan() {
			switch commands.Text() {
			case "ping":
				send("pong")
			case "release":
				os.Exit(0)
			default:
				os.Exit(4)
			}
		}
		os.Exit(0)
	}
	engine := exec.Command(os.Args[0], "-test.run=^TestHelperDetachedProcessLifetime$")
	engine.Env = append(os.Environ(), detachedLifetimeRole+"=engine")
	procutil.DetachFromTerminal(engine)
	engine.Stdout, engine.Stderr = io.Discard, io.Discard
	input, err := engine.StdinPipe()
	if err != nil {
		os.Exit(5)
	}
	if err := engine.Start(); err != nil {
		os.Exit(6)
	}
	fmt.Printf("relay-ready:%d\n", engine.Process.Pid)
	_, _ = io.Copy(io.Discard, os.Stdin)
	_ = input.Close()
	if err := engine.Wait(); err != nil {
		os.Exit(7)
	}
	os.Exit(0)
}

type detachedLifetimeFixture struct {
	process *Process
	cancel  context.CancelFunc
	engine  procutil.ProcessIdentity
	control net.Conn
	reader  *json.Decoder
}

func startDetachedLifetimeFixture(t *testing.T, home string) *detachedLifetimeFixture {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperDetachedProcessLifetime$")
	cmd.Env = append(os.Environ(), "HOME="+home, detachedLifetimeRole+"=relay", detachedLifetimeAddress+"="+listener.Addr().String())
	procutil.DetachFromTerminal(cmd)
	pipes, err := SetupProcessPipes(cmd, cancel)
	require.NoError(t, err)
	stdout, stderr := pipes.Stdout(), pipes.Stderr()
	process := NewProcess(agent.Options{AgentID: "detached-lifetime"}, ProcessLaunch{ProviderName: "kiro-fixture", ShutdownGrace: 0, PreambleDelimiter: "", PreambleMetaPrefix: ""}, pipes, ctx, cancel)
	require.NoError(t, process.StartCmd())
	process.DrainStderr(stderr)
	type readyEngine struct {
		identity procutil.ProcessIdentity
		known    bool
	}
	ready := make(chan readyEngine, 1)
	go process.ReadLines(bufio.NewScanner(stdout), func(line []byte) {
		if value, found := strings.CutPrefix(string(line), "relay-ready:"); found {
			pid, err := strconv.Atoi(value)
			if err == nil {
				identity, known := procutil.IdentifyProcess(pid)
				ready <- readyEngine{identity: identity, known: known}
			}
		}
	})
	accept := make(chan net.Conn, 1)
	acceptError := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			acceptError <- err
			return
		}
		accept <- connection
	}()
	fixture := &detachedLifetimeFixture{process: &process, cancel: cancel}
	t.Cleanup(func() {
		fixture.cancel()
		fixture.process.Stop()
		_ = fixture.process.Wait()
		identity := fixture.engine
		if identity.IsZero() {
			select {
			case event := <-ready:
				identity = event.identity
			default:
			}
		}
		_, _ = identity.Kill()
		if fixture.control != nil {
			_ = fixture.control.Close()
		}
	})
	select {
	case fixture.control = <-accept:
	case err := <-acceptError:
		require.NoError(t, err)
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the detached engine did not connect")
	}
	require.NoError(t, fixture.control.SetDeadline(time.Now().Add(30*time.Second)))
	fixture.reader = json.NewDecoder(fixture.control)
	var event detachedLifetimeEvent
	require.NoError(t, fixture.reader.Decode(&event))
	require.Equal(t, "ready", event.Type)
	select {
	case owned := <-ready:
		require.True(t, owned.known, "the relay's exact child must have a creation identity")
		require.Equal(t, owned.identity.PID, event.PID, "the socket must belong to the relay's declared child")
		fixture.engine = owned.identity
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the relay fixture did not report readiness")
	}
	return fixture
}

func (f *detachedLifetimeFixture) requirePong(t *testing.T) {
	t.Helper()
	require.NoError(t, f.control.SetDeadline(time.Now().Add(30*time.Second)))
	_, err := io.WriteString(f.control, "ping\n")
	require.NoError(t, err)
	var event detachedLifetimeEvent
	require.NoError(t, f.reader.Decode(&event))
	require.Equal(t, detachedLifetimeEvent{Type: "pong", PID: f.engine.PID}, event)
	assert.True(t, f.engine.Runs(), "the other engine keeps its exact creation identity")
}

func (f *detachedLifetimeFixture) requireClosed(t *testing.T) {
	t.Helper()
	require.NoError(t, f.control.SetDeadline(time.Now().Add(30*time.Second)))
	_, _ = io.WriteString(f.control, "ping\n")
	for index := 0; index < 2; index++ {
		var event detachedLifetimeEvent
		err := f.reader.Decode(&event)
		if err != nil {
			require.True(t, errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET), "the exact owned engine must close its socket: %v", err)
			return
		}
		if event.Type == "stdin-eof" {
			continue
		}
		t.Fatalf("the detached engine still answers after its relay ended: %+v", event)
	}
	t.Fatal("the detached engine did not close its control socket")
}

func TestContextCancellationEndsOnlyItsDetachedEngine(t *testing.T) {
	home := t.TempDir()
	first := startDetachedLifetimeFixture(t, home)
	second := startDetachedLifetimeFixture(t, home)
	first.requirePong(t)
	second.requirePong(t)
	first.cancel()
	select {
	case <-first.process.ProcessDone():
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the cancelled relay did not exit")
	}
	first.requireClosed(t)
	second.requirePong(t)
}

func TestProcessStopEndsOnlyItsDetachedEngineAfterEOF(t *testing.T) {
	home := t.TempDir()
	first := startDetachedLifetimeFixture(t, home)
	second := startDetachedLifetimeFixture(t, home)
	first.requirePong(t)
	second.requirePong(t)
	finished := make(chan struct{})
	go func() {
		first.process.Stop()
		close(finished)
	}()
	var event detachedLifetimeEvent
	require.NoError(t, first.reader.Decode(&event))
	require.Equal(t, detachedLifetimeEvent{Type: "stdin-eof", PID: first.engine.PID}, event)
	select {
	case <-finished:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the stopped relay did not exit")
	}
	first.requireClosed(t)
	second.requirePong(t)
}
