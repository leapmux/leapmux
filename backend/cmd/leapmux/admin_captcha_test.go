package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/captcha"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
)

// effectiveProvider reads Effective's config half for assertions; the
// fallback reason is asserted separately where it matters.
func effectiveProvider(snap *settings.Snapshot) captcha.Provider {
	cfg, _ := captcha.Effective(snap)
	return cfg.Provider
}
func openAdminStore(t *testing.T, dir string) store.Store {
	t.Helper()
	st, err := sqlite.Open(dir+"/hub.db", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// adminSettings opens the data dir's store and builds the same settings
// manager the admin CLI builds over it. A FRESH manager per call, never a
// shared one: the manager TTL-caches snapshots, so assertions held across
// several command invocations must read what the NEXT command would resolve,
// not a cached view of the state before it.
func adminSettings(t *testing.T, dir string) (*settings.Manager, store.Store) {
	t.Helper()
	st := openAdminStore(t, dir)
	m, err := settingsManagerFor(adminConfig(dir), st)
	require.NoError(t, err)
	return m, st
}

// captchaSnapshot reads the data dir's effective captcha state through the
// CLI's own settings manager.
func captchaSnapshot(t *testing.T, dir string) *settings.Snapshot {
	t.Helper()
	m, _ := adminSettings(t, dir)
	return m.Snapshot(context.Background())
}

// altchaSettingsOf reads the stored ALTCHA tuning from a snapshot.
func altchaSettingsOf(t *testing.T, snap *settings.Snapshot) captcha.AltchaSettings {
	t.Helper()
	return captcha.AltchaKey.Of(snap).AltchaSettings
}

// settingRow reads one stored hub_settings row raw, for assertions on the
// encrypted half that a decoded snapshot deliberately hides.
func settingRow(t *testing.T, st store.Store, key string) *store.SettingRow {
	t.Helper()
	row, err := st.Settings().Get(context.Background(), key)
	require.NoError(t, err)
	return row
}

func TestCLI_CaptchaSetShowEnableDisableReset(t *testing.T) {
	dir := setupTestDataDir(t)

	// show on a fresh install reports built-in defaults, writing nothing.
	require.NoError(t, runCaptchaShow(testAdminCtx, []string{"--data-dir", dir}))
	st := openAdminStore(t, dir)
	_, err := st.Settings().Get(context.Background(), "captcha.altcha")
	assert.ErrorIs(t, err, store.ErrNotFound)

	// Enable first: provisioning the signing secret is the enable path's
	// job now (the hub does the same at startup), and a tuning-only set
	// deliberately writes just the public half.
	require.NoError(t, runCaptchaSetEnabled(testAdminCtx, []string{"--data-dir", dir}, true))
	assert.NotEmpty(t, captcha.AltchaKey.Of(captchaSnapshot(t, dir)).HMACKey,
		"enable must provision a signing secret")

	// set persists the change into the stored row.
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{
		"--algorithm", "SHA-256", "--cost", "2000", "--expires", "10m",
		"--data-dir", dir,
	}))
	snap := captchaSnapshot(t, dir)
	assert.Equal(t, captcha.ProviderAltcha, effectiveProvider(snap))
	assert.True(t, captcha.CaptchaEnabledKey.Of(snap))
	assert.True(t, snap.Customized(captcha.AltchaKey), "set must store the altcha row")
	s := altchaSettingsOf(t, snap)
	assert.Equal(t, "SHA-256", s.Algorithm)
	assert.EqualValues(t, 2000, s.Cost)
	assert.EqualValues(t, 600, s.ChallengeExpirySeconds)

	// disable keeps settings and the selection but flips the switch.
	require.NoError(t, runCaptchaSetEnabled(testAdminCtx, []string{"--data-dir", dir}, false))
	snap = captchaSnapshot(t, dir)
	assert.False(t, captcha.CaptchaEnabledKey.Of(snap))
	assert.Equal(t, captcha.ProviderAltcha, effectiveProvider(snap))
	assert.Equal(t, "SHA-256", altchaSettingsOf(t, snap).Algorithm)

	require.NoError(t, runCaptchaSetEnabled(testAdminCtx, []string{"--data-dir", dir}, true))
	assert.True(t, captcha.CaptchaEnabledKey.Of(captchaSnapshot(t, dir)))

	// invalid configurations are refused before any write.
	err = runCaptchaSet(testAdminCtx, []string{"--algorithm", "MD5", "--data-dir", dir})
	require.Error(t, err)
	err = runCaptchaSet(testAdminCtx, []string{"--cost", "10", "--data-dir", dir})
	require.Error(t, err)
	err = runCaptchaSet(testAdminCtx, []string{"--data-dir", dir})
	require.Error(t, err, "set without any field must be refused")
	// Sub-second durations are refused, not silently truncated: 500ms
	// would otherwise persist as 0s and fail validation with a value the
	// admin never typed.
	err = runCaptchaSet(testAdminCtx, []string{"--expires", "500ms", "--data-dir", dir})
	require.ErrorContains(t, err, "not a whole number of seconds")

	// reset removes every row and re-provisions the default altcha row
	// immediately: the request path must never write, so reset cannot
	// leave the first Login to insert and activate mid-request. The
	// re-provisioned row carries a fresh signing secret.
	secretBeforeReset := captcha.AltchaKey.Of(captchaSnapshot(t, dir)).HMACKey
	require.NoError(t, runCaptchaReset(testAdminCtx, []string{"--data-dir", dir}))
	snap = captchaSnapshot(t, dir)
	assert.Equal(t, captcha.ProviderAltcha, effectiveProvider(snap), "reset must leave a selected default row")
	assert.NotEqual(t, secretBeforeReset, captcha.AltchaKey.Of(snap).HMACKey, "the deleted altcha row's secret must not come back")
}

