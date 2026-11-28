//go:build unix

package amp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	agentapi "github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir/agentdirtest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

const fakeAmpThreadID = "T-00000000-0000-4000-8000-000000000001"

// fakeAmpScript is a fake `amp` for the process tests. It records its
// arguments and its environment, prints an init line before its first reply,
// and answers each stdin line:
//
//   - `threads new` records its arguments and prints the thread URL;
//   - a line that holds "hang" waits until SIGINT, which prints Amp's own
//     cancellation result and exits, as Amp does;
//   - a line that holds "crash" writes an error to stderr and exits 3;
//   - a line that holds "run-a-tool" gets a `shell_command` call of `ls`, and
//     the turn stays open;
//   - any other line gets a text reply that ends the turn.
//
// Stdin EOF prints the process's success result, as Amp does.
const fakeAmpScript = `#!/bin/sh
if [ "$1" = "threads" ] && [ "$2" = "new" ]; then
  printf '%s\n' "$@" > '{{create_args}}'
  echo "https://ampcode.com/threads/` + fakeAmpThreadID + `"
  exit 0
fi
printf '%s\n' "$@" > '{{args}}'
printf '%s\n' "AMP_SKIP_UPDATE_CHECK=$AMP_SKIP_UPDATE_CHECK" "LEAPMUX_AGENT_HELPER=$LEAPMUX_AGENT_HELPER" > '{{env}}'
child=
trap 'if [ -n "$child" ]; then kill "$child" 2>/dev/null; fi; echo "{\"type\":\"result\",\"subtype\":\"error_during_execution\",\"duration_ms\":1,\"is_error\":true,\"num_turns\":0,\"error\":\"User cancelled (SIGINT/SIGTERM)\",\"session_id\":\"` + fakeAmpThreadID + `\"}"; exit 0' INT
sent_init=
while IFS= read -r line; do
  if [ -z "$sent_init" ]; then
    echo '{"type":"system","subtype":"init","cwd":"/work","session_id":"` + fakeAmpThreadID + `","tools":["shell_command"],"mcp_servers":[],"agent_mode":"medium"}'
    sent_init=1
  fi
  case "$line" in
    *hang*)
      sleep 30 >/dev/null 2>&1 &
      child=$!
      wait "$child"
      child=
      ;;
    *crash*)
      echo "Error: boom" >&2
      exit 3
      ;;
    *run-a-tool*)
      echo '{"type":"assistant","message":{"type":"message","role":"assistant","content":[{"type":"tool_use","id":"TU-fake","name":"shell_command","input":{"command":"ls"}}],"stop_reason":"tool_use","usage":{"input_tokens":0,"cache_creation_input_tokens":10,"cache_read_input_tokens":20,"output_tokens":3}},"parent_tool_use_id":null,"session_id":"` + fakeAmpThreadID + `"}'
      ;;
    *)
      echo '{"type":"assistant","message":{"type":"message","role":"assistant","content":[{"type":"text","text":"pong"}],"stop_reason":"end_turn","usage":{"input_tokens":0,"cache_creation_input_tokens":10,"cache_read_input_tokens":20,"output_tokens":3}},"parent_tool_use_id":null,"session_id":"` + fakeAmpThreadID + `"}'
      ;;
  esac
done
echo '{"type":"result","subtype":"success","duration_ms":1,"is_error":false,"num_turns":1,"result":"pong","session_id":"` + fakeAmpThreadID + `"}'
`

// processHarness drives an agent that starts real processes of the fake CLI.
type processHarness struct {
	t              *testing.T
	agent          *Agent
	sink           *agenttest.ControlSink
	createArgsFile string
	argsFile       string
	envFile        string
}

func newProcessHarness(t *testing.T) *processHarness {
	t.Helper()
	return newProcessHarnessWithShell(t, "/bin/sh")
}

