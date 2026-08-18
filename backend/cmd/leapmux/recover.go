package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	internalconfig "github.com/leapmux/leapmux/internal/config"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/storeopen"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type cmdLeaf struct {
	Name    string
	Summary string // shown in parent's command list
	Run     func(ctx cmdCtx, args []string) error
}

// cmdCtx carries the resolved path and description from the dispatcher
// down into the leaf, so leaves don't have to hand-type their own path or
// look up their description via a side-channel map.
type cmdCtx struct {
	Path        string // e.g. "recover password reset", "control admin user list"
	Description string
}

type cmdGroup struct {
	Name      string // group name; the root of a tree has one also
	Summary   string // one line; the parent lists it, and the group's own help prints it
	Commands  []cmdLeaf
	Subgroups []cmdGroup
}

// recoverTree is the ONLY command tree that opens the database directly.
// Everything else that used to live under the removed offline admin tree is administered
// online, authenticated, through `leapmux control admin ...` against the
// Admin*Service RPC surface; these four groups are the pieces that must
// work when the hub is stopped or locked out (first-admin bootstrap,
// password reset, schema migration, encryption-key surgery).
var recoverTree = cmdGroup{
	Name:    "recover",
	Summary: "Offline break-glass recovery (opens the database directly)",
	Subgroups: []cmdGroup{
		{
			Name:    "bootstrap",
			Summary: "First-run recovery on an empty hub (online administration: `leapmux control admin user create`)",
			Commands: []cmdLeaf{
				{Name: "create-admin", Summary: "Create the initial administrator (refuses once any admin exists)", Run: runBootstrapCreateAdmin},
			},
		},
		{
			Name:    "password",
			Summary: "Reset passwords offline",
			Commands: []cmdLeaf{
				{Name: "reset", Summary: "Reset a user's password and revoke all their sessions", Run: runPasswordReset},
			},
		},
		{
			Name:    "encryption-key",
			Summary: "Manage encryption keys",
			Commands: []cmdLeaf{
				{Name: "rotate", Summary: "Generate and add a new encryption key version", Run: runRotateEncryptionKey},
				{Name: "remove", Summary: "Remove an old encryption key version", Run: runRemoveEncryptionKey},
				{Name: "reencrypt", Summary: "Re-encrypt all secrets with the active key", Run: runReencryptSecrets},
				{Name: "rotate-pepper", Summary: "Regenerate the API-token pepper (invalidates all API/delegation tokens)", Run: runRotatePepper},
			},
		},
		{
			Name:    "db",
			Summary: "Database utilities",
			Commands: []cmdLeaf{
				{Name: "path", Summary: "Print the database path", Run: runDBPath},
				{Name: "migrate", Summary: "Run schema migrations", Run: runDBMigrate},
				{Name: "version", Summary: "Show current schema version", Run: runDBVersion},
			},
		},
	},
}

func runRecover(args []string) error {
	return dispatchGroup(recoverTree, args, []string{"recover"})
}

// matchCommandToken resolves arg against names using btrfs-style prefix
// matching: an exact match always wins; otherwise arg must be a unique
// unambiguous prefix of exactly one name. This mirrors the algorithm in
// btrfs-progs' parse_one_token (btrfs.c): shorten any command name as far
// as it stays unambiguous.
//
// Returns the matched name, or an error describing ambiguity ("ambiguous
// token 'pre'; did you mean one of: a, b") or a total miss ("unknown
// command: pre"). candidates holds the names that share the prefix, so
// the caller can render the "did you mean" list in its own error format.
func matchCommandToken(arg string, names []string) (matched string, candidates []string, err error) {
	// Exact match wins outright, even if arg is also a prefix of others.
	for _, n := range names {
		if n == arg {
			return n, nil, nil
		}
	}
	// Otherwise collect every name for which arg is a strict prefix.
	for _, n := range names {
		if strings.HasPrefix(n, arg) {
			candidates = append(candidates, n)
		}
	}
	switch len(candidates) {
	case 0:
		return "", nil, fmt.Errorf("unknown command: %s", arg)
	case 1:
		return candidates[0], candidates, nil
	default:
		return "", candidates, fmt.Errorf("ambiguous token %q; did you mean one of: %s", arg, strings.Join(candidates, ", "))
	}
}

// groupTokenMatch is what one token resolves to inside a group: at most
// one of Subgroup and Command is a valid index, and the other is -1.
// Candidates lists every name the token is a prefix of when nothing
// resolved, so the caller can render its own "did you mean" list.
type groupTokenMatch struct {
	Subgroup   int
	Command    int
	Candidates []string
}