func TestCLI_CaptchaSetSecretSurvivesAndVerifies(t *testing.T) {
	dir := setupTestDataDir(t)

	require.NoError(t, runCaptchaSetEnabled(testAdminCtx, []string{"--data-dir", dir}, true))
	firstSecret := captcha.AltchaKey.Of(captchaSnapshot(t, dir)).HMACKey

	// A subsequent set keeps the same secret: the settings-only update
	// re-splits the row around the encrypted half it decrypted, so no
	// caller can lose or corrupt the key.
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{"--cost", "20000", "--data-dir", dir}))
	snap := captchaSnapshot(t, dir)
	assert.Equal(t, firstSecret, captcha.AltchaKey.Of(snap).HMACKey, "set must not rotate the signing secret")
	assert.EqualValues(t, 20000, altchaSettingsOf(t, snap).Cost)

	// The stored secret decrypts with the data-dir keystore and produces
	// verifiable challenges.
	ks, err := keystore.LoadOrGenerate(adminConfig(dir).EncryptionKeyFilePath())
	require.NoError(t, err)
	st := openAdminStore(t, dir)
	row := settingRow(t, st, "captcha.altcha")
	plain, err := ks.Decrypt(row.Secret, keystore.SettingsSecretAAD("captcha.altcha"))
	require.NoError(t, err)
	var stored captcha.AltchaRow
	require.NoError(t, json.Unmarshal(plain, &stored))
	assert.Len(t, stored.HMACKey, 32)

	set, _ := adminSettings(t, dir)
	m := captcha.NewManager(st, set, false)
	challengeJSON, err := m.AltchaChallengeJSON(context.Background())
	require.NoError(t, err)
	assert.NotEmpty(t, challengeJSON)
}

