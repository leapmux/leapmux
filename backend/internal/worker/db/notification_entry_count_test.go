package db_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/worker/agent"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	gendb "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// journalSupplement builds one stored supplement holding n journal entries.
func journalSupplement(t *testing.T, n int) []byte {
	t.Helper()
	var supplement []byte
	for index := range n {
		entry, err := agent.NewNotificationEntry(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
			leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
			agent.MessageContent{IdempotencyKey: "native-observation-" + string(rune('a'+index)), Original: []byte(`{}`)})
		require.NoError(t, err)
		supplement, err = agent.AppendNotificationJournal(supplement, agent.MessageContent{}, entry)
		require.NoError(t, err)
	}
	return supplement
}

// insertCountedMessage stores one message through the production INSERT.
func insertCountedMessage(t *testing.T, queries *gendb.Queries, id string, supplemental []byte) {
	t.Helper()
	compressed, compression := msgcodec.Compress(supplemental)
	_, err := queries.CreateMessage(t.Context(), gendb.CreateMessageParams{
		ID: id, AgentID: "root", AgentSessionID: "counted", IdempotencyKey: "",
		Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Content: []byte(`{}`), ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		SupplementalContent: compressed, SupplementalContentCompression: compression,
		SpanLines: "[]",
	})
	require.NoError(t, err)
}

// The generated count states, for every row, whether its stored supplement
// holds a journal, how long that journal is, or that its private storage is
// corrupt. Ordinary messages count 0 whatever their supplement carries, so the
// duplicate lookup never reads them through idx_messages_notification_entries.
func TestNotificationEntryCountTracksDirectWrites(t *testing.T) {
	t.Parallel()
	store := newNotificationEntryStore(t)

	// A plain provider supplement without a journal counts 0, compressed or not.
	providerOnly, providerCompression := msgcodec.Compress([]byte(`{"provider":{"rows":["a long provider supplement to compress"]}}`))
	_, err := store.ExecContext(t.Context(), `INSERT INTO messages
		(id, agent_id, seq, agent_session_id, source, content, content_compression,
		 supplemental_content, supplemental_content_compression, agent_provider)
		VALUES ('provider-only', 'root', 10, 'direct', 2, X'7B7D', 1, ?, ?, 1)`,
		providerOnly, providerCompression)
	require.NoError(t, err)

	// Direct SQL with an uncompressed journal supplement counts its entries.
	twoEntries := journalSupplement(t, 2)
	_, err = store.ExecContext(t.Context(), `INSERT INTO messages
		(id, agent_id, seq, agent_session_id, source, content, content_compression,
		 supplemental_content, supplemental_content_compression, agent_provider)
		VALUES ('direct-journal', 'root', 11, 'direct', 2, X'7B7D', 1, ?, 1, 1)`, twoEntries)
	require.NoError(t, err)

	for _, row := range []struct {
		id   string
		want int64
	}{
		{"provider-only", 0},
		{"direct-journal", 2},
		{"root-a", 0}, // the fixture's plain rows
	} {
		var count int64
		require.NoError(t, store.QueryRowContext(t.Context(),
			"SELECT notification_entry_count FROM messages WHERE id = ?", row.id).Scan(&count))
		assert.Equalf(t, row.want, count, "%s", row.id)
	}

	// A direct UPDATE recomputes the count from the new bytes.
	_, err = store.ExecContext(t.Context(),
		"UPDATE messages SET supplemental_content = ?, supplemental_content_compression = 1 WHERE id = 'provider-only'",
		journalSupplement(t, 1))
	require.NoError(t, err)
	var count int64
	require.NoError(t, store.QueryRowContext(t.Context(),
		"SELECT notification_entry_count FROM messages WHERE id = 'provider-only'").Scan(&count))
	assert.Equal(t, int64(1), count)
	_, err = store.ExecContext(t.Context(),
		"UPDATE messages SET supplemental_content = X'' WHERE id = 'provider-only'")
	require.NoError(t, err)
	require.NoError(t, store.QueryRowContext(t.Context(),
		"SELECT notification_entry_count FROM messages WHERE id = 'provider-only'").Scan(&count))
	assert.Zero(t, count, "an emptied supplement counts no journal")
}

