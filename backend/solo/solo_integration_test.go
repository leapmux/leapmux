package solo_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/internal/hub/store"
	sqlitest "github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/locallisten"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
	"github.com/leapmux/leapmux/solo"
)

func uniqueListenURL(t *testing.T) string {
	return locallistentest.UniqueListenURL(t, "leapmux-solo-test")
}

// startForTest runs solo.Start with NoTCP + a unique local listen URL + an
// isolated home so the hub + worker don't touch shared state. Returns the
// started Instance; teardown is wired through t.Cleanup.
func startForTest(t *testing.T, localListen string, extraCfg solo.Config) *solo.Instance {
	t.Helper()

	locallistentest.SandboxHome(t)

	if localListen == "" {
		localListen = uniqueListenURL(t)
	}
	// LEAPMUX_HUB_LISTEN is the comma-delimited form of the repeatable
	// --listen flag. One local entry is the whole bind set here: NoTCP below
	// drops the TCP half anyway, and the entry replaces the platform default
	// socket with this test's unique one.
	t.Setenv("LEAPMUX_HUB_LISTEN", localListen)

	cfg := extraCfg
	cfg.NoTCP = true

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	inst, err := solo.Start(ctx, cfg)
	require.NoError(t, err, "solo.Start")
	t.Cleanup(func() { require.NoError(t, inst.Stop()) })
	return inst
}

// The Hub and Worker contexts are detached from the one handed to Start, so a
// cancelled parent reaches them ONLY through the watcher Start installs. Before
// that detachment this was free (both were children of the caller's context);
// now it is wiring, and wiring that can be removed without any other test
// noticing -- every other path reaches teardown through an explicit Stop.
func TestSoloStart_CancellingTheParentShutsTheInstanceDown(t *testing.T) {
	locallistentest.SandboxHome(t)
	t.Setenv("LEAPMUX_HUB_LISTEN", uniqueListenURL(t))

	ctx, cancel := context.WithCancel(context.Background())
	inst, err := solo.Start(ctx, solo.Config{NoTCP: true})
	require.NoError(t, err, "solo.Start")
	t.Cleanup(func() { require.NoError(t, inst.Stop()) })

	// Deliberately no Stop: cancelling is the whole trigger under test.
	cancel()

	exited := make(chan error, 1)
	go func() { exited <- inst.Wait() }()
	select {
	case waitErr := <-exited:
		assert.NoError(t, waitErr, "a cancelled parent is a clean shutdown, not a failure")
	case <-time.After(30 * time.Second):
		// A generous hang-detector, not a performance assertion: a healthy
		// teardown here is the worker drain plus the hub's ~1s GOAWAY grace.
		t.Fatal("cancelling the parent context did not shut the instance down")
	}
}

// TestSoloStart_RespectsExplicitLocalListen confirms the hub actually binds
// the URL the caller requested (and not the platform default).
func TestSoloStart_RespectsExplicitLocalListen(t *testing.T) {
	explicit := uniqueListenURL(t)
	inst := startForTest(t, explicit, solo.Config{})

	assert.Equal(t, explicit, inst.LocalListenURL(),
		"Instance.LocalListenURL should echo the configured URL")

	// Dial the reported URL to prove something is actually listening.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, locallisten.WaitReady(ctx, inst.LocalListenURL()),
		"hub listener should be dial-able at the returned URL")
}

// TestSoloStart_DefaultLocalListen confirms solo.Start uses the per-platform
// default when the caller doesn't override it, and the derived URL is
// reachable. Inlined setup here because the shared helper auto-injects
// a unique LocalListen to keep other tests isolated.
//
// On Windows the default name is `npipe:leapmux-hub-<SID>`, derived from the
// current user's SID, so two Solo instances running as the same user on the
// same machine will collide on it. That's exactly what the default is meant
// to encode: one Solo per user. When something already holds the pipe — a
// running Desktop app, or a previous test process whose handles haven't
// been reclaimed yet — we skip rather than fail; the explicit-listen test
// already covers the "binding actually works" path with a unique URL.
func TestSoloStart_DefaultLocalListen(t *testing.T) {
	locallistentest.SandboxHome(t)
	t.Setenv("LEAPMUX_HUB_LISTEN", "")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	inst, err := solo.Start(ctx, solo.Config{
		NoTCP: true,
	})
	if err != nil && isDefaultListenerInUse(err) {
		t.Skipf("default local listener already in use (likely another Solo instance on this user): %v", err)
	}
	require.NoError(t, err, "solo.Start with default local-listen")
	t.Cleanup(func() { require.NoError(t, inst.Stop()) })

	url := inst.LocalListenURL()
	require.NotEmpty(t, url, "LocalListenURL must resolve to a non-empty default")
	switch runtime.GOOS {
	case "windows":
		assert.Contains(t, url, "npipe:leapmux-hub")
	default:
		assert.Contains(t, url, "unix:")
		assert.Contains(t, url, "hub.sock")
	}

	waitCtx, waitCancel := context.WithTimeout(ctx, 3*time.Second)
	defer waitCancel()
	require.NoError(t, locallisten.WaitReady(waitCtx, url),
		"default hub listener should be dial-able")
}

