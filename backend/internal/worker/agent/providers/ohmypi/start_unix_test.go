//go:build unix

package ohmypi

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The start tests run a fake `omp` on PATH. It re-runs this test binary as
// TestHelperProcessOmp, which speaks omp's RPC mode on stdin and stdout.
//
// InstallFakeCLI sets PATH with t.Setenv, which the testing package refuses in
// a parallel test, so these tests run serially.
const (
	ompHelperWantEnv     = "GO_WANT_HELPER_PROCESS_OMP"
	ompHelperScenarioEnv = "LEAPMUX_OMP_TEST_SCENARIO"
	ompHelperRecordEnv   = "LEAPMUX_OMP_TEST_RECORD"
)

// Helper scenarios.
const (
	// ompScenarioServe answers every command, and a prompt with a short run.
	ompScenarioServe = "serve"
	// ompScenarioExitBeforeReady exits before the ready frame, as omp does for a
	// model it cannot find.
	ompScenarioExitBeforeReady = "exit-before-ready"
	// ompScenarioGoal reports the resumed session's goal before it answers
	// get_state, and a goal change during the run of a prompt.
	ompScenarioGoal = "goal"
)

// ompTerminalPaneEnv is one of the terminal identity variables that the worker
// strips; see terminalIdentityEnvKeys.
const ompTerminalPaneEnv = "TMUX_PANE"

// The fake's session, as its get_state states it.
const (
	fakeOmpSessionID   = "01a0cf77-9ae4-72d8-9a42-665c431d3beb"
	fakeOmpSessionFile = "/sessions/2026-09-23T18-11-57-284Z_01a0cf77-9ae4-72d8-9a42-665c431d3beb.jsonl"
)

// ompHelperRecord is what the fake states about how it was started.
type ompHelperRecord struct {
	Args            []string `json:"args"`
	Dir             string   `json:"dir"`
	HasTerminalPane bool     `json:"hasTerminalPane"`
}

// TestHelperProcessOmp is the fake `omp`. It returns at once in a normal test
// run.
func TestHelperProcessOmp(*testing.T) {
	if os.Getenv(ompHelperWantEnv) != "1" {
		return
	}
	args := os.Args
	if index := slices.Index(args, "--"); index >= 0 {
		args = args[index+1:]
	}
	os.Exit(runFakeOmp(os.Getenv(ompHelperScenarioEnv), os.Getenv(ompHelperRecordEnv), args))
}

func runFakeOmp(scenario, recordDir string, args []string) int {
	dir, _ := os.Getwd()
	_, hasPane := os.LookupEnv(ompTerminalPaneEnv)
	raw, _ := json.Marshal(ompHelperRecord{Args: args, Dir: dir, HasTerminalPane: hasPane})
	_ = os.WriteFile(filepath.Join(recordDir, "record.json"), raw, 0o600)
	if scenario == ompScenarioExitBeforeReady {
		_, _ = fmt.Fprintln(os.Stderr, "Error: Model not found: mock/missing")
		return 1
	}

	out := bufio.NewWriter(os.Stdout)
	write := func(frame string) {
		_, _ = out.WriteString(frame + "\n")
		_ = out.Flush()
	}
	write(frameReady)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for scanner.Scan() {
		var command struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		}
		if json.Unmarshal(scanner.Bytes(), &command) != nil || command.ID == "" {
			continue
		}
		for _, frame := range fakeOmpReply(scenario, command.ID, command.Type) {
			write(frame)
		}
	}
	// omp exits when stdin closes.
	return 0
}

