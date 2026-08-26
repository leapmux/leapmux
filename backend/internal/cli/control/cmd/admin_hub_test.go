package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/hub/captcha"
	"github.com/leapmux/leapmux/internal/hub/ratelimit"
)

// fakeAdminHub answers the admin RPCs one `control admin ...` verb makes,
// and records what the verb sent.
//
// Several rules the CLI holds are observable only ACROSS the dial: a verb
// that refuses a write before sending it, one that collapses N calls into
// one, and one that reads a value out of a reply instead of asking for it
// again. A flag-level test cannot tell any of those apart from a verb that
// simply failed to connect, so those tests run against this hub.
type fakeAdminHub struct {
	leapmuxv1connect.UnimplementedAdminSettingsServiceHandler
	leapmuxv1connect.UnimplementedAdminUserServiceHandler
	leapmuxv1connect.UnimplementedAdminOAuthServiceHandler

	mu sync.Mutex

	// values and descriptors answer ListSettings.
	values      []*leapmuxv1.SettingValue
	descriptors []*leapmuxv1.SettingDescriptor
	// restart is what UpdateSetting reports for the written key.
	restart bool
	// resetErr, when set, refuses ResetSettings.
	resetErr error
	// resetPasswordErr, when set, refuses ResetPassword.
	resetPasswordErr error
	// lockedOutUsers is what RemoveOAuthProvider reports: how many
	// accounts lost their last login method with the provider.
	lockedOutUsers int64

	listCalls        int
	updated          []*leapmuxv1.UpdateSettingRequest
	updatedMany      []*leapmuxv1.UpdateSettingsRequest
	resetKeys        [][]string
	setAdmin         []*leapmuxv1.SetUserAdminRequest
	resetPasswords   []*leapmuxv1.ResetPasswordRequest
	issuedTokens     []*leapmuxv1.IssueAPITokenRequest
	removedProviders []*leapmuxv1.RemoveOAuthProviderRequest
}

func (h *fakeAdminHub) ListSettings(
	context.Context, *connect.Request[leapmuxv1.ListSettingsRequest],
) (*connect.Response[leapmuxv1.ListSettingsResponse], error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.listCalls++
	return connect.NewResponse(&leapmuxv1.ListSettingsResponse{
		Descriptors: h.descriptors,
		Values:      h.values,
	}), nil
}

func (h *fakeAdminHub) UpdateSetting(
	_ context.Context, req *connect.Request[leapmuxv1.UpdateSettingRequest],
) (*connect.Response[leapmuxv1.UpdateSettingResponse], error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.updated = append(h.updated, req.Msg)
	return connect.NewResponse(&leapmuxv1.UpdateSettingResponse{
		Value:   &leapmuxv1.SettingValue{Key: req.Msg.GetKey(), EffectiveJson: req.Msg.GetPartialJson()},
		Restart: h.restart,
	}), nil
}

func (h *fakeAdminHub) UpdateSettings(
	_ context.Context, req *connect.Request[leapmuxv1.UpdateSettingsRequest],
) (*connect.Response[leapmuxv1.UpdateSettingsResponse], error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.updatedMany = append(h.updatedMany, req.Msg)
	values := make([]*leapmuxv1.SettingValue, 0, len(req.Msg.GetWrites()))
	for _, w := range req.Msg.GetWrites() {
		values = append(values, &leapmuxv1.SettingValue{Key: w.GetKey()})
	}
	return connect.NewResponse(&leapmuxv1.UpdateSettingsResponse{Values: values}), nil
}

func (h *fakeAdminHub) ResetSettings(
	_ context.Context, req *connect.Request[leapmuxv1.ResetSettingsRequest],
) (*connect.Response[leapmuxv1.ResetSettingsResponse], error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.resetKeys = append(h.resetKeys, req.Msg.GetKeys())
	if h.resetErr != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, h.resetErr)
	}
	values := make([]*leapmuxv1.SettingValue, 0, len(req.Msg.GetKeys()))
	for _, key := range req.Msg.GetKeys() {
		values = append(values, &leapmuxv1.SettingValue{Key: key})
	}
	return connect.NewResponse(&leapmuxv1.ResetSettingsResponse{Values: values}), nil
}

func (h *fakeAdminHub) SetUserAdmin(
	_ context.Context, req *connect.Request[leapmuxv1.SetUserAdminRequest],
) (*connect.Response[leapmuxv1.SetUserAdminResponse], error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.setAdmin = append(h.setAdmin, req.Msg)
	return connect.NewResponse(&leapmuxv1.SetUserAdminResponse{
		User: &leapmuxv1.AdminUser{Id: "u-1", Username: req.Msg.GetUsername(), IsAdmin: req.Msg.GetIsAdmin()},
	}), nil
}

