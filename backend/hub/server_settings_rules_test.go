package hub

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/captcha"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
)

// startTestServer boots the REAL server wiring — the same NewServer the
// solo, dev, and hub binaries call — over a temporary data directory, and
// serves it in the background so the deferred teardown (store close,
// listener close) runs.
//
// It binds no TCP port: cfg.Listen stays empty, and the local listener comes
// from locallistentest.UniqueListenURL. That helper picks the scheme the
// PLATFORM supports — `unix:` on Unix and `npipe:` on Windows — and keeps each
// test's address distinct. Building the URL here instead hardcoded `unix:`
// under /tmp, and locallisten.Listen has no unix listener on Windows, so every
// test in this file failed there with "unsupported local-listen scheme" before
// it reached the wiring it exists to pin. The helper also answers the reason
// the hand-built path avoided t.TempDir(): it roots the socket in a short temp
// directory, so the path stays inside macOS's 104-byte sun_path limit.
func startTestServer(t *testing.T, cfg *config.Config) *Server {
	t.Helper()

	cfg.DataDir = t.TempDir()
	cfg.LocalListen = locallistentest.UniqueListenURL(t, "lmx-hub-test")
	cfg.Storage = config.StorageConfig{Type: config.StorageTypeSQLite}

	srv, err := NewServer(cfg)
	require.NoError(t, err)

	serveCtx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- srv.Serve(serveCtx) }()
	t.Cleanup(func() {
		cancel()
		<-served
	})
	return srv
}

// TestSettingsReadTimeRulesAreRegistered pins the hub's wiring site: every
// key whose effective value differs from its stored-merged one carries a
// read-time rule ON THE MANAGER, so every admin surface asks one authority
// and no surface can hold per-key knowledge of its own.
//
// Each case makes the two values DIVERGE first, so a rule that was never
// registered fails rather than agreeing by accident. Two of them need a
// direct store write plus a reload, because the write path refuses exactly
// the states the read-time rules exist to degrade.
func TestSettingsReadTimeRulesAreRegistered(t *testing.T) {
	ctx := context.Background()
	// Dev mode holds signup open until an operator stores a row, which is
	// the signup rule's whole reason to exist.
	srv := startTestServer(t, &config.Config{DevMode: true})
	set := srv.SettingsManager()

	t.Run("signup_enabled follows dev mode until an operator stores a row", func(t *testing.T) {
		snap := set.Snapshot(ctx)
		require.False(t, snap.Customized(settings.KeySignupEnabled), "the fixture stores no row")
		assert.Equal(t, false, snap.ValueOf(settings.KeySignupEnabled),
			"the code default is closed signup")
		assert.Equal(t, true, set.Effective(snap, settings.KeySignupEnabled),
			"dev mode resolves signup open at read time")
	})

	t.Run("queue_budget reports the capacities the process runs on", func(t *testing.T) {
		snap := set.Snapshot(ctx)
		require.Equal(t, settings.QueueBudgetValue{}, snap.ValueOf(settings.KeyQueueBudget),
			"a stored 0 in every field means auto-size")

		effective, ok := set.Effective(snap, settings.KeyQueueBudget).(settings.QueueBudgetValue)
		require.True(t, ok, "the rule reports the key's own type")
		assert.Positive(t, effective.RelayBytes, "the relay pool runs on real bytes")
		assert.Positive(t, effective.WorkerBytes)
		assert.Positive(t, effective.UserEventsBytes)
	})

	t.Run("a key with no rule reports its stored-merged value", func(t *testing.T) {
		snap := set.Snapshot(ctx)
		assert.Equal(t, snap.ValueOf(settings.KeyTimeouts), set.Effective(snap, settings.KeyTimeouts),
			"the rules are per key, not a blanket override")
	})

	// The two states below cannot be reached through the write path: the
	// cross-key rules refuse them, which is exactly why a READ-time rule has
	// to degrade them. Direct SQL plus a reload is how an operator reaches
	// them, and how this test does.
	t.Run("captcha.selected reports the provider that serves challenges", func(t *testing.T) {
		selected := captcha.ProviderAlias(captcha.ProviderTurnstile)
		require.NoError(t, srv.Store().Settings().Upsert(ctx, store.UpsertSettingParams{
			Key:   captcha.CaptchaSelectedKey.Name(),
			Value: ptrconv.Ptr(`"` + selected + `"`),
		}))
		require.NoError(t, set.Load(ctx))

		snap := set.Snapshot(ctx)
		require.Equal(t, selected, snap.ValueOf(captcha.CaptchaSelectedKey),
			"the stored row selects a provider with no keys")
		assert.Equal(t, captcha.ProviderAlias(captcha.ProviderAltcha),
			set.Effective(snap, captcha.CaptchaSelectedKey),
			"an incomplete selection degrades to the provider actually serving challenges")
	})
}

// TestAltchaResetStepIsRegistered pins the fifth rule: resetting the ALTCHA
// row removes the signing key every outstanding challenge carries, so the
// hub restores it before the reset answers rather than leaving the next
// unauthenticated login to write hub_settings from its own handler.
func TestAltchaResetStepIsRegistered(t *testing.T) {
	ctx := context.Background()
	srv := startTestServer(t, &config.Config{})
	set := srv.SettingsManager()

	before := captcha.AltchaKey.Of(set.Snapshot(ctx)).HMACKey
	require.NotEmpty(t, before, "startup provisioning stores a signing key")

	require.NoError(t, set.Reset(ctx, captcha.AltchaKey))
	require.Empty(t, captcha.AltchaKey.Of(set.Snapshot(ctx)).HMACKey,
		"the reset really removed the key the step has to restore")

	require.NoError(t, set.AfterReset(ctx, captcha.AltchaKey))
	after := captcha.AltchaKey.Of(set.Snapshot(ctx)).HMACKey
	require.NotEmpty(t, after, "the post-reset step re-provisions the signing key")
	assert.NotEqual(t, before, after, "and it generates a fresh one")

	// A key with no step is not an error and writes nothing.
	require.NoError(t, set.AfterReset(ctx, settings.KeyTimeouts))
}