// newProcessHarnessWithShell starts the agent's processes through shell, and
// reads the shell's environment as Start does.
func newProcessHarnessWithShell(t *testing.T, shell string) *processHarness {
	t.Helper()
	dir := t.TempDir()
	h := &processHarness{
		t:              t,
		sink:           &agenttest.ControlSink{},
		createArgsFile: filepath.Join(dir, "create-args"),
		argsFile:       filepath.Join(dir, "args"),
		envFile:        filepath.Join(dir, "env"),
	}
	program := filepath.Join(dir, "amp")
	script := strings.NewReplacer("{{create_args}}", h.createArgsFile, "{{args}}", h.argsFile, "{{env}}", h.envFile).Replace(fakeAmpScript)
	testutil.WriteExecutable(t, program, script)

	options := agentapi.Options{AgentID: "agent-proc", WorkingDir: t.TempDir(), Shell: shell, APITimeout: 30 * time.Second}
	agentDir := agentdirtest.NewDir(t, agentDirSpec())
	clock := quartz.NewReal()
	bridge, err := newPermissionBridge(agentDir.Path(), clock)
	require.NoError(t, err)
	h.agent = newAgent(options, agentapi.NewProviderServices(h.sink), launchConfig{
		opts:          options,
		spec:          launch.Spec{Program: program},
		helperEnv:     contracts.EnvAgentHelper + "=" + filepath.Join(agentDir.Path(), helperSpecFileName),
		helperProgram: "/opt/leapmux/leapmux",
		getenv:        envOf(nil),
		home:          t.TempDir(),
		shellEnv:      shellEnvOf(options),
	}, bridge, agentDir, clock)
	t.Cleanup(h.agent.Stop)
	return h
}

func (h *processHarness) awaitTurnEnds(n int) []agenttest.Message {
	h.t.Helper()
	var ends []agenttest.Message
	require.Eventually(h.t, func() bool {
		ends = ends[:0]
		for _, m := range h.sink.Messages() {
			if m.TurnEnd {
				ends = append(ends, m)
			}
		}
		return len(ends) >= n
	}, 30*time.Second, 5*time.Millisecond, "%d turns end", n)
	return ends
}

