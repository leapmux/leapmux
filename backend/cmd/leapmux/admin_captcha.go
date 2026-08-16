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
	"github.com/leapmux/leapmux/internal/hub/settings"
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

// captchaCommandDeps bundles what a captcha admin command needs: the
// shared settings manager (reads and writes) and the captcha manager
// built over it (provisioning, Describe semantics shared with the hub).
type captchaCommandDeps struct {
	set *settings.Manager
	cap *captcha.Manager
}

// captchaDepsFor loads the settings manager exactly as the hub builds
// it, then the captcha manager over it.
func captchaDepsFor(cfg *config.Config, st store.Store) (captchaCommandDeps, error) {
	set, err := settingsManagerFor(cfg, st)
	if err != nil {
		return captchaCommandDeps{}, err
	}
	return captchaCommandDeps{set: set, cap: captcha.NewManager(st, set, false)}, nil
}

func runCaptchaShow(cmd adminCmdCtx, args []string) error {
	return withAdminStore(cmd, args, nil, func(ctx context.Context, cfg *config.Config, st store.Store) error {
		d, err := captchaDepsFor(cfg, st)
		if err != nil {
			return err
		}
		snap := d.set.Snapshot(ctx)
		effective, customized := captcha.Effective(snap), snap.Customized(captcha.AltchaKey)
		if sel, pErr := captcha.ParseProvider(captcha.CaptchaSelectedKey.Of(snap)); pErr == nil {
			customized = snap.Customized(captchaProviderDescriptor(sel))
		}
		configured := []string{}
		for _, p := range []captcha.Provider{captcha.ProviderAltcha, captcha.ProviderRecaptchaV3, captcha.ProviderTurnstile} {
			if snap.Customized(captchaProviderDescriptor(p)) {
				configured = append(configured, captcha.ProviderAlias(p))
			}
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

		d, err := captchaDepsFor(cfg, st)
		if err != nil {
			return err
		}
		snap := d.set.Snapshot(ctx)
		current := captcha.Effective(snap)

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
		base, hasRow := captcha.DescribeProvider(snap, target)

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

		// Writes go through the settings manager: the public half of the
		// target provider's key (its stored secret half is merged and
		// re-split, so it is never lost), the secret half only when a
		// --secret flag rotates it, and the selection as its own scalar
		// when switching.
		if err := d.set.Update(ctx, captchaProviderDescriptor(target), json.RawMessage(settingsJSON)); err != nil {
			return fmt.Errorf("update captcha config: %w", err)
		}
		if cf.set("secret") {
			secretDoc, mErr := json.Marshal(map[string]string{"secret_key": *cf.secret})
			if mErr != nil {
				return mErr
			}
			if err := d.set.UpdateSecret(ctx, captchaProviderDescriptor(target), secretDoc); err != nil {
				return fmt.Errorf("update captcha secret: %w", err)
			}
		}
		if switching {
			// Switching TO altcha must leave an altcha row with a signing
			// secret behind (reusing the original one when it exists) —
			// the request path never writes.
			if target == captcha.ProviderAltcha && !snap.Customized(captcha.AltchaKey) {
				if err := d.cap.EnsureProvisioned(ctx); err != nil {
					return fmt.Errorf("provision altcha row: %w", err)
				}
			}
			selDoc, mErr := json.Marshal(captcha.ProviderAlias(target))
			if mErr != nil {
				return mErr
			}
			if err := d.set.Update(ctx, captcha.CaptchaSelectedKey, selDoc); err != nil {
				return fmt.Errorf("activate captcha provider: %w", err)
			}
		}
		_ = hasRow

		fmt.Println(summary)
		if switching {
			fmt.Printf("Activated provider %s (verification is enabled; changes reach a running hub within ~30s)\n", captcha.ProviderAlias(target))
		} else {
			fmt.Println("Updated captcha configuration (changes reach a running hub within ~30s)")
		}
		return nil
	})
}

// captchaProviderDescriptor returns the settings key holding one
// provider's document (the exported face of the captcha package's own
// mapping).
func captchaProviderDescriptor(p captcha.Provider) settings.Descriptor {
	switch p {
	case captcha.ProviderRecaptchaV3:
		return captcha.RecaptchaV3Key
	case captcha.ProviderTurnstile:
		return captcha.TurnstileKey
	default:
		return captcha.AltchaKey
	}
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
		d, err := captchaDepsFor(cfg, st)
		if err != nil {
			return err
		}
		if err := d.cap.EnsureProvisioned(ctx); err != nil {
			return fmt.Errorf("provision captcha config: %w", err)
		}
		doc, err := json.Marshal(enabled)
		if err != nil {
			return err
		}
		if err := d.set.Update(ctx, captcha.CaptchaEnabledKey, doc); err != nil {
			return fmt.Errorf("update captcha config: %w", err)
		}
		fmt.Printf("%s captcha verification (the honeypot check stays active either way; the selected provider is remembered)\n", enabledWord(enabled))
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
		d, err := captchaDepsFor(cfg, st)
		if err != nil {
			return err
		}
		if explicitlySet(flags)["provider"] {
			p, err := captcha.ParseProvider(*provider)
			if err != nil {
				return fmt.Errorf("invalid captcha configuration: %w", err)
			}
			snap := d.set.Snapshot(ctx)
			selected := captcha.Effective(snap).Provider
			if err := d.set.Reset(ctx, captchaProviderDescriptor(p)); err != nil {
				return fmt.Errorf("reset captcha config: %w", err)
			}
			if selected == p && p != captcha.ProviderAltcha {
				// Resetting the selected external provider falls back to
				// the default selection (altcha).
				if err := d.set.Reset(ctx, captcha.CaptchaSelectedKey); err != nil {
					return fmt.Errorf("reset captcha selection: %w", err)
				}
			}
			if p == captcha.ProviderAltcha {
				// The request path must never write: re-provision the
				// signing secret here rather than on the hub's next use.
				if err := d.cap.EnsureProvisioned(ctx); err != nil {
					return fmt.Errorf("provision captcha config: %w", err)
				}
			}
			fmt.Printf("Reset the %s provider's configuration\n", captcha.ProviderAlias(p))
			return nil
		}
		for _, desc := range captcha.SettingsDescriptors() {
			if err := d.set.Reset(ctx, desc); err != nil {
				return fmt.Errorf("reset captcha config: %w", err)
			}
		}
		// Re-provision rather than leaving the self-heal to the hub's
		// next use, for the same read-only-request-path contract.
		if err := d.cap.EnsureProvisioned(ctx); err != nil {
			return fmt.Errorf("provision captcha config: %w", err)
		}
		fmt.Println("Reset captcha configuration to defaults (the altcha signing secret was regenerated)")
		return nil
	})
}
