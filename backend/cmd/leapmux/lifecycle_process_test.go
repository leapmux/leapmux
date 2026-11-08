package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport"

	"github.com/leapmux/leapmux/internal/util/atomicfile"
	"github.com/leapmux/leapmux/internal/util/filelock"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
)

func TestCombinedConflictPrecedesRuntimePublication(t *testing.T) {
	for _, mode := range []string{"solo", "dev"} {
		for _, component := range []lifecycle.Role{lifecycle.RoleHub, lifecycle.RoleWorker} {
			for _, running := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/running=%t", mode, component, running), func(t *testing.T) {
					root := t.TempDir()
					componentPath := filepath.Join(root, component.String())
					canonical := mustCanonicalDir(t, componentPath)
					lease, err := lifecycle.AcquireDirectory(lifecycle.NewDirectoryOwner(component, canonical), component, canonical)
					require.NoError(t, err)
					t.Cleanup(func() { _ = lease.Close() })
					if !running {
						require.NoError(t, lease.Close())
					}
					credentials := []byte("preserve these credentials exactly")
					database := []byte("preserve this database exactly")
					statePath := filepath.Join(canonical, "state.json")
					databasePath := filepath.Join(canonical, component.String()+".db")
					require.NoError(t, os.WriteFile(statePath, credentials, 0o600))
					require.NoError(t, os.WriteFile(databasePath, database, 0o600))
					result := invokeLifecycleCLI(t, t.TempDir(), mode, "run", "--data-dir", root, "--listen", "127.0.0.1:0")
					require.Equal(t, 1, result.exitCode, result.stdout+result.stderr)
					assert.Contains(t, result.stderr, "data directory")
					gotState, err := os.ReadFile(statePath)
					require.NoError(t, err)
					assert.Equal(t, credentials, gotState)
					gotDB, err := os.ReadFile(databasePath)
					require.NoError(t, err)
					assert.Equal(t, database, gotDB)
					paths := lifecycle.PathsOf(mustCanonicalDir(t, root))
					assert.NoFileExists(t, paths.RuntimeRecord, "component conflicts must precede runtime publication")
					assert.NoFileExists(t, paths.ControlToken)
					assert.NoFileExists(t, paths.ServiceSpec)
					discarded, err := filepath.Glob(filepath.Join(canonical, "state.json.discarded*"))
					require.NoError(t, err)
					assert.Empty(t, discarded)
				})
			}
		}
	}
}

