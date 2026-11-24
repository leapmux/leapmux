package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/storeopen"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// newRecoverCommand builds `leapmux recover`, the ONLY command tree that
// opens the database directly. Every other administration task runs online,
// authenticated, through `leapmux admin ...` against the
// Admin*Service RPC surface. These four commands and groups are the pieces
// that must work when the hub is stopped or locked out: first-admin
// bootstrap, password reset, schema migration, and encryption-key surgery.
func newRecoverCommand() *cobra.Command {
	group := cli.Group("recover", "Offline break-glass recovery (opens the database directly)")
	group.Long = `Offline break-glass recovery (opens the database directly).

Each command works on the database and the encryption key file of a Hub, solo or
dev instance, with no running Hub, no network call and no login. --data-dir
selects the instance; --config reads the storage settings of a Hub on PostgreSQL
or MySQL. While the Hub serves, use "leapmux admin" instead.`
	group.AddCommand(
		newBootstrapCreateAdminCommand(),
		newPasswordResetCommand(),
		cli.Group("encryption-key", "Manage encryption keys",
			newEncryptionKeyRotateCommand(),
			newEncryptionKeyRemoveCommand(),
			newEncryptionKeyReencryptCommand(),
			newEncryptionKeyRotatePepperCommand()),
		cli.Group("db", "Database utilities",
			newDBPathCommand(),
			newDBMigrateCommand(),
			newDBVersionCommand()),
	)
	return group
}

// recoverSpec declares one recover command: its leaf, without Run, and the
// check of its command line.
type recoverSpec struct {
	cli.Leaf
	// BeforeOpen runs after the parse and BEFORE the command reads the
	// configuration or opens the database. Put every check of an argument or
	// a flag here: opening the database creates the data directory and the
	// database file, and a usage error must change nothing on disk.
	BeforeOpen func(inv *cli.Invocation) error
}

// recoverConfigLeaf builds a recover command that reads the configuration of
// the instance and opens no database. The command takes --config and
// --data-dir after its own flags.
func recoverConfigLeaf(spec recoverSpec, run func(inv *cli.Invocation, cfg *config.Config) error) *cobra.Command {
	var selection recoverSelection
	leaf := spec.Leaf
	leaf.QuietUnsupported = true
	own := leaf.Flags
	leaf.Flags = func(fs *pflag.FlagSet) {
		if own != nil {
			own(fs)
		}
		selection.register(fs)
	}
	leaf.Run = func(inv *cli.Invocation) (any, error) {
		if spec.BeforeOpen != nil {
			if err := spec.BeforeOpen(inv); err != nil {
				return nil, err
			}
		}
		cfg, err := selection.load()
		if err != nil {
			return nil, err
		}
		if inv.Printer != nil && inv.Printer.Format != cli.FormatText {
			var buf bytes.Buffer
			origOut := inv.Printer.Out
			inv.Printer.Out = &buf
			runErr := run(inv, cfg)
			inv.Printer.Out = origOut
			if runErr != nil {
				return nil, runErr
			}
			return struct {
				OK      bool   `json:"ok"`
				Command string `json:"command"`
				Message string `json:"message,omitempty"`
			}{
				OK:      true,
				Command: inv.Command.CommandPath(),
				Message: strings.TrimSpace(buf.String()),
			}, nil
		}
		return nil, run(inv, cfg)
	}
	return cli.NewLeaf(leaf)
}

// recoverSelection binds the flags that select the database of a recover
// command.
//
// A recover command serves a Hub, a solo and a dev instance alike, and each
// mode has a default configuration file of its own. So the command reads a
// configuration file only when --config gives one: the default file of one
// mode would select the wrong database for the data directory of another.
type recoverSelection struct {
	configFile string
	dataDir    string
}

func (r *recoverSelection) register(fs *pflag.FlagSet) {
	fs.StringVar(&r.dataDir, "data-dir", "", "data directory of the instance; the data_dir of --config, or "+config.DefaultHubDataDirHelp+" without --config")
	fs.StringVar(&r.configFile, "config", "", "configuration file whose storage settings select the database, for a Hub on PostgreSQL or MySQL; without it, the command opens the SQLite database in --data-dir")
}

// load builds the configuration that the selection gives. With --config, it
// reads that file and the LEAPMUX_HUB_ variables, as the Hub does, and
// --data-dir still replaces the data directory of the file. Without --config,
// it selects the SQLite database in the data directory.
func (r *recoverSelection) load() (*config.Config, error) {
	if r.configFile == "" {
		dataDir := r.dataDir
		if dataDir == "" {
			dataDir = config.DefaultHubDataDir()
		}
		return &config.Config{DataDir: dataDir}, nil
	}
	// The loader skips an absent file without a word, which is right for a
	// default path and wrong for a path that the operator typed: a typo would
	// open the SQLite database of the data directory instead.
	if _, err := os.Stat(r.configFile); err != nil {
		return nil, cli.Usagef("cannot read the configuration file %s: %v", r.configFile, err)
	}
	cfg, err := config.LoadWithOptions([]string{"--config", r.configFile}, config.LoadOptions{})
	if err != nil {
		return nil, fmt.Errorf("load config from %s: %w", r.configFile, err)
	}
	if r.dataDir != "" {
		cfg.DataDir = r.dataDir
	}
	return cfg, nil
}

