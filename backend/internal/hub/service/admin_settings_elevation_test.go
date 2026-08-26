package service_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// A hub setting is deployment-wide, and several of these keys ARE the hub's
// security controls: sign-up, captcha, the rate limits, SMTP, and the
// public_url the passkey relying party derives from. Renaming yourself asked
// for a proven factor while opening the hub to the world asked for nothing,
// so every write verb here takes the same elevation window.

// settingsWrite performs one write verb, authenticated by whichever credential
// `authorize` stamps on the request.
//
// Every call is a REAL write that succeeds on an elevated session, never a
// deliberately malformed request: a refusal these tests could not tell from an
// argument error would pass whether the gate existed or not.
type settingsWrite func(env *adminSettingsEnv, authorize requestAuth) error

// requestAuth stamps a credential onto a request. Two exist: a session cookie
// and an admin-scoped bearer, and the gate answers them differently on purpose.
type requestAuth func(header interface{ Set(string, string) })

func cookieAuth(token string) requestAuth {
	return func(h interface{ Set(string, string) }) { h.Set("Cookie", auth.CookieName+"="+token) }
}

func bearerAuth(bearer string) requestAuth {
	return func(h interface{ Set(string, string) }) { h.Set("Authorization", "Bearer "+bearer) }
}

func authorized[T any](msg *T, authorize requestAuth) *connect.Request[T] {
	req := connect.NewRequest(msg)
	authorize(req.Header())
	return req
}

// settingsWriteVerbs is every write verb on the service, so a verb added
// without the gate turns these tests red rather than shipping the hole.
func settingsWriteVerbs() map[string]settingsWrite {
	key := settings.KeySignupEnabled.Name()
	return map[string]settingsWrite{
		"UpdateSetting": func(env *adminSettingsEnv, authorize requestAuth) error {
			_, err := env.client.UpdateSetting(context.Background(),
				authorized(&leapmuxv1.UpdateSettingRequest{Key: key, PartialJson: `true`}, authorize))
			return err
		},
		"UpdateSettingSecret": func(env *adminSettingsEnv, authorize requestAuth) error {
			_, err := env.client.UpdateSettingSecret(context.Background(),
				authorized(&leapmuxv1.UpdateSettingSecretRequest{
					Key: settings.KeySMTP.Name(), PartialJson: `{"password":"hunter2hunter2"}`,
				}, authorize))
			return err
		},
		"UpdateSettings": func(env *adminSettingsEnv, authorize requestAuth) error {
			_, err := env.client.UpdateSettings(context.Background(),
				authorized(&leapmuxv1.UpdateSettingsRequest{
					Writes: []*leapmuxv1.SettingWrite{{Key: key, PartialJson: `true`}},
				}, authorize))
			return err
		},
		"ResetSetting": func(env *adminSettingsEnv, authorize requestAuth) error {
			_, err := env.client.ResetSetting(context.Background(),
				authorized(&leapmuxv1.ResetSettingRequest{Key: key}, authorize))
			return err
		},
		"ResetSettings": func(env *adminSettingsEnv, authorize requestAuth) error {
			_, err := env.client.ResetSettings(context.Background(),
				authorized(&leapmuxv1.ResetSettingsRequest{Keys: []string{key}}, authorize))
			return err
		},
	}
}

func TestAdminSettingsService_WriteGate(t *testing.T) {
	for name, write := range settingsWriteVerbs() {
		t.Run(name+" refuses an un-elevated session", func(t *testing.T) {
			env := setupAdminSettingsTestUnelevated(t, &config.Config{})
			err := write(env, cookieAuth(env.token))
			require.Error(t, err)
			assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
			var connectErr *connect.Error
			require.ErrorAs(t, err, &connectErr)
			assert.Equal(t, "1", connectErr.Meta().Get(service.ElevationRequiredHeader),
				"the refusal must be the one a step-up prompt can clear")
		})

		t.Run(name+" admits an elevated session", func(t *testing.T) {
			env := setupAdminSettingsTest(t, &config.Config{})
			assert.NoError(t, write(env, cookieAuth(env.token)))
		})

		// The headless path takes the SAME rule. A command-line credential
		// carries its own step-up window now, proven in a browser through
		// /auth/cli/elevate-authorization, so admitting it unconditionally
		// would make possession of the credential file the whole of the check
		// for the hub's own security settings.
		t.Run(name+" admits an elevated command-line credential", func(t *testing.T) {
			env := setupAdminSettingsTest(t, &config.Config{})
			assert.NoError(t, write(env, bearerAuth(env.elevatedAdminBearer(t))))
		})

		// And the refusal is the one the CLI can ACT on: marked, so it runs
		// the step-up leg and retries rather than reporting a dead end.
		t.Run(name+" tells an unelevated command-line credential to verify", func(t *testing.T) {
			env := setupAdminSettingsTest(t, &config.Config{})
			bearer, _ := env.adminBearer(t)
			err := write(env, bearerAuth(bearer))
			require.Error(t, err)
			assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
			var connectErr *connect.Error
			require.ErrorAs(t, err, &connectErr)
			assert.Equal(t, "1", connectErr.Meta().Get(service.ElevationRequiredHeader))
		})
	}
}

