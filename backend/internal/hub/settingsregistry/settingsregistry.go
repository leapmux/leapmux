// Package settingsregistry assembles the hub's complete settings
// registry: every domain's keys (core, captcha, rate limits) plus the
// cross-key rules that span them. The hub server, the admin CLI, and the
// service test harness all construct their settings manager here and
// nowhere else, so the three cannot drift on which keys exist or which
// combinations are refused — a domain registered here is the whole
// surface, for every consumer.
package settingsregistry

import (
	"github.com/leapmux/leapmux/internal/hub/captcha"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/ratelimit"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// NewManager builds the settings manager over st with every domain's
// descriptors registered and both cross-key rules attached (SMTP
// availability for email verification; provider completeness for the
// captcha selection). Extra options — test TTLs and clock overrides —
// apply after the registry defaults, so callers never restate the
// registry itself.
func NewManager(st store.Store, ks *keystore.Keystore, opts ...settings.Option) *settings.Manager {
	descs := append(settings.CoreDescriptors(), captcha.SettingsDescriptors()...)
	descs = append(descs, ratelimit.SettingsDescriptors()...)
	opts = append([]settings.Option{
		settings.WithCrossValidation(settings.SMTPConfigured, captcha.SelectedConfigured),
	}, opts...)
	return settings.NewManager(st, ks, descs, opts...)
}
