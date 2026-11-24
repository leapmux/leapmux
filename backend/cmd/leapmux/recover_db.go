package main

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
)

func newDBPathCommand() *cobra.Command {
	return recoverConfigLeaf(recoverSpec{Leaf: cli.Leaf{
		Use:   "path",
		Short: "Print the database path",
		Long:  "Print the path of the SQLite database of the Hub. A Hub on PostgreSQL or MySQL keeps its data on that server instead.",
		Examples: []cli.Example{
			{Command: "leapmux recover db path"},
		},
	}}, func(inv *cli.Invocation, cfg *config.Config) error {
		_, err := fmt.Fprintln(inv.Printer.Out, cfg.SQLiteDBPath())
		return err
	})
}

func printSchemaVersions(ctx context.Context, out io.Writer, m store.Migrator) (current, latest int64, err error) {
	current, err = m.CurrentVersion(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("get current version: %w", err)
	}
	latest = m.LatestVersion()
	if _, err := fmt.Fprintf(out, "Current schema version: %d\nLatest available version: %d\n", current, latest); err != nil {
		return 0, 0, err
	}
	return current, latest, nil
}

func newDBMigrateCommand() *cobra.Command {
	var target int64
	return recoverExistingStoreLeaf(recoverSpec{
		Leaf: cli.Leaf{
			Use:     "migrate",
			Short:   "Run schema migrations",
			Long:    "Migrate the schema to the latest version, or to the version that --to gives. Also reconciles built-in rows when the schema is already at the latest version.",
			Mutates: true,
			Examples: []cli.Example{
				{Command: "leapmux recover db migrate"},
				{Comment: "Migrate a Hub whose configuration file selects PostgreSQL.", Command: "leapmux recover db migrate --config /etc/leapmux/hub.yaml"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.Int64Var(&target, "to", 0, "the schema version to migrate to; the latest version when this flag is absent")
			},
		},
		BeforeOpen: func(inv *cli.Invocation) error {
			if inv.Changed("to") && target < 0 {
				return cli.Usagef("--to %d is not a schema version", target).WithHint("give a version from 0 to the latest version")
			}
			return nil
		},
	}, func(inv *cli.Invocation, _ *config.Config, st store.Store, _ *keystore.Keystore) error {
		ctx, out := inv.Context, inv.Printer.Out
		m := st.Migrator()

		current, latest, err := printSchemaVersions(ctx, out, m)
		if err != nil {
			return err
		}

		if inv.Changed("to") {
			if _, err := fmt.Fprintf(out, "Migrating to version %d...\n", target); err != nil {
				return err
			}
			if err := m.MigrateTo(ctx, target); err != nil {
				return fmt.Errorf("migrate to version %d: %w", target, err)
			}
		} else {
			if current == latest {
				// Still run m.Migrate so MigratorWithBuiltIns reconciles built-in
				// rows even when no schema migration is pending.
				if err := m.Migrate(ctx); err != nil {
					return fmt.Errorf("migrate: %w", err)
				}
				_, err := fmt.Fprintln(out, "Already at latest version.")
				return err
			}
			if _, err := fmt.Fprintf(out, "Migrating to latest version %d...\n", latest); err != nil {
				return err
			}
			if err := m.Migrate(ctx); err != nil {
				return fmt.Errorf("migrate: %w", err)
			}
		}

		newVersion, err := m.CurrentVersion(ctx)
		if err != nil {
			return fmt.Errorf("get new version: %w", err)
		}
		_, err = fmt.Fprintf(out, "Migration complete. Current version: %d\n", newVersion)
		return err
	})
}

func newDBVersionCommand() *cobra.Command {
	return recoverExistingStoreLeaf(recoverSpec{Leaf: cli.Leaf{
		Use:   "version",
		Short: "Show current schema version",
		Examples: []cli.Example{
			{Command: "leapmux recover db version"},
		},
	}}, func(inv *cli.Invocation, _ *config.Config, st store.Store, _ *keystore.Keystore) error {
		_, _, err := printSchemaVersions(inv.Context, inv.Printer.Out, st.Migrator())
		return err
	})
}