// ResetPassword answers with the SUBJECT, in the shape the hub does: it
// fills the handle the caller did not send and leaves the one it did.
//
// The unsent handle is the point. A caller that resets by username holds no
// user id and a caller that resets by id holds no username, so the verb can
// print both only by reading them out of this reply. A verb that echoed its
// own request would print an empty string for one of them, and this stub is
// what makes that visible.
func (h *fakeAdminHub) ResetPassword(
	_ context.Context, req *connect.Request[leapmuxv1.ResetPasswordRequest],
) (*connect.Response[leapmuxv1.ResetPasswordResponse], error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.resetPasswords = append(h.resetPasswords, req.Msg)
	if h.resetPasswordErr != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, h.resetPasswordErr)
	}
	userID, username := req.Msg.GetId(), req.Msg.GetUsername()
	if userID == "" {
		userID = "u-7"
	}
	if username == "" {
		username = "amy"
	}
	// The two counts differ, so a verb that prints one under the other
	// key fails instead of matching by luck.
	return connect.NewResponse(&leapmuxv1.ResetPasswordResponse{
		UserId: userID, Username: username,
		ApiTokensRevoked: 4, DelegationTokensRevoked: 2,
	}), nil
}

func (h *fakeAdminHub) IssueAPIToken(
	_ context.Context, req *connect.Request[leapmuxv1.IssueAPITokenRequest],
) (*connect.Response[leapmuxv1.IssueAPITokenResponse], error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.issuedTokens = append(h.issuedTokens, req.Msg)
	return connect.NewResponse(&leapmuxv1.IssueAPITokenResponse{
		TokenId: "t-1", AccessToken: "lmx_a_x", RefreshToken: "lmx_r_x",
	}), nil
}

func (h *fakeAdminHub) RemoveOAuthProvider(
	_ context.Context, req *connect.Request[leapmuxv1.RemoveOAuthProviderRequest],
) (*connect.Response[leapmuxv1.RemoveOAuthProviderResponse], error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.removedProviders = append(h.removedProviders, req.Msg)
	return connect.NewResponse(&leapmuxv1.RemoveOAuthProviderResponse{
		LockedOutUsers: h.lockedOutUsers,
	}), nil
}

// The takers below read a recording and clear it, so a test that runs two
// verbs reads each one's calls without holding the lock itself.
func (h *fakeAdminHub) takeUpdates() []*leapmuxv1.UpdateSettingRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.updated
	h.updated = nil
	return out
}

func (h *fakeAdminHub) takeUpdatesMany() []*leapmuxv1.UpdateSettingsRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.updatedMany
	h.updatedMany = nil
	return out
}

func (h *fakeAdminHub) takeResets() [][]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.resetKeys
	h.resetKeys = nil
	return out
}

func (h *fakeAdminHub) takeSetAdmin() []*leapmuxv1.SetUserAdminRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.setAdmin
	h.setAdmin = nil
	return out
}

func (h *fakeAdminHub) takeResetPasswords() []*leapmuxv1.ResetPasswordRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.resetPasswords
	h.resetPasswords = nil
	return out
}

func (h *fakeAdminHub) takeIssuedTokens() []*leapmuxv1.IssueAPITokenRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.issuedTokens
	h.issuedTokens = nil
	return out
}

func (h *fakeAdminHub) takeRemovedProviders() []*leapmuxv1.RemoveOAuthProviderRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.removedProviders
	h.removedProviders = nil
	return out
}

func (h *fakeAdminHub) listCallCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.listCalls
}

// startAdminHub serves hub over httptest and returns its --hub value.
//
// The credential directory points at an empty temporary one, so the lookup
// finds no credential and the verb runs anonymously — which is what
// requireAdminClient does against a solo hub.
func startAdminHub(t *testing.T, hub *fakeAdminHub) string {
	t.Helper()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	t.Setenv("LEAPMUX_CONTROL_SOCK", "")

	mux := http.NewServeMux()
	mux.Handle(leapmuxv1connect.NewAdminSettingsServiceHandler(hub))
	mux.Handle(leapmuxv1connect.NewAdminUserServiceHandler(hub))
	mux.Handle(leapmuxv1connect.NewAdminOAuthServiceHandler(hub))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// settingValue builds one ListSettings row.
func settingValue(key, effectiveJSON string, secretSet map[string]bool) *leapmuxv1.SettingValue {
	return &leapmuxv1.SettingValue{Key: key, EffectiveJson: effectiveJSON, SecretSet: secretSet}
}

// captchaHubValues builds a whole captcha configuration: the selection, the
// enable switch, and one empty row for each provider.
//
// A row in `rows` replaces the empty one for the same key, so a test states
// only the provider it configures and inherits the rest. Turnstile used to
// be a required parameter and recaptcha_v3 a hardcoded empty row, which
// left the second provider unreachable to a test that needed it configured.
func captchaHubValues(selected, altchaJSON string, rows ...*leapmuxv1.SettingValue) []*leapmuxv1.SettingValue {
	values := []*leapmuxv1.SettingValue{
		settingValue(captcha.CaptchaEnabledKey.Name(), "true", nil),
		settingValue(captcha.CaptchaSelectedKey.Name(), `"`+selected+`"`, nil),
		settingValue(captcha.AltchaKey.Name(), altchaJSON, map[string]bool{"hmac_key": true}),
		settingValue(captcha.RecaptchaV3Key.Name(), "{}", nil),
		settingValue(captcha.TurnstileKey.Name(), "{}", nil),
	}
	for _, row := range rows {
		replaced := false
		for i, v := range values {
			if v.GetKey() == row.GetKey() {
				values[i], replaced = row, true
				break
			}
		}
		// A row for a key the base set does not hold still reaches the hub,
		// so a caller that adds one is not silently ignored.
		if !replaced {
			values = append(values, row)
		}
	}
	return values
}

// captchaSetDoc runs `captcha set` with args and returns the partial
// document that the ONE UpdateSettings write carries for key.
//
// The single-request check is part of the contract, not scaffolding: every
// key a `captcha set` invocation touches travels together, so the hub runs
// the cross-key rules once over the whole result.
func captchaSetDoc(t *testing.T, hub *fakeAdminHub, url, key string, args ...string) map[string]any {
	t.Helper()
	withCapturedStdout(t, func() {
		require.NoError(t, RunAdminCaptchaSet(fakeCmdCtx{}, append([]string{"--hub", url}, args...)))
	})
	writes := hub.takeUpdatesMany()
	require.Len(t, writes, 1, "every key travels in ONE UpdateSettings")
	return writeFor(t, writes[0], key)
}

// envelopeData decodes the `data` half of a JSON envelope.
func envelopeData(t *testing.T, out []byte) map[string]any {
	t.Helper()
	var env struct {
		Data  map[string]any    `json:"data"`
		Error map[string]string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env), "stdout: %s", out)
	require.Empty(t, env.Error, "the verb reported an error")
	return env.Data
}

