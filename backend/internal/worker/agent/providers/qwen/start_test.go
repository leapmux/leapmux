//go:build unix

package qwen

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
	qwenFakeCLIEnv     = "GO_WANT_HELPER_PROCESS_QWEN"
	qwenFakeRequestLog = "QWEN_FAKE_REQUEST_LOG"
	// qwenFakeExitMethod makes the fake Qwen exit by itself, as a crash or a
	// natural end does, with no stop of LeapMux's.
	qwenFakeExitMethod = "test/exit"
)

// qwenSessionFixture is Qwen's session/new response, reduced to what the start
// reads (probe `acp_models.jsonl`).
const qwenSessionFixture = `{"sessionId":"qwen-new","models":{"currentModelId":"mock-model(openai)","availableModels":[{"modelId":"mock-model(openai)","name":"Mock Model","description":null,"_meta":{"contextLimit":200000}},{"modelId":"mock-reasoner(openai)","name":"Mock Reasoner","description":null,"_meta":{"contextLimit":128000}}]},"modes":{"currentModeId":"default","availableModes":[{"id":"plan","name":"Plan"},{"id":"default","name":"Default"},{"id":"auto-edit","name":"Auto Edit"},{"id":"auto","name":"Auto"},{"id":"yolo","name":"YOLO"}]},"configOptions":[{"id":"mode","name":"Mode","category":"mode","type":"select","currentValue":"default","options":[{"value":"plan","name":"Plan"},{"value":"default","name":"Default"},{"value":"auto-edit","name":"Auto Edit"},{"value":"auto","name":"Auto"},{"value":"yolo","name":"YOLO"}]},{"id":"model","name":"Model","category":"model","type":"select","currentValue":"mock-model(openai)","options":[{"value":"mock-model(openai)","name":"Mock Model"},{"value":"mock-reasoner(openai)","name":"Mock Reasoner"}]},{"id":"reasoning_effort","name":"Reasoning effort","category":"thought_level","type":"select","currentValue":"high","options":[{"value":"none","name":"Thinking off"},{"value":"low","name":"Low"},{"value":"high","name":"High"}]}]}`

