package main

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/ratelimit"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// knownOperationsSuffix renders the catalogue for error messages, derived
// from KnownOperations so it can never contradict the package's list.
func knownOperationsSuffix() string {
	ops := ratelimit.KnownOperations()
	names := make([]string, len(ops))
	for i, op := range ops {
		names[i] = string(op)
	}
	return strings.Join(names, ", ")
}

// parseOperation resolves and validates the --operation flag against the
// catalogue. LimitKey is the single known-operation check: it derives
// from the same catalogue loop as every other per-operation lookup, so
// the CLI's "known" answer cannot drift from the keys it then reads.
func parseOperation(name string) (ratelimit.Operation, error) {
	if name == "" {
		return "", fmt.Errorf("--operation is required (known operations: %s)", knownOperationsSuffix())
	}
	op := ratelimit.Operation(name)
	if _, known := ratelimit.LimitKey(op); !known {
		return "", fmt.Errorf("unknown operation %q (known operations: %s)", name, knownOperationsSuffix())
	}
	return op, nil
}

// rateLimitKey loads the settings manager and resolves the operation's
// settings key — the prologue the set/set-enabled/reset verbs share.
func rateLimitKey(ctx context.Context, cfg *config.Config, st store.Store, op ratelimit.Operation) (*settings.Manager, *settings.Key[ratelimit.LimitValue], error) {
	m, err := settingsManagerFor(cfg, st)
	if err != nil {
		return nil, nil, err
	}
	key, _ := ratelimit.LimitKey(op)
	return m, key, nil
}

func runRateLimitList(cmd adminCmdCtx, args []string) error {
	return withAdminStore(cmd, args, nil, func(ctx context.Context, cfg *config.Config, st store.Store) error {
		m, err := settingsManagerFor(cfg, st)
		if err != nil {
			return err
		}
		snap := m.Snapshot(ctx)

		fmt.Printf("%-20s %-8s %-12s %-10s %s\n", "OPERATION", "ENABLED", "MAX-ATTEMPTS", "WINDOW", "SOURCE")
		for _, op := range ratelimit.KnownOperations() {
			key, _ := ratelimit.LimitKey(op)
			v := key.Of(snap)
			source := "default"
			if snap.Customized(key) {
				source = "customized"
			}
			fmt.Printf("%-20s %-8s %-12d %-10s %s\n",
				op, yesNo(v.Enabled), v.MaxAttempts, time.Duration(v.WindowSeconds)*time.Second, source)
		}
		return nil
	})
}

func runRateLimitSet(cmd adminCmdCtx, args []string) error {
	var operation *string
	var maxAttempts *int64
	var window *time.Duration
	var flags *flag.FlagSet
	return withAdminStore(cmd, args, func(fs *flag.FlagSet) {
		flags = fs
		operation = fs.String("operation", "", "operation to configure (e.g. change-password)")
		maxAttempts = fs.Int64("max-attempts", 0, "allowed failed attempts per window (0 = default)")
		window = fs.Duration("window", 0, "fixed window length (e.g. 15m; whole seconds)")
	}, func(ctx context.Context, cfg *config.Config, st store.Store) error {
		op, err := parseOperation(*operation)
		if err != nil {
			return err
		}
		set := explicitlySet(flags)
		if err := requireAnySet(set, "max-attempts", "window"); err != nil {
			return err
		}

		m, key, err := rateLimitKey(ctx, cfg, st, op)
		if err != nil {
			return err
		}

		// Overlay the request onto the current effective limits; an
		// explicit 0 restores the default for that field. The current
		// value needs no zero-filling: the key's declared default already
		// answers an absent or zeroed row.
		def, _ := ratelimit.DefaultLimits(op)
		current := key.Of(m.Snapshot(ctx))
		v := ratelimit.LimitValue{
			Enabled:       current.Enabled,
			MaxAttempts:   current.MaxAttempts,
			WindowSeconds: current.WindowSeconds,
		}
		if set["max-attempts"] {
			v.MaxAttempts = *maxAttempts
			if v.MaxAttempts == 0 {
				v.MaxAttempts = def.MaxAttempts
			}
		}
		if set["window"] {
			secs, err := wholeSeconds(*window)
			if err != nil {
				return fmt.Errorf("invalid --window: %w", err)
			}
			v.WindowSeconds = secs
			if v.WindowSeconds == 0 {
				v.WindowSeconds = def.WindowSeconds
			}
		}
		if err := ratelimit.ValidateLimits(ratelimit.Limits{
			MaxAttempts: v.MaxAttempts, WindowSeconds: v.WindowSeconds,
		}); err != nil {
			return fmt.Errorf("invalid rate limit: %w", err)
		}

		if err := key.Set(ctx, m, v); err != nil {
			return fmt.Errorf("update rate limit: %w", err)
		}
		fmt.Printf("Updated rate limit for %s (%d attempts per %s)\n",
			op, v.MaxAttempts, time.Duration(v.WindowSeconds)*time.Second)
		return nil
	})
}

func runRateLimitSetEnabled(cmd adminCmdCtx, args []string, enabled bool) error {
	var operation *string
	return withAdminStore(cmd, args, func(fs *flag.FlagSet) {
		operation = fs.String("operation", "", "operation to toggle (e.g. change-password)")
	}, func(ctx context.Context, cfg *config.Config, st store.Store) error {
		op, err := parseOperation(*operation)
		if err != nil {
			return err
		}

		m, key, err := rateLimitKey(ctx, cfg, st, op)
		if err != nil {
			return err
		}
		v := key.Of(m.Snapshot(ctx))
		v.Enabled = enabled
		if err := key.Set(ctx, m, v); err != nil {
			return fmt.Errorf("update rate limit: %w", err)
		}
		fmt.Printf("%s rate limiting for %s\n", enabledWord(enabled), op)
		return nil
	})
}

func runRateLimitReset(cmd adminCmdCtx, args []string) error {
	var operation *string
	return withAdminStore(cmd, args, func(fs *flag.FlagSet) {
		operation = fs.String("operation", "", "operation to reset (e.g. change-password)")
	}, func(ctx context.Context, cfg *config.Config, st store.Store) error {
		op, err := parseOperation(*operation)
		if err != nil {
			return err
		}
		m, key, err := rateLimitKey(ctx, cfg, st, op)
		if err != nil {
			return err
		}
		if err := m.Reset(ctx, key); err != nil {
			return fmt.Errorf("reset rate limit: %w", err)
		}
		fmt.Printf("Reset rate limit for %s to defaults\n", op)
		return nil
	})
}
