package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/mattn/go-isatty"
	"golang.org/x/term"

	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/storeopen"
)

func runAdmin(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: leapmux admin <group> <command> [flags]\n\nGroups:\n  org               Manage organizations\n  user              Manage users\n  session           Manage sessions\n  worker            Manage workers\n  oauth-provider    Manage OAuth/OIDC providers\n  encryption-key    Manage encryption keys\n  db                Database utilities")
	}

	switch args[0] {
	case "org":
		return runAdminOrg(args[1:])
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

// withAdminStore creates a flag set with --data-dir and --config, parses args,
// opens the store, and calls fn. The store is closed after fn returns.
// When --config is provided, the hub config file is loaded to obtain storage
// settings. Otherwise, a minimal config is constructed from --data-dir.
func withAdminStore(name string, args []string, setup func(fs *flag.FlagSet), fn func(ctx context.Context, cfg *config.Config, st store.Store) error) error {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	dataDir := fs.String("data-dir", "", "data directory")
	configFile := fs.String("config", "", "path to hub config file (loads storage settings)")
	if setup != nil {
		setup(fs)
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	var cfg *config.Config
	if *configFile != "" {
		var err error
		cfg, _, err = config.LoadWithOptions([]string{"--config", *configFile}, config.LoadOptions{})
		if err != nil {
			return fmt.Errorf("load config from %s: %w", *configFile, err)
		}
		if *dataDir != "" {
			cfg.DataDir = *dataDir
		}
	} else {
		cfg = adminConfig(*dataDir)
	}

	st, err := openAdminStore(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	return fn(context.Background(), cfg, st)
}

func openAdminStore(cfg *config.Config) (store.Store, error) {
	return storeopen.Open(context.Background(), cfg)
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

// withAdminConfig creates a flag set with --data-dir, parses args, and
// calls fn with the resolved config. Use this for commands that need
// the config but not a database connection.
func withAdminConfig(name string, args []string, setup func(fs *flag.FlagSet), fn func(cfg *config.Config) error) error {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	dataDir := fs.String("data-dir", "", "data directory")
	if setup != nil {
		setup(fs)
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	return fn(adminConfig(*dataDir))
}

// resolveUser looks up a user by ID or username using the Store interface.
func resolveUser(ctx context.Context, st store.Store, userID, username string) (*store.User, error) {
	if userID == "" && username == "" {
		return nil, fmt.Errorf("--id or --username is required")
	}
	if userID != "" && username != "" {
		return nil, fmt.Errorf("--id and --username are mutually exclusive")
	}

	if userID != "" {
		user, err := st.Users().GetByID(ctx, userID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, fmt.Errorf("user not found: %s", userID)
			}
			return nil, fmt.Errorf("get user by ID: %w", err)
		}
		return user, nil
	}

	user, err := st.Users().GetByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("user not found: %s", username)
		}
		return nil, fmt.Errorf("get user by username: %w", err)
	}
	return user, nil
}

// friendlyConstraintError translates uniqueness constraint violations
// into user-friendly messages.
func friendlyConstraintError(err error, username, email string) error {
	var ce *store.ConflictError
	if !errors.As(err, &ce) {
		if errors.Is(err, store.ErrConflict) {
			// Fallback for untyped ErrConflict.
			return fmt.Errorf("username %q is already taken", username)
		}
		return err
	}
	switch ce.Entity {
	case store.ConflictEntityOrg:
		// Org name = username (personal org), so username is taken.
		return fmt.Errorf("username %q is already taken", username)
	case store.ConflictEntityUser:
		if email != "" {
			return fmt.Errorf("email %q is already in use", email)
		}
		return fmt.Errorf("username %q is already taken", username)
	default:
		return err
	}
}

// promptPassword reads a password from the terminal without echoing.
// It emits OSC 133;P to signal password input to terminals that support
// it (e.g. Ghostty), enabling credential detection features.
func promptPassword(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !isatty.IsTerminal(uintptr(fd)) && !isatty.IsCygwinTerminal(uintptr(fd)) {
		return "", fmt.Errorf("--password is required (stdin is not a terminal)")
	}

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

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// maybePrintNextCursor prints a cursor hint for the next page if the result
// set is exactly limit items (indicating more pages may exist).
func maybePrintNextCursor[T any](items []T, limit int64, getTime func(T) time.Time) {
	if n := int64(len(items)); n > 0 && n == limit {
		fmt.Printf("\nNext page: --cursor %s\n", getTime(items[n-1]).UTC().Format(time.RFC3339Nano))
	}
}

// truncate shortens a string to maxLen runes, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}