func (h *processHarness) lines(path string) []string {
	h.t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(h.t, err)
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func (h *processHarness) awaitProcess(running bool) {
	h.t.Helper()
	require.Eventually(h.t, func() bool {
		h.agent.mu.Lock()
		defer h.agent.mu.Unlock()
		return (h.agent.proc != nil) == running
	}, 30*time.Second, 5*time.Millisecond)
}

func TestProcessRunsATurnWithTheLaunchArguments(t *testing.T) {
	t.Parallel()
	h := newProcessHarness(t)
	require.NoError(t, agenttest.SendInput(h.agent, "ping", nil))
	ends := h.awaitTurnEnds(1)
	assert.Equal(t, agentapi.MessageCompletionComplete, ends[0].Completion)
	assert.Equal(t, fakeAmpThreadID, h.sink.LastSessionID(), "the created thread states the session")

	settingsPath := filepath.Join(h.agent.launch.stateDir, settingsFileName)
	assert.Equal(t, []string{"threads", "new", "--mode", agentModeMedium, "--settings-file", settingsPath}, h.lines(h.createArgsFile))
	assert.Equal(t, launchArgs(fakeAmpThreadID, threadConfiguration{mode: agentModeMedium}, settingsPath), h.lines(h.argsFile))
	assert.Equal(t, []string{
		envSkipUpdateCheck + "=1",
		h.agent.launch.helperEnv,
	}, h.lines(h.envFile))
	_, err := os.Stat(settingsPath)
	assert.NoError(t, err, "the settings file exists before Amp starts")
}

func TestFastModeReachesTheNativeLaunch(t *testing.T) {
	t.Parallel()
	h := newProcessHarness(t)
	h.agent.UpdateSettings(map[string]string{"fastMode": "true"})
	require.NoError(t, agenttest.SendInput(h.agent, "ping", nil))
	h.awaitTurnEnds(1)
	assert.Contains(t, h.lines(h.createArgsFile), "--fast")
}

// SIGINT makes Amp print its cancellation and exit. The turn ends as
// interrupted, and the next message continues the thread in a new process.
func TestProcessInterruptEndsTheTurnAndTheNextMessageResumes(t *testing.T) {
	t.Parallel()
	h := newProcessHarness(t)
	require.NoError(t, agenttest.SendInput(h.agent, "hang", nil))
	require.Eventually(t, func() bool {
		h.agent.mu.Lock()
		defer h.agent.mu.Unlock()
		return h.agent.proc != nil && h.agent.proc.sawInit.Load()
	}, 30*time.Second, 5*time.Millisecond)

	require.NoError(t, h.agent.Interrupt(agentapi.StopContext{}))
	ends := h.awaitTurnEnds(1)
	assert.Equal(t, agentapi.MessageCompletionInterrupted, ends[0].Completion)
	h.awaitProcess(false)
	assert.Empty(t, h.sink.Notifications(), "an interrupt is not an error")

	require.NoError(t, agenttest.SendInput(h.agent, "ping", nil))
	ends = h.awaitTurnEnds(2)
	assert.Equal(t, agentapi.MessageCompletionComplete, ends[1].Completion)
	args := h.lines(h.argsFile)
	assert.Equal(t, []string{"threads", "continue", fakeAmpThreadID}, args[:3])
	assert.NotContains(t, args, "--no-archive-after-execute")
}

// Any other unplanned end shows the error, and the thread resumes at once
// because the turn was in progress.
func TestProcessCrashShowsTheErrorAndResumesAtOnce(t *testing.T) {
	t.Parallel()
	h := newProcessHarness(t)
	require.NoError(t, agenttest.SendInput(h.agent, "ping", nil))
	h.awaitTurnEnds(1)

	require.NoError(t, agenttest.SendInput(h.agent, "crash", nil))
	ends := h.awaitTurnEnds(2)
	assert.Equal(t, agentapi.MessageCompletionError, ends[1].Completion)
	assert.Contains(t, string(ends[1].Content), "Error: boom")

	// The resumed process is idle, so it answers the next message.
	require.Eventually(t, func() bool {
		args := h.lines(h.argsFile)
		return len(args) >= 3 && args[0] == "threads" && args[2] == fakeAmpThreadID
	}, 30*time.Second, 5*time.Millisecond, "the thread resumes with no new message")
	h.awaitProcess(true)
	require.NoError(t, agenttest.SendInput(h.agent, "ping again", nil))
	ends = h.awaitTurnEnds(3)
	assert.Equal(t, agentapi.MessageCompletionComplete, ends[2].Completion)
}

// A stop during a turn ends the turn as interrupted, although Amp answers the
// stop's SIGINT with an error result. The wait for the init line makes sure
// that the fake's trap exists, so the fake prints that result as Amp does.
func TestProcessStopEndsTheProcess(t *testing.T) {
	t.Parallel()
	h := newProcessHarness(t)
	require.NoError(t, agenttest.SendInput(h.agent, "hang", nil))
	h.awaitProcess(true)
	require.Eventually(t, func() bool {
		h.agent.mu.Lock()
		defer h.agent.mu.Unlock()
		return h.agent.proc != nil && h.agent.proc.sawInit.Load()
	}, 30*time.Second, 5*time.Millisecond)
	h.agent.mu.Lock()
	proc := h.agent.proc
	h.agent.mu.Unlock()

	h.agent.Stop()
	select {
	case <-proc.handled:
	case <-time.After(30 * time.Second):
		t.Fatal("the process outlived Stop")
	}
	ends := h.awaitTurnEnds(1)
	assert.Equal(t, agentapi.MessageCompletionInterrupted, ends[0].Completion)
}

// The user's Amp settings file is the one that Amp itself finds, which a shell
// profile can move with AMP_SETTINGS_FILE although the worker's own environment
// states none. The generated file then holds the user's rules from that file.
func TestProcessReadsTheUserSettingsThatTheShellStates(t *testing.T) {
	t.Parallel()
	userSettings := filepath.Join(t.TempDir(), "my-amp.json")
	writeFile(t, userSettings, `{"amp.permissions": [{"tool": "Bash", "action": "reject", "matches": {"cmd": "git push*"}}]}`)
	shell := filepath.Join(t.TempDir(), "sh")
	testutil.WriteExecutable(t, shell, "#!/bin/sh\nexport AMP_SETTINGS_FILE='"+userSettings+"'\nexec /bin/sh \"$@\"\n")
	h := newProcessHarnessWithShell(t, shell)

	require.NoError(t, agenttest.SendInput(h.agent, "ping", nil))
	h.awaitTurnEnds(1)
	var generated map[string]json.RawMessage
	data, err := os.ReadFile(filepath.Join(h.agent.launch.stateDir, settingsFileName))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &generated))
	assert.JSONEq(t, `[
		{"tool": "Bash", "action": "reject", "matches": {"cmd": "git push*"}},
		{"tool": "*", "action": "delegate", "to": "/opt/leapmux/leapmux"}
	]`, string(generated[settingPermissions]))
}

