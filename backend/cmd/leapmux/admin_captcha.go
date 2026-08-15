package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/hub/captcha"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// captchaShowJSON is the admin `captcha show` payload: the selected
// provider's effective settings plus whether they come from a stored row
// or built-in defaults, and which providers have rows at all. Providers
// print as their CLI aliases — never the enum number or the CAPS proto
// name — and secrets are never part of this output.
type captchaShowJSON struct {
	Provider   string                       `json:"provider"`
	Enabled    bool                         `json:"enabled"`
	Altcha     *captcha.AltchaSettings      `json:"altcha,omitempty"`
	Recaptcha3 *captcha.RecaptchaV3Settings `json:"recaptcha_v3,omitempty"`
	Turnstile  *captcha.TurnstileSettings   `json:"turnstile,omitempty"`
	Customized bool                         `json:"customized"`
	Configured []string                     `json:"configured"`
}

// fromCaptchaConfig mirrors a captcha.Config into the show shape,
// translating the enum to its alias.
func fromCaptchaConfig(cfg captcha.Config) captchaShowJSON {
	return captchaShowJSON{
		Provider:   captcha.ProviderAlias(cfg.Provider),
		Enabled:    cfg.Enabled,
		Altcha:     cfg.Altcha,
		Recaptcha3: cfg.RecaptchaV3,
		Turnstile:  cfg.Turnstile,
	}
}

// captchaManagerFor builds the same manager the hub enforces with, so the
// CLI's idea of "effective" and the hub's can never diverge. The keystore
// is loaded once per invocation. LoadOrGenerate, not LoadFromFile: the
// very first admin action on a fresh data dir may precede any hub run.
// Solo is always false here — a solo-mode hub never enforces captcha, and
// this CLI manages multi-user hub data.
func captchaManagerFor(cfg *config.Config, st store.Store) (*captcha.Manager, error) {
	ks, err := keystore.LoadOrGenerate(cfg.EncryptionKeyFilePath())
	if err != nil {
		return nil, fmt.Errorf("load encryption key: %w", err)
	}
	return captcha.NewManager(st, ks, false), nil
}

func runCaptchaShow(cmd adminCmdCtx, args []string) error {
	return withAdminStore(cmd, args, nil, func(ctx context.Context, cfg *config.Config, st store.Store) error {
		mgr, err := captchaManagerFor(cfg, st)
		if err != nil {
			return err
		}
		effective, customized, err := mgr.Describe(ctx)
		if err != nil {
			return fmt.Errorf("load captcha config: %w", err)
		}
		configured := []string{}
		rows, err := st.CaptchaConfig().List(ctx)
		if err != nil {
			return fmt.Errorf("list captcha providers: %w", err)
		}
		for _, row := range rows {
			configured = append(configured, captcha.ProviderAlias(row.Provider))
		}
		shown := fromCaptchaConfig(effective)
		shown.Customized = customized
		shown.Configured = configured
		return printJSON(shown)
	})
}

// captchaSetFlags declares runCaptchaSet's flag set once; the altcha
// flags apply to the altcha provider and the key/score flags to the
// external providers, and exactly one group is meaningful per invocation.
type captchaSetFlags struct {
	flags      *flag.FlagSet
	provider   *string
	algorithm  *string
	cost       *int64
	memoryCost *int64
	parallel   *int64
	expires    *time.Duration
	siteKey    *string
	secret     *string
	minScore   *float64
}

func (f *captchaSetFlags) declare(fs *flag.FlagSet) {
	f.flags = fs
	f.provider = fs.String("provider", "", fmt.Sprintf("provider to configure and activate (%s)", strings.Join(captcha.SupportedProviders(), ", ")))
	f.algorithm = fs.String("algorithm", "", fmt.Sprintf("ALTCHA algorithm (%s)", strings.Join(captcha.SupportedAltchaAlgorithms(), ", ")))
	f.cost = fs.Int64("cost", 0, "ALTCHA per-derivation cost (PBKDF2/SHA iterations, SCRYPT N, or ARGON2ID time parameter; 0 = algorithm default)")
	f.memoryCost = fs.Int64("memory-cost", 0, "ALTCHA SCRYPT r (block-count multiplier, NOT bytes) or ARGON2ID m (KiB); 0 = algorithm default")
	f.parallel = fs.Int64("parallelism", 0, "ALTCHA SCRYPT p or ARGON2ID threads; 0 = algorithm default")
	f.expires = fs.Duration("expires", 0, "ALTCHA challenge expiry (e.g. 20m; whole seconds)")
	f.siteKey = fs.String("site-key", "", "external provider's public site key (recaptcha_v3, turnstile)")
	f.secret = fs.String("secret", "", "external provider's secret key; required when configuring a provider for the first time")
	f.minScore = fs.Float64("min-score", 0, "reCAPTCHA v3 minimum score, 0-1 (0 = the 0.5 default)")
}

