package cmd

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// requireOneSetTabStatus returns the one SetTabStatus request that the fake
// Worker received.
func requireOneSetTabStatus(t *testing.T, w *privateCommandWorker) *leapmuxv1.SetTabStatusRequest {
	t.Helper()
	calls := w.recorded()
	require.Len(t, calls, 1)
	require.Equal(t, leapmuxv1connect.WorkerPrivateServiceSetTabStatusProcedure, calls[0].method)
	return calls[0].request.(*leapmuxv1.SetTabStatusRequest)
}

func uint32Ptr(n uint32) *uint32 { return &n }

func TestTabStatusSetOverIPC_SendsTheStatus(t *testing.T) {
	w := &privateCommandWorker{}
	tab := agentTab()
	startTabWorker(t, w, tab)
	id := tab.GetTab().GetTabId()

	data := runControl(t, "tab", "status", "set", id, "Migrating rows", "--progress", "40", "--tone", "warning").Data(t)
	assert.Equal(t, map[string]any{
		"id":     id,
		"key":    "default",
		"status": map[string]any{"text": "Migrating rows", "progress_percent": float64(40), "tone": "warning"},
	}, data)

	want := &leapmuxv1.SetTabStatusRequest{TabId: id, Status: &leapmuxv1.TabStatus{
		Text:            "Migrating rows",
		ProgressPercent: uint32Ptr(40),
		Tone:            leapmuxv1.TabStatusTone_TAB_STATUS_TONE_WARNING,
	}}
	got := requireOneSetTabStatus(t, w)
	assert.True(t, proto.Equal(want, got), "request: %v", got)
}

// Without --progress the request leaves the progress UNSET, which is what
// tells the frontend to draw no ring; 0 would draw an empty one. The tone
// defaults to neutral, which the proto states as unspecified.
func TestTabStatusSetOverIPC_LeavesTheProgressUnsetWithoutTheFlag(t *testing.T) {
	w := &privateCommandWorker{}
	startTabWorker(t, w, terminalTab())

	data := runControl(t, "tab", "status", "set", "term-2", "Waiting for input").Data(t)
	assert.Equal(t, map[string]any{"text": "Waiting for input", "tone": "neutral"}, data["status"])

	got := requireOneSetTabStatus(t, w)
	assert.Nil(t, got.GetStatus().ProgressPercent, "no progress was given")
	assert.Equal(t, leapmuxv1.TabStatusTone_TAB_STATUS_TONE_UNSPECIFIED, got.GetStatus().GetTone())
}

// 0 and 100 are the two ends of the range, and both are sent as set values.
// The result prints a progress of 0 too, because 0 draws an empty ring, and
// only an absent progress draws none.
func TestTabStatusSetOverIPC_SendsBothEndsOfTheProgressRange(t *testing.T) {
	for _, progress := range []uint32{0, 100} {
		t.Run(strconv.Itoa(int(progress)), func(t *testing.T) {
			w := &privateCommandWorker{}
			startTabWorker(t, w, agentTab())

			data := runControl(t, "tab", "status", "set", "agent-2", "Working", "--progress", strconv.Itoa(int(progress))).Data(t)
			got := requireOneSetTabStatus(t, w).GetStatus().ProgressPercent
			require.NotNil(t, got)
			assert.Equal(t, progress, *got)
			assert.Equal(t, map[string]any{"text": "Working", "progress_percent": float64(progress), "tone": "neutral"}, data["status"])
		})
	}
}

// "-" reads the text from stdin. The Worker owns the trim, so the CLI sends
// the line ending that the pipe left.
func TestTabStatusSetOverIPC_ReadsTheTextFromStdin(t *testing.T) {
	w := &privateCommandWorker{}
	startTabWorker(t, w, agentTab())

	runControlIn(t, "ok 412 tests\n", "tab", "status", "set", "agent-2", "-", "--tone", "success", "--json").Data(t)
	got := requireOneSetTabStatus(t, w).GetStatus()
	assert.Equal(t, "ok 412 tests\n", got.GetText())
	assert.Equal(t, leapmuxv1.TabStatusTone_TAB_STATUS_TONE_SUCCESS, got.GetTone())
}

