package mail

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/settings"
)

// NewSettingsSender returns a Sender that resolves the SMTP configuration
// from the settings snapshot on every Send, so an admin's `settings set
// smtp ...` applies to the next email without a restart. With SMTP
// unconfigured it behaves exactly like NewDisabledSender (ErrEmailDisabled
// from every Send). Per-Send construction of the underlying SMTPSender is
// free — it holds only a config value and dials a fresh connection per
// Send anyway.
func NewSettingsSender(set *settings.Manager) Sender {
	return settingsSender{set: set}
}

type settingsSender struct {
	set *settings.Manager
}

// Send delivers msg via the currently configured SMTP relay. The key's
// declared default owns what an unset field means (port 587, STARTTLS):
// validateSMTP rejects a zero port whenever a host is set and refuses an
// empty TLS mode outright, so the value read here always carries both.
func (s settingsSender) Send(ctx context.Context, msg Message) error {
	v := settings.KeySMTP.Of(s.set.Snapshot(ctx))
	if !v.Enabled() {
		return NewDisabledSender().Send(ctx, msg)
	}
	return NewSMTPSender(SMTPConfig{
		Host:     v.Host,
		Port:     v.Port,
		Username: v.Username,
		Password: v.Password,
		From:     v.FromAddress,
		TLSMode:  v.TLSMode,
	}).Send(ctx, msg)
}