// The production writers all land rows the generated count describes: the
// INSERT, the journal-only update, and the reseq that moves a thread.
func TestNotificationEntryCountTracksProductionWriters(t *testing.T) {
	t.Parallel()
	store := newNotificationEntryStore(t)
	queries := gendb.New(store)

	insertCountedMessage(t, queries, "fresh-journal", journalSupplement(t, 3))
	var count int64
	require.NoError(t, store.QueryRowContext(t.Context(),
		"SELECT notification_entry_count FROM messages WHERE id = 'fresh-journal'").Scan(&count))
	assert.Equal(t, int64(3), count)

	compressed, compression := msgcodec.Compress(journalSupplement(t, 2))
	rows, err := queries.UpdateNotificationJournal(t.Context(), gendb.UpdateNotificationJournalParams{
		ID: "root-a", AgentID: "root", SupplementalContent: compressed, SupplementalContentCompression: compression,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), rows)
	require.NoError(t, store.QueryRowContext(t.Context(),
		"SELECT notification_entry_count FROM messages WHERE id = 'root-a'").Scan(&count))
	assert.Equal(t, int64(2), count)

	seq, err := queries.UpdateNotificationThread(t.Context(), gendb.UpdateNotificationThreadParams{
		ID: "root-a", AgentID: "root", Content: []byte(`{}`),
		ContentCompression:  leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		SupplementalContent: compressed, SupplementalContentCompression: compression,
		SpanLines: "[]",
	})
	require.NoError(t, err)
	assert.Positive(t, seq)
	require.NoError(t, store.QueryRowContext(t.Context(),
		"SELECT notification_entry_count FROM messages WHERE id = 'root-a'").Scan(&count))
	assert.Equal(t, int64(2), count, "a reseq keeps the count with its row")
}

// A clone recomputes the count on the target rows from the same bytes.
func TestNotificationEntryCountSurvivesResumeCloning(t *testing.T) {
	t.Parallel()
	store := newNotificationEntryStore(t)
	queries := gendb.New(store)
	insertCountedMessage(t, queries, "clone-source", journalSupplement(t, 2))

	err := queries.CreateAgent(t.Context(), gendb.CreateAgentParams{
		ID: "resume-target", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	})
	require.NoError(t, err)
	cloned, err := queries.CloneAgentMessagesForResume(t.Context(), gendb.CloneAgentMessagesForResumeParams{
		TargetAgentID: "resume-target", SourceAgentID: "root",
	})
	require.NoError(t, err)
	assert.Positive(t, cloned)
	// The clone composes its ids as target:seq, so find the copied row by its
	// agent and its recomputed count rather than by id shape.
	var count int64
	require.NoError(t, store.QueryRowContext(t.Context(),
		"SELECT notification_entry_count FROM messages WHERE agent_id = 'resume-target' AND notification_entry_count = 2").Scan(&count))
	assert.Equal(t, int64(2), count)
}

// The generated column is not writable: no INSERT or UPDATE may spell it.
func TestNotificationEntryCountRefusesDirectOverrides(t *testing.T) {
	t.Parallel()
	store := newNotificationEntryStore(t)
	_, err := store.ExecContext(t.Context(), `INSERT INTO messages
		(id, agent_id, seq, source, content, content_compression, notification_entry_count)
		VALUES ('override', 'root', 20, 2, X'7B7D', 1, 5)`)
	assert.ErrorContains(t, err, "cannot INSERT into generated column", "an INSERT cannot spell the count")
	_, err = store.ExecContext(t.Context(),
		"UPDATE messages SET notification_entry_count = 5 WHERE id = 'root-a'")
	assert.ErrorContains(t, err, "cannot UPDATE generated column", "an UPDATE cannot spell the count")
}

