package amp

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir/agentdirtest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// harness holds one agent under test: a recording sink, a real permission
// bridge, a mock clock, and a starter of fake processes that the test drives.
type harness struct {
	t     *testing.T
	agent *Agent
	sink  *agenttest.ControlSink
	// events records the order of the banners that the agent publishes and
	// withdraws, on top of sink.
	events *eventSink
	clock  *quartz.Mock

	mu     sync.Mutex
	procs  []*fakeProc
	starts []string
	// startErr, when set, is what the next start returns.
	startErr error
	started  chan *fakeProc
}

type harnessOption func(*agent.Options)

func withResume(threadID string) harnessOption {
	return func(o *agent.Options) { o.ResumeSessionID = threadID }
}

func withOptions(options optionmap.Map) harnessOption {
	return func(o *agent.Options) { o.Options = options }
}

func withWorkingDir(dir string) harnessOption {
	return func(o *agent.Options) { o.WorkingDir = dir }
}

func newHarness(t *testing.T, opts ...harnessOption) *harness {
	t.Helper()
	options := agent.Options{
		AgentID:    "agent-1",
		WorkingDir: t.TempDir(),
		APITimeout: 10 * time.Second,
	}
	for _, opt := range opts {
		opt(&options)
	}
	dir := agentdirtest.NewDir(t, agentDirSpec())
	bridge, err := newPermissionBridge(dir.Path())
	require.NoError(t, err)
	sink := &agenttest.ControlSink{}
	events := &eventSink{ControlSink: sink}
	clock := quartz.NewMock(t)
	h := &harness{t: t, sink: sink, events: events, clock: clock, started: make(chan *fakeProc, 16)}
	h.agent = newAgent(options, agent.NewProviderServices(events), launchConfig{
		opts:          options,
		spec:          launch.Spec{Program: "amp"},
		helperProgram: "/opt/leapmux/leapmux",
		getenv:        func(string) string { return "" },
		home:          t.TempDir(),
	}, bridge, dir, clock)
	h.agent.startFn = h.start
	t.Cleanup(h.agent.Stop)
	return h
}

// eventSink records each banner publication and withdrawal in the order that
// they reach the sink, and it runs a hook ahead of the next publication. A test
// lands an event through the hook inside the window between a request's
// registration and its banner.
type eventSink struct {
	*agenttest.ControlSink

	mu            sync.Mutex
	events        []string
	beforePublish func()
}

// runBeforeNextPublish makes hook run once, ahead of the next publication.
func (s *eventSink) runBeforeNextPublish(hook func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.beforePublish = hook
}

func (s *eventSink) PublishControlRequest(request agent.ControlRequest) error {
	s.mu.Lock()
	hook := s.beforePublish
	s.beforePublish = nil
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
	err := s.ControlSink.PublishControlRequest(request)
	if err == nil {
		s.record("publish " + request.RequestID)
	}
	return err
}

func (s *eventSink) CancelControlRequest(requestID string) {
	s.ControlSink.CancelControlRequest(requestID)
	s.record("cancel " + requestID)
}

func (s *eventSink) record(event string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

// log returns every event so far, in order.
func (s *eventSink) log() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.events...)
}

// start is the agent's process starter: it hands the agent a fake process.
func (h *harness) start(threadID, mode string) (*ampProcess, error) {
	h.mu.Lock()
	err := h.startErr
	h.startErr = nil
	h.starts = append(h.starts, threadID)
	h.mu.Unlock()
	if err != nil {
		return nil, err
	}
	fp := newFakeProc(h.agent.agentID, threadID != "")
	if threadID == "" {
		fp.mode = mode
	}
	if err := h.agent.adopt(fp.ampProcess); err != nil {
		return nil, err
	}
	h.mu.Lock()
	h.procs = append(h.procs, fp)
	h.mu.Unlock()
	h.started <- fp
	return fp.ampProcess, nil
}

// startedThreads returns the thread id each start received, "" for a new thread.
func (h *harness) startedThreads() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.starts...)
}

