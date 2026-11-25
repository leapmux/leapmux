package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/testutil"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
)

func TestSearchAgentMessages_LiteralRegexAndFilters(t *testing.T) {
	t.Parallel()
	svc, d, _ := setupTestService(t)
	seedAgent(t, svc, "agent-a")
	seedAgent(t, svc, "agent-b")

	ctx := context.Background()
	seqA, err := svc.Queries.CreateMessage(ctx, db.CreateMessageParams{
		ID:                             "m1",
		AgentID:                        "agent-a",
		Source:                         leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		Content:                        []byte(`{"content":"Investigate flaky checkout race condition"}`),
		ContentCompression:             leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		SupplementalContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
	})
	require.NoError(t, err)
	_, err = svc.Queries.CreateMessage(ctx, db.CreateMessageParams{
		ID:                             "m2",
		AgentID:                        "agent-b",
		Source:                         leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		Content:                        []byte(`{"content":"Deploy version 2.4.1 to staging"}`),
		ContentCompression:             leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		SupplementalContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
	})
	require.NoError(t, err)

	// Case-insensitive literal search across all agents.
	w := &testResponseWriter{}
	dispatch(d, leapmuxv1connect.WorkerAgentServiceSearchAgentMessagesProcedure, &leapmuxv1.SearchAgentMessagesRequest{
		Query: "RACE CONDITION",
	}, w)
	require.Empty(t, w.errors)
	var resp leapmuxv1.SearchAgentMessagesResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.Len(t, resp.GetHits(), 1)
	assert.Equal(t, "agent-a", resp.GetHits()[0].GetAgentId())
	assert.Equal(t, seqA, resp.GetHits()[0].GetSeq())
	assert.Contains(t, resp.GetHits()[0].GetSnippet(), "race condition")

	// Regex search with agent filter.
	w2 := &testResponseWriter{}
	dispatch(d, leapmuxv1connect.WorkerAgentServiceSearchAgentMessagesProcedure, &leapmuxv1.SearchAgentMessagesRequest{
		Query:    `version \d+\.\d+\.\d+`,
		Regex:    true,
		AgentIds: []string{"agent-b"},
	}, w2)
	require.Empty(t, w2.errors)
	var resp2 leapmuxv1.SearchAgentMessagesResponse
	require.NoError(t, proto.Unmarshal(w2.responses[0].GetPayload(), &resp2))
	require.Len(t, resp2.GetHits(), 1)
	assert.Equal(t, "agent-b", resp2.GetHits()[0].GetAgentId())

	// Invalid regex rejected.
	w3 := &testResponseWriter{}
	dispatch(d, leapmuxv1connect.WorkerAgentServiceSearchAgentMessagesProcedure, &leapmuxv1.SearchAgentMessagesRequest{
		Query: "[unclosed",
		Regex: true,
	}, w3)
	require.Len(t, w3.errors, 1)
	assert.Equal(t, codeInvalidArgument, w3.errors[0].code)
}

func TestFileSearchAndGlob(t *testing.T) {
	t.Parallel()
	_, d, _ := setupTestService(t)
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "pkg", "sub"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "node_modules", "dep"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "pkg", "main.go"), []byte("package main\nfunc HelloWorld() {}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "pkg", "sub", "helper.go"), []byte("package sub\n// hello helper\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "pkg", "bin.dat"), []byte{0, 1, 2, 'h', 'e', 'l', 'l', 'o'}, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "node_modules", "dep", "ignored.go"), []byte("hello ignored\n"), 0o644))

	// Case-insensitive search skips binary and node_modules.
	w := &testResponseWriter{}
	dispatch(d, leapmuxv1connect.WorkerFileServiceSearchFilesProcedure, &leapmuxv1.SearchFilesRequest{
		Path:       root,
		Pattern:    "hello",
		IgnoreCase: true,
		Glob:       "**/*.go",
	}, w)
	require.Empty(t, w.errors)
	var sresp leapmuxv1.SearchFilesResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &sresp))
	require.Len(t, sresp.GetMatches(), 2)

	// Glob matches nested .go files and skips node_modules.
	w2 := &testResponseWriter{}
	dispatch(d, leapmuxv1connect.WorkerFileServiceGlobFilesProcedure, &leapmuxv1.GlobFilesRequest{
		Path:    root,
		Pattern: "**/*.go",
	}, w2)
	require.Empty(t, w2.errors)
	var gresp leapmuxv1.GlobFilesResponse
	require.NoError(t, proto.Unmarshal(w2.responses[0].GetPayload(), &gresp))
	assert.ElementsMatch(t, []string{"pkg/main.go", "pkg/sub/helper.go"}, gresp.GetPaths())
}

