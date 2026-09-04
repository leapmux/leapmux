package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/captcha"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/ratelimit"
	"github.com/leapmux/leapmux/internal/hub/requestsource"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/settingsregistry"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
)

// adminSettingsEnv mounts the real AdminSettingsService behind the real
// auth interceptor with an admin user (the harness shape of
// section_service_test, parameterized by cfg for solo/dev cases).
type adminSettingsEnv struct {
	client leapmuxv1connect.AdminSettingsServiceClient
	st     store.Store
	token  string
	// adminID is the account behind `token`, so a test that needs a second
	// session (or that wants to re-elevate one) does not have to look it up.
	adminID string
	// svc is the mounted service, for the one test that has to move its clock:
	// the elevation cap is measured from the stored anchor, and a case about
	// the ceiling cannot wait seven hours for the wall clock to reach it.
	svc *service.AdminSettingsService
	// set is the settings manager the service writes through, so a test can
	// read a written value back at its source rather than through the
	// listing RPC.
	set *settings.Manager
	// userClient mounts AdminUserService on the same mux, for the ONE thing
	// the settings tests need from it: minting the admin-scoped bearer that
	// `leapmux control admin settings ...` authenticates with. That bearer
	// takes the same elevation rule a session does, so that path needs a real
	// credential rather than a hand-written api_tokens row.
	userClient leapmuxv1connect.AdminUserServiceClient
}

// adminBearer mints an admin-scoped command-line credential for this
// environment's administrator, through the RPC the CLI itself uses. It
// returns the token id beside the secret, because a credential now carries an
// elevation window and a test that needs one has to identify the row.
func (e *adminSettingsEnv) adminBearer(t *testing.T) (bearer, tokenID string) {
	t.Helper()
	issued, err := e.userClient.IssueAPIToken(context.Background(), authedReq(&leapmuxv1.IssueAPITokenRequest{
		UserId: e.adminID, InstallationName: "admin-cli", Scopes: []string{"admin:read", "admin:users", "admin:settings", "admin:workers"},
	}, e.token))
	require.NoError(t, err)
	return issued.Msg.GetAccessToken(), issued.Msg.GetTokenId()
}

// elevatedAdminBearer is the same credential with a proven factor on it, which
// is what every restricted write now needs from a command-line caller.
func (e *adminSettingsEnv) elevatedAdminBearer(t *testing.T) string {
	t.Helper()
	bearer, tokenID := e.adminBearer(t)
	hubtestutil.ElevateAPIToken(t, e.st, tokenID, e.adminID)
	return bearer
}

func setupAdminSettingsTest(t *testing.T, cfg *config.Config) *adminSettingsEnv {
	return setupAdminSettingsEnv(t, cfg)
}

// setupAdminSettingsTestUnelevated is the same harness with the session left
// un-elevated, for the tests that assert the write gate itself.
func setupAdminSettingsTestUnelevated(t *testing.T, cfg *config.Config) *adminSettingsEnv {
	return setupAdminSettingsEnvRaw(t, cfg)
}

// setupAdminSettingsTestWithProvisioner registers the ALTCHA post-reset step
// the hub registers, so the reset handlers run against the real mechanism.
func setupAdminSettingsTestWithProvisioner(t *testing.T, cfg *config.Config, provision func(context.Context) error) *adminSettingsEnv {
	return setupAdminSettingsEnv(t, cfg, settings.WithAfterReset(captcha.AltchaKey.Name(), provision))
}

// setupAdminSettingsTestWithQueueBudget registers the queue_budget read-time
// rule the hub registers from its resolved pool capacities.
func setupAdminSettingsTestWithQueueBudget(t *testing.T, cfg *config.Config, queueBudget func() settings.QueueBudgetValue) *adminSettingsEnv {
	return setupAdminSettingsEnv(t, cfg,
		settings.WithEffective(settings.KeyQueueBudget.Name(), func(*settings.Snapshot) (any, bool) {
			return queueBudget(), true
		}))
}

// setupAdminSettingsEnv is the DEFAULT fixture, and its session is elevated.
//
// Every write verb on this service requires an elevated session, because a
// hub setting is deployment-wide and several of these keys are the hub's own
// security controls. Almost every test here exercises what a verb DOES rather
// than whether the gate is there, so supplying the elevation is what keeps
// those tests about their own subject.
// setupAdminSettingsTestUnelevated is for the cases that assert the gate.
func setupAdminSettingsEnv(t *testing.T, cfg *config.Config, opts ...settings.Option) *adminSettingsEnv {
	t.Helper()
	env := setupAdminSettingsEnvRaw(t, cfg, opts...)
	hubtestutil.ElevateSession(t, env.st, env.token, env.adminID)
	return env
}

