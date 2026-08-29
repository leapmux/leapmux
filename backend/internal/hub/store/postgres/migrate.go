package postgres

import (
	"bytes"
	"database/sql"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
	"github.com/pressly/goose/v3"
)

//go:embed db/migrations/*.sql
var migrations embed.FS

func newMigrator(db *sql.DB) (store.Migrator, error) {
	sub, _ := fs.Sub(migrations, "db/migrations")
	flavor, err := detectFlavor(db)
	if err != nil {
		return nil, fmt.Errorf("detect backend flavor: %w", err)
	}
	if flavor == flavorYugabyte {
		// YugabyteDB does not run DDL inside a transaction: each statement
		// commits as it executes, whatever the surrounding BEGIN says. So
		// goose's wrapping transaction protects nothing there, and it does
		// active harm -- the schema lands, then the version INSERT hits the
		// transaction that the DDL invalidated, and the migrator reports a
		// failure over a database that is fully migrated and recorded as
		// version 0. A retry then fails on tables that already exist.
		//
		// Running without the transaction makes the outcome match what the
		// database actually did. It is not a weaker guarantee, because the
		// guarantee was never there.
		sub = transformFS{inner: sub, transform: addNoTransaction}
	}
	if flavor == flavorCockroach {
		// CockroachDB parses collation names as BCP-47 language tags and
		// rejects the C locale outright ("invalid locale C"). Stripping the
		// clause is semantics-preserving there: CRDB compares STRING values
		// byte-wise by default, which is exactly the ordering COLLATE "C"
		// pins on PostgreSQL for the keyset cursor id tiebreaks and FK joins.
		// YugabyteDB accepts COLLATE "C" natively and takes the plain path.
		sub = transformFS{inner: sub, transform: stripCollateC}
	}
	return sqlutil.NewGooseMigrator(goose.DialectPostgres, db, sub)
}

// sqlFlavor is which PostgreSQL-compatible backend answered.
//
// Three of them speak the same wire protocol and differ in what the SQL means,
// so the migrator asks ONCE and both dialect decisions read the answer. Asking
// twice was the shape that let a second flavor be added without the first
// question noticing.
type sqlFlavor int

const (
	flavorPostgres sqlFlavor = iota
	flavorCockroach
	flavorYugabyte
)

// detectFlavor reports which backend is behind the PostgreSQL wire protocol.
//
// version() is the one probe all three answer. CockroachDB and YugabyteDB each
// name themselves in it; a plain PostgreSQL names neither.
func detectFlavor(db *sql.DB) (sqlFlavor, error) {
	var version string
	if err := db.QueryRow(`SELECT version()`).Scan(&version); err != nil {
		return flavorPostgres, err
	}
	return flavorOf(version), nil
}

// flavorOf classifies a version() string. It is separate from the query so a
// test can pin the classification without a container for each backend.
//
// An unrecognised string is flavorPostgres, which is the STRICT path: it keeps
// the wrapping transaction and the COLLATE "C" clause. A backend nobody
// classified therefore gets the standard behaviour and fails loudly if it
// cannot, rather than silently losing a guarantee.
func flavorOf(version string) sqlFlavor {
	switch {
	case strings.Contains(version, "CockroachDB"):
		return flavorCockroach
	case strings.Contains(version, "YB-"):
		return flavorYugabyte
	}
	return flavorPostgres
}

// gooseNoTransaction is the annotation that tells goose to run a migration's
// statements outside a transaction. It must precede the Up marker.
const gooseNoTransaction = "-- +goose NO TRANSACTION\n"

// addNoTransaction prefixes a migration with the no-transaction annotation.
//
// It is idempotent: a file that already carries the annotation is returned
// unchanged, so this stays correct if a migration is written with it.
func addNoTransaction(data []byte) []byte {
	if bytes.Contains(data, []byte(gooseNoTransaction)) {
		return data
	}
	return append([]byte(gooseNoTransaction), data...)
}

func stripCollateC(data []byte) []byte {
	return bytes.ReplaceAll(data, []byte(` COLLATE "C"`), nil)
}

// transformFS serves the inner FS with transform applied to every regular
// file's contents. Directories pass through untouched (goose only lists
// them). Read-only; sufficient for handing goose a rewritten copy of the
// embedded migrations.
type transformFS struct {
	inner     fs.FS
	transform func([]byte) []byte
}

func (t transformFS) Open(name string) (fs.File, error) {
	f, err := t.inner.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if info.IsDir() {
		return f, nil
	}
	data, err := io.ReadAll(f)
	closeErr := f.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	content := t.transform(data)
	return &memFile{
		info: memFileInfo{FileInfo: info, size: int64(len(content))},
		r:    bytes.NewReader(content),
	}, nil
}

// ReadDir delegates listing to the inner FS so goose's migration discovery
// sees the same file set.
func (t transformFS) ReadDir(name string) ([]fs.DirEntry, error) {
	return fs.ReadDir(t.inner, name)
}

// ReadFile implements fs.ReadFileFS through the transform, so a consumer that
// type-asserts the fs.ReadFile fast path (rather than falling back to Open)
// can never observe the untransformed bytes.
func (t transformFS) ReadFile(name string) ([]byte, error) {
	data, err := fs.ReadFile(t.inner, name)
	if err != nil {
		return nil, err
	}
	return t.transform(data), nil
}

// memFile is a read-only fs.File over transformed in-memory contents.
type memFile struct {
	info fs.FileInfo
	r    *bytes.Reader
}

func (f *memFile) Stat() (fs.FileInfo, error) { return f.info, nil }
func (f *memFile) Read(p []byte) (int, error) { return f.r.Read(p) }
func (f *memFile) Close() error               { return nil }

// memFileInfo wraps the inner file's metadata, overriding Size to the
// TRANSFORMED length: Stat must never report a size different from what Read
// serves, or a consumer sizing a read from Stat (io.CopyN, buffer prealloc)
// gets a truncated or over-long view of the migration.
type memFileInfo struct {
	fs.FileInfo
	size int64
}

func (i memFileInfo) Size() int64 { return i.size }
