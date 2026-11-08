package sqlitedb

import "modernc.org/sqlite"

// IsInterruption identifies a native SQLite interruption leaf. Callers must
// inspect every cause and require a canceled context before suppressing it.
func IsInterruption(err error) bool {
	native, ok := err.(*sqlite.Error)
	const sqliteInterrupt = 9
	return ok && native != nil && native.Code() == sqliteInterrupt
}
