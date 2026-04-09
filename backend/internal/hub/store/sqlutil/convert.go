package sqlutil

import "database/sql"

// RowsAffected extracts the number of affected rows from a sql.Result,
// mapping the error through the provided mapErrFn first.
func RowsAffected(result sql.Result, err error, mapErrFn func(error) error) (int64, error) {
	if err != nil {
		return 0, mapErrFn(err)
	}
	n, _ := result.RowsAffected()
	return n, nil
}
