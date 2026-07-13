// Package mysql implements the Hub store backed by MySQL.
// It wraps the sqlc-generated Queries, converting between
// backend-agnostic store types and sqlc-generated types.
package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
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
// "user:password@tcp(host:port)/dbname". Open forces parseTime,
// loc=UTC, and session time_zone='+00:00' because the schema stores
// revocation cursors in DATETIME columns and compares them directly.
func Open(cfg config.MySQLConfig) (store.Store, error) {
	dsn, err := normalizeMySQLDSN(cfg.DSN)
	if err != nil {
		return nil, err
	}
	sqlDB, err := sql.Open("mysql", dsn)
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

func normalizeMySQLDSN(dsn string) (string, error) {
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		return "", fmt.Errorf("parse mysql dsn: %w", err)
	}
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	// Force CLIENT_FOUND_ROWS so an UPDATE reports the rows its WHERE MATCHED
	// rather than the rows it CHANGED. sqlite's changes() and postgres's command
	// tag both count matched rows, so this makes a no-op UPDATE (e.g. re-stamping
	// a session already at the target auth_generation, or renaming to the current
	// name) return a consistent rows-affected across all three backends. Without
	// it, the shared rows-affected == 1 guards would spuriously see 0 on a
	// matched-but-unchanged row on MySQL only. Enforced here -- overriding any
	// user-supplied value -- alongside the other invariants so behavior cannot
	// drift by deployment DSN.
	cfg.ClientFoundRows = true
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["time_zone"] = "'+00:00'"
	return cfg.FormatDSN(), nil
}

func (s *mysqlStore) Orgs() store.OrgStore         { return &orgStore{conn: s.conn} }
func (s *mysqlStore) Users() store.UserStore       { return &userStore{conn: s.conn} }
func (s *mysqlStore) Sessions() store.SessionStore { return &sessionStore{conn: s.conn} }
func (s *mysqlStore) Workers() store.WorkerStore   { return &workerStore{conn: s.conn} }
func (s *mysqlStore) WorkerNotifications() store.WorkerNotificationStore {
	return &workerNotificationStore{conn: s.conn}
}
func (s *mysqlStore) RegistrationKeys() store.RegistrationKeyStore {
	return &registrationKeyStore{conn: s.conn}
}
func (s *mysqlStore) Workspaces() store.WorkspaceStore { return &workspaceStore{conn: s.conn} }
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
func (s *mysqlStore) RevocationEvents() store.RevocationEventStore {
	return newRevocationEventStore(s.conn)
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
	if s.conn.inTx() {
		return fn(s)
	}
	return s.conn.withTransaction(ctx, func(conn *mysqlConn) error {
		return fn(&mysqlStore{conn: conn})
	})
}

func (s *mysqlStore) RunInUserAuthTransaction(ctx context.Context, userID string, fn func(tx store.Store) error) error {
	return s.conn.withTransaction(ctx, func(conn *mysqlConn) error {
		if _, err := conn.q.LockUserAuthState(ctx, userID); err != nil {
			return mapErr(err)
		}
		return fn(&mysqlStore{conn: conn})
	})
}

func (c *mysqlConn) inTx() bool {
	_, ok := c.exec.(*sql.Tx)
	return ok
}

func (c *mysqlConn) withTransaction(ctx context.Context, fn func(tx *mysqlConn) error) error {
	if c.inTx() {
		return fn(c)
	}
	tx, err := c.shared.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	txConn := &mysqlConn{
		shared: c.shared,
		exec:   tx,
		q:      c.q.WithTx(tx),
	}
	if err := fn(txConn); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *mysqlStore) Close() error {
	return s.conn.shared.db.Close()
}