// resolveGroupToken resolves one token against a group's subgroups AND its
// commands, as ONE namespace.
//
// The union is the point. Matching each category separately and preferring
// the subgroup meant a token that was a unique prefix of both dispatched
// the subgroup in silence — and it beat an EXACT command match, which is
// the one thing prefix matching must never do. Over the union an exact
// match always wins and a prefix that fits two names is ambiguous, whether
// those names sit in the same category or not.
//
// Both walkers over this tree call it: the dispatcher that runs a leaf and
// the validator that prints help. They shared the matching rules and the
// error wording by copy before, and had already drifted apart.
func resolveGroupToken(group cmdGroup, arg string) groupTokenMatch {
	names := make([]string, 0, len(group.Subgroups)+len(group.Commands))
	for i := range group.Subgroups {
		names = append(names, group.Subgroups[i].Name)
	}
	for i := range group.Commands {
		names = append(names, group.Commands[i].Name)
	}
	out := groupTokenMatch{Subgroup: -1, Command: -1}
	matched, candidates, err := matchCommandToken(arg, names)
	if err != nil {
		out.Candidates = candidates
		return out
	}
	for i := range group.Subgroups {
		if group.Subgroups[i].Name == matched {
			out.Subgroup = i
			return out
		}
	}
	for i := range group.Commands {
		if group.Commands[i].Name == matched {
			out.Command = i
			return out
		}
	}
	return out
}

// tokenNoun gives the word for what the next token under g must be. A group
// that holds subgroups and no command of its own accepts a group name only.
// Every other group accepts a command name, so "command" is the default.
//
// The refusal, the "Usage:" line, and the section headers of the help text
// all ask the same question, so they all ask it here. A tree that later mixes
// commands into a group of groups changes all three at once.
func tokenNoun(g cmdGroup) string {
	if len(g.Commands) == 0 && len(g.Subgroups) > 0 {
		return "group"
	}
	return "command"
}

// unresolvedTokenError renders the refusal for a token that resolved to
// nothing.
//
// Both tree walkers call it, so the wording cannot drift — and only ONE of
// them is reachable for any given input: the validating walk runs first
// and returns "handled" on every failure, so the dispatcher's copy of this
// message never reaches a terminal. Two hand-written copies meant the
// spelling a test could pin was not the spelling an operator saw.
//
// fullPath ALWAYS holds the top-level name, so `["recover"]` is the recover
// root and `["control", "admin"]` is a subgroup of the control root. Both
// walkers pass that convention. The message gives the whole path an operator
// typed, which is the one they can act on. noun is what the token had to be
// — see tokenNoun.
func unresolvedTokenError(fullPath []string, arg string, candidates []string, noun string) error {
	if len(candidates) > 0 {
		return fmt.Errorf("ambiguous token %q; did you mean one of: %s", arg, strings.Join(candidates, ", "))
	}
	return fmt.Errorf("unknown %s %s: %s", strings.Join(fullPath, " "), noun, arg)
}

// dispatchGroup walks a command tree to invoke a leaf command. path is
// the fully-qualified path of group, from the top-level name down, so the
// recover root is `["recover"]`. It builds the error messages and the leaf's
// cmdCtx. walkGroupArgs passes the same convention.
//
// Command and subgroup names may be abbreviated to any unambiguous prefix
// (btrfs-style): e.g. `control admin user lis` resolves to `user list`.
func dispatchGroup(group cmdGroup, args, path []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s %s is required", strings.Join(path, " "), tokenNoun(group))
	}

	match := resolveGroupToken(group, args[0])
	switch {
	case match.Subgroup >= 0:
		sub := group.Subgroups[match.Subgroup]
		return dispatchGroup(sub, args[1:], append(path, sub.Name))
	case match.Command >= 0:
		leaf := group.Commands[match.Command]
		return leaf.Run(cmdCtx{
			Path:        strings.Join(append(path, leaf.Name), " "),
			Description: leaf.Summary + ".",
		}, args[1:])
	default:
		return unresolvedTokenError(path, args[0], match.Candidates, tokenNoun(group))
	}
}

// formatGroupUsage renders the help text for a group. fullPath is the
// command path beneath the binary (e.g., "recover", "control admin user").
func formatGroupUsage(g cmdGroup, fullPath string) string {
	var sb strings.Builder
	sb.WriteString(g.Summary + ".")
	sb.WriteString("\n\n")
	// A group of groups needs two more tokens; every other group needs one.
	if tokenNoun(g) == "group" {
		fmt.Fprintf(&sb, "Usage: leapmux %s <group> <command> [flags]\n\n", fullPath)
	} else {
		fmt.Fprintf(&sb, "Usage: leapmux %s <command> [flags]\n\n", fullPath)
	}
	// A group can hold both, and then it gets both headers. One flat list
	// under a single header told the reader that `control admin` runs, when
	// it is a group that needs another token.
	if len(g.Commands) > 0 {
		sb.WriteString("Commands:\n")
		for _, c := range g.Commands {
			fmt.Fprintf(&sb, "  %-18s%s\n", c.Name, c.Summary)
		}
	}
	if len(g.Subgroups) > 0 {
		if len(g.Commands) > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("Groups:\n")
		for _, sub := range g.Subgroups {
			fmt.Fprintf(&sb, "  %-18s%s\n", sub.Name, sub.Summary)
		}
	}
	sb.WriteString("\nAny command name can be shortened as far as it stays unambiguous.\n")
	return sb.String()
}

