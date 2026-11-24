package sqlutil

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// Dialect identifies the SQL syntax used by KeyMaintenance and test helpers.
type Dialect uint8

const (
	DialectSQLite Dialect = iota + 1
	DialectPostgres
	DialectMySQL
)

// QueryRows abstracts *sql.Rows and pgx.Rows for dialect-neutral SQL reads.
type QueryRows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close() error
}

// QueryExec abstracts *sql.DB, *sql.Tx, and pgx pool/tx handles so
// KeyMaintenance runs both outside and inside caller transactions across all
// three dialects. ScanRow must return sql.ErrNoRows when no row matches.
type QueryExec interface {
	QueryRows(ctx context.Context, query string, args ...any) (QueryRows, error)
	ScanRow(ctx context.Context, query string, args []any, dest ...any) error
	Exec(ctx context.Context, query string, args ...any) error
}

// TxRunner executes fn inside a transaction (or directly against the active
// transaction when the caller is already inside one).
type TxRunner func(ctx context.Context, fn func(QueryExec) error) error

type sqlDBTX interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type sqlQueryExec struct {
	db sqlDBTX
}

// NewSQLQueryExec adapts *sql.DB or *sql.Tx to QueryExec.
func NewSQLQueryExec(db sqlDBTX) QueryExec {
	return sqlQueryExec{db: db}
}

func (e sqlQueryExec) QueryRows(ctx context.Context, query string, args ...any) (QueryRows, error) {
	return e.db.QueryContext(ctx, query, args...)
}

func (e sqlQueryExec) ScanRow(ctx context.Context, query string, args []any, dest ...any) error {
	return e.db.QueryRowContext(ctx, query, args...).Scan(dest...)
}

func (e sqlQueryExec) Exec(ctx context.Context, query string, args ...any) error {
	_, err := e.db.ExecContext(ctx, query, args...)
	return err
}

// NewSQLTxRunner returns a TxRunner backed by db.BeginTx. When isRetryable is
// non-nil, each transaction attempt is wrapped in store.RetryOnConflict.
func NewSQLTxRunner(db *sql.DB, isRetryable func(error) bool) TxRunner {
	return func(ctx context.Context, fn func(QueryExec) error) error {
		runOnce := func() error {
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				return fmt.Errorf("begin reencrypt transaction: %w", err)
			}
			defer func() { _ = tx.Rollback() }()
			if err := fn(NewSQLQueryExec(tx)); err != nil {
				return err
			}
			return tx.Commit()
		}
		if isRetryable != nil {
			return store.RetryOnConflict(ctx, isRetryable, runOnce)
		}
		return runOnce()
	}
}

// InTxRunner returns a TxRunner that runs fn directly on an already-open
// transaction without beginning or committing a nested one.
func InTxRunner(tx QueryExec) TxRunner {
	return func(_ context.Context, fn func(QueryExec) error) error {
		return fn(tx)
	}
}

const reencryptPageSize = 200

// CountKeyVersionRefs counts rows in each table of PersistentEncryptedTables
// that contain at least one non-NULL encrypted column prefixed with version.
// All 15 tables are queried in a single UNION ALL round trip against the
// 4-byte version header so blob payloads are never transferred to Go.
// When isRetryable is non-nil (non-transactional Postgres/MySQL), transient
// serialization conflicts retry the query automatically.
func CountKeyVersionRefs(
	ctx context.Context,
	qe QueryExec,
	dialect Dialect,
	version uint32,
	isRetryable func(error) bool,
) ([]store.KeyVersionRefCount, error) {
	if err := validateDialect(dialect); err != nil {
		return nil, err
	}
	versionBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(versionBytes, version)

	query, args := buildCountUnionQuery(dialect, versionBytes)
	var out []store.KeyVersionRefCount
	runOnce := func() error {
		rows, err := qe.QueryRows(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("count key version %d refs: %w", version, err)
		}
		counts := make([]int64, len(PersistentEncryptedTables))
		for rows.Next() {
			var idx int
			var count int64
			if scanErr := rows.Scan(&idx, &count); scanErr != nil {
				_ = rows.Close()
				return fmt.Errorf("scan key version %d refs: %w", version, scanErr)
			}
			if idx < 0 || idx >= len(counts) {
				_ = rows.Close()
				return fmt.Errorf("count key version %d refs: invalid table index %d", version, idx)
			}
			counts[idx] = count
		}
		if err := closeRows(rows); err != nil {
			return fmt.Errorf("count key version %d refs: %w", version, err)
		}
		out = nil
		for i, spec := range PersistentEncryptedTables {
			if counts[i] > 0 {
				out = append(out, store.KeyVersionRefCount{
					Table: spec.Table,
					Label: spec.Label,
					Count: counts[i],
				})
			}
		}
		return nil
	}
	if isRetryable != nil {
		if err := store.RetryOnConflict(ctx, isRetryable, runOnce); err != nil {
			return nil, err
		}
		return out, nil
	}
	if err := runOnce(); err != nil {
		return nil, err
	}
	return out, nil
}

