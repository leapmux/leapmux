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
	sqlDB   *sql.DB
	dbtx    gendb.DBTX // sqlDB outside tx, *sql.Tx inside tx
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

var _ store.Store = (*sqliteStore)(nil)

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
		sqlDB:   sqlDB,
		dbtx:    sqlDB,
		queries: gendb.New(sqlDB),
		mig:     mig,
	}
	initSubStores(st)
	return st, nil
}

// NewFromDB wraps an existing *sql.DB (already opened and migrated) into a
// Store. The caller retains ownership of the DB; calling Close on the returned
// Store will close the underlying *sql.DB.
func NewFromDB(sqlDB *sql.DB) (store.Store, error) {
	mig, err := newMigrator(sqlDB)
	if err != nil {
		return nil, fmt.Errorf("init sqlite migrator: %w", err)
	}
	st := &sqliteStore{
		sqlDB:   sqlDB,
		dbtx:    sqlDB,
		queries: gendb.New(sqlDB),
		mig:     mig,
	}
	initSubStores(st)
	return st, nil
}

func initSubStores(s *sqliteStore) {
	s.orgs = orgStore{q: s.queries}
	s.users = userStore{q: s.queries}
	s.sessions = sessionStore{q: s.queries, db: s.sqlDB}
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

func (s *sqliteStore) Orgs() store.OrgStore                             { return &s.orgs }
func (s *sqliteStore) Users() store.UserStore                           { return &s.users }
func (s *sqliteStore) Sessions() store.SessionStore                     { return &s.sessions }
func (s *sqliteStore) OrgMembers() store.OrgMemberStore                 { return &s.orgMembers }
func (s *sqliteStore) Workers() store.WorkerStore                       { return &s.workers }
func (s *sqliteStore) WorkerAccessGrants() store.WorkerAccessGrantStore { return &s.workerAccessGrants }
func (s *sqliteStore) WorkerNotifications() store.WorkerNotificationStore {
	return &s.workerNotifications
}
func (s *sqliteStore) Registrations() store.RegistrationStore         { return &s.registrations }
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
func (s *sqliteStore) Migrator() store.Migrator    { return s.mig }

func (s *sqliteStore) RunInTransaction(ctx context.Context, fn func(tx store.Store) error) error {
	tx, err := s.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	txStore := &sqliteStore{
		sqlDB:   s.sqlDB,
		dbtx:    tx,
		queries: s.queries.WithTx(tx),
		mig:     s.mig,
	}
	initSubStores(txStore)
	if err := fn(txStore); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *sqliteStore) Close() error {
	// Flush the WAL before close to avoid a large WAL file on the next open.
	if _, err := s.sqlDB.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		// Log but don't fail — the close itself is more important.
		slog.Warn("WAL checkpoint failed", "error", err)
	}
	return s.sqlDB.Close()
}