func (f *captchaSetFlags) set(name string) bool {
	return explicitlySet(f.flags)[name]
}

func (f *captchaSetFlags) anySet() bool {
	for _, name := range []string{"provider", "algorithm", "cost", "memory-cost", "parallelism", "expires", "site-key", "secret", "min-score"} {
		if f.set(name) {
			return true
		}
	}
	return false
}

func runCaptchaSet(cmd adminCmdCtx, args []string) error {
	var cf captchaSetFlags
	return withAdminStore(cmd, args, cf.declare, func(ctx context.Context, cfg *config.Config, st store.Store) error {
		if !cf.anySet() {
			return fmt.Errorf("at least one of --provider, --algorithm, --cost, --memory-cost, --parallelism, --expires, --site-key, --secret, --min-score is required")
		}

		mgr, err := captchaManagerFor(cfg, st)
		if err != nil {
			return err
		}
		current, _, err := mgr.Describe(ctx)
		if err != nil {
			return fmt.Errorf("load captcha config: %w", err)
		}

		// The target provider is the --provider flag, else the currently
		// selected one. Activating (--provider given) also switches; a
		// flag-less edit tunes the selected provider in place.
		target := current.Provider
		switching := false
		if cf.set("provider") {
			target, err = captcha.ParseProvider(*cf.provider)
			if err != nil {
				return fmt.Errorf("invalid captcha configuration: %w", err)
			}
			switching = true
		}

		// Provider-foreign flags are refused rather than ignored: a cost
		// meant for ALTCHA must not be silently dropped under Turnstile,
		// and a site key meant for Turnstile must not vanish under ALTCHA.
		if target != captcha.ProviderAltcha {
			for _, name := range []string{"algorithm", "cost", "memory-cost", "parallelism", "expires"} {
				if cf.set(name) {
					return fmt.Errorf("--%s is an altcha-only flag; the target provider is %s", name, captcha.ProviderAlias(target))
				}
			}
		}
		if target == captcha.ProviderAltcha {
			for _, name := range []string{"site-key", "secret", "min-score"} {
				if cf.set(name) {
					return fmt.Errorf("--%s is an external-provider flag; the target provider is altcha", name)
				}
			}
		}
		if target == captcha.ProviderTurnstile && cf.set("min-score") {
			return fmt.Errorf("--min-score is a recaptcha_v3-only flag; the target provider is turnstile")
		}

		// Activating an external provider configures it in the same
		// invocation: the row cannot exist without its keys.
		if switching && target != captcha.ProviderAltcha {
			if !cf.set("site-key") || !cf.set("secret") {
				return fmt.Errorf("switching to %s requires --site-key and --secret in the same invocation", captcha.ProviderAlias(target))
			}
		}

		var settingsJSON string
		switch target {
		case captcha.ProviderAltcha:
			settingsJSON, err = buildAltchaSettings(&cf, current)
		case captcha.ProviderRecaptchaV3:
			settingsJSON, err = buildRecaptchaSettings(&cf, current)
		case captcha.ProviderTurnstile:
			settingsJSON, err = buildTurnstileSettings(&cf, current)
		}
		if err != nil {
			return fmt.Errorf("invalid captcha configuration: %w", err)
		}

		var secretBytes []byte
		if cf.set("secret") {
			secretBytes, err = mgr.EncryptSecret(target, *cf.secret)
			if err != nil {
				return err
			}
		}

		cs := st.CaptchaConfig()
		if switching {
			if target == captcha.ProviderAltcha {
				// Reuse the altcha row (and its original HMAC secret) when
				// one exists; otherwise provision it now.
				if err := mgr.EnsureAltchaRow(ctx); err != nil {
					return fmt.Errorf("provision altcha row: %w", err)
				}
			}
		} else if target == captcha.ProviderAltcha {
			// An in-place altcha edit needs the selected row to exist; on a
			// fresh install this provisions it with defaults first.
			if err := mgr.EnsureProvisioned(ctx); err != nil {
				return fmt.Errorf("provision captcha config: %w", err)
			}
		}
		if secretBytes != nil {
			// A secret accompanies the settings (first configuration or
			// rotation); write both.
			if err := cs.Upsert(ctx, store.UpsertCaptchaConfigParams{
				Provider: target,
				Secret:   secretBytes,
				Settings: settingsJSON,
			}); err != nil {
				return fmt.Errorf("update captcha config: %w", err)
			}
		} else if err := cs.UpdateSettings(ctx, target, settingsJSON); err != nil {
			// Settings-only write: the stored secret is never touched.
			return fmt.Errorf("update captcha config: %w", err)
		}
		if switching {
			if err := cs.Activate(ctx, target); err != nil {
				return fmt.Errorf("activate captcha provider: %w", err)
			}
		}

		if err := printCaptchaSummary(target, settingsJSON); err != nil {
			return err
		}
		if switching {
			fmt.Printf("Activated provider %s (verification is enabled; changes reach a running hub within ~30s)\n", captcha.ProviderAlias(target))
		} else {
			fmt.Println("Updated captcha configuration (changes reach a running hub within ~30s)")
		}
		return nil
	})
}

