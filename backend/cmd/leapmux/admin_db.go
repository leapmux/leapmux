package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"

	"github.com/leapmux/leapmux/internal/hub/config"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
)

func runAdminDB(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: leapmux admin db <command> [flags]\n\nCommands:\n  path              Print the database path\n  backup            Create a database backup")
	}

	switch args[0] {
	case "path":
		return runDBPath(args[1:])
	case "backup":
		return runDBBackup(args[1:])
	default:
		return fmt.Errorf("unknown db command: %s", args[0])
	}
}

func runDBPath(args []string) error {
	fs := flag.NewFlagSet("db path", flag.ContinueOnError)
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	fmt.Println(adminConfig(*dataDir).DBPath())
	return nil
}

func runDBBackup(args []string) error {
	var output *string
	return withAdminDB("db backup", args, func(fs *flag.FlagSet) {
		output = fs.String("output", "", "output file path (required)")
	}, func(ctx context.Context, _ *config.Config, sqlDB *sql.DB, _ *gendb.Queries) error {
		if *output == "" {
			return fmt.Errorf("--output is required")
		}

		// Check that the output path doesn't already exist.
		if _, err := os.Stat(*output); err == nil {
			return fmt.Errorf("output file already exists: %s", *output)
		}

		_, err := sqlDB.ExecContext(ctx, "VACUUM INTO ?", *output)
		if err != nil {
			return fmt.Errorf("backup database: %w", err)
		}

		fmt.Printf("Database backed up to %s\n", *output)
		return nil
	})
}
