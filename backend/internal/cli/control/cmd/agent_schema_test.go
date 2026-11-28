package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

func TestSchemaRetryRequestID_MatchesRequestStatusLookup(t *testing.T) {
	agentID := fullTestID("ag-schema")
	assert.Empty(t, schemaRetryRequestID("", 1))

	retryReqID := schemaRetryRequestID("req-1", 1)
	assert.Equal(t, "req-1:schema-retry:1", retryReqID)
	assert.Equal(t,
		requestScopedID(retryReqID, requestScopeAgentSend, agentID),
		requestScopedID("req-1:schema-retry:1", requestScopeAgentSend, agentID),
		"request-status derives the exact same input ID from the retry request ID",
	)
	assert.NotEqual(t,
		requestScopedID("req-1", requestScopeAgentSend, agentID),
		requestScopedID(retryReqID, requestScopeAgentSend, agentID),
	)
}

func TestAgentSendWithSchemaOverIPC_RetrySucceedsReusingSingleActivityFeed(t *testing.T) {
	id := fullTestID("ag-2")
	w := newActivityWorker(map[string]leapmuxv1.AgentActivityState{id: idle})
	startAgentWorker(t, id, w)
	ctx := testutil.DeadlineContext(t)

	bgErr := make(chan error, 1)
	go func() {
		// Attempt 0: returns prose that fails JSON Schema validation.
		select {
		case in0 := <-w.enqueued:
			w.setState(id, working)
			w.deliver(in0.GetInputId(), false, replyRow(0, "not valid json"))
			w.setState(id, idle)
		case <-ctx.Done():
			bgErr <- ctx.Err()
			return
		}
		// Attempt 1: returns valid JSON matching the schema.
		select {
		case in1 := <-w.enqueued:
			w.setState(id, working)
			w.deliver(in1.GetInputId(), false, replyRow(0, `{"ok":true}`))
			w.setState(id, idle)
			bgErr <- nil
		case <-ctx.Done():
			bgErr <- ctx.Err()
		}
	}()

	data := runControl(t, "agent", "send", id, "Check build", "--wait",
		"--output-schema", `{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"]}`,
		"--schema-retries", "2", "--request-id", "schema-ok").Data(t)
	require.NoError(t, receive(t, ctx, bgErr))

	assert.Equal(t, float64(2), data["schema_attempts"])
	assert.Equal(t, map[string]any{"ok": true}, data["structured"])
	assert.Equal(t, requestScopedID("schema-ok:schema-retry:1", requestScopeAgentSend, id), data["input_id"])

	watchCalls := 0
	for _, method := range w.called() {
		if method == leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure {
			watchCalls++
		}
	}
	assert.Equal(t, 1, watchCalls, "schema retries must reuse one activity feed instead of opening a new watch per attempt")
}

func TestAgentSendWithSchemaOverIPC_Attempt1FailureNeverReportsNotSent(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	id := fullTestID("ag-3")
	rw := &retryWorker{activityWorker: newActivityWorker(map[string]leapmuxv1.AgentActivityState{id: idle})}
	startAgentWorker(t, id, rw)

	bgErr := make(chan error, 1)
	go func() {
		select {
		case in0 := <-rw.enqueued:
			// Wait until attempt 0 completes its initial queue poll while in0
			// is still queued, then populate the exact reply and transition
			// straight to waiting_for_user without an intermediate working
			// event so attempt 0 settles with ws.state == WAITING_FOR_USER.
			select {
			case <-rw.queueRead:
			case <-ctx.Done():
				bgErr <- ctx.Err()
				return
			}
			rw.deliver(in0.GetInputId(), false, replyRow(0, "not json"))
			rw.mu.Lock()
			rw.exactInputID = in0.GetInputId()
			rw.exactReply = &leapmuxv1.AgentInputTurnReply{
				AcceptedSeq: 1,
				Outcome:     leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
			}
			rw.exactRows = []*leapmuxv1.AgentChatMessage{replyRow(2, "not json")}
			rw.mu.Unlock()
			rw.setState(id, waiting)
			bgErr <- nil
		case <-ctx.Done():
			bgErr <- ctx.Err()
		}
	}()

	o := runControl(t, "agent", "send", id, "Check build", "--wait",
		"--output-schema", `{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"]}`,
		"--schema-retries", "1", "--request-id", "schema-fail")
	require.NoError(t, receive(t, ctx, bgErr))
	f := o.requireFailure(t, cli.CodeAgentWaiting)
	require.NotNil(t, f.Receipt)
	assert.Equal(t, dispositionInProgress, f.Receipt.Disposition, "attempt >= 1 must never report not_sent after attempt 0 already ran")
	assert.Contains(t, f.Hints, "check active schema retry status: leapmux agent request-status "+id+" schema-fail:schema-retry:1")
}