// TestCLI_CaptchaSetFamilySwitchResetsParams pins the family-carryover
// fix: switching algorithms must not reinterpret the old family's
// memory parameters in the new family's units.
func TestCLI_CaptchaSetFamilySwitchResetsParams(t *testing.T) {
	dir := setupTestDataDir(t)

	// Configure ARGON2ID with a memory cost (KiB) and parallelism.
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{
		"--algorithm", "ARGON2ID", "--memory-cost", "32768", "--parallelism", "2",
		"--data-dir", dir,
	}))
	s := altchaSettingsOf(t, captchaSnapshot(t, dir))
	assert.EqualValues(t, 32768, s.MemoryCost)
	assert.EqualValues(t, 2, s.Parallelism)

	// Switching to SCRYPT without touching the memory parameters resets
	// them to SCRYPT's defaults — 32768 would otherwise become SCRYPT's
	// block multiplier r and demand 128 * N * r per derivation.
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{
		"--algorithm", "SCRYPT", "--data-dir", dir,
	}))
	s = altchaSettingsOf(t, captchaSnapshot(t, dir))
	scryptDefaults, err := captcha.DefaultAltchaSettingsFor("SCRYPT")
	require.NoError(t, err)
	assert.EqualValues(t, scryptDefaults.Cost, s.Cost)
	assert.EqualValues(t, scryptDefaults.MemoryCost, s.MemoryCost)
	assert.EqualValues(t, scryptDefaults.Parallelism, s.Parallelism)
}

// TestCLI_CaptchaSetExplicitZeroRestoresDefault pins the explicit-flag
// semantics: `--memory-cost 0` restores the algorithm default instead of
// being indistinguishable from an unset flag.
func TestCLI_CaptchaSetExplicitZeroRestoresDefault(t *testing.T) {
	dir := setupTestDataDir(t)

	require.NoError(t, runCaptchaSet(testAdminCtx, []string{
		"--algorithm", "ARGON2ID", "--memory-cost", "32768", "--data-dir", dir,
	}))
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{
		"--memory-cost", "0", "--expires", "30m", "--data-dir", dir,
	}))
	s := altchaSettingsOf(t, captchaSnapshot(t, dir))
	argonDefaults, err := captcha.DefaultAltchaSettingsFor("ARGON2ID")
	require.NoError(t, err)
	assert.EqualValues(t, argonDefaults.MemoryCost, s.MemoryCost, "explicit 0 must restore the family default")
	assert.EqualValues(t, 1800, s.ChallengeExpirySeconds)
}

// TestCLI_CaptchaSetAlgorithmSwitchKeepsExpiry pins the fix for the
// algorithm switch: only the family-specific parameters reset; the
// algorithm-independent challenge expiry keeps its stored value.
func TestCLI_CaptchaSetAlgorithmSwitchKeepsExpiry(t *testing.T) {
	dir := setupTestDataDir(t)

	require.NoError(t, runCaptchaSet(testAdminCtx, []string{
		"--algorithm", "ARGON2ID", "--expires", "5m", "--data-dir", dir,
	}))
	assert.EqualValues(t, 300, altchaSettingsOf(t, captchaSnapshot(t, dir)).ChallengeExpirySeconds)

	require.NoError(t, runCaptchaSet(testAdminCtx, []string{"--algorithm", "SCRYPT", "--data-dir", dir}))
	s := altchaSettingsOf(t, captchaSnapshot(t, dir))
	assert.Equal(t, "SCRYPT", s.Algorithm)
	assert.EqualValues(t, 300, s.ChallengeExpirySeconds, "an algorithm switch must not touch the stored expiry")
}

// TestCLI_CaptchaSetExpiresZeroRestoresDefault pins the explicit-zero
// idiom for --expires, matching the other tunable flags.
func TestCLI_CaptchaSetExpiresZeroRestoresDefault(t *testing.T) {
	dir := setupTestDataDir(t)

	require.NoError(t, runCaptchaSet(testAdminCtx, []string{"--expires", "2h", "--data-dir", dir}))
	assert.EqualValues(t, 7200, altchaSettingsOf(t, captchaSnapshot(t, dir)).ChallengeExpirySeconds)

	require.NoError(t, runCaptchaSet(testAdminCtx, []string{"--expires", "0", "--data-dir", dir}))
	defaults := captcha.DefaultAltchaSettings()
	assert.EqualValues(t, defaults.ChallengeExpirySeconds, altchaSettingsOf(t, captchaSnapshot(t, dir)).ChallengeExpirySeconds)
}

