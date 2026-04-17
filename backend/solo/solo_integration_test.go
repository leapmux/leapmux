package solo_test

import (
	"context"
	"runtime"
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
func TestSoloStart_DefaultLocalListen(t *testing.T) {
	locallistentest.SandboxHome(t)
	t.Setenv(locallisten.EnvLocalListen, "")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	inst, err := solo.Start(ctx, solo.Config{
		SkipBanner: true,
		NoTCP:      true,
	})
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
