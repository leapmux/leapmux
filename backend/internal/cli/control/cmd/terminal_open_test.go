package cmd

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
)

// localIPC starts the fake hub and Worker behind the local IPC socket, the
// transport of a command that runs inside a LeapMux tab, and returns the
// --hub value that selects it.
func localIPC(t *testing.T, hub *recordingHub, disp controlipc.LocalDispatcher) string {
	startSpawnIPC(t, hub, disp)
	return ""
}

// openTerminalOnFakeWorker runs `terminal open` with args against a fake hub and
// Worker on transport, and returns the outcome and the requests that reached
// the Worker.
func openTerminalOnFakeWorker(t *testing.T, start func(*testing.T, *recordingHub, controlipc.LocalDispatcher) string, stdin string, args ...string) (cliOutcome, *recordingHub, []recordedWorkerCall) {
	t.Helper()
	hub := &recordingHub{workers: workerRows("worker-A"), materialized: newStateBuilder().workspace("ws-1", "root-1").leafNode("root-1", "", "m").st}
	dispatcher := &creationPublicationDispatcher{}
	hubFlag := start(t, hub, dispatcher)
	base := []string{"terminal", "open", "--workspace", "ws-1", "--tile", "root-1", "--worker", "worker-A", "--dir", "/src/app", "--json"}
	base = append(base, testHubArgs(hubFlag)...)
	o := runControlIn(t, stdin, append(base, args...)...)
	return o, hub, dispatcher.recorded()
}

// requireOpenTerminalRequest asserts that calls holds exactly one
// OpenTerminal, and returns it.
func requireOpenTerminalRequest(t *testing.T, calls []recordedWorkerCall) *leapmuxv1.OpenTerminalRequest {
	t.Helper()
	require.Len(t, calls, 1, "one request reaches the Worker")
	req, ok := calls[0].in.(*leapmuxv1.OpenTerminalRequest)
	require.Truef(t, ok, "the Worker received %T", calls[0].in)
	return req
}

// `terminal open --command` sends the command line in the OpenTerminal
// request itself, so the Worker runs it in the shell that the request starts.
// The rest of the request is what an open without --command sends.
func TestTerminalOpenOverIPC_SendsTheInitialCommandInTheRequest(t *testing.T) {
	for _, transport := range tabOpenTransports {
		t.Run(transport.name, func(t *testing.T) {
			o, hub, calls := openTerminalOnFakeWorker(t, transport.start, "", "--command", "make test", "--title", "Tests")
			data := o.Data(t)
			got := requireOpenTerminalRequest(t, calls)
			assert.Equal(t, data["id"], got.GetCreation().GetTabId())
			assert.Equal(t, "ws-1", got.GetCreation().GetPlacement().GetWorkspaceId())
			assert.Equal(t, "root-1", got.GetCreation().GetPlacement().GetTileId())
			want := &leapmuxv1.OpenTerminalRequest{
				WorkingDir:     "/src/app",
				Title:          "Tests",
				InitialCommand: "make test",
				Target:         &leapmuxv1.OpenTerminalRequest_Creation{Creation: got.GetCreation()},
			}
			assert.Truef(t, proto.Equal(want, got), "the exact request:\nwant %v\ngot  %v", want, got)
			assert.NotEmpty(t, hub.called())
		})
	}
}

// An open without --command leaves the field empty, which runs nothing.
func TestTerminalOpenOverIPC_WithoutACommandSendsNone(t *testing.T) {
	o, _, calls := openTerminalOnFakeWorker(t, localIPC, "")
	o.Data(t)
	assert.Empty(t, requireOpenTerminalRequest(t, calls).GetInitialCommand())
}

func TestTerminalOpenPreservesTheLiteralWorktreePath(t *testing.T) {
	for _, transport := range tabOpenTransports {
		t.Run(transport.name, func(t *testing.T) {
			const path = "/src/worktrees/selected "
			result, _, calls := openTerminalOnFakeWorker(t, transport.start, "", "--use-worktree", path)
			_ = result.Data(t)
			assert.Equal(t, path, requireOpenTerminalRequest(t, calls).GetGitSetup().GetUseWorktree().GetPath())
		})
	}
}

