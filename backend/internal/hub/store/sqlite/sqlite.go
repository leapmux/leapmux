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
//
// Sub-stores are constructed on demand by each getter rather than
// cached on the struct: a transaction-scoped store would otherwise
// allocate all 26 sub-store structs up front (most unused per tx), and
// a fresh `&{conn}` per call is cheaper than the prior 26-field batch
// for any tx that touches fewer than ~26 tables.
type sqliteStore struct {
	conn *sqliteConn
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

	return &sqliteStore{
		conn: &sqliteConn{
			shared: &sqliteShared{
				db:       sqlDB,
				migrator: mig,
			},
			exec: sqlDB,
			q:    gendb.New(sqlDB),
		},
	}, nil
}

func (s *sqliteStore) Orgs() store.OrgStore         { return &orgStore{conn: s.conn} }
func (s *sqliteStore) Users() store.UserStore       { return &userStore{conn: s.conn} }
func (s *sqliteStore) Sessions() store.SessionStore { return &sessionStore{conn: s.conn} }
func (s *sqliteStore) Workers() store.WorkerStore   { return &workerStore{conn: s.conn} }
func (s *sqliteStore) WorkerNotifications() store.WorkerNotificationStore {
	return &workerNotificationStore{conn: s.conn}
}
func (s *sqliteStore) RegistrationKeys() store.RegistrationKeyStore {
	return &registrationKeyStore{conn: s.conn}
}
func (s *sqliteStore) Workspaces() store.WorkspaceStore { return &workspaceStore{conn: s.conn} }
func (s *sqliteStore) WorkspaceTabIndex() store.WorkspaceTabIndexStore {
	return &workspaceTabIndexStore{conn: s.conn}
}
func (s *sqliteStore) OrgOpBatches() store.OrgOpBatchesStore {
	return &orgOpBatchesStore{conn: s.conn}
}
func (s *sqliteStore) OrgState() store.OrgStateStore { return &orgStateStore{conn: s.conn} }
func (s *sqliteStore) OrgRecentBatchIDs() store.OrgRecentBatchIDStore {
	return &orgRecentBatchIDStore{conn: s.conn}
}
func (s *sqliteStore) LifecycleOutbox() store.LifecycleOutboxStore {
	return &lifecycleOutboxStore{conn: s.conn}
}
func (s *sqliteStore) WorkspaceSections() store.WorkspaceSectionStore {
	return &workspaceSectionStore{conn: s.conn}
}
func (s *sqliteStore) WorkspaceSectionItems() store.WorkspaceSectionItemStore {
	return &workspaceSectionItemStore{conn: s.conn}
}
func (s *sqliteStore) OAuthProviders() store.OAuthProviderStore {
	return &oauthProviderStore{conn: s.conn}
}
func (s *sqliteStore) OAuthStates() store.OAuthStateStore { return &oauthStateStore{conn: s.conn} }
func (s *sqliteStore) OAuthTokens() store.OAuthTokenStore { return &oauthTokenStore{conn: s.conn} }
func (s *sqliteStore) OAuthUserLinks() store.OAuthUserLinkStore {
	return &oauthUserLinkStore{conn: s.conn}
}
func (s *sqliteStore) PendingOAuthSignups() store.PendingOAuthSignupStore {
	return &pendingOAuthSignupStore{conn: s.conn}
}
func (s *sqliteStore) APITokens() store.APITokenStore { return &apiTokenStore{conn: s.conn} }
func (s *sqliteStore) DelegationTokens() store.DelegationTokenStore {
	return &delegationTokenStore{conn: s.conn}
}
func (s *sqliteStore) RevocationEvents() store.RevocationEventStore {
	return newRevocationEventStore(s.conn)
}
func (s *sqliteStore) DeviceAuthorizations() store.DeviceAuthorizationStore {
	return &deviceAuthorizationStore{conn: s.conn}
}
func (s *sqliteStore) CLIAuthorizationCodes() store.CLIAuthorizationCodeStore {
	return &cliAuthorizationCodeStore{conn: s.conn}
}
func (s *sqliteStore) Cleanup() store.CleanupStore { return &cleanupStore{conn: s.conn} }
func (s *sqliteStore) Migrator() store.Migrator    { return s.conn.shared.migrator }

func (s *sqliteStore) RunInTransaction(ctx context.Context, fn func(tx store.Store) error) error {
	if s.conn.inTx() {
		return fn(s)
	}
	return s.conn.withTransaction(ctx, func(conn *sqliteConn) error {
		return fn(&sqliteStore{conn: conn})
	})
}

func (s *sqliteStore) RunInUserAuthTransaction(ctx context.Context, userID string, fn func(tx store.Store) error) error {
	return s.conn.withTransaction(ctx, func(conn *sqliteConn) error {
		if _, err := conn.q.LockUserAuthState(ctx, userID); err != nil {
			return mapErr(err)
		}
		return fn(&sqliteStore{conn: conn})
	})
}

func (c *sqliteConn) inTx() bool {
	_, ok := c.exec.(*sql.Tx)
	return ok
}

func (c *sqliteConn) withTransaction(ctx context.Context, fn func(tx *sqliteConn) error) error {
	if c.inTx() {
		return fn(c)
	}
	tx, err := c.shared.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	txConn := &sqliteConn{
		shared: c.shared,
		exec:   tx,
		q:      c.q.WithTx(tx),
	}
	if err := fn(txConn); err != nil {
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
