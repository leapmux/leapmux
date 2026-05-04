// Package sqlite implements the Hub store backed by SQLite.
// It wraps the sqlc-generated Queries, converting between
// backend-agnostic store types and sqlc-generated types.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
)

// sqliteStore implements store.Store backed by SQLite.
type sqliteStore struct {
	conn *sqliteConn

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

var _ store.Store = (*sqliteStore)(nil)

type sqliteShared struct {
	db       *sql.DB
	migrator store.Migrator
}

type sqliteConn struct {
	shared *sqliteShared
	exec   gendb.DBTX // *sql.DB outside tx, *sql.Tx inside tx
	q      *gendb.Queries
}

// Open opens a SQLite database, runs migrations, and returns a Store.
func Open(path string, cfg sqlitedb.Config) (store.Store, error) {
	sqlDB, err := OpenDB(path, cfg)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	mig, err := newMigrator(sqlDB)
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("init sqlite migrator: %w", err)
	}
	if err := mig.Migrate(context.Background()); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("migrate sqlite: %w", err)
	}

	st := &sqliteStore{
		conn: &sqliteConn{
			shared: &sqliteShared{
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
		return nil, fmt.Errorf("init sqlite migrator: %w", err)
	}
	st := &sqliteStore{
		conn: &sqliteConn{
			shared: &sqliteShared{
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

func initSubStores(s *sqliteStore) {
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

func (s *sqliteStore) Orgs() store.OrgStore                             { return &s.orgs }
func (s *sqliteStore) Users() store.UserStore                           { return &s.users }
func (s *sqliteStore) Sessions() store.SessionStore                     { return &s.sessions }
func (s *sqliteStore) OrgMembers() store.OrgMemberStore                 { return &s.orgMembers }
func (s *sqliteStore) Workers() store.WorkerStore                       { return &s.workers }
func (s *sqliteStore) WorkerAccessGrants() store.WorkerAccessGrantStore { return &s.workerAccessGrants }
func (s *sqliteStore) WorkerNotifications() store.WorkerNotificationStore {
	return &s.workerNotifications
}
func (s *sqliteStore) RegistrationKeys() store.RegistrationKeyStore   { return &s.registrationKeys }
func (s *sqliteStore) Workspaces() store.WorkspaceStore               { return &s.workspaces }
func (s *sqliteStore) WorkspaceAccess() store.WorkspaceAccessStore    { return &s.workspaceAccess }
func (s *sqliteStore) WorkspaceTabs() store.WorkspaceTabStore         { return &s.workspaceTabs }
func (s *sqliteStore) WorkspaceLayouts() store.WorkspaceLayoutStore   { return &s.workspaceLayouts }
func (s *sqliteStore) WorkspaceSections() store.WorkspaceSectionStore { return &s.workspaceSections }
func (s *sqliteStore) WorkspaceSectionItems() store.WorkspaceSectionItemStore {
	return &s.workspaceSectionItems
}
func (s *sqliteStore) OAuthProviders() store.OAuthProviderStore { return &s.oauthProviders }
func (s *sqliteStore) OAuthStates() store.OAuthStateStore       { return &s.oauthStates }
func (s *sqliteStore) OAuthTokens() store.OAuthTokenStore       { return &s.oauthTokens }
func (s *sqliteStore) OAuthUserLinks() store.OAuthUserLinkStore { return &s.oauthUserLinks }
func (s *sqliteStore) PendingOAuthSignups() store.PendingOAuthSignupStore {
	return &s.pendingOAuthSignups
}
func (s *sqliteStore) Cleanup() store.CleanupStore { return &s.cleanup }
func (s *sqliteStore) Migrator() store.Migrator    { return s.conn.shared.migrator }

func (s *sqliteStore) RunInTransaction(ctx context.Context, fn func(tx store.Store) error) error {
	tx, err := s.conn.shared.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	txStore := &sqliteStore{
		conn: &sqliteConn{
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

func (s *sqliteStore) Close() error {
	// Flush the WAL before close to avoid a large WAL file on the next open.
	if _, err := s.conn.shared.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		// Log but don't fail — the close itself is more important.
		slog.Warn("WAL checkpoint failed", "error", err)
	}
	return s.conn.shared.db.Close()
}
