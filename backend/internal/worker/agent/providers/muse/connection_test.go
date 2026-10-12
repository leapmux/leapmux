package muse

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"os/exec"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/leapmux/leapmux/util/procutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type museRoleHostSpec struct {
	CloseExitCode   int                            `json:"closeExitCode,omitempty"`
	Initialize      json.RawMessage                `json:"initialize,omitempty"`
	InitializeError json.RawMessage                `json:"initializeError,omitempty"`
	Pages           []json.RawMessage              `json:"pages,omitempty"`
	PageError       json.RawMessage                `json:"pageError,omitempty"`
	ReadyAddress    string                         `json:"readyAddress,omitempty"`
	TranscriptPath  string                         `json:"transcriptPath"`
	BuildPages      func(string) []json.RawMessage `json:"-"`
}

type museRoleHostRecord struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func museRoleContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func museRoleLaunch(t *testing.T, spec museRoleHostSpec) (agent.Options, agent.Registration, []string, string) {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	dir := t.TempDir()
	spec.TranscriptPath = filepath.Join(dir, "native-host.jsonl")
	if spec.BuildPages != nil {
		spec.Pages = spec.BuildPages(dir)
	}
	raw, err := json.Marshal(spec)
	require.NoError(t, err)
	registration := Registration()
	registration.Locator = launch.Custom(func(context.Context, string, bool) (launch.Spec, launch.Resolution) {
		return launch.Spec{Program: executable, PrefixArgs: []string{"-test.run=^TestMuseRoleHostHelper$", "--"}}, launch.Found
	})
	opts := agent.Options{AgentID: "muse-role-test", WorkingDir: dir, Shell: terminal.ResolveDefaultShell(), StartupTimeout: 30 * time.Second, APITimeout: 30 * time.Second}
	env := append(os.Environ(), "LEAPMUX_MUSE_ROLE_HOST="+string(raw), "MUSE_NO_AUTO_UPDATE=0", "LEAPMUX_MUSE_ROLE_MARKER=private-role-host")
	return opts, registration, env, spec.TranscriptPath
}

func museRoleRecords(t *testing.T, path string) []museRoleHostRecord {
	t.Helper()
	file, err := os.Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, file.Close()) }()
	var records []museRoleHostRecord
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	for scanner.Scan() {
		var record museRoleHostRecord
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &record))
		records = append(records, record)
	}
	require.NoError(t, scanner.Err())
	return records
}

func TestMuseConnectionNegotiatesTheNativeCapabilitiesAndPinsUpdates(t *testing.T) {
	opts, registration, env, path := museRoleLaunch(t, museRoleHostSpec{})
	c, err := openConnection(museRoleContext(t), opts, registration, env, nil)
	require.NoError(t, err)
	require.True(t, c.hasCapability("rawLog"))
	require.True(t, c.hasCapability("sessionMcp"))
	assert.False(t, c.hasCapability("ungranted"))
	require.NoError(t, c.close())
	records := museRoleRecords(t, path)
	require.Len(t, records, 4)
	assert.Equal(t, "$launch", records[0].Method)
	var environment struct {
		AutoUpdate string `json:"autoUpdate"`
		Marker     string `json:"marker"`
	}
	require.NoError(t, json.Unmarshal(records[0].Params, &environment))
	assert.Equal(t, "1", environment.AutoUpdate)
	assert.Equal(t, "private-role-host", environment.Marker)
	assert.Equal(t, methodInitialize, records[1].Method)
	var initialize struct {
		Capabilities struct {
			Experimental bool     `json:"experimentalApi"`
			Requested    []string `json:"requestedCapabilities"`
		} `json:"capabilities"`
	}
	require.NoError(t, json.Unmarshal(records[1].Params, &initialize))
	assert.True(t, initialize.Capabilities.Experimental)
	assert.Equal(t, []string{"rawLog", "sessionMcp"}, initialize.Capabilities.Requested)
	assert.Equal(t, methodInitialized, records[2].Method)
	assert.Equal(t, "$closed", records[3].Method)
	assert.NotNil(t, c.Cmd().ProcessState)
	assert.True(t, c.Cmd().ProcessState.Exited())
	select {
	case <-c.outputDone:
	default:
		t.Fatal("the native output reader did not complete before close returned")
	}
}

