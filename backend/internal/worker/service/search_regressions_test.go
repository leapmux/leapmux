package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func TestFileSearch_GlobPatternEdgeCases(t *testing.T) {
	t.Parallel()

	// SCAN-1: non-ASCII UTF-8 literals must not be split byte-by-byte.
	utf8Fn, err := compileGlobPattern("café*.ts")
	require.NoError(t, err)
	assert.True(t, utf8Fn("café-app.ts"))
	assert.False(t, utf8Fn("cafe-app.ts"))

	krFn, err := compileGlobPattern("한글/?.md")
	require.NoError(t, err)
	assert.True(t, krFn("한글/가.md"))
	assert.False(t, krFn("한글/가나.md"))

	// SCAN-2: negated character class beginning with literal ']'.
	negBracketFn, err := compileGlobPattern("[!]]")
	require.NoError(t, err)
	assert.True(t, negBracketFn("a"))
	assert.False(t, negBracketFn("]"))
	assert.False(t, negBracketFn("/"))
	assert.False(t, negBracketFn("a]"))

	negCaretBracketFn, err := compileGlobPattern("[^]a]")
	require.NoError(t, err)
	assert.True(t, negCaretBracketFn("b"))
	assert.False(t, negCaretBracketFn("]"))
	assert.False(t, negCaretBracketFn("a"))
	assert.False(t, negCaretBracketFn("/"))

	// SCAN-3: backslashes inside [...] are literal, never RE2 shorthand escapes.
	backslashDFn, err := compileGlobPattern(`[\d]`)
	require.NoError(t, err)
	assert.True(t, backslashDFn(`\`))
	assert.True(t, backslashDFn("d"))
	assert.False(t, backslashDFn("5"))

	backslashOnlyFn, err := compileGlobPattern(`[\]`)
	require.NoError(t, err)
	assert.True(t, backslashOnlyFn(`\`))
	assert.False(t, backslashOnlyFn("]"))

	// SCAN-4: "**/" only collapses zero-or-more segments at a segment boundary.
	nonBoundaryFn, err := compileGlobPattern("foo**/bar.go")
	require.NoError(t, err)
	assert.False(t, nonBoundaryFn("foobar.go"))
	assert.True(t, nonBoundaryFn("foo/bar.go"))
	assert.True(t, nonBoundaryFn("foox/bar.go"))

	// Trailing /** matches entries under pkg/, not a file literally equal to "pkg".
	trailingFn, err := compileGlobPattern("pkg/**")
	require.NoError(t, err)
	assert.True(t, trailingFn("pkg/a.go"))
	assert.True(t, trailingFn("pkg/sub/b.go"))
	assert.False(t, trailingFn("pkg"))
}

func TestFileSearch_LocalTimeoutSendsDeadlineExceeded(t *testing.T) {
	// Not parallel: mutates package-level fileSearchTimeout.
	orig := fileSearchTimeout
	fileSearchTimeout = -time.Second
	t.Cleanup(func() { fileSearchTimeout = orig })

	_, d, _ := setupTestService(t)
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\n"), 0o644))

	wSearch := &testResponseWriter{}
	dispatch(d, leapmuxv1connect.WorkerFileServiceSearchFilesProcedure, &leapmuxv1.SearchFilesRequest{
		Path:    root,
		Pattern: "hello",
	}, wSearch)
	require.Len(t, wSearch.errors, 1, "expired local timeout must send DeadlineExceeded instead of returning silently")
	assert.Equal(t, int32(codes.DeadlineExceeded), wSearch.errors[0].code)

	wGlob := &testResponseWriter{}
	dispatch(d, leapmuxv1connect.WorkerFileServiceGlobFilesProcedure, &leapmuxv1.GlobFilesRequest{
		Path:    root,
		Pattern: "*.txt",
	}, wGlob)
	require.Len(t, wGlob.errors, 1, "expired local timeout must send DeadlineExceeded instead of returning silently")
	assert.Equal(t, int32(codes.DeadlineExceeded), wGlob.errors[0].code)
}

func TestAgentSearch_LocalTimeoutSendsDeadlineExceeded(t *testing.T) {
	// Not parallel: mutates package-level agentSearchTimeout.
	orig := agentSearchTimeout
	agentSearchTimeout = -time.Second
	t.Cleanup(func() { agentSearchTimeout = orig })

	svc, d, _ := setupTestService(t)
	seedAgent(t, svc, "agent-timeout")

	w := &testResponseWriter{}
	dispatch(d, leapmuxv1connect.WorkerAgentServiceSearchAgentMessagesProcedure, &leapmuxv1.SearchAgentMessagesRequest{
		Query: "hello",
	}, w)
	require.Len(t, w.errors, 1, "expired local timeout must send DeadlineExceeded instead of returning silently")
	assert.Equal(t, int32(codes.DeadlineExceeded), w.errors[0].code)
}

func TestAgentSearch_IgnoresNonReplyAgentFramesAndReportsQueryErrors(t *testing.T) {
	t.Parallel()
	svc, d, _ := setupTestService(t)
	seedAgent(t, svc, "agent-search-1")

	ctx := context.Background()
	// Non-reply agent reasoning/tool frames carrying "text" or "content" keys
	// must NOT match transcript search when Provider.ReplyText returns nil.
	_, err := svc.Queries.CreateMessage(ctx, db.CreateMessageParams{
		ID:                             "m-reasoning",
		AgentID:                        "agent-search-1",
		Source:                         leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		Content:                        []byte(`{"type":"assembled_message","kind":"reasoning","text":"secret internal thinking"}`),
		ContentCompression:             leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		SupplementalContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
	})
	require.NoError(t, err)
	_, err = svc.Queries.CreateMessage(ctx, db.CreateMessageParams{
		ID:                             "m-tool",
		AgentID:                        "agent-search-1",
		Source:                         leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		Content:                        []byte(`{"type":"tool_result","content":"secret tool output"}`),
		ContentCompression:             leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		SupplementalContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
	})
	require.NoError(t, err)
	// Real user prompt row using Worker's {"content": ...} shape must match.
	_, err = svc.Queries.CreateMessage(ctx, db.CreateMessageParams{
		ID:                             "m-user",
		AgentID:                        "agent-search-1",
		Source:                         leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		Content:                        []byte(`{"content":"please check secret user prompt"}`),
		ContentCompression:             leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		SupplementalContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
	})
	require.NoError(t, err)

	w := &testResponseWriter{}
	dispatch(d, leapmuxv1connect.WorkerAgentServiceSearchAgentMessagesProcedure, &leapmuxv1.SearchAgentMessagesRequest{
		Query: "secret",
	}, w)
	require.Empty(t, w.errors)
	var resp leapmuxv1.SearchAgentMessagesResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.Len(t, resp.GetHits(), 1, "only the user prompt should match, not non-reply agent JSON frames")
	assert.Contains(t, resp.GetHits()[0].GetSnippet(), "secret user prompt")

	// SCAN-7: SQL error while scanning an agent's messages must return Internal, not OK with 0 hits.
	_, err = svc.DB.ExecContext(ctx, `DROP TABLE messages`)
	require.NoError(t, err)

	wErr := &testResponseWriter{}
	dispatch(d, leapmuxv1connect.WorkerAgentServiceSearchAgentMessagesProcedure, &leapmuxv1.SearchAgentMessagesRequest{
		Query:    "secret",
		AgentIds: []string{"agent-search-1"},
	}, wErr)
	require.Len(t, wErr.errors, 1, "SQL query error during message scan must return an RPC error")
	assert.Equal(t, int32(codes.Internal), wErr.errors[0].code)
}

func TestAgentSearch_BuildSearchSnippetWindow(t *testing.T) {
	t.Parallel()
	prefix := strings.Repeat("가", 150)
	suffix := strings.Repeat("나", 150)
	text := prefix + "   NEEDLE\tWORD   " + suffix
	start := strings.Index(text, "NEEDLE")

	snippet := buildSearchSnippet(text, start)
	assert.True(t, strings.HasPrefix(snippet, "…"))
	assert.True(t, strings.HasSuffix(snippet, "…"))
	assert.Contains(t, snippet, "NEEDLE WORD")
	assert.LessOrEqual(t, len([]rune(snippet)), agentSearchSnippetMaxRunes+2)
}
