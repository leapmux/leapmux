//go:build unix

package mimo

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The start tests run a fake `mimo` on PATH. It re-runs this test binary as
// TestHelperProcessMiMoServe, which serves the same fake server the unit tests
// use on a real loopback port, and prints MiMo's listen line.
const (
	helperWantEnv     = "GO_WANT_HELPER_PROCESS_MIMO"
	helperScenarioEnv = "LEAPMUX_MIMO_TEST_SCENARIO"
	helperRecordEnv   = "LEAPMUX_MIMO_TEST_RECORD"
)

// Helper scenarios.
const (
	scenarioServe          = "serve"
	scenarioResumeMissing  = "resume-missing"
	scenarioWrongPassword  = "wrong-password"
	scenarioNoListen       = "no-listen"
	scenarioCatalogRefused = "catalog-refused"
	scenarioCreateRefused  = "create-refused"
)

// helperRecord is what the helper states about how it was started.
type helperRecord struct {
	Args       []string `json:"args"`
	Password   string   `json:"password"`
	Username   string   `json:"username"`
	QuestionOn string   `json:"questionTool"`
	Client     string   `json:"client"`
	// ApproveDelete and SkipPermissions are the two variables that seed MiMo's
	// permission switches.
	ApproveDelete   string `json:"approveDelete"`
	SkipPermissions string `json:"skipPermissions"`
	Dir             string `json:"dir"`
	PID             int    `json:"pid"`
	RequestPath     string `json:"requestPath"`
}

// TestHelperProcessMiMoServe is the fake `mimo serve`. It returns at once in a
// normal test run.
func TestHelperProcessMiMoServe(*testing.T) {
	if os.Getenv(helperWantEnv) != "1" {
		return
	}
	scenario := os.Getenv(helperScenarioEnv)
	recordDir := os.Getenv(helperRecordEnv)
	args := os.Args
	if index := slices.Index(args, "--"); index >= 0 {
		args = args[index+1:]
	}
	dir, _ := os.Getwd()
	record := helperRecord{
		Args:            args,
		Password:        os.Getenv(envServerPassword),
		Username:        os.Getenv(envServerUsername),
		QuestionOn:      os.Getenv(envQuestionTool),
		Client:          os.Getenv(envClient),
		ApproveDelete:   os.Getenv(envAutoApproveDelete),
		SkipPermissions: os.Getenv(envSkipPermissions),
		Dir:             dir,
		PID:             os.Getpid(),
		RequestPath:     filepath.Join(recordDir, "requests.log"),
	}
	raw, _ := json.Marshal(record)
	_ = os.WriteFile(filepath.Join(recordDir, "record.json"), raw, 0o600)
	if scenario == scenarioNoListen {
		fmt.Println("Error: no project found")
		os.Exit(3)
	}

	password := record.Password
	if scenario == scenarioWrongPassword {
		password = "a password LeapMux never sent"
	}
	server := newFakeServer(password)
	switch scenario {
	case scenarioResumeMissing:
		server.respond("GET /session/ses_missing", http.StatusNotFound, `{"name":"NotFoundError"}`)
	case scenarioCatalogRefused:
		server.respond("GET /config/providers", http.StatusInternalServerError, `{}`)
	case scenarioCreateRefused:
		server.respond("POST /session", http.StatusInternalServerError, `{"name":"UnknownError"}`)
	}
	requestLog, _ := os.OpenFile(record.RequestPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	logged := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(requestLog, "%s %s %s\n", r.Method, r.URL.Path, r.Header.Get(directoryHeader))
		server.ServeHTTP(w, r)
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(4)
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	go func() {
		<-signals
		os.Exit(0)
	}()
	fmt.Println("Warning: a log line before the listen line")
	fmt.Printf("mimocode server listening on http://%s\n", listener.Addr().String())
	_ = http.Serve(listener, logged)
	os.Exit(0)
}

// installFakeMiMo puts the fake `mimo` on PATH and returns the directory the
// helper writes its record to.
func installFakeMiMo(t *testing.T, scenario string) string {
	t.Helper()
	recordDir := t.TempDir()
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary:      mimoBinaryName,
		HelperRun:   "TestHelperProcessMiMoServe",
		WantEnv:     helperWantEnv,
		Env:         []string{helperScenarioEnv + "=" + scenario, helperRecordEnv + "=" + recordDir},
		ForwardArgs: true,
	})
	return recordDir
}

