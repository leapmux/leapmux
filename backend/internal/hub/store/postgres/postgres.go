// Package postgres implements the Hub store backed by PostgreSQL.
// It wraps the sqlc-generated Queries, converting between
// backend-agnostic store types and sqlc-generated types.
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
)

// pgStore implements store.Store backed by PostgreSQL.
//
// Sub-stores are constructed on demand by each getter rather than
// cached on the struct — see sqlite.sqliteStore for the rationale.
type pgStore struct {
	conn *pgConn
}

var _ store.Store = (*pgStore)(nil)

type pgShared struct {
	pool        *pgxpool.Pool
	migrationDB *sql.DB // database/sql wrapper for goose migrations
	migrator    store.Migrator
}

type pgConn struct {
	shared *pgShared
	exec   gendb.DBTX // pool outside tx, pgx.Tx inside tx
	q      *gendb.Queries
}

// Open connects to a PostgreSQL database, runs migrations, and returns a Store.
func Open(ctx context.Context, cfg config.PostgresConfig) (store.Store, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("parse postgres config: %w", err)
	}
	if cfg.MaxConns > 0 {
		poolCfg.MaxConns = int32(cfg.MaxConns)
	}
	if cfg.MinConns > 0 {
		poolCfg.MinConns = int32(cfg.MinConns)
	}
	if cfg.ConnMaxLifetimeSeconds > 0 {
		poolCfg.MaxConnLifetime = time.Duration(cfg.ConnMaxLifetimeSeconds) * time.Second
	}
	if cfg.MaxConnIdleTimeSeconds > 0 {
		poolCfg.MaxConnIdleTime = time.Duration(cfg.MaxConnIdleTimeSeconds) * time.Second
	}
	if cfg.HealthCheckPeriodSeconds > 0 {
		poolCfg.HealthCheckPeriod = time.Duration(cfg.HealthCheckPeriodSeconds) * time.Second
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}

	// goose requires database/sql; wrap the pgx pool.
	sqlDB := stdlib.OpenDBFromPool(pool)

	mig, err := newMigrator(sqlDB)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("init postgres migrator: %w", err)
	}
	if err := mig.Migrate(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate postgres: %w", err)
	}

	return &pgStore{
		conn: &pgConn{
			shared: &pgShared{
				pool:        pool,
				migrationDB: sqlDB,
				migrator:    mig,
			},
			exec: pool,
			q:    gendb.New(pool),
		},
	}, nil
}

// newFromPool wraps an existing pool (already migrated) into a Store.
func newFromPool(pool *pgxpool.Pool, migrationDB *sql.DB) (*pgStore, error) {
	mig, err := newMigrator(migrationDB)
	if err != nil {
		return nil, fmt.Errorf("init postgres migrator: %w", err)
	}
	return &pgStore{
		conn: &pgConn{
			shared: &pgShared{
				pool:        pool,
				migrationDB: migrationDB,
				migrator:    mig,
			},
			exec: pool,
			q:    gendb.New(pool),
		},
	}, nil
}

func (s *pgStore) Orgs() store.OrgStore             { return &orgStore{conn: s.conn} }
func (s *pgStore) Users() store.UserStore           { return &userStore{conn: s.conn} }
func (s *pgStore) Sessions() store.SessionStore     { return &sessionStore{conn: s.conn} }
func (s *pgStore) OrgMembers() store.OrgMemberStore { return &orgMemberStore{conn: s.conn} }
func (s *pgStore) Workers() store.WorkerStore       { return &workerStore{conn: s.conn} }
func (s *pgStore) WorkerAccessGrants() store.WorkerAccessGrantStore {
	return &workerAccessGrantStore{conn: s.conn}
}
func (s *pgStore) WorkerNotifications() store.WorkerNotificationStore {
	return &workerNotificationStore{conn: s.conn}
}
func (s *pgStore) RegistrationKeys() store.RegistrationKeyStore {
	return &registrationKeyStore{conn: s.conn}
}
func (s *pgStore) Workspaces() store.WorkspaceStore { return &workspaceStore{conn: s.conn} }
func (s *pgStore) WorkspaceAccess() store.WorkspaceAccessStore {
	return &workspaceAccessStore{conn: s.conn}
}
func (s *pgStore) WorkspaceTabIndex() store.WorkspaceTabIndexStore {
	return &workspaceTabIndexStore{conn: s.conn}
}
func (s *pgStore) OrgOpBatches() store.OrgOpBatchesStore { return &orgOpBatchesStore{conn: s.conn} }
func (s *pgStore) OrgState() store.OrgStateStore         { return &orgStateStore{conn: s.conn} }
func (s *pgStore) OrgRecentBatchIDs() store.OrgRecentBatchIDStore {
	return &orgRecentBatchIDStore{conn: s.conn}
}
func (s *pgStore) LifecycleOutbox() store.LifecycleOutboxStore {
	return &lifecycleOutboxStore{conn: s.conn}
}
func (s *pgStore) WorkspaceSections() store.WorkspaceSectionStore {
	return &workspaceSectionStore{conn: s.conn}
}
func (s *pgStore) WorkspaceSectionItems() store.WorkspaceSectionItemStore {
	return &workspaceSectionItemStore{conn: s.conn}
}
func (s *pgStore) OAuthProviders() store.OAuthProviderStore { return &oauthProviderStore{conn: s.conn} }
func (s *pgStore) OAuthStates() store.OAuthStateStore       { return &oauthStateStore{conn: s.conn} }
func (s *pgStore) OAuthTokens() store.OAuthTokenStore       { return &oauthTokenStore{conn: s.conn} }
func (s *pgStore) OAuthUserLinks() store.OAuthUserLinkStore {
	return &oauthUserLinkStore{conn: s.conn}
}
func (s *pgStore) PendingOAuthSignups() store.PendingOAuthSignupStore {
	return &pendingOAuthSignupStore{conn: s.conn}
}
func (s *pgStore) APITokens() store.APITokenStore { return &apiTokenStore{conn: s.conn} }
func (s *pgStore) DelegationTokens() store.DelegationTokenStore {
	return &delegationTokenStore{conn: s.conn}
}
func (s *pgStore) DeviceAuthorizations() store.DeviceAuthorizationStore {
	return &deviceAuthorizationStore{conn: s.conn}
}
func (s *pgStore) CLIAuthorizationCodes() store.CLIAuthorizationCodeStore {
	return &cliAuthorizationCodeStore{conn: s.conn}
}
func (s *pgStore) Cleanup() store.CleanupStore { return &cleanupStore{conn: s.conn} }
func (s *pgStore) Migrator() store.Migrator    { return s.conn.shared.migrator }

func (s *pgStore) RunInTransaction(ctx context.Context, fn func(tx store.Store) error) error {
	pgxTx, err := s.conn.shared.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = pgxTx.Rollback(ctx) }()

	txStore := &pgStore{
		conn: &pgConn{
			shared: s.conn.shared,
			exec:   pgxTx,
			q:      s.conn.q.WithTx(pgxTx),
		},
	}
	if err := fn(txStore); err != nil {
		return err
	}
	return pgxTx.Commit(ctx)
}

func (s *pgStore) Close() error {
	s.conn.shared.pool.Close()
	return s.conn.shared.migrationDB.Close()
}