func TestTerminalOpenSendsAnExactWorktreeBranch(t *testing.T) {
	for _, transport := range tabOpenTransports {
		t.Run(transport.name, func(t *testing.T) {
			result, _, calls := openTerminalOnFakeWorker(t, transport.start, "", "--use-worktree", "branch:origin/Feature")
			_ = result.Data(t)
			request := requireOpenTerminalRequest(t, calls)
			assert.Equal(t, "/src/app", request.GetWorkingDir())
			assert.Equal(t, "origin/Feature", request.GetGitSetup().GetUseWorktree().GetBranch())
			assert.Empty(t, request.GetGitSetup().GetUseWorktree().GetPath())
		})
	}
}

// --command reads stdin for "-", and it drops the line endings at the end as
// `terminal run` does, because the Worker presses Enter itself. The line
// endings inside the command stay.
func TestTerminalOpenOverIPC_ReadsTheCommandAsTerminalRunReadsIt(t *testing.T) {
	for name, tc := range map[string]struct {
		stdin string
		value string
		want  string
	}{
		"a line from stdin":        {"go test ./...\n", "-", "go test ./..."},
		"CRLF line endings":        {"dir\r\n\r\n", "-", "dir"},
		"a trailing newline":       {"", "make\n", "make"},
		"lines inside the command": {"", "cd /tmp\nls\n", "cd /tmp\nls"},
		"leading blanks stay":      {"", "  ls", "  ls"},
	} {
		t.Run(name, func(t *testing.T) {
			o, _, calls := openTerminalOnFakeWorker(t, localIPC, tc.stdin, "--command", tc.value)
			o.Data(t)
			assert.Equal(t, tc.want, requireOpenTerminalRequest(t, calls).GetInitialCommand())
		})
	}
}

// A --command that holds nothing to run fails before the command sends
// anything: the hub and the Worker see nothing.
func TestTerminalOpenOverIPC_RefusesAnEmptyCommand(t *testing.T) {
	for name, tc := range map[string]struct{ stdin, value string }{
		"an empty value":      {"", ""},
		"blanks":              {"", " \t "},
		"line endings only":   {"", "\r\n\n"},
		"an empty stdin":      {"", "-"},
		"a stdin of newlines": {"\n\n", "-"},
	} {
		t.Run(name, func(t *testing.T) {
			o, hub, calls := openTerminalOnFakeWorker(t, localIPC, tc.stdin, "--command", tc.value)
			f := o.requireFailure(t, cli.CodeUsage)
			assert.Equal(t, "--command is empty", f.Message)
			assert.Empty(t, hub.called(), "a refused open calls no hub")
			assert.Empty(t, calls, "a refused open reaches no Worker")
		})
	}
}

func TestTerminalOpenRequest_CarriesTheInitialCommand(t *testing.T) {
	creation := &leapmuxv1.TabCreation{TabId: "terminal-creation"}
	req := terminalOpenRequest(openTerminalArgs{WorkerID: "w-1", WorkingDir: "/src", InitialCommand: "make test"}, creation)
	assert.Equal(t, "make test", req.GetInitialCommand())
	assert.Equal(t, "/src", req.GetWorkingDir())
	assert.Same(t, creation, req.GetCreation())
}

// The help of `terminal open` states the contract of --command: when the
// shell runs it, that a restart does not, and how to read it from stdin.
func TestTerminalOpenLong(t *testing.T) {
	flat := strings.Join(strings.Fields(terminalOpenLong), " ")
	for _, fact := range []string{
		"in the original interactive shell after its startup files run",
		"if the shell cannot acknowledge the handoff",
		"recovery reports an unverified outcome without running it again",
		"A restart of the terminal does not run it again",
		`"-" to read it from stdin`,
	} {
		assert.Contains(t, flat, fact)
	}
	for _, line := range strings.Split(terminalOpenLong, "\n") {
		assert.LessOrEqualf(t, len(line), helpWidth, "line %q", line)
	}
}
