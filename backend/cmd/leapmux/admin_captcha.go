package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"maps"
	"slices"
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

// captchaManagerFor builds the same manager the hub enforces with, so the
// CLI's idea of "effective" and the hub's can never diverge. The keystore
// is loaded once per invocation and also returned: the CLI's write path
// (secret encryption, row provisioning) needs it directly. LoadOrGenerate,
// not LoadFromFile: the very first admin action on a fresh data dir may
// precede any hub run. Solo is always false here — a solo-mode hub never
// enforces captcha, and this CLI manages multi-user hub data.
func captchaManagerFor(cfg *config.Config, st store.Store) (*captcha.Manager, *keystore.Keystore, error) {
	ks, err := keystore.LoadOrGenerate(cfg.EncryptionKeyFilePath())
	if err != nil {
		return nil, nil, fmt.Errorf("load encryption key: %w", err)
	}
	return captcha.NewManager(st, ks, false), ks, nil
}

func runCaptchaShow(cmd adminCmdCtx, args []string) error {
	return withAdminStore(cmd, args, nil, func(ctx context.Context, cfg *config.Config, st store.Store) error {
		mgr, _, err := captchaManagerFor(cfg, st)
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
		shown := captchaShowJSON{
			Provider:   captcha.ProviderAlias(effective.Provider),
			Enabled:    effective.Enabled,
			Altcha:     effective.Altcha,
			Recaptcha3: effective.RecaptchaV3,
			Turnstile:  effective.Turnstile,
			Customized: customized,
			Configured: configured,
		}
		return printJSON(shown)
	})
}

// flagProviders maps each runCaptchaSet setting flag to the providers
// that accept it. The provider-foreign refusals, the required-any check,
// and the usage error all derive from this one table, so a new flag
// registers in exactly one place together with the provider set that
// owns it — no per-provider list or ad-hoc refusal to keep in lockstep.
var flagProviders = map[string][]captcha.Provider{
	"algorithm":   {captcha.ProviderAltcha},
	"cost":        {captcha.ProviderAltcha},
	"memory-cost": {captcha.ProviderAltcha},
	"parallelism": {captcha.ProviderAltcha},
	"expires":     {captcha.ProviderAltcha},
	"site-key":    {captcha.ProviderRecaptchaV3, captcha.ProviderTurnstile},
	"secret":      {captcha.ProviderRecaptchaV3, captcha.ProviderTurnstile},
	"min-score":   {captcha.ProviderRecaptchaV3},
}

// captchaFlagNames lists runCaptchaSet's setting flags, sorted once at
// init, so derived output (usage errors, required-any checks) is
// deterministic without re-deriving the list per call.
var captchaFlagNames = slices.Sorted(maps.Keys(flagProviders))

// flagOwnerAliases renders the providers that accept one flag, for
// refusal messages.
func flagOwnerAliases(name string) string {
	owners := flagProviders[name]
	aliases := make([]string, len(owners))
	for i, p := range owners {
		aliases[i] = captcha.ProviderAlias(p)
	}
	return strings.Join(aliases, " and ")
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
	f.expires = fs.Duration("expires", 0, "ALTCHA challenge expiry (e.g. 20m; whole seconds; 0 = the 20m default)")
	f.siteKey = fs.String("site-key", "", "external provider's public site key (recaptcha_v3, turnstile)")
	f.secret = fs.String("secret", "", "external provider's secret key; required when configuring a provider for the first time")
	f.minScore = fs.Float64("min-score", 0, fmt.Sprintf("reCAPTCHA v3 minimum score, 0-1 (0 = the %g default)", captcha.DefaultRecaptchaV3Settings().MinScore))
}

func (f *captchaSetFlags) set(name string) bool {
	return explicitlySet(f.flags)[name]
}