// isDefaultListenerInUse reports whether err comes from solo.Start failing
// because the default local listener (named pipe / unix socket) is already
// bound by another process. The wrapping is "create hub server: listen
// local: <transport-specific>"; the transport-specific tail differs across
// platforms (Windows: "Access is denied" from FILE_FLAG_FIRST_PIPE_INSTANCE;
// Unix: "address already in use" from bind() against an existing socket).
func isDefaultListenerInUse(err error) bool {
	msg := err.Error()
	if !strings.Contains(msg, "listen local:") {
		return false
	}
	return strings.Contains(msg, "Access is denied") ||
		strings.Contains(msg, "address already in use")
}

// TestSoloStart_WarnsOnNonLoopbackListen confirms solo.Start emits a security
// warning when the TCP listener is bound to a non-loopback address. The
// warning matters because the first TCP caller can claim a passwordless solo
// account. The warning makes that setup race explicit.
//
// We assert by piping `os.Stderr` to a buffer before solo.Start runs, because
// `logging.Setup` (invoked at the top of solo.Start) captures `os.Stderr` at
// handler-construction time and writes its output there. Substituting the
// default slog handler instead would be clobbered by `logging.Setup`.
func TestSoloStart_WarnsOnNonLoopbackListen(t *testing.T) {
	const warnMarker = "non-loopback address"

	cases := []struct {
		name     string
		listen   string
		wantWarn bool
	}{
		{"loopback IPv4 does not warn", "127.0.0.1:0", false},
		{"wildcard warns", "0.0.0.0:0", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			locallistentest.SandboxHome(t)
			// Both entries in the list: the TCP address under test and this
			// test's unique local socket. The list is the bind set, so naming
			// only the local entry would drop the TCP address entirely.
			t.Setenv("LEAPMUX_HUB_LISTEN", tc.listen+","+uniqueListenURL(t))

			var inst *solo.Instance
			var startErr error
			// Restores stderr before returning, so the assertions below (and any
			// require/t.Fatal output) reach the real terminal, not the pipe.
			out := testutil.CaptureStderr(t, func() {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				inst, startErr = solo.Start(ctx, solo.Config{
					Listen: tc.listen,
				})
			})
			if startErr == nil {
				t.Cleanup(func() { require.NoError(t, inst.Stop()) })
			}

			require.NoError(t, startErr, "solo.Start; captured stderr:\n%s", out)

			haveWarn := strings.Contains(out, warnMarker)
			if haveWarn != tc.wantWarn {
				t.Fatalf("warn presence: got %v, want %v; captured stderr:\n%s",
					haveWarn, tc.wantWarn, out)
			}
			// The URL to open names the port the Hub bound, not the requested 0.
			assert.Regexp(t, `^http://localhost:[1-9][0-9]*$`, inst.AccessURL())
		})
	}
}

// TestSoloStart_InvalidListenEntryErrors confirms an unparseable bind-set
// entry surfaces as a startup error without leaking resources.
func TestSoloStart_InvalidListenEntryErrors(t *testing.T) {
	locallistentest.SandboxHome(t)
	t.Setenv("LEAPMUX_HUB_LISTEN", "gopher://example:70/bogus")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := solo.Start(ctx, solo.Config{
		NoTCP: true,
	})
	require.Error(t, err, "solo.Start should reject an unparseable listen entry")
	assert.Contains(t, err.Error(), "invalid listen",
		"error should surface the offending entry with its index")
}

