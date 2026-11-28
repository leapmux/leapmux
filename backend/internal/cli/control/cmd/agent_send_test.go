package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
)

func TestParseSendAt(t *testing.T) {
	now := time.Date(2026, 11, 19, 7, 0, 0, 0, time.UTC)
	for raw, want := range map[string]struct {
		at       time.Time
		relative bool
	}{
		"2026-11-20T09:00:00+09:00":     {time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC), false},
		"2026-11-19T07:00:01Z":          {time.Date(2026, 11, 19, 7, 0, 1, 0, time.UTC), false},
		" 2026-11-19T08:30:00.250Z ":    {time.Date(2026, 11, 19, 8, 30, 0, 250_000_000, time.UTC), false},
		"2026-11-19T02:00:00.5-05:30":   {time.Date(2026, 11, 19, 7, 30, 0, 500_000_000, time.UTC), false},
		"+10m":                          {now.Add(10 * time.Minute), true},
		"+1h30m":                        {now.Add(90 * time.Minute), true},
		"+1ms":                          {now.Add(time.Millisecond), true},
		"+87600h":                       {now.Add(87600 * time.Hour), true},
		"2026-11-19T07:00:00.001+00:00": {now.Add(time.Millisecond), false},
		"2026-11-19T07:00:00Z":          {now, false},
		"2025-11-19T09:00:00Z":          {time.Date(2025, 11, 19, 9, 0, 0, 0, time.UTC), false},
	} {
		sched, err := parseSendAt(raw, now)
		require.NoError(t, err, raw)
		assert.True(t, want.at.Equal(sched.At), "%q: want %v, got %v", raw, want.at, sched.At)
		assert.Equal(t, want.relative, sched.Relative, raw)
	}

	for raw, message := range map[string]string{
		"":                          "--at is empty",
		"   ":                       "--at is empty",
		"10m":                       "neither an RFC 3339 time",
		"2026-11-20T09:00:00":       "neither an RFC 3339 time",
		"2026-11-20 09:00:00+09:00": "neither an RFC 3339 time",
		"tomorrow":                  "neither an RFC 3339 time",
		"+":                         "is not a duration",
		"++10m":                     "is not a duration",
		"+++10m":                    "is not a duration",
		"+ten minutes":              "is not a duration",
		"+0s":                       "is not after now",
		"+-5m":                      "is not after now",
	} {
		_, err := parseSendAt(raw, now)
		e := cli.FromError(err)
		require.NotNil(t, e, raw)
		assert.Equal(t, cli.CodeUsage, e.Code, raw)
		assert.Contains(t, e.Message, message, raw)
	}
}

// writeFile writes data to name in dir and returns its path.
func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

func TestReadAttachments(t *testing.T) {
	dir := t.TempDir()
	png := writeFile(t, dir, "screen shot.png", []byte{0x89, 'P', 'N', 'G'})
	notes := writeFile(t, dir, "notes.md", []byte("# Notes"))
	empty := writeFile(t, dir, "empty.txt", nil)

	t.Run("reads each file with its name and no media type", func(t *testing.T) {
		got, err := readAttachments([]string{png, notes, empty}, 1024)
		require.NoError(t, err)
		require.Len(t, got, 3)
		assert.Equal(t, "screen shot.png", got[0].GetFilename())
		assert.Equal(t, []byte{0x89, 'P', 'N', 'G'}, got[0].GetData())
		assert.Empty(t, got[0].GetMimeType(), "the Worker infers the type with the classifier of its providers")
		assert.Equal(t, "notes.md", got[1].GetFilename())
		assert.Empty(t, got[2].GetData(), "an empty file is an empty attachment")
	})
	t.Run("no file", func(t *testing.T) {
		got, err := readAttachments(nil, 0)
		require.NoError(t, err)
		assert.Empty(t, got)
	})
	t.Run("fits the budget exactly", func(t *testing.T) {
		_, err := readAttachments([]string{png, notes}, 4+7)
		require.NoError(t, err)
	})
	t.Run("the budget counts every file", func(t *testing.T) {
		_, err := readAttachments([]string{png, notes}, 4+6)
		requireUsage(t, err, "--attach "+notes+" makes the message too large")
	})
	t.Run("an empty budget takes an empty file only", func(t *testing.T) {
		_, err := readAttachments([]string{empty}, 0)
		require.NoError(t, err)
		_, err = readAttachments([]string{png}, 0)
		requireUsage(t, err, "--attach "+png+" makes the message too large")
	})
	t.Run("an empty path", func(t *testing.T) {
		_, err := readAttachments([]string{""}, 1024)
		requireUsage(t, err, `--attach ""`)
	})
	t.Run("a missing file", func(t *testing.T) {
		missing := filepath.Join(dir, "missing.png")
		_, err := readAttachments([]string{missing}, 1024)
		requireUsage(t, err, missing)
	})
	t.Run("a directory", func(t *testing.T) {
		_, err := readAttachments([]string{dir}, 1024)
		requireUsage(t, err, "is a directory")
	})
	t.Run("a file without an end", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows has no /dev/zero")
		}
		// The read stops one byte past the budget, so a stream without an
		// end fails instead of filling the memory of the CLI.
		_, err := readAttachments([]string{"/dev/zero"}, 1024)
		requireUsage(t, err, "--attach /dev/zero makes the message too large")
	})
}

