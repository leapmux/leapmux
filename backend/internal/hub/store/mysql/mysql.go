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
type mysqlStore struct {
	conn *mysqlConn

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

	st := &mysqlStore{
		conn: &mysqlConn{
			shared: &mysqlShared{
				db:       sqlDB,
				migrator: mig,
			},
			exec: sqlDB,
			q:    gendb.New(sqlDB),
		},
	}
	initSubStores(st)
	return st, nil
}

// NewFromDB wraps an existing *sql.DB (already opened and migrated) into a
// Store. The returned Store takes ownership of the DB handle; calling Close
// on the Store will close the underlying *sql.DB.
func NewFromDB(sqlDB *sql.DB) (store.Store, error) {
	mig, err := newMigrator(sqlDB)
	if err != nil {
		return nil, fmt.Errorf("init mysql migrator: %w", err)
	}
	st := &mysqlStore{
		conn: &mysqlConn{
			shared: &mysqlShared{
				db:       sqlDB,
				migrator: mig,
			},
			exec: sqlDB,
			q:    gendb.New(sqlDB),
		},
	}
	initSubStores(st)
	return st, nil
}

func initSubStores(s *mysqlStore) {
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

func (s *mysqlStore) Orgs() store.OrgStore                             { return &s.orgs }
func (s *mysqlStore) Users() store.UserStore                           { return &s.users }
func (s *mysqlStore) Sessions() store.SessionStore                     { return &s.sessions }
func (s *mysqlStore) OrgMembers() store.OrgMemberStore                 { return &s.orgMembers }
func (s *mysqlStore) Workers() store.WorkerStore                       { return &s.workers }
func (s *mysqlStore) WorkerAccessGrants() store.WorkerAccessGrantStore { return &s.workerAccessGrants }
func (s *mysqlStore) WorkerNotifications() store.WorkerNotificationStore {
	return &s.workerNotifications
}
func (s *mysqlStore) RegistrationKeys() store.RegistrationKeyStore   { return &s.registrationKeys }
func (s *mysqlStore) Workspaces() store.WorkspaceStore               { return &s.workspaces }
func (s *mysqlStore) WorkspaceAccess() store.WorkspaceAccessStore    { return &s.workspaceAccess }
func (s *mysqlStore) WorkspaceTabs() store.WorkspaceTabStore         { return &s.workspaceTabs }
func (s *mysqlStore) WorkspaceLayouts() store.WorkspaceLayoutStore   { return &s.workspaceLayouts }
func (s *mysqlStore) WorkspaceSections() store.WorkspaceSectionStore { return &s.workspaceSections }
func (s *mysqlStore) WorkspaceSectionItems() store.WorkspaceSectionItemStore {
	return &s.workspaceSectionItems
}
func (s *mysqlStore) OAuthProviders() store.OAuthProviderStore { return &s.oauthProviders }
func (s *mysqlStore) OAuthStates() store.OAuthStateStore       { return &s.oauthStates }
func (s *mysqlStore) OAuthTokens() store.OAuthTokenStore       { return &s.oauthTokens }
func (s *mysqlStore) OAuthUserLinks() store.OAuthUserLinkStore { return &s.oauthUserLinks }
func (s *mysqlStore) PendingOAuthSignups() store.PendingOAuthSignupStore {
	return &s.pendingOAuthSignups
}
func (s *mysqlStore) Cleanup() store.CleanupStore { return &s.cleanup }
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
	initSubStores(txStore)
	if err := fn(txStore); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *mysqlStore) Close() error {
	return s.conn.shared.db.Close()
}