// fakeOmpReply returns the frames that the fake writes for one command, in
// order.
func fakeOmpReply(scenario, id, command string) []string {
	response := func(data string) string {
		frame := map[string]any{"type": "response", "id": id, "command": command, "success": true}
		if data != "" {
			frame["data"] = json.RawMessage(data)
		}
		encoded, _ := json.Marshal(frame)
		return string(encoded)
	}
	switch command {
	case CommandGetState:
		state := response(`{"model":{"id":"mock-model","provider":"mock"},"thinkingLevel":"medium",` +
			`"sessionId":"` + fakeOmpSessionID + `","sessionFile":"` + fakeOmpSessionFile + `"}`)
		if scenario == ompScenarioGoal {
			return []string{`{"type":"goal_updated","goal":{"id":"g1","objective":"Ship the release","status":"active","createdAt":1790187118735}}`, state}
		}
		return []string{state}
	case CommandGetAvailableModels:
		return []string{response(availableModels)}
	case CommandPrompt:
		frames := []string{response(""), `{"type":"agent_start"}`,
			`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Hello from the fake."}],"stopReason":"stop"}}`}
		if scenario == ompScenarioGoal {
			frames = append(frames, `{"type":"goal_updated","goal":{"id":"g1","objective":"Ship the release","status":"complete","createdAt":1790187118735}}`)
		}
		return append(frames, `{"type":"agent_end","isTerminal":true,"messages":[{"role":"assistant","stopReason":"stop"}]}`)
	default:
		return []string{response("")}
	}
}

// installFakeOmp puts the fake `omp` on PATH and returns the directory the fake
// writes its record to.
func installFakeOmp(t *testing.T, scenario string) string {
	t.Helper()
	recordDir := t.TempDir()
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary:      "omp",
		HelperRun:   "TestHelperProcessOmp",
		WantEnv:     ompHelperWantEnv,
		Env:         []string{ompHelperScenarioEnv + "=" + scenario, ompHelperRecordEnv + "=" + recordDir},
		ForwardArgs: true,
	})
	return recordDir
}

func readOmpRecord(t *testing.T, recordDir string) ompHelperRecord {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(recordDir, "record.json"))
	require.NoError(t, err)
	var record ompHelperRecord
	require.NoError(t, json.Unmarshal(raw, &record))
	return record
}

func ompStartOptions(t *testing.T) agent.Options {
	t.Helper()
	return agent.Options{
		AgentID:        "omp-start",
		WorkingDir:     t.TempDir(),
		HomeDir:        t.TempDir(),
		Shell:          testutil.TestShell(),
		AgentProvider:  leapmuxv1.AgentProvider_AGENT_PROVIDER_OH_MY_PI,
		APITimeout:     30 * time.Second,
		StartupTimeout: 60 * time.Second,
	}
}

func startFakeOmp(t *testing.T, opts agent.Options) (*Agent, *agenttest.Sink) {
	t.Helper()
	sink := &agenttest.Sink{}
	started, err := Start(t.Context(), opts, agent.NewProviderServices(sink))
	require.NoError(t, err)
	a, ok := started.(*Agent)
	require.True(t, ok)
	t.Cleanup(func() {
		a.Stop()
		_ = a.Wait()
	})
	return a, sink
}

func TestStartRunsTheHandshake(t *testing.T) {
	recordDir := installFakeOmp(t, ompScenarioServe)
	// A worker that runs in a terminal carries its terminal's identity. omp would
	// record the LeapMux session as that terminal's last session.
	t.Setenv(ompTerminalPaneEnv, "%7")
	opts := ompStartOptions(t)

	a, sink := startFakeOmp(t, opts)

	record := readOmpRecord(t, recordDir)
	assert.Equal(t, []string{"--mode", "rpc-ui", "--cwd", opts.WorkingDir, "--approval-mode", "write"}, record.Args)
	assert.False(t, record.HasTerminalPane, "omp never sees the terminal identity of the worker")
	expectedDir, err := filepath.EvalSymlinks(opts.WorkingDir)
	require.NoError(t, err)
	actualDir, err := filepath.EvalSymlinks(record.Dir)
	require.NoError(t, err)
	assert.Equal(t, expectedDir, actualDir)

	assert.Equal(t, fakeOmpSessionFile, sink.LastSessionID(), "the handle is the session file")
	assert.Equal(t, []string{fakeOmpSessionFile}, sink.StatusActives())
	assert.False(t, a.startupSnapshot.Load(), "a goal reported after the start is news")
	groups := a.OptionGroups()
	assert.Equal(t, "mock/mock-model", groupByID(groups, agent.OptionIDModel).GetCurrentValue())
	assert.Equal(t, []string{"mock/mock-model-2", "mock/mock-model"}, optionIDs(groupByID(groups, agent.OptionIDModel)), "the catalog loaded")
	assert.Equal(t, agent.EffortAuto, groupByID(groups, agent.OptionIDEffort).GetCurrentValue(),
		"the reader chose omp's own level, whatever level omp resolved")

	require.NoError(t, a.SendInput("hello", nil))
	waitFor(t, func() bool { return len(turnEndRows(sink.Messages())) == 1 })
	assert.Equal(t, []string{"message_end", "agent_end"}, persistedTypes(sink.Messages()), "the reply, then the turn's end")
	active, _ := sink.LastTurnActive()
	assert.False(t, active)

	a.Stop()
	select {
	case <-a.ProcessDone():
	case <-time.After(30 * time.Second):
		t.Fatal("omp did not exit when its stdin closed")
	}
}

