package sqlutil

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/pressly/goose/v3"
)

var _ store.Migrator = (*GooseMigrator)(nil)

// GooseMigrator wraps a goose.Provider for thread-safe schema migrations.
type GooseMigrator struct {
	provider *goose.Provider
}

// NewGooseMigrator creates a Migrator backed by a goose.Provider instance.
// Unlike the legacy goose global-state API, each provider is independent
// and safe for concurrent use.
func NewGooseMigrator(dialect goose.Dialect, db *sql.DB, fsys fs.FS) (*GooseMigrator, error) {
	provider, err := goose.NewProvider(dialect, db, fsys)
	if err != nil {
		return nil, fmt.Errorf("create goose provider: %w", err)
	}
	return &GooseMigrator{provider: provider}, nil
}

func (m *GooseMigrator) CurrentVersion(ctx context.Context) (int64, error) {
	v, err := m.provider.GetDBVersion(ctx)
	if err != nil {
		return 0, fmt.Errorf("get version: %w", err)
	}
	return v, nil
}

func (m *GooseMigrator) LatestVersion() int64 {
	sources := m.provider.ListSources()
	if len(sources) == 0 {
		return 0
	}
	return sources[len(sources)-1].Version
}

func (m *GooseMigrator) Migrate(ctx context.Context) error {
	if _, err := m.provider.Up(ctx); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}

func (m *GooseMigrator) MigrateTo(ctx context.Context, version int64) error {
	current, err := m.provider.GetDBVersion(ctx)
	if err != nil {
		return fmt.Errorf("get version: %w", err)
	}
	if version > current {
		if _, err := m.provider.UpTo(ctx, version); err != nil {
			return err
		}
	} else if version < current {
		if _, err := m.provider.DownTo(ctx, version); err != nil {
			return err
		}
	}
	return nil
}
