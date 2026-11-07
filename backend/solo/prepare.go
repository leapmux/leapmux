package solo

import (
	"context"
	"fmt"

	internalconfig "github.com/leapmux/leapmux/internal/config"
	hubconfig "github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/lifecycle"
)

// PreparedConfig is a resolved launch: the loaded hub configuration and
// the canonical containing identity the data leases must agree with.
// Preparation is read-only -- it creates no directory and starts no
// component -- so a caller can inspect what a launch would use before
// it commits anything.
type PreparedConfig struct {
	hubCfg *hubconfig.Config
	// role is the containing mode this launch runs as.
	role lifecycle.Role
	// dataDir is the canonical containing data root.
	dataDir string
	// config retains the launch options that apply after configuration loading.
	config Config
}

// DataDirectory returns the canonical containing data root.
func (p *PreparedConfig) DataDirectory() string { return p.dataDir }

// Role returns the containing mode this launch runs as.
func (p *PreparedConfig) Role() lifecycle.Role { return p.role }

// Prepare resolves the launch for cfg: the hub configuration and the
// canonical containing identity, with no filesystem writes and no
// component starts.
func Prepare(cfg Config) (*PreparedConfig, error) {
	opts, modeName := soloLoadOptions(cfg)
	hubCfg, showVersion, err := hubconfig.LoadWithOptions(cfg.Args, opts)
	if err != nil {
		return nil, fmt.Errorf("load hub config: %w", err)
	}
	if showVersion {
		return nil, internalconfig.ErrVersionRequested
	}
	hubCfg.DevMode = cfg.DevMode
	role, err := lifecycle.ParseRole(modeName)
	if err != nil {
		return nil, err
	}
	canonical, err := lifecycle.CanonicalDir(hubCfg.DataDir)
	if err != nil {
		return nil, err
	}
	return &PreparedConfig{hubCfg: hubCfg, role: role, dataDir: canonical, config: cfg}, nil
}

// Start launches a Hub and Worker in-process, as one combined instance
// under its own data root. It is the convenience path for the desktop
// and library callers: the same preparation, the same root-lease
// acquisition, and the same prepared startup the CLI's common runtime
// drives through StartPrepared.
func Start(ctx context.Context, cfg Config) (*Instance, error) {
	prepared, err := Prepare(cfg)
	if err != nil {
		return nil, err
	}
	owner := lifecycle.NewDirectoryOwner(prepared.role, prepared.dataDir)
	root, err := lifecycle.AcquireDirectory(owner, prepared.role, prepared.dataDir)
	if err != nil {
		return nil, fmt.Errorf("acquire the %s data directory: %w", prepared.role, err)
	}
	return StartPrepared(ctx, prepared, root)
}

// StartPrepared starts the prepared launch under the supplied root
// lease. It takes ownership of the lease at the call boundary,
// INCLUDING the rejection paths: a rejected root lease is closed here
// (idempotently when already closed) and the prepared configuration is
// left untouched.
//
// A nil prepared configuration, or a nil, closed, or mismatched root
// lease, is rejected before any component lease, state access,
// registration, or listener creation runs.
func StartPrepared(ctx context.Context, prepared *PreparedConfig, root *lifecycle.DirectoryLease) (*Instance, error) {
	if prepared == nil {
		_ = root.Close()
		return nil, fmt.Errorf("solo: StartPrepared requires a prepared configuration")
	}
	if err := validateRootLease(prepared, root); err != nil {
		_ = root.Close()
		return nil, err
	}
	return start(ctx, prepared.config, defaultDeps(), prepared, root)
}

// validateRootLease states what StartPrepared accepts as its root lease.
func validateRootLease(prepared *PreparedConfig, root *lifecycle.DirectoryLease) error {
	if root == nil {
		return fmt.Errorf("solo: StartPrepared requires the root data lease")
	}
	if root.Closed() {
		return fmt.Errorf("solo: StartPrepared received a closed root lease; acquire a fresh one")
	}
	if root.Mode() != prepared.role {
		return fmt.Errorf("solo: StartPrepared requires the %s root lease, not %s", prepared.role, root.Mode())
	}
	if root.Path() != prepared.DataDirectory() {
		return fmt.Errorf("solo: the root lease holds %s, not the prepared data directory %s", root.Path(), prepared.DataDirectory())
	}
	if owner := root.Owner(); owner.AccountID != lifecycle.CurrentAccountID() || owner.Mode != prepared.role || owner.Root != prepared.DataDirectory() {
		return fmt.Errorf("solo: the root lease belongs to another instance (%s of %s)", owner.Mode, owner.Root)
	}
	return nil
}
