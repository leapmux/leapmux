package amp

import (
	"encoding/json"
	"fmt"
	"os"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func TestIsSubagentTool(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		contracts.AmpSubagentToolTask, contracts.AmpSubagentToolOracle,
		contracts.AmpSubagentToolLibrarian, contracts.AmpSubagentToolFinder,
	} {
		assert.Truef(t, isSubagentTool(name), "%s runs as a subagent", name)
	}
	for _, name := range []string{"shell_command", "edit_file", "task", "Oracle", ""} {
		assert.Falsef(t, isSubagentTool(name), "%q is not a subagent tool; the names are case-sensitive", name)
	}
}

func TestSubagentRow(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name            string
		tool            string
		input           string
		wantTitle       string
		wantDescription string
	}{
		{
			name:            "task with a description",
			tool:            contracts.AmpSubagentToolTask,
			input:           `{"description":"Count Go files","prompt":"Count the Go files under backend."}`,
			wantTitle:       "Count Go files",
			wantDescription: "Count the Go files under backend.",
		},
		{
			name:            "task with no description takes the prompt's first line",
			tool:            contracts.AmpSubagentToolTask,
			input:           `{"description":"  ","prompt":"\n  First line\nSecond line"}`,
			wantTitle:       "First line",
			wantDescription: "\n  First line\nSecond line",
		},
		{
			name:            "oracle",
			tool:            contracts.AmpSubagentToolOracle,
			input:           `{"task":"Review the lock order","context":"The deadlock shows in CI."}`,
			wantTitle:       "Oracle: Review the lock order",
			wantDescription: "Review the lock order\n\nThe deadlock shows in CI.",
		},
		{
			name:            "librarian",
			tool:            contracts.AmpSubagentToolLibrarian,
			input:           `{"query":"How does quartz trap timers?"}`,
			wantTitle:       "Librarian: How does quartz trap timers?",
			wantDescription: "How does quartz trap timers?",
		},
		{
			name:            "finder",
			tool:            contracts.AmpSubagentToolFinder,
			input:           `{"query":"where the bridge closes"}`,
			wantTitle:       "Finder: where the bridge closes",
			wantDescription: "where the bridge closes",
		},
		{
			name:      "a specialist with no request keeps its name",
			tool:      contracts.AmpSubagentToolOracle,
			input:     `{}`,
			wantTitle: "Oracle",
		},
		{
			name:      "input that is not an object",
			tool:      contracts.AmpSubagentToolFinder,
			input:     `"text"`,
			wantTitle: "Finder",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			title, description := subagentRow(tc.tool, json.RawMessage(tc.input))
			assert.Equal(t, tc.wantTitle, title)
			assert.Equal(t, tc.wantDescription, description)
		})
	}
}

// A subagent call opens a registry row when its tool_use arrives, and its
// tool_result closes the row. No child transcript exists: Amp prints none.
func TestSubagentCallOpensAndClosesARegistryRow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		isError bool
		want    bgtask.Status
	}{
		{name: "completed", want: bgtask.StatusCompleted},
		{name: "failed", isError: true, want: bgtask.StatusFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			fp := h.send("delegate")
			h.feed(fp, assistantLine("["+toolUseBlock("TU-task", contracts.AmpSubagentToolTask,
				`{"description":"Count files","prompt":"Count the files."}`)+"]", "tool_use"))

			tasks := h.sink.BackgroundTasks()
			require.Len(t, tasks, 1)
			assert.Equal(t, "TU-task", tasks[0].RowKey)
			assert.Equal(t, bgtask.KindSubagent, tasks[0].Kind)
			assert.Equal(t, "Count files", tasks[0].Title)
			assert.Equal(t, "Count the files.", tasks[0].Description)
			assert.Equal(t, bgtask.StatusRunning, tasks[0].Status)
			assert.Empty(t, tasks[0].ChildAgentID, "Amp prints no child transcript, so no child agent exists")

			h.feed(fp, toolResultLine("TU-task", "42 files", tc.isError))
			tasks = h.sink.BackgroundTasks()
			require.Len(t, tasks, 1)
			assert.Equal(t, tc.want, tasks[0].Status)
			assert.False(t, tasks[0].EndedAt.IsZero())
			assert.Empty(t, h.sink.ChildAgentIDs())
		})
	}
}