// The result is the status that the Worker stored, not the command line.
func TestTabStatusSetOverIPC_PrintsTheStatusThatTheWorkerStored(t *testing.T) {
	w := &privateCommandWorker{answers: map[string]proto.Message{
		leapmuxv1connect.WorkerPrivateServiceSetTabStatusProcedure: &leapmuxv1.SetTabStatusResponse{Status: &leapmuxv1.TabStatus{
			Text: "Stored text",
			Tone: leapmuxv1.TabStatusTone_TAB_STATUS_TONE_ERROR,
		}},
	}}
	startTabWorker(t, w, agentTab())

	data := runControl(t, "tab", "status", "set", "agent-2", " Sent text\n", "--progress", "10").Data(t)
	assert.Equal(t, map[string]any{"text": "Stored text", "tone": "error"}, data["status"],
		"a progress that the Worker did not store is not printed")
}

func TestTabStatusClearOverIPC_SendsNoStatus(t *testing.T) {
	w := &privateCommandWorker{}
	startTabWorker(t, w, agentTab())

	o := runControl(t, "tab", "status", "clear", "agent-2")
	env := o.envelope(t)
	require.Nil(t, env.Error)
	assert.Equal(t, "agent-2", env.Data["id"])
	assert.Contains(t, env.Data, "status", "a clear states the status explicitly")
	assert.Nil(t, env.Data["status"], "a cleared status is null")

	got := requireOneSetTabStatus(t, w)
	assert.True(t, proto.Equal(&leapmuxv1.SetTabStatusRequest{TabId: "agent-2"}, got), "request: %v", got)
	assert.Nil(t, got.Status, "an absent status is what clears it")
}

func TestTabStatusSetOverIPC_PrintsTheStatusAsText(t *testing.T) {
	w := &privateCommandWorker{}
	startTabWorker(t, w, agentTab())

	o := runControlText(t, "tab", "status", "set", "agent-2", "Running the tests", "--progress", "75")
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
	assert.Equal(t, "id: agent-2  key: default  status: {text: \"Running the tests\", progress_percent: 75, tone: neutral}\n", o.Stdout)
}

func TestTabStatusKeysOverIPC_SetClearAndList(t *testing.T) {
	w := &privateCommandWorker{answers: map[string]proto.Message{
		leapmuxv1connect.WorkerPrivateServiceListTabStatusesProcedure: &leapmuxv1.ListTabStatusesResponse{
			Statuses: []*leapmuxv1.TabStatusEntry{
				{Key: "tests", Status: &leapmuxv1.TabStatus{Text: "Running tests", ProgressPercent: uint32Ptr(0)}},
				{Key: "build", Status: &leapmuxv1.TabStatus{Text: "Build ready", Tone: leapmuxv1.TabStatusTone_TAB_STATUS_TONE_SUCCESS}},
			},
		},
	}}
	startTabWorker(t, w, agentTab())

	assert.Equal(t, "tests", runControl(t, "tab", "status", "set", "agent-2", "Running tests", "--key", "tests").Data(t)["key"])
	data := runControl(t, "tab", "status", "list", "agent-2").Data(t)
	assert.Equal(t, map[string]any{
		"id": "agent-2",
		"statuses": []any{
			map[string]any{"key": "tests", "text": "Running tests", "progress_percent": float64(0), "tone": "neutral"},
			map[string]any{"key": "build", "text": "Build ready", "tone": "success"},
		},
	}, data)
	rendered := runControlText(t, "tab", "status", "list", "agent-2")
	require.Equal(t, cli.ExitOK, rendered.Code)
	assert.Contains(t, rendered.Stdout, "PROGRESS %")
	assert.Contains(t, rendered.Stdout, "Running tests")
	assert.Contains(t, rendered.Stdout, "Build ready")
	assert.Equal(t, "tests", runControl(t, "tab", "status", "clear", "agent-2", "--key", "tests").Data(t)["key"])

	calls := w.recorded()
	require.Len(t, calls, 4)
	assert.True(t, proto.Equal(&leapmuxv1.SetTabStatusRequest{
		TabId: "agent-2", Key: new("tests"), Status: &leapmuxv1.TabStatus{Text: "Running tests"},
	}, calls[0].request))
	assert.True(t, proto.Equal(&leapmuxv1.ListTabStatusesRequest{TabId: "agent-2"}, calls[1].request))
	assert.True(t, proto.Equal(&leapmuxv1.SetTabStatusRequest{TabId: "agent-2", Key: new("tests")}, calls[3].request))
}

