package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
)

func runDBPath(cmd adminCmdCtx, args []string) error {
	return withAdminConfig(cmd, args, nil, func(cfg *config.Config) error {
		fmt.Println(cfg.SQLiteDBPath())
		return nil
	})
}

func printSchemaVersions(ctx context.Context, m store.Migrator) (current, latest int64, err error) {
	current, err = m.CurrentVersion(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("get current version: %w", err)
	}
	latest = m.LatestVersion()
	fmt.Printf("Current schema version: %d\n", current)
	fmt.Printf("Latest available version: %d\n", latest)
	return current, latest, nil
}

func runDBMigrate(cmd adminCmdCtx, args []string) error {
	var targetVersion *int64
	return withAdminStore(cmd, args, func(fs *flag.FlagSet) {
		targetVersion = fs.Int64("version", -1, "target migration version (-1 for latest)")
	}, func(ctx context.Context, _ *config.Config, st store.Store) error {
		m := st.Migrator()

		current, latest, err := printSchemaVersions(ctx, m)
		if err != nil {
			return err
		}

		if *targetVersion >= 0 {
			fmt.Printf("Migrating to version %d...\n", *targetVersion)
			if err := m.MigrateTo(ctx, *targetVersion); err != nil {
				return fmt.Errorf("migrate to version %d: %w", *targetVersion, err)
			}
		} else {
			// Opening the store already applies pending migrations to the
			// latest version. When no explicit target is given, confirm the
			// schema is up to date.
			if current == latest {
				fmt.Println("Already at latest version.")
				return nil
			}
			fmt.Printf("Migrating to latest version %d...\n", latest)
			if err := m.Migrate(ctx); err != nil {
				return fmt.Errorf("migrate: %w", err)
			}
		}

		newVersion, err := m.CurrentVersion(ctx)
		if err != nil {
			return fmt.Errorf("get new version: %w", err)
		}
		fmt.Printf("Migration complete. Current version: %d\n", newVersion)
		return nil
	})
}

func runDBVersion(cmd adminCmdCtx, args []string) error {
	return withAdminStore(cmd, args, nil,
		func(ctx context.Context, _ *config.Config, st store.Store) error {
			_, _, err := printSchemaVersions(ctx, st.Migrator())
			return err
		})
}
