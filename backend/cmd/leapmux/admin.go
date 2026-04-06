package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/mattn/go-isatty"
	"golang.org/x/term"
	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/db"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
)

func runAdmin(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: leapmux admin <group> <command> [flags]\n\nGroups:\n  user              Manage users\n  session           Manage sessions\n  worker            Manage workers\n  oauth-provider    Manage OAuth/OIDC providers\n  encryption-key    Manage encryption keys\n  db                Database utilities")
	}

	switch args[0] {
	case "user":
		return runAdminUser(args[1:])
	case "session":
		return runAdminSession(args[1:])
	case "worker":
		return runAdminWorker(args[1:])
	case "oauth-provider":
		return runAdminOAuthProvider(args[1:])
	case "encryption-key":
		return runAdminEncryptionKey(args[1:])
	case "db":
		return runAdminDB(args[1:])
	default:
		return fmt.Errorf("unknown admin group: %s", args[0])
	}
}

// ---- Helpers ----

// withAdminDB creates a flag set with --data-dir, parses args, opens the
// database, and calls fn. The database is closed after fn returns.
func withAdminDB(name string, args []string, setup func(fs *flag.FlagSet), fn func(ctx context.Context, cfg *config.Config, sqlDB *sql.DB, q *gendb.Queries) error) error {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	dataDir := fs.String("data-dir", "", "data directory")
	if setup != nil {
		setup(fs)
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg := adminConfig(*dataDir)
	sqlDB, q, err := openAdminDB(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	return fn(context.Background(), cfg, sqlDB, q)
}

// openAdminDB opens the database, runs migrations, and returns the connection
// and queries handle. The caller must close the returned *sql.DB.
func openAdminDB(cfg *config.Config) (*sql.DB, *gendb.Queries, error) {
	dbPath := cfg.DBPath()
	sqlDB, err := db.Open(dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open database: %w", err)
	}
	if err := db.Migrate(sqlDB); err != nil {
		_ = sqlDB.Close()
		return nil, nil, fmt.Errorf("migrate database: %w", err)
	}
	return sqlDB, gendb.New(sqlDB), nil
}

// adminConfig returns a minimal Config with DataDir set. When dataDir is
// empty it uses the default hub data directory.
func adminConfig(dataDir string) *config.Config {
	cfg := &config.Config{}
	if dataDir != "" {
		cfg.DataDir = dataDir
	} else {
		cfg.DataDir = config.DefaultHubDataDir()
	}
	return cfg
}

// resolveUser looks up a user by ID or username. Exactly one of userID or
// username must be non-empty.
func resolveUser(ctx context.Context, q *gendb.Queries, userID, username string) (*gendb.User, error) {
	if userID == "" && username == "" {
		return nil, fmt.Errorf("--id or --username is required")
	}

	var user gendb.User
	var err error

	if userID != "" {
		user, err = q.GetUserByID(ctx, userID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("user not found: %s", userID)
			}
			return nil, fmt.Errorf("get user by ID: %w", err)
		}
	} else {
		user, err = q.GetUserByUsername(ctx, username)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("user not found: %s", username)
			}
			return nil, fmt.Errorf("get user by username: %w", err)
		}
	}

	return &user, nil
}

// sqliteConstraintUnique is the extended error code for UNIQUE constraint
// violations: SQLITE_CONSTRAINT (19) | (8 << 8) = 2067.
const sqliteConstraintUnique = sqlite3.SQLITE_CONSTRAINT | (8 << 8)

// friendlyConstraintError translates SQLite UNIQUE constraint violations
// into user-friendly messages.
func friendlyConstraintError(err error, username, email string) error {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code() != sqliteConstraintUnique {
		return err
	}
	// It's a UNIQUE constraint violation. Check the error message to determine which field.
	msg := err.Error()
	if strings.Contains(msg, "orgs.name") || strings.Contains(msg, "users.username") {
		return fmt.Errorf("username %q is already taken", username)
	}
	if strings.Contains(msg, "users.email") {
		return fmt.Errorf("email %q is already in use", email)
	}
	return err
}

// promptPassword reads a password from the terminal without echoing.
// It emits OSC 133;P to signal password input to terminals that support
// it (e.g. Ghostty), enabling credential detection features.
func promptPassword(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !isatty.IsTerminal(uintptr(fd)) && !isatty.IsCygwinTerminal(uintptr(fd)) {
		return "", fmt.Errorf("--password is required (stdin is not a terminal)")
	}

	// OSC 133;P signals the start of password input to supporting terminals.
	fmt.Fprint(os.Stderr, "\x1b]133;P\x07")
	fmt.Fprint(os.Stderr, prompt)
	pw, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr) // newline after hidden input
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(pw), nil
}

// requirePassword returns the password from the flag if set, otherwise
// prompts interactively.
func requirePassword(pw string, prompt string) (string, error) {
	if pw != "" {
		return pw, nil
	}
	return promptPassword(prompt)
}

func yesNo(v int64) string {
	if v == 1 {
		return "yes"
	}
	return "no"
}

// truncate shortens a string to maxLen characters, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
