// Package postgres implements the Hub store backed by PostgreSQL.
// It wraps the sqlc-generated Queries, converting between
// backend-agnostic store types and sqlc-generated types.
package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/util/userid"
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
	if cfg.ConnMaxLifetime > 0 {
		poolCfg.MaxConnLifetime = cfg.ConnMaxLifetime
	}
	if cfg.MaxConnIdleTime > 0 {
		poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	}
	if cfg.HealthCheckPeriod > 0 {
		poolCfg.HealthCheckPeriod = cfg.HealthCheckPeriod
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
	st := &pgStore{conn: newPoolConn(&pgShared{
		pool:        pool,
		migrationDB: sqlDB,
		migrator:    mig,
	}, pool)}
	// One call is the whole boot: migrate, then seed and reconcile the
	// built-in registrations. Migrator() wraps the raw migrator so every
	// caller that migrates this store later completes the same sequence.
	if err := st.Migrator().Migrate(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate postgres: %w", err)
	}
	return st, nil
}

// newFromPool wraps an existing pool (already migrated) into a Store.
func newFromPool(pool *pgxpool.Pool, migrationDB *sql.DB) (*pgStore, error) {
	mig, err := newMigrator(migrationDB)
	if err != nil {
		return nil, fmt.Errorf("init postgres migrator: %w", err)
	}
	return &pgStore{conn: newPoolConn(&pgShared{
		pool:        pool,
		migrationDB: migrationDB,
		migrator:    mig,
	}, pool)}, nil
}

// newPoolConn builds the NON-transactional conn, and it is the only way to
// build one.
//
// Both entry points went through the same four-line literal, and the second
// half of that literal is a security-relevant decision rather than plumbing:
// BOTH fields must carry conflictRetryDBTX, so a statement a distributed
// backend aborts for a retryable conflict runs again instead of reaching the
// caller as a failure it did not earn. Two copies of that decision is one copy
// that can lose the wrapper with nothing to notice. See conflict_retry.go.
//
// exec is the RAW-SQL path, and it carries the wrapper for the same reason q
// does. This dialect routes its bulk tab-index writes through sqlc arrays, so
// exec reaches only the publish statement today -- the mysql twin is where a
// raw pool actually loses the retry. Both are wrapped, so the rule is one rule
// rather than a per-dialect judgement.
//
// Inside a transaction runTransaction installs the raw pgx.Tx in exec and
// rebuilds q through WithTx, so neither field retries there and nothing runs
// twice inside a dead transaction. inTx() still reads correctly, because a
// wrapped POOL is not a pgx.Tx either way.
//
// The pool arrives as an argument rather than through shared.pool so a test
// can supply a stand-in and prove the wiring, not merely the wrapper.
func newPoolConn(shared *pgShared, pool gendb.DBTX) *pgConn {
	retrying := conflictRetryDBTX{inner: pool}
	return &pgConn{
		shared: shared,
		exec:   retrying,
		q:      gendb.New(retrying),
	}
}

