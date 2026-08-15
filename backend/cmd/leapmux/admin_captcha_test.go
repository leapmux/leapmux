package main

import (
	"context"
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

func TestCLI_CaptchaSetShowEnableDisableReset(t *testing.T) {
	dir := setupTestDataDir(t)

	// show on a fresh install reports built-in defaults, writing nothing.
	require.NoError(t, runCaptchaShow(testAdminCtx, []string{"--data-dir", dir}))
	st := openAdminStore(t, dir)
	_, err := st.CaptchaConfig().Get(context.Background())
	assert.ErrorIs(t, err, store.ErrNotFound)

	// set provisions the row and persists the change.
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{
		"--algorithm", "SHA-256", "--cost", "2000", "--expires", "10m",
		"--data-dir", dir,
	}))
	row, err := st.CaptchaConfig().Get(context.Background())
	require.NoError(t, err)
	assert.True(t, row.Enabled)
	assert.Equal(t, "SHA-256", row.Algorithm)
	assert.EqualValues(t, 2000, row.Cost)
	assert.EqualValues(t, 600, row.ChallengeExpirySeconds)
	assert.NotEmpty(t, row.Secret, "set must provision a signing secret")

	// disable keeps settings but flips the switch.
	require.NoError(t, runCaptchaSetEnabled(testAdminCtx, []string{"--data-dir", dir}, false))
	row, err = st.CaptchaConfig().Get(context.Background())
	require.NoError(t, err)
	assert.False(t, row.Enabled)
	assert.Equal(t, "SHA-256", row.Algorithm)

	require.NoError(t, runCaptchaSetEnabled(testAdminCtx, []string{"--data-dir", dir}, true))
	row, err = st.CaptchaConfig().Get(context.Background())
	require.NoError(t, err)
	assert.True(t, row.Enabled)

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
	_, err = st.CaptchaConfig().Get(context.Background())
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestCLI_CaptchaSetSecretSurvivesAndVerifies(t *testing.T) {
	dir := setupTestDataDir(t)

	require.NoError(t, runCaptchaSetEnabled(testAdminCtx, []string{"--data-dir", dir}, true))
	st := openAdminStore(t, dir)
	row, err := st.CaptchaConfig().Get(context.Background())
	require.NoError(t, err)
	firstSecret := row.Secret

	// A subsequent set keeps the same secret: the UPDATE never touches the
	// column, so no caller can lose or corrupt the key.
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{"--cost", "20000", "--data-dir", dir}))
	row, err = st.CaptchaConfig().Get(context.Background())
	require.NoError(t, err)
	assert.Equal(t, firstSecret, row.Secret, "set must not rotate the signing secret")
	assert.EqualValues(t, 20000, row.Cost)

	// The stored secret decrypts with the data-dir keystore and produces
	// verifiable challenges.
	ks, err := keystore.LoadOrGenerate(adminConfig(dir).EncryptionKeyFilePath())
	require.NoError(t, err)
	plain, err := ks.Decrypt(row.Secret, keystore.CaptchaSecretAAD())
	require.NoError(t, err)
	assert.Len(t, plain, 32)

	m := captcha.NewManager(st, ks, false)
	challengeJSON, err := m.ChallengeJSON(context.Background())
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
	row, err := st.CaptchaConfig().Get(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 32768, row.MemoryCost)
	assert.EqualValues(t, 2, row.Parallelism)

	// Switching to SCRYPT without touching the memory parameters resets
	// them to SCRYPT's defaults — 32768 would otherwise become SCRYPT's
	// block multiplier r and demand 128 * N * r per derivation.
	require.NoError(t, runCaptchaSet(testAdminCtx, []string{
		"--algorithm", "SCRYPT", "--data-dir", dir,
	}))
	row, err = st.CaptchaConfig().Get(context.Background())
	require.NoError(t, err)
	scryptDefaults, err := captcha.FamilyDefaults("SCRYPT")
	require.NoError(t, err)
	assert.EqualValues(t, scryptDefaults.Cost, row.Cost)
	assert.EqualValues(t, scryptDefaults.MemoryCost, row.MemoryCost)
	assert.EqualValues(t, scryptDefaults.Parallelism, row.Parallelism)
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
	row, err := st.CaptchaConfig().Get(context.Background())
	require.NoError(t, err)
	argonDefaults, err := captcha.FamilyDefaults("ARGON2ID")
	require.NoError(t, err)
	assert.EqualValues(t, argonDefaults.MemoryCost, row.MemoryCost, "explicit 0 must restore the family default")
	assert.EqualValues(t, 1800, row.ChallengeExpirySeconds)
}
