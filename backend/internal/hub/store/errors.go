package store

import (
	"errors"
	"fmt"
)

// Sentinel errors returned by Store implementations.
var (
	// ErrNotFound is returned when a requested record does not exist.
	ErrNotFound = errors.New("not found")

	// ErrConflict is returned when an operation violates a uniqueness
	// constraint (e.g. duplicate ID, username, or email).
	//
	// It is a bare sentinel, with no structured shape carrying WHICH unique
	// index fired. That is deliberate, not an omission: the three dialects
	// expose three different things -- sqlite names the column in free text,
	// postgres exposes an index name structurally (pgconn.PgError.
	// ConstraintName), mysql names a different index in free text -- so a typed
	// field would be honest on one backend and guesswork on the other two.
	// Callers that need the specific field pre-check availability instead; see
	// cmd/leapmux's checkAdminUserFieldsAvailable.
	ErrConflict = errors.New("conflict")

	// ErrHubAlreadyRunning is returned when another Hub holds the singleton
	// database runtime lease.
	ErrHubAlreadyRunning = errors.New("another Hub is already running")

	// ErrSectionNotEmpty is returned when attempting to delete a
	// workspace section that still contains items.
	ErrSectionNotEmpty = errors.New("section not empty")

	// ErrInvalidArgument is returned when a store method receives input that
	// violates a store-level invariant (e.g. an empty or non-slug username). The
	// service layer validates the same constraints upstream; the store re-checks
	// so a store-level caller that bypasses the service cannot land the invariant
	// violation.
	ErrInvalidArgument = errors.New("invalid argument")
)

// ErrLockingReadOutsideTransaction refuses a locking read that no transaction
// encloses.
//
// SELECT ... FOR UPDATE takes a row lock that the enclosing transaction holds;
// with no transaction the database takes and releases the lock at once, so the
// caller reads a row it does not hold and every later write races what it just
// read. On the mysql dialect the loss is additionally SILENT: conflictRetryDBTX
// cannot wrap QueryRowContext, so a lock-wait timeout on a bare single-row
// SELECT reaches the caller unretried.
//
// It is a caller mistake rather than a query the store can answer, so every
// ...ForUpdate method on every dialect returns this before it reaches the
// database. Every caller today goes through RunInTransaction, so the guard
// refuses nothing that exists. It is here so that the next one fails loudly
// instead.
//
// ONE sentinel, in the shared package, rather than the rule restated at each
// of the six methods. It WRAPS ErrInvalidArgument, which is what the guards
// returned before it and what the dialect-independent mapping still keys on,
// so errors.Is answers for both; and a test can now pin THIS mistake rather
// than "some invalid argument".
var ErrLockingReadOutsideTransaction = fmt.Errorf(
	"locking read outside a transaction: %w", ErrInvalidArgument)