// generatedRules reads the permission rules of the agent's settings file.
func (h *processHarness) generatedRules() string {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(h.agent.launch.stateDir, settingsFileName))
	require.NoError(h.t, err)
	var generated map[string]json.RawMessage
	require.NoError(h.t, json.Unmarshal(data, &generated))
	return string(generated[settingPermissions])
}

// delegateToHelper is the generated rule list when the user states no rule.
const delegateToHelper = `[{"tool":"*","action":"delegate","to":"/opt/leapmux/leapmux"}]`

// The generated file delegates every call to the helper in BOTH modes, and the
// helper asks the agent, which decides from its current mode. A file that
// allowed every call in Allow All would keep a switch to Ask from applying until
// Amp read the file again.
func TestProcessSettingsFileDelegatesInBothModes(t *testing.T) {
	t.Parallel()
	for _, mode := range permissionModes {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			h := newProcessHarness(t)
			h.agent.UpdateSettings(map[string]string{agentapi.OptionIDPermissionMode: mode})
			require.NoError(t, agenttest.SendInput(h.agent, "ping", nil))
			h.awaitTurnEnds(1)
			assert.JSONEq(t, delegateToHelper, h.generatedRules())
		})
	}
}

// A switch from Allow All to Ask applies to the very next permission request.
// The agent rewrites no file, and nothing waits for Amp to read one: Amp runs
// the helper for every call in both modes, and the helper asks the agent.
func TestProcessSwitchToAskAsksAtTheVeryNextRequest(t *testing.T) {
	t.Parallel()
	h := newProcessHarness(t)
	h.agent.UpdateSettings(map[string]string{agentapi.OptionIDPermissionMode: contracts.AmpPermissionModeAllowAll})
	require.NoError(t, agenttest.SendInput(h.agent, "run-a-tool", nil))
	require.Eventually(t, func() bool {
		h.agent.mu.Lock()
		defer h.agent.mu.Unlock()
		return h.agent.tools["TU-fake"] != nil
	}, 30*time.Second, 5*time.Millisecond, "the agent reads the call")
	settingsPath := filepath.Join(h.agent.launch.stateDir, settingsFileName)
	before, err := os.Stat(settingsPath)
	require.NoError(t, err)

	config := bridgeHelperConfig(t, h.agent.bridge)
	env := map[string]string{envToolName: "shell_command", envThreadID: fakeAmpThreadID}
	allowed := runHelperWith(t, config, env, strings.NewReader(`{"command":"ls"}`))
	assert.Equal(t, helperExitAllow, allowed.exitCode(t), "Allow All allows the call")
	assert.Zero(t, h.sink.PublishedControlCount(), "Allow All shows no banner")

	h.agent.UpdateSettings(map[string]string{agentapi.OptionIDPermissionMode: contracts.AmpPermissionModeAsk})
	after, err := os.Stat(settingsPath)
	require.NoError(t, err)
	assert.True(t, os.SameFile(before, after), "the switch rewrites no settings file")
	assert.JSONEq(t, delegateToHelper, h.generatedRules())

	asked := runHelperWith(t, config, env, strings.NewReader(`{"command":"ls"}`))
	require.Eventually(t, func() bool { return h.sink.PublishedControlCount() == 1 },
		30*time.Second, 2*time.Millisecond, "the very next request shows a banner")
	request := h.sink.LastPublishedControl()
	assert.Equal(t, "TU-fake", permissionPayload(t, request.Payload).ToolUseID, "the banner is for the call")
	asked.assertStillWaits(t)
	require.NoError(t, h.agent.SendRawInput(h.sink.Answer(request.RequestID, controlAnswer(t, request.RequestID, contracts.ControlAnswerBehaviorDeny, ""))))
	assert.Equal(t, helperExitReject, asked.exitCode(t))
}