func TestGitOps_DiffLogCommitAndWorktree(t *testing.T) {
	t.Parallel()
	_, d, _ := setupTestService(t)
	repo := testutil.NewGitRepo(t)
	runGit := func(args ...string) {
		t.Helper()
		require.NoError(t, gitutil.Run(context.Background(), repo, args...))
	}
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644))
	runGit("add", "README.md")
	runGit("commit", "-m", "Initial commit")

	// Modify file and check GetGitDiff.
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\nworld\n"), 0o644))
	wDiff := &testResponseWriter{}
	dispatch(d, leapmuxv1connect.WorkerGitServiceGetGitDiffProcedure, &leapmuxv1.GetGitDiffRequest{
		Path: repo,
	}, wDiff)
	require.Empty(t, wDiff.errors)
	var diffResp leapmuxv1.GetGitDiffResponse
	require.NoError(t, proto.Unmarshal(wDiff.responses[0].GetPayload(), &diffResp))
	assert.Contains(t, diffResp.GetDiff(), "+world")
	assert.False(t, diffResp.GetTruncated())

	// Option injection rejected.
	wInj := &testResponseWriter{}
	dispatch(d, leapmuxv1connect.WorkerGitServiceGetGitDiffProcedure, &leapmuxv1.GetGitDiffRequest{
		Path:    repo,
		BaseRef: "--no-index",
	}, wInj)
	require.Len(t, wInj.errors, 1)
	assert.Equal(t, codeInvalidArgument, wInj.errors[0].code)

	// GitCommit with stage_all.
	wCommit := &testResponseWriter{}
	dispatch(d, leapmuxv1connect.WorkerGitServiceGitCommitProcedure, &leapmuxv1.GitCommitRequest{
		Path:     repo,
		Message:  "Add world line",
		StageAll: true,
	}, wCommit)
	require.Empty(t, wCommit.errors)
	var commitResp leapmuxv1.GitCommitResponse
	require.NoError(t, proto.Unmarshal(wCommit.responses[0].GetPayload(), &commitResp))
	assert.NotEmpty(t, commitResp.GetHash())
	assert.Equal(t, "Add world line", commitResp.GetSubject())

	// GetGitLog returns both commits newest-first.
	wLog := &testResponseWriter{}
	dispatch(d, leapmuxv1connect.WorkerGitServiceGetGitLogProcedure, &leapmuxv1.GetGitLogRequest{
		Path:  repo,
		Limit: 10,
	}, wLog)
	require.Empty(t, wLog.errors)
	var logResp leapmuxv1.GetGitLogResponse
	require.NoError(t, proto.Unmarshal(wLog.responses[0].GetPayload(), &logResp))
	require.Len(t, logResp.GetCommits(), 3)
	assert.Equal(t, "Add world line", logResp.GetCommits()[0].GetSubject())
	assert.Equal(t, "Initial commit", logResp.GetCommits()[1].GetSubject())
	assert.Equal(t, "init", logResp.GetCommits()[2].GetSubject())

	// CreateGitWorktree + RemoveGitWorktree lifecycle.
	wtPath := filepath.Join(t.TempDir(), "wt-feat")
	wCreate := &testResponseWriter{}
	dispatch(d, leapmuxv1connect.WorkerGitServiceCreateGitWorktreeProcedure, &leapmuxv1.CreateGitWorktreeRequest{
		Path:         repo,
		Branch:       "feat-cli",
		WorktreePath: wtPath,
	}, wCreate)
	require.Empty(t, wCreate.errors)
	var createResp leapmuxv1.CreateGitWorktreeResponse
	require.NoError(t, proto.Unmarshal(wCreate.responses[0].GetPayload(), &createResp))
	assert.Equal(t, "feat-cli", createResp.GetBranch())
	assert.DirExists(t, createResp.GetWorktreePath())

	// Refuses removing main working tree.
	wMain := &testResponseWriter{}
	dispatch(d, leapmuxv1connect.WorkerGitServiceRemoveGitWorktreeProcedure, &leapmuxv1.RemoveGitWorktreeRequest{
		Path: repo,
	}, wMain)
	require.Len(t, wMain.errors, 1)
	assert.Equal(t, codeInvalidArgument, wMain.errors[0].code)

	// Removes linked worktree cleanly.
	wRm := &testResponseWriter{}
	dispatch(d, leapmuxv1connect.WorkerGitServiceRemoveGitWorktreeProcedure, &leapmuxv1.RemoveGitWorktreeRequest{
		Path:         createResp.GetWorktreePath(),
		DeleteBranch: true,
	}, wRm)
	require.Empty(t, wRm.errors)
	assert.NoDirExists(t, createResp.GetWorktreePath())
}

