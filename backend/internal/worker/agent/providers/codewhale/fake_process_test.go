//go:build unix

package codewhale

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// The fake runtime PROCESS: the test binary, re-executed in place of
// `codewhale app-server --http`. It answers the routes a start reads, keeps its
// threads in the store the worker points it at, and streams the events of each
// turn it starts. A start test runs the real launch against it: the locator, the
// port reservation, the listening line, the health poll and the thread routes.

const (
	// fakeProcessWantEnv confirms the re-exec.
	fakeProcessWantEnv = "LEAPMUX_TEST_CODEWHALE_RUNTIME"
	// fakeProcessScenarioEnv selects a behavior:
	//   - "" serves.
	//   - "bind-once" fails the first bind.
	//   - "bind-always" fails every bind.
	//   - "exit" fails every start.
	//   - "busy-turn" serves threads whose snapshot states a turn that still runs.
	fakeProcessScenarioEnv = "LEAPMUX_TEST_CODEWHALE_SCENARIO"
	// fakeRecoveredTurnID is the running turn that "busy-turn" states.
	fakeRecoveredTurnID = "turn_recovered"
	// fakeProcessRecordEnv is a file the process appends one launch record to.
	fakeProcessRecordEnv = "LEAPMUX_TEST_CODEWHALE_RECORD"
)

// fakeLaunchRecord is what one launch of the fake process saw.
type fakeLaunchRecord struct {
	Args        []string `json:"args"`
	TasksDir    string   `json:"tasks_dir"`
	RuntimeDir  string   `json:"runtime_dir"`
	HasToken    bool     `json:"has_token"`
	WorkerFlag  string   `json:"worker_flag"`
	SandboxFlag string   `json:"sandbox_flag"`
}

// installFakeCodewhale puts the fake process on PATH as `codewhale`. Not
// parallel: it sets PATH for the whole test process.
func installFakeCodewhale(t *testing.T, scenario string) (record string) {
	t.Helper()
	record = filepath.Join(t.TempDir(), "launches.jsonl")
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary:      codewhaleBinaryName,
		HelperRun:   "TestHelperCodewhaleRuntime",
		WantEnv:     fakeProcessWantEnv,
		Env:         []string{fakeProcessScenarioEnv + "=" + scenario, fakeProcessRecordEnv + "=" + record},
		ForwardArgs: true,
	})
	clearCodewhaleLaunchCache()
	t.Cleanup(clearCodewhaleLaunchCache)
	return record
}

// readLaunchRecords reads every launch the fake process recorded.
func readLaunchRecords(t *testing.T, path string) []fakeLaunchRecord {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var records []fakeLaunchRecord
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var record fakeLaunchRecord
		if json.Unmarshal([]byte(line), &record) == nil {
			records = append(records, record)
		}
	}
	return records
}

// TestHelperCodewhaleRuntime is the fake process. It returns at once in the test
// process itself.
func TestHelperCodewhaleRuntime(t *testing.T) {
	if os.Getenv(fakeProcessWantEnv) != "1" {
		return
	}
	os.Exit(runFakeRuntimeProcess(forwardedArgs(os.Args)))
}

// forwardedArgs is the argv the launcher forwarded after `--`.
func forwardedArgs(args []string) []string {
	for i, arg := range args {
		if arg == "--" {
			return args[i+1:]
		}
	}
	return nil
}