func TestAgentSendWithSchemaOverIPC_Attempt1WaitingPointsRequestStatusToRetryRequestID(t *testing.T) {
	id := fullTestID("ag-2")
	w := newActivityWorker(map[string]leapmuxv1.AgentActivityState{id: idle})
	startAgentWorker(t, id, w)
	ctx := testutil.DeadlineContext(t)

	bgErr := make(chan error, 1)
	go func() {
		select {
		case in0 := <-w.enqueued:
			w.setState(id, working)
			w.deliver(in0.GetInputId(), false, replyRow(0, "not valid json"))
			w.setState(id, idle)
		case <-ctx.Done():
			bgErr <- ctx.Err()
			return
		}
		select {
		case in1 := <-w.enqueued:
			w.setState(id, working)
			w.deliver(in1.GetInputId(), true, replyRow(0, "May I run the tool?"))
			w.setState(id, waiting)
			bgErr <- nil
		case <-ctx.Done():
			bgErr <- ctx.Err()
		}
	}()

	o := runControl(t, "agent", "send", id, "Check build", "--wait",
		"--output-schema", `{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"]}`,
		"--schema-retries", "1", "--request-id", "schema-wait")
	require.NoError(t, receive(t, ctx, bgErr))
	require.Equal(t, cli.ExitWaitEnded, o.Code, o.Stderr)

	data := envelopeData(t, []byte(o.Stdout))
	wantRetryReqID := "schema-wait:schema-retry:1"
	wantRetryInputID := requestScopedID(wantRetryReqID, requestScopeAgentSend, id)
	assert.Equal(t, wantRetryInputID, data["input_id"])
	assert.Equal(t, float64(2), data["schema_attempts"])
	receipt, ok := data["receipt"].(map[string]any)
	require.True(t, ok, "non-settled keyed wait must include receipt")
	assert.Equal(t, wantRetryReqID, receipt["request_id"])
	assert.Equal(t, "leapmux agent request-status "+id+" "+wantRetryReqID, receipt["query_command"])
	assert.Contains(t, o.Stderr, "check status: leapmux agent request-status "+id+" "+wantRetryReqID)
}

func TestAgentSendWithSchemaOverIPC_ZeroRetriesFailsWithConflictAndPreservesReceipt(t *testing.T) {
	id := fullTestID("ag-4")
	w := newActivityWorker(map[string]leapmuxv1.AgentActivityState{id: idle})
	startAgentWorker(t, id, w)
	ctx := testutil.DeadlineContext(t)

	bgErr := make(chan error, 1)
	go func() {
		select {
		case in0 := <-w.enqueued:
			w.setState(id, working)
			w.deliver(in0.GetInputId(), false, replyRow(0, "not valid json"))
			w.setState(id, idle)
			bgErr <- nil
		case <-ctx.Done():
			bgErr <- ctx.Err()
		}
	}()

	o := runControl(t, "agent", "send", id, "Check build", "--wait",
		"--output-schema", `{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"]}`,
		"--schema-retries", "0", "--request-id", "schema-zero")
	require.NoError(t, receive(t, ctx, bgErr))
	assert.Equal(t, cli.ExitWaitEnded, o.Code, "stderr: %s", o.Stderr)

	f := o.Failure(t)
	assert.Equal(t, cli.CodeConflict, f.Code)
	assert.Contains(t, f.Message, "after 1 attempt(s)")
	require.NotNil(t, f.Receipt, "schema validation conflict must preserve retry receipt")
	assert.Equal(t, "schema-zero", f.Receipt.RequestID)
	assert.Equal(t, dispositionInProgress, f.Receipt.Disposition)
	assert.Equal(t, "leapmux agent request-status "+id+" schema-zero", f.Receipt.QueryCommand)
	hints := strings.Join(f.Hints, "\n")
	assert.NotContains(t, hints, "belongs to a request with other arguments", "must not attach misleading request-id collision hint")
	assert.Contains(t, hints, "check status: leapmux agent request-status "+id+" schema-zero")

	enqueueCalls := 0
	for _, method := range w.called() {
		if method == leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure {
			enqueueCalls++
		}
	}
	assert.Equal(t, 1, enqueueCalls, "--schema-retries 0 must not send a corrective follow-up turn")
}

func TestReadFileAndOutputSchemaAutoExcludeLeapMuxTmp(t *testing.T) {
	t.Run("readFileOrStdin excludes .leapmux/tmp in git info exclude", func(t *testing.T) {
		repo := testutil.NewGitRepo(t)
		tmpDir := filepath.Join(repo, ".leapmux", "tmp")
		require.NoError(t, os.MkdirAll(tmpDir, 0o755))
		briefPath := filepath.Join(tmpDir, "review-brief.md")
		require.NoError(t, os.WriteFile(briefPath, []byte("Review this diff."), 0o644))

		got, err := readFileOrStdin(strings.NewReader(""), briefPath, "--file")
		require.NoError(t, err)
		assert.Equal(t, "Review this diff.", string(got))

		excludeBytes, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
		require.NoError(t, err)
		assert.Contains(t, string(excludeBytes), ".leapmux/tmp/")
	})

	t.Run("readOutputSchemaSpec excludes .leapmux/tmp in git info exclude", func(t *testing.T) {
		repo := testutil.NewGitRepo(t)
		tmpDir := filepath.Join(repo, ".leapmux", "tmp")
		require.NoError(t, os.MkdirAll(tmpDir, 0o755))
		schemaPath := filepath.Join(tmpDir, "review-schema.json")
		require.NoError(t, os.WriteFile(schemaPath, []byte(`{"type":"object","properties":{"ok":{"type":"boolean"}}}`), 0o644))

		cmd := &cobra.Command{Use: "open"}
		addOutputSchemaFlags(cmd.Flags())
		require.NoError(t, cmd.Flags().Set(outputSchemaFlag, schemaPath))
		inv := &cli.Invocation{Command: cmd, Stdin: strings.NewReader("")}

		spec, err := readOutputSchemaSpec(inv, true, true, false)
		require.NoError(t, err)
		require.NotNil(t, spec)

		excludeBytes, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
		require.NoError(t, err)
		assert.Contains(t, string(excludeBytes), ".leapmux/tmp/")
	})
}
