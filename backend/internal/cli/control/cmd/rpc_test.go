package cmd

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/hubrpc"
)

func TestProcedurePath_ReadsEachFormOfAProcedure(t *testing.T) {
	for input, want := range map[string]string{
		"leapmux.v1.WorkspaceService/ListTabs":  "/leapmux.v1.WorkspaceService/ListTabs",
		"/leapmux.v1.WorkspaceService/ListTabs": "/leapmux.v1.WorkspaceService/ListTabs",
		"WorkspaceService/ListTabs":             "/leapmux.v1.WorkspaceService/ListTabs",
		" WorkerAgentService/ListAgents ":       "/leapmux.v1.WorkerAgentService/ListAgents",
	} {
		got, ok := procedurePath(input)
		assert.True(t, ok, "%q", input)
		assert.Equal(t, want, got, "%q", input)
	}
	for _, input := range []string{"", "ListTabs", "/", "WorkspaceService/", "/ListTabs", "a/b/c"} {
		_, ok := procedurePath(input)
		assert.False(t, ok, "%q is not a procedure", input)
	}
}

func TestLookupProcedure_ResolvesAHubAndAWorkerProcedure(t *testing.T) {
	hub, err := lookupProcedure("leapmux.v1.WorkspaceService/ListTabs")
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.ServiceHost_SERVICE_HOST_HUB, hub.host)
	assert.Equal(t, "leapmux.v1.ListTabsRequest", string(hub.method.Input().FullName()))
	assert.Equal(t, "leapmux.v1.WorkspaceService/ListTabs", hub.name())

	worker, err := lookupProcedure("WorkerAgentService/ListAgents")
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.ServiceHost_SERVICE_HOST_WORKER, worker.host)
	assert.Equal(t, "/leapmux.v1.WorkerAgentService/ListAgents", worker.path)
	assert.Equal(t, "leapmux.v1.ListAgentsResponse", string(worker.method.Output().FullName()))
}

func TestLookupProcedure_RefusesWhatItCannotCall(t *testing.T) {
	cases := []struct {
		input string
		wants string
	}{
		{"ListTabs", "is not a procedure"},
		{"leapmux.v1.WorkspaceService/ListTabz", "unknown procedure"},
		{"leapmux.v1.WorkerWatchService/WatchEvents", "streams"},
		{"leapmux.v1.WorkerTunnelService/OpenTunnelConn", "streams"},
		{"leapmux.v1.ControlIPCService/CallInner", "socket of a LeapMux tab"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			_, err := lookupProcedure(tc.input)
			e := cli.FromError(err)
			assert.Equal(t, cli.CodeUsage, e.Code)
			assert.Contains(t, e.Message, tc.wants)
		})
	}
}

func TestLookupProcedure_SuggestsTheClosestNames(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"leapmux.v1.WorkspaceService/ListTabz", "leapmux.v1.WorkspaceService/ListTabs"},
		{"workspaceservice/listtabs", "leapmux.v1.WorkspaceService/ListTabs"},
		{"WorkerAgentService/ListTabs", "leapmux.v1.WorkspaceService/ListTabs"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			_, err := lookupProcedure(tc.input)
			e := cli.FromError(err)
			require.NotEmpty(t, e.Hints)
			assert.Contains(t, e.Hints[0], "did you mean")
			assert.Contains(t, e.Hints[0], tc.want)
		})
	}
	_, err := lookupProcedure("NoSuchService/Nothing")
	e := cli.FromError(err)
	require.Len(t, e.Hints, 1, "a name close to nothing gets no suggestion")
	assert.Contains(t, e.Hints[0], "proto/leapmux/v1")
}

func TestCloseProcedures_ListsAtMostFiveNames(t *testing.T) {
	// Many services declare a method of this name, and each of them counts as
	// close.
	got := closeProcedures("/leapmux.v1.NoSuchService/ListAgents")
	assert.LessOrEqual(t, len(got), maxProcedureSuggestions)
	assert.Contains(t, got, "leapmux.v1.WorkerAgentService/ListAgents")
}

func TestEditDistance(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "abc", 0},
		{"", "abc", 3},
		{"kitten", "sitting", 3},
		{"漢字", "漢", 1},
	} {
		assert.Equal(t, tc.want, cli.LevenshteinDistance(tc.a, tc.b), "%q %q", tc.a, tc.b)
		assert.Equal(t, tc.want, cli.LevenshteinDistance(tc.b, tc.a), "the distance is symmetric")
	}
}

