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
	pool    *pgxpool.Pool
	dbtx    gendb.DBTX // pool outside tx, pgx.Tx inside tx
	sqlDB   *sql.DB    // database/sql wrapper for goose migrations
	queries *gendb.Queries
	mig     store.Migrator

	orgs                  orgStore
	users                 userStore
	sessions              sessionStore
	orgMembers            orgMemberStore
	workers               workerStore
	workerAccessGrants    workerAccessGrantStore
	workerNotifications   workerNotificationStore
	registrations         registrationStore
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
		pool:    pool,
		dbtx:    pool,
		sqlDB:   sqlDB,
		queries: gendb.New(pool),
		mig:     mig,
	}
	initSubStores(st)
	return st, nil
}

// newFromPool wraps an existing pool (already migrated) into a Store.
func newFromPool(pool *pgxpool.Pool, sqlDB *sql.DB) (*pgStore, error) {
	mig, err := newMigrator(sqlDB)
	if err != nil {
		return nil, fmt.Errorf("init postgres migrator: %w", err)
	}
	st := &pgStore{
		pool:    pool,
		dbtx:    pool,
		sqlDB:   sqlDB,
		queries: gendb.New(pool),
		mig:     mig,
	}
	initSubStores(st)
	return st, nil
}

func initSubStores(s *pgStore) {
	s.orgs = orgStore{q: s.queries}
	s.users = userStore{q: s.queries}
	s.sessions = sessionStore{q: s.queries, pool: s.pool}
	s.orgMembers = orgMemberStore{q: s.queries}
	s.workers = workerStore{q: s.queries}
	s.workerAccessGrants = workerAccessGrantStore{q: s.queries}
	s.workerNotifications = workerNotificationStore{q: s.queries}
	s.registrations = registrationStore{q: s.queries}
	s.workspaces = workspaceStore{q: s.queries}
	s.workspaceAccess = workspaceAccessStore{q: s.queries}
	s.workspaceTabs = workspaceTabStore{q: s.queries, dbtx: s.dbtx}
	s.workspaceLayouts = workspaceLayoutStore{q: s.queries}
	s.workspaceSections = workspaceSectionStore{q: s.queries}
	s.workspaceSectionItems = workspaceSectionItemStore{q: s.queries}
	s.oauthProviders = oauthProviderStore{q: s.queries}
	s.oauthStates = oauthStateStore{q: s.queries}
	s.oauthTokens = oauthTokenStore{q: s.queries}
	s.oauthUserLinks = oauthUserLinkStore{q: s.queries}
	s.pendingOAuthSignups = pendingOAuthSignupStore{q: s.queries}
	s.cleanup = cleanupStore{q: s.queries}
}

func (s *pgStore) Orgs() store.OrgStore                               { return &s.orgs }
func (s *pgStore) Users() store.UserStore                             { return &s.users }
func (s *pgStore) Sessions() store.SessionStore                       { return &s.sessions }
func (s *pgStore) OrgMembers() store.OrgMemberStore                   { return &s.orgMembers }
func (s *pgStore) Workers() store.WorkerStore                         { return &s.workers }
func (s *pgStore) WorkerAccessGrants() store.WorkerAccessGrantStore   { return &s.workerAccessGrants }
func (s *pgStore) WorkerNotifications() store.WorkerNotificationStore { return &s.workerNotifications }
func (s *pgStore) Registrations() store.RegistrationStore             { return &s.registrations }
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
func (s *pgStore) Migrator() store.Migrator                           { return s.mig }

func (s *pgStore) RunInTransaction(ctx context.Context, fn func(tx store.Store) error) error {
	pgxTx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = pgxTx.Rollback(ctx) }()

	txStore := &pgStore{
		pool:    s.pool,
		dbtx:    pgxTx,
		queries: s.queries.WithTx(pgxTx),
		mig:     s.mig,
	}
	initSubStores(txStore)
	if err := fn(txStore); err != nil {
		return err
	}
	return pgxTx.Commit(ctx)
}

func (s *pgStore) Close() error {
	s.pool.Close()
	return s.sqlDB.Close()
}
