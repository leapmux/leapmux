package zcodetest

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

const sessionRuntimeMarker = "zcode-session-store"

func NewSessionRuntimeFixture(t *testing.T, home, workingDir, helperPattern string) agenttest.SessionRuntimeFixture {
	t.Helper()
	return agenttest.CreateSessionRuntimeFixture(t, home, workingDir, helperPattern, sessionRuntimeMarker)
}

// RunSessionStorePeer requires the native read-only storage acknowledgement.
func RunSessionStorePeer() bool {
	if os.Getenv(agenttest.SessionRuntimeMarkerEnv) != sessionRuntimeMarker {
		return false
	}
	if err := serveSessionStorePeer(flag.Args(), os.Getenv("HOME"), os.Stdin, os.Stdout, agenttest.WriteSessionRuntimeInvocation); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
	return true
}

func serveSessionStorePeer(args []string, home string, input io.Reader, output io.Writer, record func() error) error {
	if !slices.Equal(args, []string{"app-server", "--stdio", "--prepare-storage"}) || !filepath.IsAbs(home) {
		return errors.New("the controlled ZCode session peer received invalid arguments or home")
	}
	path := filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")
	encoder := json.NewEncoder(output)
	if err := encoder.Encode(map[string]any{"method": "startup/storagePath", "params": map[string]string{"path": path}}); err != nil {
		return err
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 1024), 1024)
	if !scanner.Scan() {
		return errors.Join(errors.New("the controlled ZCode storage acknowledgement is absent"), scanner.Err())
	}
	var fields map[string]json.RawMessage
	var method string
	var reuse bool
	if json.Unmarshal(scanner.Bytes(), &fields) != nil || len(fields) != 2 ||
		json.Unmarshal(fields["method"], &method) != nil || method != "startup/storagePathReady" ||
		json.Unmarshal(fields["reuse"], &reuse) != nil || !reuse {
		return errors.New("the controlled ZCode storage acknowledgement is invalid")
	}
	if err := record(); err != nil {
		return err
	}
	return encoder.Encode(map[string]any{"method": "startup/storagePrepared", "params": map[string]any{}})
}