// sendTo runs `agent send <agent> <args...> --json` against w and returns the
// outcome.
func sendTo(t *testing.T, agentID string, args ...string) cliOutcome {
	t.Helper()
	return runControl(t, append([]string{"agent", "send", agentID}, args...)...)
}

func TestAgentSendOverIPC_SendsTheAttachmentsAndTheSchedule(t *testing.T) {
	id := fullTestID("ag-2")
	w := newQueueWorker()
	startAgentWorker(t, id, w)
	dir := t.TempDir()
	png := writeFile(t, dir, "shot.png", []byte{0x89, 'P', 'N', 'G'})
	log := writeFile(t, dir, "console.log", []byte("TypeError"))

	data := sendTo(t, id, "Why does the page look like this?", "--attach", png, "--attach", log, "--at", "2099-01-02T12:04:05.678+09:00").Data(t)
	inputID, _ := data["input_id"].(string)
	require.NotEmpty(t, inputID)
	reqID, _ := data["request_id"].(string)
	require.NotEmpty(t, reqID)
	assert.Equal(t, map[string]any{"id": id, "input_id": inputID, "request_id": reqID, "scheduled_at": "2099-01-02T03:04:05.678Z"}, data, "the result states the time in UTC")
	requireRequests(t, w, &leapmuxv1.EnqueueAgentInputRequest{
		AgentId: id, InputId: inputID, Text: "Why does the page look like this?",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		Attachments: []*leapmuxv1.Attachment{
			{Filename: "shot.png", Data: []byte{0x89, 'P', 'N', 'G'}},
			{Filename: "console.log", Data: []byte("TypeError")},
		},
		ScheduledAt:           "2099-01-02T03:04:05.678Z",
		RequireFutureSchedule: true,
	})
}

func TestAgentSendOverIPC_RelativeScheduleOmitsAutoRequestID(t *testing.T) {
	id := fullTestID("ag-rel")
	w := newQueueWorker()
	startAgentWorker(t, id, w)

	data := sendTo(t, id, "Check later", "--at", "+10m").Data(t)
	assert.Equal(t, id, data["id"])
	assert.NotEmpty(t, data["input_id"])
	assert.NotEmpty(t, data["scheduled_at"])
	_, hasRequestID := data["request_id"]
	assert.False(t, hasRequestID, "relative --at cannot be idempotently retried with an auto-generated request_id")
	got := w.recorded()
	require.Len(t, got, 1)
	req := got[0].(*leapmuxv1.EnqueueAgentInputRequest)
	assert.True(t, req.GetRequireFutureSchedule())
	assert.NotEmpty(t, req.GetScheduledAt())
}

func TestAgentSendOverIPC_SendsAttachmentsWithoutText(t *testing.T) {
	id := fullTestID("ag-2")
	w := newQueueWorker()
	startAgentWorker(t, id, w)
	png := writeFile(t, t.TempDir(), "shot.png", []byte{0x89, 'P', 'N', 'G'})

	data := sendTo(t, id, "--attach", png).Data(t)
	requireRequests(t, w, &leapmuxv1.EnqueueAgentInputRequest{
		AgentId: id, InputId: data["input_id"].(string), Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		Attachments: []*leapmuxv1.Attachment{{Filename: "shot.png", Data: []byte{0x89, 'P', 'N', 'G'}}},
	})
}

