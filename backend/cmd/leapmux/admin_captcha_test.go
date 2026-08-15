package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/captcha"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
)

func openAdminStore(t *testing.T, dir string) store.Store {
	t.Helper()
	st, err := sqlite.Open(dir+"/hub.db", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func getSelectedRow(t *testing.T, st store.Store) *store.CaptchaConfig {
	t.Helper()
	row, err := st.CaptchaConfig().GetSelected(context.Background())
	require.NoError(t, err)
	return row
}

func altchaSettingsOf(t *testing.T, row *store.CaptchaConfig) captcha.AltchaSettings {
	t.Helper()
	var s captcha.AltchaSettings
	require.NoError(t, json.Unmarshal([]byte(row.Settings), &s))
	return s
}

func TestCLI_CaptchaSetShowEnableDisableReset(t *testing.T) {
	dir := setupTestDataDir(t)

	// show on a fresh install reports built-in defaults, writing nothing.
	require.NoError(t, runCaptchaShow(testAdminCtx, []string{"--data-dir", dir}))
	st := openAdminStore(t, dir)
	_, err := st.CaptchaConfig().GetSelected(context.Background())
	assert.ErrorIs(t, err, store.ErrNotFound)

	// set provisions the row and persists the change.
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{
		"--algorithm", "SHA-256", "--cost", "2000", "--expires", "10m",
		"--data-dir", dir,
	}))
	row := getSelectedRow(t, st)
	assert.Equal(t, captcha.ProviderAltcha, row.Provider)
	assert.True(t, row.Enabled)
	s := altchaSettingsOf(t, row)
	assert.Equal(t, "SHA-256", s.Algorithm)
	assert.EqualValues(t, 2000, s.Cost)
	assert.EqualValues(t, 600, s.ChallengeExpirySeconds)
	assert.NotEmpty(t, row.Secret, "set must provision a signing secret")

	// disable keeps settings and the selection but flips the switch.
	require.NoError(t, runCaptchaSetEnabled(testAdminCtx, []string{"--data-dir", dir}, false))
	row = getSelectedRow(t, st)
	assert.False(t, row.Enabled)
	assert.True(t, row.Selected)
	assert.Equal(t, "SHA-256", altchaSettingsOf(t, row).Algorithm)

	require.NoError(t, runCaptchaSetEnabled(testAdminCtx, []string{"--data-dir", dir}, true))
	assert.True(t, getSelectedRow(t, st).Enabled)

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

	// reset removes the row entirely.
	require.NoError(t, runCaptchaReset(testAdminCtx, []string{"--data-dir", dir}))
	_, err = st.CaptchaConfig().GetSelected(context.Background())
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestCLI_CaptchaSetSecretSurvivesAndVerifies(t *testing.T) {
	dir := setupTestDataDir(t)

	require.NoError(t, runCaptchaSetEnabled(testAdminCtx, []string{"--data-dir", dir}, true))
	st := openAdminStore(t, dir)
	firstSecret := getSelectedRow(t, st).Secret

	// A subsequent set keeps the same secret: the settings-only update
	// never touches the column, so no caller can lose or corrupt the key.
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{"--cost", "20000", "--data-dir", dir}))
	row := getSelectedRow(t, st)
	assert.Equal(t, firstSecret, row.Secret, "set must not rotate the signing secret")
	assert.EqualValues(t, 20000, altchaSettingsOf(t, row).Cost)

	// The stored secret decrypts with the data-dir keystore and produces
	// verifiable challenges.
	ks, err := keystore.LoadOrGenerate(adminConfig(dir).EncryptionKeyFilePath())
	require.NoError(t, err)
	plain, err := ks.Decrypt(row.Secret, keystore.CaptchaSecretAAD("altcha"))
	require.NoError(t, err)
	assert.Len(t, plain, 32)

	m := captcha.NewManager(st, ks, false)
	challengeJSON, err := m.AltchaChallengeJSON(context.Background())
	require.NoError(t, err)
	assert.NotEmpty(t, challengeJSON)
}

