package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
	"golang.org/x/term"

	internalconfig "github.com/leapmux/leapmux/internal/config"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/storeopen"
)

type adminCommand struct {
	Name    string
	Summary string // shown in parent's command list
	Run     func(ctx adminCmdCtx, args []string) error
}

// adminCmdCtx carries the resolved path and description from the dispatcher
// down into the leaf, so leaves don't have to hand-type their own path or
// look up their description via a side-channel map.
type adminCmdCtx struct {
	Path        string // e.g. "user list", "worker reg-key revoke"
	Description string
}

type adminGroup struct {
	Name      string // empty for the root
	Summary   string // shown in parent's command list
	Commands  []adminCommand
	Subgroups []adminGroup
}

// rootAdminDescription is the leaf/root description for the bare `admin`
// group. Other groups derive their description from Summary at render time.
const rootAdminDescription = "Manage LeapMux resources."

// adminTree is the single source of truth for admin subcommand structure,
// summaries, and dispatch. Per-group help text and per-leaf descriptions are
// derived from each entry's Summary at render time.
var adminTree = adminGroup{
	Subgroups: []adminGroup{
		{
			Name:    "org",
			Summary: "Manage organizations",
			Commands: []adminCommand{
				{Name: "list", Summary: "List organizations", Run: runOrgList},
			},
		},
		{
			Name:    "user",
			Summary: "Manage users",
			Commands: []adminCommand{
				{Name: "list", Summary: "List users", Run: runUserList},
				{Name: "get", Summary: "Get user details", Run: runUserGet},
				{Name: "create", Summary: "Create a new user", Run: runUserCreate},
				{Name: "update", Summary: "Update user fields", Run: runUserUpdate},
				{Name: "delete", Summary: "Delete a user", Run: runUserDelete},
				{Name: "reset-password", Summary: "Reset a user's password", Run: runUserResetPassword},
				{Name: "grant-admin", Summary: "Grant admin privileges", Run: runUserGrantAdmin},
				{Name: "revoke-admin", Summary: "Revoke admin privileges", Run: runUserRevokeAdmin},
				{Name: "list-sessions", Summary: "List a user's active sessions", Run: runUserListSessions},
			},
		},
		{
			Name:    "session",
			Summary: "Manage sessions",
			Commands: []adminCommand{
				{Name: "list", Summary: "List all active sessions", Run: runSessionList},
				{Name: "revoke", Summary: "Revoke a session by ID", Run: runSessionRevoke},
				{Name: "revoke-user", Summary: "Revoke all sessions for a user", Run: runSessionRevokeUser},
				{Name: "purge-expired", Summary: "Delete all expired sessions", Run: runSessionPurgeExpired},
			},
		},
		{
			Name:    "worker",
			Summary: "Manage workers",
			Commands: []adminCommand{
				{Name: "list", Summary: "List workers", Run: runWorkerList},
				{Name: "get", Summary: "Get worker details", Run: runWorkerGet},
				{Name: "deregister", Summary: "Deregister a worker", Run: runWorkerDeregister},
			},
			Subgroups: []adminGroup{
				{
					Name:    "reg-key",
					Summary: "Manage worker registration keys",
					Commands: []adminCommand{
						{Name: "list", Summary: "List worker registration keys", Run: runWorkerRegKeyList},
						{Name: "revoke", Summary: "Revoke a registration key by ID", Run: runWorkerRegKeyRevoke},
						{Name: "purge-expired", Summary: "Hard-delete all expired or revoked keys", Run: runWorkerRegKeyPurgeExpired},
					},
				},
			},
		},
		{
			Name:    "oauth-provider",
			Summary: "Manage OAuth/OIDC providers",
			Commands: []adminCommand{
				{Name: "add", Summary: "Add an OAuth/OIDC provider", Run: runAddOAuthProvider},
				{Name: "list", Summary: "List configured providers", Run: runListOAuthProviders},
				{Name: "remove", Summary: "Remove a provider", Run: runRemoveOAuthProvider},
				{Name: "enable", Summary: "Enable a provider", Run: func(cmd adminCmdCtx, args []string) error { return runSetOAuthProviderEnabled(cmd, args, true) }},
				{Name: "disable", Summary: "Disable a provider", Run: func(cmd adminCmdCtx, args []string) error { return runSetOAuthProviderEnabled(cmd, args, false) }},
			},
		},
		{
			Name:    "encryption-key",
			Summary: "Manage encryption keys",
			Commands: []adminCommand{
				{Name: "rotate", Summary: "Generate and add a new encryption key version", Run: runRotateEncryptionKey},
				{Name: "remove", Summary: "Remove an old encryption key version", Run: runRemoveEncryptionKey},
				{Name: "reencrypt", Summary: "Re-encrypt all secrets with the active key", Run: runReencryptSecrets},
			},
		},
		{
			Name:    "db",
			Summary: "Database utilities",
			Commands: []adminCommand{
				{Name: "path", Summary: "Print the database path", Run: runDBPath},
				{Name: "migrate", Summary: "Run schema migrations", Run: runDBMigrate},
				{Name: "version", Summary: "Show current schema version", Run: runDBVersion},
			},
		},
		{
			Name:    "api-token",
			Summary: "Manage durable API tokens (CLI / integrations)",
			Commands: []adminCommand{
				{Name: "list", Summary: "List API tokens", Run: runAPITokenList},
				{Name: "issue", Summary: "Issue a new API token (e.g. for headless service accounts)", Run: runAPITokenIssue},
				{Name: "revoke", Summary: "Revoke an API token by id", Run: runAPITokenRevoke},
			},
		},
		{
			Name:    "delegation-token",
			Summary: "Manage worker-minted delegation tokens",
			Commands: []adminCommand{
				{Name: "list", Summary: "List delegation tokens", Run: runDelegationTokenList},
				{Name: "revoke", Summary: "Revoke a delegation token by id", Run: runDelegationTokenRevoke},
			},
		},
	},
}

