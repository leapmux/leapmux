package store

import "errors"

type transactionCommitError struct {
	cause error
}

func (e *transactionCommitError) Error() string            { return "commit transaction: " + e.cause.Error() }
func (e *transactionCommitError) Unwrap() error            { return e.cause }
func (*transactionCommitError) CommitOutcomeUnknown() bool { return true }

// TransactionCommitResult marks a commit failure whose outcome the caller
// cannot prove. Adapters return known rollback failures without this marker.
// The original cause remains available to driver-specific error classification.
func TransactionCommitResult(err error) error {
	if err == nil {
		return nil
	}
	return &transactionCommitError{cause: err}
}

// CommitOutcomeUnknown reports uncertainty through wrappers and store adapters.
func CommitOutcomeUnknown(err error) bool {
	var uncertain interface{ CommitOutcomeUnknown() bool }
	return errors.As(err, &uncertain) && uncertain.CommitOutcomeUnknown()
}