// buildAltchaSettings applies the altcha flags onto the current (or
// default) ALTCHA settings. An algorithm switch resets the
// family-specific parameters to the new family's defaults (unless passed
// on this command line): carrying the old family's values would silently
// reinterpret them in the new family's units - an ARGON2ID memory in KiB
// would become SCRYPT's block multiplier.
func buildAltchaSettings(cf *captchaSetFlags, current captcha.Config) (string, error) {
	next := captcha.DefaultAltchaSettings()
	if current.Provider == captcha.ProviderAltcha && current.Altcha != nil {
		next = *current.Altcha
	}
	if cf.set("algorithm") {
		family, err := captcha.DefaultAltchaSettingsFor(*cf.algorithm)
		if err != nil {
			return "", err
		}
		next = family
	}
	// An explicit 0 restores the algorithm family's default for that
	// parameter (the derive funcs substitute their own defaults for zero
	// values, and DefaultAltchaSettingsFor documents them).
	if cf.set("cost") {
		next.Cost = *cf.cost
		if next.Cost == 0 {
			next.Cost = altchaFamilyDefaults(next.Algorithm).Cost
		}
	}
	if cf.set("memory-cost") {
		next.MemoryCost = *cf.memoryCost
		if next.MemoryCost == 0 {
			next.MemoryCost = altchaFamilyDefaults(next.Algorithm).MemoryCost
		}
	}
	if cf.set("parallelism") {
		next.Parallelism = *cf.parallel
		if next.Parallelism == 0 {
			next.Parallelism = altchaFamilyDefaults(next.Algorithm).Parallelism
		}
	}
	if cf.set("expires") {
		secs, err := wholeSeconds(*cf.expires)
		if err != nil {
			return "", fmt.Errorf("invalid --expires: %w", err)
		}
		next.ChallengeExpirySeconds = secs
	}
	if err := next.Validate(); err != nil {
		return "", err
	}
	return marshalSettings(next)
}

// buildRecaptchaSettings applies the recaptcha_v3 flags onto the current
// (or default) settings. An explicit --min-score 0 restores the 0.5
// default.
func buildRecaptchaSettings(cf *captchaSetFlags, current captcha.Config) (string, error) {
	next := captcha.DefaultRecaptchaV3Settings()
	if current.Provider == captcha.ProviderRecaptchaV3 && current.RecaptchaV3 != nil {
		next = *current.RecaptchaV3
	}
	if cf.set("site-key") {
		next.SiteKey = *cf.siteKey
	}
	if cf.set("min-score") {
		next.MinScore = *cf.minScore
		if next.MinScore == 0 {
			next.MinScore = 0.5
		}
	}
	if err := next.Validate(); err != nil {
		return "", err
	}
	return marshalSettings(next)
}

