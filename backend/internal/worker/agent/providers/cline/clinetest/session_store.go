package clinetest

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

const sessionRuntimeMarker = "cline-session-store"

func NewSessionRuntimeFixture(t *testing.T, home, workingDir, helperPattern string) agenttest.SessionRuntimeFixture {
	t.Helper()
	return agenttest.CreateSessionRuntimeFixture(t, home, workingDir, helperPattern, sessionRuntimeMarker)
}

// RunSessionStorePeer runs only the controlled Cline history invocation.
func RunSessionStorePeer() bool {
	if os.Getenv(agenttest.SessionRuntimeMarkerEnv) != sessionRuntimeMarker {
		return false
	}
	if err := serveSessionStorePeer(flag.Args(), os.Stdout, os.Getenv, agenttest.WriteSessionRuntimeInvocation); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
	return true
}

func serveSessionStorePeer(args []string, output io.Writer, getenv func(string) string, record func() error) error {
	if !slices.Equal(args, []string{"history", "--json", "--limit", "500"}) {
		return errors.New("the controlled Cline session peer received invalid arguments")
	}
	if getenv("CLINE_SESSION_BACKEND_MODE") != "local" || getenv("CLINE_NO_AUTO_UPDATE") != "1" || getenv("CLINE_HUB_URL") != "" || getenv("CLINE_HUB_TOKEN") != "" {
		return errors.New("the controlled Cline session peer has no isolated local backend")
	}
	if err := record(); err != nil {
		return err
	}
	_, err := fmt.Fprintln(output, "[]")
	return err
}