func setupAdminSettingsEnvRaw(t *testing.T, cfg *config.Config, opts ...settings.Option) *adminSettingsEnv {
	t.Helper()

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrator().Migrate(context.Background()))

	// An admin user, created through the production path, logged in for a
	// session cookie the interceptor accepts.
	hash, err := password.Hash("adminpass123")
	require.NoError(t, err)
	admin, err := service.CreateUser(context.Background(), st, service.CreateUserParams{
		Username:              "admin",
		PasswordHash:          hash,
		DisplayName:           "Admin",
		FirstCredentialExempt: true,
		IsAdmin:               true,
	})
	require.NoError(t, err)
	token, _, _, err := auth.Login(context.Background(), st, "admin", "adminpass123", auth.DefaultSessionDuration)
	require.NoError(t, err)

	tv, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)

	ks, err := keystore.LoadOrGenerate(filepath.Join(t.TempDir(), "encryption.key"))
	require.NoError(t, err)
	setMgr := settingsregistry.NewManager(st, ks, opts...)
	require.NoError(t, setMgr.Load(context.Background()))

	mux := http.NewServeMux()
	interceptor, contexts := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st, TokenValidator: tv})
	// The slide reporter rides beside the auth interceptor, exactly as the hub
	// mounts it. Every write verb here slides the elevation window, and the
	// deadline it produced reaches a client only through this rung -- so a
	// harness without it would pass whether the report worked or not.
	connectOpts := connect.WithInterceptors(interceptor, service.NewElevationSlideInterceptor())
	adminSvc := service.NewAdminSettingsService(setMgr, cfg, st)
	path, handler := leapmuxv1connect.NewAdminSettingsServiceHandler(adminSvc, connectOpts)
	mux.Handle(path, handler)
	userPath, userHandler := leapmuxv1connect.NewAdminUserServiceHandler(service.NewAdminUserService(service.AdminUserServiceDeps{
		Store:     st,
		Validator: tv,
		Lifecycle: auth.NewCredentialLifecycleEffects(contexts, nil, nil),
	}), connectOpts)
	mux.Handle(userPath, userHandler)

	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	return &adminSettingsEnv{
		client:     leapmuxv1connect.NewAdminSettingsServiceClient(server.Client(), server.URL, connect.WithGRPC()),
		st:         st,
		token:      token,
		adminID:    admin.ID,
		set:        setMgr,
		svc:        adminSvc,
		userClient: leapmuxv1connect.NewAdminUserServiceClient(server.Client(), server.URL, connect.WithGRPC()),
	}
}

func TestAdminSettingsService_ListDescriptorsComplete(t *testing.T) {
	env := setupAdminSettingsTest(t, &config.Config{})
	resp, err := env.client.ListSettings(context.Background(), authedReq(&leapmuxv1.ListSettingsRequest{}, env.token))
	require.NoError(t, err)

	keys := map[string]*leapmuxv1.SettingDescriptor{}
	for _, d := range resp.Msg.GetDescriptors() {
		keys[d.GetKey()] = d
		// Every descriptor declares its presentation and editable shape.
		assert.NotEmpty(t, d.GetCategory(), d.GetKey())
		assert.NotEmpty(t, d.GetTitle(), d.GetKey())
		assert.NotEmpty(t, d.GetFields(), d.GetKey())
	}
	// Every declaration domain is present.
	for _, want := range []string{"smtp", "signup_enabled", "captcha.altcha", "rate_limit.elevation", "max_message_size_bytes", "trusted_proxy_ranges"} {
		assert.Contains(t, keys, want)
	}

	// Restart-class keys report it (the dialog renders the warning badge).
	assert.True(t, keys["max_message_size_bytes"].GetRestart())
	assert.False(t, keys["signup_enabled"].GetRestart())

	// Secrets are declared write-only on the field schema.
	smtp := keys["smtp"]
	var passwordField *leapmuxv1.SettingField
	for _, f := range smtp.GetFields() {
		if f.GetName() == "password" {
			passwordField = f
		}
	}
	require.NotNil(t, passwordField, "smtp declares its password field")
	assert.True(t, passwordField.GetSecret())
}