// TestCLI_CaptchaProviderSwitching pins the provider lifecycle: switching
// to an external provider requires its keys, per-provider rows keep their
// own secrets across switches, and provider-foreign flags are refused.
func TestCLI_CaptchaProviderSwitching(t *testing.T) {
	dir := setupTestDataDir(t)

	// Provision altcha and capture its secret.
	require.NoError(t, runCaptchaSetEnabled(testAdminCtx, []string{"--data-dir", dir}, true))
	altchaSecret := captcha.AltchaKey.Of(captchaSnapshot(t, dir)).HMACKey

	// Switching to an external provider without its keys is refused.
	err := runCaptchaSet(testAdminCtx, []string{"--provider", "turnstile", "--data-dir", dir})
	require.ErrorContains(t, err, "requires --site-key and --secret")
	err = runCaptchaSet(testAdminCtx, []string{"--provider", "turnstile", "--site-key", "1x00AA", "--data-dir", dir})
	require.ErrorContains(t, err, "requires --site-key and --secret")
	// An empty secret would fail every verification at the provider; it is
	// refused rather than stored.
	err = runCaptchaSet(testAdminCtx, []string{"--provider", "turnstile", "--site-key", "1x00AA", "--secret", "", "--data-dir", dir})
	require.ErrorContains(t, err, "--secret must not be empty")

	// Unknown providers are refused.
	err = runCaptchaSet(testAdminCtx, []string{"--provider", "hcaptcha", "--site-key", "k", "--secret", "s", "--data-dir", dir})
	require.ErrorContains(t, err, "unsupported captcha provider")

	// Switching to Turnstile with keys selects it, enabled, with the
	// operator's secret stored encrypted under the row's own AAD.
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{
		"--provider", "turnstile", "--site-key", "1x00000000000000000000AA", "--secret", "0x00AA",
		"--data-dir", dir,
	}))
	snap := captchaSnapshot(t, dir)
	assert.Equal(t, captcha.ProviderTurnstile, effectiveProvider(snap))
	assert.True(t, captcha.CaptchaEnabledKey.Of(snap))
	assert.Equal(t, "1x00000000000000000000AA", captcha.TurnstileKey.Of(snap).SiteKey)
	assert.Equal(t, "0x00AA", captcha.TurnstileKey.Of(snap).SecretKey)

	ks, err := keystore.LoadOrGenerate(adminConfig(dir).EncryptionKeyFilePath())
	require.NoError(t, err)
	st := openAdminStore(t, dir)
	row := settingRow(t, st, "captcha.turnstile")
	plain, err := ks.Decrypt(row.Secret, keystore.SettingsSecretAAD("captcha.turnstile"))
	require.NoError(t, err)
	var ts captcha.TurnstileRow
	require.NoError(t, json.Unmarshal(plain, &ts))
	assert.Equal(t, "0x00AA", ts.SecretKey)

	// The altcha row survives the switch with its original secret.
	snap = captchaSnapshot(t, dir)
	assert.True(t, snap.Customized(captcha.AltchaKey))
	assert.Equal(t, altchaSecret, captcha.AltchaKey.Of(snap).HMACKey, "switching away must not regenerate the altcha secret")

	// Provider-foreign flags are refused rather than ignored.
	err = runCaptchaSet(testAdminCtx, []string{"--cost", "20000", "--data-dir", dir})
	require.ErrorContains(t, err, "--cost applies only to altcha")
	err = runCaptchaSet(testAdminCtx, []string{"--min-score", "0.7", "--data-dir", dir})
	require.ErrorContains(t, err, "--min-score applies only to recaptcha_v3")

	// A settings-only edit of the selected external provider updates the
	// site key, keeps the secret, and leaves the selection enabled. The
	// ciphertext is re-encrypted by the merge, so the surviving secret is
	// asserted through its decrypted value.
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{"--site-key", "2x00BB", "--data-dir", dir}))
	snap = captchaSnapshot(t, dir)
	assert.Equal(t, captcha.ProviderTurnstile, effectiveProvider(snap))
	assert.True(t, captcha.CaptchaEnabledKey.Of(snap))
	assert.Equal(t, "2x00BB", captcha.TurnstileKey.Of(snap).SiteKey)
	assert.Equal(t, "0x00AA", captcha.TurnstileKey.Of(snap).SecretKey, "a site-key edit must not lose the secret")

	// Switching back to altcha reuses the original row and its secret.
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{"--provider", "altcha", "--data-dir", dir}))
	snap = captchaSnapshot(t, dir)
	assert.Equal(t, captcha.ProviderAltcha, effectiveProvider(snap))
	assert.True(t, captcha.CaptchaEnabledKey.Of(snap))
	assert.Equal(t, altchaSecret, captcha.AltchaKey.Of(snap).HMACKey)

	// The recaptcha_v3 flow persists its score setting.
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{
		"--provider", "recaptcha_v3", "--site-key", "site", "--secret", "sec", "--min-score", "0.7",
		"--data-dir", dir,
	}))
	snap = captchaSnapshot(t, dir)
	assert.Equal(t, captcha.ProviderRecaptchaV3, effectiveProvider(snap))
	rs := captcha.RecaptchaV3Key.Of(snap)
	assert.Equal(t, "site", rs.SiteKey)
	assert.Equal(t, 0.7, rs.MinScore)

	// An explicit --min-score 0 restores the 0.5 default rather than
	// being indistinguishable from an unset flag.
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{"--min-score", "0", "--data-dir", dir}))
	assert.Equal(t, 0.5, captcha.RecaptchaV3Key.Of(captchaSnapshot(t, dir)).MinScore)

	// Per-provider reset drops only that provider's row. Deleting the
	// SELECTED provider re-provisions the default altcha row immediately
	// — never mid-request on the hub's next use — while the other
	// providers' rows survive untouched.
	require.NoError(t, runCaptchaReset(testAdminCtx, []string{"--provider", "recaptcha_v3", "--data-dir", dir}))
	st = openAdminStore(t, dir)
	_, err = st.Settings().Get(context.Background(), "captcha.recaptcha_v3")
	assert.ErrorIs(t, err, store.ErrNotFound)
	snap = captchaSnapshot(t, dir)
	assert.Equal(t, captcha.ProviderAltcha, effectiveProvider(snap), "reset must re-select the default provider at once")
	rows, err := st.Settings().GetAll(context.Background())
	require.NoError(t, err)
	assert.NotEmpty(t, rows, "the other providers' rows survive")

	err = runCaptchaReset(testAdminCtx, []string{"--provider", "nope", "--data-dir", dir})
	require.ErrorContains(t, err, "unsupported captcha provider")
}