// Reads stay ungated. The dialog lists every key before the operator edits
// one, so gating the listing would prompt for a factor to look at a page.
func TestAdminSettingsService_ListNeedsNoElevation(t *testing.T) {
	env := setupAdminSettingsTestUnelevated(t, &config.Config{})
	resp, err := env.client.ListSettings(context.Background(),
		authedReq(&leapmuxv1.ListSettingsRequest{}, env.token))
	require.NoError(t, err)
	assert.NotEmpty(t, resp.Msg.GetDescriptors())
}

// The same rule for a COMMAND-LINE credential, which is where it matters most:
// an operator running one gated command a minute would otherwise be sent back
// to the browser every two hours, for a session the hub can see is continuous.
func TestAdminSettingsService_WriteSlidesACredentialWindow(t *testing.T) {
	// The ELEVATED fixture, because minting the credential is itself gated on
	// the consenting session. What this measures is the CREDENTIAL's window,
	// which the mint leaves unset.
	env := setupAdminSettingsTest(t, &config.Config{})
	ctx := context.Background()

	bearer, tokenID := env.adminBearer(t)

	// Elevate with a deadline close to lapsing, so a slide is visible as an
	// increase rather than as a value the harness could have written itself.
	now := time.Now().UTC()
	nearly := now.Add(time.Minute)
	n, err := env.st.APITokens().Elevate(ctx, store.ElevateAPITokenParams{
		TokenID:            tokenID,
		UserID:             userid.MustNew(env.adminID),
		ElevationProvenAt:  now,
		ElevationExpiresAt: nearly,
	}, now)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)

	_, err = env.client.UpdateSetting(ctx, authorized(&leapmuxv1.UpdateSettingRequest{
		Key: settings.KeySignupEnabled.Name(), PartialJson: `true`,
	}, bearerAuth(bearer)))
	require.NoError(t, err)

	row, err := env.st.APITokens().GetByID(ctx, tokenID)
	require.NoError(t, err)
	require.NotNil(t, row.ElevationExpiresAt)
	assert.True(t, row.ElevationExpiresAt.After(nearly),
		"a successful write must extend the window that admitted it: had %s, want later than %s",
		row.ElevationExpiresAt, nearly)
}

// The hub's standing rule is that a sensitive action slides the window that
// admitted it. A gated verb that did not slide would be a verb the window
// does not count as use, and the operator editing settings for two hours
// would be prompted again in the middle of the work.
func TestAdminSettingsService_WriteSlidesTheElevationWindow(t *testing.T) {
	env := setupAdminSettingsTestUnelevated(t, &config.Config{})
	ctx := context.Background()

	// Elevate with a deadline close to lapsing, so a slide is visible as an
	// increase rather than as a value the harness could have written itself.
	now := time.Now().UTC()
	nearly := now.Add(time.Minute)
	n, err := env.st.Sessions().Elevate(ctx, elevateParams(env, now, nearly), now)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)

	_, err = env.client.UpdateSetting(ctx, authedReq(&leapmuxv1.UpdateSettingRequest{
		Key: settings.KeySignupEnabled.Name(), PartialJson: `true`,
	}, env.token))
	require.NoError(t, err)

	session, err := env.st.Sessions().GetByID(ctx, env.token, time.Now().UTC())
	require.NoError(t, err)
	require.NotNil(t, session.ElevationExpiresAt)
	assert.True(t, session.ElevationExpiresAt.After(nearly),
		"a successful write must extend the window that admitted it: had %s, want later than %s",
		session.ElevationExpiresAt, nearly)
}

// A REFUSED write must not slide. The window is a record of proven presence,
// and a request the hub rejected proves nothing.
func TestAdminSettingsService_RefusedWriteDoesNotSlideTheWindow(t *testing.T) {
	env := setupAdminSettingsTestUnelevated(t, &config.Config{})
	ctx := context.Background()

	now := time.Now().UTC()
	nearly := now.Add(time.Minute)
	n, err := env.st.Sessions().Elevate(ctx, elevateParams(env, now, nearly), now)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)

	_, err = env.client.UpdateSetting(ctx, authedReq(&leapmuxv1.UpdateSettingRequest{
		Key: "no_such_setting_key", PartialJson: `true`,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	session, err := env.st.Sessions().GetByID(ctx, env.token, time.Now().UTC())
	require.NoError(t, err)
	require.NotNil(t, session.ElevationExpiresAt)
	assert.False(t, session.ElevationExpiresAt.After(nearly),
		"a refused write must leave the window where it was: had %s, expected no later than %s",
		session.ElevationExpiresAt, nearly)
}

// An unknown key is reported as an unknown key even without a factor, so the
// operator is not asked to verify for a request the hub was going to refuse
// on its arguments anyway.
func TestAdminSettingsService_UnknownKeyIsReportedBeforeTheGate(t *testing.T) {
	env := setupAdminSettingsTestUnelevated(t, &config.Config{})
	_, err := env.client.UpdateSetting(context.Background(), authedReq(&leapmuxv1.UpdateSettingRequest{
		Key: "no_such_setting_key", PartialJson: `true`,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// elevateParams stamps a window that ENDS at `until`, so a slide shows up as
// an increase over a deadline the test chose rather than over one the shared
// helper wrote.
func elevateParams(env *adminSettingsEnv, provenAt, until time.Time) store.ElevateSessionParams {
	return store.ElevateSessionParams{
		SessionID:          env.token,
		UserID:             userid.MustNew(env.adminID),
		ElevationProvenAt:  provenAt,
		ElevationExpiresAt: until,
	}
}