// nextProc returns the next process the agent started.
func (h *harness) nextProc() *fakeProc {
	h.t.Helper()
	select {
	case fp := <-h.started:
		return fp
	case <-time.After(30 * time.Second):
		h.t.Fatal("the agent started no process")
		return nil
	}
}

// drainStarted forgets every process that the agent started so far, so that a
// later read of h.started sees only the processes that start after this call.
func (h *harness) drainStarted() {
	for {
		select {
		case <-h.started:
		default:
			return
		}
	}
}

// send sends a message and returns the process it went to.
func (h *harness) send(text string) *fakeProc {
	h.t.Helper()
	require.NoError(h.t, h.agent.SendInput(text, nil))
	h.mu.Lock()
	defer h.mu.Unlock()
	require.NotEmpty(h.t, h.procs)
	return h.procs[len(h.procs)-1]
}

// feed hands one stdout line of fp to the agent, as fp's reader would.
func (h *harness) feed(fp *fakeProc, line string) {
	h.t.Helper()
	var proc *ampProcess
	if fp != nil {
		proc = fp.ampProcess
	}
	h.agent.handleLine(proc, providerkit.ParseLine([]byte(line)))
}

// feedFixture hands every line of a probe transcript to the agent.
func (h *harness) feedFixture(fp *fakeProc, name string) {
	h.t.Helper()
	for _, line := range fixtureLines(h.t, name) {
		h.feed(fp, line)
	}
}

func fixtureLines(t *testing.T, name string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// turnEnds returns the rows PersistTurnEnd recorded.
func (h *harness) turnEnds() []agenttest.Message {
	var out []agenttest.Message
	for _, m := range h.sink.Messages() {
		if m.TurnEnd {
			out = append(out, m)
		}
	}
	return out
}

// rows returns the rows that are not turn ends.
func (h *harness) rows() []agenttest.Message {
	var out []agenttest.Message
	for _, m := range h.sink.Messages() {
		if !m.TurnEnd {
			out = append(out, m)
		}
	}
	return out
}

func (h *harness) turnActive() bool {
	h.agent.mu.Lock()
	defer h.agent.mu.Unlock()
	return h.agent.turn.active
}

// fakeProc is a process with a recorded stdin and an exit the test causes.
type fakeProc struct {
	*ampProcess
	stdin *lineRecorder
	done  chan struct{}
	once  sync.Once
}

func newFakeProc(agentID string, resumed bool) *fakeProc {
	fp := &fakeProc{done: make(chan struct{})}
	fp.stdin = &lineRecorder{onClose: fp.exit}
	stderrDone := make(chan struct{})
	close(stderrDone)
	ctx, cancel := context.WithCancel(context.Background())
	fp.ampProcess = &ampProcess{
		Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID:      agentID,
			ProviderName: "amp",
			Stdin:        fp.stdin,
			Ctx:          ctx,
			Cancel: func() {
				cancel()
				fp.exit()
			},
			ProcessDone: fp.done,
			StderrDone:  stderrDone,
		}),
		resumed: resumed,
		handled: make(chan struct{}),
	}
	return fp
}

// exit ends the process, as a real exit does.
func (fp *fakeProc) exit() { fp.once.Do(func() { close(fp.done) }) }

// awaitHandled waits until the agent handled fp's exit.
func (fp *fakeProc) awaitHandled(t *testing.T) {
	t.Helper()
	select {
	case <-fp.handled:
	case <-time.After(30 * time.Second):
		t.Fatal("the agent did not handle the exit")
	}
}

// lines returns the JSON lines the agent wrote to fp's stdin.
func (fp *fakeProc) lines() []map[string]any {
	var out []map[string]any
	for _, line := range fp.stdin.lines() {
		var decoded map[string]any
		if json.Unmarshal([]byte(line), &decoded) == nil {
			out = append(out, decoded)
		}
	}
	return out
}