// --file gives the text of a message with attachments, as the argument does.
func TestAgentSendOverIPC_SendsTheTextOfAFileWithTheAttachments(t *testing.T) {
	id := fullTestID("ag-2")
	w := newQueueWorker()
	startAgentWorker(t, id, w)
	dir := t.TempDir()
	prompt := writeFile(t, dir, "prompt.md", []byte("Explain this screenshot"))
	png := writeFile(t, dir, "shot.png", []byte{0x89, 'P', 'N', 'G'})

	data := sendTo(t, id, "--file", prompt, "--attach", png).Data(t)
	requireRequests(t, w, &leapmuxv1.EnqueueAgentInputRequest{
		AgentId: id, InputId: data["input_id"].(string), Text: "Explain this screenshot",
		Kind:        leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		Attachments: []*leapmuxv1.Attachment{{Filename: "shot.png", Data: []byte{0x89, 'P', 'N', 'G'}}},
	})
}

// A message holds as many attachments as the limit states, and no more (see
// TestAgentSend_RefusesAMessageThatTheCLICanJudge).
func TestAgentSendOverIPC_SendsTheMostAttachmentsThatAMessageHolds(t *testing.T) {
	id := fullTestID("ag-2")
	w := newQueueWorker()
	startAgentWorker(t, id, w)
	png := writeFile(t, t.TempDir(), "shot.png", []byte{0x89, 'P', 'N', 'G'})
	args := []string{"Compare these"}
	for range contracts.MaxAgentInputAttachmentsPerItem {
		args = append(args, "--attach", png)
	}

	sendTo(t, id, args...).Data(t)
	got := w.recorded()
	require.Len(t, got, 1)
	assert.Len(t, got[0].(*leapmuxv1.EnqueueAgentInputRequest).GetAttachments(), contracts.MaxAgentInputAttachmentsPerItem)
}

// The text and the attachments of one message share one limit: a message
// whose text and attachments fill it exactly is sent, and one more byte of
// text refuses the attachment.
func TestAgentSendOverIPC_TheTextAndTheAttachmentsShareOneLimit(t *testing.T) {
	id := fullTestID("ag-2")
	w := newQueueWorker()
	startAgentWorker(t, id, w)
	attachment := []byte{0x89, 'P', 'N', 'G'}
	png := writeFile(t, t.TempDir(), "shot.png", attachment)
	fits := strings.Repeat("x", contracts.MaxAgentInputItemBytes-len(attachment))

	sendTo(t, id, fits, "--attach", png).Data(t)
	got := w.recorded()
	require.Len(t, got, 1)
	sent := got[0].(*leapmuxv1.EnqueueAgentInputRequest)
	assert.Len(t, sent.GetText(), len(fits))
	assert.Equal(t, attachment, sent.GetAttachments()[0].GetData())

	f := sendTo(t, id, fits+"x", "--attach", png).requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "--attach "+png+" makes the message too large")
	assert.Len(t, w.recorded(), 1, "the refused message reached no Worker")
}

// A retry with --at as an RFC 3339 time states the same time as the first
// run, so the retry repeats the first request exactly.
func TestAgentSendOverIPC_ARetryWithTheRequestIDKeepsAnRFC3339Time(t *testing.T) {
	id := fullTestID("ag-2")
	w := newQueueWorker()
	startAgentWorker(t, id, w)
	args := []string{"Check the deploy", "--at", "2099-01-02T12:00:00+09:00", "--request-id", "deploy-1"}

	first := sendTo(t, id, args...).Data(t)
	assert.Equal(t, map[string]any{"id": id, "input_id": requestScopedID("deploy-1", requestScopeAgentSend, id), "request_id": "deploy-1", "scheduled_at": "2099-01-02T03:00:00.000Z"}, first)
	assert.Equal(t, first, sendTo(t, id, args...).Data(t))
	got := w.recorded()
	require.Len(t, got, 2)
	assert.True(t, proto.Equal(got[0], got[1]), "the retry repeats the first request:\nfirst %v\nretry %v", got[0], got[1])
	assert.Len(t, w.itemIDs(), 1, "the queue holds one message")
}

