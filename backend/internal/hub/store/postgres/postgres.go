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
type pgStore struct {
	conn *pgConn

	orgs                  orgStore
	users                 userStore
	sessions              sessionStore
	orgMembers            orgMemberStore
	workers               workerStore
	workerAccessGrants    workerAccessGrantStore
	workerNotifications   workerNotificationStore
	registrationKeys      registrationKeyStore
	workspaces            workspaceStore
	workspaceAccess       workspaceAccessStore
	workspaceTabs         workspaceTabStore
	workspaceLayouts      workspaceLayoutStore
	workspaceSections     workspaceSectionStore
	workspaceSectionItems workspaceSectionItemStore
	oauthProviders        oauthProviderStore
	oauthStates           oauthStateStore
	oauthTokens           oauthTokenStore
	oauthUserLinks        oauthUserLinkStore
	pendingOAuthSignups   pendingOAuthSignupStore
	cleanup               cleanupStore
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

	st := &pgStore{
		conn: &pgConn{
			shared: &pgShared{
				pool:        pool,
				migrationDB: sqlDB,
				migrator:    mig,
			},
			exec: pool,
			q:    gendb.New(pool),
		},
	}
	initSubStores(st)
	return st, nil
}

// newFromPool wraps an existing pool (already migrated) into a Store.
func newFromPool(pool *pgxpool.Pool, migrationDB *sql.DB) (*pgStore, error) {
	mig, err := newMigrator(migrationDB)
	if err != nil {
		return nil, fmt.Errorf("init postgres migrator: %w", err)
	}
	st := &pgStore{
		conn: &pgConn{
			shared: &pgShared{
				pool:        pool,
				migrationDB: migrationDB,
				migrator:    mig,
			},
			exec: pool,
			q:    gendb.New(pool),
		},
	}
	initSubStores(st)
	return st, nil
}

func initSubStores(s *pgStore) {
	s.orgs = orgStore{conn: s.conn}
	s.users = userStore{conn: s.conn}
	s.sessions = sessionStore{conn: s.conn}
	s.orgMembers = orgMemberStore{conn: s.conn}
	s.workers = workerStore{conn: s.conn}
	s.workerAccessGrants = workerAccessGrantStore{conn: s.conn}
	s.workerNotifications = workerNotificationStore{conn: s.conn}
	s.registrationKeys = registrationKeyStore{conn: s.conn}
	s.workspaces = workspaceStore{conn: s.conn}
	s.workspaceAccess = workspaceAccessStore{conn: s.conn}
	s.workspaceTabs = workspaceTabStore{conn: s.conn}
	s.workspaceLayouts = workspaceLayoutStore{conn: s.conn}
	s.workspaceSections = workspaceSectionStore{conn: s.conn}
	s.workspaceSectionItems = workspaceSectionItemStore{conn: s.conn}
	s.oauthProviders = oauthProviderStore{conn: s.conn}
	s.oauthStates = oauthStateStore{conn: s.conn}
	s.oauthTokens = oauthTokenStore{conn: s.conn}
	s.oauthUserLinks = oauthUserLinkStore{conn: s.conn}
	s.pendingOAuthSignups = pendingOAuthSignupStore{conn: s.conn}
	s.cleanup = cleanupStore{conn: s.conn}
}

func (s *pgStore) Orgs() store.OrgStore                               { return &s.orgs }
func (s *pgStore) Users() store.UserStore                             { return &s.users }
func (s *pgStore) Sessions() store.SessionStore                       { return &s.sessions }
func (s *pgStore) OrgMembers() store.OrgMemberStore                   { return &s.orgMembers }
func (s *pgStore) Workers() store.WorkerStore                         { return &s.workers }
func (s *pgStore) WorkerAccessGrants() store.WorkerAccessGrantStore   { return &s.workerAccessGrants }
func (s *pgStore) WorkerNotifications() store.WorkerNotificationStore { return &s.workerNotifications }
func (s *pgStore) RegistrationKeys() store.RegistrationKeyStore       { return &s.registrationKeys }
func (s *pgStore) Workspaces() store.WorkspaceStore                   { return &s.workspaces }
func (s *pgStore) WorkspaceAccess() store.WorkspaceAccessStore        { return &s.workspaceAccess }
func (s *pgStore) WorkspaceTabs() store.WorkspaceTabStore             { return &s.workspaceTabs }
func (s *pgStore) WorkspaceLayouts() store.WorkspaceLayoutStore       { return &s.workspaceLayouts }
func (s *pgStore) WorkspaceSections() store.WorkspaceSectionStore     { return &s.workspaceSections }
func (s *pgStore) WorkspaceSectionItems() store.WorkspaceSectionItemStore {
	return &s.workspaceSectionItems
}
func (s *pgStore) OAuthProviders() store.OAuthProviderStore           { return &s.oauthProviders }
func (s *pgStore) OAuthStates() store.OAuthStateStore                 { return &s.oauthStates }
func (s *pgStore) OAuthTokens() store.OAuthTokenStore                 { return &s.oauthTokens }
func (s *pgStore) OAuthUserLinks() store.OAuthUserLinkStore           { return &s.oauthUserLinks }
func (s *pgStore) PendingOAuthSignups() store.PendingOAuthSignupStore { return &s.pendingOAuthSignups }
func (s *pgStore) Cleanup() store.CleanupStore                        { return &s.cleanup }
func (s *pgStore) Migrator() store.Migrator                           { return s.conn.shared.migrator }

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
	initSubStores(txStore)
	if err := fn(txStore); err != nil {
		return err
	}
	return pgxTx.Commit(ctx)
}

func (s *pgStore) Close() error {
	s.conn.shared.pool.Close()
	return s.conn.shared.migrationDB.Close()
}