func TestPhase4_UTF8SafetyLongLinesAndGlobEdgeCases(t *testing.T) {
	t.Parallel()
	_, d, _ := setupTestService(t)
	root := t.TempDir()

	// Multi-byte UTF-8 long line (>500 bytes), non-UTF-8 Latin-1 bytes, and >16 KiB line followed by a match.
	longUTF8 := strings.Repeat("한글", 300) + " NEEDLE_UTF8\n"
	nonUTF8 := append([]byte("prefix \xff\xfe NEEDLE_LATIN\n"), []byte(strings.Repeat("x", 20*1024)+"\nAFTER_BIG_LINE\n")...)
	require.NoError(t, os.WriteFile(filepath.Join(root, "utf8.txt"), []byte(longUTF8), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "mixed.txt"), nonUTF8, 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "a"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a", "b.txt"), []byte("ok\n"), 0o644))

	wSearch := &testResponseWriter{}
	dispatch(d, leapmuxv1connect.WorkerFileServiceSearchFilesProcedure, &leapmuxv1.SearchFilesRequest{
		Path:    root,
		Pattern: "NEEDLE_|AFTER_BIG_LINE",
		Regex:   true,
	}, wSearch)
	require.Empty(t, wSearch.errors)
	var resp leapmuxv1.SearchFilesResponse
	require.NoError(t, proto.Unmarshal(wSearch.responses[0].GetPayload(), &resp))
	require.Len(t, resp.GetMatches(), 3)

	// Glob edge cases: [!x] must NOT match '/', and []a] must compile as literal ']' or 'a'.
	negFn, err := compileGlobPattern("[!x]*")
	require.NoError(t, err)
	assert.False(t, negFn("/b.txt"), "[!x] must not match path separator '/'")
	assert.True(t, negFn("a.txt"))

	bracketFn, err := compileGlobPattern("[]a].txt")
	require.NoError(t, err)
	assert.True(t, bracketFn("].txt"))
	assert.True(t, bracketFn("a.txt"))
	assert.False(t, bracketFn("b.txt"))
}

func TestPhase4_GitDiffMidRuneTruncationAndGoalEventNotify(t *testing.T) {
	t.Parallel()
	_, d, _ := setupTestService(t)
	repo := testutil.NewGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "f.txt"), []byte("base\n"), 0o644))
	require.NoError(t, gitutil.Run(context.Background(), repo, "add", "f.txt"))
	require.NoError(t, gitutil.Run(context.Background(), repo, "commit", "-m", "init"))

	// Write multi-byte UTF-8 content so small byte limits land mid-rune.
	require.NoError(t, os.WriteFile(filepath.Join(repo, "f.txt"), []byte("base\n"+strings.Repeat("가나다라", 40)+"\n"), 0o644))
	for limit := int64(80); limit <= 92; limit++ {
		w := &testResponseWriter{}
		dispatch(d, leapmuxv1connect.WorkerGitServiceGetGitDiffProcedure, &leapmuxv1.GetGitDiffRequest{
			Path:  repo,
			Limit: limit,
		}, w)
		require.Empty(t, w.errors, "limit=%d should not fail UTF-8 proto marshaling", limit)
		var resp leapmuxv1.GetGitDiffResponse
		require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
		assert.True(t, resp.GetTruncated())
	}

	// Verify AgentEvent_GoalChanged is classified as classNotify for WATCH_MODE_NOTIFY.
	assert.Equal(t, classNotify, agentEventClass(&leapmuxv1.AgentEvent{
		Event: &leapmuxv1.AgentEvent_GoalChanged{},
	}))
}

