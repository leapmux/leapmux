package store

import "errors"

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