// TestCLI_CaptchaSwitchBackKeepsStoredSettings pins the one-row-per-
// provider contract: a switch back to a provider keeps that row's stored
// settings — only the selection changes — and a fully configured row
// switches with no flags at all.
func TestCLI_CaptchaSwitchBackKeepsStoredSettings(t *testing.T) {
	dir := setupTestDataDir(t)

	// Tune altcha, then leave it for turnstile.
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{
		"--algorithm", "ARGON2ID", "--cost", "3", "--expires", "7m", "--data-dir", dir,
	}))
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{
		"--provider", "turnstile", "--site-key", "1x00000000000000000000AA", "--secret", "0x00AA",
		"--data-dir", dir,
	}))

	// Switch back with no flags: the stored tuning survives. The secret
	// survives too (pinned by TestCLI_CaptchaProviderSwitching); before the
	// fix the settings silently reverted to PBKDF2/10000/20m.
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{"--provider", "altcha", "--data-dir", dir}))
	snap := captchaSnapshot(t, dir)
	assert.Equal(t, captcha.ProviderAltcha, effectiveProvider(snap))
	s := altchaSettingsOf(t, snap)
	assert.Equal(t, "ARGON2ID", s.Algorithm)
	assert.EqualValues(t, 3, s.Cost)
	assert.EqualValues(t, 420, s.ChallengeExpirySeconds)

	// A configured external row switches back the same way: no re-supplied
	// keys, and the stored min_score survives the round trip.
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{
		"--provider", "recaptcha_v3", "--site-key", "site", "--secret", "sec", "--min-score", "0.8",
		"--data-dir", dir,
	}))
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{"--provider", "turnstile", "--data-dir", dir}))
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{"--provider", "recaptcha_v3", "--data-dir", dir}))
	snap = captchaSnapshot(t, dir)
	assert.Equal(t, captcha.ProviderRecaptchaV3, effectiveProvider(snap))
	rs := captcha.RecaptchaV3Key.Of(snap)
	assert.Equal(t, "site", rs.SiteKey)
	assert.Equal(t, 0.8, rs.MinScore, "a switch back must keep the stored settings")
}