func runAdmin(args []string) error {
	return dispatchAdminGroup(adminTree, args, nil)
}

// dispatchAdminGroup walks adminTree to invoke a leaf command. path is the
// fully-qualified group path leading to group (empty for the root), used to
// build error messages and the leaf's adminCmdCtx.
func dispatchAdminGroup(group adminGroup, args, path []string) error {
	if len(args) == 0 {
		if len(path) == 0 {
			return fmt.Errorf("admin group is required")
		}
		return fmt.Errorf("%s command is required", strings.Join(path, " "))
	}
	for i := range group.Subgroups {
		if group.Subgroups[i].Name == args[0] {
			return dispatchAdminGroup(group.Subgroups[i], args[1:], append(path, args[0]))
		}
	}
	for i := range group.Commands {
		c := group.Commands[i]
		if c.Name == args[0] {
			ctx := adminCmdCtx{
				Path:        strings.Join(append(path, c.Name), " "),
				Description: c.Summary + ".",
			}
			return c.Run(ctx, args[1:])
		}
	}
	if len(path) == 0 {
		return fmt.Errorf("unknown admin group: %s", args[0])
	}
	return fmt.Errorf("unknown %s command: %s", strings.Join(path, " "), args[0])
}

// formatAdminGroupUsage renders the help text for a group. fullPath is the
// command path beneath the binary (e.g., "admin", "admin user").
func formatAdminGroupUsage(g adminGroup, fullPath string) string {
	var sb strings.Builder
	description := rootAdminDescription
	if g.Name != "" {
		description = g.Summary + "."
	}
	sb.WriteString(description)
	sb.WriteString("\n\n")
	// Only the root admin group requires the user to pick a subgroup.
	if g.Name == "" {
		fmt.Fprintf(&sb, "Usage: leapmux %s <group> <command> [flags]\n\n", fullPath)
		sb.WriteString("Groups:\n")
	} else {
		fmt.Fprintf(&sb, "Usage: leapmux %s <command> [flags]\n\n", fullPath)
		sb.WriteString("Commands:\n")
	}
	for _, c := range g.Commands {
		fmt.Fprintf(&sb, "  %-18s%s\n", c.Name, c.Summary)
	}
	for _, sub := range g.Subgroups {
		fmt.Fprintf(&sb, "  %-18s%s\n", sub.Name, sub.Summary)
	}
	return sb.String()
}

// ---- Helpers ----

// withAdminStore creates a flag set with --data-dir and --config, parses args,
// opens the store, and calls fn. The store is closed after fn returns.
// When --config is provided, the hub config file is loaded to obtain storage
// settings. Otherwise, a minimal config is constructed from --data-dir.
func withAdminStore(cmd adminCmdCtx, args []string, setup func(fs *flag.FlagSet), fn func(ctx context.Context, cfg *config.Config, st store.Store) error) error {
	fs := flag.NewFlagSet("leapmux admin "+cmd.Path, flag.ContinueOnError)
	dataDir := fs.String("data-dir", "", "data directory")
	configFile := fs.String("config", "", "path to hub config file (loads storage settings)")
	if setup != nil {
		setup(fs)
	}
	if err := internalconfig.ConfigureAndParse(fs, args, cmd.Description, nil, nil); err != nil {
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

	st, err := storeopen.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	return fn(context.Background(), cfg, st)
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

// adminAllUsersLimit caps how many users `collectAcrossUsers` will
// scan when --user is unset on a list-style admin command. Hub
// deployments large enough to bump against this should pass --user
// to narrow the query; we surface the cap here so changes are
// reviewed in one place.
const adminAllUsersLimit = 1000

// collectAcrossUsers runs `fetch` once per user in the store when
// `userID` is empty, otherwise just once for the named user. The
// per-user results are concatenated in user-listing order. Shared by
// `api-token list` and `delegation-token list` which both walk every
// user when --user is unset.
func collectAcrossUsers[T any](ctx context.Context, st store.Store, userID string, fetch func(uid string) ([]T, error)) ([]T, error) {
	if userID != "" {
		return fetch(userID)
	}
	users, err := st.Users().ListAll(ctx, store.ListAllUsersParams{Limit: adminAllUsersLimit})
	if err != nil {
		return nil, err
	}
	var rows []T
	for _, u := range users {
		batch, err := fetch(u.ID)
		if err != nil {
			return nil, err
		}
		rows = append(rows, batch...)
	}
	return rows, nil
}

// withAdminConfig creates a flag set with --data-dir, parses args, and
// calls fn with the resolved config. Use this for commands that need
// the config but not a database connection.
func withAdminConfig(cmd adminCmdCtx, args []string, setup func(fs *flag.FlagSet), fn func(cfg *config.Config) error) error {
	fs := flag.NewFlagSet("leapmux admin "+cmd.Path, flag.ContinueOnError)
	dataDir := fs.String("data-dir", "", "data directory")
	if setup != nil {
		setup(fs)
	}
	if err := internalconfig.ConfigureAndParse(fs, args, cmd.Description, nil, nil); err != nil {
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

// printJSON writes v to stdout as indented JSON. Admin commands use it
// for output that needs to round-trip through `jq` / scripts.
func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
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