func TestStartLaunchesWithTheOptionsAndTheResumeHandle(t *testing.T) {
	recordDir := installFakeOmp(t, ompScenarioServe)
	opts := ompStartOptions(t)
	opts.Options = optionmap.Map{
		agent.OptionIDModel:          "mock/mock-model",
		agent.OptionIDEffort:         "high",
		agent.OptionIDPermissionMode: "always-ask",
	}
	file := filepath.Join(opts.HomeDir, ".omp", "agent", "sessions", "-p", "2026-09-23T18-11-57-284Z_01a0cf77.jsonl")
	opts.ResumeSessionID = file

	a, _ := startFakeOmp(t, opts)

	assert.Equal(t, []string{"--mode", "rpc-ui", "--cwd", opts.WorkingDir, "--model", "mock/mock-model", "--thinking", "high",
		"--approval-mode", "always-ask", "--resume", file}, readOmpRecord(t, recordDir).Args)
	groups := a.OptionGroups()
	assert.Equal(t, "medium", groupByID(groups, agent.OptionIDEffort).GetCurrentValue(), "omp states the level it runs")
	assert.Equal(t, "always-ask", groupByID(groups, agent.OptionIDPermissionMode).GetCurrentValue())
}

func TestStartRefusesAnInvalidResumeHandle(t *testing.T) {
	recordDir := installFakeOmp(t, ompScenarioServe)
	opts := ompStartOptions(t)
	opts.ResumeSessionID = "relative/a.jsonl"

	_, err := Start(t.Context(), opts, agent.NewProviderServices(&agenttest.Sink{}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "send /clear", "the failure states how to recover")
	_, statErr := os.Stat(filepath.Join(recordDir, "record.json"))
	assert.ErrorIs(t, statErr, os.ErrNotExist, "omp never starts")
}

func TestStartReportsAnOmpThatExitsBeforeItIsReady(t *testing.T) {
	installFakeOmp(t, ompScenarioExitBeforeReady)
	_, err := Start(t.Context(), ompStartOptions(t), agent.NewProviderServices(&agenttest.Sink{}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ready")
	assert.Contains(t, err.Error(), "Model not found: mock/missing", "the reader learns why omp exited")
}

// A goal that omp reports while the session opens is the resumed session's own
// goal, so it is a snapshot. A goal change during a run is news.
func TestStartReportsTheResumedGoalAsASnapshot(t *testing.T) {
	installFakeOmp(t, ompScenarioGoal)
	a, sink := startFakeOmp(t, ompStartOptions(t))

	goals := sink.Goals()
	require.Len(t, goals, 1)
	assert.True(t, goals[0].Snapshot)
	assert.Equal(t, agent.GoalStatusActive, goals[0].Status)

	require.NoError(t, a.SendInput("finish it", nil))
	waitFor(t, func() bool { return len(sink.Goals()) == 2 })
	assert.False(t, sink.Goals()[1].Snapshot)
	assert.Equal(t, agent.GoalStatusDone, sink.Goals()[1].Status)
}