func TestApplicationOwnershipConflictPreservesStoredDataInBothOrders(t *testing.T) {
	for _, combined := range []string{"solo", "dev"} {
		for _, component := range []string{"hub", "worker"} {
			for _, combinedFirst := range []bool{false, true} {
				for _, running := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/combined-first=%t/running=%t", combined, component, combinedFirst, running), func(t *testing.T) {
						ctx := testutil.DeadlineContext(t)
						root := mustCanonicalDir(t, t.TempDir())
						componentRoot := filepath.Join(root, component)
						firstMode, firstRoot, secondMode, secondRoot := component, componentRoot, combined, root
						if combinedFirst {
							firstMode, firstRoot, secondMode, secondRoot = combined, root, component, componentRoot
						}
						if firstMode == "worker" {
							cfg := writeRegisteredStateAgainst(t, "http://127.0.0.1:1")
							state, err := os.ReadFile(cfg.StatePath())
							require.NoError(t, err)
							require.NoError(t, os.MkdirAll(firstRoot, 0o700))
							require.NoError(t, atomicfile.WriteFileDurable(filepath.Join(firstRoot, "state.json"), state, 0o600))
						}
						arguments := []string{firstMode, "run", "--data-dir", firstRoot}
						if firstMode != "worker" {
							arguments = append(arguments, "--listen", locallistentest.UniqueListenURL(t, "lmx-conflict"))
						}
						command := exec.CommandContext(ctx, os.Args[0], append([]string{"-test.run=^TestHelperProcessLifecycleCLI$", "--"}, arguments...)...)
						command.Env = lifecycleCLIEnvironment(t.TempDir())
						var output bytes.Buffer
						command.Stdout, command.Stderr = &output, &output
						require.NoError(t, command.Start())
						done := make(chan struct{})
						var outcome error
						go func() { outcome = command.Wait(); close(done) }()
						t.Cleanup(func() {
							_ = command.Process.Kill()
							<-done
							if t.Failed() {
								t.Log(output.String())
							}
						})
						require.Eventually(t, func() bool {
							status, err := lifecycle.Observe(ctx, firstRoot)
							return err == nil && status.Readiness != nil && (*status.Readiness == lifecycle.ReadinessReady || *status.Readiness == lifecycle.ReadinessReadyForSetup)
						}, 30*time.Second, 20*time.Millisecond)
						stop := func() {
							result := invokeLifecycleCLI(t, t.TempDir(), firstMode, "stop", "--data-dir", firstRoot)
							require.Zero(t, result.exitCode, result.stdout+result.stderr)
							select {
							case <-done:
								require.NoError(t, outcome, output.String())
							case <-ctx.Done():
								t.Fatal("original process did not stop")
							}
						}
						if !running {
							stop()
						}
						before := make(map[string][]byte)
						for _, name := range []string{"state.json", component + ".db"} {
							path := filepath.Join(componentRoot, name)
							data, err := os.ReadFile(path)
							if errors.Is(err, os.ErrNotExist) {
								continue
							}
							require.NoError(t, err)
							before[path] = data
						}
						arguments = []string{secondMode, "run", "--data-dir", secondRoot}
						if secondMode != "worker" {
							arguments = append(arguments, "--listen", locallistentest.UniqueListenURL(t, "lmx-refused"))
						}
						result := invokeLifecycleCLI(t, t.TempDir(), arguments...)
						require.Equal(t, 1, result.exitCode, result.stdout+result.stderr)
						require.Contains(t, result.stderr, "data directory")
						for path, data := range before {
							actual, err := os.ReadFile(path)
							require.NoError(t, err)
							assert.Equal(t, data, actual, path)
						}
						assert.NoFileExists(t, lifecycle.PathsOf(secondRoot).RuntimeRecord)
						assert.NoFileExists(t, lifecycle.PathsOf(secondRoot).ControlToken)
						assert.NoFileExists(t, lifecycle.PathsOf(secondRoot).ServiceSpec)
						discarded, err := filepath.Glob(filepath.Join(componentRoot, "state.json.discarded*"))
						require.NoError(t, err)
						assert.Empty(t, discarded)
						if running {
							stop()
						}
					})
				}
			}
		}
	}
}

const lifecycleCLIHelperEnv = "LEAPMUX_TEST_LIFECYCLE_CLI_HELPER"
const lifecycleStartGateEnv = "LEAPMUX_TEST_LIFECYCLE_START_GATE"

func TestHelperProcessLifecycleCLI(t *testing.T) {
	if os.Getenv(lifecycleCLIHelperEnv) != "1" {
		return
	}
	if gate := os.Getenv(lifecycleStartGateEnv); gate != "" {
		connection, err := net.DialTimeout("tcp", gate, 30*time.Second)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if err := connection.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		_, err = connection.Write([]byte{'r'})
		if err == nil {
			_, err = io.ReadFull(connection, make([]byte, 1))
		}
		if closeErr := connection.Close(); err != nil || closeErr != nil {
			fmt.Fprintf(os.Stderr, "startup synchronization failed: %v\n", errors.Join(err, closeErr))
			os.Exit(2)
		}
	}
	for index, arg := range os.Args {
		if arg == "--" && index+2 < len(os.Args) {
			os.Exit(runCLI(os.Args[index+1:], os.Stdout, os.Stderr, cliRunners{runMode: runModeCommand}))
		}
	}
	os.Exit(2)
}

type lifecycleCLIResult struct {
	exitCode int
	stdout   string
	stderr   string
}

