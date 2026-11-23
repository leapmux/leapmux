package sqlutil

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/leapmux/leapmux/internal/hub/keystore"
)

func openTestSQLiteDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	raw, err := os.ReadFile(filepath.Join("..", "sqlite", "db", "migrations", "00001_initial.sql"))
	require.NoError(t, err)
	upSQL, _, _ := strings.Cut(string(raw), "-- +goose Down")
	_, err = db.ExecContext(context.Background(), upSQL)
	require.NoError(t, err)
	return db
}

func sqliteTxRunner(db *sql.DB) TxRunner {
	return func(ctx context.Context, fn func(*sql.Tx) error) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if err := fn(tx); err != nil {
			_ = tx.Rollback()
			return err
		}
		return tx.Commit()
	}
}

func makeKeyRing(t *testing.T) (*keystore.Keystore, *keystore.Keystore, *keystore.Keystore) {
	t.Helper()
	k1, err := keystore.GenerateKey()
	require.NoError(t, err)
	k2, err := keystore.GenerateKey()
	require.NoError(t, err)
	ksV1, err := keystore.New(map[uint32][32]byte{1: k1})
	require.NoError(t, err)
	ksV12, err := keystore.New(map[uint32][32]byte{1: k1, 2: k2})
	require.NoError(t, err)
	ksV2, err := keystore.New(map[uint32][32]byte{2: k2})
	require.NoError(t, err)
	return ksV1, ksV12, ksV2
}

func TestReencryptAllConcurrentUpdateAndSiblingColumn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestSQLiteDB(t)
	ksV1, ksV12, ksV2 := makeKeyRing(t)

	// Seed two users under v1:
	// - u1: will be fully updated to v2 before its per-row lock runs.
	// - u2: will have its sibling column (prefs) updated to v2 before lock while password_hash stays v1.
	seedUser := func(id, username, pw, prefs string) {
		pwCt, err := ksV1.Encrypt([]byte(pw), keystore.UserPasswordHashAAD(id))
		require.NoError(t, err)
		prefsCt, err := ksV1.Encrypt([]byte(prefs), keystore.UserPrefsAAD(id))
		require.NoError(t, err)
		_, err = db.ExecContext(ctx,
			`INSERT INTO users (id, username, password_hash, prefs) VALUES (?, ?, ?, ?)`,
			id, username, pwCt, prefsCt)
		require.NoError(t, err)
	}
	seedUser("u1", "alice", "hash-alice-v1", `{"a":1}`)
	seedUser("u2", "bob", "hash-bob-v1", `{"b":1}`)

	// Intercept the first two transactions (for u1 and u2) to simulate concurrent
	// writes landing between page scan and row lock.
	callIdx := 0
	var interceptedRunner TxRunner = func(ctx context.Context, fn func(*sql.Tx) error) error {
		callIdx++
		switch callIdx {
		case 1:
			// Simulate concurrent writer updating BOTH columns of u1 to v2 before u1 is locked.
			pwV2, err := ksV12.Encrypt([]byte("hash-alice-v2-concurrent"), keystore.UserPasswordHashAAD("u1"))
			require.NoError(t, err)
			prefsV2, err := ksV12.Encrypt([]byte(`{"a":2}`), keystore.UserPrefsAAD("u1"))
			require.NoError(t, err)
			_, err = db.ExecContext(ctx, `UPDATE users SET password_hash = ?, prefs = ? WHERE id = 'u1'`, pwV2, prefsV2)
			require.NoError(t, err)
		case 2:
			// Simulate concurrent writer updating ONLY prefs of u2 to v2 before u2 is locked.
			prefsV2, err := ksV12.Encrypt([]byte(`{"b":2}`), keystore.UserPrefsAAD("u2"))
			require.NoError(t, err)
			_, err = db.ExecContext(ctx, `UPDATE users SET prefs = ? WHERE id = 'u2'`, prefsV2)
			require.NoError(t, err)
		}
		return sqliteTxRunner(db)(ctx, fn)
	}

	res, err := ReencryptAll(ctx, db, interceptedRunner, DialectSQLite, ksV12)
	require.NoError(t, err)
	assert.Equal(t, uint32(2), res.ActiveVersion)
	// u1 was already migrated by the concurrent writer, so only u2 counts as updated.
	assert.Equal(t, int64(1), res.RowsUpdated)

	// Verify zero v1 refs remain.
	refs, err := CountKeyVersionRefs(ctx, db, DialectSQLite, 1)
	require.NoError(t, err)
	assert.Empty(t, refs)

	// Verify u1 keeps its newer concurrent values and decrypts under v2-only.
	var pw1, prefs1 []byte
	require.NoError(t, db.QueryRowContext(ctx, `SELECT password_hash, prefs FROM users WHERE id = 'u1'`).Scan(&pw1, &prefs1))
	gotPW1, err := ksV2.Decrypt(pw1, keystore.UserPasswordHashAAD("u1"))
	require.NoError(t, err)
	assert.Equal(t, "hash-alice-v2-concurrent", string(gotPW1))
	gotPrefs1, err := ksV2.Decrypt(prefs1, keystore.UserPrefsAAD("u1"))
	require.NoError(t, err)
	assert.Equal(t, `{"a":2}`, string(gotPrefs1))

	// Verify u2 preserved its v2 sibling prefs AND migrated password_hash to v2.
	var pw2, prefs2 []byte
	require.NoError(t, db.QueryRowContext(ctx, `SELECT password_hash, prefs FROM users WHERE id = 'u2'`).Scan(&pw2, &prefs2))
	gotPW2, err := ksV2.Decrypt(pw2, keystore.UserPasswordHashAAD("u2"))
	require.NoError(t, err)
	assert.Equal(t, "hash-bob-v1", string(gotPW2))
	gotPrefs2, err := ksV2.Decrypt(prefs2, keystore.UserPrefsAAD("u2"))
	require.NoError(t, err)
	assert.Equal(t, `{"b":2}`, string(gotPrefs2))
}