func TestMuseConnectionRejectsMalformedHandshakesAndClosesTheOwnedHost(t *testing.T) {
	for _, raw := range []string{
		`null`, `{}`, `{"serverInfo":{"name":"foreign"},"schema":{"version":1,"fingerprint":"native"}}`,
		`{"serverInfo":{"name":"muse"},"schema":{"version":2,"fingerprint":"native"}}`,
		`{"serverInfo":{"name":"muse"},"schema":{"version":1,"fingerprint":""}}`,
		`{"serverInfo":{"name":"muse"},"schema":{"version":"one","fingerprint":"native"}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			opts, registration, env, path := museRoleLaunch(t, museRoleHostSpec{Initialize: json.RawMessage(raw)})
			c, err := prepareConnection(museRoleContext(t), opts, registration, env)
			require.NoError(t, err)
			require.Error(t, c.initialize(opts, nil, nil))
			assert.True(t, c.IsStopped())
			assert.True(t, c.Cmd().ProcessState.Exited())
			records := museRoleRecords(t, path)
			require.Len(t, records, 3)
			assert.Equal(t, "$closed", records[2].Method)
		})
	}
}

func TestMuseConnectionPreservesTheNativeHandshakeRefusal(t *testing.T) {
	opts, registration, env, path := museRoleLaunch(t, museRoleHostSpec{InitializeError: json.RawMessage(`{"code":-32000,"message":"The native schema is unavailable"}`)})
	c, err := prepareConnection(museRoleContext(t), opts, registration, env)
	require.NoError(t, err)
	require.ErrorContains(t, c.initialize(opts, nil, nil), "The native schema is unavailable")
	assert.True(t, c.Cmd().ProcessState.Exited())
	assert.Equal(t, "$closed", museRoleRecords(t, path)[2].Method)
}

func TestMuseConnectionRejectsInvalidTrustBeforeItResolvesACommand(t *testing.T) {
	opts, registration, env, _ := museRoleLaunch(t, museRoleHostSpec{})
	opts.Options = map[string]string{"workspaceTrust": "invalid"}
	resolved := false
	registration.Locator = launch.Custom(func(context.Context, string, bool) (launch.Spec, launch.Resolution) {
		resolved = true
		return launch.Spec{}, launch.Missing
	})
	c, err := prepareConnection(museRoleContext(t), opts, registration, env)
	require.ErrorContains(t, err, "workspace trust")
	assert.Nil(t, c)
	assert.False(t, resolved)
}

func TestMuseRoleHostHelper(t *testing.T) {
	raw := os.Getenv("LEAPMUX_MUSE_ROLE_HOST")
	if raw == "" {
		return
	}
	var spec museRoleHostSpec
	if json.Unmarshal([]byte(raw), &spec) != nil {
		os.Exit(10)
	}
	recordFile, err := os.OpenFile(spec.TranscriptPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		os.Exit(11)
	}
	record := func(method string, params any) {
		data, err := json.Marshal(map[string]any{"method": method, "params": params})
		if err != nil {
			os.Exit(12)
		}
		if _, err := fmt.Fprintln(recordFile, string(data)); err != nil {
			os.Exit(13)
		}
	}
	record("$launch", map[string]any{"autoUpdate": os.Getenv("MUSE_NO_AUTO_UPDATE"), "marker": os.Getenv("LEAPMUX_MUSE_ROLE_MARKER"), "pid": os.Getpid()})
	write := func(value any) {
		data, err := json.Marshal(value)
		if err != nil {
			os.Exit(14)
		}
		if _, err := fmt.Fprintln(os.Stdout, string(data)); err != nil {
			os.Exit(15)
		}
	}
	page := 0
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request frame
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(16)
		}
		record(request.Method, request.Params)
		response := map[string]any{"jsonrpc": "2.0", "id": request.ID}
		switch request.Method {
		case methodInitialize:
			if len(spec.InitializeError) != 0 {
				response["error"] = spec.InitializeError
			} else if len(spec.Initialize) != 0 {
				response["result"] = spec.Initialize
			} else {
				response["result"] = map[string]any{"serverInfo": map[string]any{"name": "muse", "version": "role-test"}, "schema": map[string]any{"version": 1, "fingerprint": "native-role-schema"}, "grantedCapabilities": []string{"rawLog", "sessionMcp"}}
			}
		case methodInitialized:
			continue
		case methodSessionList:
			if spec.ReadyAddress != "" {
				ready, err := net.Dial("tcp", spec.ReadyAddress)
				if err != nil {
					os.Exit(17)
				}
				if _, err := ready.Write([]byte{1}); err != nil {
					os.Exit(18)
				}
				var release [1]byte
				_, _ = ready.Read(release[:])
				_ = ready.Close()
				os.Exit(0)
			}
			if len(spec.PageError) != 0 {
				response["error"] = spec.PageError
			} else if page < len(spec.Pages) {
				response["result"] = spec.Pages[page]
				page++
			} else {
				response["error"] = map[string]any{"code": -32000, "message": "The native test host received an extra page request"}
			}
		default:
			response["error"] = map[string]any{"code": -32601, "message": "The native query host received an unsupported method"}
		}
		write(response)
	}
	if scanner.Err() != nil {
		os.Exit(19)
	}
	record("$closed", nil)
	if recordFile.Close() != nil {
		os.Exit(20)
	}
	os.Exit(spec.CloseExitCode)
}

func museRoleHostIdentity(t *testing.T, path string) procutil.ProcessIdentity {
	t.Helper()
	records := museRoleRecords(t, path)
	require.NotEmpty(t, records)
	var launch struct {
		PID int `json:"pid"`
	}
	require.NoError(t, json.Unmarshal(records[0].Params, &launch))
	identity, exists := procutil.IdentifyProcess(launch.PID)
	if !exists {
		return procutil.ProcessIdentity{}
	}
	return identity
}

func TestMuseConnectionPreservesHandshakeAndOwnedCleanupFailures(t *testing.T) {
	for _, nativeRefusal := range []bool{false, true} {
		t.Run(map[bool]string{false: "invalid handshake", true: "native refusal"}[nativeRefusal], func(t *testing.T) {
			spec := museRoleHostSpec{CloseExitCode: 7, Initialize: json.RawMessage(`null`)}
			cause := "the Muse host returned an invalid handshake"
			if nativeRefusal {
				spec.Initialize = nil
				spec.InitializeError = json.RawMessage(`{"code":-32000,"message":"The native schema is unavailable"}`)
				cause = "The native schema is unavailable"
			}
			opts, registration, env, path := museRoleLaunch(t, spec)
			connection, err := prepareConnection(museRoleContext(t), opts, registration, env)
			require.NoError(t, err)
			err = connection.initialize(opts, nil, nil)
			require.ErrorContains(t, err, cause)
			var nativeExit *exec.ExitError
			require.ErrorAs(t, err, &nativeExit)
			assert.Equal(t, 7, nativeExit.ExitCode())
			assert.True(t, connection.Cmd().ProcessState.Exited())
			records := museRoleRecords(t, path)
			assert.Equal(t, "$closed", records[len(records)-1].Method)
		})
	}
}