// envelopeError decodes the `error` half of a JSON envelope. The sibling of
// envelopeData: a refused verb states a stable CODE that an operator's
// script branches on, so a test reads that field rather than searching the
// whole envelope for the text.
func envelopeError(t *testing.T, out []byte) map[string]string {
	t.Helper()
	var env struct {
		Error map[string]string `json:"error"`
	}
	// Decoding the WHOLE buffer also pins that exactly one envelope was
	// written: a second concatenated object fails here.
	require.NoError(t, json.Unmarshal(out, &env), "stdout: %s", out)
	require.NotEmpty(t, env.Error, "the verb reported no error")
	return env.Error
}

// writeFor returns the partial document that req carries for key.
func writeFor(t *testing.T, req *leapmuxv1.UpdateSettingsRequest, key string) map[string]any {
	t.Helper()
	for _, w := range req.GetWrites() {
		if w.GetKey() != key {
			continue
		}
		var doc map[string]any
		require.NoError(t, json.Unmarshal([]byte(w.GetPartialJson()), &doc))
		return doc
	}
	t.Fatalf("no write for %q", key)
	return nil
}

// The write's own reply states the propagation class, so `settings set`
// must not ask for it again. Reading it back through a second ListSettings
// serialized every registered key to learn one boolean.
func TestAdminSettingsSet_ReportsThePropagationWithoutASecondRead(t *testing.T) {
	hub := &fakeAdminHub{}
	url := startAdminHub(t, hub)

	out := withCapturedStdout(t, func() {
		require.NoError(t, RunAdminSettingsSet(fakeCmdCtx{}, []string{"--hub", url, "theme", `"dark"`}))
	})

	data := envelopeData(t, out)
	assert.Equal(t, "hot", data["propagation"])
	require.Contains(t, data, "note_hot", "a hot key must say when the running hub applies the value")
	assert.NotContains(t, data, "note_restart")
	assert.Contains(t, data["note_hot"], "at once")
	assert.Contains(t, data["note_hot"], "30 seconds",
		"the other instances that share the database lag by the snapshot TTL")

	assert.Len(t, hub.takeUpdates(), 1)
	assert.Zero(t, hub.listCallCount(), "the propagation class rides the write's reply, so no listing is needed")
}

func TestAdminSettingsSet_ReportsARestartClassKey(t *testing.T) {
	hub := &fakeAdminHub{restart: true}
	url := startAdminHub(t, hub)

	out := withCapturedStdout(t, func() {
		require.NoError(t, RunAdminSettingsSet(fakeCmdCtx{}, []string{"--hub", url, "listen_addr", `":8080"`}))
	})

	data := envelopeData(t, out)
	assert.Equal(t, "restart", data["propagation"])
	assert.Contains(t, data, "note_restart")
	assert.NotContains(t, data, "note_hot", "a restart-class key must not claim the value is already live")
}

// `settings list` and `settings get` must point at the verb that can
// actually write a key the cross-key rules tie to another key. Printing the
// hub's summary verbatim sent an operator to `settings set captcha.selected`,
// which the hub refuses until the provider's row is complete -- and only
// `captcha set` writes both halves in one transaction.
func TestAdminSettingsList_DescribesAKeyWithItsDomainVerb(t *testing.T) {
	hub := &fakeAdminHub{
		values: []*leapmuxv1.SettingValue{
			settingValue(captcha.CaptchaSelectedKey.Name(), `"altcha"`, nil),
			settingValue("public_url", `"https://hub.example"`, nil),
		},
		descriptors: []*leapmuxv1.SettingDescriptor{
			{Key: captcha.CaptchaSelectedKey.Name(), Summary: "the active captcha provider alias"},
			{Key: "public_url", Summary: "the public origin"},
		},
	}
	url := startAdminHub(t, hub)

	out := withCapturedStdout(t, func() {
		require.NoError(t, RunAdminSettingsList(fakeCmdCtx{}, []string{"--hub", url}))
	})

	var env struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	require.Len(t, env.Data, 2)
	assert.Equal(t, "the active captcha provider alias (prefer `leapmux control admin captcha ...`)",
		env.Data[0]["description"])
	assert.Equal(t, "the public origin", env.Data[1]["description"],
		"a key with no domain verb keeps the hub's own summary")
}

