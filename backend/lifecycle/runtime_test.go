package lifecycle

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/util/useronly"
)

func TestServiceRuntimeRequiresSavedSpecification(t *testing.T) {
	runtimeEnv(t)
	root := filepath.Join(t.TempDir(), "not-created")
	runtime, err := StartRuntime(RuntimeConfig{Mode: RoleHub, DataDir: root, Execution: ExecutionService})
	if runtime != nil {
		t.Cleanup(func() { _ = runtime.Finish(nil) })
	}
	require.ErrorContains(t, err, "service specification")
	assert.NoDirExists(t, root)
}

func TestRuntimeReadySignalsApplicationInitializationOnce(t *testing.T) {
	for _, ready := range []string{ReadinessReady, ReadinessReadyForSetup} {
		t.Run(ready, func(t *testing.T) {
			runtimeEnv(t)
			runtime := startTestRuntime(t, RoleHub, t.TempDir())
			for _, initial := range []string{ReadinessStarting, ReadinessFailed} {
				runtime.SetReadiness(initial)
				select {
				case <-runtime.Ready():
					t.Fatal("control publication or an initialization failure must not signal readiness")
				default:
				}
			}
			runtime.SetReadiness(ready)
			select {
			case <-runtime.Ready():
			default:
				t.Fatal("required initialization must signal readiness")
			}
			require.NotPanics(t, func() { runtime.SetReadiness(ready); runtime.SetReadiness(ReadinessReady) })
		})
	}
}

func TestRuntimeShutdownReleasesUntransferredLease(t *testing.T) {
	runtimeEnv(t)
	r := startTestRuntime(t, RoleWorker, t.TempDir())
	lease := r.Lease()
	t.Cleanup(func() { _ = lease.Close() })
	require.NoError(t, r.Shutdown())
	assert.True(t, lease.Closed(), "shutdown must release a root lease that dispatch never consumed")
	replacement, err := AcquireDirectory(lease.Owner(), lease.Mode(), lease.Path())
	require.NoError(t, err, "a rejected startup must not retain ownership of its data directory")
	require.NoError(t, replacement.Close())
}

func TestRuntimeRejectsInvalidExecutionBeforePublication(t *testing.T) {
	runtimeEnv(t)
	for _, execution := range []ExecutionKind{"", "background"} {
		t.Run(string(execution), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "must-not-exist")
			runtime, err := StartRuntime(RuntimeConfig{Mode: RoleWorker, DataDir: root, Execution: execution})
			if runtime != nil {
				require.NoError(t, runtime.Shutdown())
			}
			require.Error(t, err, "execution must use the declared status vocabulary")
			assert.NoDirExists(t, root)
		})
	}
}

func TestRuntimeStatusDoesNotExposeMutableState(t *testing.T) {
	runtimeEnv(t)
	r := startTestRuntime(t, RoleWorker, t.TempDir())
	workerID, hubURL, lastError, source := new("original worker"), new("original hub"), new("original error"), new("original source")
	r.SetRegistration(RegistrationStatus{State: RegistrationRegistered, WorkerID: workerID, HubURL: hubURL})
	r.SetConnection(ConnectionStatus{State: ConnectionDisconnected, LastError: lastError})
	r.AddDiagnostic(Diagnostic{Kind: "log", Title: "Startup", Body: "failure", Source: source})
	*workerID, *hubURL, *lastError, *source = "changed input", "changed input", "changed input", "changed input"
	first := r.Status()
	assert.Equal(t, new("original worker"), first.Registration.WorkerID)
	assert.Equal(t, new("original hub"), first.Registration.HubURL)
	assert.Equal(t, new("original error"), first.Connection.LastError)
	require.Len(t, first.Diagnostics, 1)
	assert.Equal(t, new("original source"), first.Diagnostics[0].Source)
	*first.Registration.WorkerID, *first.Registration.HubURL = "changed snapshot", "changed snapshot"
	*first.Connection.LastError, *first.Diagnostics[0].Source = "changed snapshot", "changed snapshot"
	second := r.Status()
	assert.Equal(t, new("original worker"), second.Registration.WorkerID)
	assert.Equal(t, new("original hub"), second.Registration.HubURL)
	assert.Equal(t, new("original error"), second.Connection.LastError)
	assert.Equal(t, new("original source"), second.Diagnostics[0].Source)
}

