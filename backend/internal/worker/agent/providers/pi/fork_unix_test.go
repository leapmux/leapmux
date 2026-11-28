//go:build unix

package pi

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
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir/agentdirtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	piForkHelperWantEnv     = "GO_WANT_HELPER_PROCESS_PI_FORK"
	piForkHelperScenarioEnv = "LEAPMUX_PI_FORK_TEST_SCENARIO"
	piForkHelperRecordEnv   = "LEAPMUX_PI_FORK_TEST_RECORD"
)

const (
	piForkScenarioServe      = "serve"
	piForkScenarioFailBefore = "fail-before-state"
)

type piForkHelperRecord struct {
	Args []string `json:"args"`
}

func TestHelperProcessPiFork(*testing.T) {
	if os.Getenv(piForkHelperWantEnv) != "1" {
		return
	}
	args := os.Args
	if idx := slices.Index(args, "--"); idx >= 0 {
		args = args[idx+1:]
	}
	os.Exit(runFakePiFork(os.Getenv(piForkHelperScenarioEnv), os.Getenv(piForkHelperRecordEnv), args))
}

func runFakePiFork(scenario, recordDir string, args []string) int {
	raw, _ := json.Marshal(piForkHelperRecord{Args: args})
	_ = os.WriteFile(filepath.Join(recordDir, "record.json"), raw, 0o600)
	if scenario == piForkScenarioFailBefore {
		return 1
	}

	sessionFile := ""
	if idx := slices.Index(args, "--session"); idx >= 0 && idx+1 < len(args) {
		sessionFile = args[idx+1]
	}

	out := bufio.NewWriter(os.Stdout)
	write := func(frame string) {
		_, _ = out.WriteString(frame + "\n")
		_ = out.Flush()
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for scanner.Scan() {
		var cmd struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		}
		if json.Unmarshal(scanner.Bytes(), &cmd) != nil || cmd.ID == "" {
			continue
		}
		var data any
		switch cmd.Type {
		case CommandGetState:
			data = map[string]any{
				"sessionId":   "forked-pi-session",
				"sessionFile": sessionFile,
			}
		case CommandGetAvailableModels:
			data = map[string]any{"models": []any{}}
		default:
			data = map[string]any{}
		}
		reply, _ := json.Marshal(map[string]any{
			"type":    "response",
			"id":      cmd.ID,
			"command": cmd.Type,
			"success": true,
			"data":    data,
		})
		write(string(reply))
	}
	return 0
}

func installFakePiFork(t *testing.T, scenario string) string {
	t.Helper()
	recordDir := t.TempDir()
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary:      "pi",
		HelperRun:   "TestHelperProcessPiFork",
		WantEnv:     piForkHelperWantEnv,
		Env:         []string{piForkHelperScenarioEnv + "=" + scenario, piForkHelperRecordEnv + "=" + recordDir},
		ForwardArgs: true,
	})
	return recordDir
}

func piForkStartOptions(t *testing.T, home, workingDir string) agent.Options {
	t.Helper()
	return agent.Options{
		AgentDirs:      agentdirtest.NewDirs(t, []agentdir.Spec{{Prefix: "pi"}}, t.TempDir()),
		AgentID:        "pi-fork-test",
		WorkingDir:     workingDir,
		HomeDir:        home,
		Shell:          testutil.TestShell(),
		AgentProvider:  leapmuxv1.AgentProvider_AGENT_PROVIDER_PI,
		APITimeout:     30 * time.Second,
		StartupTimeout: 30 * time.Second,
	}
}

