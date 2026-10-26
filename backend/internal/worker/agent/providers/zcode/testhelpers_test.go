package zcode

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/require"
)

// zcodeBuiltinZAIAccountConfig holds the Z.ai account rules of an installed release, with
// the model lists that the release ships.
const zcodeBuiltinZAIAccountConfig = `{
  "revision":30,
  "config":{"providerConfigRules":{"providerRules":[
    {"providerId":"account:zai-individual-coding-plan","config":{"access":{"type":"zhipu-account","accountType":"zai","mode":"individual-coding-plan"},"builtinModelIds":["GLM-5.3","GLM-5.3-Flash"]}},
    {"providerId":"account:zai-team-coding-plan","config":{"access":{"type":"zhipu-account","accountType":"zai","mode":"team-coding-plan"},"builtinModelIds":["GLM-5.3","GLM-5.3-Flash"]}},
    {"providerId":"account:zai-start-plan","config":{"access":{"type":"zhipu-account","accountType":"zai","mode":"start-plan"},"builtinModelIds":["GLM-5.3-Flash","GLM-5.2","GLM-5-Turbo"]}}
  ]}}
}`

// zcodeTestRelease writes content as an installed zcode-builtin.json and loads it,
// so a test states exactly the account rules it needs.
func zcodeTestRelease(t *testing.T, content string) (zcodeBuiltinProviderFile, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "zcode-builtin.json")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	release, basedOn, err := loadZCodeBuiltinProviderFile(path)
	require.NoError(t, err)
	return release, basedOn
}

// zcodeTestBindings binds the account plans of catalog against the release in content
// and the credential store in credentials.
func zcodeTestBindings(t *testing.T, catalog zcodeCatalog, content string, credentials zcodeCredentials) []zcodeAccountBinding {
	t.Helper()
	release, _ := zcodeTestRelease(t, content)
	bindings, err := catalog.accountProviderBindings(release.Config.ProviderConfigRules.ProviderRules, credentials)
	require.NoError(t, err)
	return bindings
}

// zcodeTestAccountPayload builds the account snapshot of catalog against the release in
// content, with no credential store and the Start Plan entitlements in startPlans.
func zcodeTestAccountPayload(t *testing.T, catalog zcodeCatalog, content string, startPlans map[string]zcodeAccountEntitlement) zcodeAccountProviderPayload {
	t.Helper()
	release, basedOn := zcodeTestRelease(t, content)
	bindings, err := catalog.accountProviderBindings(release.Config.ProviderConfigRules.ProviderRules, zcodeCredentials{})
	require.NoError(t, err)
	return newZCodeAccountProviderPayload(bindings, basedOn, "leapmux-agent", startPlans)
}

// zcodeTestAccountBridge records the account bridge of catalog against the release in
// content, as an accepted account snapshot does.
func zcodeTestAccountBridge(t *testing.T, catalog *zcodeCatalog, content string) {
	t.Helper()
	catalog.recordAccountBridge(zcodeTestBindings(t, *catalog, content, zcodeCredentials{}))
}

// zcodeTestCredentialSecret is the secret of every test credential store.
const zcodeTestCredentialSecret = "leapmux-test-secret"

// zcodeTestCredentialGetenv supplies the test secret as ZCODE_CREDENTIAL_SECRET.
func zcodeTestCredentialGetenv(name string) string {
	if name == zcodeCredentialSecretEnv {
		return zcodeTestCredentialSecret
	}
	return ""
}

// zcodeTestEncryptCredential encrypts plain as ZCode's credential cipher does.
func zcodeTestEncryptCredential(t *testing.T, key [sha256.Size]byte, plain string) string {
	t.Helper()
	iv := make([]byte, zcodeCredentialIVBytes)
	_, err := rand.Read(iv)
	require.NoError(t, err)
	block, err := aes.NewCipher(key[:])
	require.NoError(t, err)
	gcm, err := cipher.NewGCM(block)
	require.NoError(t, err)
	sealed := gcm.Seal(nil, iv, []byte(plain), nil)
	ciphertext, tag := sealed[:len(sealed)-zcodeCredentialTagBytes], sealed[len(sealed)-zcodeCredentialTagBytes:]
	encode := base64.RawURLEncoding.EncodeToString
	return zcodeCredentialCipherPrefix + encode(iv) + "." + encode(tag) + "." + encode(ciphertext)
}

