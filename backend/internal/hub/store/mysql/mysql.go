// Package mysql implements the Hub store backed by MySQL.
// It wraps the sqlc-generated Queries, converting between
// backend-agnostic store types and sqlc-generated types.
package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/util/userid"
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
	if cfg.ConnMaxLifetime > 0 {
		sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	}
	if cfg.ConnMaxIdleTime > 0 {
		sqlDB.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
	}

	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping mysql: %w", err)
	}

	enforceTiDBConstraints(context.Background(), sqlDB)

	mig, err := newMigrator(sqlDB)
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("init mysql migrator: %w", err)
	}
	if err := mig.Migrate(context.Background()); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("migrate mysql: %w", err)
	}

	return &mysqlStore{conn: newPoolConn(&mysqlShared{
		db:       sqlDB,
		migrator: mig,
	}, sqlDB)}, nil
}

// newPoolConn builds the NON-transactional conn, and it is the only way to
// build one.
//
// BOTH fields carry conflictRetryDBTX, so a statement the backend aborts for a
// retryable conflict runs again instead of reaching the caller as a failure it
// did not earn. That is a decision rather than plumbing, and a second copy of
// it is one that can lose the wrapper with nothing to notice. See
// conflict_retry.go.
//
// exec is the RAW-SQL path -- the workspace tab index composes its bulk
// INSERT and DELETE statements by hand, because sqlc cannot generate a
// variable-length VALUES list. Those statements take the same row locks the
// generated ones do, so they need the same retry. Leaving exec raw made the
// coverage depend on who called: the tab index runs inside RunInTransaction
// today, which repeats the whole unit of work, but the store interface states
// no such requirement.
//
// Inside a transaction runTransaction installs the raw *sql.Tx in exec and
// rebuilds q through WithTx, so neither field retries there and nothing runs
// twice inside a dead transaction. inTx() still reads correctly, because a
// wrapped POOL is not a *sql.Tx either way.
//
// The pool arrives as an argument rather than through shared.db so a test can
// supply a stand-in and prove the wiring, not merely the wrapper.
func newPoolConn(shared *mysqlShared, db gendb.DBTX) *mysqlConn {
	retrying := conflictRetryDBTX{inner: db}
	return &mysqlConn{
		shared: shared,
		exec:   retrying,
		q:      gendb.New(retrying),
	}
}

// tidbEnforcementVariables are the TiDB system variables that decide whether
// the schema's declared constraints do anything at all. TiDB PARSES a CHECK
// and a FOREIGN KEY and then IGNORES it unless the matching variable is ON, so
// each entry states what stays unenforced while its variable is off.
var tidbEnforcementVariables = []struct{ name, unenforced string }{
	{
		name: "tidb_enable_check_constraint",
		unenforced: "every CHECK in the schema -- the elevation pair, users.id <> '', op_count > 0, " +
			"holder_id <> '', last_seq >= 0, the seq/published_at coupling, and the kind allowlists",
	},
	{
		name:       "tidb_enable_foreign_key",
		unenforced: "every FOREIGN KEY in the schema",
	},
}

// tidbServer is the narrow view of the database that the enforcement check
// needs: run one statement, and read one scalar back.
//
// Two function values rather than an interface over *sql.DB, because
// QueryRowContext returns the CONCRETE *sql.Row, whose only method reads
// unexported fields -- no stand-in can build one. That is the same obstacle
// conflict_retry.go documents on its own QueryRowContext. The seam lets a test
// exercise the policy below without a driver stand-in.
type tidbServer struct {
	exec   func(ctx context.Context, statement string) error
	scalar func(ctx context.Context, query string) (string, error)
}

// enforceTiDBConstraints turns the variables above ON and then READS EACH ONE
// BACK.
func enforceTiDBConstraints(ctx context.Context, db *sql.DB) {
	enforceTiDBConstraintsOn(ctx, slog.Default(), tidbServer{
		exec: func(ctx context.Context, statement string) error {
			_, err := db.ExecContext(ctx, statement)
			return err
		},
		scalar: func(ctx context.Context, query string) (string, error) {
			var value string
			err := db.QueryRowContext(ctx, query).Scan(&value)
			return value, err
		},
	})
}