// `captcha set` must refuse a provider whose stored row lacks a half that
// this invocation does not supply. The hub refuses the same state through
// its cross-key rule, but it answers with the settings KEY rather than with
// the flags to add, and only after the write has travelled.
func TestAdminCaptchaSet_RefusesAProviderWithNoStoredCredentials(t *testing.T) {
	hub := &fakeAdminHub{values: captchaHubValues("altcha", `{"algorithm":"PBKDF2/SHA-256"}`,
		settingValue(captcha.TurnstileKey.Name(), "{}", nil))}
	url := startAdminHub(t, hub)

	out := withCapturedStdout(t, func() {
		err := RunAdminCaptchaSet(fakeCmdCtx{}, []string{"--hub", url, "--provider", "turnstile"})
		require.Error(t, err)
		assert.True(t, control.IsEmitted(err))
	})

	assert.Contains(t, string(out), "turnstile has no stored site key and secret")
	assert.Contains(t, string(out), "--site-key and --secret")
	assert.Empty(t, hub.takeUpdatesMany(), "the refusal must come before the write, not from the hub")
}

// The refusal names only the half that is MISSING: a stored site key beside
// a passed secret is a complete row.
func TestAdminCaptchaSet_NamesOnlyTheMissingHalf(t *testing.T) {
	hub := &fakeAdminHub{values: captchaHubValues("altcha", "{}",
		settingValue(captcha.TurnstileKey.Name(), `{"site_key":"stored"}`, nil))}
	url := startAdminHub(t, hub)

	out := withCapturedStdout(t, func() {
		err := RunAdminCaptchaSet(fakeCmdCtx{}, []string{"--hub", url, "--provider", "turnstile"})
		require.Error(t, err)
	})
	assert.Contains(t, string(out), "turnstile has no stored secret; pass --secret")
	assert.NotContains(t, string(out), "site key", "the stored half must not appear in the refusal")
}

// The other half of the rule: a provider whose row is complete activates,
// so an operator can select an already-configured provider with no flags.
func TestAdminCaptchaSet_ActivatesAProviderWhoseRowIsComplete(t *testing.T) {
	hub := &fakeAdminHub{values: captchaHubValues("altcha", "{}",
		settingValue(captcha.TurnstileKey.Name(), `{"site_key":"stored"}`, map[string]bool{"secret_key": true}))}
	url := startAdminHub(t, hub)

	out := withCapturedStdout(t, func() {
		require.NoError(t, RunAdminCaptchaSet(fakeCmdCtx{}, []string{"--hub", url, "--provider", "turnstile"}))
	})

	data := envelopeData(t, out)
	assert.Equal(t, "turnstile", data["provider"])
	assert.Equal(t, true, data["activated"])
	require.Len(t, hub.takeUpdatesMany(), 1, "every key travels in ONE UpdateSettings")
}

// readCaptchaState must ask whether ANY secret field is stored, because
// each provider names its own: ALTCHA signs with `hmac_key` and the
// external providers verify with `secret_key`. Reading one hardcoded name
// reported ALTCHA as unconfigured whatever it held.
func TestReadCaptchaState_ReportsEachProvidersOwnSecretField(t *testing.T) {
	hub := &fakeAdminHub{values: captchaHubValues("altcha", `{"algorithm":"SCRYPT"}`,
		settingValue(captcha.TurnstileKey.Name(), `{"site_key":"k"}`, map[string]bool{"secret_key": true}))}
	url := startAdminHub(t, hub)

	c, err := control.NewClientOrAnonymous(url)
	require.NoError(t, err)
	state, err := readCaptchaState(c)
	require.NoError(t, err)

	assert.True(t, state.secretSet[captcha.AltchaKey.Name()], "ALTCHA stores its signing key as hmac_key")
	assert.True(t, state.secretSet[captcha.TurnstileKey.Name()], "Turnstile stores its API secret as secret_key")
	assert.False(t, state.secretSet[captcha.RecaptchaV3Key.Name()], "an empty row holds no secret")
	assert.True(t, state.siteKeySet[captcha.TurnstileKey.Name()])
	assert.Equal(t, "SCRYPT", state.altchaAlgorithm, "the stored family decides what a passed 0 restores")
	assert.Equal(t, captcha.ProviderAltcha, state.selected)
}

// A tuning flag passed as 0 restores the default of the family in place,
// not the literal 0 — which the key's own range check refuses with a
// message about the value rather than about the flag.
func TestAdminCaptchaSet_SubstitutesTheFamilyDefaultForAPassedZero(t *testing.T) {
	hub := &fakeAdminHub{values: captchaHubValues("altcha", `{"algorithm":"SCRYPT"}`)}
	url := startAdminHub(t, hub)

	doc := captchaSetDoc(t, hub, url, captcha.AltchaKey.Name(), "--cost", "0", "--parallelism", "0")

	family, err := captcha.DefaultAltchaSettingsFor("SCRYPT")
	require.NoError(t, err)
	assert.EqualValues(t, family.Cost, doc["cost"], "SCRYPT's own default cost, not 0")
	assert.EqualValues(t, family.Parallelism, doc["parallelism"])
}