// VerifyKeyring verifies across all PersistentEncryptedTables that:
//  1. Every non-NULL encrypted column carries a valid ciphertext header and
//     meets the minimum envelope length (keystore.CiphertextOverhead).
//  2. Every key version present in the database is retained in ks.
//  3. At least one sample row for each distinct key version present decrypts
//     cleanly with its real AAD under ks (catching wrong key material even when
//     version numbers match).
//
// Returns nil when the database holds no persistent encrypted data yet.
func VerifyKeyring(
	ctx context.Context,
	qe QueryExec,
	dialect Dialect,
	ks *keystore.Keystore,
	isRetryable func(error) bool,
) error {
	if err := validateDialect(dialect); err != nil {
		return err
	}
	if ks == nil {
		return errors.New("verify keyring: nil keystore")
	}

	retained := make(map[uint32]struct{}, len(ks.Versions()))
	for _, v := range ks.Versions() {
		retained[v] = struct{}{}
	}

	type colRef struct {
		tableIdx int
		colIdx   int
	}
	var samples map[uint32]colRef
	unionQuery := buildDistinctVersionsUnionQuery(dialect)
	scanVersions := func() error {
		rows, err := qe.QueryRows(ctx, unionQuery)
		if err != nil {
			return fmt.Errorf("verify keyring: scan key versions: %w", err)
		}
		samples = make(map[uint32]colRef)
		for rows.Next() {
			var tableIdx, colIdx int
			var prefix []byte
			var minLen int64
			if scanErr := rows.Scan(&tableIdx, &colIdx, &prefix, &minLen); scanErr != nil {
				_ = rows.Close()
				return fmt.Errorf("verify keyring: scan version row: %w", scanErr)
			}
			if tableIdx < 0 || tableIdx >= len(PersistentEncryptedTables) {
				_ = rows.Close()
				return fmt.Errorf("verify keyring: invalid table index %d", tableIdx)
			}
			spec := PersistentEncryptedTables[tableIdx]
			if colIdx < 0 || colIdx >= len(spec.Columns) {
				_ = rows.Close()
				return fmt.Errorf("verify keyring: invalid column index %d for %s", colIdx, spec.Table)
			}
			col := spec.Columns[colIdx]
			if len(prefix) != 4 || minLen < keystore.CiphertextOverhead {
				_ = rows.Close()
				return fmt.Errorf("verify keyring: %s.%s contains malformed ciphertext (prefix_len=%d, min_len=%d)", spec.Table, col.Name, len(prefix), minLen)
			}
			ver := binary.BigEndian.Uint32(prefix)
			if _, ok := retained[ver]; !ok {
				_ = rows.Close()
				return fmt.Errorf("verify keyring: %s.%s contains data encrypted with key version %d, which is not in the loaded key ring %v", spec.Table, col.Name, ver, ks.Versions())
			}
			if _, seen := samples[ver]; !seen {
				samples[ver] = colRef{tableIdx: tableIdx, colIdx: colIdx}
			}
		}
		if err := closeRows(rows); err != nil {
			return fmt.Errorf("verify keyring: read key versions: %w", err)
		}
		return nil
	}
	if isRetryable != nil {
		if err := store.RetryOnConflict(ctx, isRetryable, scanVersions); err != nil {
			return err
		}
	} else if err := scanVersions(); err != nil {
		return err
	}

	if len(samples) == 0 {
		return nil
	}

	versions := make([]uint32, 0, len(samples))
	for v := range samples {
		versions = append(versions, v)
	}
	slices.Sort(versions)

	for _, ver := range versions {
		ref := samples[ver]
		spec := PersistentEncryptedTables[ref.tableIdx]
		col := spec.Columns[ref.colIdx]
		verBytes := make([]byte, 4)
		binary.BigEndian.PutUint32(verBytes, ver)
		sampleSQL := buildSampleRowQuery(dialect, spec, col)

		var raw []byte
		var aadVals []any
		var found bool
		readSample := func() error {
			raw = nil
			aadVals = nil
			found = false
			aadDests, extractAAD := newKeyScanners(spec.AADColumns)
			scanDests := append(aadDests, &raw)
			err := qe.ScanRow(ctx, sampleSQL, []any{verBytes}, scanDests...)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("verify keyring: read sample from %s.%s (key version %d): %w", spec.Table, col.Name, ver, err)
			}
			aadVals = extractAAD()
			found = true
			return nil
		}
		if isRetryable != nil {
			if err := store.RetryOnConflict(ctx, isRetryable, readSample); err != nil {
				return err
			}
		} else if err := readSample(); err != nil {
			return err
		}
		if !found {
			continue
		}
		if _, decErr := ks.Decrypt(raw, col.AADFunc(aadVals)); decErr != nil {
			return fmt.Errorf("verify keyring: cannot decrypt %s.%s (key version %d): %w", spec.Table, col.Name, ver, decErr)
		}
	}
	return nil
}