func (s *pgStore) Users() store.UserStore       { return &userStore{conn: s.conn} }
func (s *pgStore) Sessions() store.SessionStore { return &sessionStore{conn: s.conn} }
func (s *pgStore) Workers() store.WorkerStore   { return &workerStore{conn: s.conn} }
func (s *pgStore) WorkerNotifications() store.WorkerNotificationStore {
	return &workerNotificationStore{conn: s.conn}
}
func (s *pgStore) RegistrationKeys() store.RegistrationKeyStore {
	return &registrationKeyStore{conn: s.conn}
}
func (s *pgStore) Workspaces() store.WorkspaceStore { return &workspaceStore{conn: s.conn} }
func (s *pgStore) WorkspaceTabIndex() store.WorkspaceTabIndexStore {
	return &workspaceTabIndexStore{conn: s.conn}
}
func (s *pgStore) UserOpBatches() store.UserOpBatchesStore { return &userOpBatchesStore{conn: s.conn} }
func (s *pgStore) UserState() store.UserStateStore         { return &userStateStore{conn: s.conn} }
func (s *pgStore) UserRecentBatchIDs() store.UserRecentBatchIDStore {
	return &userRecentBatchIDStore{conn: s.conn}
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
func (s *pgStore) PasskeyCredentials() store.PasskeyCredentialStore {
	return &passkeyCredentialStore{conn: s.conn}
}
func (s *pgStore) WebAuthnSessions() store.WebAuthnSessionStore {
	return &webAuthnSessionStore{conn: s.conn}
}
func (s *pgStore) Settings() store.SettingsStore {
	return &settingsStore{conn: s.conn}
}
func (s *pgStore) AltchaSalts() store.AltchaSaltsStore {
	return &altchaSaltsStore{conn: s.conn}
}
func (s *pgStore) APITokens() store.APITokenStore { return &apiTokenStore{conn: s.conn} }
func (s *pgStore) DelegationTokens() store.DelegationTokenStore {
	return &delegationTokenStore{conn: s.conn}
}
func (s *pgStore) RevocationEvents() store.RevocationEventStore {
	return newRevocationEventStore(s.conn)
}
func (s *pgStore) DeviceAuthorizations() store.DeviceAuthorizationStore {
	return &deviceAuthorizationStore{conn: s.conn}
}
func (s *pgStore) OAuthAuthorizationCodes() store.OAuthAuthorizationCodeStore {
	return &oauthAuthorizationCodeStore{conn: s.conn}
}

func (s *pgStore) OAuthClients() store.OAuthClientStore {
	return &oauthClientStore{conn: s.conn}
}
func (s *pgStore) Cleanup() store.CleanupStore { return &cleanupStore{conn: s.conn} }

// Migrator wraps the raw goose migrator so a completed migration also seeds
// and reconciles the built-in registrations -- the boot sequence, wherever it
// runs. See store.MigratorWithBuiltIns.
func (s *pgStore) Migrator() store.Migrator {
	return store.MigratorWithBuiltIns(s.conn.shared.migrator, s)
}

// RunInTransaction runs fn in one transaction. fn may run MORE THAN ONCE when
// the backend aborts the transaction for a retryable conflict; see
// withTransaction and the contract on store.Store.
func (s *pgStore) RunInTransaction(ctx context.Context, fn func(tx store.Store) error) error {
	if s.conn.inTx() {
		return fn(s)
	}
	return s.conn.withTransaction(ctx, func(conn *pgConn) error {
		return fn(&pgStore{conn: conn})
	})
}

// A zero userID is deliberately NOT refused here. This selects the row to
// LOCK; it is not an ownership predicate, and LockUserAuthState is a `:one`
// query filtered on `deleted_at IS NULL`, so an id matching nothing already
// aborts the transaction with ErrNotFound. Refusing earlier would additionally
// make a blank-user session row unconstructible, which is the fixture the
// corrupt-data fail-close tests need in order to prove ValidateToken denies it.
func (s *pgStore) RunInUserAuthTransaction(ctx context.Context, userID userid.UserID, fn func(tx store.Store) error) error {
	return s.conn.withTransaction(ctx, func(conn *pgConn) error {
		if _, err := conn.q.LockUserAuthState(ctx, userID.String()); err != nil {
			return mapErr(err)
		}
		return fn(&pgStore{conn: conn})
	})
}

func (c *pgConn) inTx() bool {
	_, ok := c.exec.(pgx.Tx)
	return ok
}

// withTransaction runs fn in one transaction, and runs the WHOLE transaction
// again when the backend aborts it for a retryable conflict.
//
// The retry has to wrap the whole unit of work rather than the statement that
// conflicted: the abort killed the transaction, so every later statement in it
// answers "current transaction is aborted" and re-running one would rejoin a
// dead transaction. That is why the statement-level wrapper covers the pool
// alone -- see conflict_retry.go.
//
// So fn CAN RUN MORE THAN ONCE, and store.Store states that as the contract
// its callers must meet. A caller that writes a result through a captured
// variable is fine, because a re-run overwrites it. A caller that ACCUMULATES
// into captured state -- appends to a slice, adds to a counter, sends on a
// channel, calls a lifecycle effect -- is not, because the aborted attempt's
// contribution stays. Every caller in this repository was read against that
// rule before the retry was turned on; all of them assign.
//
// An abort can arrive from a statement inside fn or from Commit itself, and
// both reach store.RetryOnConflict here. A conflict that fn converts into an error
// type carrying no wrapped cause is the one shape this cannot see, and it
// simply does not retry -- the caller gets the same answer it got before.
//
// An already-open transaction returns fn(c) untouched: the OUTERMOST call owns
// the retry, and a nested one that retried would repeat part of a unit of work
// somebody else is still assembling.
func (c *pgConn) withTransaction(ctx context.Context, fn func(tx *pgConn) error) error {
	if c.inTx() {
		return fn(c)
	}
	return store.RetryOnConflict(ctx, isRetryableConflict, func() error { return c.runTransaction(ctx, fn) })
}

// runTransaction is one attempt: begin, run, commit, and roll back whatever
// the attempt left behind.
func (c *pgConn) runTransaction(ctx context.Context, fn func(tx *pgConn) error) error {
	pgxTx, err := c.shared.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = pgxTx.Rollback(ctx) }()

	txConn := &pgConn{
		shared: c.shared,
		exec:   pgxTx,
		// WithTx rebuilds Queries over the transaction, which deliberately
		// drops the statement-level retry: inside a transaction the whole
		// attempt is what repeats, and this function is what repeats it.
		q: c.q.WithTx(pgxTx),
	}
	if err := fn(txConn); err != nil {
		return err
	}
	return pgxTx.Commit(ctx)
}

func (s *pgStore) Close() error {
	s.conn.shared.pool.Close()
	return s.conn.shared.migrationDB.Close()
}