// The family comes from --algorithm when the same invocation passes it, so
// `--algorithm ARGON2ID --memory-cost 0` restores ARGON2ID's memory cost
// and not the stored family's.
func TestAdminCaptchaSet_TakesTheFamilyFromThePassedAlgorithm(t *testing.T) {
	hub := &fakeAdminHub{values: captchaHubValues("altcha", `{"algorithm":"SCRYPT"}`)}
	url := startAdminHub(t, hub)

	doc := captchaSetDoc(t, hub, url, captcha.AltchaKey.Name(), "--algorithm", "ARGON2ID", "--memory-cost", "0")

	family, err := captcha.DefaultAltchaSettingsFor("ARGON2ID")
	require.NoError(t, err)
	assert.EqualValues(t, family.MemoryCost, doc["memory_cost"])
}

// A non-zero value is stored verbatim, or the substitution would overwrite
// every tuning an operator makes.
func TestAdminCaptchaSet_KeepsANonZeroTuningValue(t *testing.T) {
	hub := &fakeAdminHub{values: captchaHubValues("altcha", `{"algorithm":"SCRYPT"}`)}
	url := startAdminHub(t, hub)

	doc := captchaSetDoc(t, hub, url, captcha.AltchaKey.Name(), "--cost", "4096")
	assert.EqualValues(t, 4096, doc["cost"])
}

// --expires differs from its ALTCHA siblings: it restores a FIXED default
// (DefaultAltchaSettings) rather than the default of the family the
// invocation leaves in place. That is only correct while no family sets an
// expiry of its own, so the loop states that condition. A family that ever
// does makes the fixed lookup in RunAdminCaptchaSet wrong, and this fails
// then rather than storing another family's expiry in silence.
//
// The substitution itself is not decoration: the key refuses anything
// outside 60..86400 seconds, so a stored 0 comes back as a complaint about
// the value rather than about the flag.
func TestAdminCaptchaSet_ExpiresPassedAsZeroRestoresTheFixedDefault(t *testing.T) {
	fixed := captcha.DefaultAltchaSettings().ChallengeExpirySeconds
	for _, algorithm := range captcha.SupportedAltchaAlgorithms() {
		family, err := captcha.DefaultAltchaSettingsFor(algorithm)
		require.NoError(t, err)
		require.EqualValuesf(t, fixed, family.ChallengeExpirySeconds,
			"%s carries its own challenge expiry, so --expires 0 must take the FAMILY default", algorithm)
	}

	hub := &fakeAdminHub{values: captchaHubValues("altcha", `{"algorithm":"SCRYPT"}`)}
	url := startAdminHub(t, hub)

	doc := captchaSetDoc(t, hub, url, captcha.AltchaKey.Name(), "--expires", "0")
	assert.EqualValues(t, fixed, doc["challenge_expiry_seconds"], "0 restores the default; it is never stored")
}

// The other half: a non-zero expiry is stored verbatim, or the
// substitution would overwrite every expiry an operator sets.
func TestAdminCaptchaSet_KeepsANonZeroExpires(t *testing.T) {
	require.NotEqualValues(t, 3600, captcha.DefaultAltchaSettings().ChallengeExpirySeconds,
		"the value under test must differ from the default, or a substitution would pass unseen")

	hub := &fakeAdminHub{values: captchaHubValues("altcha", `{"algorithm":"SCRYPT"}`)}
	url := startAdminHub(t, hub)

	doc := captchaSetDoc(t, hub, url, captcha.AltchaKey.Name(), "--expires", "3600")
	assert.EqualValues(t, 3600, doc["challenge_expiry_seconds"])
}

// --min-score is the recaptcha_v3 twin of --expires: a passed 0 restores
// the FIXED default, because recaptcha_v3 has no families to take one
// from. The key refuses a score outside (0, 1], so a stored 0 refuses the
// write with a complaint about the value.
//
// The stored row holds both credentials, so this invocation tunes the
// score alone and the completeness check has nothing to refuse.
func TestAdminCaptchaSet_MinScorePassedAsZeroRestoresTheFixedDefault(t *testing.T) {
	hub := &fakeAdminHub{values: captchaHubValues("altcha", "{}",
		settingValue(captcha.RecaptchaV3Key.Name(), `{"site_key":"stored"}`, map[string]bool{"secret_key": true}))}
	url := startAdminHub(t, hub)

	doc := captchaSetDoc(t, hub, url, captcha.RecaptchaV3Key.Name(),
		"--provider", "recaptcha_v3", "--min-score", "0")
	assert.EqualValues(t, captcha.DefaultRecaptchaV3Settings().MinScore, doc["min_score"],
		"0 restores Google's documented default; it is never stored")
}

// A non-zero score is stored verbatim. 0.7 is the threshold an operator
// raises to under attack, and a substitution here would silently return
// the site to the default.
func TestAdminCaptchaSet_KeepsANonZeroMinScore(t *testing.T) {
	require.NotEqualValues(t, 0.7, captcha.DefaultRecaptchaV3Settings().MinScore,
		"the value under test must differ from the default, or a substitution would pass unseen")

	hub := &fakeAdminHub{values: captchaHubValues("altcha", "{}",
		settingValue(captcha.RecaptchaV3Key.Name(), `{"site_key":"stored"}`, map[string]bool{"secret_key": true}))}
	url := startAdminHub(t, hub)

	doc := captchaSetDoc(t, hub, url, captcha.RecaptchaV3Key.Name(),
		"--provider", "recaptcha_v3", "--min-score", "0.7")
	assert.EqualValues(t, 0.7, doc["min_score"])
}