// lineRecorder is a stdin that records every line.
type lineRecorder struct {
	mu      sync.Mutex
	buf     strings.Builder
	closed  bool
	onClose func()
}

func (r *lineRecorder) Write(data []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, os.ErrClosed
	}
	return r.buf.Write(data)
}

func (r *lineRecorder) Close() error {
	r.mu.Lock()
	already := r.closed
	r.closed = true
	r.mu.Unlock()
	if !already && r.onClose != nil {
		r.onClose()
	}
	return nil
}

func (r *lineRecorder) lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	scanner := bufio.NewScanner(strings.NewReader(r.buf.String()))
	scanner.Buffer(make([]byte, 0, 64<<10), 64<<20)
	for scanner.Scan() {
		out = append(out, scanner.Text())
	}
	return out
}

// Lines of Amp's stream for the tests. Each one is the shape the probes
// captured.

func initLine(threadID string) string {
	return `{"type":"system","subtype":"init","cwd":"/work","session_id":"` + threadID + `","tools":["shell_command"],"mcp_servers":[],"agent_mode":"medium"}`
}

func textLine(text, stopReason string) string {
	return assistantLine(`[{"type":"text","text":`+jsonString(text)+`}]`, stopReason)
}

func assistantLine(content, stopReason string) string {
	stop := "null"
	if stopReason != "" {
		stop = `"` + stopReason + `"`
	}
	return `{"type":"assistant","message":{"type":"message","role":"assistant","content":` + content +
		`,"stop_reason":` + stop + `,"usage":{"input_tokens":0,"cache_creation_input_tokens":100,"cache_read_input_tokens":200,"output_tokens":7,"service_tier":"standard"}},"parent_tool_use_id":null,"session_id":"T-1"}`
}

func toolUseBlock(id, name, input string) string {
	return `{"type":"tool_use","id":"` + id + `","name":"` + name + `","input":` + input + `}`
}

func toolResultLine(id, content string, isError bool) string {
	errorText := "false"
	if isError {
		errorText = "true"
	}
	return `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":` + jsonString(content) + `,"is_error":` + errorText + `}]},"parent_tool_use_id":null,"session_id":"T-1"}`
}

func userEchoLine(text string) string {
	return `{"type":"user","message":{"role":"user","content":[{"type":"text","text":` + jsonString(text) + `}]},"parent_tool_use_id":null,"session_id":"T-1"}`
}

func errorResult(message string) string {
	return `{"type":"result","subtype":"error_during_execution","duration_ms":5,"is_error":true,"num_turns":0,"error":` + jsonString(message) + `,"session_id":"T-1"}`
}

func jsonString(text string) string {
	encoded, err := json.Marshal(text)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// decodeRow decodes one persisted row.
func decodeRow(t *testing.T, content []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(content, &decoded))
	return decoded
}

// rowBlocks returns the content blocks of one assistant or user row.
func rowBlocks(t *testing.T, content []byte) []any {
	t.Helper()
	message, ok := decodeRow(t, content)["message"].(map[string]any)
	require.True(t, ok, "the row carries a message")
	blocks, ok := message["content"].([]any)
	require.True(t, ok, "the message carries a content list")
	return blocks
}

// permissionPayload decodes one published permission request.
func permissionPayload(t *testing.T, payload []byte) contracts.AmpPermissionRequest {
	t.Helper()
	var request contracts.AmpPermissionRequest
	require.NoError(t, json.Unmarshal(payload, &request))
	return request
}

// shortTempDir creates a private directory for a test and removes it when the
// test ends. t.TempDir() holds the test's name, and a socket path in it can pass
// the platform's limit of about 104 bytes.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "amp")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// newTestBridge opens a bridge for a test and closes it when the test ends.
func newTestBridge(t *testing.T) *permissionBridge {
	t.Helper()
	bridge, err := newPermissionBridge(shortTempDir(t))
	require.NoError(t, err)
	t.Cleanup(func() { bridge.close(errAgentStopped) })
	return bridge
}