func TestPhase4_TerminalSendInputIdempotency(t *testing.T) {
	t.Parallel()
	var c terminalSendInputReceipts
	req1 := &leapmuxv1.SendInputRequest{TerminalId: "t1", Data: []byte("echo hi\n"), RequestId: "req-1"}
	fp1 := terminalSendInputFingerprint(req1)

	started := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan sendInputReceiptOutcome, 1)
	go func() {
		o, err := c.execute(context.Background(), "t1", "req-1", fp1, func() error {
			close(started)
			<-release
			return nil
		})
		require.NoError(t, err)
		firstDone <- o
	}()
	<-started

	// WRAPPERS-1: Filling the 1024-entry completed FIFO must not evict the in-flight entry.
	for i := range maxTerminalSendInputReceipts + 16 {
		id := strings.Repeat("k", 1) + string(rune('a'+(i%26))) + "-" + string(rune('0'+(i/26)%10)) + "-" + string(rune('0'+(i/260)%10)) + "-" + string(rune('0'+(i/2600)%10))
		o, err := c.execute(context.Background(), "t1", id, fp1, func() error { return nil })
		require.NoError(t, err)
		require.Equal(t, sendInputNew, o)
	}

	// A concurrent retry while the first call is still in flight must wait and
	// observe sendInputDuplicate once fn returns nil — never sendInputNew.
	dupDone := make(chan sendInputReceiptOutcome, 1)
	go func() {
		o, err := c.execute(context.Background(), "t1", "req-1", fp1, func() error {
			t.Error("duplicate retry must not execute fn")
			return nil
		})
		require.NoError(t, err)
		dupDone <- o
	}()

	// TRAPS-13: Canceled context while waiting on an in-flight duplicate returns promptly.
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.execute(canceledCtx, "t1", "req-1", fp1, func() error { return nil })
	require.ErrorIs(t, err, context.Canceled)

	// Different payload with the same request_id conflicts immediately.
	req2 := &leapmuxv1.SendInputRequest{TerminalId: "t1", Data: []byte("rm -rf /\n"), RequestId: "req-1"}
	conflictOutcome, err := c.execute(context.Background(), "t1", "req-1", terminalSendInputFingerprint(req2), func() error { return nil })
	require.NoError(t, err)
	assert.Equal(t, sendInputConflict, conflictOutcome)

	close(release)
	assert.Equal(t, sendInputNew, <-firstDone)
	assert.Equal(t, sendInputDuplicate, <-dupDone)

	// Subsequent retry after completion also observes duplicate.
	afterOutcome, err := c.execute(context.Background(), "t1", "req-1", fp1, func() error { return nil })
	require.NoError(t, err)
	assert.Equal(t, sendInputDuplicate, afterOutcome)

	// TRAPS-13: Panic in fn releases the reservation so a later retry runs cleanly.
	require.Panics(t, func() {
		_, _ = c.execute(context.Background(), "t1", "req-panic", fp1, func() error {
			panic("boom")
		})
	})
	o, err := c.execute(context.Background(), "t1", "req-panic", fp1, func() error { return nil })
	require.NoError(t, err)
	assert.Equal(t, sendInputNew, o)
}