func TestRuntimeShutdownPersistsFailure(t *testing.T) {
	runtimeEnv(t)
	r := startTestRuntime(t, RoleWorker, t.TempDir())
	lease := r.Lease()
	t.Cleanup(func() { _ = lease.Close() })
	r.SetReadiness(ReadinessFailed)
	r.SetProcessState(ProcessFailed)
	r.AddDiagnostic(Diagnostic{Kind: "error", Title: "Startup failed", Body: "open worker database: permission denied"})
	require.NoError(t, r.Shutdown())
	status, err := Observe(context.Background(), lease.Path())
	require.NoError(t, err)
	assert.Equal(t, ProcessFailed, status.Process.State)
	assert.Equal(t, new(ReadinessFailed), status.Readiness)
	assert.NotNil(t, status.Attempt.EndedAt)
	assert.NotNil(t, status.Attempt.ExitCode)
	assert.Contains(t, status.Diagnostics, Diagnostic{
		Kind: "error", Title: "Startup failed", Body: "open worker database: permission denied",
	})
}

// runtimeEnv redirects the control sockets into one private, SHORT
// directory for the test, so tests never collide on the account's real
// one and macOS's sockaddr_un limit cannot bite a deep TMPDIR. The
// literal path length is what bind sees, so the short spelling works
// even though /tmp is a symlink on macOS.
func runtimeEnv(t *testing.T) string {
	t.Helper()
	base := "/tmp"
	if runtime.GOOS == "windows" {
		base = t.TempDir()
	}
	dir, err := useronly.MkdirTemp(base, "lmrt-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("LEAPMUX_RUNTIME_DIR", dir)
	return dir
}

// startTestRuntime starts a foreground runtime for the mode and data
// directory, failing the test on any startup error.
func startTestRuntime(t *testing.T, mode Role, dataDir string) *Runtime {
	t.Helper()
	r, err := StartRuntime(RuntimeConfig{Mode: mode, DataDir: dataDir, Execution: ExecutionForeground})
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Shutdown() })
	return r
}

// A second mode over the same canonical data directory is rejected
// before it writes any state, replaces any token, or installs anything:
// the directory-scoped data lock and the ownership record both refuse.
// Both orders refuse, and the first instance's files survive.
func TestDifferentModesCannotShareDirectory(t *testing.T) {
	runtimeEnv(t)
	for _, order := range [][2]Role{{RoleHub, RoleWorker}, {RoleWorker, RoleHub}, {RoleSolo, RoleHub}} {
		t.Run(order[0].String()+" then "+order[1].String(), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "shared")
			first := startTestRuntime(t, order[0], root)

			second, err := StartRuntime(RuntimeConfig{Mode: order[1], DataDir: root, Execution: ExecutionForeground})
			require.Error(t, err, "the second mode must not acquire the directory")
			assert.Nil(t, second)
			assert.ErrorIs(t, err, ErrDirectoryInUse,
				"the directory-scoped data lock refuses the second mode")

			// The first instance's files are intact.
			paths := pathsFor(mustCanonical(t, root))
			for _, p := range []string{paths.OwnershipRecord, paths.RuntimeRecord, paths.ControlToken} {
				assert.FileExists(t, p)
			}
			record, recordErr := ReadRuntimeRecord(paths.RuntimeRecord)
			require.NoError(t, recordErr)
			assert.Equal(t, order[0], record.Mode, "the first instance's record stands")

			require.NoError(t, first.Shutdown())
		})
	}
}

// A combined instance's component directory and a direct launcher of
// the same component cannot overlap, in both start orders, whether the
// original runs or stopped. The refusal comes from the lock (running)
// or the containing-owner record (stopped), before any credential
// write or registration.
func TestEmbeddedAndStandaloneStorageCannotOverlap(t *testing.T) {
	runtimeEnv(t)
	t.Run("combined running, then direct", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "solo-root")
		combined := startTestRuntime(t, RoleSolo, root)
		workerDir := filepath.Join(root, "worker")

		lease := combined.Lease().TakeComponent(RoleWorker)
		require.NotNil(t, lease)

		direct := NewDirectoryOwner(RoleWorker, mustCanonical(t, workerDir))
		conflicting, err := AcquireDirectory(direct, RoleWorker, workerDir)
		require.Error(t, err, "a direct worker cannot take the combined component's directory")
		assert.Nil(t, conflicting)
		assert.ErrorIs(t, err, ErrDirectoryInUse)
		require.NoError(t, lease.Close())
		require.NoError(t, combined.Shutdown())
	})

	t.Run("direct stopped, then combined", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "solo-root")
		workerDir := filepath.Join(root, "worker")

		// A direct worker owned the directory and stopped; its record
		// stays and identifies it.
		direct := NewDirectoryOwner(RoleWorker, mustCanonical(t, workerDir))
		lease, err := AcquireDirectory(direct, RoleWorker, workerDir)
		require.NoError(t, err)
		require.NoError(t, lease.Close())

		// The combined instance's component lease under the same
		// directory is an ownership conflict, even stopped.
		combinedOwner := DirectoryOwner{AccountID: CurrentAccountID(), Mode: RoleSolo, Root: mustCanonical(t, root)}
		conflicting, err := AcquireDirectory(combinedOwner, RoleWorker, workerDir)
		require.Error(t, err)
		assert.Nil(t, conflicting)
		var conflict *OwnershipConflict
		require.ErrorAs(t, err, &conflict)
		assert.Equal(t, RoleWorker, conflict.OwningMode)

		// The refused start wrote nothing new into the directory.
		entries, listErr := os.ReadDir(pathsFor(mustCanonical(t, workerDir)).Dir)
		require.NoError(t, listErr)
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		assert.ElementsMatch(t, []string{ownershipRecordName, dataLockName, ".leapmux-directory-publication.yaml"}, names,
			"a refused acquisition creates no runtime records or tokens")
	})

	t.Run("combined running, then combined again", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "solo-root")
		first := startTestRuntime(t, RoleSolo, root)
		second, err := StartRuntime(RuntimeConfig{Mode: RoleSolo, DataDir: root, Execution: ExecutionForeground})
		require.Error(t, err, "one instance runs per account and configuration")
		// The refusal identifies whichever lock the second start reached:
		// the data lease while the first still holds it.
		assert.True(t, errors.Is(err, ErrInstanceRunning) || errors.Is(err, ErrDirectoryInUse),
			"the refusal identifies a held lock: %v", err)
		assert.Nil(t, second)
		require.NoError(t, first.Shutdown())
	})
}

