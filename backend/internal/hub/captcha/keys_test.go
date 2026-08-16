package captcha

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// crossRuleManager builds a settings manager over the captcha keys with
// the SelectedConfigured cross rule attached — the wiring the hub, the
// CLI, and settingsregistry all use.
func crossRuleManager(t *testing.T) *settings.Manager {
	t.Helper()
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	key, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)
	m := settings.NewManager(st, ks, SettingsDescriptors(),
		settings.WithCrossValidation(SelectedConfigured))
	require.NoError(t, m.Load(context.Background()))
	return m
}

// TestSelectedConfiguredRejectsUnconfiguredSelection pins the
// write-path guard: selecting an external provider whose row has no
// stored keys must be refused wherever it is introduced — the state
// otherwise sits stored while Effective silently runs ALTCHA, telling
// the operator two different things.
func TestSelectedConfiguredRejectsUnconfiguredSelection(t *testing.T) {
	m := crossRuleManager(t)
	ctx := context.Background()

	err := m.Update(ctx, CaptchaSelectedKey, json.RawMessage(`"recaptcha_v3"`))
	require.ErrorContains(t, err, "requires its site key and secret")

	// Configuring the row first makes the same selection succeed.
	require.NoError(t, RecaptchaV3Key.Set(ctx, m, RecaptchaV3Row{
		SiteKey: "site-key", MinScore: 0.5, SecretKey: "api-secret",
	}))
	require.NoError(t, m.Update(ctx, CaptchaSelectedKey, json.RawMessage(`"recaptcha_v3"`)))
	assert.Equal(t, "recaptcha_v3", CaptchaSelectedKey.Of(m.Snapshot(ctx)))

	// Clearing the keys of the SELECTED provider is refused the same way.
	err = m.Update(ctx, RecaptchaV3Key, json.RawMessage(`{"site_key":""}`))
	require.ErrorContains(t, err, "requires its site key and secret")

	// Resetting the selected provider's row directly is refused too; the
	// selection must move first (the CLI wrapper orders them that way).
	err = m.Reset(ctx, RecaptchaV3Key)
	require.ErrorContains(t, err, "requires its site key and secret")
	require.NoError(t, m.Reset(ctx, CaptchaSelectedKey))
	require.NoError(t, m.Reset(ctx, RecaptchaV3Key))

	// Selecting altcha needs nothing: its row self-provisions.
	require.NoError(t, m.Update(ctx, CaptchaSelectedKey, json.RawMessage(`"altcha"`)))
}

// TestRecaptchaRowRejectsZeroMinScore pins the validator agreement: the
// provider's own validation rejects a zero threshold (it would accept
// every score), so the write path must reject it too — with an error,
// not a silent round-trip back to the 0.5 default.
func TestRecaptchaRowRejectsZeroMinScore(t *testing.T) {
	m := crossRuleManager(t)
	ctx := context.Background()

	err := m.Update(ctx, RecaptchaV3Key, json.RawMessage(`{"min_score":0}`))
	require.ErrorContains(t, err, "greater than 0")

	require.NoError(t, m.Update(ctx, RecaptchaV3Key, json.RawMessage(`{"min_score":0.9}`)))
	assert.Equal(t, 0.9, RecaptchaV3Key.Of(m.Snapshot(ctx)).MinScore)
}