func TestAgentSendOverIPC_ReplaysAnInputAfterItsDueTime(t *testing.T) {
	agentID := fullTestID("ag-2")
	inputID := requestScopedID("deploy-1", requestScopeAgentSend, agentID)
	worker := newQueueWorker()
	worker.deliver(inputID)
	startAgentWorker(t, agentID, worker)

	result := sendTo(t, agentID, "Check the deploy", "--at", "2001-01-02T12:00:00+09:00", "--request-id", "deploy-1").Data(t)
	assert.Equal(t, inputID, result["input_id"])
	assert.Equal(t, "2001-01-02T03:00:00.000Z", result["scheduled_at"])
	requests := worker.recorded()
	require.Len(t, requests, 1)
	request := requests[0].(*leapmuxv1.EnqueueAgentInputRequest)
	assert.Equal(t, inputID, request.GetInputId())
	assert.Equal(t, "2001-01-02T03:00:00.000Z", request.GetScheduledAt())
	assert.True(t, request.GetRequireFutureSchedule())
	assert.Empty(t, worker.itemIDs(), "the replay does not queue an already delivered input")
}

// The Worker refuses a file that the agent's provider cannot read, and the
// command reports the refusal as invalid_argument with the file's name.
func TestAgentSendOverIPC_ReportsTheProvidersRefusalOfAFile(t *testing.T) {
	id := fullTestID("ag-2")
	w := newQueueWorker()
	w.refuse(codes.InvalidArgument, "invalid queued agent input: claude code does not support binary attachments: build.tar")
	startAgentWorker(t, id, w)
	tarball := writeFile(t, t.TempDir(), "build.tar", []byte{0xff, 0x00})

	o := sendTo(t, id, "Unpack this", "--attach", tarball)
	f := o.requireFailure(t, cli.CodeInvalidArgument)
	assert.Contains(t, f.Message, "build.tar")
	assert.Equal(t, cli.ExitFailure, o.Code)
	assert.Empty(t, w.itemIDs())
}

// A retry of `agent send --request-id` carries the input id of the first run,
// so the Worker keeps one message, whether the first run's message still
// waits in the queue or already reached the agent. Another message under the
// same request id is a conflict that states the cause.
func TestAgentSendOverIPC_ARetryWithTheRequestIDSendsOnce(t *testing.T) {
	id := fullTestID("ag-2")
	w := newQueueWorker()
	startAgentWorker(t, id, w)
	wantID := requestScopedID("release-1", requestScopeAgentSend, id)

	first := sendTo(t, id, "Run the release checklist", "--request-id", "release-1").Data(t)
	assert.Equal(t, map[string]any{"id": id, "input_id": wantID, "request_id": "release-1"}, first)
	again := sendTo(t, id, "Run the release checklist", "--request-id", "release-1").Data(t)
	assert.Equal(t, first, again, "the retry prints the result of the first run")
	assert.Equal(t, []string{wantID}, w.itemIDs(), "the queue holds one message")

	w.deliver(wantID)
	sendTo(t, id, "Run the release checklist", "--request-id", "release-1").Data(t)
	assert.Empty(t, w.itemIDs(), "a retry after the delivery queues nothing")
	for _, request := range w.recorded() {
		assert.Equal(t, wantID, request.(*leapmuxv1.EnqueueAgentInputRequest).GetInputId())
	}

	other := sendTo(t, id, "Run the release checklist", "--request-id", "release-2").Data(t)
	assert.NotEqual(t, wantID, other["input_id"], "another request id is another message")

	plain := sendTo(t, id, "Run the release checklist").Data(t)
	assert.NotEqual(t, sendTo(t, id, "Run the release checklist").Data(t)["input_id"], plain["input_id"], "without a request id each run is a new message")

	w.refuse(codes.FailedPrecondition, "queued agent input conflicts with existing data")
	f := sendTo(t, id, "Run the linter", "--request-id", "release-1").requireFailure(t, cli.CodeConflict)
	require.NotEmpty(t, f.Hints)
	assert.Contains(t, f.Hints[0], `--request-id "release-1"`)
}