// zcodeTestCredentialStore writes values, encrypted, into a credential store under a
// new home directory, and opens it with the test secret.
func zcodeTestCredentialStore(t *testing.T, values map[string]string) zcodeCredentialStore {
	t.Helper()
	store := newZCodeCredentialStore(t.TempDir(), zcodeTestCredentialGetenv)
	encrypted := map[string]string{}
	for key, value := range values {
		encrypted[key] = zcodeTestEncryptCredential(t, store.key, value)
	}
	data, err := json.Marshal(encrypted)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(store.path), 0o700))
	require.NoError(t, os.WriteFile(store.path, data, 0o600))
	return store
}

// zcodeTestReadCredentials reads a test credential store.
func zcodeTestReadCredentials(t *testing.T, store zcodeCredentialStore) zcodeCredentials {
	t.Helper()
	credentials, err := store.read()
	require.NoError(t, err)
	return credentials
}

// zcodeTestUserInfo is the stored profile of a signed-in Z.ai account: the raw
// profile of the Z.ai user API, which the desktop stores for Z.ai.
func zcodeTestUserInfo(id string) string {
	return `{"email":"u@example.com","name":"User","user_id":"` + id + `"}`
}

// zcodeOfflineTransport fails the test on any request, so a test agent can never reach
// ZCode's billing API.
type zcodeOfflineTransport struct{ t *testing.T }

func (o zcodeOfflineTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	o.t.Errorf("a test agent sent a request to %s", req.URL.Redacted())
	return nil, errors.New("the test agent is offline")
}

// zcodeTestStartPlanServer is a billing/balance endpoint over TLS. It returns the
// server and the client that trusts it, with the redirect policy of production.
func zcodeTestStartPlanServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *http.Client) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	return server, newZCodeStartPlanHTTPClient(server.Client().Transport)
}

// seedZCodeAccountStateForTests binds the agent's catalog against the Z.ai release and
// the agent's credential store, and records the result as an accepted account
// snapshot, as pushAccountProviderConfig does.
func seedZCodeAccountStateForTests(t *testing.T, a *Agent) {
	t.Helper()
	credentials, err := a.credentials.read()
	require.NoError(t, err)
	bindings := zcodeTestBindings(t, a.catalog, zcodeBuiltinZAIAccountConfig, credentials)
	a.Mu.Lock()
	defer a.Mu.Unlock()
	a.catalog.recordAccountBridge(bindings)
	entitlements := map[string]zcodeAccountEntitlement{}
	for _, binding := range bindings {
		if binding.rule.isStartPlan() {
			require.NotEmpty(t, binding.rule.Config.BuiltinModelIDs)
			entitlements[binding.legacyID] = zcodeAvailable(binding.rule.Config.BuiltinModelIDs...)
		}
	}
	a.account = newZCodeAccountState(zcodeAccountSnapshotInputs{bindings: bindings}, entitlements, a.Clock().Now())
}

// zcodeRecordedStdin is the stdin a test agent writes its requests to.
//
// A real pipe is not needed and a nil one would panic: an event handler can start an
// RPC of its own (a finished turn re-reads the session's usage), so the frames have
// to land somewhere. They are kept, so a test can assert on what was SENT.
type zcodeRecordedStdin struct {
	mu     sync.Mutex
	frames [][]byte
}

func (w *zcodeRecordedStdin) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.frames = append(w.frames, append([]byte(nil), p...))
	return len(p), nil
}

func (w *zcodeRecordedStdin) Close() error { return nil }

// Frames returns every line written to stdin, in order.
func (w *zcodeRecordedStdin) Frames() [][]byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([][]byte(nil), w.frames...)
}

// Requests decodes the frames as request envelopes, so a test can assert which
// method was called with which params.
func (w *zcodeRecordedStdin) Requests(t *testing.T) []zcodeSentRequest {
	t.Helper()
	out := []zcodeSentRequest{}
	for _, frame := range w.Frames() {
		var req zcodeSentRequest
		if json.Unmarshal(frame, &req) != nil {
			continue
		}
		out = append(out, req)
	}
	return out
}