func TestOrdinaryToolCallOpensNoRegistryRow(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("run")
	h.feed(fp, assistantLine("["+toolUseBlock("TU-shell", "shell_command", `{"command":"ls"}`)+"]", "tool_use"))
	h.feed(fp, toolResultLine("TU-shell", "a\nb", false))
	assert.Empty(t, h.sink.BackgroundTasks())
}

// startBackgroundCommand runs one turn whose `shell_command` call Amp moved to
// the background under pid, and returns the process.
func startBackgroundCommand(h *harness, command string, pid int) *fakeProc {
	h.t.Helper()
	fp := h.send("serve")
	h.feed(fp, assistantLine("["+toolUseBlock("TU-dev", contracts.AmpShellToolShellCommand, `{"command":`+jsonString(command)+`,"workdir":"/work"}`)+"]", "tool_use"))
	h.feed(fp, toolResultLine("TU-dev", fmt.Sprintf(`{"output":"listening\n","running":true,"pid":%d}`, pid), false))
	return fp
}

// A command that outlives its call opens a shell row, keyed by the call.
func TestBackgroundCommandOpensAShellRow(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	startBackgroundCommand(h, "npm run dev", 4242)

	row, ok := h.sink.BackgroundTask(shellRowKey("TU-dev"))
	require.True(t, ok)
	assert.Equal(t, bgtask.KindShell, row.Kind)
	assert.Equal(t, "npm run dev", row.Title)
	assert.True(t, row.TitleIsCommand)
	assert.Equal(t, "Process 4242, in the background", row.Description)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Empty(t, row.ChildAgentID)
}

// A background command outlives its turn, so the turn end leaves its row open.
func TestBackgroundCommandRowOutlivesTheTurn(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := startBackgroundCommand(h, "npm run dev", 4242)
	h.feed(fp, textLine("The server runs.", stopReasonEndTurn))

	require.Len(t, h.turnEnds(), 1)
	row, ok := h.sink.BackgroundTask(shellRowKey("TU-dev"))
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
}

// A status or a kill that states the end of the command closes its row with
// the matching status. A status of a command that still runs changes nothing.
func TestBackgroundCommandRowClosesAtTheEndThatACallStates(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		tool   string
		result string
		want   bgtask.Status
	}{
		{"status of a success", contracts.AmpShellToolShellCommandStatus, `{"output":"done\n","exitCode":0,"running":false,"pid":4242}`, bgtask.StatusCompleted},
		{"status of a failure", contracts.AmpShellToolShellCommandStatus, `{"output":"boom\n","exitCode":2,"running":false,"pid":4242}`, bgtask.StatusFailed},
		{"status of a signal", contracts.AmpShellToolShellCommandStatus, `{"output":"Command terminated by signal SIGTERM (no exit code)\n","exitCode":-1,"running":false,"pid":4242}`, bgtask.StatusFailed},
		{"status of a PID that Amp forgot", contracts.AmpShellToolShellCommandStatus, `{"output":"No tracked shell command for PID 4242","exitCode":1,"running":false,"pid":4242}`, bgtask.StatusFailed},
		{"status of an end with no exit code", contracts.AmpShellToolShellCommandStatus, `{"output":"","running":false,"pid":4242}`, bgtask.StatusFailed},
		{"kill", contracts.AmpShellToolShellCommandKill, `{"output":"","exitCode":143,"running":false,"pid":4242}`, bgtask.StatusStopped},
		{"status of a command that runs", contracts.AmpShellToolShellCommandStatus, `{"output":"more\n","running":true,"pid":4242}`, bgtask.StatusRunning},
		{"kill that the command outlives", contracts.AmpShellToolShellCommandKill, `{"output":"","running":true,"pid":4242}`, bgtask.StatusRunning},
		{"status of another PID", contracts.AmpShellToolShellCommandStatus, `{"output":"","exitCode":0,"running":false,"pid":7}`, bgtask.StatusRunning},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			fp := startBackgroundCommand(h, "npm test", 4242)
			h.feed(fp, assistantLine("["+toolUseBlock("TU-next", tc.tool, `{"pid":4242}`)+"]", "tool_use"))
			h.feed(fp, toolResultLine("TU-next", tc.result, false))

			row, ok := h.sink.BackgroundTask(shellRowKey("TU-dev"))
			require.True(t, ok)
			assert.Equal(t, tc.want, row.Status)
			assert.Len(t, h.sink.BackgroundTasks(), 1, "a status or a kill opens no row of its own")
		})
	}
}

