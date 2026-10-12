//go:build unix

package gemini

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const geminiLoadReplayHelperEnv = "LEAPMUX_GEMINI_LOAD_REPLAY_HELPER"
const geminiLoadReplayRequestsEnv = "LEAPMUX_GEMINI_LOAD_REPLAY_REQUESTS"

// TestHelperProcessGeminiLoadReplay plays Gemini CLI 0.62.0 on a session/load.
// It writes the frames that the real CLI wrote, in the real order: the first
// replay frame, the session/load reply, and then the rest of the replay. A
// session/prompt streams the new answer before its reply. Each reply keeps the
// real result and answers the ID of the request that it answers.
func TestHelperProcessGeminiLoadReplay(*testing.T) {
	if os.Getenv(geminiLoadReplayHelperEnv) != "1" {
		return
	}
	writer := bufio.NewWriter(os.Stdout)
	write := func(line string) {
		_, _ = writer.WriteString(line + "\n")
		_ = writer.Flush()
	}
	reply := func(id json.RawMessage, frame string) {
		var recorded struct {
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal([]byte(frame), &recorded) != nil {
			os.Exit(2)
		}
		write(`{"jsonrpc":"2.0","id":` + string(id) + `,"result":` + string(recorded.Result) + `}`)
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				SessionID string `json:"sessionId"`
			} `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			continue
		}
		file, err := os.OpenFile(os.Getenv(geminiLoadReplayRequestsEnv), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(2)
		}
		_, writeErr := fmt.Fprintf(file, "%s %s\n", request.Method, request.Params.SessionID)
		if closeErr := file.Close(); writeErr != nil || closeErr != nil {
			os.Exit(2)
		}
		switch request.Method {
		case acp.MethodInitialize:
			reply(request.ID, geminiInitializeReply)
		case acp.MethodSessionLoad:
			write(geminiReplayContext)
			reply(request.ID, geminiLoadReply)
			write(geminiReplayPrompt)
			write(geminiReplayAnswer)
			write(geminiReplayCommands)
		case acp.MethodSessionPrompt:
			write(geminiNewAnswer)
			reply(request.ID, geminiPromptReply)
		case acp.MethodSessionSetModel, acp.MethodSessionSetMode:
			write(`{"jsonrpc":"2.0","id":` + string(request.ID) + `,"result":{}}`)
		case acp.MethodSessionCancel:
		default:
			if len(request.ID) > 0 {
				write(`{"jsonrpc":"2.0","id":` + string(request.ID) + `,"error":{"code":-32601,"message":"Method not found"}}`)
			}
		}
	}
	os.Exit(0)
}

// The whole path of a resume: Start loads the stored session through
// session/load, the reader reads the replay that Gemini sends after the load
// reply, and the next prompt stores its answer. The replayed answer
// GEMINI_MOCK_REPLY is the stored transcript of the old tab, which the Worker
// copied into the new tab. Before the fix, the prompt stored ONE row,
// GEMINI_MOCK_REPLYOLDER_NATIVE_RESUME_CONFIRMED.
func TestGeminiIdleReplayContentIsNotRendered(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "work")
	require.NoError(t, os.MkdirAll(work, 0o700))
	requestsPath := filepath.Join(home, "load-replay-requests.txt")
	t.Setenv("GEMINI_CLI_HOME", home)
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary: "gemini", HelperRun: "TestHelperProcessGeminiLoadReplay", WantEnv: geminiLoadReplayHelperEnv,
		Env: []string{geminiLoadReplayRequestsEnv + "=" + requestsPath},
	})
	sink := &agenttest.Sink{}
	started, err := Start(t.Context(), agent.Options{
		AgentID: "gemini-load-replay", HomeDir: home, WorkingDir: work, ResumeSessionID: geminiReplaySessionID,
		// A plain shell keeps the private PATH instead of loading a user's zsh startup configuration.
		Shell: "/bin/sh", StartupTimeout: 30 * time.Second,
		Options: optionmap.Map{agent.OptionIDModel: "gemini-2.5-pro", agent.OptionIDPermissionMode: "default"},
	}, agent.NewProviderServices(sink))
	require.NoError(t, err)
	t.Cleanup(func() {
		started.Stop()
		err := started.Wait()
		require.True(t, expectedGeminiStopError(err), "unexpected controlled-peer cleanup failure: %v", err)
	})
	a, valid := started.(*Agent)
	require.True(t, valid)
	require.Equal(t, geminiReplaySessionID, a.CurrentSessionID(), "the load keeps the stored session")
	// The reader handles the frames in order. The command list arrives after the
	// replayed prompt and answer, so once it applies, the reader handled the
	// whole replay while no prompt ran.
	testutil.RequireEventually(t, func() bool { return a.HasAvailableCommand("memory") })
	assert.Empty(t, geminiRows(t, sink.Messages()), "the replay stores nothing")

	require.NoError(t, a.SendInput("OLDER_NATIVE_NEXT_PROMPT", nil))
	testutil.RequireEventually(t, func() bool { return !a.PromptActive() })

	assert.Equal(t, []string{"text:OLDER_NATIVE_RESUME_CONFIRMED", "turn_end:0"}, geminiRows(t, sink.Messages()),
		"the prompt stores exactly its own answer")
	requests, err := os.ReadFile(requestsPath)
	require.NoError(t, err)
	assert.Equal(t, []string{
		acp.MethodInitialize + " ",
		acp.MethodSessionLoad + " " + geminiReplaySessionID,
		acp.MethodSessionPrompt + " " + geminiReplaySessionID,
	}, strings.Split(strings.TrimSuffix(string(requests), "\n"), "\n"), "the peer saw the load and the prompt")
}