// What the CLI can check, it refuses before it sends anything.
func TestTabStatusSet_RefusesAnInvalidCommandLine(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		message string
	}{
		{"an empty text", []string{"agent-2", ""}, "the text is empty"},
		{"a text of spaces", []string{"agent-2", "   "}, "the text is empty"},
		{"a negative progress", []string{"agent-2", "x", "--progress", "-1"}, "from 0 to 100; got -1"},
		{"a progress over 100", []string{"agent-2", "x", "--progress", "101"}, "from 0 to 100; got 101"},
		{"a progress that is no number", []string{"agent-2", "x", "--progress", "half"}, "progress"},
		{"an unknown tone", []string{"agent-2", "x", "--tone", "purple"}, `--tone must be one of neutral, success, warning, error; got "purple"`},
		{"no text", []string{"agent-2"}, ""},
		{"no tab", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := &privateCommandWorker{}
			startTabWorker(t, w, agentTab())

			f := runControl(t, append([]string{"tab", "status", "set"}, tc.args...)...).requireFailure(t, cli.CodeUsage)
			assert.Contains(t, f.Message, tc.message)
			assert.Empty(t, w.recorded(), "a refused command line sends nothing")
		})
	}
}

// The command changes what every window shows, so a bare clear is a usage
// error rather than a clear of the caller's own tab.
func TestTabStatusClear_RequiresATab(t *testing.T) {
	w := &privateCommandWorker{}
	startTabWorker(t, w, agentTab())

	runControl(t, "tab", "status", "clear").requireFailure(t, cli.CodeUsage)
	assert.Empty(t, w.recorded())
}

// The Worker owns the text limit and the hosted-tab check, so its refusals
// reach the caller with their own words and codes.
func TestTabStatusOverIPC_ReportsTheWorkersRefusal(t *testing.T) {
	cases := []struct {
		name    string
		refusal privateCommandRefusal
		want    cli.Code
	}{
		{"a text over the limit", privateCommandRefusal{codes.InvalidArgument, "status.text is 200 bytes long; the limit is 128 bytes"}, cli.CodeInvalidArgument},
		{"a tab that the worker does not host", privateCommandRefusal{codes.NotFound, "tab not found on this worker"}, cli.CodeNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := &privateCommandWorker{refusals: map[string]privateCommandRefusal{
				leapmuxv1connect.WorkerPrivateServiceSetTabStatusProcedure: tc.refusal,
			}}
			startTabWorker(t, w, agentTab())

			f := runControl(t, "tab", "status", "set", "agent-2", "Running").requireFailure(t, tc.want)
			assert.Contains(t, f.Message, tc.refusal.message)
		})
	}
}

// Every tone token that a result prints, --tone accepts back.
func TestTabStatusTone_TokensRoundTrip(t *testing.T) {
	assert.Equal(t, []string{"neutral", "success", "warning", "error"}, tabStatusToneTokens())
	for _, token := range tabStatusToneTokens() {
		tone, err := parseTabStatusTone(token)
		require.NoError(t, err, token)
		assert.Equal(t, token, tabStatusToneToken(tone))
	}
	tone, err := parseTabStatusTone("ERROR")
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.TabStatusTone_TAB_STATUS_TONE_ERROR, tone, "a token matches without regard to case")

	for _, bad := range []string{"", "unspecified", "danger"} {
		_, err := parseTabStatusTone(bad)
		assert.Error(t, err, "%q is no tone", bad)
	}
}

