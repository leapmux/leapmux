package store

import "errors"

// Sentinel errors returned by Store implementations.
var (
	// ErrNotFound is returned when a requested record does not exist.
	ErrNotFound = errors.New("not found")

	// ErrConflict is returned when an operation violates a uniqueness
	// constraint (e.g. duplicate ID, username, or email).
	ErrConflict = errors.New("conflict")

	// ErrRollbackNotSupported is returned by NoSQL Migrators when a
	// downgrade migration is not possible.
	ErrRollbackNotSupported = errors.New("rollback not supported")

	// ErrSectionNotEmpty is returned when attempting to delete a
	// workspace section that still contains items.
	ErrSectionNotEmpty = errors.New("section not empty")
)

// ConflictEntity identifies the entity type that caused a uniqueness violation.
type ConflictEntity string

const (
	ConflictEntityOrg  ConflictEntity = "org"
	ConflictEntityUser ConflictEntity = "user"
)

// ConflictError is a structured conflict error that carries the entity
// type (e.g. "org", "user") that caused the uniqueness violation.
// It matches ErrConflict via errors.Is.
type ConflictError struct {
	Entity ConflictEntity
}

func (e *ConflictError) Error() string {
	return "conflict: " + string(e.Entity)
}

func (e *ConflictError) Is(target error) bool {
	return target == ErrConflict
}

// NewConflictError wraps an ErrConflict-chain error with entity
// information. If err is not an ErrConflict, it is returned as-is.
func NewConflictError(err error, entity ConflictEntity) error {
	if errors.Is(err, ErrConflict) {
		return &ConflictError{Entity: entity}
	}
	return err
}