func TestPiStartForkCleansUpAndRespectsExtraEnv(t *testing.T) {
	t.Run("does not write forked session file when AgentDirs is nil", func(t *testing.T) {
		_ = installFakePiFork(t, piForkScenarioServe)
		home := t.TempDir()
		srcDir := t.TempDir()
		targetDir := t.TempDir()
		root := filepath.Join(home, ".pi", "agent", "sessions")
		srcID := "pi-src-nil-dirs"
		writePiTranscript(t, root, srcDir, "2026-09-01T12-00-00-000Z_"+srcID+".jsonl", time.Now(),
			`{"type":"session","version":3,"id":"`+srcID+`","timestamp":"2026-09-01T12:00:00.000Z","cwd":`+agenttest.JSONString(srcDir)+`}`,
			`{"type":"message","message":{"role":"user","content":"hello"}}`,
		)

		opts := piForkStartOptions(t, home, targetDir)
		opts.AgentDirs = nil
		opts.Session = agent.ForkSession(srcID, srcDir)

		_, err := Start(t.Context(), opts, agent.NewProviderServices(&agenttest.Sink{}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "private agent directories")

		targetBucket := filepath.Join(root, manglePiPath(targetDir))
		entries, readErr := os.ReadDir(targetBucket)
		if !os.IsNotExist(readErr) {
			require.NoError(t, readErr)
			assert.Empty(t, entries, "nil AgentDirs must fail before writing any forked session file")
		}
	})

	t.Run("removes forked session file when startup fails", func(t *testing.T) {
		_ = installFakePiFork(t, piForkScenarioFailBefore)
		home := t.TempDir()
		srcDir := t.TempDir()
		targetDir := t.TempDir()
		root := filepath.Join(home, ".pi", "agent", "sessions")
		srcID := "pi-src-fail-start"
		writePiTranscript(t, root, srcDir, "2026-09-01T12-00-00-000Z_"+srcID+".jsonl", time.Now(),
			`{"type":"session","version":3,"id":"`+srcID+`","timestamp":"2026-09-01T12:00:00.000Z","cwd":`+agenttest.JSONString(srcDir)+`}`,
			`{"type":"message","message":{"role":"user","content":"hello"}}`,
		)

		opts := piForkStartOptions(t, home, targetDir)
		opts.Session = agent.ForkSession(srcID, srcDir)

		_, err := Start(t.Context(), opts, agent.NewProviderServices(&agenttest.Sink{}))
		require.Error(t, err)

		targetBucket := filepath.Join(root, manglePiPath(targetDir))
		entries, readErr := os.ReadDir(targetBucket)
		if !os.IsNotExist(readErr) {
			require.NoError(t, readErr)
			assert.Empty(t, entries, "failed startup must remove the orphaned forked .jsonl file")
		}
	})

	t.Run("respects PI_CODING_AGENT_SESSION_DIR passed via ExtraEnv", func(t *testing.T) {
		recordDir := installFakePiFork(t, piForkScenarioServe)
		home := t.TempDir()
		srcDir := t.TempDir()
		targetDir := t.TempDir()
		customSessionsRoot := filepath.Join(t.TempDir(), "custom-pi-sessions")
		srcID := "pi-src-extra-env"
		writePiTranscript(t, customSessionsRoot, srcDir, "2026-09-01T12-00-00-000Z_"+srcID+".jsonl", time.Now(),
			`{"type":"session","version":3,"id":"`+srcID+`","timestamp":"2026-09-01T12:00:00.000Z","cwd":`+agenttest.JSONString(srcDir)+`}`,
			`{"type":"message","message":{"role":"user","content":"from extra env"}}`,
		)

		opts := piForkStartOptions(t, home, targetDir)
		opts.Session = agent.ForkSession(srcID, srcDir)
		opts.ExtraEnv = []string{piSessionDirEnv + "=" + customSessionsRoot}

		started, err := Start(t.Context(), opts, agent.NewProviderServices(&agenttest.Sink{}))
		require.NoError(t, err)
		t.Cleanup(func() {
			started.Stop()
			_ = started.Wait()
		})

		rawRec, err := os.ReadFile(filepath.Join(recordDir, "record.json"))
		require.NoError(t, err)
		var rec piForkHelperRecord
		require.NoError(t, json.Unmarshal(rawRec, &rec))
		sessionIdx := slices.Index(rec.Args, "--session")
		require.GreaterOrEqual(t, sessionIdx, 0)
		require.Greater(t, len(rec.Args), sessionIdx+1)
		assert.Equal(t, filepath.Join(customSessionsRoot, manglePiPath(targetDir)), filepath.Dir(rec.Args[sessionIdx+1]))
	})
}
