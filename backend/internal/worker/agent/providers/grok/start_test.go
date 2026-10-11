//go:build unix

package grok

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

const (
	grokFakeCLIEnv     = "GO_WANT_HELPER_PROCESS_GROK"
	grokFakeRequestLog = "GROK_FAKE_REQUEST_LOG"
	// grokFakeTrustFirst makes the fake Grok ask for folder trust before it
	// answers session/new. The real Grok starts that prompt inside session/new
	// as a detached task, so the prompt can reach LeapMux first.
	grokFakeTrustFirst = "GROK_FAKE_TRUST_BEFORE_SESSION"
)

// grokInitializeFixture is Grok's initialize response, reduced to what the
// start reads.
const grokInitializeFixture = `{"protocolVersion":1,"agentCapabilities":{"loadSession":true,"promptCapabilities":{"image":false,"audio":false,"embeddedContext":true},"sessionCapabilities":{"list":{},"resume":{},"close":{}}},"_meta":{"agentVersion":"1.0.41","availableCommands":[{"name":"compact"},{"name":"goal"}]}}`

// grokSessionFixture is Grok's session/new response. It states no mode list.
const grokSessionFixture = `{"sessionId":"grok-new","models":{"currentModelId":"grok-4.6","availableModels":[{"modelId":"grok-4.6","name":"Grok 4.6","_meta":{"totalContextTokens":500000}},{"modelId":"mock","name":"Mock Model","_meta":{"totalContextTokens":128000}}]},"configOptions":[{"id":"model","name":"Model","category":"model","type":"select","currentValue":"grok-4.6","options":[{"value":"grok-4.6","name":"Grok 4.6"},{"value":"mock","name":"Mock Model"}]}]}`

// TestHelperProcessGrokCLI is the fake Grok: it records each request line and
// answers the handshake.
func TestHelperProcessGrokCLI(*testing.T) {
	if os.Getenv(grokFakeCLIEnv) != "1" {
		return
	}
	log, err := os.OpenFile(os.Getenv(grokFakeRequestLog), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(2)
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	writer := bufio.NewWriter(os.Stdout)
	// Each session/new after the first opens a session of its own id, as a
	// context clear sees it.
	newSessions := 0
	for scanner.Scan() {
		_, _ = log.Write(append(append([]byte(nil), scanner.Bytes()...), '\n'))
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil || len(req.ID) == 0 {
			continue
		}
		result := `{}`
		switch req.Method {
		case acp.MethodInitialize:
			result = grokInitializeFixture
		case acp.MethodSessionNew:
			newSessions++
			result = grokSessionFixture
			sessionID := "grok-new"
			if newSessions > 1 {
				sessionID = fmt.Sprintf("grok-new-%d", newSessions)
				result = strings.Replace(result, `"grok-new"`, `"`+sessionID+`"`, 1)
			}
			if os.Getenv(grokFakeTrustFirst) == "1" {
				_, _ = fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%d,"method":%q,"params":{"sessionId":%q,"cwd":"/repo","workspace":"/repo","configKinds":["mcp"]}}`+"\n",
					900+newSessions, contracts.GrokMethodFolderTrust, sessionID)
			}
		case acp.MethodSessionResume:
			result = grokSessionFixture
		}
		_, _ = fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"result":%s}`+"\n", req.ID, result)
		_ = writer.Flush()
	}
	_ = log.Close()
	os.Exit(0)
}

// startFakeGrok starts the provider against the fake Grok, and returns the
// agent and the requests that the fake recorded.
func startFakeGrok(t *testing.T, opts agent.Options) (*Agent, func() []agenttest.RecordedRequest, string) {
	t.Helper()
	return startFakeGrokWith(t, opts, &agenttest.Sink{})
}

// startFakeGrokWith is startFakeGrok with the sink of the agent, and with env
// entries ("KEY=value") that select the behavior of the fake Grok.
func startFakeGrokWith(t *testing.T, opts agent.Options, sink agent.ServiceFacets, env ...string) (*Agent, func() []agenttest.RecordedRequest, string) {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	logFile := filepath.Join(dir, "requests.jsonl")
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary:    "grok",
		HelperRun: "TestHelperProcessGrokCLI",
		WantEnv:   grokFakeCLIEnv,
		ArgsFile:  argsFile,
		Env:       append([]string{grokFakeRequestLog + "=" + logFile}, env...),
	})
	opts.AgentID = "grok-agent"
	opts.WorkingDir = t.TempDir()
	opts.Shell = testutil.TestShell()
	opts.AgentProvider = leapmuxv1.AgentProvider_AGENT_PROVIDER_GROK_BUILD
	provider, err := Start(context.Background(), opts, agent.NewProviderServices(sink))
	require.NoError(t, err)
	a := provider.(*Agent)
	t.Cleanup(func() {
		a.Stop()
		_ = a.Wait()
	})
	requests := func() []agenttest.RecordedRequest {
		data, err := os.ReadFile(logFile)
		require.NoError(t, err)
		var out []agenttest.RecordedRequest
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var req struct {
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if json.Unmarshal([]byte(line), &req) == nil {
				out = append(out, agenttest.RecordedRequest{Method: req.Method, Params: req.Params, Raw: line})
			}
		}
		return out
	}
	args, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	return a, requests, strings.TrimSpace(string(args))
}