// TestCLI_CaptchaSameProviderFlagTunesInPlace pins that specifying the
// already-selected provider with --provider is a tuning edit, not a
// switch: no keys are demanded and the selection stays.
func TestCLI_CaptchaSameProviderFlagTunesInPlace(t *testing.T) {
	dir := setupTestDataDir(t)

	require.NoError(t, runCaptchaSet(testAdminCtx, []string{
		"--provider", "recaptcha_v3", "--site-key", "site", "--secret", "sec",
		"--data-dir", dir,
	}))

	// No --site-key/--secret re-supply, no error: the provider is already
	// selected, so this is an in-place edit.
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{
		"--provider", "recaptcha_v3", "--min-score", "0.8", "--data-dir", dir,
	}))
	snap := captchaSnapshot(t, dir)
	assert.Equal(t, captcha.ProviderRecaptchaV3, effectiveProvider(snap))
	rs := captcha.RecaptchaV3Key.Of(snap)
	assert.Equal(t, "site", rs.SiteKey)
	assert.Equal(t, 0.8, rs.MinScore)
}

// TestCLI_CaptchaSwitchReenablesVerification pins the old Activate
// invariant the settings rewrite had dropped: choosing a provider means
// running it. A switch with the provider's keys must leave verification
// enabled even when the hub had been disabled — the success message says
// so, and a hub disabled for debugging must not silently stay undefended
// through a provider change. In-place tuning leaves the switch alone.
func TestCLI_CaptchaSwitchReenablesVerification(t *testing.T) {
	dir := setupTestDataDir(t)

	// Disable, then switch to an external provider with its keys.
	require.NoError(t, runCaptchaSetEnabled(testAdminCtx, []string{"--data-dir", dir}, false))
	snap := captchaSnapshot(t, dir)
	assert.False(t, captcha.CaptchaEnabledKey.Of(snap))

	require.NoError(t, runCaptchaSet(testAdminCtx, []string{
		"--provider", "recaptcha_v3", "--site-key", "site-key", "--secret", "api-secret",
		"--data-dir", dir,
	}))
	snap = captchaSnapshot(t, dir)
	assert.Equal(t, captcha.ProviderRecaptchaV3, effectiveProvider(snap))
	assert.True(t, captcha.CaptchaEnabledKey.Of(snap), "switching providers re-enables verification")

	// Disabling again and tuning the selected provider in place must NOT
	// re-enable: the update path leaves the on/off switch alone.
	require.NoError(t, runCaptchaSetEnabled(testAdminCtx, []string{"--data-dir", dir}, false))
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{"--min-score", "0.9", "--data-dir", dir}))
	snap = captchaSnapshot(t, dir)
	assert.False(t, captcha.CaptchaEnabledKey.Of(snap), "in-place tuning leaves the enabled switch alone")
	assert.Equal(t, 0.9, captcha.RecaptchaV3Key.Of(snap).MinScore)
}