// ReencryptAll re-encrypts every outdated non-NULL encrypted column across all
// tables in PersistentEncryptedTables to ks.ActiveVersion().
//
// Each table is scanned in keyset-ordered pages selecting only primary keys,
// closing rows before opening any write transaction (preventing SQLite
// SetMaxOpenConns(1) deadlocks). Each row is then locked and re-read inside
// runTx, re-encrypting only columns whose 4-byte version prefix still differs
// from ks.ActiveVersion(). When isRetryable is non-nil, page-scan reads retry
// transient serialization conflicts automatically.
func ReencryptAll(
	ctx context.Context,
	qe QueryExec,
	runTx TxRunner,
	dialect Dialect,
	ks *keystore.Keystore,
	isRetryable func(error) bool,
) (store.ReencryptResult, error) {
	if err := validateDialect(dialect); err != nil {
		return store.ReencryptResult{}, err
	}
	if ks == nil {
		return store.ReencryptResult{}, errors.New("reencrypt: nil keystore")
	}

	activeVersion := ks.ActiveVersion()
	activeBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(activeBytes, activeVersion)

	res := store.ReencryptResult{ActiveVersion: activeVersion}
	for _, spec := range PersistentEncryptedTables {
		n, err := reencryptTable(ctx, qe, runTx, dialect, ks, spec, activeBytes, isRetryable)
		if err != nil {
			return res, err
		}
		res.RowsUpdated += n
	}
	return res, nil
}

func reencryptTable(
	ctx context.Context,
	qe QueryExec,
	runTx TxRunner,
	dialect Dialect,
	ks *keystore.Keystore,
	spec EncryptedTableSpec,
	activeBytes []byte,
	isRetryable func(error) bool,
) (int64, error) {
	selectSQL := buildLockSelectRowQuery(dialect, spec)
	var rowsUpdated int64
	var lastPK []any

	for {
		pagePKs, err := fetchOutdatedPKPage(ctx, qe, dialect, spec, activeBytes, lastPK, isRetryable)
		if err != nil {
			return rowsUpdated, err
		}
		if len(pagePKs) == 0 {
			break
		}
		lastPK = pagePKs[len(pagePKs)-1]

		for _, pk := range pagePKs {
			var updated bool
			err := runTx(ctx, func(tx QueryExec) error {
				updated = false
				didUpdate, txErr := reencryptRowTx(ctx, tx, dialect, ks, spec, selectSQL, pk, activeBytes)
				if txErr != nil {
					return txErr
				}
				updated = didUpdate
				return nil
			})
			if err != nil {
				return rowsUpdated, err
			}
			if updated {
				rowsUpdated++
			}
		}

		if len(pagePKs) < reencryptPageSize {
			break
		}
	}

	return rowsUpdated, nil
}

