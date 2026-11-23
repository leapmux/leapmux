package servicetest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/settingsregistry"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/testutil"
	hubwebauthn "github.com/leapmux/leapmux/internal/hub/webauthn"
)

// Package servicetest holds shared test helpers for the hub's service
// layer, beside the storetest convention. It cannot live in testutil:
// it imports the service package, whose internal tests import testutil
// themselves.

// NewSettingsManager builds the settings manager tests hand to the
// services that consume runtime settings, registered with every key the
// hub registers (core, captcha, rate limits) so a test can share the
// manager with a captcha manager or the rate-limit interceptor. It must
// share the store with the services under test: a second manager over
// the same rows would still converge, but only after its TTL — writes
// through the shared manager apply to the next request.
func NewSettingsManager(t *testing.T, st store.Store) *settings.Manager {
	t.Helper()
	m := settingsregistry.NewManager(st)
	require.NoError(t, m.Load(context.Background()))
	return m
}

// AuthPolicy adapts a settings manager to the auth interceptor's Policy
// closure the way the hub wires it, so a test flips the verification
// requirement, the session duration, or the cookie name by writing the
// settings key. PolicyFromSettings is the same mapping the hub's own
// closure uses, so a test cannot enforce a policy the hub computes
// differently.
func AuthPolicy(set *settings.Manager) func(context.Context) auth.Policy {
	return func(ctx context.Context) auth.Policy {
		return auth.PolicyFromSettings(ctx, set.Snapshot(ctx))
	}
}

// AuthServiceDeps builds the service.AuthServiceDeps literal every hub
// test constructs: the stub mail sender, the zero-value renderer, and
// nil captcha. Store, Config, Settings, and Lifecycle stay explicit at
// each call site (they are what the tests vary); Captcha is overridden
// on the returned struct where a test exercises it. set must share st
// (see NewSettingsManager).
func AuthServiceDeps(st store.Store, cfg *config.Config, set *settings.Manager, lifecycle *auth.CredentialLifecycleEffects) service.AuthServiceDeps {
	return service.AuthServiceDeps{
		SessionHasher:       testutil.TestSecretHasher(),
		Store:               st,
		Config:              cfg,
		Settings:            set,
		Lifecycle:           lifecycle,
		Mail:                mail.NewStubSender(),
		Renderer:            mail.Renderer{},
		Captcha:             nil,
		PendingOAuthSignups: service.NewPendingOAuthSignupStore(),
		WebAuthnSessions:    hubwebauthn.NewSessionStore(),
	}
}