func TestReencryptAllMultiPageKeysetPagination(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestSQLiteDB(t)
	ksV1, ksV12, ksV2 := makeKeyRing(t)

	// Seed one user under v2 (so only the 250-row tables below have v1 rows).
	pwV2, err := ksV12.Encrypt([]byte("pw"), keystore.UserPasswordHashAAD("owner-1"))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO users (id, username, password_hash) VALUES ('owner-1', 'owner', ?)`, pwV2)
	require.NoError(t, err)

	const rowCount = 250 // > reencryptPageSize (200) to exercise multi-page keyset pagination.

	// 1) Single string PK: workspaces
	for i := range rowCount {
		wsID := fmt.Sprintf("ws-%04d", i)
		titleCt, err := ksV1.Encrypt([]byte(fmt.Sprintf("title-%04d", i)), keystore.WorkspaceTitleAAD(wsID))
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `INSERT INTO workspaces (id, owner_user_id, title) VALUES (?, 'owner-1', ?)`, wsID, titleCt)
		require.NoError(t, err)
	}

	// 2) Single int64 PK with separate AAD columns: lifecycle_outbox
	for i := range rowCount {
		opType := int32((i % 3) + 1)
		payloadCt, err := ksV1.Encrypt([]byte(fmt.Sprintf("outbox-%04d", i)), keystore.LifecycleOutboxPayloadAAD("owner-1", opType))
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `INSERT INTO lifecycle_outbox (user_id, op_type, payload) VALUES ('owner-1', ?, ?)`, opType, payloadCt)
		require.NoError(t, err)
	}

	// 3) Composite PK (string, int64, int64): user_op_batches
	for i := range rowCount {
		phys := int64(1000 + (i / 5))
		logical := int64(i % 5)
		batchCt, err := ksV1.Encrypt([]byte(fmt.Sprintf("batch-%04d", i)), keystore.UserOpBatchPayloadAAD("owner-1", phys, logical))
		require.NoError(t, err)
		transCt, err := ksV1.Encrypt([]byte(fmt.Sprintf("trans-%04d", i)), keystore.UserOpBatchTransitionsAAD("owner-1", phys, logical))
		require.NoError(t, err)
		_, err = db.ExecContext(ctx,
			`INSERT INTO user_op_batches (user_id, physical_ms, logical, batch_id, batch_payload, transitions_payload, op_count) VALUES ('owner-1', ?, ?, ?, ?, ?, 1)`,
			phys, logical, fmt.Sprintf("b-%04d", i), batchCt, transCt)
		require.NoError(t, err)
	}

	refs, err := CountKeyVersionRefs(ctx, db, DialectSQLite, 1)
	require.NoError(t, err)
	assert.Len(t, refs, 3)

	res, err := ReencryptAll(ctx, db, sqliteTxRunner(db), DialectSQLite, ksV12)
	require.NoError(t, err)
	assert.Equal(t, int64(3*rowCount), res.RowsUpdated)

	refsAfter, err := CountKeyVersionRefs(ctx, db, DialectSQLite, 1)
	require.NoError(t, err)
	assert.Empty(t, refsAfter)

	// Spot-check first, page-boundary, and last rows decrypt cleanly under v2-only.
	for _, idx := range []int{0, 199, 200, 249} {
		wsID := fmt.Sprintf("ws-%04d", idx)
		var ct []byte
		require.NoError(t, db.QueryRowContext(ctx, `SELECT title FROM workspaces WHERE id = ?`, wsID).Scan(&ct))
		plain, err := ksV2.Decrypt(ct, keystore.WorkspaceTitleAAD(wsID))
		require.NoError(t, err)
		assert.Equal(t, fmt.Sprintf("title-%04d", idx), string(plain))

		phys := int64(1000 + (idx / 5))
		logical := int64(idx % 5)
		var bCt, tCt []byte
		require.NoError(t, db.QueryRowContext(ctx,
			`SELECT batch_payload, transitions_payload FROM user_op_batches WHERE user_id = 'owner-1' AND physical_ms = ? AND logical = ?`,
			phys, logical).Scan(&bCt, &tCt))
		bPlain, err := ksV2.Decrypt(bCt, keystore.UserOpBatchPayloadAAD("owner-1", phys, logical))
		require.NoError(t, err)
		assert.Equal(t, fmt.Sprintf("batch-%04d", idx), string(bPlain))
		tPlain, err := ksV2.Decrypt(tCt, keystore.UserOpBatchTransitionsAAD("owner-1", phys, logical))
		require.NoError(t, err)
		assert.Equal(t, fmt.Sprintf("trans-%04d", idx), string(tPlain))
	}

	var outboxCount int
	rows, err := db.QueryContext(ctx, `SELECT user_id, op_type, payload FROM lifecycle_outbox`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var uid string
		var opType int32
		var ct []byte
		require.NoError(t, rows.Scan(&uid, &opType, &ct))
		_, err := ksV2.Decrypt(ct, keystore.LifecycleOutboxPayloadAAD(uid, opType))
		require.NoError(t, err)
		outboxCount++
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, rowCount, outboxCount)
}

func TestReencryptAllReturnsErrorOnUndecryptableRow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestSQLiteDB(t)
	_, _, ksV2 := makeKeyRing(t)

	// Seed a user encrypted under unknown key version 99.
	k99, err := keystore.GenerateKey()
	require.NoError(t, err)
	ksV99, err := keystore.New(map[uint32][32]byte{99: k99})
	require.NoError(t, err)

	pwCt, err := ksV99.Encrypt([]byte("pw"), keystore.UserPasswordHashAAD("u-bad"))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO users (id, username, password_hash) VALUES ('u-bad', 'bad', ?)`, pwCt)
	require.NoError(t, err)

	_, err = ReencryptAll(ctx, db, sqliteTxRunner(db), DialectSQLite, ksV2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown key version 99")
}
