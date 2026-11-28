//go:build unix

package claude

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	claudeForkHelperWantEnv     = "GO_WANT_HELPER_PROCESS_CLAUDE_FORK"
	claudeForkHelperScenarioEnv = "LEAPMUX_CLAUDE_FORK_TEST_SCENARIO"
	claudeForkHelperRecordEnv   = "LEAPMUX_CLAUDE_FORK_TEST_RECORD"
)

const (
	claudeForkScenarioServe      = "serve"
	claudeForkScenarioFailBefore = "fail-before-init"
)

type claudeForkHelperRecord struct {
	Args []string `json:"args"`
}

func TestHelperProcessClaudeFork(*testing.T) {
	if os.Getenv(claudeForkHelperWantEnv) != "1" {
		return
	}
	args := os.Args
	if idx := slices.Index(args, "--"); idx >= 0 {
		args = args[idx+1:]
	}
	os.Exit(runFakeClaudeFork(os.Getenv(claudeForkHelperScenarioEnv), os.Getenv(claudeForkHelperRecordEnv), args))
}

func runFakeClaudeFork(scenario, recordDir string, args []string) int {
	raw, _ := json.Marshal(claudeForkHelperRecord{Args: args})
	_ = os.WriteFile(filepath.Join(recordDir, "record.json"), raw, 0o600)
	if scenario == claudeForkScenarioFailBefore {
		return 1
	}

	out := bufio.NewWriter(os.Stdout)
	write := func(frame string) {
		_, _ = out.WriteString(frame + "\n")
		_ = out.Flush()
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for scanner.Scan() {
		var req struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
			Request   struct {
				Subtype string `json:"subtype"`
			} `json:"request"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil || req.Type != "control_request" {
			continue
		}
		respObj := map[string]any{}
		if req.Request.Subtype == "initialize" {
			respObj["models"] = []map[string]any{
				{"value": "sonnet", "displayName": "Sonnet"},
			}
		}
		reply, _ := json.Marshal(map[string]any{
			"type": "control_response",
			"response": map[string]any{
				"subtype":    "success",
				"request_id": req.RequestID,
				"response":   respObj,
			},
		})
		write(string(reply))
	}
	return 0
}

func installFakeClaudeFork(t *testing.T, scenario string) string {
	t.Helper()
	recordDir := t.TempDir()
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary:      "claude",
		HelperRun:   "TestHelperProcessClaudeFork",
		WantEnv:     claudeForkHelperWantEnv,
		Env:         []string{claudeForkHelperScenarioEnv + "=" + scenario, claudeForkHelperRecordEnv + "=" + recordDir},
		ForwardArgs: true,
	})
	return recordDir
}

func TestClaudeStartForkDefersSessionIDUntilInit(t *testing.T) {
	recordDir := installFakeClaudeFork(t, claudeForkScenarioServe)
	home := t.TempDir()
	workDir := t.TempDir()
	srcSessionID := "3f9a1c2e-77b4-4d81-9e0f-5a6b7c8d9e0f"

	sink := &agenttest.Sink{}
	started, err := Start(t.Context(), agent.Options{
		AgentID:        "claude-fork-defer",
		WorkingDir:     workDir,
		HomeDir:        home,
		Shell:          testutil.TestShell(),
		AgentProvider:  leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Session:        agent.ForkSession(srcSessionID, workDir),
		StartupTimeout: 30 * time.Second,
		APITimeout:     30 * time.Second,
	}, agent.NewProviderServices(sink))
	require.NoError(t, err)
	a := started.(*Agent)
	t.Cleanup(func() {
		a.Stop()
		_ = a.Wait()
	})

	// Claude in stream-json mode does not create <newID>.jsonl during initialize;
	// Start must not persist the minted fork UUID until turn 1 emits system/init.
	assert.Empty(t, sink.LastSessionID(), "forked Claude session ID must not be persisted before turn 1 init")

	rawRec, err := os.ReadFile(filepath.Join(recordDir, "record.json"))
	require.NoError(t, err)
	var rec claudeForkHelperRecord
	require.NoError(t, json.Unmarshal(rawRec, &rec))
	idx := slices.Index(rec.Args, "--session-id")
	require.GreaterOrEqual(t, idx, 0)
	require.Greater(t, len(rec.Args), idx+1)
	mintedID := rec.Args[idx+1]
	assert.NotEqual(t, srcSessionID, mintedID)

	// Once turn 1 starts and Claude emits system/init, the session ID is persisted.
	a.HandleOutput([]byte(`{"type":"system","subtype":"init","session_id":"` + mintedID + `"}`))
	assert.Equal(t, mintedID, sink.LastSessionID())
}

func TestClaudeStartForkStagesTranscriptWithExtraEnvConfigDir(t *testing.T) {
	_ = installFakeClaudeFork(t, claudeForkScenarioServe)
	home := t.TempDir()
	customConfigDir := filepath.Join(t.TempDir(), "custom-claude-cfg")
	srcDir := t.TempDir()
	targetDir := t.TempDir()
	srcSessionID := "3f9a1c2e-77b4-4d81-9e0f-5a6b7c8d9e0f"

	srcProjectDir := filepath.Join(customConfigDir, "projects", mangleClaudePath(srcDir))
	writeClaudeTranscript(t, srcProjectDir, srcSessionID, time.Now(),
		claudeUserRecord(srcDir, srcSessionID, "from custom extra env config dir"))

	sink := &agenttest.Sink{}
	started, err := Start(t.Context(), agent.Options{
		AgentID:        "claude-fork-extraenv",
		WorkingDir:     targetDir,
		HomeDir:        home,
		Shell:          testutil.TestShell(),
		AgentProvider:  leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Session:        agent.ForkSession(srcSessionID, srcDir),
		ExtraEnv:       []string{"CLAUDE_CONFIG_DIR=" + customConfigDir},
		StartupTimeout: 30 * time.Second,
		APITimeout:     30 * time.Second,
	}, agent.NewProviderServices(sink))
	require.NoError(t, err)
	t.Cleanup(func() {
		started.Stop()
		_ = started.Wait()
	})

	stagedPath := filepath.Join(customConfigDir, "projects", mangleClaudePath(targetDir), srcSessionID+".jsonl")
	raw, err := os.ReadFile(stagedPath)
	require.NoError(t, err, "staging must respect CLAUDE_CONFIG_DIR passed via opts.ExtraEnv")
	assert.Contains(t, string(raw), "from custom extra env config dir")
}

func TestClaudeStartForkRemovesStagedTranscriptWhenInitializeFails(t *testing.T) {
	_ = installFakeClaudeFork(t, claudeForkScenarioFailBefore)
	home := t.TempDir()
	srcDir := t.TempDir()
	targetDir := t.TempDir()
	srcSessionID := "3f9a1c2e-77b4-4d81-9e0f-5a6b7c8d9e0f"

	srcProjectDir := filepath.Join(home, ".claude", "projects", mangleClaudePath(srcDir))
	writeClaudeTranscript(t, srcProjectDir, srcSessionID, time.Now(),
		claudeUserRecord(srcDir, srcSessionID, "source transcript"))

	_, err := Start(t.Context(), agent.Options{
		AgentID:        "claude-fork-fail-init",
		WorkingDir:     targetDir,
		HomeDir:        home,
		Shell:          testutil.TestShell(),
		AgentProvider:  leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Session:        agent.ForkSession(srcSessionID, srcDir),
		StartupTimeout: 30 * time.Second,
		APITimeout:     30 * time.Second,
	}, agent.NewProviderServices(&agenttest.Sink{}))
	require.Error(t, err)

	stagedPath := filepath.Join(home, ".claude", "projects", mangleClaudePath(targetDir), srcSessionID+".jsonl")
	_, statErr := os.Stat(stagedPath)
	assert.ErrorIs(t, statErr, os.ErrNotExist, "failed startup before initialize must clean up staged transcript")
}
