package service

import (
	"fmt"
	"strings"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/worker/agent"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/require"
)

// Measure the actual SQLite lookup with ordinary messages before the keyed notification.
func BenchmarkNotificationDuplicateLookup(b *testing.B) {
	for _, count := range []int{0, 100, 1_000, 10_000} {
		for _, providerSupplement := range []bool{false, true} {
			for _, exists := range []bool{false, true} {
				b.Run(fmt.Sprintf("rows=%d/provider=%t/duplicate=%t", count, providerSupplement, exists), func(b *testing.B) {
					queries, entry := notificationLookupFixture(b, count, providerSupplement)
					if !exists {
						entry.IdempotencyKey = "absent-key"
					}
					b.ReportAllocs()
					for b.Loop() {
						found, err := findNotificationDuplicate(queries, "lookup-agent", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
							leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, "lookup-session", entry, notificationThreaded)
						if err != nil || found != exists {
							b.Fatalf("The duplicate lookup returns %t with error %v. Expected duplicate: %t.", found, err, exists)
						}
					}
				})
			}
		}
	}
}

func notificationLookupFixture(b *testing.B, count int, providerSupplement bool) (*db.Queries, agent.NotificationEntry) {
	b.Helper()
	ctx := b.Context()
	database, err := workerdb.Open(":memory:", sqlitedb.Config{})
	require.NoError(b, err)
	b.Cleanup(func() { _ = database.Close() })
	require.NoError(b, workerdb.Migrate(ctx, database))
	queries := db.New(database)
	require.NoError(b, queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "lookup-agent", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	transaction, err := database.BeginTx(ctx, nil)
	require.NoError(b, err)
	b.Cleanup(func() { _ = transaction.Rollback() })
	writer := db.New(transaction)
	var supplement []byte
	supplementCompression := leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE
	if providerSupplement {
		supplement, supplementCompression = msgcodec.Compress([]byte(`{"provider":{"payload":"` + strings.Repeat("x", 1_024) + `"}}`))
	}
	write := func(id string, supplemental []byte, compression leapmuxv1.ContentCompression) {
		_, err := createMessageRow(ctx, writer, db.CreateMessageParams{
			ID: id, AgentID: "lookup-agent", AgentSessionID: "lookup-session",
			Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
			Content: []byte(`{"content":"ordinary message"}`), ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
			SupplementalContent: supplemental, SupplementalContentCompression: compression,
			SpanLines: "[]",
			CreatedAt: sqltime.NewSQLiteTime(time.Date(2026, time.October, 9, 0, 0, 0, 0, time.UTC)),
		})
		require.NoError(b, err)
	}
	for index := range count {
		write(fmt.Sprintf("ordinary-%d", index), supplement, supplementCompression)
	}
	content := agent.MessageContent{Original: []byte(`{"type":"system","text":"the known notification"}`), IdempotencyKey: "known-key"}
	entry, err := agent.NewNotificationEntry(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content)
	require.NoError(b, err)
	stored, err := agent.AppendNotificationJournal(nil, content, entry)
	require.NoError(b, err)
	stored, storedCompression := msgcodec.Compress(stored)
	write("known-notification", stored, storedCompression)
	require.NoError(b, transaction.Commit())
	return queries, entry
}