// A retry of `agent send --request-id` after someone removed the first run's
// message from the queue queues nothing, because the Worker keeps the id of a
// removed input. Without --wait the retry succeeds and prints the result of
// the first run: the Worker took the request once, as the first run said, and
// the removal that followed stands.
func TestAgentSendOverIPC_ARetryAfterARemovalPrintsTheFirstResult(t *testing.T) {
	id := fullTestID("ag-2")
	w := newQueueWorker()
	startAgentWorker(t, id, w)
	wantID := requestScopedID("release-1", requestScopeAgentSend, id)

	first := sendTo(t, id, "Run the release checklist", "--request-id", "release-1").Data(t)
	require.Equal(t, wantID, first["input_id"])
	runControl(t, "agent", "queue", "remove", id, wantID).Data(t)

	again := sendTo(t, id, "Run the release checklist", "--request-id", "release-1")
	assert.Equal(t, cli.ExitOK, again.Code)
	assert.Equal(t, first, again.Data(t), "the retry prints the result of the first run")
	var enqueued []string
	for _, request := range w.recorded() {
		if in, ok := request.(*leapmuxv1.EnqueueAgentInputRequest); ok {
			enqueued = append(enqueued, in.GetInputId())
		}
	}
	assert.Equal(t, []string{wantID, wantID}, enqueued, "each run sends the input id of the first run")
}

func TestAgentSend_RefusesAMessageThatTheCLICanJudge(t *testing.T) {
	clearRemoteEnv(t)
	dir := t.TempDir()
	png := writeFile(t, dir, "shot.png", []byte{0x89, 'P', 'N', 'G'})
	tooMany := make([]string, 0, 2*(contracts.MaxAgentInputAttachmentsPerItem+1))
	for range contracts.MaxAgentInputAttachmentsPerItem + 1 {
		tooMany = append(tooMany, "--attach", png)
	}
	for name, tc := range map[string]struct {
		args    []string
		message string
	}{
		"an explicit empty text with a file":      {[]string{"", "--attach", png}, "the text is empty"},
		"neither text nor a file":                 {nil, "missing <text>"},
		"a missing file":                          {[]string{"hi", "--attach", filepath.Join(dir, "missing.png")}, "missing.png"},
		"too many files":                          {append([]string{"hi"}, tooMany...), "at most 100 attachments"},
		"a text over the limit":                   {[]string{strings.Repeat("x", contracts.MaxAgentInputItemBytes+1)}, "one message holds at most"},
		"a time in the past":                      {[]string{"hi", "--at", "2001-01-01T00:00:00Z"}, "is not after now"},
		"a double-plus duration":                  {[]string{"hi", "--at", "++10m"}, "is not a duration"},
		"a time without an offset":                {[]string{"hi", "--at", "2099-01-01T00:00:00"}, "neither an RFC 3339 time"},
		"a relative time with a request id":       {[]string{"hi", "--at", "+10m", "--request-id", "r1"}, "--request-id needs --at as an RFC 3339 time"},
		"an empty request id with a scheduled at": {[]string{"hi", "--at", "2099-01-01T00:00:00Z", "--request-id", ""}, "--request-id is empty"},
	} {
		t.Run(name, func(t *testing.T) {
			args := append([]string{"agent", "send", fullTestID("ag-1")}, tc.args...)
			f := runControl(t, append(args, "--hub", "https://stub.invalid")...).requireFailure(t, cli.CodeUsage)
			assert.Contains(t, f.Message, tc.message)
		})
	}
}

func TestAgentSendOverIPC_PrintsAlignedKeyValueLines(t *testing.T) {
	id := fullTestID("ag-text")
	w := newQueueWorker()
	startAgentWorker(t, id, w)

	wantInputID := requestScopedID("req-text-1", requestScopeAgentSend, id)
	out := runControlText(t, "agent", "send", id, "Ship the release", "--request-id", "req-text-1")
	require.Equal(t, cli.ExitOK, out.Code, "stderr: %s", out.Stderr)
	assert.Equal(t, "id:         "+id+"\ninput_id:   "+wantInputID+"\nrequest_id: req-text-1\n", out.Stdout)

	wantScheduledID := requestScopedID("req-text-2", requestScopeAgentSend, id)
	scheduled := runControlText(t, "agent", "send", id, "Check metrics", "--request-id", "req-text-2", "--at", "2099-01-02T03:04:05Z")
	require.Equal(t, cli.ExitOK, scheduled.Code, "stderr: %s", scheduled.Stderr)
	assert.Equal(t, "id:           "+id+"\ninput_id:     "+wantScheduledID+"\nrequest_id:   req-text-2\nscheduled_at: 2099-01-02T03:04:05.000Z\n", scheduled.Stdout)
}