// TestCLI_CaptchaSetFamilySwitchResetsParams pins the family-carryover
// fix: switching algorithms must not reinterpret the old family's
// memory parameters in the new family's units.
func TestCLI_CaptchaSetFamilySwitchResetsParams(t *testing.T) {
	dir := setupTestDataDir(t)
	st := openAdminStore(t, dir)

	// Configure ARGON2ID with a memory cost (KiB) and parallelism.
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{
		"--algorithm", "ARGON2ID", "--memory-cost", "32768", "--parallelism", "2",
		"--data-dir", dir,
	}))
	s := altchaSettingsOf(t, getSelectedRow(t, st))
	assert.EqualValues(t, 32768, s.MemoryCost)
	assert.EqualValues(t, 2, s.Parallelism)

	// Switching to SCRYPT without touching the memory parameters resets
	// them to SCRYPT's defaults — 32768 would otherwise become SCRYPT's
	// block multiplier r and demand 128 * N * r per derivation.
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{
		"--algorithm", "SCRYPT", "--data-dir", dir,
	}))
	s = altchaSettingsOf(t, getSelectedRow(t, st))
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
	st := openAdminStore(t, dir)

	require.NoError(t, runCaptchaSet(testAdminCtx, []string{
		"--algorithm", "ARGON2ID", "--memory-cost", "32768", "--data-dir", dir,
	}))
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{
		"--memory-cost", "0", "--expires", "30m", "--data-dir", dir,
	}))
	s := altchaSettingsOf(t, getSelectedRow(t, st))
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
	st := openAdminStore(t, dir)

	require.NoError(t, runCaptchaSet(testAdminCtx, []string{
		"--algorithm", "ARGON2ID", "--expires", "5m", "--data-dir", dir,
	}))
	assert.EqualValues(t, 300, altchaSettingsOf(t, getSelectedRow(t, st)).ChallengeExpirySeconds)

	require.NoError(t, runCaptchaSet(testAdminCtx, []string{"--algorithm", "SCRYPT", "--data-dir", dir}))
	s := altchaSettingsOf(t, getSelectedRow(t, st))
	assert.Equal(t, "SCRYPT", s.Algorithm)
	assert.EqualValues(t, 300, s.ChallengeExpirySeconds, "an algorithm switch must not touch the stored expiry")
}

// TestCLI_CaptchaSetExpiresZeroRestoresDefault pins the explicit-zero
// idiom for --expires, matching the other tunable flags.
func TestCLI_CaptchaSetExpiresZeroRestoresDefault(t *testing.T) {
	dir := setupTestDataDir(t)
	st := openAdminStore(t, dir)

	require.NoError(t, runCaptchaSet(testAdminCtx, []string{"--expires", "2h", "--data-dir", dir}))
	assert.EqualValues(t, 7200, altchaSettingsOf(t, getSelectedRow(t, st)).ChallengeExpirySeconds)

	require.NoError(t, runCaptchaSet(testAdminCtx, []string{"--expires", "0", "--data-dir", dir}))
	defaults := captcha.DefaultAltchaSettings()
	assert.EqualValues(t, defaults.ChallengeExpirySeconds, altchaSettingsOf(t, getSelectedRow(t, st)).ChallengeExpirySeconds)
}