// installWrappedFakeMiMo is installFakeMiMo behind a launcher that does what
// MiMo's Node script does: it runs the server as a CHILD and forwards no
// signal to it.
func installWrappedFakeMiMo(t *testing.T) string {
	t.Helper()
	recordDir := t.TempDir()
	binDir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\n%s=%q %s=%q %q -test.run=TestHelperProcessMiMoServe -- \"$@\" &\nwait $!\n",
		helperScenarioEnv, scenarioServe, helperRecordEnv, recordDir, os.Args[0])
	require.NoError(t, os.WriteFile(filepath.Join(binDir, mimoBinaryName), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(helperWantEnv, "1")
	return recordDir
}

func readHelperRecord(t *testing.T, recordDir string) helperRecord {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(recordDir, "record.json"))
	require.NoError(t, err)
	var record helperRecord
	require.NoError(t, json.Unmarshal(raw, &record))
	return record
}

func readHelperRequests(t *testing.T, recordDir string) []string {
	t.Helper()
	file, err := os.Open(filepath.Join(recordDir, "requests.log"))
	require.NoError(t, err)
	defer func() { _ = file.Close() }()
	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines
}

func startOptions(t *testing.T) agent.Options {
	t.Helper()
	return agent.Options{
		AgentID:       "mimo-start",
		WorkingDir:    t.TempDir(),
		Shell:         testutil.TestShell(),
		LoginShell:    false,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_MIMO_CODE,
	}
}

func startFake(t *testing.T, opts agent.Options) (*Agent, *agenttest.Sink) {
	t.Helper()
	sink := &agenttest.Sink{}
	provider, err := Start(t.Context(), opts, agent.NewProviderServices(sink))
	require.NoError(t, err)
	a := provider.(*Agent)
	t.Cleanup(func() {
		a.Stop()
		_ = a.Wait()
	})
	return a, sink
}

func TestStartHandshake(t *testing.T) {
	recordDir := installFakeMiMo(t, scenarioServe)
	// Values a user's environment can carry. None of them may reach the server.
	t.Setenv(envServerPassword, "inherited")
	t.Setenv(envServerUsername, "someone")
	t.Setenv(envClient, "acp")
	t.Setenv(envAutoApproveDelete, "true")
	t.Setenv(envSkipPermissions, "true")
	opts := startOptions(t)

	a, sink := startFake(t, opts)
	assert.Equal(t, "ses_created", a.sessionID)
	assert.Equal(t, "ses_created", sink.LastSessionID())
	assert.Equal(t, 1, sink.StatusActiveCount())
	assert.Equal(t, []bool{true}, sink.GoalClearSnapshots(), "a new process holds no goal, whatever the row stored")

	record := readHelperRecord(t, recordDir)
	assert.Equal(t, mimoServeArgs, record.Args)
	assert.NotEmpty(t, record.Password)
	assert.NotEqual(t, "inherited", record.Password, "an inherited password would lock the worker out")
	for _, arg := range record.Args {
		assert.NotContains(t, arg, record.Password, "the credential never reaches argv, where any local user can read it")
	}
	assert.Equal(t, serverUser, record.Username)
	assert.Equal(t, "1", record.QuestionOn)
	assert.Empty(t, record.Client, "an inherited acp client would turn the question tool off")
	assert.Empty(t, record.ApproveDelete, "an inherited switch would approve every delete while LeapMux shows Ask")
	assert.Empty(t, record.SkipPermissions, "an inherited switch would approve every ask while LeapMux shows Ask")
	expectedDir, err := filepath.EvalSymlinks(opts.WorkingDir)
	require.NoError(t, err)
	actualDir, err := filepath.EvalSymlinks(record.Dir)
	require.NoError(t, err)
	assert.Equal(t, expectedDir, actualDir)

	groups := a.OptionGroups()
	assert.Equal(t, "mock/beta", optionids.GroupByID(groups, agent.OptionIDModel).GetCurrentValue(), "the configured model runs")
	assert.Equal(t, contracts.MiMoModeBuild, optionids.GroupByID(groups, agent.OptionIDPermissionMode).GetCurrentValue())

	require.NoError(t, a.SendInput("hello", nil))
	requests := readHelperRequests(t, recordDir)
	for _, want := range []string{"GET " + routeHealth, "GET " + routeEvents, "GET " + routeConfigProviders, "POST " + routeSessions,
		"POST " + routeSkipAll, "POST " + routeAutoApproveDel, "POST /session/ses_created/prompt_async"} {
		assert.Contains(t, requestRoutes(requests), want)
	}
	for _, line := range requests {
		assert.True(t, strings.HasSuffix(line, " "+opts.WorkingDir), "every request names the working directory: %q", line)
	}
	assert.Less(t, slices.Index(requestRoutes(requests), "GET "+routeEvents), slices.Index(requestRoutes(requests), "POST "+routeSessions),
		"the event stream opens before the session exists, so no event of the session is lost")
}