// The provider-foreign refusal covers the HUB-RESOLVED target too, not only
// an explicit --provider. Enforcing it in one of two mutually exclusive
// branches left the rule with no single statement.
func TestAdminCaptchaSet_RefusesAForeignFlagAgainstTheResolvedTarget(t *testing.T) {
	hub := &fakeAdminHub{values: captchaHubValues("turnstile", "{}",
		settingValue(captcha.TurnstileKey.Name(), `{"site_key":"k"}`, map[string]bool{"secret_key": true}))}
	url := startAdminHub(t, hub)

	out := withCapturedStdout(t, func() {
		err := RunAdminCaptchaSet(fakeCmdCtx{}, []string{"--hub", url, "--cost", "5"})
		require.Error(t, err)
	})

	assert.Contains(t, string(out), "--cost applies only to altcha")
	assert.Contains(t, string(out), "the target provider is turnstile")
	assert.Empty(t, hub.takeUpdatesMany(), "a foreign flag must reach no key at all")
}

// `captcha reset` clears every key in ONE transaction. The loop it replaced
// destroyed the selection and two provider rows before a refusal on the
// next key answered that it had reset nothing.
func TestAdminCaptchaReset_ClearsEveryKeyInOneCall(t *testing.T) {
	hub := &fakeAdminHub{}
	url := startAdminHub(t, hub)

	out := withCapturedStdout(t, func() {
		require.NoError(t, RunAdminCaptchaReset(fakeCmdCtx{}, []string{"--hub", url}))
	})

	data := envelopeData(t, out)
	assert.ElementsMatch(t, captchaSettingKeys(), data["reset"])

	resets := hub.takeResets()
	require.Len(t, resets, 1, "one request, so no key order can leave an illegal intermediate state")
	assert.ElementsMatch(t, captchaSettingKeys(), resets[0])
	assert.Zero(t, hub.listCallCount(), "resetting every key needs no pre-read")
}

// Resetting the SELECTED external provider's row alone would leave a
// selection whose row holds no keys, so the selection joins the same
// request. A provider that is not selected takes its row alone.
func TestAdminCaptchaReset_OneProviderCarriesTheSelectionWhenItIsSelected(t *testing.T) {
	hub := &fakeAdminHub{values: captchaHubValues("turnstile", "{}",
		settingValue(captcha.TurnstileKey.Name(), `{"site_key":"k"}`, map[string]bool{"secret_key": true}))}
	url := startAdminHub(t, hub)

	withCapturedStdout(t, func() {
		require.NoError(t, RunAdminCaptchaReset(fakeCmdCtx{}, []string{"--hub", url, "--provider", "turnstile"}))
	})
	resets := hub.takeResets()
	require.Len(t, resets, 1)
	assert.ElementsMatch(t,
		[]string{captcha.CaptchaSelectedKey.Name(), captcha.TurnstileKey.Name()}, resets[0])

	withCapturedStdout(t, func() {
		require.NoError(t, RunAdminCaptchaReset(fakeCmdCtx{}, []string{"--hub", url, "--provider", "recaptcha_v3"}))
	})
	resets = hub.takeResets()
	require.Len(t, resets, 1)
	assert.Equal(t, []string{captcha.RecaptchaV3Key.Name()}, resets[0],
		"an unselected provider's row cannot make the selection illegal")
}

// A refused reset must leave nothing behind. One request means the hub
// either clears the whole set or none of it, and the CLI must not fall back
// to clearing the keys one at a time.
func TestAdminCaptchaReset_ARefusalClearsNothing(t *testing.T) {
	hub := &fakeAdminHub{resetErr: errors.New("cross rule refused")}
	url := startAdminHub(t, hub)

	out := withCapturedStdout(t, func() {
		err := RunAdminCaptchaReset(fakeCmdCtx{}, []string{"--hub", url})
		require.Error(t, err)
		assert.True(t, control.IsEmitted(err))
	})

	envErr := envelopeError(t, out)
	assert.Equal(t, "reset_failed", envErr["code"])
	assert.Contains(t, envErr["message"], "cross rule refused")
	assert.Len(t, hub.takeResets(), 1, "a refusal must not fall back to a per-key sequence")
}

// A field passed as 0 restores the operation's catalogue default, the same
// escape every captcha tuning flag gives.
func TestAdminRateLimitSet_SubstitutesTheCatalogueDefaultForAPassedZero(t *testing.T) {
	hub := &fakeAdminHub{}
	url := startAdminHub(t, hub)

	withCapturedStdout(t, func() {
		require.NoError(t, RunAdminRateLimitSet(fakeCmdCtx{},
			[]string{"--hub", url, "--operation", "elevation", "--max-attempts", "0", "--window", "0"}))
	})

	def, ok := ratelimit.DefaultLimits(ratelimit.OpElevation)
	require.True(t, ok)
	updates := hub.takeUpdates()
	require.Len(t, updates, 1)
	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(updates[0].GetPartialJson()), &doc))
	assert.EqualValues(t, def.MaxAttempts, doc["max_attempts"])
	assert.EqualValues(t, def.WindowSeconds, doc["window_seconds"])
	assert.NotContains(t, doc, "enabled", "`rate-limit enable|disable` owns the switch")
}

