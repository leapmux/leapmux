package servicetest

import (
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// Package servicetest holds shared test helpers for the hub's service
// layer, beside the storetest convention. It cannot live in testutil:
// it imports the service package, whose internal tests import testutil
// themselves.

// AuthServiceDeps builds the service.AuthServiceDeps literal every hub
// test constructs: the stub mail sender, the zero-value renderer, and nil
// keystore/captcha. Store, Config, and Lifecycle stay explicit at each
// call site (they are what the tests vary); Keystore and Captcha are
// overridden on the returned struct where a test exercises them.
func AuthServiceDeps(st store.Store, cfg *config.Config, lifecycle *auth.CredentialLifecycleEffects) service.AuthServiceDeps {
	return service.AuthServiceDeps{
		Store:     st,
		Config:    cfg,
		Lifecycle: lifecycle,
		Keystore:  nil,
		Mail:      mail.NewStubSender(),
		Renderer:  mail.Renderer{},
		Captcha:   nil,
	}
}
