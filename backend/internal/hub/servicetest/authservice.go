package servicetest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/settingsregistry"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// Package servicetest holds shared test helpers for the hub's service
// layer, beside the storetest convention. It cannot live in testutil:
// it imports the service package, whose internal tests import testutil
// themselves.

// NewSettingsManager builds the settings manager tests hand to the
// services that consume runtime settings, registered with every key the
// hub registers (core, captcha, rate limits) so a test can share the
// manager with a captcha manager or the rate-limit interceptor. It must
// share the store (and, for secret-bearing keys, the keystore) with the
// services under test: a second manager over the same rows would still
// converge, but only after its TTL — writes through the shared manager
// apply to the next request. A nil keystore mints a throwaway ring, so
// seeds on secret-bearing keys (the SMTP password half) work anywhere;
// tests whose services read those secrets back pass their real keystore.
func NewSettingsManager(t *testing.T, st store.Store, ks *keystore.Keystore) *settings.Manager {
	t.Helper()
	if ks == nil {
		var key [32]byte
		minted, err := keystore.New(map[uint32][32]byte{1: key})
		require.NoError(t, err)
		ks = minted
	}
	m := settingsregistry.NewManager(st, ks)
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
// nil keystore/captcha. Store, Config, Settings, and Lifecycle stay
// explicit at each call site (they are what the tests vary); Keystore
// and Captcha are overridden on the returned struct where a test
// exercises them. set must share st (see NewSettingsManager).
func AuthServiceDeps(st store.Store, cfg *config.Config, set *settings.Manager, lifecycle *auth.CredentialLifecycleEffects) service.AuthServiceDeps {
	return service.AuthServiceDeps{
		Store:     st,
		Config:    cfg,
		Settings:  set,
		Lifecycle: lifecycle,
		Keystore:  nil,
		Mail:      mail.NewStubSender(),
		Renderer:  mail.Renderer{},
		Captcha:   nil,
	}
}