func fetchOutdatedPKPage(
	ctx context.Context,
	qe QueryExec,
	dialect Dialect,
	spec EncryptedTableSpec,
	activeBytes []byte,
	lastPK []any,
	isRetryable func(error) bool,
) ([][]any, error) {
	query, args := buildOutdatedPKPageQuery(dialect, spec, activeBytes, lastPK)
	var pks [][]any
	runOnce := func() error {
		rows, err := qe.QueryRows(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("reencrypt: scan outdated keys in %s: %w", spec.Table, err)
		}
		pks = nil
		for rows.Next() {
			dests, extract := newKeyScanners(spec.PrimaryKey)
			if scanErr := rows.Scan(dests...); scanErr != nil {
				_ = rows.Close()
				return fmt.Errorf("reencrypt: scan key in %s: %w", spec.Table, scanErr)
			}
			pks = append(pks, extract())
		}
		if err := closeRows(rows); err != nil {
			return fmt.Errorf("reencrypt: read key page in %s: %w", spec.Table, err)
		}
		return nil
	}
	if isRetryable != nil {
		if err := store.RetryOnConflict(ctx, isRetryable, runOnce); err != nil {
			return nil, err
		}
		return pks, nil
	}
	if err := runOnce(); err != nil {
		return nil, err
	}
	return pks, nil
}

func reencryptRowTx(
	ctx context.Context,
	tx QueryExec,
	dialect Dialect,
	ks *keystore.Keystore,
	spec EncryptedTableSpec,
	selectSQL string,
	pk []any,
	activeBytes []byte,
) (bool, error) {
	aadDests, extractAAD := newKeyScanners(spec.AADColumns)
	rawCols := make([][]byte, len(spec.Columns))
	// Track SQL NULL-ness separately from the scanned []byte slice: modernc.org/sqlite
	// delivers driver.Value(nil) for a non-NULL zero-length BLOB (x''), so neither
	// *[]byte nor sql.Scanner can tell x'' from SQL NULL without an explicit SQL check.
	nonNullCols := make([]int64, len(spec.Columns))
	scanDests := make([]any, 0, len(aadDests)+2*len(spec.Columns))
	scanDests = append(scanDests, aadDests...)
	for i := range spec.Columns {
		scanDests = append(scanDests, &rawCols[i], &nonNullCols[i])
	}

	err := tx.ScanRow(ctx, selectSQL, pk, scanDests...)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reencrypt: lock row in %s: %w", spec.Table, err)
	}

	aadVals := extractAAD()
	var updateCols []string
	var updateArgs []any

	for i, col := range spec.Columns {
		if nonNullCols[i] == 0 {
			if col.Nullable {
				continue
			}
			return false, fmt.Errorf("reencrypt: unexpected NULL ciphertext in %s.%s", spec.Table, col.Name)
		}
		raw := rawCols[i]
		if len(raw) == 0 {
			return false, fmt.Errorf("reencrypt: empty ciphertext in %s.%s", spec.Table, col.Name)
		}
		if len(raw) >= 4 && bytes.Equal(raw[:4], activeBytes) {
			continue
		}
		aad := col.AADFunc(aadVals)
		plain, decErr := ks.Decrypt(raw, aad)
		if decErr != nil {
			return false, fmt.Errorf("reencrypt: decrypt %s.%s: %w", spec.Table, col.Name, decErr)
		}
		sealed, encErr := ks.Encrypt(plain, aad)
		if encErr != nil {
			return false, fmt.Errorf("reencrypt: encrypt %s.%s: %w", spec.Table, col.Name, encErr)
		}
		updateCols = append(updateCols, col.Name)
		updateArgs = append(updateArgs, sealed)
	}

	if len(updateCols) == 0 {
		return false, nil
	}

	updateSQL := buildUpdateRowQuery(dialect, spec, updateCols)
	allArgs := append(updateArgs, pk...)
	if err := tx.Exec(ctx, updateSQL, allArgs...); err != nil {
		return false, fmt.Errorf("reencrypt: update %s: %w", spec.Table, err)
	}
	return true, nil
}

func closeRows(rows QueryRows) error {
	rowsErr := rows.Err()
	if closeErr := rows.Close(); closeErr != nil && rowsErr == nil {
		rowsErr = closeErr
	}
	return rowsErr
}

func validateDialect(dialect Dialect) error {
	switch dialect {
	case DialectSQLite, DialectPostgres, DialectMySQL:
		return nil
	default:
		return fmt.Errorf("unknown sqlutil dialect %d", dialect)
	}
}

func quoteIdent(dialect Dialect, name string) string {
	if dialect == DialectMySQL {
		return "`" + name + "`"
	}
	return name
}