// TestHelperProcessQwenCLI is the fake Qwen: it records its environment and
// each request line, and answers the handshake.
func TestHelperProcessQwenCLI(*testing.T) {
	if os.Getenv(qwenFakeCLIEnv) != "1" {
		return
	}
	logPath := os.Getenv(qwenFakeRequestLog)
	env := "QWEN_CODE_NO_RELAUNCH=" + os.Getenv("QWEN_CODE_NO_RELAUNCH") + "\n" +
		"QWEN_CODE_DISABLE_CRON=" + os.Getenv("QWEN_CODE_DISABLE_CRON")
	if err := os.WriteFile(logPath+".env", []byte(env), 0o600); err != nil {
		os.Exit(2)
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(2)
	}
	scanner := bufio.NewScanner(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	for scanner.Scan() {
		_, _ = log.Write(append(append([]byte(nil), scanner.Bytes()...), '\n'))
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			continue
		}
		if req.Method == qwenFakeExitMethod {
			_ = log.Close()
			os.Exit(0)
		}
		if len(req.ID) == 0 {
			continue
		}
		result := `{}`
		switch req.Method {
		case acp.MethodInitialize:
			result = `{"protocolVersion":1,"agentInfo":{"name":"qwen-code","version":"0.24.3"},"agentCapabilities":{"loadSession":true,"promptCapabilities":{"image":true,"audio":true,"embeddedContext":true},"sessionCapabilities":{"list":{},"resume":{}}}}`
		case acp.MethodSessionNew, acp.MethodSessionResume, acp.MethodSessionSetConfigOption:
			result = qwenSessionFixture
		}
		_, _ = fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"result":%s}`+"\n", req.ID, result)
		_ = writer.Flush()
	}
	_ = log.Close()
	os.Exit(0)
}

// startFakeQwen starts the provider against the fake Qwen.
func startFakeQwen(t *testing.T, opts agent.Options) (*Agent, func() []agenttest.RecordedRequest, string, string) {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	logFile := filepath.Join(dir, "requests.jsonl")
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary:    "qwen",
		HelperRun: "TestHelperProcessQwenCLI",
		WantEnv:   qwenFakeCLIEnv,
		ArgsFile:  argsFile,
		Env:       []string{qwenFakeRequestLog + "=" + logFile},
	})
	opts.AgentID = "qwen-agent"
	opts.WorkingDir = t.TempDir()
	opts.Shell = testutil.TestShell()
	opts.AgentProvider = leapmuxv1.AgentProvider_AGENT_PROVIDER_QWEN_CODE
	provider, err := Start(context.Background(), opts, agent.NewProviderServices(&agenttest.Sink{}))
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
	env, err := os.ReadFile(logFile + ".env")
	require.NoError(t, err)
	return a, requests, strings.TrimSpace(string(args)), string(env)
}

func TestStartQwenStatesTheApprovalModeAndTheProcessLayer(t *testing.T) {
	a, _, args, env := startFakeQwen(t, agent.Options{})

	assert.Equal(t, "--acp --approval-mode default", args, "Qwen's own default mode approves through a classifier")
	assert.Equal(t, "QWEN_CODE_NO_RELAUNCH=1\nQWEN_CODE_DISABLE_CRON=1", env,
		"the CLI does not relaunch itself into a second process, and it runs no cron or loop turn that states no end")
	assert.Equal(t, "qwen-new", a.SessionIDForTest())
	current := agent.CurrentOptions(a.OptionGroups())
	assert.Equal(t, contracts.QwenModeDefault, current[agent.OptionIDPermissionMode])
	assert.Equal(t, "high", current[contracts.QwenConfigReasoningEffort])
	windows := map[string]int64{}
	for _, model := range a.AvailableModelsForTest() {
		windows[model.Id] = model.ContextWindow
	}
	assert.Equal(t, map[string]int64{"mock-model(openai)": 200000, "mock-reasoner(openai)": 128000}, windows)
}

func TestStartQwenLaunchesInTheStoredMode(t *testing.T) {
	_, requests, args, _ := startFakeQwen(t, agent.Options{Options: map[string]string{agent.OptionIDPermissionMode: contracts.QwenModeYolo}})

	assert.Equal(t, "--acp --approval-mode yolo", args)
	// The session reports `default`, so the start moves it to the stored mode.
	modes := requestsFor(requests(), acp.MethodSessionSetConfigOption)
	modes = append(modes, requestsFor(requests(), acp.MethodSessionSetMode)...)
	assert.NotEmpty(t, modes, "the session enters the stored mode")
}

func TestStartQwenResumesWithoutAReplay(t *testing.T) {
	_, requests, _, _ := startFakeQwen(t, agent.Options{ResumeSessionID: "70dab2cd-62c7-4f76-b53e-50a950df1520"})

	assert.Empty(t, requestsFor(requests(), acp.MethodSessionLoad), "a load would replay the conversation")
	resumes := requestsFor(requests(), acp.MethodSessionResume)
	require.Len(t, resumes, 1)
	assert.Equal(t, "70dab2cd-62c7-4f76-b53e-50a950df1520", resumes[0].Params["sessionId"])
}

// startTranscriptReader starts the reader of one background child's transcript
// on a started agent, and returns it.
func startTranscriptReader(t *testing.T, a *Agent) *transcriptTail {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "projects", "-ws", "subagents", "qwen-new")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	a.startBackgroundTranscript("call_bg", "Background agent launched successfully.\noutput_file: "+filepath.Join(dir, "agent-x.jsonl"))
	a.stateMu.Lock()
	tail := a.children.tails["call_bg"]
	a.stateMu.Unlock()
	require.NotNil(t, tail, "the reader runs")
	return tail
}

// Stop ends every reader of a background transcript before it stops the
// process. A reader that outlived the agent would poll a file for an agent that
// can no longer show it.
func TestStartQwenStopEndsTheTranscriptReaders(t *testing.T) {
	a, _, _, _ := startFakeQwen(t, agent.Options{})
	tail := startTranscriptReader(t, a)

	a.Stop()

	select {
	case <-tail.stopped:
	default:
		t.Fatal("the reader still runs after Stop")
	}
	a.stateMu.Lock()
	assert.Empty(t, a.children.tails)
	a.stateMu.Unlock()
}

// Wait ends every reader of a background transcript once the process exited
// by itself: no later record can reach an agent that ended.
func TestStartQwenWaitEndsTheTranscriptReadersOfAnExitedProcess(t *testing.T) {
	a, _, _, _ := startFakeQwen(t, agent.Options{})
	tail := startTranscriptReader(t, a)

	require.NoError(t, a.SendNotification(qwenFakeExitMethod, nil))
	require.NoError(t, a.Wait())

	select {
	case <-tail.stopped:
	default:
		t.Fatal("the reader still runs after the process exited")
	}
	a.stateMu.Lock()
	assert.Empty(t, a.children.tails)
	a.stateMu.Unlock()
}