func invokeLifecycleCLI(t *testing.T, home string, args ...string) lifecycleCLIResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], append([]string{
		"-test.run=^TestHelperProcessLifecycleCLI$", "--",
	}, args...)...)
	command.Env = lifecycleCLIEnvironment(home)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	stdout, err := command.Output()
	result := lifecycleCLIResult{stdout: string(stdout), stderr: stderr.String()}
	if err != nil {
		var exit *exec.ExitError
		require.ErrorAs(t, err, &exit)
		result.exitCode = exit.ExitCode()
	}
	require.NoError(t, ctx.Err(), "the CLI did not finish before its test deadline")
	return result
}

func lifecycleCLIEnvironment(home string) []string {
	var environment []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "LEAPMUX_") && !strings.EqualFold(key, "HOME") && !strings.EqualFold(key, "USERPROFILE") {
			environment = append(environment, entry)
		}
	}
	return append(environment, lifecycleCLIHelperEnv+"=1", "HOME="+home, "USERPROFILE="+home)
}

func TestConcurrentForegroundProcessesHaveOneOwner(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	gate, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = gate.Close() })
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	require.NoError(t, gate.SetDeadline(deadline))
	root := t.TempDir()
	listen := locallistentest.UniqueListenURL(t, "lmx-concurrent-hub")
	type contender struct {
		command *exec.Cmd
		output  bytes.Buffer
		done    chan struct{}
		err     error
	}
	var contenders []*contender
	exits := make(chan *contender, 2)
	for range 2 {
		child := &contender{done: make(chan struct{})}
		child.command = exec.CommandContext(ctx, os.Args[0],
			"-test.run=^TestHelperProcessLifecycleCLI$", "--", "hub", "run", "--data-dir", root, "--listen", listen)
		child.command.Env = append(lifecycleCLIEnvironment(t.TempDir()), lifecycleStartGateEnv+"="+gate.Addr().String())
		child.command.Stdout, child.command.Stderr = &child.output, &child.output
		require.NoError(t, child.command.Start())
		go func() {
			child.err = child.command.Wait()
			close(child.done)
			exits <- child
		}()
		t.Cleanup(func() {
			_ = child.command.Process.Kill()
			<-child.done
			if t.Failed() {
				t.Logf("contender %d: %s", child.command.Process.Pid, child.output.String())
			}
		})
		contenders = append(contenders, child)
	}
	var arrivals []net.Conn
	for range 2 {
		connection, err := gate.Accept()
		require.NoError(t, err)
		t.Cleanup(func() { _ = connection.Close() })
		require.NoError(t, connection.SetDeadline(deadline))
		var signal [1]byte
		_, err = io.ReadFull(connection, signal[:])
		require.NoError(t, err)
		require.Equal(t, byte('r'), signal[0])
		arrivals = append(arrivals, connection)
	}
	for _, connection := range arrivals {
		_, err := connection.Write([]byte{'s'})
		require.NoError(t, err)
		require.NoError(t, connection.Close())
	}
	var loser *contender
	select {
	case loser = <-exits:
	case <-ctx.Done():
		t.Fatal("neither contender reported a result")
	}
	var exit *exec.ExitError
	require.ErrorAs(t, loser.err, &exit, loser.output.String())
	assert.Equal(t, 1, exit.ExitCode())
	assert.Contains(t, loser.output.String(), "in use")
	winner := contenders[0]
	if winner == loser {
		winner = contenders[1]
	}
	canonical := mustCanonicalDir(t, root)
	require.Eventually(t, func() bool {
		status, err := lifecycle.Observe(ctx, canonical)
		return err == nil && status.Process.PID != nil && *status.Process.PID == winner.command.Process.Pid &&
			status.Readiness != nil && *status.Readiness == lifecycle.ReadinessReady
	}, 30*time.Second, 50*time.Millisecond, "exactly one contender must become ready")
	select {
	case <-winner.done:
		t.Fatalf("the winning runtime ended before stop: %v\n%s", winner.err, winner.output.String())
	default:
	}
	result := invokeLifecycleCLI(t, t.TempDir(), "hub", "stop", "--data-dir", root)
	require.Equal(t, 0, result.exitCode, result.stdout+result.stderr)
	select {
	case <-winner.done:
		require.NoError(t, winner.err, winner.output.String())
	case <-ctx.Done():
		t.Fatal("the winning runtime did not finish after stop")
	}
}