func prefixExpr(dialect Dialect, col string) string {
	q := quoteIdent(dialect, col)
	switch dialect {
	case DialectSQLite:
		return fmt.Sprintf("SUBSTR(%s, 1, 4)", q)
	case DialectPostgres:
		return fmt.Sprintf("substring(%s from 1 for 4)", q)
	case DialectMySQL:
		return fmt.Sprintf("SUBSTRING(%s, 1, 4)", q)
	default:
		panic(fmt.Sprintf("unknown dialect %d", dialect))
	}
}

func paramPlaceholder(dialect Dialect, idx int) string {
	if dialect == DialectPostgres {
		return fmt.Sprintf("$%d", idx)
	}
	return "?"
}

func pkWhereClause(dialect Dialect, pk []KeyColumnSpec, startIdx int) string {
	whereParts := make([]string, len(pk))
	for i, col := range pk {
		whereParts[i] = fmt.Sprintf("%s = %s", quoteIdent(dialect, col.Name), paramPlaceholder(dialect, startIdx+i))
	}
	return strings.Join(whereParts, " AND ")
}

func byteLenExpr(dialect Dialect, col string) string {
	q := quoteIdent(dialect, col)
	switch dialect {
	case DialectSQLite:
		return fmt.Sprintf("LENGTH(%s)", q)
	case DialectPostgres:
		return fmt.Sprintf("octet_length(%s)", q)
	case DialectMySQL:
		return fmt.Sprintf("OCTET_LENGTH(%s)", q)
	default:
		panic(fmt.Sprintf("unknown dialect %d", dialect))
	}
}

func buildDistinctVersionsUnionQuery(dialect Dialect) string {
	var branches []string
	for tIdx, spec := range PersistentEncryptedTables {
		qTable := quoteIdent(dialect, spec.Table)
		for cIdx, col := range spec.Columns {
			qCol := quoteIdent(dialect, col.Name)
			pfx := prefixExpr(dialect, col.Name)
			lenExpr := byteLenExpr(dialect, col.Name)
			branches = append(branches, fmt.Sprintf(
				"SELECT %d, %d, %s, MIN(%s) FROM %s WHERE %s IS NOT NULL GROUP BY %s",
				tIdx, cIdx, pfx, lenExpr, qTable, qCol, pfx,
			))
		}
	}
	return strings.Join(branches, " UNION ALL ")
}

func buildSampleRowQuery(dialect Dialect, spec EncryptedTableSpec, col EncryptedColumnSpec) string {
	selectCols := make([]string, 0, len(spec.AADColumns)+1)
	for _, c := range spec.AADColumns {
		selectCols = append(selectCols, quoteIdent(dialect, c.Name))
	}
	qCol := quoteIdent(dialect, col.Name)
	selectCols = append(selectCols, qCol)
	return fmt.Sprintf(
		"SELECT %s FROM %s WHERE %s IS NOT NULL AND %s = %s LIMIT 1",
		strings.Join(selectCols, ", "),
		quoteIdent(dialect, spec.Table),
		qCol,
		prefixExpr(dialect, col.Name),
		paramPlaceholder(dialect, 1),
	)
}

func buildCountUnionQuery(dialect Dialect, versionBytes []byte) (string, []any) {
	branches := make([]string, len(PersistentEncryptedTables))
	var args []any
	if dialect == DialectPostgres {
		args = []any{versionBytes}
	}
	for i, spec := range PersistentEncryptedTables {
		preds := make([]string, len(spec.Columns))
		for j, col := range spec.Columns {
			qCol := quoteIdent(dialect, col.Name)
			ph := "$1"
			if dialect != DialectPostgres {
				ph = "?"
				args = append(args, versionBytes)
			}
			preds[j] = fmt.Sprintf("(%s IS NOT NULL AND %s = %s)", qCol, prefixExpr(dialect, col.Name), ph)
		}
		branches[i] = fmt.Sprintf("SELECT %d, COUNT(*) FROM %s WHERE %s", i, quoteIdent(dialect, spec.Table), strings.Join(preds, " OR "))
	}
	return strings.Join(branches, " UNION ALL "), args
}