// zcodeAwaitReplies waits until stdin holds n frames and returns them decoded. A
// reply that the agent sends from a goroutine of its own needs it.
func zcodeAwaitReplies(t *testing.T, stdin *zcodeRecordedStdin, n int) []zcodeSentRequest {
	t.Helper()
	require.Eventually(t, func() bool { return len(stdin.Frames()) >= n }, 30*time.Second, time.Millisecond)
	requests := stdin.Requests(t)
	require.Len(t, requests, n)
	return requests
}

// zcodeSentRequest is a frame LeapMux wrote, in either direction: a request of our
// own (method + params) or a reply to a server request (id + result).
type zcodeSentRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *zcodeError     `json:"error"`
}

// newZCodeTestAgent builds a Agent with no process behind it.
//
// Every handler under test is fed through HandleOutput or called directly, so no
// child process is needed -- and none is started, which keeps the suite free of the
// ZCode installation the launch resolver looks for. The context is live so a code
// path that consults a.ctx does not read a nil channel.
func newZCodeTestAgent(t *testing.T, sink agent.ProviderServices) *Agent {
	t.Helper()
	return newZCodeTestAgentWithStdin(t, sink, &zcodeRecordedStdin{})
}

func newZCodeTestAgentWithStdin(t *testing.T, sink agent.ProviderServices, stdin *zcodeRecordedStdin) *Agent {
	t.Helper()
	return newZCodeTestAgentWithClock(t, sink, stdin, nil)
}

// newZCodeTestAgentWithClock builds a test agent whose timers run on clock. A nil
// clock selects the real one.
func newZCodeTestAgentWithClock(t *testing.T, sink agent.ProviderServices, stdin *zcodeRecordedStdin, clock quartz.Clock) *Agent {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	// A handler may start an RPC whose reply never comes (nothing answers this
	// stdin). Cancelling at cleanup unblocks that goroutine instead of leaving it
	// parked on the API timeout for the rest of the run.
	t.Cleanup(cancel)
	a := &Agent{
		Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID:     "test-agent",
			Ctx:         ctx,
			Cancel:      cancel,
			ProcessDone: make(chan struct{}),
			Stdin:       stdin,
			Clock:       clock,
		}),
		sink:          sink,
		workingDir:    "/tmp/zcode-workspace",
		workspace:     zcodeWorkspaceFor("/tmp/zcode-workspace"),
		sessionID:     "sess-1",
		mode:          contracts.ZCodeDefaultMode,
		identity:      zcodeClientIdentity{appVersion: zcodeUnknownAppVersion, deviceMid: "00000000-0000-4000-8000-000000000000"},
		startPlanHTTP: newZCodeStartPlanHTTPClient(zcodeOfflineTransport{t: t}),
		projectTokens: newZCodeProjectTokenClient(newZCodeStartPlanHTTPClient(zcodeOfflineTransport{t: t}),
			zcodeProductionAccountEndpoints, quartz.NewReal(), func() float64 { return 0 }),
		toolCalls:       map[string]*zcodeToolCall{},
		pendingControls: map[string]*pendingControl{},
	}
	a.sink = agent.NewModelProgressResetSink(a.sink)
	return a
}

// zcodeEventLine renders one session/event notification exactly as the app-server
// sends it: the envelope flat inside `params`, with no wrapper object.
func zcodeEventLine(t *testing.T, seq int64, eventType string, payload string) []byte {
	t.Helper()
	body := map[string]any{
		"eventId":   "evt",
		"sessionId": "sess-1",
		"seq":       seq,
		"type":      eventType,
	}
	if payload != "" {
		body["payload"] = json.RawMessage(payload)
	}
	params, err := json.Marshal(body)
	require.NoError(t, err)
	line, err := json.Marshal(map[string]any{
		"method": NotifySessionEvent,
		"params": json.RawMessage(params),
	})
	require.NoError(t, err)
	return line
}

// zcodeStateLine renders one top-level state.updated notification.
func zcodeStateLine(t *testing.T, scope, reason, patch string) []byte {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"method": contracts.ZCodeMethodStateUpdated,
		"params": map[string]any{
			"scope":     scope,
			"sessionId": "sess-1",
			"revision":  7,
			"reason":    reason,
			"patch":     json.RawMessage(patch),
		},
	})
	require.NoError(t, err)
	return line
}

