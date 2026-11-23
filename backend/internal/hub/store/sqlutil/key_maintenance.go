package sqlutil

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// Dialect identifies the SQL syntax used by KeyMaintenance helpers.
type Dialect uint8

const (
	DialectSQLite Dialect = iota + 1
	DialectPostgres
	DialectMySQL
)

// TxRunner executes fn inside a *sql.Tx with dialect-appropriate conflict retry
// (plain BeginTx on SQLite; RetryOnConflict + BeginTx on Postgres and MySQL).
type TxRunner func(ctx context.Context, fn func(*sql.Tx) error) error

const reencryptPageSize = 200

// CountKeyVersionRefs counts rows in each table of PersistentEncryptedTables
// that contain at least one non-NULL encrypted column prefixed with version.
// The check runs in SQL against the 4-byte version header so blob payloads are
// never transferred to Go.
func CountKeyVersionRefs(ctx context.Context, db DBQuery, dialect Dialect, version uint32) ([]store.KeyVersionRefCount, error) {
	if err := validateDialect(dialect); err != nil {
		return nil, err
	}
	versionBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(versionBytes, version)

	var out []store.KeyVersionRefCount
	for _, spec := range PersistentEncryptedTables {
		query, args := buildCountQuery(dialect, spec, versionBytes)
		rows, err := db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("count key version %d refs in %s: %w", version, spec.Table, err)
		}
		var count int64
		if rows.Next() {
			if scanErr := rows.Scan(&count); scanErr != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan key version %d refs in %s: %w", version, spec.Table, scanErr)
			}
		}
		rowsErr := rows.Err()
		if closeErr := rows.Close(); closeErr != nil && rowsErr == nil {
			rowsErr = closeErr
		}
		if rowsErr != nil {
			return nil, fmt.Errorf("count key version %d refs in %s: %w", version, spec.Table, rowsErr)
		}
		if count > 0 {
			out = append(out, store.KeyVersionRefCount{
				Table: spec.Table,
				Label: spec.Label,
				Count: count,
			})
		}
	}
	return out, nil
}

// ReencryptAll re-encrypts every outdated non-NULL encrypted column across all
// tables in PersistentEncryptedTables to ks.ActiveVersion().
//
// Each table is scanned in keyset-ordered pages selecting only primary keys,
// closing *sql.Rows before opening any write transaction (preventing SQLite
// SetMaxOpenConns(1) deadlocks). Each row is then locked and re-read inside
// runTx, re-encrypting only columns whose 4-byte version prefix still differs
// from ks.ActiveVersion().
func ReencryptAll(ctx context.Context, db DBQuery, runTx TxRunner, dialect Dialect, ks *keystore.Keystore) (store.ReencryptResult, error) {
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
		n, err := reencryptTable(ctx, db, runTx, dialect, ks, spec, activeBytes)
		if err != nil {
			return res, err
		}
		res.RowsUpdated += n
	}
	return res, nil
}