func buildOutdatedPKPageQuery(dialect Dialect, spec EncryptedTableSpec, activeBytes []byte, lastPK []any) (string, []any) {
	pkCols := make([]string, len(spec.PrimaryKey))
	for i, pk := range spec.PrimaryKey {
		pkCols[i] = quoteIdent(dialect, pk.Name)
	}

	var args []any
	nextParam := func(val any) string {
		args = append(args, val)
		return paramPlaceholder(dialect, len(args))
	}

	outdatedPreds := make([]string, len(spec.Columns))
	for i, col := range spec.Columns {
		qCol := quoteIdent(dialect, col.Name)
		pfx := prefixExpr(dialect, col.Name)
		// Include `pfx IS NULL` because SQLite returns NULL for SUBSTR(x'', 1, 4)
		// on a non-NULL zero-length BLOB; without it, corrupt empty BLOBs would
		// silently escape ReencryptAll.
		outdatedPreds[i] = fmt.Sprintf("(%s IS NOT NULL AND (%s IS NULL OR %s <> %s))", qCol, pfx, pfx, nextParam(activeBytes))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "SELECT %s FROM %s WHERE (%s)", strings.Join(pkCols, ", "), quoteIdent(dialect, spec.Table), strings.Join(outdatedPreds, " OR "))

	if len(lastPK) > 0 {
		if len(spec.PrimaryKey) == 1 {
			fmt.Fprintf(&b, " AND %s > %s", pkCols[0], nextParam(lastPK[0]))
		} else {
			// Standard SQL row-value comparison (supported by SQLite 3.15+,
			// Postgres, CockroachDB, YugabyteDB, MySQL 8, and TiDB) lets the
			// planner perform an index range seek instead of an O(N^2) OR scan.
			placeholders := make([]string, len(spec.PrimaryKey))
			for j := range spec.PrimaryKey {
				placeholders[j] = nextParam(lastPK[j])
			}
			fmt.Fprintf(&b, " AND (%s) > (%s)", strings.Join(pkCols, ", "), strings.Join(placeholders, ", "))
		}
	}

	fmt.Fprintf(&b, " ORDER BY %s LIMIT %d", strings.Join(pkCols, " ASC, ")+" ASC", reencryptPageSize)
	return b.String(), args
}

func buildLockSelectRowQuery(dialect Dialect, spec EncryptedTableSpec) string {
	selectCols := make([]string, 0, len(spec.AADColumns)+2*len(spec.Columns))
	for _, c := range spec.AADColumns {
		selectCols = append(selectCols, quoteIdent(dialect, c.Name))
	}
	for _, c := range spec.Columns {
		q := quoteIdent(dialect, c.Name)
		selectCols = append(selectCols, q, fmt.Sprintf("CASE WHEN %s IS NULL THEN 0 ELSE 1 END", q))
	}

	query := fmt.Sprintf("SELECT %s FROM %s WHERE %s", strings.Join(selectCols, ", "), quoteIdent(dialect, spec.Table), pkWhereClause(dialect, spec.PrimaryKey, 1))
	if dialect == DialectPostgres || dialect == DialectMySQL {
		query += " FOR UPDATE"
	}
	return query
}

func buildUpdateRowQuery(dialect Dialect, spec EncryptedTableSpec, updateCols []string) string {
	setParts := make([]string, len(updateCols))
	for i, col := range updateCols {
		setParts[i] = fmt.Sprintf("%s = %s", quoteIdent(dialect, col), paramPlaceholder(dialect, i+1))
	}
	return fmt.Sprintf("UPDATE %s SET %s WHERE %s", quoteIdent(dialect, spec.Table), strings.Join(setParts, ", "), pkWhereClause(dialect, spec.PrimaryKey, len(updateCols)+1))
}

func newKeyScanners(specs []KeyColumnSpec) ([]any, func() []any) {
	dests := make([]any, len(specs))
	for i, s := range specs {
		switch s.Kind {
		case KeyKindString:
			var v string
			dests[i] = &v
		case KeyKindInt64:
			var v int64
			dests[i] = &v
		case KeyKindInt32:
			var v int32
			dests[i] = &v
		default:
			panic(fmt.Sprintf("unknown key column kind %d", s.Kind))
		}
	}
	return dests, func() []any {
		vals := make([]any, len(specs))
		for i, s := range specs {
			switch s.Kind {
			case KeyKindString:
				vals[i] = *(dests[i].(*string))
			case KeyKindInt64:
				vals[i] = *(dests[i].(*int64))
			case KeyKindInt32:
				vals[i] = *(dests[i].(*int32))
			}
		}
		return vals
	}
}