func runCaptchaSet(cmd adminCmdCtx, args []string) error {
	var cf captchaSetFlags
	return withAdminStore(cmd, args, cf.declare, func(ctx context.Context, cfg *config.Config, st store.Store) error {
		if err := requireAnySet(explicitlySet(cf.flags), append([]string{"provider"}, captchaFlagNames...)...); err != nil {
			return err
		}
		if cf.set("secret") && *cf.secret == "" {
			return fmt.Errorf("--secret must not be empty; an empty secret fails every verification")
		}

		mgr, ks, err := captchaManagerFor(cfg, st)
		if err != nil {
			return err
		}
		current, _, err := mgr.Describe(ctx)
		if err != nil {
			return fmt.Errorf("load captcha config: %w", err)
		}

		// The target provider is the --provider flag, else the currently
		// selected one. Specifying the already-selected provider tunes it
		// in place; only a different provider switches.
		target := current.Provider
		if cf.set("provider") {
			target, err = captcha.ParseProvider(*cf.provider)
			if err != nil {
				return fmt.Errorf("invalid captcha configuration: %w", err)
			}
		}
		switching := target != current.Provider

		// Provider-foreign flags are refused rather than ignored: a cost
		// meant for ALTCHA must not be silently dropped under Turnstile,
		// and a site key meant for Turnstile must not vanish under ALTCHA.
		// The scope comes from the flagProviders table.
		for _, name := range captchaFlagNames {
			if !cf.set(name) || slices.Contains(flagProviders[name], target) {
				continue
			}
			return fmt.Errorf("--%s applies only to %s; the target provider is %s", name, flagOwnerAliases(name), captcha.ProviderAlias(target))
		}

		// Flag edits overlay the TARGET provider's own stored settings, so
		// a switch back keeps that row's tuning — only the selection
		// changes. A provider with no row starts from its defaults.
		base, hasRow, err := captcha.DescribeProvider(ctx, st, target)
		if err != nil {
			return fmt.Errorf("load captcha config: %w", err)
		}

		// Switching to an external provider that has never been configured
		// requires its keys in the same invocation: the row cannot exist
		// without them. A stored, complete row needs nothing — the switch
		// alone activates it.
		if switching && target != captcha.ProviderAltcha && base.SiteKey() == "" {
			if !cf.set("site-key") || !cf.set("secret") {
				return fmt.Errorf("configuring %s requires --site-key and --secret in the same invocation (its row has no stored keys)", captcha.ProviderAlias(target))
			}
		}

		settingsJSON, summary, err := resolveCaptchaSettings(target, &cf, base)
		if err != nil {
			return err
		}

		var secretBytes []byte
		if cf.set("secret") {
			secretBytes, err = captcha.EncryptSecret(ks, target, *cf.secret)
			if err != nil {
				return err
			}
		}

		cs := st.CaptchaConfig()
		if switching {
			if target == captcha.ProviderAltcha {
				// Reuse the altcha row (and its original HMAC secret) when
				// one exists; otherwise provision it now.
				if err := captcha.EnsureAltchaRow(ctx, st, ks); err != nil {
					return fmt.Errorf("provision altcha row: %w", err)
				}
			}
		} else if target == captcha.ProviderAltcha && !hasRow {
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

		fmt.Println(summary)
		if switching {
			fmt.Printf("Activated provider %s (verification is enabled; changes reach a running hub within ~30s)\n", captcha.ProviderAlias(target))
		} else {
			fmt.Println("Updated captcha configuration (changes reach a running hub within ~30s)")
		}
		return nil
	})
}

// resolveCaptchaSettings builds the target provider's settings JSON and
// the human summary for one `captcha set` invocation: apply the flags
// onto the target row's stored settings, marshal, and describe. The
// switch is exhaustive over the providerSpec registry's closed set —
// target comes from ParseProvider or Manager.Describe, both of which
// refuse unsupported providers — so the trailing return is unreachable
// defense.
func resolveCaptchaSettings(target captcha.Provider, cf *captchaSetFlags, base captcha.Config) (string, string, error) {
	switch target {
	case captcha.ProviderAltcha:
		settings, err := buildAltchaSettings(cf, base.Altcha)
		return finalizeCaptchaSettings(settings, err,
			func(s captcha.AltchaSettings) string {
				return fmt.Sprintf("Saved altcha settings (algorithm %s, cost %d, expiry %s)",
					s.Algorithm, s.Cost, time.Duration(s.ChallengeExpirySeconds)*time.Second)
			})
	case captcha.ProviderRecaptchaV3:
		settings, err := buildRecaptchaSettings(cf, base.RecaptchaV3)
		return finalizeCaptchaSettings(settings, err,
			func(s captcha.RecaptchaV3Settings) string {
				return fmt.Sprintf("Saved recaptcha_v3 settings (site key %s, min score %g)", s.SiteKey, s.MinScore)
			})
	case captcha.ProviderTurnstile:
		settings, err := buildTurnstileSettings(cf, base.Turnstile)
		return finalizeCaptchaSettings(settings, err,
			func(s captcha.TurnstileSettings) string {
				return fmt.Sprintf("Saved turnstile settings (site key %s)", s.SiteKey)
			})
	}
	return "", "", fmt.Errorf("unsupported captcha provider %s", captcha.ProviderAlias(target))
}

// finalizeCaptchaSettings is the shared tail of every provider leg: wrap
// a settings-builder error, marshal the settings, and render the human
// summary — so each leg states only its builder and its summary line.
func finalizeCaptchaSettings[T any](settings T, err error, summarize func(T) string) (string, string, error) {
	if err != nil {
		return "", "", fmt.Errorf("invalid captcha configuration: %w", err)
	}
	settingsJSON, err := marshalSettings(settings)
	if err != nil {
		return "", "", err
	}
	return settingsJSON, summarize(settings), nil
}

// buildAltchaSettings applies the altcha flags onto the target row's
// stored ALTCHA settings (the caller already based them on the target
// row, never on another provider's config). An algorithm switch resets
// only the family-specific parameters to the new family's defaults:
// carrying the old family's values would silently reinterpret them in the
// new family's units — an ARGON2ID memory in KiB would become SCRYPT's
// block multiplier — while the algorithm-independent expiry keeps its
// stored value.
func buildAltchaSettings(cf *captchaSetFlags, stored *captcha.AltchaSettings) (captcha.AltchaSettings, error) {
	next := captcha.DefaultAltchaSettings()
	if stored != nil {
		next = *stored
	}
	var family captcha.AltchaSettings
	if cf.set("algorithm") {
		var err error
		family, err = captcha.DefaultAltchaSettingsFor(*cf.algorithm)
		if err != nil {
			return next, err
		}
		next.Algorithm = family.Algorithm
		next.Cost = family.Cost
		next.MemoryCost = family.MemoryCost
		next.Parallelism = family.Parallelism
	} else {
		// The algorithm is the stored (or default) one, already validated;
		// the lookup below only reads its family defaults.
		validated, err := captcha.DefaultAltchaSettingsFor(next.Algorithm)
		if err != nil {
			return next, err
		}
		family = validated
	}
	// An explicit 0 restores the algorithm family's default for that
	// parameter (the derive funcs substitute their own defaults for zero
	// values, and DefaultAltchaSettingsFor documents them).
	if cf.set("cost") {
		next.Cost = *cf.cost
		if next.Cost == 0 {
			next.Cost = family.Cost
		}
	}
	if cf.set("memory-cost") {
		next.MemoryCost = *cf.memoryCost
		if next.MemoryCost == 0 {
			next.MemoryCost = family.MemoryCost
		}
	}
	if cf.set("parallelism") {
		next.Parallelism = *cf.parallel
		if next.Parallelism == 0 {
			next.Parallelism = family.Parallelism
		}
	}
	if cf.set("expires") {
		if *cf.expires == 0 {
			next.ChallengeExpirySeconds = captcha.DefaultAltchaSettings().ChallengeExpirySeconds
		} else {
			secs, err := wholeSeconds(*cf.expires)
			if err != nil {
				return next, fmt.Errorf("invalid --expires: %w", err)
			}
			next.ChallengeExpirySeconds = secs
		}
	}
	if err := next.Validate(); err != nil {
		return next, err
	}
	return next, nil
}

// buildRecaptchaSettings applies the recaptcha_v3 flags onto the target
// row's stored settings. An explicit --min-score 0 restores the package
// default, whatever that default is.
func buildRecaptchaSettings(cf *captchaSetFlags, stored *captcha.RecaptchaV3Settings) (captcha.RecaptchaV3Settings, error) {
	next := captcha.DefaultRecaptchaV3Settings()
	if stored != nil {
		next = *stored
	}
	if cf.set("site-key") {
		next.SiteKey = *cf.siteKey
	}
	if cf.set("min-score") {
		next.MinScore = *cf.minScore
		if next.MinScore == 0 {
			next.MinScore = captcha.DefaultRecaptchaV3Settings().MinScore
		}
	}
	if err := next.Validate(); err != nil {
		return next, err
	}
	return next, nil
}

func buildTurnstileSettings(cf *captchaSetFlags, stored *captcha.TurnstileSettings) (captcha.TurnstileSettings, error) {
	next := captcha.DefaultTurnstileSettings()
	if stored != nil {
		next = *stored
	}
	if cf.set("site-key") {
		next.SiteKey = *cf.siteKey
	}
	if err := next.Validate(); err != nil {
		return next, err
	}
	return next, nil
}

func marshalSettings(settings any) (string, error) {
	b, err := json.Marshal(settings)
	if err != nil {
		return "", fmt.Errorf("marshal captcha settings: %w", err)
	}
	return string(b), nil
}

func runCaptchaSetEnabled(cmd adminCmdCtx, args []string, enabled bool) error {
	return withAdminStore(cmd, args, nil, func(ctx context.Context, cfg *config.Config, st store.Store) error {
		mgr, _, err := captchaManagerFor(cfg, st)
		if err != nil {
			return err
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
		mgr, _, err := captchaManagerFor(cfg, st)
		if err != nil {
			return err
		}
		if explicitlySet(flags)["provider"] {
			p, err := captcha.ParseProvider(*provider)
			if err != nil {
				return fmt.Errorf("invalid captcha configuration: %w", err)
			}
			current, _, err := mgr.Describe(ctx)
			if err != nil {
				return fmt.Errorf("load captcha config: %w", err)
			}
			if err := st.CaptchaConfig().DeleteProvider(ctx, p); err != nil {
				return fmt.Errorf("reset captcha config: %w", err)
			}
			if current.Provider == p {
				// Deleting the selected row leaves nothing selected, so
				// re-provision the default altcha row here — not on the
				// hub's next use. The request path must never write: a
				// read-only or locked store would deny the first Login
				// with the uniform captcha error. Same contract as
				// startup provisioning and every other admin command.
				if err := mgr.EnsureProvisioned(ctx); err != nil {
					return fmt.Errorf("provision captcha config: %w", err)
				}
				fmt.Printf("Reset the %s provider's configuration (it was selected; altcha, the default provider, is active again)\n", captcha.ProviderAlias(p))
			} else {
				fmt.Printf("Reset the %s provider's configuration (re-create it with `captcha set --provider %s`)\n", captcha.ProviderAlias(p), captcha.ProviderAlias(p))
			}
			return nil
		}
		if err := st.CaptchaConfig().Delete(ctx); err != nil {
			return fmt.Errorf("reset captcha config: %w", err)
		}
		// Re-provision rather than leaving the self-heal to the hub's
		// next use, for the same read-only-request-path contract.
		if err := mgr.EnsureProvisioned(ctx); err != nil {
			return fmt.Errorf("provision captcha config: %w", err)
		}
		fmt.Println("Reset captcha configuration to defaults (the altcha signing secret was regenerated)")
		return nil
	})
}