// A local-listen URL whose parent directory does not exist fails at BIND time,
// inside hub.NewServer, and solo.Start must surface that immediately rather than
// wait for a Hub that never started.
//
// Scoped to the bind failure on purpose. This used to assert `Contains("hub
// serve")` and claim to cover the Serve-time arm, which it never reached --
// "create hub server" merely contains that substring. The Serve arm is a
// different code path with its own coverage in solo_test.go, which drives it
// through the serveHub seam.
func TestSoloStart_SurfacesHubBindError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix-socket-specific reproduction")
	}
	locallistentest.SandboxHome(t)
	// Well-formed URL, but the parent directory does not exist, so
	// net.Listen("unix", ...) fails with ENOENT.
	t.Setenv("LEAPMUX_HUB_LISTEN", "unix:/nonexistent-parent-dir-for-solo-test/hub.sock")

	// Use a short deadline, so a regression that waits for the Hub fails here.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// The ERROR TEXT proves that the bind failure stopped the launch, so the
	// completion guard here only has to catch a hang.
	type startResult struct{ err error }
	done := make(chan startResult, 1)
	go func() {
		_, sErr := solo.Start(ctx, solo.Config{
			NoTCP: true,
		})
		done <- startResult{sErr}
	}()
	select {
	case got := <-done:
		require.Error(t, got.err, "solo.Start should surface the bind failure")
		assert.Contains(t, got.err.Error(), "create hub server",
			"error should attribute the failure to server construction")
	case <-time.After(60 * time.Second):
		t.Fatal("solo.Start hung instead of surfacing the bind failure")
	}
}

// A launch that fails before the Instance exists has exactly one reporter:
// Start, which RETURNS the error. Nothing may log it as well.
//
// This covers the pre-Instance half only -- the failure happens inside
// hub.NewServer, so no watcher has been installed yet. The half where the
// watcher DOES exist and has to stay quiet is
// TestSoloStart_DoesNotAlsoLogAServeTimeFailure, which reaches it via the
// serveHub seam.
func TestSoloStart_DoesNotAlsoLogANewServerFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix-socket-specific reproduction")
	}
	locallistentest.SandboxHome(t)
	t.Setenv("LEAPMUX_HUB_LISTEN", "unix:/nonexistent-parent-dir-for-solo-test/hub.sock")

	var startErr error
	out := testutil.CaptureStderr(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, startErr = solo.Start(ctx, solo.Config{NoTCP: true})
	})

	require.Error(t, startErr, "solo.Start should surface the bind failure")
	assert.NotContains(t, out, "hub error",
		"the failure Start returns must not also be logged; captured stderr:\n"+out)
}

// A solo or desktop launch restores the same Worker across restarts, and each
// launch logs the fingerprint a user checks against the key-change dialog's
// Actual value.
//
// This is the property the pin forces: a second launch has to reproduce the keys
// the Hub pinned at the first, because a single key of difference would make the
// state unusable and register a new Worker on every start.
func TestSoloStart_KeepsItsWorkerAcrossRestarts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on Windows")
	}
	// Once per test: startForTest sandboxes a NEW home on every call, which would
	// move HOME out from under the first launch's saved state.
	home := locallistentest.SandboxHome(t)
	listenURL := uniqueListenURL(t)
	t.Setenv("LEAPMUX_HUB_LISTEN", listenURL)

	// Each launch's context is created OUTSIDE the capture. solo.Start's watcher
	// shuts the instance down when its parent context ends, so a cancel deferred
	// inside the captured call would tear the instance down at once.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Only each solo.Start call is captured: solo.Start runs logging.Setup, which
	// installs a fresh default logger on stderr, so CaptureDefaultLogger cannot
	// work here.
	launch := func(t *testing.T) (*solo.Instance, string) {
		t.Helper()
		var inst *solo.Instance
		var startErr error
		out := testutil.CaptureStderr(t, func() {
			inst, startErr = solo.Start(ctx, solo.Config{NoTCP: true})
		})
		require.NoError(t, startErr, "solo.Start; captured stderr:\n"+out)
		return inst, out
	}

	first, firstOut := launch(t)
	firstID := requireOnlineWorkerID(t, listenURL)
	firstFingerprint := requireRegisteredFingerprint(t, firstOut)
	require.NoError(t, first.Stop())

	second, secondOut := launch(t)
	t.Cleanup(func() { _ = second.Stop() })
	secondID := requireOnlineWorkerID(t, listenURL)
	secondFingerprint := requireRegisteredFingerprint(t, secondOut)

	assert.Equal(t, firstID, secondID, "a solo launch must keep its Worker across restarts")
	assert.Equal(t, firstFingerprint, secondFingerprint,
		"a restart must restore the same keys, which is what the dialog's Actual fingerprint shows")

	matches, err := filepath.Glob(filepath.Join(home, ".config", "leapmux", "*", "worker", "state.json"))
	require.NoError(t, err)
	require.Len(t, matches, 1, "the launch must have written one worker state file")
	corrupt, err := filepath.Glob(matches[0] + ".corrupt-*")
	require.NoError(t, err)
	assert.Empty(t, corrupt, "neither launch may treat the saved state as unusable")
}