func TestCallableProcedures_ListsTheUnaryProceduresOfTheHubAndTheWorkers(t *testing.T) {
	names := callableProcedures()
	assert.True(t, slices.IsSorted(names))
	assert.Contains(t, names, "leapmux.v1.WorkspaceService/ListTabs")
	assert.Contains(t, names, "leapmux.v1.WorkerAgentService/ListAgents")
	assert.NotContains(t, names, "leapmux.v1.WorkerWatchService/WatchEvents", "a stream has no command line here")
	assert.NotContains(t, names, "leapmux.v1.ControlIPCService/CallInner", "the socket carries the calls")
}

func TestCompleteProcedures_CompletesTheProcedureAlone(t *testing.T) {
	got, directive := completeProcedures(nil, cli.ArgSpec{Name: "procedure"}, "leapmux.v1.WorkspaceService/List")
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
	assert.Contains(t, got, "leapmux.v1.WorkspaceService/ListTabs")
	for _, name := range got {
		assert.True(t, strings.HasPrefix(name, "leapmux.v1.WorkspaceService/List"), name)
	}
	got, _ = completeProcedures(nil, cli.ArgSpec{Name: "request"}, "")
	assert.Empty(t, got)
}

func TestRPCLong_ListsEveryProcedureThatTheSocketRelays(t *testing.T) {
	help := rpcLong()
	for path := range hubrpc.Registry {
		assert.Contains(t, help, "  - "+strings.TrimPrefix(path, "/")+"\n")
	}
}

func TestRPCResponse_PrintsProtobufJSON(t *testing.T) {
	resp, err := rpcResponseOf(&leapmuxv1.ListAvailableShellsResponse{Shells: []string{"/bin/sh", "/bin/zsh"}, DefaultShell: "/bin/zsh"})
	require.NoError(t, err)

	var text bytes.Buffer
	require.NoError(t, resp.RenderText(&text))
	assert.Equal(t, "{\n  \"shells\": [\n    \"/bin/sh\",\n    \"/bin/zsh\"\n  ],\n  \"default_shell\": \"/bin/zsh\"\n}\n", text.String(),
		"the text form is indented, with the field names of the .proto file")

	raw, err := json.Marshal(map[string]any{"data": resp})
	require.NoError(t, err)
	assert.JSONEq(t, `{"data":{"shells":["/bin/sh","/bin/zsh"],"default_shell":"/bin/zsh"}}`, string(raw))
}

func TestRPCResponse_PrintsAnEmptyResponse(t *testing.T) {
	resp, err := rpcResponseOf(&leapmuxv1.ListAvailableShellsResponse{})
	require.NoError(t, err)
	var text bytes.Buffer
	require.NoError(t, resp.RenderText(&text))
	assert.Equal(t, "{}\n", text.String(), "a field that holds its default value does not print")
}

func TestRPC_RefusesACommandLineBeforeItSendsAnything(t *testing.T) {
	clearRemoteEnv(t)
	cases := []struct {
		name  string
		args  []string
		stdin string
		wants string
	}{
		{"an unknown procedure", []string{"WorkspaceService/ListTabz"}, "", "unknown procedure"},
		{"a stream", []string{"WorkerWatchService/WatchEvents"}, "", "streams"},
		{"a Worker procedure without --worker", []string{"WorkerAgentService/ListAgents"}, "", "needs --worker"},
		{"a hub procedure with --worker", []string{"WorkspaceService/ListTabs", "--worker", "self"}, "", "takes no --worker"},
		{"an empty --worker", []string{"WorkerAgentService/ListAgents", "--worker", " "}, "", "--worker is empty"},
		{"a request that is no JSON", []string{"WorkspaceService/ListTabs", "{"}, "", "is not a leapmux.v1.ListTabsRequest"},
		{"a field that the request does not declare", []string{"WorkspaceService/ListTabs", `{"tab_ids": ["x"]}`}, "", "is not a leapmux.v1.ListTabsRequest"},
		{"a value of the wrong type", []string{"WorkspaceService/ListTabs", `{"workspace_ids": "ws-1"}`}, "", "is not a leapmux.v1.ListTabsRequest"},
		{"an empty request", []string{"WorkspaceService/ListTabs", " "}, "", "the request is empty"},
		{"an empty request on stdin", []string{"WorkspaceService/ListTabs", "-"}, "\n", "the request is empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"rpc"}, tc.args...)
			args = append(args, "--hub", "https://stub.invalid", "--json")
			o := runControlIn(t, tc.stdin, args...)
			f := o.requireFailure(t, cli.CodeUsage)
			assert.Contains(t, f.Message, tc.wants)
		})
	}
}