func TestDevForegroundStatusAdvancesAfterAdministratorSetup(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	dataDir := t.TempDir()
	listen := locallistentest.UniqueListenURL(t, "lmx-dev-setup")
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperProcessLifecycleCLI$", "--",
		"dev", "run", "--data-dir", dataDir, "--listen", listen)
	command.Env = lifecycleCLIEnvironment(t.TempDir())
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	require.NoError(t, command.Start())
	done := make(chan struct{})
	var commandErr error
	go func() { commandErr = command.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = command.Process.Kill()
		<-done
		if t.Failed() {
			t.Log(output.String())
		}
	})
	canonical := mustCanonicalDir(t, dataDir)
	require.Eventually(t, func() bool {
		status, err := lifecycle.Observe(ctx, canonical)
		return err == nil && status.Readiness != nil && *status.Readiness == lifecycle.ReadinessReadyForSetup &&
			status.Connection.State == lifecycle.ConnectionPendingSetup && status.Registration.State == lifecycle.RegistrationPending
	}, 30*time.Second, 50*time.Millisecond)
	endpoint, err := hubtransport.New(listen)
	require.NoError(t, err)
	t.Cleanup(endpoint.CloseIdleConnections)
	client := leapmuxv1connect.NewAuthServiceClient(connect.NewClient(connecthttp.NewTransport(
		endpoint.HTTP2OnlyClient(), endpoint.BaseURL(), connecthttp.WithReadMaxBytes(0), connecthttp.WithGRPC())))
	_, err = client.SignUp(ctx, &leapmuxv1.SignUpRequest{Username: "lifecycle-owner", Password: "test-password-123", DisplayName: "Lifecycle Owner"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		status, err := lifecycle.Observe(ctx, canonical)
		return err == nil && status.Readiness != nil && *status.Readiness == lifecycle.ReadinessReady &&
			status.Registration.State == lifecycle.RegistrationRegistered && status.Registration.WorkerID != nil &&
			status.Connection.State == lifecycle.ConnectionConnected
	}, 30*time.Second, 50*time.Millisecond, "dev must publish automatic registration and worker readiness after setup")
	result := invokeLifecycleCLI(t, t.TempDir(), "dev", "stop", "--data-dir", dataDir)
	require.Equal(t, 0, result.exitCode, result.stdout+result.stderr)
	select {
	case <-done:
		require.NoError(t, commandErr, output.String())
	case <-ctx.Done():
		t.Fatal("the dev process did not join after stop")
	}
	status, err := lifecycle.Observe(ctx, canonical)
	require.NoError(t, err)
	assert.Equal(t, lifecycle.ProcessStopped, status.Process.State)
	assert.Equal(t, lifecycle.ConnectionDisconnected, status.Connection.State)
	if status.Connection.LastError != nil {
		t.Logf("final connection error: %s", *status.Connection.LastError)
	}
	assert.Nil(t, status.Connection.LastError)
	assert.Equal(t, lifecycle.RegistrationRegistered, status.Registration.State)

}

func TestForegroundVersionDoesNotStartRuntime(t *testing.T) {
	for _, mode := range []string{"hub", "worker", "solo", "dev"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			result := invokeLifecycleCLI(t, home, mode, "run", "--version")
			assert.Equal(t, 0, result.exitCode, result.stderr)
			assert.NotEmpty(t, result.stdout)
			assert.NotContains(t, result.stderr, "panic:")
			assert.NoDirExists(t, filepath.Join(home, ".config", "leapmux", mode, ".lifecycle"))
		})
	}
}