// requireOnlineWorkerID waits until the local Hub reports one online Worker and
// returns its id. The local socket admits a credential-free caller, so no token is
// needed.
func requireOnlineWorkerID(t *testing.T, listenURL string) string {
	t.Helper()
	client := localWorkerManagementClient(t, listenURL)
	ctx := testutil.DeadlineContext(t)

	var workerID string
	require.Eventually(t, func() bool {
		resp, listErr := client.ListWorkers(ctx, &leapmuxv1.ListWorkersRequest{})
		if listErr != nil {
			return false
		}
		for _, w := range resp.GetWorkers() {
			if w.GetOnline() {
				workerID = w.GetId()
				return true
			}
		}
		return false
	}, 30*time.Second, 10*time.Millisecond, "the solo worker did not come online")
	return workerID
}

// localWorkerManagementClient dials the Hub's local IPC socket.
func localWorkerManagementClient(t *testing.T, listenURL string) leapmuxv1connect.WorkerManagementServiceClient {
	t.Helper()
	endpoint, err := hubtransport.New(listenURL)
	require.NoError(t, err)
	t.Cleanup(endpoint.CloseIdleConnections)
	return leapmuxv1connect.NewWorkerManagementServiceClient(connect.NewClient(connecthttp.NewTransport(
		endpoint.UnaryClient(hubtransport.DefaultUnaryTimeout), endpoint.BaseURL(), connecthttp.WithReadMaxBytes(0))))
}

// requireRegisteredFingerprint reads a launch's "worker initialized" JSON line
// and returns its key_fingerprint.
func requireRegisteredFingerprint(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) != nil {
			continue
		}
		if record["msg"] != "worker initialized" {
			continue
		}
		fingerprint, _ := record["key_fingerprint"].(string)
		require.NotEmpty(t, fingerprint, "the registration log line must carry key_fingerprint")
		return fingerprint
	}
	t.Fatalf("no \"worker initialized\" line in the captured stderr:\n%s", out)
	return ""
}

// A launch whose predecessor CRASHED leaves a lease row behind: its holder is
// dead, so the row expires after its TTL and the launch must wait it out and
// start. Before the lease wait, Start failed with "another Hub is already
// running" and the user had to retry by hand.
func TestSoloStart_WaitsOutAnOrphanedLease(t *testing.T) {
	locallistentest.SandboxHome(t)
	listen := locallistentest.UniqueListenURL(t, "leapmux-hub-orphan")
	dataDir := t.TempDir()

	// The orphaned row: a holder that is gone, with a 1-second TTL.
	hubDB := filepath.Join(dataDir, "hub", "hub.db")
	require.NoError(t, os.MkdirAll(filepath.Dir(hubDB), 0o750))
	sqlDB, err := sqlitest.OpenDB(hubDB, sqlitedb.Config{})
	require.NoError(t, err)
	require.NoError(t, sqlitest.MigrateDB(sqlDB))
	hubStore, err := sqlitest.Open(hubDB, sqlitedb.Config{})
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	_, err = hubStore.RevocationEvents().AcquireHubRuntimeLease(context.Background(), store.AcquireHubRuntimeLeaseParams{
		HolderID: "dead-predecessor", LeaseDuration: time.Second,
	})
	require.NoError(t, err)
	require.NoError(t, hubStore.Close())

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	inst, err := solo.Start(ctx, solo.Config{
		NoTCP: true,
		Args:  []string{"--listen=" + listen, "--data-dir=" + dataDir},
	})
	require.NoError(t, err, "the launch must wait out the orphaned lease and start")
	t.Cleanup(func() { require.NoError(t, inst.Stop()) })
}
