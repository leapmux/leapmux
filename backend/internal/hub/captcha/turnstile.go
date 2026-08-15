package captcha

import (
	"context"
	"encoding/json"
	"fmt"
)

// turnstileVerifyURL is Cloudflare's fixed siteverify endpoint.
const turnstileVerifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

// TurnstileSettings configure the Cloudflare Turnstile provider, stored
// as the turnstile row's settings JSON. SiteKey is the public key the
// frontend renders the widget with; the secret (Cloudflare calls it the
// secret key) is the row's keystore-encrypted secret, not a settings
// field. Turnstile has no tunables beyond the keys: tokens are valid for
// five minutes and single-use, and the widget's behavior is chosen
// client-side.
type TurnstileSettings struct {
	SiteKey string `json:"site_key"`
}

// DefaultTurnstileSettings returns the default Turnstile settings (empty
// site key — the operator must supply theirs).
func DefaultTurnstileSettings() TurnstileSettings {
	return TurnstileSettings{}
}

// Validate rejects settings that cannot work at runtime: an empty site
// key leaves the frontend nothing to render.
func (s TurnstileSettings) Validate() error {
	if s.SiteKey == "" {
		return fmt.Errorf("turnstile site key must not be empty")
	}
	return nil
}

// parseTurnstileSettings decodes the stored settings JSON; an
// undecodable blob yields the defaults. Validation happens in Effective,
// which falls back to DefaultConfig on refusal.
func parseTurnstileSettings(raw string) TurnstileSettings {
	s := DefaultTurnstileSettings()
	if raw == "" {
		return s
	}
	var stored TurnstileSettings
	if err := json.Unmarshal([]byte(raw), &stored); err == nil && stored.SiteKey != "" {
		s.SiteKey = stored.SiteKey
	}
	return s
}

// verifyTurnstile checks one Turnstile token against Cloudflare's
// siteverify endpoint. The widget is rendered with the procedure's
// action, and siteverify echoes it back — a mismatch means the token was
// minted for a different form, so it is refused exactly like an
// reCAPTCHA action mismatch. Tokens are single-use and valid for five
// minutes; both surface as timeout-or-duplicate.
func (m *Manager) verifyTurnstile(ctx context.Context, secret, token, expectedAction string) error {
	result, err := verifyWithClient(ctx, m.turnstile, ProviderTurnstile, secret, token, func(resp siteverifyResponse) bool {
		return resp.Action == expectedAction
	})
	return m.counted(ProviderTurnstile, result, err)
}
