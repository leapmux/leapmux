package main

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/hub/captcha"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// captchaEffectiveJSON is the admin `captcha show` payload: the effective
// settings (embedded, so the JSON shape stays locked to the one Config
// definition) plus whether they come from a stored row or built-in
// defaults.
type captchaEffectiveJSON struct {
	captcha.Config
	Customized bool `json:"customized"`
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
		return printJSON(captchaEffectiveJSON{Config: effective, Customized: customized})
	})
}

func runCaptchaSet(cmd adminCmdCtx, args []string) error {
	var algorithm *string
	var cost *int64
	var memoryCost *int64
	var parallelism *int64
	var expires *time.Duration
	var flags *flag.FlagSet
	return withAdminStore(cmd, args, func(fs *flag.FlagSet) {
		flags = fs
		algorithm = fs.String("algorithm", "", fmt.Sprintf("ALTCHA algorithm (%s)", strings.Join(captcha.SupportedAlgorithms(), ", ")))
		cost = fs.Int64("cost", 0, "per-derivation cost (PBKDF2/SHA iterations, SCRYPT N, or ARGON2ID time parameter; 0 = algorithm default)")
		memoryCost = fs.Int64("memory-cost", 0, "SCRYPT r (block-count multiplier, NOT bytes) or ARGON2ID m (KiB); 0 = algorithm default")
		parallelism = fs.Int64("parallelism", 0, "SCRYPT p or ARGON2ID threads; 0 = algorithm default")
		expires = fs.Duration("expires", 0, "challenge expiry (e.g. 20m; whole seconds)")
	}, func(ctx context.Context, cfg *config.Config, st store.Store) error {
		set := explicitlySet(flags)
		if !set["algorithm"] && !set["cost"] && !set["memory-cost"] && !set["parallelism"] && !set["expires"] {
			return fmt.Errorf("at least one of --algorithm, --cost, --memory-cost, --parallelism, --expires is required")
		}

		mgr, err := captchaManagerFor(cfg, st)
		if err != nil {
			return err
		}
		next, _, err := mgr.Describe(ctx)
		if err != nil {
			return fmt.Errorf("load captcha config: %w", err)
		}

		// A family switch resets the family-specific parameters to the new
		// family's defaults (unless passed on this command line): carrying
		// the old family's values would silently reinterpret them in the
		// new family's units — an ARGON2ID memory in KiB would become
		// SCRYPT's block multiplier.
		if set["algorithm"] {
			family, err := captcha.FamilyDefaults(*algorithm)
			if err != nil {
				return fmt.Errorf("invalid captcha configuration: %w", err)
			}
			next.Algorithm = family.Algorithm
			next.Cost, next.MemoryCost, next.Parallelism = family.Cost, family.MemoryCost, family.Parallelism
		}
		// An explicit 0 restores the algorithm's default for that
		// parameter (the derive funcs substitute their own defaults for
		// zero values, and FamilyDefaults documents them).
		if set["cost"] {
			next.Cost = *cost
			if next.Cost == 0 {
				next.Cost = familyParam(next.Algorithm).Cost
			}
		}
		if set["memory-cost"] {
			next.MemoryCost = *memoryCost
			if next.MemoryCost == 0 {
				next.MemoryCost = familyParam(next.Algorithm).MemoryCost
			}
		}
		if set["parallelism"] {
			next.Parallelism = *parallelism
			if next.Parallelism == 0 {
				next.Parallelism = familyParam(next.Algorithm).Parallelism
			}
		}
		if set["expires"] {
			secs, err := wholeSeconds(*expires)
			if err != nil {
				return fmt.Errorf("invalid --expires: %w", err)
			}
			next.ChallengeExpirySeconds = secs
		}
		if err := next.Validate(); err != nil {
			return fmt.Errorf("invalid captcha configuration: %w", err)
		}

		if err := mgr.EnsureProvisioned(ctx); err != nil {
			return fmt.Errorf("provision captcha config: %w", err)
		}
		if err := storeCaptchaConfig(ctx, st, next); err != nil {
			return err
		}
		fmt.Printf("Updated captcha configuration (algorithm %s, cost %d, expiry %s)\n",
			next.Algorithm, next.Cost, time.Duration(next.ChallengeExpirySeconds)*time.Second)
		return nil
	})
}

// familyParam returns the algorithm family's default parameters; the
// algorithm is known because Validate or FamilyDefaults already accepted
// it.
func familyParam(algorithm string) captcha.Config {
	family, err := captcha.FamilyDefaults(algorithm)
	if err != nil {
		return captcha.DefaultConfig()
	}
	return family
}

func runCaptchaSetEnabled(cmd adminCmdCtx, args []string, enabled bool) error {
	return withAdminStore(cmd, args, nil, func(ctx context.Context, cfg *config.Config, st store.Store) error {
		mgr, err := captchaManagerFor(cfg, st)
		if err != nil {
			return err
		}
		current, _, err := mgr.Describe(ctx)
		if err != nil {
			return fmt.Errorf("load captcha config: %w", err)
		}
		current.Enabled = enabled

		if err := mgr.EnsureProvisioned(ctx); err != nil {
			return fmt.Errorf("provision captcha config: %w", err)
		}
		if err := storeCaptchaConfig(ctx, st, current); err != nil {
			return err
		}
		fmt.Printf("%s captcha verification (the honeypot check stays active either way)\n", enabledWord(enabled))
		return nil
	})
}

func runCaptchaReset(cmd adminCmdCtx, args []string) error {
	return withAdminStore(cmd, args, nil, func(ctx context.Context, _ *config.Config, st store.Store) error {
		if err := st.CaptchaConfig().Delete(ctx); err != nil {
			return fmt.Errorf("reset captcha config: %w", err)
		}
		fmt.Println("Reset captcha configuration to defaults (the signing secret is regenerated on next hub use)")
		return nil
	})
}

// storeCaptchaConfig writes the configuration columns of the singleton
// row. The signing secret is deliberately absent: provisioning is its only
// writer, so no configuration change can lose or corrupt the key.
func storeCaptchaConfig(ctx context.Context, st store.Store, cfg captcha.Config) error {
	if err := st.CaptchaConfig().Update(ctx, store.UpdateCaptchaConfigParams{
		Enabled:                cfg.Enabled,
		Algorithm:              cfg.Algorithm,
		Cost:                   cfg.Cost,
		MemoryCost:             cfg.MemoryCost,
		Parallelism:            cfg.Parallelism,
		ChallengeExpirySeconds: cfg.ChallengeExpirySeconds,
	}); err != nil {
		return fmt.Errorf("update captcha config: %w", err)
	}
	return nil
}