// Two same-instance starts synchronized at the gate: exactly one owns
// the runtime.
func TestConcurrentRunsHaveOneOwner(t *testing.T) {
	runtimeEnv(t)
	root := filepath.Join(t.TempDir(), "instance")

	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]*Runtime, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			<-start
			results[slot], errs[slot] = StartRuntime(RuntimeConfig{
				Mode: RoleWorker, DataDir: root, Execution: ExecutionForeground,
			})
		}(i)
	}
	close(start)
	wg.Wait()

	owners := 0
	for i := range 2 {
		if errs[i] == nil {
			owners++
			require.NotNil(t, results[i])
			t.Cleanup(func() { _ = results[i].Shutdown() })
		} else {
			assert.ErrorIs(t, errs[i], ErrDirectoryInUse, "the loser's refusal identifies the held directory")
		}
	}
	assert.Equal(t, 1, owners, "exactly one start owns the runtime")
}

// The helper process for the crash test: it starts a runtime and waits
// for its stdin to close, so the test can kill it mid-run.
func TestHelperProcessLifecycle(t *testing.T) {
	if os.Getenv("LEAPMUX_LIFECYCLE_HELPER") != "runtime-holder" {
		return
	}
	r, err := StartRuntime(RuntimeConfig{
		Mode:      RoleWorker,
		DataDir:   os.Getenv("LEAPMUX_LIFECYCLE_HELPER_DIR"),
		Execution: ExecutionForeground,
	})
	if err != nil {
		panic(err)
	}
	// The runtime stays held until the process ends; the reference
	// keeps it alive for the linter and the reader alike.
	defer func() { _ = r.Shutdown() }()
	// Park until the test ends us; the runtime lock stays held.
	buf := make([]byte, 1)
	_, _ = os.Stdin.Read(buf)
	os.Exit(0)
}

// A killed owner leaves recoverable runtime files: the freed locks
// allow a later start, and the stale socket does not block the new
// one's endpoint.
func TestCrashLeavesRecoverableRuntimeFiles(t *testing.T) {
	socketDir := runtimeEnv(t)
	root := filepath.Join(t.TempDir(), "crash")

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcessLifecycle$")
	cmd.Env = append(os.Environ(),
		"LEAPMUX_LIFECYCLE_HELPER=runtime-holder",
		"LEAPMUX_LIFECYCLE_HELPER_DIR="+root,
		"LEAPMUX_RUNTIME_DIR="+socketDir)
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	require.Eventually(t, func() bool {
		_, statErr := os.Stat(pathsFor(mustCanonical(t, root)).RuntimeRecord)
		return statErr == nil
	}, 10*time.Second, 50*time.Millisecond, "the holder published its runtime record")

	// Kill the holder: its locks release with the process.
	require.NoError(t, cmd.Process.Kill())
	_ = stdin.Close()
	_ = cmd.Wait()

	// A later start recovers: the freed runtime lock and the stale
	// socket both yield.
	recovered := startTestRuntime(t, RoleWorker, root)
	status := recovered.Status()
	assert.Equal(t, ProcessRunning, status.Process.State)
}

func mustCanonical(t *testing.T, dir string) string {
	t.Helper()
	canonical, err := CanonicalDir(dir)
	require.NoError(t, err)
	return canonical
}