// enforceTiDBConstraintsOn is the policy half.
//
// The read-back is the whole point. SET GLOBAL needs SYSTEM_VARIABLES_ADMIN,
// which a managed TiDB usually withholds, and the statement then fails. The
// hub discarded that error, so every CHECK in the schema became inert with no
// signal anywhere -- the shared storetest suite cannot catch a violation
// either, because the constraint never fires there.
//
// It does NOT refuse to start. An operator who cannot obtain the privilege
// still needs a hub, and only the elevation pair has a Go twin that refuses
// the bad shape on its own (auth.NewElevation). Reporting exactly what is
// unenforced is what the operator can act on.
//
// It runs only when the server reports itself as TiDB. Real MySQL has neither
// variable and answers "Unknown system variable", which is expected there and
// must stay silent.
func enforceTiDBConstraintsOn(ctx context.Context, log *slog.Logger, server tidbServer) {
	version, err := server.scalar(ctx, "SELECT VERSION()")
	if err != nil {
		log.WarnContext(ctx, "could not read the database server version, so the TiDB constraint check did not run",
			"err", err)
		return
	}
	if !strings.Contains(strings.ToLower(version), "tidb") {
		return
	}
	for _, v := range tidbEnforcementVariables {
		// The name comes from the table above, never from configuration, and
		// MySQL accepts no placeholder for a system variable name.
		if err := server.exec(ctx, "SET GLOBAL "+v.name+" = ON"); err != nil {
			log.ErrorContext(ctx, "could not turn on a TiDB constraint enforcement variable",
				"variable", v.name, "version", version, "err", err)
		}
		value, err := server.scalar(ctx, "SELECT @@global."+v.name)
		if err != nil {
			log.ErrorContext(ctx, "could not read back a TiDB constraint enforcement variable, so the schema may be unenforced",
				"variable", v.name, "unenforced", v.unenforced, "version", version, "err", err)
			continue
		}
		if !tidbVariableIsOn(value) {
			log.ErrorContext(ctx, "TiDB does not enforce part of the schema",
				"variable", v.name, "value", value, "unenforced", v.unenforced, "version", version,
				"remedy", "grant SYSTEM_VARIABLES_ADMIN to the hub database user, or set the variable on the server")
		}
	}
}

// tidbVariableIsOn reads a boolean TiDB system variable. TiDB reports one as
// "ON"/"OFF" and as "1"/"0" depending on the version, so both spellings count.
func tidbVariableIsOn(value string) bool {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "ON", "1":
		return true
	}
	return false
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
func (s *mysqlStore) UserOpBatches() store.UserOpBatchesStore {
	return &userOpBatchesStore{conn: s.conn}
}
func (s *mysqlStore) UserState() store.UserStateStore { return &userStateStore{conn: s.conn} }
func (s *mysqlStore) UserRecentBatchIDs() store.UserRecentBatchIDStore {
	return &userRecentBatchIDStore{conn: s.conn}
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
func (s *mysqlStore) PasskeyCredentials() store.PasskeyCredentialStore {
	return &passkeyCredentialStore{conn: s.conn}
}
func (s *mysqlStore) WebAuthnSessions() store.WebAuthnSessionStore {
	return &webAuthnSessionStore{conn: s.conn}
}
func (s *mysqlStore) Settings() store.SettingsStore {
	return &settingsStore{conn: s.conn}
}
func (s *mysqlStore) AltchaSalts() store.AltchaSaltsStore {
	return &altchaSaltsStore{conn: s.conn}
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
func (s *mysqlStore) OAuthAuthorizationCodes() store.OAuthAuthorizationCodeStore {
	return &oauthAuthorizationCodeStore{conn: s.conn}
}

func (s *mysqlStore) OAuthClients() store.OAuthClientStore {
	return &oauthClientStore{conn: s.conn}
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

// A zero userID is deliberately NOT refused here. This selects the row to
// LOCK; it is not an ownership predicate, and LockUserAuthState is a `:one`
// query filtered on `deleted_at IS NULL`, so an id matching nothing already
// aborts the transaction with ErrNotFound. Refusing earlier would additionally
// make a blank-user session row unconstructible, which is the fixture the
// corrupt-data fail-close tests need in order to prove ValidateToken denies it.
func (s *mysqlStore) RunInUserAuthTransaction(ctx context.Context, userID userid.UserID, fn func(tx store.Store) error) error {
	return s.conn.withTransaction(ctx, func(conn *mysqlConn) error {
		if _, err := conn.q.LockUserAuthState(ctx, userID.String()); err != nil {
			return mapErr(err)
		}
		return fn(&mysqlStore{conn: conn})
	})
}

func (c *mysqlConn) inTx() bool {
	_, ok := c.exec.(*sql.Tx)
	return ok
}

// withTransaction runs fn in one transaction, and runs the WHOLE transaction
// again when the backend aborts it for a retryable conflict.
//
// The retry wraps the unit of work rather than the statement that conflicted:
// InnoDB rolls the whole transaction back when it picks a deadlock victim, so
// re-running one statement would rejoin a transaction that no longer exists.
// This is also what covers a single-row SELECT, which conflictRetryDBTX cannot
// wrap -- see its type doc.
//
// So fn CAN RUN MORE THAN ONCE, and store.Store states that as the contract
// its callers must meet: assigning a result through a captured variable is
// fine, accumulating into one is not.
//
// An already-open transaction returns fn(c) untouched: the OUTERMOST call owns
// the retry.
func (c *mysqlConn) withTransaction(ctx context.Context, fn func(tx *mysqlConn) error) error {
	if c.inTx() {
		return fn(c)
	}
	return store.RetryOnConflict(ctx, isRetryableConflict, func() error { return c.runTransaction(ctx, fn) })
}

// runTransaction is one attempt: begin, run, commit, and roll back whatever
// the attempt left behind.
func (c *mysqlConn) runTransaction(ctx context.Context, fn func(tx *mysqlConn) error) error {
	tx, err := c.shared.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	txConn := &mysqlConn{
		shared: c.shared,
		exec:   tx,
		// WithTx rebuilds Queries over the transaction, which deliberately
		// drops the statement-level retry: inside a transaction the whole
		// attempt is what repeats, and this function is what repeats it.
		q: c.q.WithTx(tx),
	}
	if err := fn(txConn); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *mysqlStore) Close() error {
	return s.conn.shared.db.Close()
}