func TestConfigurationMetadataAndInvalidFlagsDoNotMutate(t *testing.T) {
	for _, mode := range []string{"hub", "worker", "solo", "dev"} {
		for _, verb := range []string{"enable", "disable"} {
			for _, argument := range []string{"--help", "--version", "--unknown-option", "unexpected"} {
				t.Run(mode+"/"+verb+"/"+argument, func(t *testing.T) {
					root := filepath.Join(t.TempDir(), "absent")
					result := invokeLifecycleCLI(t, t.TempDir(), mode, verb, argument, "--data-dir", root)
					if argument == "--help" || argument == "--version" {
						assert.Equal(t, 0, result.exitCode, result.stdout+result.stderr)
						assert.NotEmpty(t, result.stdout+result.stderr)
						assert.NotContains(t, result.stdout, "No startup configuration")
					} else {
						assert.Equal(t, 1, result.exitCode, result.stdout+result.stderr)
					}
					assert.NoDirExists(t, root)
				})
			}
		}
	}
}

func TestWorkerRunRefusesRegistrationFlags(t *testing.T) {
	for _, flag := range []string{"--hub", "--registration-key"} {
		t.Run(flag, func(t *testing.T) {
			cfg := writeRegisteredStateAgainst(t, "http://127.0.0.1:1")
			// A database obstruction makes the old accepting path exit,
			// while the corrected parser must refuse before reaching it.
			require.NoError(t, os.Mkdir(filepath.Join(cfg.DataDir, "worker.db"), 0o700))
			result := invokeLifecycleCLI(t, t.TempDir(), "worker", "run", "--data-dir", cfg.DataDir, flag, "registration-only")
			require.Equal(t, 1, result.exitCode)
			assert.Contains(t, result.stderr, "belongs to `leapmux worker register`")
			assert.NoFileExists(t, lifecycle.PathsOf(mustCanonicalDir(t, cfg.DataDir)).RuntimeRecord)
		})
	}
}

func TestStoppedStatusPrintsJSON(t *testing.T) {
	result := invokeLifecycleCLI(t, t.TempDir(), "worker", "status", "--json", "--data-dir", t.TempDir())
	assert.Equal(t, 3, result.exitCode, result.stderr)
	var status lifecycle.Status
	require.NoError(t, json.Unmarshal([]byte(result.stdout), &status), result.stdout)
	assert.Equal(t, new("worker"), status.Mode)
	assert.Equal(t, lifecycle.ProcessStopped, status.Process.State)
	assert.Equal(t, lifecycle.RegistrationUnregistered, status.Registration.State)
}

func TestCombinedCLIStatusPreservesLiveRegistration(t *testing.T) {
	for _, mode := range []lifecycle.Role{lifecycle.RoleSolo, lifecycle.RoleDev} {
		t.Run(mode.String(), func(t *testing.T) {
			runtime, err := lifecycle.StartRuntime(lifecycle.RuntimeConfig{Mode: mode, DataDir: t.TempDir(), Execution: lifecycle.ExecutionForeground})
			require.NoError(t, err)
			t.Cleanup(func() { _ = runtime.Finish(nil) })
			runtime.SetRegistration(lifecycle.RegistrationStatus{State: lifecycle.RegistrationRegistered, WorkerID: new("local-worker"), HubURL: new("http://local-hub")})
			runtime.SetConnection(lifecycle.ConnectionStatus{State: lifecycle.ConnectionConnected})
			runtime.SetReadiness(lifecycle.ReadinessReady)
			result := invokeLifecycleCLI(t, t.TempDir(), mode.String(), "status", "--data-dir", runtime.Record().DataDir, "--json")
			require.Equal(t, 0, result.exitCode, result.stdout+result.stderr)
			var status lifecycle.Status
			require.NoError(t, json.Unmarshal([]byte(result.stdout), &status))
			assert.Equal(t, lifecycle.RegistrationRegistered, status.Registration.State)
			assert.Equal(t, new("local-worker"), status.Registration.WorkerID)
			assert.Equal(t, lifecycle.ConnectionConnected, status.Connection.State)
		})
	}
}

