package main

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
)

type recoverDBPathResult struct {
	Path string `json:"path"`
}

func (r recoverDBPathResult) RenderText(w io.Writer) error {
	_, err := fmt.Fprintln(w, r.Path)
	return err
}

type recoverDBVersionResult struct {
	CurrentVersion int64 `json:"current_version"`
	LatestVersion  int64 `json:"latest_version"`
}

func (r recoverDBVersionResult) RenderText(w io.Writer) error {
	_, err := fmt.Fprintf(w, "Current schema version: %d\nLatest available version: %d\n", r.CurrentVersion, r.LatestVersion)
	return err
}

type recoverDBMigrateResult struct {
	PreviousVersion int64  `json:"previous_version"`
	LatestVersion   int64  `json:"latest_version"`
	TargetVersion   *int64 `json:"target_version,omitempty"`
	CurrentVersion  int64  `json:"current_version"`
	AlreadyLatest   bool   `json:"already_latest"`
}

func (r recoverDBMigrateResult) RenderText(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "Current schema version: %d\nLatest available version: %d\n", r.PreviousVersion, r.LatestVersion); err != nil {
		return err
	}
	if r.TargetVersion != nil {
		_, err := fmt.Fprintf(w, "Migrating to version %d...\nMigration complete. Current version: %d\n", *r.TargetVersion, r.CurrentVersion)
		return err
	}
	if r.AlreadyLatest {
		_, err := fmt.Fprintln(w, "Already at latest version.")
		return err
	}
	_, err := fmt.Fprintf(w, "Migrating to latest version %d...\nMigration complete. Current version: %d\n", r.LatestVersion, r.CurrentVersion)
	return err
}

func newDBPathCommand() *cobra.Command {
	return recoverConfigLeaf(recoverSpec{Leaf: cli.Leaf{
		Use:   "path",
		Short: "Print the database path",
		Long:  "Print the path of the SQLite database of the Hub. A Hub on PostgreSQL or MySQL keeps its data on that server instead.",
		Examples: []cli.Example{
			{Command: "leapmux recover db path"},
		},
	}}, func(_ *cli.Invocation, cfg *config.Config) (any, error) {
		return recoverDBPathResult{Path: cfg.SQLiteDBPath()}, nil
	})
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
	}, func(inv *cli.Invocation, _ *config.Config, st store.Store, _ *keystore.Keystore) (any, error) {
		ctx := inv.Context
		m := st.Migrator()

		current, err := m.CurrentVersion(ctx)
		if err != nil {
			return nil, fmt.Errorf("get current version: %w", err)
		}
		latest := m.LatestVersion()

		res := recoverDBMigrateResult{
			PreviousVersion: current,
			LatestVersion:   latest,
			CurrentVersion:  current,
		}
		if inv.Changed("to") {
			t := target
			res.TargetVersion = &t
			if err := m.MigrateTo(ctx, target); err != nil {
				return nil, fmt.Errorf("migrate to version %d: %w", target, err)
			}
		} else {
			if current == latest {
				// Still run m.Migrate so MigratorWithBuiltIns reconciles built-in
				// rows even when no schema migration is pending.
				if err := m.Migrate(ctx); err != nil {
					return nil, fmt.Errorf("migrate: %w", err)
				}
				res.AlreadyLatest = true
				return res, nil
			}
			if err := m.Migrate(ctx); err != nil {
				return nil, fmt.Errorf("migrate: %w", err)
			}
		}

		newVersion, err := m.CurrentVersion(ctx)
		if err != nil {
			return nil, fmt.Errorf("get new version: %w", err)
		}
		res.CurrentVersion = newVersion
		return res, nil
	})
}

func newDBVersionCommand() *cobra.Command {
	return recoverExistingStoreLeaf(recoverSpec{Leaf: cli.Leaf{
		Use:   "version",
		Short: "Show current schema version",
		Examples: []cli.Example{
			{Command: "leapmux recover db version"},
		},
	}}, func(inv *cli.Invocation, _ *config.Config, st store.Store, _ *keystore.Keystore) (any, error) {
		m := st.Migrator()
		current, err := m.CurrentVersion(inv.Context)
		if err != nil {
			return nil, fmt.Errorf("get current version: %w", err)
		}
		return recoverDBVersionResult{
			CurrentVersion: current,
			LatestVersion:  m.LatestVersion(),
		}, nil
	})
}