func runFakeRuntimeProcess(args []string) int {
	record := fakeLaunchRecord{
		Args:        args,
		TasksDir:    os.Getenv(envTasksDir),
		RuntimeDir:  os.Getenv(envRuntimeDir),
		HasToken:    os.Getenv(envRuntimeToken) != "",
		WorkerFlag:  os.Getenv("LEAPMUX_WORKER"),
		SandboxFlag: os.Getenv("CODEWHALE_SANDBOX"),
	}
	if path := os.Getenv(fakeProcessRecordEnv); path != "" {
		if encoded, err := json.Marshal(record); err == nil {
			if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
				_, _ = f.Write(append(encoded, '\n'))
				_ = f.Close()
			}
		}
	}
	port := ""
	for i, arg := range args {
		if arg == "--port" && i+1 < len(args) {
			port = args[i+1]
		}
	}
	switch os.Getenv(fakeProcessScenarioEnv) {
	case "exit":
		fmt.Fprintln(os.Stderr, "Error: the fake runtime refused to start")
		return 2
	case "bind-once":
		marker := os.Getenv(fakeProcessRecordEnv) + ".bound"
		if _, err := os.Stat(marker); err != nil {
			_ = os.WriteFile(marker, nil, 0o600)
			fmt.Fprintf(os.Stderr, "Error: Failed to bind 127.0.0.1:%s: Address already in use (os error 48)\n", port)
			return 1
		}
	case "bind-always":
		fmt.Fprintf(os.Stderr, "Error: Failed to bind 127.0.0.1:%s: Address already in use (os error 48)\n", port)
		return 1
	}
	listener, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Failed to bind 127.0.0.1:%s: %v\n", port, err)
		return 1
	}
	server := newFakeProcessServer(record.RuntimeDir, os.Getenv(envRuntimeToken))
	httpServer := &http.Server{Handler: server, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = httpServer.Serve(listener) }()
	fmt.Printf("Runtime API listening on http://%s\n", listener.Addr())

	// The runtime ends on a signal, and a stream it holds open ends with it.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	<-signals
	return 0
}

// fakeProcessServer is the runtime API the fake process serves.
type fakeProcessServer struct {
	runtimeDir string
	token      string

	mu      sync.Mutex
	seq     uint64
	streams map[chan []byte]string
	// log keeps every event, so a stream that connects late replays what it
	// missed, as the runtime's `since_seq` does.
	log []fakeLoggedEvent
}

type fakeLoggedEvent struct {
	threadID string
	seq      uint64
	data     []byte
}

func newFakeProcessServer(runtimeDir, token string) *fakeProcessServer {
	return &fakeProcessServer{runtimeDir: runtimeDir, token: token, streams: make(map[chan []byte]string)}
}

func (s *fakeProcessServer) threadFile(id string) string {
	return filepath.Join(s.runtimeDir, "threads", id+".json")
}

func (s *fakeProcessServer) save(thread threadRecord) {
	_ = os.MkdirAll(filepath.Dir(s.threadFile(thread.ID)), 0o700)
	encoded, _ := json.Marshal(thread)
	_ = os.WriteFile(s.threadFile(thread.ID), encoded, 0o600)
	_ = os.WriteFile(filepath.Join(s.runtimeDir, "state.json"), []byte(`{"schema_version":1}`), 0o600)
}

func (s *fakeProcessServer) load(id string) (threadRecord, bool) {
	data, err := os.ReadFile(s.threadFile(id))
	if err != nil {
		return threadRecord{}, false
	}
	var thread threadRecord
	return thread, json.Unmarshal(data, &thread) == nil
}

func (s *fakeProcessServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == routeHealth {
		writeFakeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+s.token {
		writeFakeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"message": "unauthorized"}})
		return
	}
	path := r.URL.Path
	switch {
	case path == routeRuntimeInfo:
		writeFakeJSON(w, http.StatusOK, map[string]any{"codewhale_version": "0.9.13"})
	case path == routeThreads && r.Method == http.MethodPost:
		s.createThread(w, r)
	case strings.HasPrefix(path, routeProviders+"/"):
		writeFakeJSON(w, http.StatusOK, map[string]any{"models": []map[string]any{
			{"id": "deepseek-flash", "image_input": "unsupported", "reasoning_effort": "supported", "reasoning_effort_levels": []string{"low", "high"}},
			{"id": "deepseek-pro", "image_input": "supported", "reasoning_effort": "unknown"},
		}})
	case strings.HasPrefix(path, routeThreads+"/"):
		s.threadRoute(w, r, strings.TrimPrefix(path, routeThreads+"/"))
	default:
		writeFakeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"message": "not found"}})
	}
}