func TestStoppedStatusUsesNullForUnavailableScalars(t *testing.T) {
	result := invokeLifecycleCLI(t, t.TempDir(), "worker", "status", "--json", "--data-dir", t.TempDir())
	require.Equal(t, 3, result.exitCode, result.stderr)
	var status map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.stdout), &status))
	for _, key := range []string{"execution", "readiness", "startup"} {
		require.Contains(t, status, key)
		assert.Nil(t, status[key], key)
	}
	for group, fields := range map[string][]string{
		"registration": {"worker_id", "hub_url"},
		"connection":   {"last_error"},
		"attempt":      {"id", "started_at", "ended_at", "exit_code", "error", "log_path"},
		"process":      {"pid", "started_at"},
	} {
		values, ok := status[group].(map[string]any)
		require.True(t, ok, group)
		for _, key := range fields {
			require.Contains(t, values, key)
			assert.Nil(t, values[key], group+"."+key)
		}
	}
	assert.NotEmpty(t, status["instance_id"])
	assert.Equal(t, "worker", status["mode"])
	assert.Equal(t, []any{}, status["diagnostics"])
}

func TestForegroundFailureAppearsInAttemptLogAndStatus(t *testing.T) {
	cfg := writeRegisteredStateAgainst(t, "http://127.0.0.1:1")
	require.NoError(t, os.Mkdir(filepath.Join(cfg.DataDir, "worker.db"), 0o700))
	home := t.TempDir()
	result := invokeLifecycleCLI(t, home, "worker", "run", "--data-dir", cfg.DataDir)
	require.Equal(t, 1, result.exitCode, result.stdout+result.stderr)
	require.Contains(t, result.stderr, "open worker db")
	paths := lifecycle.PathsOf(mustCanonicalDir(t, cfg.DataDir))
	record, err := lifecycle.ReadRuntimeRecord(paths.RuntimeRecord)
	require.NoError(t, err)
	log, err := os.ReadFile(paths.AttemptLogPath(record.AttemptID))
	require.NoError(t, err)
	assert.Contains(t, string(log), "open worker db")
	statusResult := invokeLifecycleCLI(t, home, "worker", "status", "--json", "--data-dir", cfg.DataDir)
	require.Equal(t, 1, statusResult.exitCode, statusResult.stdout+statusResult.stderr)
	var status lifecycle.Status
	require.NoError(t, json.Unmarshal([]byte(statusResult.stdout), &status))
	assert.Equal(t, lifecycle.ProcessFailed, status.Process.State)
	var found bool
	for _, diagnostic := range status.Diagnostics {
		if diagnostic.Kind == "log" && strings.Contains(diagnostic.Body, "open worker db") {
			found = true
		}
	}
	assert.True(t, found, "status must include the relevant attempt-log excerpt")
}