// zcodeReplyLine is one app-server response, keyed by the request id LeapMux minted.
func zcodeReplyLine(t *testing.T, id int64, result json.RawMessage) []byte {
	t.Helper()
	if len(result) == 0 {
		result = json.RawMessage(`{}`)
	}
	line, err := json.Marshal(map[string]any{"id": id, "result": result})
	require.NoError(t, err)
	return line
}

// zcodeErrorReplyLine is one app-server error response, keyed by the request id
// LeapMux minted.
func zcodeErrorReplyLine(t *testing.T, id int64, code int, message string) []byte {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"id":    id,
		"error": map[string]any{"code": code, "message": message},
	})
	require.NoError(t, err)
	return line
}

// zcodeTestRPCTimeout limits an RPC that the test EXPECTS an answer for. It is a
// backstop only: a reply that never arrives fails the test in seconds, instead of
// parking the run until the package timeout.
const zcodeTestRPCTimeout = 5 * time.Second

// answerZCodeRequest replies to the next request for method with result.
//
// The reply goes out from a separate goroutine, because the RPC blocks the test
// goroutine until the reply lands. Each call waits for its OWN method, so a test
// prepares every answer of a multi-step exchange before it starts the exchange.
func answerZCodeRequest(t *testing.T, a *Agent, stdin *zcodeRecordedStdin, method, result string) {
	t.Helper()
	go func() {
		req := waitZCodeRequest(t, stdin, method)
		a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, req), json.RawMessage(result)))
	}()
}

// refuseZCodeRequest answers the next request for method with an app-server error.
func refuseZCodeRequest(t *testing.T, a *Agent, stdin *zcodeRecordedStdin, method string, code int, message string) {
	t.Helper()
	go func() {
		req := waitZCodeRequest(t, stdin, method)
		a.HandleOutput(zcodeErrorReplyLine(t, zcodeSentRequestID(t, req), code, message))
	}()
}

// waitZCodeRequest waits until stdin holds a request for method (or any request
// when method is empty). The setters block until a reply arrives, so the test
// answers on this goroutine after this returns.
func waitZCodeRequest(t *testing.T, stdin *zcodeRecordedStdin, method string) zcodeSentRequest {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, req := range stdin.Requests(t) {
			if method == "" || req.Method == method {
				return req
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for a %s request", method)
	return zcodeSentRequest{}
}

func zcodeSentRequestID(t *testing.T, req zcodeSentRequest) int64 {
	t.Helper()
	var id int64
	require.NoError(t, json.Unmarshal(req.ID, &id))
	return id
}

// zcodeTestCatalog builds a catalog from an inline configuration, so a test states
// exactly the providers and models it needs.
func zcodeTestCatalog(t *testing.T, configJSON string) zcodeCatalog {
	t.Helper()
	var cfg zcodeConfigFile
	require.NoError(t, json.Unmarshal([]byte(configJSON), &cfg))
	catalog, _ := buildZCodeCatalog(cfg)
	return catalog
}

// zcodeTwoProviderConfig is the configuration most tests use: one enabled provider
// with a reasoning-capable text model and a model that reads images and PDFs, and
// one disabled provider that still holds a key.
const zcodeTwoProviderConfig = `{
  "provider": {
    "builtin:zai": {
      "name": "Z.ai",
      "kind": "openai-compatible",
      "source": "builtin",
      "enabled": true,
      "options": {"apiKey": "zai-key", "baseURL": "https://api.z.ai/v1"},
      "models": {
        "GLM-5.3": {
          "name": "GLM-5.3",
          "reasoning": {"enabled": true, "variants": ["low", "high", "max"], "defaultVariant": "high"},
          "limit": {"context": 200000, "output": 32000},
          "modalities": {"input": ["text"], "output": ["text"]}
        },
        "GLM-5.3-Flash": {
          "name": "GLM-5.3 Flash",
          "limit": {"context": 128000, "output": 16000},
          "modalities": {"input": ["text", "image", "pdf"], "output": ["text"]}
        }
      }
    },
    "acme": {
      "name": "Acme",
      "kind": "anthropic",
      "source": "custom",
      "options": {"apiKey": "acme-key"},
      "models": {"acme-1": {"name": "Acme One"}}
    }
  }
}`