func requestRoutes(lines []string) []string {
	routes := make([]string, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			routes = append(routes, fields[0]+" "+fields[1])
		}
	}
	return routes
}

func TestStartResumesAStoredSession(t *testing.T) {
	recordDir := installFakeMiMo(t, scenarioServe)
	opts := startOptions(t)
	opts.ResumeSessionID = "ses_stored"

	a, sink := startFake(t, opts)
	assert.Equal(t, "ses_stored", a.sessionID)
	assert.Equal(t, "ses_stored", sink.LastSessionID())
	routes := requestRoutes(readHelperRequests(t, recordDir))
	assert.Contains(t, routes, "GET /session/ses_stored")
	assert.Contains(t, routes, "GET /session/ses_stored/message", "the resume restores the spawn links and the cost")
	assert.NotContains(t, routes, "POST "+routeSessions)
}

func TestStartFailsForAMissingSession(t *testing.T) {
	installFakeMiMo(t, scenarioResumeMissing)
	opts := startOptions(t)
	opts.ResumeSessionID = "ses_missing"

	_, err := Start(t.Context(), opts, agent.NewProviderServices(&agenttest.Sink{}))
	require.Error(t, err)
	assert.ErrorContains(t, err, "ses_missing", "a failed resume never starts an empty session in its place")
}

// A login shell profile that exports MiMo's credential variables replaces the
// credential the worker set. The server then refuses every request, and the
// start says why.
func TestStartReportsARefusedCredential(t *testing.T) {
	installFakeMiMo(t, scenarioWrongPassword)
	_, err := Start(t.Context(), startOptions(t), agent.NewProviderServices(&agenttest.Sink{}))
	require.Error(t, err)
	assert.ErrorContains(t, err, errCredentialRefused.Error(), "the start error names the export that replaced the credential")
}

func TestStartFailsWhenTheServerNeverListens(t *testing.T) {
	installFakeMiMo(t, scenarioNoListen)
	_, err := Start(t.Context(), startOptions(t), agent.NewProviderServices(&agenttest.Sink{}))
	require.Error(t, err)
	assert.ErrorContains(t, err, "listen")
}

func TestStartFailsWithoutAModelCatalog(t *testing.T) {
	installFakeMiMo(t, scenarioCatalogRefused)
	_, err := Start(t.Context(), startOptions(t), agent.NewProviderServices(&agenttest.Sink{}))
	require.Error(t, err)
	assert.ErrorContains(t, err, "model catalog")
}

// A new session that the server cannot create fails the start at the session
// phase. Only a resume keeps its own error, which names the session.
func TestStartFailsWhenTheServerCannotCreateASession(t *testing.T) {
	installFakeMiMo(t, scenarioCreateRefused)
	_, err := Start(t.Context(), startOptions(t), agent.NewProviderServices(&agenttest.Sink{}))
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "session: POST /session: 500"), "the phase leads the error: %v", err)
	assert.NotContains(t, err.Error(), "could not resume", "a new session is no resume")
}

// MiMo's launcher is a Node script that runs the real server as a child and
// forwards no signal. Stop must end both, or the server outlives the agent.
func TestStopEndsTheWholeProcessGroup(t *testing.T) {
	recordDir := installWrappedFakeMiMo(t)
	sink := &agenttest.Sink{}
	provider, err := Start(t.Context(), startOptions(t), agent.NewProviderServices(sink))
	require.NoError(t, err)
	a := provider.(*Agent)
	server := readHelperRecord(t, recordDir).PID
	require.NotEqual(t, a.Cmd().Process.Pid, server, "the server runs as a child of the launcher")

	a.Stop()
	_ = a.Wait()
	waitFor(t, func() bool {
		return errors.Is(syscall.Kill(server, 0), syscall.ESRCH)
	}, "the server that the launcher started is gone")
	select {
	case <-a.streamDone:
	default:
		t.Error("Stop waits for the event stream to end")
	}
}
