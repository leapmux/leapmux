package captcha

import (
	"context"
	"fmt"
)

// recaptchaVerifyURL is Google's fixed siteverify endpoint.
const recaptchaVerifyURL = "https://www.google.com/recaptcha/api/siteverify"

// defaultRecaptchaMinScore is Google's documented default score
// threshold: 0.0 is certainly automated, 1.0 is certainly human, and
// scores at or above the minimum are accepted. A stored min_score of 0
// means this default.
const defaultRecaptchaMinScore = 0.5

// RecaptchaV3Settings configure the reCAPTCHA v3 provider, stored as the
// recaptcha_v3 row's settings JSON. SiteKey is the public key the
// frontend's script is loaded with; MinScore is the acceptance threshold
// applied to Google's reply (0 means the 0.5 default). The secret is the
// row's keystore-encrypted secret, not a settings field.
type RecaptchaV3Settings struct {
	SiteKey  string  `json:"site_key"`
	MinScore float64 `json:"min_score"`
}

// DefaultRecaptchaV3Settings returns the recommended reCAPTCHA v3
// settings (empty site key — the operator must supply theirs).
func DefaultRecaptchaV3Settings() RecaptchaV3Settings {
	return RecaptchaV3Settings{MinScore: defaultRecaptchaMinScore}
}

// Validate rejects settings that cannot work at runtime: an empty site
// key leaves the frontend nothing to load, and a score outside (0, 1]
// either accepts everything or nothing.
func (s RecaptchaV3Settings) Validate() error {
	if s.SiteKey == "" {
		return fmt.Errorf("recaptcha_v3 site key must not be empty")
	}
	if s.MinScore <= 0 || s.MinScore > 1 {
		return fmt.Errorf("recaptcha_v3 min_score must be between 0 (exclusive) and 1 (got %g)", s.MinScore)
	}
	return nil
}

// verifyRecaptcha checks one reCAPTCHA v3 token against Google's
// siteverify endpoint. Google's server-side guidance is encoded here:
// verify the action name the token was executed with matches the
// procedure being protected, and require the reply's score to clear the
// configured minimum. Tokens are single-use and expire after two
// minutes; both surface as timeout-or-duplicate.
func (m *Manager) verifyRecaptcha(ctx context.Context, secret, token, expectedAction string, minScore float64) error {
	result, err := verifyWithClient(ctx, m.recaptcha, ProviderRecaptchaV3, secret, token, func(resp siteverifyResponse) bool {
		return resp.Action == expectedAction && resp.Score >= minScore
	})
	return m.counted(ProviderRecaptchaV3, result, err)
}