func TestStartGrokRunsTheStdioAgentOnItsOwn(t *testing.T) {
	a, requests, args := startFakeGrok(t, agent.Options{})

	assert.Equal(t, "agent --no-leader stdio", args)
	assert.Equal(t, "grok-new", a.SessionIDForTest())

	initialize := requestsFor(requests(), acp.MethodInitialize)
	require.Len(t, initialize, 1)
	params := initialize[0].Params
	assert.Equal(t, map[string]any{"clientType": "leapmux", "clientIdentifier": "leapmux"}, params["_meta"],
		"Grok scopes its approval-mode notification to this identifier")
	capabilities := params["clientCapabilities"].(map[string]any)
	assert.Equal(t, false, capabilities["terminal"], "Grok runs its own shell commands")
	assert.Equal(t, map[string]any{"interactive": true}, capabilities["_meta"].(map[string]any)[grokFolderTrustCapability])
}

// Grok asks whether to trust the repository from inside session/new, in a task
// of its own, so the question can arrive before the answer that gives the
// session id. The worker stored that question under no session, and then
// refused the reader's answer as an answer for a different provider session:
// the trust card never cleared (E2E 149-grok-settings-trust). The question
// states its session, so it is stored under that session.
func TestStartGrokStoresAFolderTrustThatPrecedesTheSessionUnderThatSession(t *testing.T) {
	sink := &agenttest.ControlSink{}
	a, _, _ := startFakeGrokWith(t, agent.Options{}, sink, grokFakeTrustFirst+"=1")

	require.Equal(t, "grok-new", a.SessionIDForTest())
	published := sink.PublishedControls()
	require.Len(t, published, 1, "the question of the opening session reaches the reader")
	assert.Contains(t, string(published[0].Payload), contracts.GrokMethodFolderTrust)
	assert.Equal(t, "grok-new", published[0].AgentSessionID)
	assert.Equal(t, "grok-new", sink.LastSessionID(), "the agent enters the session that the answer must match")
}

func TestStartGrokStatesTheApprovalModeInTheSession(t *testing.T) {
	a, requests, _ := startFakeGrok(t, agent.Options{Options: map[string]string{contracts.GrokOptionApprovalMode: contracts.GrokApprovalModeAlwaysApprove}})

	sessions := requestsFor(requests(), acp.MethodSessionNew)
	require.Len(t, sessions, 1)
	assert.Equal(t, map[string]any{"yoloMode": true, "autoMode": false}, sessions[0].Params["_meta"])
	assert.Equal(t, contracts.GrokApprovalModeAlwaysApprove, agent.CurrentOptions(a.OptionGroups())[contracts.GrokOptionApprovalMode])
}

func TestStartGrokReadsTheHandshake(t *testing.T) {
	a, _, _ := startFakeGrok(t, agent.Options{})

	assert.Len(t, a.SupportedGoalActions(), 4, "the initialize response states the goal command before any update")
	windows := map[string]int64{}
	for _, model := range a.AvailableModelsForTest() {
		windows[model.Id] = model.ContextWindow
	}
	assert.Equal(t, map[string]int64{"grok-4.6": 500000, "mock": 128000}, windows)
	current := agent.CurrentOptions(a.OptionGroups())
	assert.Equal(t, contracts.GrokModeDefault, current[agent.OptionIDPermissionMode], "the static list stands in for the list the session omits")
	assert.Equal(t, contracts.GrokApprovalModeAsk, current[contracts.GrokOptionApprovalMode])
}

// Grok advertises session/close in its initialize response, so a context clear
// ends the outgoing session with it. session/cancel stops only a running turn,
// and the close is what stops the subagents and background commands of the
// session, which run while the session is idle too.
func TestStartGrokClosesTheSessionThatAContextClearReplaces(t *testing.T) {
	a, requests, _ := startFakeGrok(t, agent.Options{})

	sessionID, err := a.ClearContext()
	require.NoError(t, err)
	assert.Equal(t, "grok-new-2", sessionID)

	// The close goes out detached, after the new session opened.
	testutil.RequireEventually(t, func() bool { return len(requestsFor(requests(), acp.MethodSessionClose)) == 1 })
	assert.Equal(t, []string{"grok-new"}, sessionIDs(requestsFor(requests(), acp.MethodSessionClose)))
	assert.Empty(t, requestsFor(requests(), acp.MethodSessionCancel), "the idle session runs no turn to cancel")
}

func TestStartGrokResumesWithoutAReplay(t *testing.T) {
	_, requests, _ := startFakeGrok(t, agent.Options{ResumeSessionID: "01a0cf79-ace2"})

	assert.Empty(t, requestsFor(requests(), acp.MethodSessionLoad), "a load would replay the conversation")
	resumes := requestsFor(requests(), acp.MethodSessionResume)
	require.Len(t, resumes, 1)
	assert.Equal(t, "01a0cf79-ace2", resumes[0].Params["sessionId"])
	assert.Equal(t, map[string]any{"yoloMode": false, "autoMode": false}, resumes[0].Params["_meta"])
}
