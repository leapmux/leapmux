package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

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
				{Name: "rotate-pepper", Summary: "Regenerate the API-token pepper (invalidates all API/delegation tokens)", Run: runRotatePepper},
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

// resolveUserFilter resolves an optional --user-id/--username flag pair into a
// user-id list filter for the admin listings. Unlike resolveUser (which serves
// user-mutation commands and therefore sees only live users), the ID path
// resolves via GetByIDIncludeDeleted: the admin listings deliberately surface
// soft-deleted owners' still-live rows for audit, so an operator must be able
// to scope a listing to a soft-deleted user's id (e.g. to enumerate and revoke
// a compromised account's outstanding tokens). A username resolves among live
// users only -- usernames are freed for re-registration on soft-delete, so a
// name is not a stable handle for a deleted account; use the id instead.
// Returns nil (no filter) when both flags are empty; a nonexistent id or
// username fails loudly rather than printing an empty table.
func resolveUserFilter(ctx context.Context, st store.Store, userID, username string) (*string, error) {
	if userID == "" && username == "" {
		return nil, nil
	}
	if userID != "" && username != "" {
		return nil, fmt.Errorf("--user-id and --username are mutually exclusive")
	}
	if userID != "" {
		user, err := st.Users().GetByIDIncludeDeleted(ctx, userID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, fmt.Errorf("user not found: %s", userID)
			}
			return nil, fmt.Errorf("get user by ID: %w", err)
		}
		return &user.ID, nil
	}
	user, err := st.Users().GetByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("user not found: %s", username)
		}
		return nil, fmt.Errorf("get user by username: %w", err)
	}
	return &user.ID, nil
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

// cursorFlagUsage is the shared --cursor help text for the admin list
// subcommands; the value is the opaque keyset cursor emitted by
// maybePrintNextCursor.
const cursorFlagUsage = "cursor for pagination (opaque; copy from the previous page's output)"

// addListFlags registers the shared --limit/--cursor flag pair every admin
// list subcommand takes, so a new listing cannot register a divergent default
// or usage string. validateListLimit still runs per command: it needs the
// parsed value, which exists only after flag parsing inside the run callback.
func addListFlags(fs *flag.FlagSet) (limit *int64, cursor *string) {
	limit = fs.Int64("limit", 50, "maximum number of results")
	cursor = fs.String("cursor", "", cursorFlagUsage)
	return limit, cursor
}

// maybePrintNextCursor prints the cursor hint for the next page when the
// store reports one. The store owns both the has-more probe and the cursor
// encoding (see store.NewPage), so the CLI cannot mispair the cursor column
// or hint at a page that turns out empty. The hint goes to stderr so it never
// pollutes piped output: stdout stays pure tabular data for `| awk`-style
// consumers, while an interactive operator still sees the hint.
func maybePrintNextCursor[T store.PageCursorer](page store.Page[T]) {
	if page.HasMore() {
		fmt.Fprintf(os.Stderr, "\nNext page: --cursor %s\n", page.NextCursor)
	}
}

// classifyListError maps a store list error to a CLI-friendly message. A
// malformed or stale --cursor is bad operator input (the store surfaces it
// wrapped in store.ErrInvalidCursor), not a server fault: return an
// actionable hint so the operator knows to re-copy the cursor or omit it,
// rather than an opaque wrapped error that reads like a hub failure. Mirrors
// the ListWorkers RPC's errors.Is(err, store.ErrInvalidCursor) -> connect.Code
// InvalidArgument classification in worker_mgmt_service.go.
func classifyListError(cmd string, err error) error {
	if errors.Is(err, store.ErrInvalidCursor) {
		return fmt.Errorf("%s: invalid cursor (pass the --cursor value printed at the end of the previous page, or omit --cursor to start from the first page): %w", cmd, err)
	}
	return fmt.Errorf("%s: %w", cmd, err)
}

// ownerLabel renders a JOINed owner/creator username for the admin listings; a
// soft-deleted owner surfaces as "(deleted)". The store returns the raw
// (username, deleted) state -- the placeholder is a presentation decision made
// here, not in SQL.
func ownerLabel(username string, deleted bool) string {
	if deleted {
		return "(deleted)"
	}
	return username
}

// validateListLimit rejects a non-positive --limit. A paginated store listing
// treats a limit of 0 (and, after clamping, any negative) as "return no rows"
// (store.FetchLimit / store.ClampListLimit), so a stray `--limit 0` or
// `--limit -5` would print an empty listing an operator cannot distinguish
// from a genuinely empty result. Reject it at the CLI boundary so bad input
// fails loudly instead of masquerading as "none found".
func validateListLimit(limit int64) error {
	if limit <= 0 {
		return fmt.Errorf("--limit must be a positive number (got %d)", limit)
	}
	return nil
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
