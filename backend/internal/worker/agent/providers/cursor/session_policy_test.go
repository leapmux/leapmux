package cursor

import (
	"context"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadCursorBypassPolicy(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, metadata string
		want, invalid  bool
	}{
		{"on", `{"agentId":"native","isRunEverything":true,"blobEncryptionKey":"private"}`, true, false},
		{"off", `{"agentId":"native","isRunEverything":false}`, false, false},
		{"absent", `{"agentId":"native"}`, false, false},
		{"null flag", `{"agentId":"native","isRunEverything":null}`, false, true},
		{"numeric zero", `{"agentId":"native","isRunEverything":0}`, false, true},
		{"negative", `{"agentId":"native","isRunEverything":-1}`, false, true},
		{"large", `{"agentId":"native","isRunEverything":1e100}`, false, true},
		{"string", `{"agentId":"native","isRunEverything":"true"}`, false, true},
		{"empty string", `{"agentId":"native","isRunEverything":""}`, false, true},
		{"null metadata", `null`, false, true},
		{"array metadata", `[]`, false, true},
		{"missing identity", `{"isRunEverything":true}`, false, true},
		{"other identity", `{"agentId":"other","isRunEverything":true}`, false, true},
		{"invalid json", `{"blobEncryptionKey":"private"`, false, true},
	} {
		for _, encoded := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: " json", true: " hex"}[encoded], func(t *testing.T) {
				t.Parallel()
				query := agent.StoredSessionQuery{HomeDir: t.TempDir()}
				path := cursorACPStorePath(query, "native")
				db := agenttest.NewFixtureDB(t, path, cursorStoreDDL)
				_, err := db.Exec(`PRAGMA journal_mode=WAL`)
				require.NoError(t, err)
				raw := tc.metadata
				if encoded {
					raw = hex.EncodeToString([]byte(raw))
				}
				_, err = db.Exec(`INSERT INTO meta(key,value) VALUES(?,?)`, cursorMetaKey, raw)
				require.NoError(t, err)
				got, err := readCursorBypassPolicy(t.Context(), query, "native")
				if tc.invalid {
					require.Error(t, err)
					assert.NotContains(t, err.Error(), "private")
				} else {
					require.NoError(t, err)
					assert.Equal(t, tc.want, got)
				}
				var after string
				require.NoError(t, db.QueryRow(`SELECT value FROM meta WHERE key=?`, cursorMetaKey).Scan(&after))
				assert.Equal(t, raw, after, "reading permissions must preserve every native field")
			})
		}
	}
}

func TestReadCursorBypassPolicyUnavailable(t *testing.T) {
	t.Parallel()
	query := agent.StoredSessionQuery{HomeDir: t.TempDir()}
	for _, id := range []string{"", ".", "..", filepath.Join("..", "outside"), "missing"} {
		_, err := readCursorBypassPolicy(t.Context(), query, id)
		require.Error(t, err)
	}
	path := cursorACPStorePath(query, "native")
	db := agenttest.NewFixtureDB(t, path, cursorStoreDDL)
	_, err := readCursorBypassPolicy(t.Context(), query, "native")
	require.Error(t, err, "missing metadata is unknown, not Off")
	_, err = db.Exec(`INSERT INTO meta(key,value) VALUES(?,?)`, cursorMetaKey, `{"agentId":"native","isRunEverything":true}`)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = readCursorBypassPolicy(ctx, query, "native")
	require.ErrorIs(t, err, context.Canceled)
	_, err = db.Exec(`UPDATE meta SET value=? WHERE key=?`, strings.Repeat("x", 1024), cursorMetaKey)
	require.NoError(t, err)
	_, err = readCursorBypassPolicy(t.Context(), query, "native")
	require.Error(t, err)
}
