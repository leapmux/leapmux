//go:build unix

package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexStartupOverlapsModelDiscovery(t *testing.T) {
	for _, refused := range []bool{false, true} {
		t.Run(fmt.Sprintf("thread refusal %t", refused), func(t *testing.T) {
			// The fake CLI changes PATH, so this test cannot run in parallel.
			agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
				Binary: "codex", HelperRun: "TestHelperCodexConcurrentStartup", WantEnv: "LEAPMUX_TEST_CODEX_STARTUP",
				Env: []string{fmt.Sprintf("LEAPMUX_TEST_CODEX_THREAD_REFUSED=%t", refused)},
			})
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			started, err := Start(ctx, agent.Options{AgentID: "startup", WorkingDir: t.TempDir(), Shell: testutil.TestShell(), StartupTimeout: 30 * time.Second}, agent.NewProviderServices(&agenttest.Sink{}))
			if refused {
				require.ErrorContains(t, err, "thread refused")
				assert.Nil(t, started)
				return
			}
			require.NoError(t, err)
			t.Cleanup(func() { started.Stop(); _ = started.Wait() })
			a := started.(*Agent)
			assert.NotNil(t, agent.FindAvailableModel(a.availableModels, "account-model"))
			assert.Equal(t, "account-model", a.model)
		})
	}
}

func TestHelperCodexConcurrentStartup(t *testing.T) {
	if os.Getenv("LEAPMUX_TEST_CODEX_STARTUP") != "1" {
		return
	}
	send := func(id json.RawMessage, field, body string) {
		_, err := fmt.Fprintf(os.Stdout, "{\"jsonrpc\":\"2.0\",\"id\":%s,\"%s\":%s}\n", id, field, body)
		require.NoError(t, err)
	}
	var modelID, profileID json.RawMessage
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &request))
		switch request.Method {
		case "initialize":
			send(request.ID, "result", `{}`)
		case "initialized":
		case "model/list":
			modelID = request.ID
		case "permissionProfile/list":
			profileID = request.ID
		case "configRequirements/read":
			send(request.ID, "result", `{"requirements":null}`)
		case "config/read":
			send(request.ID, "result", `{"config":{}}`)
		case "thread/start":
			if os.Getenv("LEAPMUX_TEST_CODEX_THREAD_REFUSED") == "true" {
				send(request.ID, "error", `{"code":-32000,"message":"thread refused"}`)
			} else {
				send(request.ID, "result", `{"thread":{"id":"thread-1"},"model":"account-model"}`)
				send(modelID, "result", `{"data":[{"id":"account-model","model":"account-model","defaultReasoningEffort":"high"}],"nextCursor":null}`)
			}
		default:
			send(request.ID, "result", `{}`)
		}
		// Discovery must overlap: neither request can finish before the other arrives.
		if modelID != nil && profileID != nil {
			send(profileID, "result", `{"data":[{"id":":workspace","allowed":true}],"nextCursor":null}`)
			profileID = nil
		}
	}
	os.Exit(0)
}