// Corrupt private storage answers -1, stays selected by the count <> 0 lookup,
// and surfaces its corruption when the supplement is decoded for real: the
// lookup must never treat -1 as an empty journal.
func TestNotificationEntryCountMarksCorruptStorageForTheLookup(t *testing.T) {
	t.Parallel()
	store := newNotificationEntryStore(t)

	// Invalid private storage: the journal field is not an array.
	_, err := store.ExecContext(t.Context(), `INSERT INTO messages
		(id, agent_id, seq, agent_session_id, source, content, content_compression,
		 supplemental_content, supplemental_content_compression, agent_provider)
		VALUES ('invalid-journal', 'root', 21, 'corrupt', 2, X'7B7D', 1, ?, 1, 1)`,
		[]byte(`{"metadata":{"notification_entries":null}}`))
	require.NoError(t, err)

	// Decompression failure: bytes that are not the zstd they claim.
	_, err = store.ExecContext(t.Context(), `INSERT INTO messages
		(id, agent_id, seq, agent_session_id, source, content, content_compression,
		 supplemental_content, supplemental_content_compression, agent_provider)
		VALUES ('bad-zstd', 'root', 22, 'corrupt', 2, X'7B7D', 1, ?, 2, 1)`,
		[]byte("definitely not a zstd frame"))
	require.NoError(t, err)

	for _, id := range []string{"invalid-journal", "bad-zstd"} {
		var count int64
		require.NoError(t, store.QueryRowContext(t.Context(),
			"SELECT notification_entry_count FROM messages WHERE id = ?", id).Scan(&count))
		assert.Equalf(t, int64(-1), count, "%s must record its corruption as -1", id)
	}

	// The lookup still selects both corrupt rows (count <> 0 includes -1) and
	// the ordinary session's rows stay out.
	queries := gendb.New(store)
	rows, err := queries.ListMessageSupplementsByAgentAndSession(t.Context(), gendb.ListMessageSupplementsByAgentAndSessionParams{
		AgentID: "root", AgentSessionID: "corrupt",
	})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	for _, row := range rows {
		_, err := agent.DecodeNotificationJournal(row.SupplementalContent)
		assert.Error(t, err, "decoding a -1 row must surface its corruption, not an empty journal")
	}
}

// A threaded aggregate row can hold an empty row-level idempotency key with a
// journal, so idx_messages_idempotency_key cannot find it; the count-based
// lookup must, because the count is what identifies the row.
func TestNotificationEntryLookupFindsThreadedAggregates(t *testing.T) {
	t.Parallel()
	store := newNotificationEntryStore(t)
	queries := gendb.New(store)
	insertCountedMessage(t, queries, "threaded-aggregate", journalSupplement(t, 1))
	_, err := queries.UpdateNotificationJournal(t.Context(), gendb.UpdateNotificationJournalParams{
		ID: "root-a", AgentID: "root", SupplementalContent: journalSupplement(t, 1),
		SupplementalContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
	})
	require.NoError(t, err)

	rows, err := queries.ListMessageSupplementsByAgentAndSession(t.Context(), gendb.ListMessageSupplementsByAgentAndSessionParams{
		AgentID: "root", AgentSessionID: "counted",
	})
	require.NoError(t, err)
	require.Len(t, rows, 1, "the aggregate with an empty row-level key is found through its count")

	// root-b holds the fixture's journal-free ordinary rows; this test gave
	// root-a (session a) a journal, so 'a' is no longer the ordinary control.
	ordinary, err := queries.ListMessageSupplementsByAgentAndSession(t.Context(), gendb.ListMessageSupplementsByAgentAndSessionParams{
		AgentID: "root", AgentSessionID: "b",
	})
	require.NoError(t, err)
	assert.Empty(t, ordinary, "journal-free ordinary rows are never read")
}

// External SQLite operations on the stored database: integrity holds, a fresh
// connection reads the stored values, and the stored values survive without
// re-running the function. External WRITERS must register the same
// deterministic function, as the schema comment on the column records.
func TestNotificationEntryCountSupportsExternalSQLiteOperations(t *testing.T) {
	t.Parallel()
	store := newNotificationEntryStore(t)
	queries := gendb.New(store)
	insertCountedMessage(t, queries, "external-read", journalSupplement(t, 2))

	var integrity string
	require.NoError(t, store.QueryRowContext(t.Context(), "PRAGMA integrity_check").Scan(&integrity))
	assert.Equal(t, "ok", integrity)

	// A second connection to the same database reads the stored count; the
	// STORED column needs no function evaluation, and the integrity walk
	// never re-runs it.
	path := t.TempDir() + "/external.sqlite"
	file, err := workerdb.Open(path, sqlitedb.Config{})
	require.NoError(t, err)
	defer func() { _ = file.Close() }()
	require.NoError(t, workerdb.Migrate(t.Context(), file))
	fileQueries := gendb.New(file)
	require.NoError(t, fileQueries.CreateAgent(t.Context(), gendb.CreateAgentParams{
		ID: "root", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	insertCountedMessage(t, fileQueries, "file-journal", journalSupplement(t, 1))
	var count int64
	require.NoError(t, file.QueryRowContext(t.Context(),
		"SELECT notification_entry_count FROM messages WHERE id = 'file-journal'").Scan(&count))
	assert.Equal(t, int64(1), count)
	require.NoError(t, file.QueryRowContext(t.Context(), "PRAGMA integrity_check").Scan(&integrity))
	assert.Equal(t, "ok", integrity)
}