func TestUnregisteredWorkerRefusesNativeStartupBeforePrompt(t *testing.T) {
	for _, verb := range []string{"start", "restart"} {
		t.Run(verb, func(t *testing.T) {
			home, dataDir := t.TempDir(), t.TempDir()
			result := invokeLifecycleCLI(t, home, "worker", verb, "--startup=login",
				"--config", filepath.Join(home, "worker.yaml"), "--data-dir", dataDir)
			assert.Equal(t, 1, result.exitCode, result.stdout+result.stderr)
			assert.Contains(t, result.stderr, "register")
			assert.NotContains(t, result.stdout, "Apply this configuration")
			assert.NoFileExists(t, lifecycle.PathsOf(mustCanonicalDir(t, dataDir)).ServiceSpec)
		})
	}
}

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
	require.NoError(t, atomicfile.WriteFileDurable(filepath.Join(dataDir, "state.json"), stateBytes, 0o600))

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
	}, 30*time.Second, 100*time.Millisecond, "the foreground runtime never took its lock")

	require.Eventually(t, func() bool {
		status, err := lifecycle.Observe(t.Context(), canonical)
		return err == nil && status.Readiness != nil && *status.Readiness == lifecycle.ReadinessReady
	}, 30*time.Second, 50*time.Millisecond)

	// The stop verb from THIS process stops the child.
	require.NoError(t, runModeCommand("worker", []string{"stop", "--data-dir", dataDir}))

	select {
	case err := <-exited:
		require.NoError(t, err, "the stopped runtime exits cleanly: %s", output.String())
	case <-time.After(30 * time.Second):
		t.Fatalf("the runtime did not exit after the stop: %s", output.String())
	}
	assert.False(t, lockHeld(paths), "the runtime lock released before the stop verb returned")
	state, err := cfg.LoadState()
	require.NoError(t, err)
	keys, err := state.CompositeKeypair()
	require.NoError(t, err)
	fingerprintLogs := 0
	for _, line := range strings.Split(output.String(), "\n") {
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) != nil {
			continue
		}
		fingerprint, exists := record["key_fingerprint"]
		if !exists {
			continue
		}
		fingerprintLogs++
		assert.Equal(t, keys.Fingerprint(), fingerprint)
		assert.Equal(t, state.WorkerID, record["worker_id"])
	}
	assert.Equal(t, 1, fingerprintLogs, "each worker runtime must log its actual key fingerprint once")
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
	require.NoError(t, atomicfile.MkdirAllDurable(paths.Dir))
	require.NoError(t, lifecycle.WriteServiceSpec(paths.ServiceSpec, lifecycle.ServiceSpec{
		InstanceID:    lifecycle.NewInstanceID(lifecycle.CurrentAccountID(), lifecycle.RoleWorker, canonical).String(),
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
		InstanceID: lifecycle.NewInstanceID(lifecycle.CurrentAccountID(), lifecycle.RoleWorker, canonical).String(),
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
	assert.Contains(t, err.Error(), "stop it", "disable identifies the stop requirement")

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

func TestWorkerReportsConnectingDuringFirstHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()
	accepted := make(chan net.Conn, 1)
	var held net.Conn
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	cfg := writeRegisteredStateAgainst(t, "http://"+listener.Addr().String())
	runtime, err := lifecycle.StartRuntime(lifecycle.RuntimeConfig{Mode: lifecycle.RoleWorker, DataDir: cfg.DataDir, Execution: lifecycle.ExecutionForeground})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	done := make(chan error, 1)
	go func() { done <- runWorkerForeground(ctx, runtime, preparedWorkerConfig(t, cfg.DataDir)) }()
	t.Cleanup(func() {
		cancel()
		_ = listener.Close()
		<-acceptDone
		select {
		case conn := <-accepted:
			_ = conn.Close()
		default:
		}
		require.NoError(t, <-done)
		if held != nil {
			_ = held.Close()
		}
		require.NoError(t, runtime.Shutdown())
		status, err := lifecycle.Observe(context.Background(), mustCanonicalDir(t, cfg.DataDir))
		require.NoError(t, err)
		assert.Equal(t, lifecycle.ProcessStopped, status.Process.State)
		assert.Equal(t, lifecycle.ConnectionDisconnected, status.Connection.State)
		if status.Connection.LastError != nil {
			t.Logf("final connection error: %s", *status.Connection.LastError)
		}
		assert.Nil(t, status.Connection.LastError)
		assert.Equal(t, lifecycle.RegistrationRegistered, status.Registration.State)
		assert.FileExists(t, cfg.StatePath())
	})
	select {
	case <-runtime.Ready():
	case <-ctx.Done():
		t.Fatal("local worker initialization did not finish")
	}
	select {
	case conn := <-accepted:
		held = conn
	case <-ctx.Done():
		t.Fatal("first connection did not begin")
	}
	status := runtime.Status()
	assert.Equal(t, lifecycle.ConnectionConnecting, status.Connection.State)
	if status.Connection.LastError != nil {
		t.Logf("final connection error: %s", *status.Connection.LastError)
	}
	assert.Nil(t, status.Connection.LastError)
	require.NotNil(t, status.Readiness)
	assert.Equal(t, lifecycle.ReadinessReady, *status.Readiness)
	assert.Equal(t, lifecycle.RegistrationRegistered, status.Registration.State)
}
