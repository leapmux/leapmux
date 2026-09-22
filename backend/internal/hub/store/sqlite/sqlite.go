// Package sqlite implements the Hub store backed by SQLite.
// It wraps the sqlc-generated Queries, converting between
// backend-agnostic store types and sqlc-generated types.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/userid"
	sqlite3 "modernc.org/sqlite"
	sqlitelib "modernc.org/sqlite/lib"
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
	// One call is the whole boot: migrate, then seed and reconcile the
	// built-in registrations. Migrator() wraps the raw migrator so every
	// caller that migrates this store later completes the same sequence.
	if err := st.Migrator().Migrate(context.Background()); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("migrate sqlite: %w", err)
	}
	return st, nil
}

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
func (s *sqliteStore) UserOpBatches() store.UserOpBatchesStore {
	return &userOpBatchesStore{conn: s.conn}
}
func (s *sqliteStore) UserState() store.UserStateStore { return &userStateStore{conn: s.conn} }
func (s *sqliteStore) UserRecentBatchIDs() store.UserRecentBatchIDStore {
	return &userRecentBatchIDStore{conn: s.conn}
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
func (s *sqliteStore) PasskeyCredentials() store.PasskeyCredentialStore {
	return &passkeyCredentialStore{conn: s.conn}
}
func (s *sqliteStore) WebAuthnSessions() store.WebAuthnSessionStore {
	return &webAuthnSessionStore{conn: s.conn}
}
func (s *sqliteStore) Settings() store.SettingsStore {
	return &settingsStore{conn: s.conn}
}
func (s *sqliteStore) AltchaSalts() store.AltchaSaltsStore {
	return &altchaSaltsStore{conn: s.conn}
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
func (s *sqliteStore) OAuthAuthorizationCodes() store.OAuthAuthorizationCodeStore {
	return &oauthAuthorizationCodeStore{conn: s.conn}
}

func (s *sqliteStore) OAuthClients() store.OAuthClientStore {
	return &oauthClientStore{conn: s.conn}
}
func (s *sqliteStore) Cleanup() store.CleanupStore { return &cleanupStore{conn: s.conn} }

// Migrator wraps the raw goose migrator so a completed migration also seeds
// and reconciles the built-in registrations -- the boot sequence, wherever it
// runs. See store.MigratorWithBuiltIns.
func (s *sqliteStore) Migrator() store.Migrator {
	return store.MigratorWithBuiltIns(s.conn.shared.migrator, s)
}

func (s *sqliteStore) RunInTransaction(ctx context.Context, fn func(tx store.Store) error) error {
	if s.conn.inTx() {
		return fn(s)
	}
	return s.conn.withTransaction(ctx, func(conn *sqliteConn) error {
		return fn(&sqliteStore{conn: conn})
	})
}

// A zero userID is deliberately NOT refused here. This selects the row to
// LOCK; it is not an ownership predicate, and LockUserAuthState is a `:one`
// query filtered on `deleted_at IS NULL`, so an id matching nothing already
// aborts the transaction with ErrNotFound. Refusing earlier would additionally
// make a blank-user session row unconstructible, which is the fixture the
// corrupt-data fail-close tests need in order to prove ValidateToken denies it.
func (s *sqliteStore) RunInUserAuthTransaction(ctx context.Context, userID userid.UserID, fn func(tx store.Store) error) error {
	return s.conn.withTransaction(ctx, func(conn *sqliteConn) error {
		if _, err := conn.q.LockUserAuthState(ctx, userID.String()); err != nil {
			return mapErr(err)
		}
		return fn(&sqliteStore{conn: conn})
	})
}

func (c *sqliteConn) inTx() bool {
	_, ok := c.exec.(*sql.Tx)
	return ok
}

// withTransaction runs `fn` in one transaction, starting over when SQLite
// refuses the write for contention.
//
// `fn` MUST be safe to run again. A refused transaction rolls back whole, so
// nothing it wrote survives, and the retry re-reads everything it read. A `fn`
// that accumulates into state OUTSIDE the transaction has to reset that state at
// its own start rather than append to it.
//
// The 60-second `busy_timeout` this database opens with does NOT cover these
// two. That handler waits for a lock; it is never invoked for a deferred
// transaction whose snapshot went stale under another writer (BUSY_SNAPSHOT), or
// for one that cannot upgrade a read to a write (BUSY). SQLite returns
// immediately in both cases, and only the application can start over -- which is
// why `worker/inputqueue` carries the same retry around its own accept.
//
// Without this, a workspace delete racing another writer failed the whole RPC
// with `delete workspace: database is locked`, surfaced as a 500.
func (c *sqliteConn) withTransaction(ctx context.Context, fn func(tx *sqliteConn) error) error {
	if c.inTx() {
		return fn(c)
	}
	for attempt := 0; ; attempt++ {
		err := c.runTransaction(ctx, fn)
		if err == nil || attempt == sqliteBusyRetries || !isSQLiteBusy(err) {
			return err
		}
	}
}

// sqliteBusyRetries bounds the restarts above. Contention here is one writer
// losing a race, so the next attempt almost always wins; a run of losses this
// long is a different problem and the caller has to hear about it.
const sqliteBusyRetries = 5

// isSQLiteBusy reports whether SQLite refused this transaction for contention
// rather than for anything the caller did wrong.
func isSQLiteBusy(err error) bool {
	var sqliteErr *sqlite3.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	code := sqliteErr.Code()
	return code == sqlitelib.SQLITE_BUSY || code == sqlitelib.SQLITE_BUSY_SNAPSHOT
}

func (c *sqliteConn) runTransaction(ctx context.Context, fn func(tx *sqliteConn) error) error {
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