// ---- Helpers ----

// printJSON writes an indented JSON document to stdout (worker_pins and
// the recover leaves share it).
func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// withRecoverStore creates a flag set with --data-dir and --config, parses
// args, opens the store, and calls fn. The store is closed after fn returns.
// When --config is provided, the hub config file is loaded to obtain storage
// settings. Otherwise, a minimal config is constructed from --data-dir.
func withRecoverStore(cmd cmdCtx, args []string, setup func(fs *flag.FlagSet), fn func(ctx context.Context, cfg *config.Config, st store.Store) error) error {
	fs := flag.NewFlagSet("leapmux "+cmd.Path, flag.ContinueOnError)
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
		cfg = recoverConfig(*dataDir)
	}

	// The hub creates the data dir at startup; recover commands run
	// before (or instead of) it, so create it here for the same
	// out-of-the-box behavior (Validate is MkdirAll + storage checks,
	// exactly what NewServer runs).
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate config: %w", err)
	}

	st, err := storeopen.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	return fn(context.Background(), cfg, st)
}

// recoverConfig returns a minimal Config with DataDir set. When dataDir is
// empty it uses the default hub data directory.
func recoverConfig(dataDir string) *config.Config {
	cfg := &config.Config{}
	if dataDir != "" {
		cfg.DataDir = dataDir
	} else {
		cfg.DataDir = config.DefaultHubDataDir()
	}
	return cfg
}

// withRecoverConfig creates a flag set with --data-dir, parses args, and
// calls fn with the resolved config. Use this for commands that need
// the config but not a database connection.
func withRecoverConfig(cmd cmdCtx, args []string, setup func(fs *flag.FlagSet), fn func(cfg *config.Config) error) error {
	fs := flag.NewFlagSet("leapmux "+cmd.Path, flag.ContinueOnError)
	dataDir := fs.String("data-dir", "", "data directory")
	if setup != nil {
		setup(fs)
	}
	if err := internalconfig.ConfigureAndParse(fs, args, cmd.Description, nil, nil); err != nil {
		return err
	}
	return fn(recoverConfig(*dataDir))
}

// resolveUser looks up a user by ID or username using the Store interface.
//
// The selector RULE lives in service.ResolveUserSelector, shared with the
// online admin RPCs so the two surfaces cannot disagree about what a valid
// selector is. The WORDING stays here: this one talks to a person at a
// terminal, so it lists flags and echoes the selector they typed.
func resolveUser(ctx context.Context, st store.Store, userID, username string) (*store.User, error) {
	user, err := service.ResolveUserSelector(ctx, st, userID, username)
	if err == nil {
		return user, nil
	}
	selector := userID
	field := "ID"
	if userID == "" {
		selector, field = username, "username"
	}
	switch {
	case errors.Is(err, service.ErrNoUserSelector):
		return nil, fmt.Errorf("--id or --username is required")
	case errors.Is(err, service.ErrAmbiguousUserSelector):
		return nil, fmt.Errorf("--id and --username are mutually exclusive")
	case errors.Is(err, store.ErrNotFound):
		return nil, fmt.Errorf("user not found: %s", selector)
	default:
		return nil, fmt.Errorf("get user by %s: %w", field, err)
	}
}

// mintResolvedUserID mints the typed id of a user row resolveUser returned.
//
// The id is a COLUMN, not a literal, so it cannot go through userid.MustNew.
// A blank one is corrupt data in the users table. `recover password reset`
// hands the typed id to an owner-scoped bulk revocation of sessions, API
// tokens, and delegation tokens. An unminted id unwraps to "" there, which
// MATCHES every blank-owner row instead of none, so the command would touch
// the wrong rows or report success having revoked nothing. One refusal, one
// wording, for every recover command that resolves a user by --id/--username.
func mintResolvedUserID(user *store.User) (userid.UserID, error) {
	uid, ok := userid.New(user.ID)
	if !ok {
		return userid.UserID{}, fmt.Errorf("user %q has a blank id", user.Username)
	}
	return uid, nil
}