func TestAdminSettingsService_ValuesDefaultAndCustomized(t *testing.T) {
	env := setupAdminSettingsTest(t, &config.Config{})
	ctx := context.Background()

	// Before any write: not customized, effective == default.
	resp, err := env.client.ListSettings(ctx, authedReq(&leapmuxv1.ListSettingsRequest{}, env.token))
	require.NoError(t, err)
	byKey := map[string]*leapmuxv1.SettingValue{}
	for _, v := range resp.Msg.GetValues() {
		byKey[v.GetKey()] = v
	}
	require.Contains(t, byKey, "session_duration_seconds")
	assert.False(t, byKey["session_duration_seconds"].GetCustomized())
	assert.JSONEq(t, "604800", byKey["session_duration_seconds"].GetEffectiveJson())

	require.Contains(t, byKey, "queue_budget")
	assert.False(t, byKey["queue_budget"].GetCustomized())
	assert.JSONEq(t, `{"relay_bytes":0,"worker_bytes":0,"userevents_bytes":0}`, byKey["queue_budget"].GetEffectiveJson())

	require.Contains(t, byKey, "trusted_proxy_ranges")
	assert.False(t, byKey["trusted_proxy_ranges"].GetCustomized())
	assert.JSONEq(t, `[]`, byKey["trusted_proxy_ranges"].GetEffectiveJson())

	// A partial update merges onto the current value.
	upd, err := env.client.UpdateSetting(ctx, authedReq(&leapmuxv1.UpdateSettingRequest{
		Key:         "session_duration_seconds",
		PartialJson: "3600",
	}, env.token))
	require.NoError(t, err)
	assert.True(t, upd.Msg.GetValue().GetCustomized())
	assert.JSONEq(t, "3600", upd.Msg.GetValue().GetEffectiveJson())
	assert.NotZero(t, upd.Msg.GetValue().GetUpdatedAt())

	// An invalid value is refused with nothing stored.
	_, err = env.client.UpdateSetting(ctx, authedReq(&leapmuxv1.UpdateSettingRequest{
		Key:         "session_duration_seconds",
		PartialJson: "1",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	// A stale field name is refused too (DisallowUnknownFields).
	_, err = env.client.UpdateSetting(ctx, authedReq(&leapmuxv1.UpdateSettingRequest{
		Key:         "smtp",
		PartialJson: `{"hast":"x"}`,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	// Reset returns the default.
	reset, err := env.client.ResetSetting(ctx, authedReq(&leapmuxv1.ResetSettingRequest{
		Key: "session_duration_seconds",
	}, env.token))
	require.NoError(t, err)
	assert.False(t, reset.Msg.GetValue().GetCustomized())
	assert.JSONEq(t, "604800", reset.Msg.GetValue().GetEffectiveJson())
}

func TestAdminSettingsService_RejectsOverlappingTrustedProxySelectors(t *testing.T) {
	env := setupAdminSettingsTest(t, &config.Config{})
	_, err := env.client.UpdateSetting(context.Background(), authedReq(&leapmuxv1.UpdateSettingRequest{
		Key:         requestsource.KeyTrustedProxyRanges.Name(),
		PartialJson: `["192.0.2.0/24","192.0.2.7"]`,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "overlaps")
}

// TestAdminSettingsService_SecretsNeverLeave covers all THREE documents the
// handler sends, because only two of them can carry a secret.
//
// value_json is the stored PUBLIC column verbatim, which the write path
// makes secret-free by construction: it splits the secret fields out before
// it writes that column. The assertion on it guards the split, and it cannot
// report a redaction fault.
//
// merged_json and effective_json are the two that can. Both are built from
// the DECRYPTED merged value, so each one carries the operator's password in
// the clear until desc.Redacted scrubs it. Deleting either Redacted call
// sends the secret to every admin client.
func TestAdminSettingsService_SecretsNeverLeave(t *testing.T) {
	env := setupAdminSettingsTest(t, &config.Config{})
	ctx := context.Background()

	// Write a secret through the secret verb.
	_, err := env.client.UpdateSettingSecret(ctx, authedReq(&leapmuxv1.UpdateSettingSecretRequest{
		Key:         "smtp",
		PartialJson: `{"password":"hunter2"}`,
	}, env.token))
	require.NoError(t, err)

	resp, err := env.client.ListSettings(ctx, authedReq(&leapmuxv1.ListSettingsRequest{}, env.token))
	require.NoError(t, err)
	var smtp *leapmuxv1.SettingValue
	for _, v := range resp.Msg.GetValues() {
		if v.GetKey() == "smtp" {
			smtp = v
		}
	}
	require.NotNil(t, smtp)
	assert.NotContains(t, smtp.GetValueJson(), "hunter2",
		"the stored public column is secret-free: the write path split the secret half out")

	// The two documents built from the decrypted value.
	assert.NotContains(t, smtp.GetMergedJson(), "hunter2", "a stored secret never leaves the hub")
	assert.NotContains(t, smtp.GetEffectiveJson(), "hunter2")
	var merged map[string]any
	require.NoError(t, json.Unmarshal([]byte(smtp.GetMergedJson()), &merged))
	assert.Equal(t, "<redacted>", merged["password"],
		"the merged document reports the secret as set, without its value")
	var effective map[string]any
	require.NoError(t, json.Unmarshal([]byte(smtp.GetEffectiveJson()), &effective))
	assert.Equal(t, "<redacted>", effective["password"])

	assert.Contains(t, smtp.GetSecretSet(), "password")
	assert.True(t, smtp.GetSecretSet()["password"], "secret_set reports the stored secret")

	// UpdateSettingSecret on a key with no secret fields is refused.
	_, err = env.client.UpdateSettingSecret(ctx, authedReq(&leapmuxv1.UpdateSettingSecretRequest{
		Key:         "signup_enabled",
		PartialJson: "true",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// TestAdminSettingsService_EffectiveDiffersUnderDevMode pins the handler's
// whole effective_json contract: it reports what the MANAGER says is in
// effect, and the three documents stay apart. The rule itself belongs to the
// key -- dev mode holding signup open until an operator stores a row is the
// canonical one -- so the handler holds no per-key knowledge and the test
// registers the rule exactly as the hub's wiring site does.
func TestAdminSettingsService_EffectiveDiffersUnderDevMode(t *testing.T) {
	// Dev mode runs with open signup as the read-time default, but the
	// stored row (customized) stays false — the exact case effective_json
	// exists for.
	cfg := &config.Config{DevMode: true}
	env := setupAdminSettingsEnv(t, cfg,
		settings.WithEffective(settings.KeySignupEnabled.Name(), func(s *settings.Snapshot) (any, bool) {
			return settings.SignupEnabledEffective(s, cfg.DevMode), true
		}))
	resp, err := env.client.ListSettings(context.Background(), authedReq(&leapmuxv1.ListSettingsRequest{}, env.token))
	require.NoError(t, err)
	var signup *leapmuxv1.SettingValue
	for _, v := range resp.Msg.GetValues() {
		if v.GetKey() == "signup_enabled" {
			signup = v
		}
	}
	require.NotNil(t, signup)
	assert.False(t, signup.GetCustomized())
	assert.Empty(t, signup.GetValueJson(), "no row is stored, so the stored document is absent")
	assert.JSONEq(t, "false", signup.GetMergedJson(), "the code default merged, with no row over it")
	assert.JSONEq(t, "true", signup.GetEffectiveJson(), "dev mode resolves signup open at read time")
}

func TestAdminSettingsService_EmptyAndUnknownKey(t *testing.T) {
	env := setupAdminSettingsTest(t, &config.Config{})
	ctx := context.Background()

	_, err := env.client.UpdateSetting(ctx, authedReq(&leapmuxv1.UpdateSettingRequest{
		PartialJson: "true",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "key is required")

	_, err = env.client.UpdateSetting(ctx, authedReq(&leapmuxv1.UpdateSettingRequest{
		Key:         "not_a_setting",
		PartialJson: "true",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "unknown setting key")

	_, err = env.client.ResetSetting(ctx, authedReq(&leapmuxv1.ResetSettingRequest{}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "key is required")

	_, err = env.client.UpdateSettingSecret(ctx, authedReq(&leapmuxv1.UpdateSettingSecretRequest{
		PartialJson: `{"password":"x"}`,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "key is required")
}

// listedKeysByCategory lists one mode's descriptors, grouped by the
// category the preferences dialog draws its sections from.
func listedKeysByCategory(t *testing.T, cfg *config.Config) map[string][]string {
	t.Helper()
	env := setupAdminSettingsTest(t, cfg)
	resp, err := env.client.ListSettings(context.Background(), authedReq(&leapmuxv1.ListSettingsRequest{}, env.token))
	require.NoError(t, err)
	out := map[string][]string{}
	for _, d := range resp.Msg.GetDescriptors() {
		out[d.GetCategory()] = append(out[d.GetCategory()], d.GetKey())
	}
	return out
}

// flattenKeys collects every key of a by-category listing into one set.
func flattenKeys(byCategory map[string][]string) map[string]bool {
	out := map[string]bool{}
	for _, keys := range byCategory {
		for _, k := range keys {
			out[k] = true
		}
	}
	return out
}

// This test asserts the hidden set EXACTLY, in both directions, against
// the same registry listed in hub mode. A one-directional test (each
// expected key is absent) passes just as well when solo hides half the
// surface, and hiding a key that solo still reads makes it
// unadministrable in the dialog AND in `leapmux control admin settings`.
func TestAdminSettingsService_SoloOmitsHiddenInSolo(t *testing.T) {
	hub := flattenKeys(listedKeysByCategory(t, &config.Config{}))
	solo := flattenKeys(listedKeysByCategory(t, &config.Config{SoloMode: true}))

	omitted := []string{}
	for key := range hub {
		if !solo[key] {
			omitted = append(omitted, key)
		}
	}
	sort.Strings(omitted)

	assert.Equal(t, []string{
		// Solo runs no captcha, whatever it serves: the sign-in form a
		// password-holding solo hub shows is guarded by the address-keyed
		// rate_limit.login_anonymous instead, which stays listed. No second
		// user means no per-user budget, and the mail-abuse limits are inert
		// for the same reason as smtp: no mail.
		"captcha.altcha", "captcha.enabled", "captcha.recaptcha_v3",
		"captcha.selected", "captcha.turnstile", "mail_limits",
		"rate_limit.elevation", "rate_limit.email_change",
		"signup_enabled",
		// Both senders are unreachable: solo refuses sign-up and email
		// change, and the solo user can never hold a verified address.
		"smtp",
	}, omitted)

	// The mirror. HiddenInHub is what makes this a second list rather than
	// an empty one: the filter subtracts in BOTH directions now, so "solo
	// lists nothing hub does not" stopped being true the moment a key existed
	// that only a single-user local hub administers.
	//
	// Stated as an exact list for the same reason the one above is: a key
	// that quietly stops appearing in one deployment is the failure this test
	// exists to catch, and a subset assertion would not see it.
	soloOnly := []string{}
	for key := range solo {
		if !hub[key] {
			soloOnly = append(soloOnly, key)
		}
	}
	sort.Strings(soloOnly)

	assert.Equal(t, []string{
		// `leapmux hub` and `leapmux dev` already bind every interface and
		// already authenticate every caller, so extra addresses would only
		// offer them a way to break a working deployment. -listen and a
		// reverse proxy are how a multi-user hub is published.
		"extra_listen_addresses",
	}, soloOnly)
	assert.Contains(t, hub, "trusted_proxy_ranges")
	assert.Contains(t, solo, "trusted_proxy_ranges")
}

// The section-level outcome the hiding exists to produce. Categories are
// what the dialog renders as sections, and a section disappears only when
// its every key hides.
//
// `general` survives WHOLE in solo. public_url is the banner URL and the
// worker_hub_url a remote worker dials; the other two are read by the sign-in
// a solo hub serves once its account holds a password -- the session's
// lifetime, and whether its cookie carries the __Host- prefix.
//
// `rate-limits` is the case that proves hiding is per KEY, and it arrived when
// the answer stopped being uniform. Elevation is keyed by USER and solo has
// one, so that key hides; the two anonymous limits are keyed by client ADDRESS
// on surfaces solo also serves, so they stay. A blanket rule for the section
// would take them out of the preferences dialog AND out of `leapmux control
// admin settings`, which is the whole reach an operator has.
func TestAdminSettingsService_SoloSectionsGeneralSurvivesWithPublicURL(t *testing.T) {
	solo := listedKeysByCategory(t, &config.Config{SoloMode: true})

	assert.Equal(t, []string{
		settings.KeyPublicURL.Name(),
		settings.KeySecureCookies.Name(),
		settings.KeySessionDurationSeconds.Name(),
	}, solo["general"], "every general key must stay administrable in solo")
	assert.Empty(t, solo["signup"], "the sign-up section must vanish in solo")
	assert.Empty(t, solo["email"], "the email section must vanish in solo")
	assert.Empty(t, solo["captcha"], "the bot-protection section must vanish in solo")
	assert.Equal(t, []string{
		ratelimit.SettingKeyPrefix + string(ratelimit.OpLoginAnonymous),
		ratelimit.SettingKeyPrefix + string(ratelimit.OpOAuthAnonymous),
	}, solo["rate-limits"],
		"solo keeps the two address-keyed limits and drops the per-user ones")

	// The sections that solo keeps whole. Without this, hiding every
	// remaining key would still satisfy the assertions above.
	assert.NotEmpty(t, solo["limits"])
	assert.NotEmpty(t, solo["advanced"])
}

// queue_budget stores 0 to mean "auto-size from the process memory
// limit", and the hub then runs on a real byte count. Reporting the
// stored 0 as the effective value told an admin the pool was zero-sized.
func TestAdminSettingsService_QueueBudgetEffectiveReportsResolvedBytes(t *testing.T) {
	resolved := settings.QueueBudgetValue{
		RelayBytes:      64 << 20,
		WorkerBytes:     32 << 20,
		UserEventsBytes: 16 << 20,
	}
	env := setupAdminSettingsTestWithQueueBudget(t, &config.Config{},
		func() settings.QueueBudgetValue { return resolved })

	resp, err := env.client.ListSettings(context.Background(), authedReq(&leapmuxv1.ListSettingsRequest{}, env.token))
	require.NoError(t, err)

	var value *leapmuxv1.SettingValue
	for _, v := range resp.Msg.GetValues() {
		if v.GetKey() == settings.KeyQueueBudget.Name() {
			value = v
		}
	}
	require.NotNil(t, value, "queue_budget must be listed")

	assert.Empty(t, value.GetValueJson(), "an unconfigured hub stores no row at all")

	// Merged: the code default, every field 0 (auto).
	var merged settings.QueueBudgetValue
	require.NoError(t, json.Unmarshal([]byte(value.GetMergedJson()), &merged))
	assert.Equal(t, settings.QueueBudgetValue{}, merged,
		"the merged default is auto (0) in every field")

	// Effective: what the process actually runs on.
	var effective settings.QueueBudgetValue
	require.NoError(t, json.Unmarshal([]byte(value.GetEffectiveJson()), &effective))
	assert.Equal(t, resolved, effective,
		"the effective value must be the resolved capacities, not the stored zeros")
}

// Without a resolver (a hub that never started its pools, and every test
// that does not exercise this) the stored document is the honest answer.
func TestAdminSettingsService_QueueBudgetEffectiveFallsBackToStored(t *testing.T) {
	env := setupAdminSettingsTest(t, &config.Config{})
	resp, err := env.client.ListSettings(context.Background(), authedReq(&leapmuxv1.ListSettingsRequest{}, env.token))
	require.NoError(t, err)
	for _, v := range resp.Msg.GetValues() {
		if v.GetKey() == settings.KeyQueueBudget.Name() {
			assert.JSONEq(t, v.GetMergedJson(), v.GetEffectiveJson())
			return
		}
	}
	t.Fatal("queue_budget must be listed")
}

// Resetting captcha.altcha removes the signing key every outstanding
// challenge carries. The hub re-provisions inside the reset, so the
// next unauthenticated login does not have to write settings from its own
// request handler.
func TestAdminSettingsService_ResetAltchaReprovisions(t *testing.T) {
	var calls int
	env := setupAdminSettingsTestWithProvisioner(t, &config.Config{},
		func(context.Context) error { calls++; return nil })

	_, err := env.client.ResetSetting(context.Background(),
		authedReq(&leapmuxv1.ResetSettingRequest{Key: captcha.AltchaKey.Name()}, env.token))
	require.NoError(t, err)
	assert.Equal(t, 1, calls, "resetting the ALTCHA key must re-provision its signing key")

	// Any other key leaves it alone.
	_, err = env.client.ResetSetting(context.Background(),
		authedReq(&leapmuxv1.ResetSettingRequest{Key: settings.KeySignupEnabled.Name()}, env.token))
	require.NoError(t, err)
	assert.Equal(t, 1, calls, "an unrelated reset must not re-provision")
}

// A provisioning failure must surface, not leave the caller believing the
// row was restored.
func TestAdminSettingsService_ResetAltchaReportsProvisionFailure(t *testing.T) {
	env := setupAdminSettingsTestWithProvisioner(t, &config.Config{},
		func(context.Context) error { return errors.New("keystore unavailable") })

	_, err := env.client.ResetSetting(context.Background(),
		authedReq(&leapmuxv1.ResetSettingRequest{Key: captcha.AltchaKey.Name()}, env.token))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the post-reset step failed",
		"the manager identifies the key whose step failed")
	assert.Contains(t, err.Error(), "keystore unavailable",
		"and the step's own cause stays readable")
}

// TestAdminSettingsService_ValueJsonIsTheStoredHalfOnly pins the three
// documents apart. value_json is the STORED row, verbatim; merged_json is
// that row merged onto the code default. Sending the merged document as
// value_json made every field of every key read as customized on a virgin
// hub, because the defaults are non-zero and Decode merges them in -- so
// the dialog offered a destructive "reset all of timeouts" on ten rows
// nobody touched.
func TestAdminSettingsService_ValueJsonIsTheStoredHalfOnly(t *testing.T) {
	env := setupAdminSettingsTest(t, &config.Config{})
	ctx := context.Background()

	virgin := settingValueByKey(t, env, settings.KeyTimeouts.Name())
	assert.Empty(t, virgin.GetValueJson(), "a virgin key stores no row, so it reports no stored document")
	assert.False(t, virgin.GetCustomized())
	var mergedDoc map[string]any
	require.NoError(t, json.Unmarshal([]byte(virgin.GetMergedJson()), &mergedDoc))
	assert.Contains(t, mergedDoc, "api_seconds", "the merged document always carries every field")

	// One field written: the stored document carries THAT FIELD ONLY, while
	// the merged one still carries all three.
	_, err := env.client.UpdateSetting(ctx, authedReq(&leapmuxv1.UpdateSettingRequest{
		Key: settings.KeyTimeouts.Name(), PartialJson: `{"api_seconds":11}`,
	}, env.token))
	require.NoError(t, err)

	written := settingValueByKey(t, env, settings.KeyTimeouts.Name())
	assert.True(t, written.GetCustomized())
	var storedDoc map[string]any
	require.NoError(t, json.Unmarshal([]byte(written.GetValueJson()), &storedDoc))
	assert.InDelta(t, 11.0, storedDoc["api_seconds"], 0, "the write reached the stored row")
	// Once a row exists the write path stores the whole merged document, so
	// the two agree for a key with no secret half. The difference the split
	// exists for is the VIRGIN row above, which is where the merged document
	// made every field read as customized.
	assert.JSONEq(t, written.GetMergedJson(), written.GetValueJson())
	require.NoError(t, json.Unmarshal([]byte(written.GetMergedJson()), &mergedDoc))
	assert.Len(t, mergedDoc, 3, "the merged document carries every field")
	assert.JSONEq(t, written.GetMergedJson(), written.GetEffectiveJson(),
		"timeouts has no read-time override, so merged and effective agree")
}

// settingValueByKey returns one listed setting value, failing the test
// when the key is absent.
func settingValueByKey(t *testing.T, env *adminSettingsEnv, key string) *leapmuxv1.SettingValue {
	t.Helper()
	resp, err := env.client.ListSettings(context.Background(), authedReq(&leapmuxv1.ListSettingsRequest{}, env.token))
	require.NoError(t, err)
	for _, v := range resp.Msg.GetValues() {
		if v.GetKey() == key {
			return v
		}
	}
	t.Fatalf("setting %q must be listed", key)
	return nil
}

// TestAdminSettingsService_SoloRefusesWritingAHiddenKey pins the invariant
// HiddenInSolo states: the flag takes a key out of the WHOLE administration
// surface. Only the listing enforced it, so eleven keys a solo operator
// could not read were keys that operator could still write -- and the
// admin CLI's `settings get` answered "unknown setting key" while
// `settings set` succeeded.
func TestAdminSettingsService_SoloRefusesWritingAHiddenKey(t *testing.T) {
	env := setupAdminSettingsTest(t, &config.Config{SoloMode: true})
	ctx := context.Background()
	hidden := settings.KeySignupEnabled.Name()

	resp, err := env.client.ListSettings(ctx, authedReq(&leapmuxv1.ListSettingsRequest{}, env.token))
	require.NoError(t, err)
	for _, v := range resp.Msg.GetValues() {
		require.NotEqual(t, hidden, v.GetKey(), "a solo hub must not list the key")
	}

	_, err = env.client.UpdateSetting(ctx, authedReq(&leapmuxv1.UpdateSettingRequest{
		Key: hidden, PartialJson: "true",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "not administrable in solo mode")

	_, err = env.client.ResetSetting(ctx, authedReq(&leapmuxv1.ResetSettingRequest{Key: hidden}, env.token))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not administrable in solo mode")

	// A key solo DOES read stays writable.
	_, err = env.client.UpdateSetting(ctx, authedReq(&leapmuxv1.UpdateSettingRequest{
		Key: settings.KeyPublicURL.Name(), PartialJson: `"https://hub.example.com"`,
	}, env.token))
	require.NoError(t, err)
}

// A solo hub whose account holds a password signs its network callers in with
// an ordinary session and an ordinary cookie: Login reads
// session_duration_seconds for the lifetime, and every cookie it writes reads
// secure_cookies for the __Host- prefix and the Secure attribute. Both keys
// therefore have to be administrable there.
//
// Hiding them left a hub published behind a TLS proxy with no way to ask for a
// secure cookie -- not from the dialog, and not from the admin CLI either,
// because one predicate filters the listing and the write alike.
func TestAdminSettingsService_SoloAdministersWhatItsSignInReads(t *testing.T) {
	env := setupAdminSettingsTest(t, &config.Config{SoloMode: true})
	ctx := context.Background()

	listed := flattenKeys(listedKeysByCategory(t, &config.Config{SoloMode: true}))
	for _, key := range []string{
		settings.KeySecureCookies.Name(),
		settings.KeySessionDurationSeconds.Name(),
	} {
		assert.True(t, listed[key], "a solo sign-in reads %s, so the operator must see it", key)
	}

	_, err := env.client.UpdateSetting(ctx, authedReq(&leapmuxv1.UpdateSettingRequest{
		Key: settings.KeySecureCookies.Name(), PartialJson: "true",
	}, env.token))
	require.NoError(t, err, "a hub published behind TLS must be able to ask for a secure cookie")

	_, err = env.client.UpdateSetting(ctx, authedReq(&leapmuxv1.UpdateSettingRequest{
		Key: settings.KeySessionDurationSeconds.Name(), PartialJson: "3600",
	}, env.token))
	require.NoError(t, err)
}

// TestAdminSettingsService_UpdateReportsRestart pins the restart flag: a
// restart-class write IS stored, but the running hub keeps the previous
// value, and the caller should not have to look the descriptor up again to
// learn that.
func TestAdminSettingsService_UpdateReportsRestart(t *testing.T) {
	env := setupAdminSettingsTest(t, &config.Config{})
	ctx := context.Background()

	hot, err := env.client.UpdateSetting(ctx, authedReq(&leapmuxv1.UpdateSettingRequest{
		Key: settings.KeyTimeouts.Name(), PartialJson: `{"api_seconds":11}`,
	}, env.token))
	require.NoError(t, err)
	assert.False(t, hot.Msg.GetRestart(), "a hot key applies within the snapshot TTL")

	restart, err := env.client.UpdateSetting(ctx, authedReq(&leapmuxv1.UpdateSettingRequest{
		Key: settings.KeyQueueBudget.Name(), PartialJson: `{"relay_bytes":0}`,
	}, env.token))
	require.NoError(t, err)
	assert.True(t, restart.Msg.GetRestart(), "queue_budget is restart-class")
}

// TestAdminSettingsService_UpdateSettingSecretRequiresASecretField pins
// the secret verb's own rule at the RPC surface: the wire contract says
// the document must specify at least one secret field, and without the
// check the verb rewrote public fields.
func TestAdminSettingsService_UpdateSettingSecretRequiresASecretField(t *testing.T) {
	env := setupAdminSettingsTest(t, &config.Config{})
	ctx := context.Background()

	_, err := env.client.UpdateSettingSecret(ctx, authedReq(&leapmuxv1.UpdateSettingSecretRequest{
		Key: settings.KeySMTP.Name(), PartialJson: `{"host":"evil.example"}`,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "must specify at least one of")

	value := settingValueByKey(t, env, settings.KeySMTP.Name())
	assert.False(t, value.GetCustomized(), "the refused write must store nothing")

	_, err = env.client.UpdateSettingSecret(ctx, authedReq(&leapmuxv1.UpdateSettingSecretRequest{
		Key: settings.KeySMTP.Name(), PartialJson: `{"password":"hunter2"}`,
	}, env.token))
	require.NoError(t, err)
	value = settingValueByKey(t, env, settings.KeySMTP.Name())
	assert.True(t, value.GetSecretSet()["password"])
}

// TestAdminSettingsService_ResetSettingsIsOneTransaction pins the batched
// reset. A per-key loop reached a cross-key refusal AFTER it already
// destroyed the earlier keys, so the hub told the operator that the reset
// failed while the selection and two provider rows were gone.
func TestAdminSettingsService_ResetSettingsIsOneTransaction(t *testing.T) {
	env := setupAdminSettingsTest(t, &config.Config{})
	ctx := context.Background()

	_, err := env.client.UpdateSettings(ctx, authedReq(&leapmuxv1.UpdateSettingsRequest{
		Writes: []*leapmuxv1.SettingWrite{
			{Key: captcha.RecaptchaV3Key.Name(), PartialJson: `{"site_key":"site","min_score":0.5}`, SecretPartialJson: `{"secret_key":"api-secret"}`},
			{Key: captcha.TurnstileKey.Name(), PartialJson: `{"site_key":"ts-site"}`, SecretPartialJson: `{"secret_key":"ts-secret"}`},
			{Key: captcha.CaptchaSelectedKey.Name(), PartialJson: `"recaptcha_v3"`},
		},
	}, env.token))
	require.NoError(t, err)

	// The hub refuses to clear the provider rows while the selection stands,
	// and BOTH rows survive.
	_, err = env.client.ResetSettings(ctx, authedReq(&leapmuxv1.ResetSettingsRequest{
		Keys: []string{captcha.TurnstileKey.Name(), captcha.RecaptchaV3Key.Name()},
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.True(t, settingValueByKey(t, env, captcha.TurnstileKey.Name()).GetCustomized(),
		"the first key of a refused batch must survive")
	assert.True(t, settingValueByKey(t, env, captcha.RecaptchaV3Key.Name()).GetCustomized())

	// The selection joins the same batch, and the whole set clears at once.
	got, err := env.client.ResetSettings(ctx, authedReq(&leapmuxv1.ResetSettingsRequest{
		Keys: []string{captcha.CaptchaSelectedKey.Name(), captcha.TurnstileKey.Name(), captcha.RecaptchaV3Key.Name()},
	}, env.token))
	require.NoError(t, err)
	require.Len(t, got.Msg.GetValues(), 3, "the reply reports every key the request listed, in request order")
	assert.Equal(t, captcha.CaptchaSelectedKey.Name(), got.Msg.GetValues()[0].GetKey())
	for _, v := range got.Msg.GetValues() {
		assert.Falsef(t, v.GetCustomized(), "%s must be back at its default", v.GetKey())
	}
}

// TestAdminSettingsService_ResetSettingsArgumentChecks pins the argument
// refusals, which must match the single-key verb's.
func TestAdminSettingsService_ResetSettingsArgumentChecks(t *testing.T) {
	env := setupAdminSettingsTest(t, &config.Config{})
	ctx := context.Background()

	_, err := env.client.ResetSettings(ctx, authedReq(&leapmuxv1.ResetSettingsRequest{}, env.token))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "keys is required")

	_, err = env.client.ResetSettings(ctx, authedReq(&leapmuxv1.ResetSettingsRequest{
		Keys: []string{"no.such.key"},
	}, env.token))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown setting key")

	_, err = env.client.ResetSettings(ctx, authedReq(&leapmuxv1.ResetSettingsRequest{
		Keys: []string{settings.KeyTimeouts.Name(), settings.KeyTimeouts.Name()},
	}, env.token))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "appears twice in one reset")
}

// TestAdminSettingsService_UpdateSettingsArgumentChecks pins the atomic
// verb's argument refusals against the single-key verbs'.
//
// UpdateSettings forwarded secret_partial_json for ANY key, so the one
// verb that never asked whether the key HAS an encrypted half accepted a
// document UpdateSettingSecret refuses -- and the merge then overlaid it
// as if it were the public half. The refusal lives in the Manager, so
// this handler and every other caller of the verb answer alike.
func TestAdminSettingsService_UpdateSettingsArgumentChecks(t *testing.T) {
	env := setupAdminSettingsTest(t, &config.Config{})
	ctx := context.Background()

	// A key with no secret fields: session_duration_seconds is a scalar.
	noSecrets := settings.KeySessionDurationSeconds.Name()

	_, err := env.client.UpdateSettings(ctx, authedReq(&leapmuxv1.UpdateSettingsRequest{
		Writes: []*leapmuxv1.SettingWrite{{Key: noSecrets, SecretPartialJson: `{"password":"hunter2"}`}},
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "has no secret fields")

	// The single-key verb refuses the same input in the same words.
	_, singleErr := env.client.UpdateSettingSecret(ctx, authedReq(&leapmuxv1.UpdateSettingSecretRequest{
		Key: noSecrets, PartialJson: `{"password":"hunter2"}`,
	}, env.token))
	require.Error(t, singleErr)
	assert.Equal(t, connect.CodeOf(singleErr), connect.CodeOf(err))
	assert.Equal(t, connect.CodeOf(singleErr).String()+": "+
		"settings key \""+noSecrets+"\" has no secret fields", err.Error())

	assert.False(t, settingValueByKey(t, env, noSecrets).GetCustomized(),
		"the refused write must store nothing")

	// The same shared rule refuses a write that carries neither half, and one
	// whose document cannot change anything.
	for _, tc := range []struct {
		name  string
		write *leapmuxv1.SettingWrite
		want  string
	}{
		{"neither half", &leapmuxv1.SettingWrite{Key: noSecrets}, "the partial document is required"},
		{"empty document", &leapmuxv1.SettingWrite{Key: noSecrets, PartialJson: `{}`}, "specifies no field"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := env.client.UpdateSettings(ctx, authedReq(&leapmuxv1.UpdateSettingsRequest{
				Writes: []*leapmuxv1.SettingWrite{tc.write},
			}, env.token))
			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestAdminSettingsService_EffectiveRedactsTheRulesOwnValue pins the
// redaction of the document the STORED value never passes through.
//
// effective_json is not the stored row: a read-time rule can replace the
// whole value with one the manager assembled at read time, and that value
// carries the key's secret fields too. Redacting only the stored-merged
// document leaves a key whose rule returns a live credential publishing it
// to every admin client. The captcha selection has exactly this shape -- it
// returns another provider's value, secret half included.
func TestAdminSettingsService_EffectiveRedactsTheRulesOwnValue(t *testing.T) {
	env := setupAdminSettingsEnv(t, &config.Config{},
		settings.WithEffective(settings.KeySMTP.Name(), func(*settings.Snapshot) (any, bool) {
			return settings.SMTPValue{
				Host:        "resolved.example",
				Port:        2525,
				FromAddress: "hub@example.com",
				Password:    "rule-secret",
			}, true
		}))
	ctx := context.Background()

	_, err := env.client.UpdateSetting(ctx, authedReq(&leapmuxv1.UpdateSettingRequest{
		Key:         settings.KeySMTP.Name(),
		PartialJson: `{"host":"stored.example","from_address":"hub@example.com","password":"stored-secret"}`,
	}, env.token))
	require.NoError(t, err)

	value := settingValueByKey(t, env, settings.KeySMTP.Name())

	var merged map[string]any
	require.NoError(t, json.Unmarshal([]byte(value.GetMergedJson()), &merged))
	assert.Equal(t, "stored.example", merged["host"], "merged_json stays the stored row merged onto the default")
	assert.Equal(t, "<redacted>", merged["password"])

	var effective map[string]any
	require.NoError(t, json.Unmarshal([]byte(value.GetEffectiveJson()), &effective))
	assert.Equal(t, "resolved.example", effective["host"], "the rule's value really reaches the wire")
	assert.Equal(t, "<redacted>", effective["password"], "and the rule's own secret is scrubbed")

	// The whole reply, over every document at once.
	assert.NotContains(t, value.GetEffectiveJson(), "rule-secret")
	assert.NotContains(t, value.GetMergedJson(), "stored-secret")
	assert.NotContains(t, value.GetValueJson(), "stored-secret")
}

// TestAdminSettingsService_EffectiveOfAWrongTypedRuleValue pins the
// fallback branch of the redaction: a value that does not decode into a JSON
// object at all.
//
// A read-time rule returns `any`, so a wiring mistake can hand a
// secret-bearing key a value of the wrong type. Redacted cannot find the
// secret fields in it, and reporting the value anyway would publish whatever
// the rule returned. The document becomes "<undecodable>" instead, and the
// listing still answers -- one broken rule must not fail the whole surface.
func TestAdminSettingsService_EffectiveOfAWrongTypedRuleValue(t *testing.T) {
	env := setupAdminSettingsEnv(t, &config.Config{},
		settings.WithEffective(settings.KeySMTP.Name(), func(*settings.Snapshot) (any, bool) {
			return 42, true
		}))

	value := settingValueByKey(t, env, settings.KeySMTP.Name())
	assert.JSONEq(t, `"<undecodable>"`, value.GetEffectiveJson(),
		"a value the redaction cannot examine is not reported")

	var merged map[string]any
	require.NoError(t, json.Unmarshal([]byte(value.GetMergedJson()), &merged))
	assert.Contains(t, merged, "port", "the other documents are unaffected")

	// A sibling key is unaffected too, which is what "the listing still
	// answers" means.
	assert.NotEmpty(t, settingValueByKey(t, env, settings.KeyTimeouts.Name()).GetEffectiveJson())
}

// TestAdminSettingsService_ResetSettingsRunsEachKeysAfterResetStep pins the
// post-reset step on the BATCH verb.
//
// The step is the KEY's, so which verb cleared the row cannot change whether
// it runs: resetting captcha.altcha inside a batch removes the same signing
// key every outstanding challenge carries. The tests covered only the
// single-key verb, so deleting the batch verb's runAfterReset call passed
// the suite.
func TestAdminSettingsService_ResetSettingsRunsEachKeysAfterResetStep(t *testing.T) {
	var calls int
	env := setupAdminSettingsTestWithProvisioner(t, &config.Config{},
		func(context.Context) error { calls++; return nil })
	ctx := context.Background()

	// A batch of keys that have no step runs nothing.
	_, err := env.client.ResetSettings(ctx, authedReq(&leapmuxv1.ResetSettingsRequest{
		Keys: []string{settings.KeySignupEnabled.Name(), settings.KeyTimeouts.Name()},
	}, env.token))
	require.NoError(t, err)
	assert.Zero(t, calls, "no key in that batch carries a post-reset step")

	// The same batch with the ALTCHA key in it runs its step once.
	_, err = env.client.ResetSettings(ctx, authedReq(&leapmuxv1.ResetSettingsRequest{
		Keys: []string{settings.KeySignupEnabled.Name(), captcha.AltchaKey.Name()},
	}, env.token))
	require.NoError(t, err)
	assert.Equal(t, 1, calls, "the batch verb runs the step, exactly as the single-key verb does")
}

// A step that fails inside the batch verb must surface, for the reason it
// does on the single-key verb: the caller must not believe the row was
// restored.
func TestAdminSettingsService_ResetSettingsReportsAStepFailure(t *testing.T) {
	env := setupAdminSettingsTestWithProvisioner(t, &config.Config{},
		func(context.Context) error { return errors.New("keystore unavailable") })

	_, err := env.client.ResetSettings(context.Background(), authedReq(&leapmuxv1.ResetSettingsRequest{
		Keys: []string{settings.KeySignupEnabled.Name(), captcha.AltchaKey.Name()},
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "the post-reset step failed",
		"the manager identifies the key whose step failed")
	assert.Contains(t, err.Error(), "keystore unavailable",
		"and the step's own cause stays readable")
}
