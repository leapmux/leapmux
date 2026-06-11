//go:build unix

package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeACPCLISpec configures a fake ACP CLI installed on PATH by
// installFakeACPCLI. Every provider's fake shares the same launcher shape; the
// spec captures only what differs.
type fakeACPCLISpec struct {
	binary    string // CLI name placed on PATH, e.g. "goose"
	helperRun string // TestHelperProcess* the launcher re-execs via -test.run
	wantEnv   string // env var that confirms the re-exec, e.g. "GO_WANT_HELPER_PROCESS_GOOSE"
	// env entries ("KEY=value") exported inline on the exec line so the helper
	// process can read them (e.g. a per-test scenario).
	env []string
	// argsFile, when set, makes the launcher record its argv ("$@") here so a
	// test can assert the startup flags.
	argsFile string
	// forwardArgs, when true, forwards "$@" to the re-exec'd helper process.
	forwardArgs bool
}

// installFakeACPCLI writes a shell launcher named spec.binary onto PATH that
// re-execs the test binary into spec.helperRun (the fake ACP server). It is the
// shared core behind each provider's installFake*CLI helper.
func installFakeACPCLI(t *testing.T, spec fakeACPCLISpec) {
	t.Helper()

	dir := t.TempDir()
	launcher := filepath.Join(dir, spec.binary)

	var sb strings.Builder
	sb.WriteString("#!/bin/sh\n")
	if spec.argsFile != "" {
		fmt.Fprintf(&sb, "echo \"$@\" > %q\n", spec.argsFile)
	}
	for _, kv := range spec.env {
		k, v, _ := strings.Cut(kv, "=")
		fmt.Fprintf(&sb, "%s=%q ", k, v)
	}
	fmt.Fprintf(&sb, "exec %q -test.run=%s --", os.Args[0], spec.helperRun)
	if spec.forwardArgs {
		sb.WriteString(` "$@"`)
	}
	sb.WriteString("\n")
	require.NoError(t, os.WriteFile(launcher, []byte(sb.String()), 0o755))

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(spec.wantEnv, "1")
}

// runFakeACPServer is the body of a TestHelperProcess* fake ACP server. It
// returns immediately in the parent test process (wantEnv unset) and otherwise
// reads JSON-RPC requests from stdin, asking handle for each method's response.
// handle returns the result/error body, whether it is an error, and whether to
// respond at all (notifications like session/cancel return respond=false).
func runFakeACPServer(wantEnv string, handle func(method string) (body string, isError, respond bool)) {
	if os.Getenv(wantEnv) != "1" {
		return
	}

	scanner := bufio.NewScanner(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	defer func() { _ = writer.Flush() }()

	for scanner.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}

		body, isError, respond := handle(req.Method)
		if !respond {
			continue
		}
		field := "result"
		if isError {
			field = "error"
		}
		_, _ = fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"%s":%s}`+"\n", string(req.ID), field, body)
		_ = writer.Flush()
	}
	os.Exit(0)
}