// A shell call that ended within its call, or whose result is not Amp's
// record, opens no row.
func TestShellCallThatEndedOpensNoShellRow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		result  string
		isError bool
	}{
		{"ended in its call", `{"output":"a\n","exitCode":0}`, false},
		{"refused by a rule", "Tool rejected by plugin: Matches built-in permissions rule 75: ask shell_command", false},
		{"refused by the reader", "Plugin error: not now\n", true},
		{"running with no PID", `{"output":"","running":true}`, false},
		{"running with a PID of zero", `{"output":"","running":true,"pid":0}`, false},
		{"running with a negative PID", `{"output":"","running":true,"pid":-1}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			fp := h.send("run")
			h.feed(fp, assistantLine("["+toolUseBlock("TU-sh", contracts.AmpShellToolShellCommand, `{"command":"ls"}`)+"]", "tool_use"))
			h.feed(fp, toolResultLine("TU-sh", tc.result, tc.isError))
			assert.Empty(t, h.sink.BackgroundTasks())
		})
	}
}

// Another tool whose result happens to look like the record opens no row.
func TestOnlyTheShellToolsFollowABackgroundCommand(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := h.send("run")
	h.feed(fp, assistantLine("["+toolUseBlock("TU-x", "code_exec", `{"code":"1"}`)+"]", "tool_use"))
	h.feed(fp, toolResultLine("TU-x", `{"output":"","running":true,"pid":4242}`, false))
	assert.Empty(t, h.sink.BackgroundTasks())
}

// A call that states no command titles its row with the PID, as prose.
func TestBackgroundCommandWithNoCommandTitlesItsRowWithThePID(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	startBackgroundCommand(h, "  ", 4242)
	row, ok := h.sink.BackgroundTask(shellRowKey("TU-dev"))
	require.True(t, ok)
	assert.Equal(t, "Process 4242", row.Title)
	assert.False(t, row.TitleIsCommand)
}

// A later command that takes the PID of an earlier one closes the earlier
// row, which Amp never stated the end of.
func TestReusedPIDClosesTheEarlierRow(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := startBackgroundCommand(h, "npm run dev", 4242)
	h.feed(fp, assistantLine("["+toolUseBlock("TU-again", contracts.AmpShellToolShellCommand, `{"command":"npm run watch"}`)+"]", "tool_use"))
	h.feed(fp, toolResultLine("TU-again", `{"output":"","running":true,"pid":4242}`, false))

	earlier, ok := h.sink.BackgroundTask(shellRowKey("TU-dev"))
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusStopped, earlier.Status)
	later, ok := h.sink.BackgroundTask(shellRowKey("TU-again"))
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, later.Status)

	// The PID now belongs to the later command alone.
	h.feed(fp, assistantLine("["+toolUseBlock("TU-st", contracts.AmpShellToolShellCommandStatus, `{"pid":4242}`)+"]", "tool_use"))
	h.feed(fp, toolResultLine("TU-st", `{"output":"","exitCode":0,"running":false,"pid":4242}`, false))
	later, _ = h.sink.BackgroundTask(shellRowKey("TU-again"))
	assert.Equal(t, bgtask.StatusCompleted, later.Status)
	earlier, _ = h.sink.BackgroundTask(shellRowKey("TU-dev"))
	assert.Equal(t, bgtask.StatusStopped, earlier.Status)
}

// Amp stops every command that it started when it shuts down in order, and it
// prints its `result` on that path. So an exit after the result closes each open
// row as stopped.
func TestProcessExitClosesEveryShellRow(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fp := startBackgroundCommand(h, "npm run dev", 4242)
	h.feed(fp, textLine("Running.", stopReasonEndTurn))
	h.feed(fp, `{"type":"result","subtype":"success","is_error":false,"num_turns":1,"result":"Running.","session_id":"T-1"}`)
	fp.exit()
	fp.awaitHandled(t)

	row, ok := h.sink.BackgroundTask(shellRowKey("TU-dev"))
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusStopped, row.Status)
	assert.Equal(t, []bgtask.Status{bgtask.StatusRunning, bgtask.StatusStopped}, h.sink.BackgroundTaskStatuses(shellRowKey("TU-dev")))
}

// A crash skips Amp's shutdown, so a background command can outlive Amp. The
// row then closes as interrupted: LeapMux lost track of the command, and it
// does not claim that the command stopped.
func TestProcessCrashClosesEveryShellRowAsInterrupted(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		lines []string
	}{
		{"between turns", []string{textLine("Running.", stopReasonEndTurn)}},
		{"in a turn", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			fp := startBackgroundCommand(h, "npm run dev", 4242)
			for _, line := range tc.lines {
				h.feed(fp, line)
			}
			fp.exit()
			fp.awaitHandled(t)

			row, ok := h.sink.BackgroundTask(shellRowKey("TU-dev"))
			require.True(t, ok)
			assert.Equal(t, bgtask.StatusInterrupted, row.Status)
		})
	}
}

// Stop closes the rows at once, and the exit handler that follows closes none
// again.
func TestStopClosesEveryShellRowOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	// The sink keeps the first close of a row, so only a count of the calls
	// shows a second close.
	var closes atomic.Int32
	h.sink.OnCloseBackgroundTask = func(string, bgtask.Status) { closes.Add(1) }
	fp := startBackgroundCommand(h, "npm run dev", 4242)
	h.agent.Stop()
	fp.awaitHandled(t)

	row, ok := h.sink.BackgroundTask(shellRowKey("TU-dev"))
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusStopped, row.Status)
	assert.Equal(t, int32(1), closes.Load())
}

func TestShellEndStatus(t *testing.T) {
	t.Parallel()
	code := func(value int) *int { return &value }
	assert.Equal(t, bgtask.StatusCompleted, shellEndStatus(contracts.AmpShellResult{ExitCode: code(0)}))
	assert.Equal(t, bgtask.StatusFailed, shellEndStatus(contracts.AmpShellResult{ExitCode: code(1)}))
	assert.Equal(t, bgtask.StatusFailed, shellEndStatus(contracts.AmpShellResult{ExitCode: code(-1)}), "a signal states no exit code of its own")
	assert.Equal(t, bgtask.StatusFailed, shellEndStatus(contracts.AmpShellResult{}), "an end with no exit code is not a success")
}

func TestToolResultText(t *testing.T) {
	t.Parallel()
	assert.Equal(t, `{"running":true}`, toolResultText(json.RawMessage(`"{\"running\":true}"`)))
	assert.Equal(t, "a\nb", toolResultText(json.RawMessage(`[{"type":"text","text":"a"},{"type":"image"},{"type":"text","text":"b"}]`)))
	assert.Empty(t, toolResultText(nil))
	assert.Empty(t, toolResultText(json.RawMessage(`{"output":"x"}`)))
}

// ampSubagentTitleFixture mirrors testdata/amp_subagent_title_conformance.json.
type ampSubagentTitleFixture struct {
	Cases []struct {
		Tool  string          `json:"tool"`
		Input json.RawMessage `json:"input"`
		Title string          `json:"title"`
		Why   string          `json:"why"`
	} `json:"cases"`
}

// The worker half of testdata/amp_subagent_title_conformance.json. The browser
// suite replays the same file against ampAgentRequest, so the registry row and
// the transcript card of one call show one title.
func TestSubagentTitleConformance(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(testutil.RepoPath(t, "testdata", "amp_subagent_title_conformance.json"))
	require.NoError(t, err)
	var fixture ampSubagentTitleFixture
	require.NoError(t, json.Unmarshal(raw, &fixture))
	// A fixture that loads no case would pass while it asserts nothing.
	require.NotEmpty(t, fixture.Cases)
	for _, tc := range fixture.Cases {
		title, _ := subagentRow(tc.Tool, tc.Input)
		assert.Equal(t, tc.Title, title, tc.Why)
	}
}
