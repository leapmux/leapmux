package main

import (
	"bytes"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/filelock"
	"github.com/leapmux/leapmux/lifecycle"
)

// The lock-holder helper: a child process takes the named lock and
// holds it until the test ends, so a verb observes a lock ANOTHER
// process holds.
const lockHolderEnv = "LEAPMUX_TEST_LOCK_HOLDER_PATH"

// TestHelperProcessLockHolder holds the named lock while its process
// lives. The test binary re-executes itself here through startLockHolder.
func TestHelperProcessLockHolder(t *testing.T) {
	path := os.Getenv(lockHolderEnv)
	if path == "" {
		return
	}
	f, err := filelock.Open(path, false)
	if err != nil {
		os.Exit(2)
	}
	held, err := filelock.TryLock(f)
	if err != nil || !held {
		os.Exit(3)
	}
	// Park for the life of the process; the test kills it. A timer,
	// not a bare channel: the runtime's deadlock detector treats an
	// all-channel park as fatal and would end the holder early.
	for {
		time.Sleep(time.Hour)
	}
}

// startLockHolder starts the holder child on the lock path and waits
// until it actually holds the lock: the child's own signal, not a
// timed guess.
func startLockHolder(t *testing.T, path string) (stop func()) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcessLockHolder$")
	cmd.Env = append(os.Environ(), lockHolderEnv+"="+path)
	require.NoError(t, cmd.Start())
	var once sync.Once
	stop = func() {
		once.Do(func() {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		})
	}
	t.Cleanup(stop)
	require.Eventually(t, func() bool {
		held, err := lifecycle.ProbeLockAt(path)
		return err == nil && held
	}, 30*time.Second, 50*time.Millisecond, "the holder child never took the lock at %s", path)
	return stop
}

// TestStopControlsForegroundProcess: the stop verb, run by ANOTHER CLI
// process, stops a real foreground runtime and waits until its lock
// releases. A registered worker against a stub hub provides the
// runtime.
func TestStopControlsForegroundProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns two real processes")
	}
	// A hub endpoint that accepts and holds every connection: a raw
	// listener, not an httptest.Server, because a raw listener's Close
	// cannot block on a half-open stream the way a served one can.
	hubListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	heldConns := make(chan net.Conn, 8)
	go func() {
		for {
			conn, acceptErr := hubListener.Accept()
			if acceptErr != nil {
				return
			}
			heldConns <- conn
		}
	}()
	t.Cleanup(func() {
		_ = hubListener.Close()
		for {
			select {
			case conn := <-heldConns:
				_ = conn.Close()
			default:
				return
			}
		}
	})
	hubURL := "http://" + hubListener.Addr().String()

	dataDir := t.TempDir()
	cfg := writeRegisteredStateAgainst(t, hubURL)
	require.NoError(t, os.MkdirAll(cfg.DataDir, 0o700))
	// Move the state into the lifecycle data dir the run verb uses.
	stateBytes, err := os.ReadFile(cfg.StatePath())
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "state.json"), stateBytes, 0o600))

	cmd := exec.Command(os.Args[0], "worker", "run", "--data-dir", dataDir, "--log-level", "info")
	cmd.Env = append(os.Environ(), runMainEnv+"=1")
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	// Wait until the worker holds its runtime lock -- the evidence the
	// stop verb acts on, not a tab count.
	canonical := mustCanonicalDir(t, dataDir)
	paths := lifecycle.PathsOf(canonical)
	require.Eventually(t, func() bool {
		held, _ := lifecycle.ProbeRuntimeLock(paths)
		return held
	}, 30*time.Second, 100*time.Millisecond, "the foreground runtime never took its lock: %s", output.String())

	// The stop verb from THIS process stops the child.
	require.NoError(t, runModeCommand("worker", []string{"stop", "--data-dir", dataDir}))

	select {
	case err := <-exited:
		require.NoError(t, err, "the stopped runtime exits cleanly: %s", output.String())
	case <-time.After(30 * time.Second):
		t.Fatalf("the runtime did not exit after the stop: %s", output.String())
	}
	assert.False(t, lockHeld(paths), "the runtime lock released before the stop verb returned")
}

// lockHeld probes a lock path.
func lockHeld(paths lifecycle.Paths) bool {
	held, err := lifecycle.ProbeRuntimeLock(paths)
	return err == nil && held
}

// TestConcurrentLifecycleCommands: synchronized stop and disable
// commands cannot remove a native definition under an active startup,
// and duplicate stops create no second instance. The serialized
// operation lock plus the runtime-lock probe give the guarantee.
func TestConcurrentLifecycleCommands(t *testing.T) {
	dataDir := t.TempDir()
	canonical := mustCanonicalDir(t, dataDir)
	paths := lifecycle.PathsOf(canonical)

	// A saved specification: disable must defend it.
	require.NoError(t, os.MkdirAll(paths.Dir, 0o700))
	require.NoError(t, lifecycle.WriteServiceSpec(paths.ServiceSpec, lifecycle.ServiceSpec{
		InstanceID:    "conc0000000000001",
		Mode:          lifecycle.RoleWorker,
		Startup:       lifecycle.StartupLogin,
		AccountID:     lifecycle.CurrentAccountID(),
		AccountName:   "test",
		Executable:    "/usr/bin/true",
		ConfigFile:    filepath.Join(dataDir, "worker.yaml"),
		DataDirectory: canonical,
	}))
	// A published runtime record the stop verbs reconcile.
	require.NoError(t, lifecycle.WriteControlToken(paths.ControlToken, "token-material"))
	require.NoError(t, lifecycle.WriteRuntimeRecord(paths.RuntimeRecord, lifecycle.RuntimeRecord{
		InstanceID: "conc0000000000001",
		AttemptID:  "e9a0425886d148f68a8f94716ed47a78",
		Mode:       lifecycle.RoleWorker,
		DataDir:    canonical,
		ControlURL: "unix:/nonexistent.sock",
	}))

	// A running instance: a holder child owns the runtime lock.
	stopHolder := startLockHolder(t, paths.RuntimeLock)
	defer stopHolder()
	require.True(t, lockHeld(paths), "the holder child owns the runtime lock")

	// disable refuses while the instance runs.
	err := runModeCommand("worker", []string{"disable", "--data-dir", dataDir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stop it", "disable names the stop requirement")

	// Two concurrent stops: both answer, exactly one instance existed,
	// and the second reports the lock held by the first stop's wait.
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			results[slot] = runModeCommand("worker", []string{"stop", "--timeout", "300ms", "--data-dir", dataDir})
		}(i)
	}
	wg.Wait()
	for i, err := range results {
		// The instance never answers its endpoint, so both stops
		// report what the evidence says -- the serialized operation
		// lock or the unresponsive endpoint -- and neither removed
		// anything.
		if err != nil {
			assert.True(t,
				strings.Contains(err.Error(), "unresponsive") ||
					strings.Contains(err.Error(), "operation lock") ||
					strings.Contains(err.Error(), "did not accept"),
				"stop %d reports the held instance: %v", i, err)
		}
	}
	// The holder still owns the lock: no stop killed another process.
	assert.True(t, lockHeld(paths))
}