func TestTabStatusClear_SingleChannelOnAll(t *testing.T) {
	w := &privateCommandWorker{answers: map[string]proto.Message{
		leapmuxv1connect.WorkerPrivateServiceListTabStatusesProcedure: &leapmuxv1.ListTabStatusesResponse{
			Statuses: []*leapmuxv1.TabStatusEntry{
				{Key: "build", Status: &leapmuxv1.TabStatus{Text: "Running"}},
				{Key: "lint", Status: &leapmuxv1.TabStatus{Text: "OK"}},
			},
		},
	}}
	hub := &recordingHub{workers: workerRows("worker-A"), locateTab: agentTab()}
	h := startHubChannelHarness(t, hub, w)
	h.dispatcher.Handle(leapmuxv1connect.WorkerPrivateServiceListTabStatusesProcedure, channel.Registration{}, w.DispatchWith)
	h.dispatcher.Handle(leapmuxv1connect.WorkerPrivateServiceSetTabStatusProcedure, channel.Registration{}, w.DispatchWith)

	data := runControl(t, "tab", "status", "clear", "agent-2", "--all", "--hub", h.url).Data(t)
	assert.Equal(t, "agent-2", data["id"])
	assert.Equal(t, []any{"build", "lint"}, data["cleared"])
	assert.Equal(t, 1, h.channelOpens(), "ListTabStatuses and SetTabStatus clears must share one E2EE channel")
}

func TestTabStatusClear_QuietPrintsClearedKeysConsistently(t *testing.T) {
	w := &privateCommandWorker{answers: map[string]proto.Message{
		leapmuxv1connect.WorkerPrivateServiceListTabStatusesProcedure: &leapmuxv1.ListTabStatusesResponse{
			Statuses: []*leapmuxv1.TabStatusEntry{
				{Key: "build", Status: &leapmuxv1.TabStatus{Text: "Running"}},
				{Key: "lint", Status: &leapmuxv1.TabStatus{Text: "OK"}},
			},
		},
	}}
	startTabWorker(t, w, agentTab())

	// `tab status set -q` keeps printing the tab ID.
	setQuiet := runControlText(t, "tab", "status", "set", "agent-2", "Working", "-q")
	require.Equal(t, cli.ExitOK, setQuiet.Code, setQuiet.Stderr)
	assert.Equal(t, "agent-2\n", setQuiet.Stdout)

	// Every `tab status clear -q` form prints cleared status key names.
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"default key", []string{"tab", "status", "clear", "agent-2", "-q"}, "default\n"},
		{"flag key", []string{"tab", "status", "clear", "agent-2", "--key", "tests", "-q"}, "tests\n"},
		{"single positional key", []string{"tab", "status", "clear", "agent-2", "build", "-q"}, "build\n"},
		{"multiple positional keys", []string{"tab", "status", "clear", "agent-2", "build", "lint", "-q"}, "build\nlint\n"},
		{"all keys", []string{"tab", "status", "clear", "agent-2", "--all", "-q"}, "build\nlint\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := runControlText(t, tc.args...)
			require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
			assert.Equal(t, tc.want, o.Stdout)
		})
	}
}

func TestTabStatusClear_RejectsEmptyKeysBeforeRPC(t *testing.T) {
	for _, args := range [][]string{
		{"tab", "status", "clear", "agent-2", "--key", "   "},
		{"tab", "status", "clear", "agent-2", "   "},
	} {
		w := &privateCommandWorker{}
		startTabWorker(t, w, agentTab())

		runControl(t, args...).requireFailure(t, cli.CodeUsage)
		assert.Empty(t, w.recorded(), "empty status key must fail before calling the Worker")
	}
}