// Removing your OWN administrator access needs --force, and the hub can
// enforce that only when the flag reaches the request.
func TestAdminUserSetAdmin_CarriesForceToTheHub(t *testing.T) {
	hub := &fakeAdminHub{}
	url := startAdminHub(t, hub)

	withCapturedStdout(t, func() {
		require.NoError(t, RunAdminUserSetAdmin(fakeCmdCtx{},
			[]string{"--hub", url, "--username", "amy", "--force"}, false))
		require.NoError(t, RunAdminUserSetAdmin(fakeCmdCtx{},
			[]string{"--hub", url, "--username", "amy"}, false))
	})

	calls := hub.takeSetAdmin()
	require.Len(t, calls, 2)
	assert.True(t, calls[0].GetForce())
	assert.False(t, calls[1].GetForce(), "the flag is not set by default")
}

// `user reset-password` prints four fields, and the reply is the ONLY
// source for three of them. The CLI holds just the handle the operator
// typed, so the other handle comes back from the hub; and the two revoked
// counts exist nowhere on this side at all. A reset fences every bearer
// token the account holds, so an operator who cannot see those counts has
// no stated cause for the integrations that stop working afterwards.
func TestAdminUserResetPassword_ReportsTheSubjectAndTheRevokedCounts(t *testing.T) {
	hub := &fakeAdminHub{}
	url := startAdminHub(t, hub)

	out := withCapturedStdout(t, func() {
		require.NoError(t, RunAdminUserResetPassword(fakeCmdCtx{},
			[]string{"--hub", url, "--id", "u-7", "--password", "s3cr3t"}))
	})

	data := envelopeData(t, out)
	assert.Equal(t, "u-7", data["user_id"])
	assert.Equal(t, "amy", data["username"],
		"the username comes back from the hub; a caller that addressed the user by id never had it")
	assert.EqualValues(t, 4, data["api_tokens_revoked"])
	assert.EqualValues(t, 2, data["delegation_tokens_revoked"])

	// The request half: the selector and the new password must travel, or
	// the hub resets nothing and the counts above describe no work.
	calls := hub.takeResetPasswords()
	require.Len(t, calls, 1)
	assert.Equal(t, "u-7", calls[0].GetId())
	assert.Empty(t, calls[0].GetUsername(), "the operator addressed the user by id")
	assert.Equal(t, "s3cr3t", calls[0].GetPassword())
}

// The mirror case over the other handle. Addressing the user by name is
// what an operator does at a support desk, and the user id in the reply is
// the handle every following verb takes.
func TestAdminUserResetPassword_ReportsTheUserIDWhenAddressedByUsername(t *testing.T) {
	hub := &fakeAdminHub{}
	url := startAdminHub(t, hub)

	out := withCapturedStdout(t, func() {
		require.NoError(t, RunAdminUserResetPassword(fakeCmdCtx{},
			[]string{"--hub", url, "--username", "amy", "--password", "s3cr3t"}))
	})

	data := envelopeData(t, out)
	assert.Equal(t, "u-7", data["user_id"],
		"the user id comes back from the hub; a caller that addressed the user by name never had it")
	assert.Equal(t, "amy", data["username"])

	calls := hub.takeResetPasswords()
	require.Len(t, calls, 1)
	assert.Equal(t, "amy", calls[0].GetUsername())
	assert.Empty(t, calls[0].GetId())
}

// A hub that refuses the reset must reach the operator under this verb's
// own code. `reset_failed` separates it from `rpc_failed`, which says the
// call never landed -- and the two demand different next steps.
func TestAdminUserResetPassword_ARefusalRendersTheResetFailedCode(t *testing.T) {
	hub := &fakeAdminHub{resetPasswordErr: errors.New("password does not meet the policy")}
	url := startAdminHub(t, hub)

	out := withCapturedStdout(t, func() {
		err := RunAdminUserResetPassword(fakeCmdCtx{},
			[]string{"--hub", url, "--id", "u-7", "--password", "short"})
		require.Error(t, err)
		assert.True(t, control.IsEmitted(err))
	})

	envErr := envelopeError(t, out)
	assert.Equal(t, "reset_failed", envErr["code"])
	assert.Contains(t, envErr["message"], "password does not meet the policy",
		"the hub's reason is the actionable half; the CLI only frames it")
	require.Len(t, hub.takeResetPasswords(), 1, "the refusal came from the hub, so the call travelled")
}

// The pre-dial half of this verb -- the selector check that must answer
// before the password prompt -- is pinned by
// TestAdminUserResetPassword_RefusesAMissingSelectorBeforeItPrompts, which
// needs no hub because BeforeDial runs before the dial.

