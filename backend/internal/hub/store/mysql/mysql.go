// Package mysql implements the Hub store backed by MySQL.
// It wraps the sqlc-generated Queries, converting between
// backend-agnostic store types and sqlc-generated types.
package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
)

// mysqlStore implements store.Store backed by MySQL.
//
// Sub-stores are constructed on demand by each getter rather than
// cached on the struct — see sqlite.sqliteStore for the rationale.
type mysqlStore struct {
	conn *mysqlConn
}

var _ store.Store = (*mysqlStore)(nil)

type mysqlShared struct {
	db       *sql.DB
	migrator store.Migrator
}

type mysqlConn struct {
	shared *mysqlShared
	exec   gendb.DBTX // *sql.DB outside tx, *sql.Tx inside tx
	q      *gendb.Queries
}

// Open opens a MySQL database, runs migrations, and returns a Store.
// The DSN should be a go-sql-driver/mysql DSN string, e.g.
// "user:password@tcp(host:port)/dbname?parseTime=true".
func Open(cfg config.MySQLConfig) (store.Store, error) {
	sqlDB, err := sql.Open("mysql", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}

	if cfg.MaxConns > 0 {
		sqlDB.SetMaxOpenConns(cfg.MaxConns)
	}
	if cfg.MaxIdleConns > 0 {
		sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	}
	if cfg.ConnMaxLifetimeSeconds > 0 {
		sqlDB.SetConnMaxLifetime(time.Duration(cfg.ConnMaxLifetimeSeconds) * time.Second)
	}
	if cfg.ConnMaxIdleTimeSeconds > 0 {
		sqlDB.SetConnMaxIdleTime(time.Duration(cfg.ConnMaxIdleTimeSeconds) * time.Second)
	}

	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping mysql: %w", err)
	}

	// Best-effort: enable FK support for TiDB (silently ignored on real MySQL).
	_, _ = sqlDB.ExecContext(context.Background(), "SET GLOBAL tidb_enable_foreign_key = ON")

	mig, err := newMigrator(sqlDB)
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("init mysql migrator: %w", err)
	}
	if err := mig.Migrate(context.Background()); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("migrate mysql: %w", err)
	}

	return &mysqlStore{
		conn: &mysqlConn{
			shared: &mysqlShared{
				db:       sqlDB,
				migrator: mig,
			},
			exec: sqlDB,
			q:    gendb.New(sqlDB),
		},
	}, nil
}

// NewFromDB wraps an existing *sql.DB (already opened and migrated) into a
// Store. The returned Store takes ownership of the DB handle; calling Close
// on the Store will close the underlying *sql.DB.
func NewFromDB(sqlDB *sql.DB) (store.Store, error) {
	mig, err := newMigrator(sqlDB)
	if err != nil {
		return nil, fmt.Errorf("init mysql migrator: %w", err)
	}
	return &mysqlStore{
		conn: &mysqlConn{
			shared: &mysqlShared{
				db:       sqlDB,
				migrator: mig,
			},
			exec: sqlDB,
			q:    gendb.New(sqlDB),
		},
	}, nil
}

func (s *mysqlStore) Orgs() store.OrgStore             { return &orgStore{conn: s.conn} }
func (s *mysqlStore) Users() store.UserStore           { return &userStore{conn: s.conn} }
func (s *mysqlStore) Sessions() store.SessionStore     { return &sessionStore{conn: s.conn} }
func (s *mysqlStore) OrgMembers() store.OrgMemberStore { return &orgMemberStore{conn: s.conn} }
func (s *mysqlStore) Workers() store.WorkerStore       { return &workerStore{conn: s.conn} }
func (s *mysqlStore) WorkerAccessGrants() store.WorkerAccessGrantStore {
	return &workerAccessGrantStore{conn: s.conn}
}
func (s *mysqlStore) WorkerNotifications() store.WorkerNotificationStore {
	return &workerNotificationStore{conn: s.conn}
}
func (s *mysqlStore) RegistrationKeys() store.RegistrationKeyStore {
	return &registrationKeyStore{conn: s.conn}
}
func (s *mysqlStore) Workspaces() store.WorkspaceStore { return &workspaceStore{conn: s.conn} }
func (s *mysqlStore) WorkspaceAccess() store.WorkspaceAccessStore {
	return &workspaceAccessStore{conn: s.conn}
}
func (s *mysqlStore) WorkspaceTabIndex() store.WorkspaceTabIndexStore {
	return &workspaceTabIndexStore{conn: s.conn}
}
func (s *mysqlStore) OrgOpBatches() store.OrgOpBatchesStore {
	return &orgOpBatchesStore{conn: s.conn}
}
func (s *mysqlStore) OrgState() store.OrgStateStore { return &orgStateStore{conn: s.conn} }
func (s *mysqlStore) OrgRecentBatchIDs() store.OrgRecentBatchIDStore {
	return &orgRecentBatchIDStore{conn: s.conn}
}
func (s *mysqlStore) LifecycleOutbox() store.LifecycleOutboxStore {
	return &lifecycleOutboxStore{conn: s.conn}
}
func (s *mysqlStore) WorkspaceSections() store.WorkspaceSectionStore {
	return &workspaceSectionStore{conn: s.conn}
}
func (s *mysqlStore) WorkspaceSectionItems() store.WorkspaceSectionItemStore {
	return &workspaceSectionItemStore{conn: s.conn}
}
func (s *mysqlStore) OAuthProviders() store.OAuthProviderStore {
	return &oauthProviderStore{conn: s.conn}
}
func (s *mysqlStore) OAuthStates() store.OAuthStateStore { return &oauthStateStore{conn: s.conn} }
func (s *mysqlStore) OAuthTokens() store.OAuthTokenStore { return &oauthTokenStore{conn: s.conn} }
func (s *mysqlStore) OAuthUserLinks() store.OAuthUserLinkStore {
	return &oauthUserLinkStore{conn: s.conn}
}
func (s *mysqlStore) PendingOAuthSignups() store.PendingOAuthSignupStore {
	return &pendingOAuthSignupStore{conn: s.conn}
}
func (s *mysqlStore) APITokens() store.APITokenStore { return &apiTokenStore{conn: s.conn} }
func (s *mysqlStore) DelegationTokens() store.DelegationTokenStore {
	return &delegationTokenStore{conn: s.conn}
}
func (s *mysqlStore) DeviceAuthorizations() store.DeviceAuthorizationStore {
	return &deviceAuthorizationStore{conn: s.conn}
}
func (s *mysqlStore) CLIAuthorizationCodes() store.CLIAuthorizationCodeStore {
	return &cliAuthorizationCodeStore{conn: s.conn}
}
func (s *mysqlStore) Cleanup() store.CleanupStore { return &cleanupStore{conn: s.conn} }
func (s *mysqlStore) Migrator() store.Migrator    { return s.conn.shared.migrator }

func (s *mysqlStore) RunInTransaction(ctx context.Context, fn func(tx store.Store) error) error {
	tx, err := s.conn.shared.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	txStore := &mysqlStore{
		conn: &mysqlConn{
			shared: s.conn.shared,
			exec:   tx,
			q:      s.conn.q.WithTx(tx),
		},
	}
	if err := fn(txStore); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *mysqlStore) Close() error {
	return s.conn.shared.db.Close()
}
