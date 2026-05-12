package solo_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

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
	t.Setenv(locallisten.EnvLocalListen, localListen)

	cfg := extraCfg
	cfg.SkipBanner = true
	cfg.NoTCP = true

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	inst, err := solo.Start(ctx, cfg)
	require.NoError(t, err, "solo.Start")
	t.Cleanup(inst.Stop)
	return inst
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
	t.Setenv(locallisten.EnvLocalListen, "")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	inst, err := solo.Start(ctx, solo.Config{
		SkipBanner: true,
		NoTCP:      true,
	})
	if err != nil && isDefaultListenerInUse(err) {
		t.Skipf("default local listener already in use (likely another Solo instance on this user): %v", err)
	}
	require.NoError(t, err, "solo.Start with default local-listen")
	t.Cleanup(inst.Stop)

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
// warning matters because solo mode auto-authenticates every request as the
// admin — exposing the port to anyone reachable on the network would otherwise
// hand them passwordless admin access silently.
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
			t.Setenv(locallisten.EnvLocalListen, uniqueListenURL(t))

			origStderr := os.Stderr
			r, w, err := os.Pipe()
			require.NoError(t, err, "create stderr pipe")
			defer func() { _ = r.Close() }()
			os.Stderr = w

			// Drain concurrently — a pipe's kernel buffer (typically 64 KiB)
			// would block once full and stall solo.Start.
			drained := make(chan string, 1)
			go func() {
				var buf bytes.Buffer
				_, _ = io.Copy(&buf, r)
				drained <- buf.String()
			}()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// Panic insurance: if solo.Start panics, this defer still
			// restores os.Stderr and closes the pipe so the drainer goroutine
			// doesn't block forever on io.Copy. On the happy path these run
			// after the explicit close below; the second w.Close is a no-op.
			defer func() {
				os.Stderr = origStderr
				_ = w.Close()
			}()

			inst, startErr := solo.Start(ctx, solo.Config{
				Listen:     tc.listen,
				SkipBanner: true,
			})
			if startErr == nil {
				t.Cleanup(inst.Stop)
			}

			// Restore stderr before reading drained so any subsequent
			// require/t.Fatal output reaches the real terminal, and close w
			// so the drainer sees EOF.
			os.Stderr = origStderr
			_ = w.Close()
			out := <-drained

			require.NoError(t, startErr, "solo.Start; captured stderr:\n%s", out)

			haveWarn := strings.Contains(out, warnMarker)
			if haveWarn != tc.wantWarn {
				t.Fatalf("warn presence: got %v, want %v; captured stderr:\n%s",
					haveWarn, tc.wantWarn, out)
			}
		})
	}
}

// TestSoloStart_InvalidLocalListenErrors confirms an unparseable URL surfaces
// as a startup error without leaking resources.
func TestSoloStart_InvalidLocalListenErrors(t *testing.T) {
	locallistentest.SandboxHome(t)
	t.Setenv(locallisten.EnvLocalListen, "gopher://example:70/bogus")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := solo.Start(ctx, solo.Config{
		SkipBanner: true,
		NoTCP:      true,
	})
	require.Error(t, err, "solo.Start should reject an unparseable LocalListen")
	assert.Contains(t, err.Error(), "local_listen",
		"error should surface the offending flag name")
}

// TestSoloStart_SurfacesHubServeError confirms that when the hub's Serve
// goroutine fails before the listener is ready (e.g. a well-formed URL that
// points at an unwritable location), solo.Start returns the underlying error
// immediately instead of blocking on the 5-second WaitReady timeout.
//
// Regression test for a bug where any Serve-time failure was masked as
// "wait for hub local listener: ... not ready after 5s", making diagnosis
// unnecessarily hard.
func TestSoloStart_SurfacesHubServeError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix-socket-specific reproduction")
	}
	locallistentest.SandboxHome(t)
	// Well-formed URL, but the parent directory does not exist, so
	// net.Listen("unix", ...) fails with ENOENT.
	t.Setenv(locallisten.EnvLocalListen, "unix:/nonexistent-parent-dir-for-solo-test/hub.sock")

	// Use a short deadline: if the fix regresses, we'd fall back to the
	// 5s WaitReady timeout and this test would flake-fail, making the
	// regression obvious.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	_, err := solo.Start(ctx, solo.Config{
		SkipBanner: true,
		NoTCP:      true,
	})
	elapsed := time.Since(start)

	require.Error(t, err, "solo.Start should surface the bind failure")
	assert.Contains(t, err.Error(), "hub serve",
		"error should attribute the failure to hub Serve, not the WaitReady timeout")
	assert.Less(t, elapsed, 5*time.Second,
		"should not wait the full WaitReady timeout once Serve has already failed")
}
