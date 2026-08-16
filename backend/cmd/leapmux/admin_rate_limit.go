package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/ratelimit"
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
// catalogue.
func parseOperation(name string) (ratelimit.Operation, error) {
	if name == "" {
		return "", fmt.Errorf("--operation is required (known operations: %s)", knownOperationsSuffix())
	}
	op := ratelimit.Operation(name)
	if _, known := ratelimit.DefaultLimits(op); !known {
		return "", fmt.Errorf("unknown operation %q (known operations: %s)", name, knownOperationsSuffix())
	}
	return op, nil
}

func runRateLimitList(cmd adminCmdCtx, args []string) error {
	return withAdminStore(cmd, args, nil, func(ctx context.Context, _ *config.Config, st store.Store) error {
		rows, err := st.RateLimitConfig().List(ctx)
		if err != nil {
			return fmt.Errorf("list rate-limit config: %w", err)
		}
		stored := make(map[string]store.RateLimitConfig, len(rows))
		for _, row := range rows {
			stored[row.Operation] = row
		}

		fmt.Printf("%-20s %-8s %-12s %-10s %s\n", "OPERATION", "ENABLED", "MAX-ATTEMPTS", "WINDOW", "SOURCE")
		for _, op := range ratelimit.KnownOperations() {
			var row *store.RateLimitConfig
			if r, exists := stored[string(op)]; exists {
				row = &r
			}
			enabled, limits := ratelimit.EffectiveLimits(op, row)
			source := "default"
			if row != nil {
				source = "customized"
			}
			fmt.Printf("%-20s %-8s %-12d %-10s %s\n",
				op, yesNo(enabled), limits.MaxAttempts, time.Duration(limits.WindowSeconds)*time.Second, source)
		}
		return nil
	})
}

// loadRateLimitRow returns the stored row for an operation, or nil when
// none exists (defaults apply).
func loadRateLimitRow(ctx context.Context, st store.Store, op ratelimit.Operation) (*store.RateLimitConfig, error) {
	row, err := st.RateLimitConfig().Get(ctx, string(op))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("load rate-limit config: %w", err)
	}
	return row, nil
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
	}, func(ctx context.Context, _ *config.Config, st store.Store) error {
		op, err := parseOperation(*operation)
		if err != nil {
			return err
		}
		set := explicitlySet(flags)
		if err := requireAnySet(set, "max-attempts", "window"); err != nil {
			return err
		}

		// Overlay the request onto the current effective limits; an
		// explicit 0 restores the default for that field.
		row, err := loadRateLimitRow(ctx, st, op)
		if err != nil {
			return err
		}
		enabled, limits := ratelimit.EffectiveLimits(op, row)
		def, _ := ratelimit.DefaultLimits(op)
		if set["max-attempts"] {
			limits.MaxAttempts = *maxAttempts
			if limits.MaxAttempts == 0 {
				limits.MaxAttempts = def.MaxAttempts
			}
		}
		if set["window"] {
			secs, err := wholeSeconds(*window)
			if err != nil {
				return fmt.Errorf("invalid --window: %w", err)
			}
			limits.WindowSeconds = secs
			if limits.WindowSeconds == 0 {
				limits.WindowSeconds = def.WindowSeconds
			}
		}
		if err := ratelimit.ValidateLimits(limits); err != nil {
			return fmt.Errorf("invalid rate limit: %w", err)
		}

		if err := st.RateLimitConfig().Upsert(ctx, store.UpsertRateLimitConfigParams{
			Operation:     string(op),
			Enabled:       enabled,
			MaxAttempts:   limits.MaxAttempts,
			WindowSeconds: limits.WindowSeconds,
		}); err != nil {
			return fmt.Errorf("update rate limit: %w", err)
		}
		fmt.Printf("Updated rate limit for %s (%d attempts per %s)\n",
			op, limits.MaxAttempts, time.Duration(limits.WindowSeconds)*time.Second)
		return nil
	})
}

func runRateLimitSetEnabled(cmd adminCmdCtx, args []string, enabled bool) error {
	var operation *string
	return withAdminStore(cmd, args, func(fs *flag.FlagSet) {
		operation = fs.String("operation", "", "operation to toggle (e.g. change-password)")
	}, func(ctx context.Context, _ *config.Config, st store.Store) error {
		op, err := parseOperation(*operation)
		if err != nil {
			return err
		}

		row, err := loadRateLimitRow(ctx, st, op)
		if err != nil {
			return err
		}
		_, limits := ratelimit.EffectiveLimits(op, row)

		if err := st.RateLimitConfig().Upsert(ctx, store.UpsertRateLimitConfigParams{
			Operation:     string(op),
			Enabled:       enabled,
			MaxAttempts:   limits.MaxAttempts,
			WindowSeconds: limits.WindowSeconds,
		}); err != nil {
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
	}, func(ctx context.Context, _ *config.Config, st store.Store) error {
		op, err := parseOperation(*operation)
		if err != nil {
			return err
		}
		if err := st.RateLimitConfig().Delete(ctx, string(op)); err != nil {
			return fmt.Errorf("reset rate limit: %w", err)
		}
		fmt.Printf("Reset rate limit for %s to defaults\n", op)
		return nil
	})
}