// Removing a provider that is the last login method of an account needs
// --force, and the hub can enforce that only when the flag reaches the
// request. Without the flag the operator has no way past the refusal
// except the offline `leapmux recover`.
func TestAdminOAuthProviderRemove_CarriesForceToTheHub(t *testing.T) {
	hub := &fakeAdminHub{}
	url := startAdminHub(t, hub)

	withCapturedStdout(t, func() {
		require.NoError(t, RunAdminOAuthProviderRemove(fakeCmdCtx{},
			[]string{"--hub", url, "--id", "prov-1", "--force"}))
		require.NoError(t, RunAdminOAuthProviderRemove(fakeCmdCtx{},
			[]string{"--hub", url, "--id", "prov-1"}))
	})

	calls := hub.takeRemovedProviders()
	require.Len(t, calls, 2)
	assert.Equal(t, "prov-1", calls[0].GetId())
	assert.True(t, calls[0].GetForce())
	assert.False(t, calls[1].GetForce(), "the flag is not set by default")
}

// A forced removal locks accounts out, and the reply's count is the only
// report of how many. The verb must print it.
func TestAdminOAuthProviderRemove_ReportsTheLockedOutCount(t *testing.T) {
	hub := &fakeAdminHub{lockedOutUsers: 3}
	url := startAdminHub(t, hub)

	out := withCapturedStdout(t, func() {
		require.NoError(t, RunAdminOAuthProviderRemove(fakeCmdCtx{},
			[]string{"--hub", url, "--id", "prov-1", "--force"}))
	})

	data := envelopeData(t, out)
	assert.Equal(t, "prov-1", data["removed"])
	assert.EqualValues(t, 3, data["locked_out_users"])
	require.Len(t, hub.takeRemovedProviders(), 1)
}

// A provider that nobody depends on reports zero, and the field must
// still be there: an absent key reads as "this verb does not report it",
// which is what sends an operator looking for the count somewhere else.
func TestAdminOAuthProviderRemove_ReportsZeroWhenNobodyIsLockedOut(t *testing.T) {
	hub := &fakeAdminHub{}
	url := startAdminHub(t, hub)

	out := withCapturedStdout(t, func() {
		require.NoError(t, RunAdminOAuthProviderRemove(fakeCmdCtx{},
			[]string{"--hub", url, "--id", "prov-1"}))
	})

	data := envelopeData(t, out)
	require.Contains(t, data, "locked_out_users")
	assert.EqualValues(t, 0, data["locked_out_users"])
}

// `api-token issue` takes the SAME (user-id | username) selector as every
// other user-addressing verb, so the one verb that MINTS a credential lets
// an operator name the owner the way the rest of the surface does.
func TestAdminAPITokenIssue_AddressesTheOwnerByUsername(t *testing.T) {
	hub := &fakeAdminHub{}
	url := startAdminHub(t, hub)

	withCapturedStdout(t, func() {
		require.NoError(t, RunAdminAPITokenIssue(fakeCmdCtx{},
			[]string{"--hub", url, "--username", "amy", "--client-name", "ci-bot"}))
	})

	issued := hub.takeIssuedTokens()
	require.Len(t, issued, 1)
	assert.Equal(t, "amy", issued[0].GetUsername())
	assert.Empty(t, issued[0].GetUserId())
	assert.Equal(t, "ci-bot", issued[0].GetClientName())
}

// An admin verb never touches a TOFU pin, so a pins.json that cannot be
// parsed must not refuse one. Opening the store in the constructor made a
// corrupt file break every verb, and report it under the not_logged_in
// code, which names neither the file nor the cause.
func TestAdminVerb_RunsWithACorruptPinsFile(t *testing.T) {
	hub := &fakeAdminHub{}
	url := startAdminHub(t, hub)

	dir, err := control.ConfigDir()
	require.NoError(t, err)
	host, err := control.HubHost(url)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, host), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, host, "pins.json"), []byte("{ not json"), 0o644))

	withCapturedStdout(t, func() {
		require.NoError(t, RunAdminSettingsList(fakeCmdCtx{}, []string{"--hub", url}))
	})
	assert.Equal(t, 1, hub.listCallCount(), "the verb reached the hub")
}

// `settings set KEY VALUE --help` must print the positional form. The flag
// package knows only the flags, so the help of a leaf that REQUIRES two
// positionals said `[flags]` and nothing more; only a wrong count answered
// with the form.
func TestAdminPositionalLeaves_AllPrintTheirFormInHelp(t *testing.T) {
	leaves := map[string]func(any, []string) error{
		"settings get":        RunAdminSettingsGet,
		"settings set":        RunAdminSettingsSet,
		"settings set-secret": RunAdminSettingsSetSecret,
		"settings reset":      RunAdminSettingsReset,
	}
	for name, run := range leaves {
		t.Run(name, func(t *testing.T) {
			out := captureOSStdout(t, func() {
				err := run(fakeCmdCtx{}, []string{"--help"})
				require.ErrorIs(t, err, flag.ErrHelp)
			})
			assert.Contains(t, out, "usage: leapmux control admin "+name+" ",
				"--help must name the positional arguments the leaf requires")
			assert.Contains(t, out, "-hub", "and the flags are still printed")
		})
	}
}

// captureOSStdout captures what fn writes to the process's stdout.
//
// The help path writes THERE, not to control.Out: internalconfig.HasHelpArg
// redirects the flag set to os.Stdout so `--help` reaches a terminal even
// when the caller redirected the JSON envelope.
func captureOSStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	prev := os.Stdout
	os.Stdout = w
	read := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		read <- string(b)
	}()
	fn()
	os.Stdout = prev
	require.NoError(t, w.Close())
	out := <-read
	require.NoError(t, r.Close())
	return out
}
