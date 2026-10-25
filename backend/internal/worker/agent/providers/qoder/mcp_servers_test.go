package qoder

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// qoderScriptedReply is the answer to one control request. A reply with a
// refusal text is an error reply.
type qoderScriptedReply struct {
	response string
	refusal  string
}

type qoderScriptedRequest struct {
	subtype string
	request json.RawMessage
}

// qoderControlScript answers each control request with the next scripted reply
// for its subtype. A subtype without a remaining reply gets no answer, so its
// request waits for its deadline. beforeReply runs before each reply, or in
// place of the missing one.
type qoderControlScript struct {
	agent       *Agent
	mu          sync.Mutex
	replies     map[string][]qoderScriptedReply
	requests    []qoderScriptedRequest
	beforeReply func(subtype string)
}

func (s *qoderControlScript) Write(raw []byte) (int, error) {
	var frame struct {
		RequestID string          `json:"request_id"`
		Request   json.RawMessage `json:"request"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		return 0, err
	}
	subtype := providerkit.StringField(frame.Request, "subtype")
	s.mu.Lock()
	s.requests = append(s.requests, qoderScriptedRequest{subtype: subtype, request: frame.Request})
	queue := s.replies[subtype]
	var reply *qoderScriptedReply
	if len(queue) > 0 {
		reply = &queue[0]
		s.replies[subtype] = queue[1:]
	}
	hook := s.beforeReply
	s.mu.Unlock()
	if hook != nil {
		hook(subtype)
	}
	if reply == nil {
		return len(raw), nil
	}
	envelope := map[string]any{"subtype": "success", "request_id": frame.RequestID}
	if reply.refusal != "" {
		envelope = map[string]any{"subtype": "error", "request_id": frame.RequestID, "error": reply.refusal}
	} else if reply.response != "" {
		envelope["response"] = json.RawMessage(reply.response)
	}
	response, err := json.Marshal(map[string]any{"type": "control_response", "response": envelope})
	if err != nil {
		return 0, err
	}
	s.agent.HandleOutput(response)
	return len(raw), nil
}

func (*qoderControlScript) Close() error { return nil }

func (s *qoderControlScript) subtypes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	subtypes := make([]string, 0, len(s.requests))
	for _, request := range s.requests {
		subtypes = append(subtypes, request.subtype)
	}
	return subtypes
}

// setServers returns the `servers` object of the n-th mcp_set_servers request.
func (s *qoderControlScript) setServers(t *testing.T, n int) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := 0
	for _, request := range s.requests {
		if request.subtype != "mcp_set_servers" {
			continue
		}
		if seen == n {
			var body struct {
				Servers json.RawMessage `json:"servers"`
			}
			require.NoError(t, json.Unmarshal(request.request, &body))
			return string(body.Servers)
		}
		seen++
	}
	t.Fatalf("no mcp_set_servers request #%d", n)
	return ""
}

func newQoderControlScript(t *testing.T, replies map[string][]qoderScriptedReply) (*Agent, *qoderControlScript) {
	t.Helper()
	a := newOfflineAgent(t, &agenttest.Sink{})
	script := &qoderControlScript{agent: a, replies: replies}
	a.SetStdinForTest(script)
	return a, script
}

// mcpStatus is an mcp_status reply that lists the given server objects.
func mcpStatus(servers ...string) qoderScriptedReply {
	list := "["
	for i, server := range servers {
		if i > 0 {
			list += ","
		}
		list += server
	}
	return qoderScriptedReply{response: `{"servers":` + list + `]}`}
}

const (
	userConfig     = `{"command":"/usr/bin/node","args":["user-server.mjs"],"env":{"API_TOKEN":"user-secret-value"},"scope":"user"}`
	projectConfig  = `{"command":"project-server","args":[],"scope":"project"}`
	userDynamic    = `{"command":"/usr/bin/node","args":["user-server.mjs"],"env":{"API_TOKEN":"user-secret-value"},"scope":"dynamic"}`
	projectDynamic = `{"command":"project-server","args":[],"scope":"dynamic"}`
	oneTool        = `[{"name":"mcp__server__ask","description":"Ask."}]`
	noPayload      = ""
)

func mcpServerEntry(name, status, config, tools string) string {
	return `{"name":"` + name + `","status":"` + status + `","config":` + config + `,"tools":` + tools + `}`
}

func TestQoderFormServersSelectsActiveConfiguredServers(t *testing.T) {
	t.Parallel()
	active := func(name, config string) qoderMcpServer {
		return qoderMcpServer{Name: name, Status: "connected", Config: json.RawMessage(config), Tools: []json.RawMessage{json.RawMessage(`{"name":"t"}`)}}
	}
	cases := []struct {
		name    string
		servers []qoderMcpServer
		want    map[string]string
	}{
		{name: "no server", servers: nil, want: nil},
		{
			name: "every configured scope",
			servers: []qoderMcpServer{
				active("user", `{"command":"u","scope":"user"}`),
				active("project", `{"command":"p","scope":"project"}`),
				active("local", `{"command":"l","scope":"local"}`),
				active("flag", `{"url":"http://127.0.0.1:1/mcp","type":"http","scope":"flag"}`),
			},
			want: map[string]string{
				"user":    `{"command":"u","scope":"user"}`,
				"project": `{"command":"p","scope":"project"}`,
				"local":   `{"command":"l","scope":"local"}`,
				"flag":    `{"url":"http://127.0.0.1:1/mcp","type":"http","scope":"flag"}`,
			},
		},
		{
			name: "a server that the session skipped lists no tool",
			servers: []qoderMcpServer{
				{Name: "skipped", Status: "connected", Config: json.RawMessage(`{"command":"s","scope":"user"}`), Tools: []json.RawMessage{}},
				{Name: "absent-tools", Status: "connected", Config: json.RawMessage(`{"command":"s","scope":"user"}`)},
			},
			want: nil,
		},
		{
			name: "a server that is not connected",
			servers: func() []qoderMcpServer {
				var servers []qoderMcpServer
				for _, status := range []string{"disconnected", "disconnecting", "connecting", "needs-auth", "auth_required", "blocked", "disabled", ""} {
					server := active("s-"+status, `{"command":"s","scope":"user"}`)
					server.Status = status
					servers = append(servers, server)
				}
				return servers
			}(),
			want: nil,
		},
		{
			name: "plugin and SDK servers",
			servers: []qoderMcpServer{
				active("plugin", `{"command":"p","scope":"user","plugin":{"name":"helper","id":"helper@market"}}`),
				active("sdk", `{"type":"sdk","scope":"user"}`),
				active("null-plugin", `{"command":"n","scope":"user","plugin":null}`),
			},
			want: map[string]string{"null-plugin": `{"command":"n","scope":"user","plugin":null}`},
		},
		{
			name: "an unknown or absent scope",
			servers: []qoderMcpServer{
				active("unknown", `{"command":"u","scope":"managed"}`),
				active("absent", `{"command":"a"}`),
			},
			want: nil,
		},
		{
			name: "a configuration that is not an object",
			servers: []qoderMcpServer{
				active("string", `"user"`),
				active("array", `[]`),
				active("null", `null`),
				active("empty", ``),
				active("number", `0`),
			},
			want: nil,
		},
		{
			name:    "a server without a name",
			servers: []qoderMcpServer{active("", `{"command":"u","scope":"user"}`)},
			want:    nil,
		},
		{
			name: "a dynamic server leaves the whole set to its host",
			servers: []qoderMcpServer{
				active("user", `{"command":"u","scope":"user"}`),
				{Name: "host", Status: "disconnected", Config: json.RawMessage(`{"command":"h","scope":"dynamic"}`)},
			},
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := qoderFormServers(tc.servers)
			if tc.want == nil {
				assert.Nil(t, got)
				return
			}
			gotText := make(map[string]string, len(got))
			for name, config := range got {
				gotText[name] = string(config)
			}
			assert.Equal(t, tc.want, gotText)
		})
	}
}

func TestQoderRegistersActiveConfiguredMcpServersForForms(t *testing.T) {
	t.Parallel()
	a, script := newQoderControlScript(t, map[string][]qoderScriptedReply{
		"mcp_status": {
			mcpStatus(
				mcpServerEntry("user_server", "connected", userConfig, oneTool),
				mcpServerEntry("project_server", "connected", projectConfig, oneTool),
				mcpServerEntry("blocked_server", "connected", `{"command":"b","scope":"user"}`, `[]`),
			),
			mcpStatus(
				mcpServerEntry("user_server", "connected", userDynamic, oneTool),
				mcpServerEntry("project_server", "connected", projectDynamic, oneTool),
				mcpServerEntry("blocked_server", "connected", `{"command":"b","scope":"user"}`, `[]`),
			),
		},
		"mcp_set_servers": {{response: `{"added":["project_server","user_server"],"removed":[],"errors":{}}`}},
	})
	require.NoError(t, a.registerMcpServersForForms())
	assert.Equal(t, []string{"mcp_status", "mcp_set_servers", "mcp_status"}, script.subtypes())
	// Each configuration goes back unchanged, secrets included, and only to Qoder.
	assert.JSONEq(t, `{"user_server":`+userConfig+`,"project_server":`+projectConfig+`}`, script.setServers(t, 0))
}

func TestQoderMcpRegistrationSendsNothingWithoutAnActiveConfiguredServer(t *testing.T) {
	t.Parallel()
	for name, status := range map[string]qoderScriptedReply{
		"no server":      mcpStatus(),
		"skipped server": mcpStatus(mcpServerEntry("blocked_server", "connected", `{"command":"b","scope":"user"}`, `[]`)),
		"failed server":  mcpStatus(mcpServerEntry("failed_server", "disconnected", `{"command":"f","scope":"project"}`, `[]`)),
		"absent list":    {response: `{}`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, script := newQoderControlScript(t, map[string][]qoderScriptedReply{"mcp_status": {status}})
			require.NoError(t, a.registerMcpServersForForms())
			assert.Equal(t, []string{"mcp_status"}, script.subtypes())
		})
	}
}

func TestQoderMcpRegistrationLeavesTheDynamicSetOfAnotherHost(t *testing.T) {
	t.Parallel()
	a, script := newQoderControlScript(t, map[string][]qoderScriptedReply{
		"mcp_status": {mcpStatus(
			mcpServerEntry("user_server", "connected", userConfig, oneTool),
			mcpServerEntry("host_server", "connected", `{"command":"h","scope":"dynamic"}`, oneTool),
		)},
	})
	require.NoError(t, a.registerMcpServersForForms())
	assert.Equal(t, []string{"mcp_status"}, script.subtypes())
}

func TestQoderMcpRegistrationRestoresACopyThatFailsToConnect(t *testing.T) {
	t.Parallel()
	a, script := newQoderControlScript(t, map[string][]qoderScriptedReply{
		"mcp_status": {
			mcpStatus(
				mcpServerEntry("user_server", "connected", userConfig, oneTool),
				mcpServerEntry("project_server", "connected", projectConfig, oneTool),
			),
			mcpStatus(
				mcpServerEntry("user_server", "disconnected", userDynamic, `[]`),
				mcpServerEntry("project_server", "connected", projectDynamic, oneTool),
			),
		},
		"mcp_set_servers": {
			{response: `{"added":["project_server","user_server"],"removed":[],"errors":{}}`},
			{response: `{"added":[],"removed":["user_server"],"errors":{}}`},
		},
	})
	err := a.registerMcpServersForForms()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "user_server")
	assert.NotContains(t, err.Error(), "user-secret-value", "the error states names, never a configuration")
	assert.Equal(t, []string{"mcp_status", "mcp_set_servers", "mcp_status", "mcp_set_servers"}, script.subtypes())
	assert.JSONEq(t, `{"project_server":`+projectConfig+`}`, script.setServers(t, 1))
}

func TestQoderMcpRegistrationRestoresEveryServerWhenNoCopyConnects(t *testing.T) {
	t.Parallel()
	a, script := newQoderControlScript(t, map[string][]qoderScriptedReply{
		"mcp_status": {
			mcpStatus(mcpServerEntry("user_server", "connected", userConfig, oneTool)),
			mcpStatus(mcpServerEntry("user_server", "auth_required", userDynamic, `[]`)),
		},
		"mcp_set_servers": {
			{response: `{"added":["user_server"],"removed":[],"errors":{}}`},
			{response: `{"added":[],"removed":["user_server"],"errors":{}}`},
		},
	})
	require.Error(t, a.registerMcpServersForForms())
	assert.Equal(t, []string{"mcp_status", "mcp_set_servers", "mcp_status", "mcp_set_servers"}, script.subtypes())
	// Qoder refuses a null set, so the restoring request sends an empty object.
	assert.Equal(t, `{}`, script.setServers(t, 1))
}

func TestQoderMcpRegistrationReportsARefusedServerWithoutRestoringIt(t *testing.T) {
	t.Parallel()
	a, script := newQoderControlScript(t, map[string][]qoderScriptedReply{
		"mcp_status": {
			mcpStatus(
				mcpServerEntry("user_server", "connected", userConfig, oneTool),
				mcpServerEntry("managed_server", "connected", `{"qoder_url":"bad","scope":"user"}`, oneTool),
			),
			mcpStatus(
				mcpServerEntry("user_server", "connected", userDynamic, oneTool),
				mcpServerEntry("managed_server", "connected", `{"qoder_url":"bad","scope":"user"}`, oneTool),
			),
		},
		"mcp_set_servers": {{response: `{"added":["user_server"],"removed":[],"errors":{"managed_server":"Invalid managed MCP Gateway URL"}}`}},
	})
	err := a.registerMcpServersForForms()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "managed_server")
	assert.NotContains(t, err.Error(), "user_server")
	// The refused server kept its own entry, so nothing needs a restore.
	assert.Equal(t, []string{"mcp_status", "mcp_set_servers", "mcp_status"}, script.subtypes())
}

func TestQoderMcpRegistrationStopsAtARefusedRequest(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		replies map[string][]qoderScriptedReply
		want    []string
	}{
		"status": {
			replies: map[string][]qoderScriptedReply{"mcp_status": {{refusal: "MCP client manager is not available in this session"}}},
			want:    []string{"mcp_status"},
		},
		"malformed status": {
			replies: map[string][]qoderScriptedReply{"mcp_status": {{response: `{"servers":{}}`}}},
			want:    []string{"mcp_status"},
		},
		"absent status": {
			replies: map[string][]qoderScriptedReply{"mcp_status": {{response: noPayload}}},
			want:    []string{"mcp_status"},
		},
		"registration that stored no copy": {
			replies: map[string][]qoderScriptedReply{
				"mcp_status": {
					mcpStatus(mcpServerEntry("user_server", "connected", userConfig, oneTool)),
					mcpStatus(mcpServerEntry("user_server", "connected", userConfig, oneTool)),
				},
				"mcp_set_servers": {{refusal: "`servers` must be an object keyed by server name"}},
			},
			want: []string{"mcp_status", "mcp_set_servers", "mcp_status"},
		},
		"registration and its status check": {
			replies: map[string][]qoderScriptedReply{
				"mcp_status": {
					mcpStatus(mcpServerEntry("user_server", "connected", userConfig, oneTool)),
					{refusal: "MCP client manager is not available in this session"},
				},
				"mcp_set_servers": {{refusal: "Failed to restart MCP servers: boom"}},
			},
			want: []string{"mcp_status", "mcp_set_servers", "mcp_status"},
		},
		"malformed registration": {
			replies: map[string][]qoderScriptedReply{
				"mcp_status":      {mcpStatus(mcpServerEntry("user_server", "connected", userConfig, oneTool))},
				"mcp_set_servers": {{response: `{"errors":[]}`}},
			},
			want: []string{"mcp_status", "mcp_set_servers"},
		},
		"verification": {
			replies: map[string][]qoderScriptedReply{
				"mcp_status": {
					mcpStatus(mcpServerEntry("user_server", "connected", userConfig, oneTool)),
					{refusal: "MCP client manager is not available in this session"},
				},
				"mcp_set_servers": {{response: `{"added":["user_server"],"removed":[],"errors":{}}`}},
			},
			want: []string{"mcp_status", "mcp_set_servers", "mcp_status"},
		},
		"restoration": {
			replies: map[string][]qoderScriptedReply{
				"mcp_status": {
					mcpStatus(mcpServerEntry("user_server", "connected", userConfig, oneTool)),
					mcpStatus(mcpServerEntry("user_server", "disconnected", userDynamic, `[]`)),
				},
				"mcp_set_servers": {
					{response: `{"added":["user_server"],"removed":[],"errors":{}}`},
					{refusal: "Failed to restart MCP servers: boom"},
				},
			},
			want: []string{"mcp_status", "mcp_set_servers", "mcp_status", "mcp_set_servers"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, script := newQoderControlScript(t, tc.replies)
			err := a.registerMcpServersForForms()
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "user-secret-value")
			assert.Equal(t, tc.want, script.subtypes())
		})
	}
}

// Qoder stores the dynamic set before it restarts the servers, so a refused
// restart can leave a copy that hides the working configured entry.
func TestQoderMcpRegistrationRestoresACopyAfterARefusedRestart(t *testing.T) {
	t.Parallel()
	a, script := newQoderControlScript(t, map[string][]qoderScriptedReply{
		"mcp_status": {
			mcpStatus(
				mcpServerEntry("user_server", "connected", userConfig, oneTool),
				mcpServerEntry("project_server", "connected", projectConfig, oneTool),
			),
			mcpStatus(
				mcpServerEntry("user_server", "disconnected", userDynamic, `[]`),
				mcpServerEntry("project_server", "connected", projectDynamic, oneTool),
			),
		},
		"mcp_set_servers": {
			{refusal: "Failed to restart MCP servers: user_server exited"},
			{response: `{"added":[],"removed":["user_server"],"errors":{}}`},
		},
	})
	err := a.registerMcpServersForForms()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Failed to restart MCP servers")
	assert.Contains(t, err.Error(), "the registered copies of the MCP servers user_server did not connect")
	assert.NotContains(t, err.Error(), "user-secret-value")
	assert.Equal(t, []string{"mcp_status", "mcp_set_servers", "mcp_status", "mcp_set_servers"}, script.subtypes())
	assert.JSONEq(t, `{"project_server":`+projectConfig+`}`, script.setServers(t, 1))
}

func TestQoderMcpRegistrationAcceptsARegistrationReplyWithoutAPayload(t *testing.T) {
	t.Parallel()
	a, script := newQoderControlScript(t, map[string][]qoderScriptedReply{
		"mcp_status": {
			mcpStatus(mcpServerEntry("user_server", "connected", userConfig, oneTool)),
			mcpStatus(mcpServerEntry("user_server", "connected", userDynamic, oneTool)),
		},
		"mcp_set_servers": {{response: noPayload}},
	})
	require.NoError(t, a.registerMcpServersForForms())
	assert.Equal(t, []string{"mcp_status", "mcp_set_servers", "mcp_status"}, script.subtypes())
}

func TestQoderMcpRegistrationWaitsForTheRestartOnTheInjectedClock(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	processDone := make(chan struct{})
	t.Cleanup(func() { close(processDone) })
	a := newOfflineAgentWithRuntime(t, &agenttest.Sink{}, clock, processDone)
	script := &qoderControlScript{agent: a, replies: map[string][]qoderScriptedReply{
		"mcp_status": {mcpStatus(mcpServerEntry("user_server", "connected", userConfig, oneTool))},
	}}
	a.SetStdinForTest(script)
	trap := clock.Trap().NewTimer(providerkit.AwaitResponseTimerTag, "qoder control mcp_set_servers")
	defer trap.Close()
	result := make(chan error, 1)
	go func() { result <- a.registerMcpServersForForms() }()
	ctx := testutil.DeadlineContext(t)
	wait := testutil.WaitForTimer(t, ctx, trap)
	assert.Equal(t, mcpServerRestartTimeout, wait)
	clock.Advance(wait).MustWait(ctx)
	select {
	case err := <-result:
		assert.ErrorIs(t, err, errControlTimeout)
	case <-ctx.Done():
		t.Fatal("the registration did not end at the injected deadline")
	}
	assert.Equal(t, []string{"mcp_status", "mcp_set_servers"}, script.subtypes())
}

func TestQoderStartStreamRegistersMcpServersAfterTheHandshake(t *testing.T) {
	t.Parallel()
	a, script := newQoderControlScript(t, map[string][]qoderScriptedReply{
		"initialize": {{response: `{"models":[]}`}},
		"mcp_status": {
			mcpStatus(mcpServerEntry("user_server", "connected", userConfig, oneTool)),
			mcpStatus(mcpServerEntry("user_server", "connected", userDynamic, oneTool)),
		},
		"mcp_set_servers": {{response: `{"added":["user_server"],"removed":[],"errors":{}}`}},
	})
	a.opts.HomeDir = t.TempDir()
	require.NoError(t, a.startStream())
	assert.Equal(t, []string{"initialize", "mcp_status", "mcp_set_servers", "mcp_status"}, script.subtypes())
}

func TestQoderStartStreamKeepsAnAgentWhoseMcpRegistrationFails(t *testing.T) {
	t.Parallel()
	a, script := newQoderControlScript(t, map[string][]qoderScriptedReply{
		"initialize": {{response: `{"models":[]}`}},
		"mcp_status": {{refusal: "MCP client manager is not available in this session"}},
	})
	a.opts.HomeDir = t.TempDir()
	require.NoError(t, a.startStream(), "the agent works without input forms")
	assert.Equal(t, []string{"initialize", "mcp_status"}, script.subtypes())
}

func TestQoderStartStreamFailsWhenTheProcessEndsDuringMcpRegistration(t *testing.T) {
	t.Parallel()
	processDone := make(chan struct{})
	a := newOfflineAgentWithRuntime(t, &agenttest.Sink{}, nil, processDone)
	a.opts.HomeDir = t.TempDir()
	script := &qoderControlScript{agent: a, replies: map[string][]qoderScriptedReply{
		"initialize": {{response: `{"models":[]}`}},
	}}
	var once sync.Once
	script.beforeReply = func(subtype string) {
		if subtype == "mcp_status" {
			once.Do(func() { close(processDone) })
		}
	}
	a.SetStdinForTest(script)
	err := a.startStream()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "register Qoder MCP servers for input forms")
	assert.Contains(t, err.Error(), "agent process exited")
}

func TestQoderStartStreamFailsWithoutTheHandshake(t *testing.T) {
	t.Parallel()
	a, script := newQoderControlScript(t, map[string][]qoderScriptedReply{
		"initialize": {{refusal: "authentication_required"}},
	})
	err := a.startStream()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "initialize Qoder stream")
	assert.Equal(t, []string{"initialize"}, script.subtypes(), "no MCP request precedes a successful handshake")
}
