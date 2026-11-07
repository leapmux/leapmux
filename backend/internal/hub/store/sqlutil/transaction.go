package sqlutil

import (
	"context"
	"database/sql"
	"errors"
)

// WithTransactionCancellation retains the BEGIN context's cancellation when
// database/sql reports automatic rollback as ErrTxDone. Callers supply only
// concrete query or Commit results from that transaction. Arbitrary callback
// errors do not establish this provenance. The BEGIN context must be non-nil.
func WithTransactionCancellation(beginContext context.Context, err error) error {
	if err != sql.ErrTxDone || beginContext.Err() == nil {
		return err
	}
	return errors.Join(err, beginContext.Err())
}
