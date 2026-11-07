// Package musetest supplies controlled peers for Muse's native protocol.
package musetest

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"slices"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

const sessionRuntimeMarker = "muse-session-store"

func NewSessionRuntimeFixture(t *testing.T, home, workingDir, helperPattern string) agenttest.SessionRuntimeFixture {
	t.Helper()
	return agenttest.CreateSessionRuntimeFixture(t, home, workingDir, helperPattern, sessionRuntimeMarker)
}

// RunSessionStorePeer serves one controlled empty session store through the native handshake.
func RunSessionStorePeer() bool {
	if os.Getenv(agenttest.SessionRuntimeMarkerEnv) != sessionRuntimeMarker {
		return false
	}
	if !slices.Equal(flag.Args(), []string{"serve"}) {
		os.Exit(2)
	}
	if err := agenttest.WriteSessionRuntimeInvocation(); err != nil {
		os.Exit(3)
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil || len(request.ID) == 0 {
			continue
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"serverInfo": map[string]string{"name": "muse", "version": "controlled"}, "museHome": "", "grantedCapabilities": []string{}, "experimentalApi": true, "sessionDurability": "", "schema": map[string]any{"version": 1, "fingerprint": "controlled"}}
		case "session/list":
			result = map[string]any{"sessions": []any{}, "nextCursor": nil}
		default:
			os.Exit(4)
		}
		data, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		if err != nil {
			os.Exit(5)
		}
		fmt.Println(string(data))
	}
	if scanner.Err() != nil {
		os.Exit(6)
	}
	os.Exit(0)
	return true
}