func (s *fakeProcessServer) createThread(w http.ResponseWriter, r *http.Request) {
	var request createThreadRequest
	_ = json.NewDecoder(r.Body).Decode(&request)
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	thread := threadRecord{
		ID: "thr_" + hex.EncodeToString(suffix), Workspace: request.Workspace,
		Model: "deepseek-flash", ModelProvider: "deepseek", ModelProviderID: "deepseek",
		Mode: contractsModeAgent, PermissionPosture: "ask",
		CreatedAt: time.Now().UTC().Format(time.RFC3339), UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if request.Model != "" {
		thread.Model = request.Model
	}
	if request.Mode != "" {
		thread.Mode = request.Mode
	}
	if request.PermissionPosture != "" {
		thread.PermissionPosture = request.PermissionPosture
	}
	s.save(thread)
	writeFakeJSON(w, http.StatusCreated, thread)
}

// contractsModeAgent is the default mode of a new thread.
const contractsModeAgent = "agent"

func (s *fakeProcessServer) threadRoute(w http.ResponseWriter, r *http.Request, rest string) {
	id, sub, _ := strings.Cut(rest, "/")
	thread, ok := s.load(id)
	if !ok {
		writeFakeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"message": "thread not found"}})
		return
	}
	switch {
	case sub == "" && r.Method == http.MethodGet:
		turns := []any{}
		if os.Getenv(fakeProcessScenarioEnv) == "busy-turn" {
			// A dead worker left this turn running, and the runtime recovered it.
			turns = append(turns, map[string]any{"id": fakeRecoveredTurnID, "status": "in_progress"})
		}
		writeFakeJSON(w, http.StatusOK, map[string]any{"thread": thread, "turns": turns, "latest_seq": 0})
	case sub == "" && r.Method == http.MethodPatch:
		var update updateThreadRequest
		_ = json.NewDecoder(r.Body).Decode(&update)
		if update.Model != nil {
			thread.Model = *update.Model
		}
		if update.Mode != nil {
			thread.Mode = *update.Mode
		}
		if update.PermissionPosture != nil {
			thread.PermissionPosture = *update.PermissionPosture
		}
		s.save(thread)
		writeFakeJSON(w, http.StatusOK, thread)
	case sub == "resume" && r.Method == http.MethodPost:
		writeFakeJSON(w, http.StatusOK, thread)
	case sub == "events" && r.Method == http.MethodGet:
		s.stream(w, r)
	case sub == "turns" && r.Method == http.MethodPost:
		s.startTurn(w, thread)
	default:
		writeFakeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"message": "not found"}})
	}
}

func (s *fakeProcessServer) startTurn(w http.ResponseWriter, thread threadRecord) {
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	turnID := "turn_" + hex.EncodeToString(suffix)
	thread.LatestTurnID = turnID
	s.save(thread)
	writeFakeJSON(w, http.StatusCreated, map[string]any{"thread": thread, "turn": map[string]any{"id": turnID, "status": "queued"}})
	go func() {
		s.broadcast(thread.ID, "turn.started", turnID, "", map[string]any{"turn": map[string]any{"id": turnID, "status": "in_progress"}})
		s.broadcast(thread.ID, "item.completed", turnID, "item_1", map[string]any{"item": map[string]any{"id": "item_1", "kind": "agent_message", "status": "completed", "detail": "Hello from the fake runtime."}})
		s.broadcast(thread.ID, "turn.completed", turnID, "", map[string]any{"turn": map[string]any{"id": turnID, "status": "completed"}})
	}()
}

func (s *fakeProcessServer) broadcast(threadID, name, turnID, itemID string, payload any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	encoded, _ := json.Marshal(map[string]any{
		"schema_version": 1, "seq": s.seq, "event": name, "kind": name,
		"thread_id": threadID, "turn_id": turnID, "item_id": itemID, "payload": payload,
	})
	s.log = append(s.log, fakeLoggedEvent{threadID: threadID, seq: s.seq, data: encoded})
	for stream, streamThread := range s.streams {
		if streamThread == threadID {
			stream <- encoded
		}
	}
}

func (s *fakeProcessServer) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}
	threadID, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, routeThreads+"/"), "/")
	var since uint64
	_, _ = fmt.Sscan(r.URL.Query().Get(eventsQuerySinceSeq), &since)
	events := make(chan []byte, 256)
	// Subscribe and read the backlog under one lock, so no event falls between.
	s.mu.Lock()
	s.streams[events] = threadID
	for _, logged := range s.log {
		if logged.threadID == threadID && logged.seq > since {
			events <- logged.data
		}
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.streams, events)
		s.mu.Unlock()
	}()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case data := <-events:
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}