func buildTurnstileSettings(cf *captchaSetFlags, current captcha.Config) (string, error) {
	next := captcha.DefaultTurnstileSettings()
	if current.Provider == captcha.ProviderTurnstile && current.Turnstile != nil {
		next = *current.Turnstile
	}
	if cf.set("site-key") {
		next.SiteKey = *cf.siteKey
	}
	if err := next.Validate(); err != nil {
		return "", err
	}
	return marshalSettings(next)
}

// altchaFamilyDefaults returns the algorithm family's default parameters;
// the algorithm is known because Validate or DefaultAltchaSettingsFor
// already accepted it.
func altchaFamilyDefaults(algorithm string) captcha.AltchaSettings {
	family, err := captcha.DefaultAltchaSettingsFor(algorithm)
	if err != nil {
		return captcha.DefaultAltchaSettings()
	}
	return family
}

func marshalSettings(settings any) (string, error) {
	b, err := json.Marshal(settings)
	if err != nil {
		return "", fmt.Errorf("marshal captcha settings: %w", err)
	}
	return string(b), nil
}

func printCaptchaSummary(provider captcha.Provider, settingsJSON string) error {
	switch provider {
	case captcha.ProviderAltcha:
		var s captcha.AltchaSettings
		if err := json.Unmarshal([]byte(settingsJSON), &s); err != nil {
			return err
		}
		fmt.Printf("Saved altcha settings (algorithm %s, cost %d, expiry %s)\n",
			s.Algorithm, s.Cost, time.Duration(s.ChallengeExpirySeconds)*time.Second)
	case captcha.ProviderRecaptchaV3:
		var s captcha.RecaptchaV3Settings
		if err := json.Unmarshal([]byte(settingsJSON), &s); err != nil {
			return err
		}
		fmt.Printf("Saved recaptcha_v3 settings (site key %s, min score %g)\n", s.SiteKey, s.MinScore)
	case captcha.ProviderTurnstile:
		var s captcha.TurnstileSettings
		if err := json.Unmarshal([]byte(settingsJSON), &s); err != nil {
			return err
		}
		fmt.Printf("Saved turnstile settings (site key %s)\n", s.SiteKey)
	}
	return nil
}

func runCaptchaSetEnabled(cmd adminCmdCtx, args []string, enabled bool) error {
	return withAdminStore(cmd, args, nil, func(ctx context.Context, cfg *config.Config, st store.Store) error {
		mgr, err := captchaManagerFor(cfg, st)
		if err != nil {
			return err
		}
		if _, _, err := mgr.Describe(ctx); err != nil {
			return fmt.Errorf("load captcha config: %w", err)
		}

		if err := mgr.EnsureProvisioned(ctx); err != nil {
			return fmt.Errorf("provision captcha config: %w", err)
		}
		if err := st.CaptchaConfig().SetEnabled(ctx, enabled); err != nil {
			return fmt.Errorf("update captcha config: %w", err)
		}
		fmt.Printf("%s captcha verification (the honeypot check stays active either way)\n", enabledWord(enabled))
		return nil
	})
}

func runCaptchaReset(cmd adminCmdCtx, args []string) error {
	var provider *string
	var flags *flag.FlagSet
	return withAdminStore(cmd, args, func(fs *flag.FlagSet) {
		flags = fs
		provider = fs.String("provider", "", fmt.Sprintf("reset only this provider's row (%s); omit to reset everything", strings.Join(captcha.SupportedProviders(), ", ")))
	}, func(ctx context.Context, cfg *config.Config, st store.Store) error {
		if explicitlySet(flags)["provider"] {
			p, err := captcha.ParseProvider(*provider)
			if err != nil {
				return fmt.Errorf("invalid captcha configuration: %w", err)
			}
			if err := st.CaptchaConfig().DeleteProvider(ctx, p); err != nil {
				return fmt.Errorf("reset captcha config: %w", err)
			}
			fmt.Printf("Reset the %s provider's configuration (the row is recreated with fresh defaults on next use)\n", captcha.ProviderAlias(p))
			return nil
		}
		if err := st.CaptchaConfig().Delete(ctx); err != nil {
			return fmt.Errorf("reset captcha config: %w", err)
		}
		fmt.Println("Reset captcha configuration to defaults (the altcha signing secret is regenerated on next hub use)")
		return nil
	})
}