// TestCLI_CaptchaProviderSwitching pins the provider lifecycle: switching
// to an external provider requires its keys, per-provider rows keep their
// own secrets across switches, and provider-foreign flags are refused.
func TestCLI_CaptchaProviderSwitching(t *testing.T) {
	dir := setupTestDataDir(t)
	st := openAdminStore(t, dir)

	// Provision altcha and capture its secret.
	require.NoError(t, runCaptchaSetEnabled(testAdminCtx, []string{"--data-dir", dir}, true))
	altchaSecret := getSelectedRow(t, st).Secret

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
	// operator's secret stored encrypted.
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{
		"--provider", "turnstile", "--site-key", "1x00000000000000000000AA", "--secret", "0x00AA",
		"--data-dir", dir,
	}))
	row := getSelectedRow(t, st)
	assert.Equal(t, captcha.ProviderTurnstile, row.Provider)
	assert.True(t, row.Enabled)
	var ts captcha.TurnstileSettings
	require.NoError(t, json.Unmarshal([]byte(row.Settings), &ts))
	assert.Equal(t, "1x00000000000000000000AA", ts.SiteKey)

	ks, err := keystore.LoadOrGenerate(adminConfig(dir).EncryptionKeyFilePath())
	require.NoError(t, err)
	plain, err := ks.Decrypt(row.Secret, keystore.CaptchaSecretAAD("turnstile"))
	require.NoError(t, err)
	assert.Equal(t, "0x00AA", string(plain))

	// The altcha row survives the switch with its original secret.
	altchaRow, err := st.CaptchaConfig().Get(context.Background(), captcha.ProviderAltcha)
	require.NoError(t, err)
	assert.False(t, altchaRow.Selected)
	assert.Equal(t, altchaSecret, altchaRow.Secret, "switching away must not regenerate the altcha secret")

	// Provider-foreign flags are refused rather than ignored.
	err = runCaptchaSet(testAdminCtx, []string{"--cost", "20000", "--data-dir", dir})
	require.ErrorContains(t, err, "altcha-only flag")
	err = runCaptchaSet(testAdminCtx, []string{"--min-score", "0.7", "--data-dir", dir})
	require.ErrorContains(t, err, "recaptcha_v3-only flag")

	// A settings-only edit of the selected external provider updates the
	// site key, keeps the secret, and leaves the selection enabled.
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{"--site-key", "2x00BB", "--data-dir", dir}))
	row = getSelectedRow(t, st)
	assert.Equal(t, captcha.ProviderTurnstile, row.Provider)
	assert.True(t, row.Enabled)
	secretBefore := row.Secret
	require.NoError(t, json.Unmarshal([]byte(row.Settings), &ts))
	assert.Equal(t, "2x00BB", ts.SiteKey)
	plain, err = ks.Decrypt(row.Secret, keystore.CaptchaSecretAAD("turnstile"))
	require.NoError(t, err)
	assert.Equal(t, "0x00AA", string(plain), "a site-key edit must not lose the secret")
	require.Equal(t, secretBefore, row.Secret)

	// Switching back to altcha reuses the original row and its secret.
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{"--provider", "altcha", "--data-dir", dir}))
	row = getSelectedRow(t, st)
	assert.Equal(t, captcha.ProviderAltcha, row.Provider)
	assert.True(t, row.Enabled)
	assert.Equal(t, altchaSecret, row.Secret)

	// The recaptcha_v3 flow persists its score setting.
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{
		"--provider", "recaptcha_v3", "--site-key", "site", "--secret", "sec", "--min-score", "0.7",
		"--data-dir", dir,
	}))
	row = getSelectedRow(t, st)
	assert.Equal(t, captcha.ProviderRecaptchaV3, row.Provider)
	var rs captcha.RecaptchaV3Settings
	require.NoError(t, json.Unmarshal([]byte(row.Settings), &rs))
	assert.Equal(t, "site", rs.SiteKey)
	assert.Equal(t, 0.7, rs.MinScore)

	// An explicit --min-score 0 restores the 0.5 default rather than
	// being indistinguishable from an unset flag.
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{"--min-score", "0", "--data-dir", dir}))
	row = getSelectedRow(t, st)
	require.NoError(t, json.Unmarshal([]byte(row.Settings), &rs))
	assert.Equal(t, 0.5, rs.MinScore)

	// Per-provider reset drops only that provider's row. Deleting the SELECTED
	// provider leaves nothing selected until the hub's lazy provisioning
	// re-arms the altcha row.
	require.NoError(t, runCaptchaReset(testAdminCtx, []string{"--provider", "recaptcha_v3", "--data-dir", dir}))
	_, err = st.CaptchaConfig().Get(context.Background(), captcha.ProviderRecaptchaV3)
	assert.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.CaptchaConfig().GetSelected(context.Background())
	assert.ErrorIs(t, err, store.ErrNotFound, "the hub lazily re-provisions altcha on next use")
	rows, err := st.CaptchaConfig().List(context.Background())
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
	st := openAdminStore(t, dir)

	// Tune altcha, then leave it for turnstile.
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{
		"--algorithm", "ARGON2ID", "--cost", "3", "--expires", "7m", "--data-dir", dir,
	}))
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{
		"--provider", "turnstile", "--site-key", "1x00000000000000000000AA", "--secret", "0x00AA",
		"--data-dir", dir,
	}))

	// Switch back with no flags: the stored tuning survives. The secret
	// survives too (pinned by TestCLI_CaptchaProviderSwitching); before
	// the fix the settings silently reverted to PBKDF2/10000/20m.
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{"--provider", "altcha", "--data-dir", dir}))
	row := getSelectedRow(t, st)
	assert.Equal(t, captcha.ProviderAltcha, row.Provider)
	s := altchaSettingsOf(t, row)
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
	row = getSelectedRow(t, st)
	assert.Equal(t, captcha.ProviderRecaptchaV3, row.Provider)
	var rs captcha.RecaptchaV3Settings
	require.NoError(t, json.Unmarshal([]byte(row.Settings), &rs))
	assert.Equal(t, "site", rs.SiteKey)
	assert.Equal(t, 0.8, rs.MinScore, "a switch back must keep the stored settings")
}

// TestCLI_CaptchaSameProviderFlagTunesInPlace pins that naming the
// already-selected provider with --provider is a tuning edit, not a
// switch: no keys are demanded and the selection stays.
func TestCLI_CaptchaSameProviderFlagTunesInPlace(t *testing.T) {
	dir := setupTestDataDir(t)
	st := openAdminStore(t, dir)

	require.NoError(t, runCaptchaSet(testAdminCtx, []string{
		"--provider", "recaptcha_v3", "--site-key", "site", "--secret", "sec",
		"--data-dir", dir,
	}))

	// No --site-key/--secret re-supply, no error: the provider is already
	// selected, so this is an in-place edit.
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{
		"--provider", "recaptcha_v3", "--min-score", "0.8", "--data-dir", dir,
	}))
	row := getSelectedRow(t, st)
	assert.Equal(t, captcha.ProviderRecaptchaV3, row.Provider)
	var rs captcha.RecaptchaV3Settings
	require.NoError(t, json.Unmarshal([]byte(row.Settings), &rs))
	assert.Equal(t, "site", rs.SiteKey)
	assert.Equal(t, 0.8, rs.MinScore)
}