// loadExistingKeystore loads an existing encryption.key file, reporting a
// clear operator-facing hint when the file is missing while preserving parse
// and permission errors verbatim.
func loadExistingKeystore(keyPath string) (*keystore.Keystore, error) {
	ks, err := keystore.LoadFromFile(keyPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, cli.Wrap(cli.CodeNotFound, err, fmt.Sprintf("no encryption key file at %s", keyPath)).
				WithHint("start the Hub once to generate it, or select the Hub with --data-dir or --config")
		}
		return nil, fmt.Errorf("load encryption key from %s: %w", keyPath, err)
	}
	return ks, nil
}

// recoverStoreLeaf builds a recover command that loads or generates
// encryption.key, opens and migrates the database (with built-in seeding), and
// calls run. Use this solely for first-run bootstrap (`recover create-admin`).
func recoverStoreLeaf(spec recoverSpec, run func(inv *cli.Invocation, cfg *config.Config, st store.Store) error) *cobra.Command {
	return recoverConfigLeaf(spec, func(inv *cli.Invocation, cfg *config.Config) error {
		// The hub creates the data dir at startup; recover commands run
		// before (or instead of) it, so create it here for the same
		// out-of-the-box behavior (Validate is MkdirAll + storage checks,
		// exactly what NewServer runs).
		if err := cfg.Validate(); err != nil {
			return fmt.Errorf("validate config: %w", err)
		}
		ks, err := keystore.LoadOrGenerate(cfg.EncryptionKeyFilePath())
		if err != nil {
			return fmt.Errorf("load encryption keystore: %w", err)
		}
		st, err := storeopen.Open(inv.Context, cfg, ks)
		if err != nil {
			return err
		}
		defer func() { _ = st.Close() }()
		return run(inv, cfg, st)
	})
}

// recoverExistingStoreLeaf builds a recover command that opens an existing Hub
// store without running migrations or seeding built-in rows, and loads
// encryption.key via loadExistingKeystore so a missing key file fails loudly
// instead of generating a new key that cannot decrypt existing rows.
func recoverExistingStoreLeaf(spec recoverSpec, run func(inv *cli.Invocation, cfg *config.Config, st store.Store, ks *keystore.Keystore) error) *cobra.Command {
	return recoverConfigLeaf(spec, func(inv *cli.Invocation, cfg *config.Config) error {
		if err := cfg.Validate(); err != nil {
			return fmt.Errorf("validate config: %w", err)
		}
		ks, err := loadExistingKeystore(cfg.EncryptionKeyFilePath())
		if err != nil {
			return err
		}
		st, err := storeopen.OpenWithoutMigrate(inv.Context, cfg, ks)
		if err != nil {
			return err
		}
		defer func() { _ = st.Close() }()
		return run(inv, cfg, st, ks)
	})
}

// resolveUser returns the user that sel selects. The selector RULE lives in
// service.ResolveUserSelector, shared with the online admin RPCs, so the two
// surfaces cannot disagree about what a selector selects. The WORDING stays
// here: it speaks to a person at a terminal and repeats the <user> that the
// person typed.
func resolveUser(ctx context.Context, st store.Store, sel *leapmuxv1.UserSelector, typed string) (*store.User, error) {
	user, err := service.ResolveUserSelector(ctx, st, sel)
	switch {
	case err == nil:
		return user, nil
	case errors.Is(err, store.ErrNotFound):
		return nil, cli.Newf(cli.CodeNotFound, "no user %q", typed)
	default:
		return nil, fmt.Errorf("look up the user %q: %w", typed, err)
	}
}

// mintResolvedUserID mints the typed id of a user row resolveUser returned.
//
// The id is a COLUMN, not a literal, so it cannot go through userid.MustNew.
// A blank one is corrupt data in the users table. `recover reset-password`
// hands the typed id to an owner-scoped bulk revocation of sessions, API
// tokens, and delegation tokens. An unminted id unwraps to "" there, which
// MATCHES every blank-owner row instead of none, so the command would touch
// the wrong rows or report success having revoked nothing. One refusal, one
// wording, for every recover command that resolves a <user>.
func mintResolvedUserID(user *store.User) (userid.UserID, error) {
	uid, ok := userid.New(user.ID)
	if !ok {
		return userid.UserID{}, fmt.Errorf("user %q has a blank id", user.Username)
	}
	return uid, nil
}