func reencryptTable(
	ctx context.Context,
	db DBQuery,
	runTx TxRunner,
	dialect Dialect,
	ks *keystore.Keystore,
	spec EncryptedTableSpec,
	activeBytes []byte,
) (int64, error) {
	var rowsUpdated int64
	var lastPK []any

	for {
		pagePKs, err := fetchOutdatedPKPage(ctx, db, dialect, spec, activeBytes, lastPK)
		if err != nil {
			return rowsUpdated, err
		}
		if len(pagePKs) == 0 {
			break
		}
		lastPK = pagePKs[len(pagePKs)-1]

		for _, pk := range pagePKs {
			var updated bool
			err := runTx(ctx, func(tx *sql.Tx) error {
				updated = false
				didUpdate, txErr := reencryptRowTx(ctx, tx, dialect, ks, spec, pk, activeBytes)
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
	db DBQuery,
	dialect Dialect,
	spec EncryptedTableSpec,
	activeBytes []byte,
	lastPK []any,
) ([][]any, error) {
	query, args := buildOutdatedPKPageQuery(dialect, spec, activeBytes, lastPK)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("reencrypt: scan outdated keys in %s: %w", spec.Table, err)
	}

	var pks [][]any
	for rows.Next() {
		dests, extract := newKeyScanners(spec.PrimaryKey)
		if scanErr := rows.Scan(dests...); scanErr != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("reencrypt: scan key in %s: %w", spec.Table, scanErr)
		}
		pks = append(pks, extract())
	}
	rowsErr := rows.Err()
	if closeErr := rows.Close(); closeErr != nil && rowsErr == nil {
		rowsErr = closeErr
	}
	if rowsErr != nil {
		return nil, fmt.Errorf("reencrypt: read key page in %s: %w", spec.Table, rowsErr)
	}
	return pks, nil
}

func reencryptRowTx(
	ctx context.Context,
	tx *sql.Tx,
	dialect Dialect,
	ks *keystore.Keystore,
	spec EncryptedTableSpec,
	pk []any,
	activeBytes []byte,
) (bool, error) {
	selectSQL := buildLockSelectRowQuery(dialect, spec)
	aadDests, extractAAD := newKeyScanners(spec.AADColumns)
	rawCols := make([][]byte, len(spec.Columns))
	scanDests := make([]any, 0, len(aadDests)+len(rawCols))
	scanDests = append(scanDests, aadDests...)
	for i := range rawCols {
		scanDests = append(scanDests, &rawCols[i])
	}

	err := tx.QueryRowContext(ctx, selectSQL, pk...).Scan(scanDests...)
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
		raw := rawCols[i]
		if len(raw) == 0 {
			if col.Nullable && raw == nil {
				continue
			}
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
	if _, err := tx.ExecContext(ctx, updateSQL, allArgs...); err != nil {
		return false, fmt.Errorf("reencrypt: update %s: %w", spec.Table, err)
	}
	return true, nil
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

func buildCountQuery(dialect Dialect, spec EncryptedTableSpec, versionBytes []byte) (string, []any) {
	preds := make([]string, len(spec.Columns))
	args := make([]any, len(spec.Columns))
	for i, col := range spec.Columns {
		qCol := quoteIdent(dialect, col.Name)
		preds[i] = fmt.Sprintf("(%s IS NOT NULL AND %s = %s)", qCol, prefixExpr(dialect, col.Name), paramPlaceholder(dialect, i+1))
		args[i] = versionBytes
	}
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s", quoteIdent(dialect, spec.Table), strings.Join(preds, " OR "))
	return query, args
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
		outdatedPreds[i] = fmt.Sprintf("(%s IS NOT NULL AND %s <> %s)", qCol, prefixExpr(dialect, col.Name), nextParam(activeBytes))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "SELECT %s FROM %s WHERE (%s)", strings.Join(pkCols, ", "), quoteIdent(dialect, spec.Table), strings.Join(outdatedPreds, " OR "))

	if len(lastPK) > 0 {
		// Standard tuple comparison expanded for SQLite/Postgres/MySQL portability:
		// (k0 > v0) OR (k0 = v0 AND k1 > v1) OR ...
		var keysetBranches []string
		for j := range spec.PrimaryKey {
			var parts []string
			for k := range j {
				parts = append(parts, fmt.Sprintf("%s = %s", pkCols[k], nextParam(lastPK[k])))
			}
			parts = append(parts, fmt.Sprintf("%s > %s", pkCols[j], nextParam(lastPK[j])))
			keysetBranches = append(keysetBranches, "("+strings.Join(parts, " AND ")+")")
		}
		fmt.Fprintf(&b, " AND (%s)", strings.Join(keysetBranches, " OR "))
	}

	fmt.Fprintf(&b, " ORDER BY %s LIMIT %d", strings.Join(pkCols, " ASC, ")+" ASC", reencryptPageSize)
	return b.String(), args
}

func buildLockSelectRowQuery(dialect Dialect, spec EncryptedTableSpec) string {
	selectCols := make([]string, 0, len(spec.AADColumns)+len(spec.Columns))
	for _, c := range spec.AADColumns {
		selectCols = append(selectCols, quoteIdent(dialect, c.Name))
	}
	for _, c := range spec.Columns {
		selectCols = append(selectCols, quoteIdent(dialect, c.Name))
	}

	whereParts := make([]string, len(spec.PrimaryKey))
	for i, pk := range spec.PrimaryKey {
		whereParts[i] = fmt.Sprintf("%s = %s", quoteIdent(dialect, pk.Name), paramPlaceholder(dialect, i+1))
	}

	query := fmt.Sprintf("SELECT %s FROM %s WHERE %s", strings.Join(selectCols, ", "), quoteIdent(dialect, spec.Table), strings.Join(whereParts, " AND "))
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
	whereParts := make([]string, len(spec.PrimaryKey))
	offset := len(updateCols)
	for i, pk := range spec.PrimaryKey {
		whereParts[i] = fmt.Sprintf("%s = %s", quoteIdent(dialect, pk.Name), paramPlaceholder(dialect, offset+i+1))
	}
	return fmt.Sprintf("UPDATE %s SET %s WHERE %s", quoteIdent(dialect, spec.Table), strings.Join(setParts, ", "), strings.Join(whereParts, " AND "))
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
		case KeyKindBytes:
			var v []byte
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
			case KeyKindBytes:
				vals[i] = bytes.Clone(*(dests[i].(*[]byte)))
			}
		}
		return vals
	}
}
