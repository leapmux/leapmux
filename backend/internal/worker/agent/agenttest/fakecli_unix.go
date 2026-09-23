//go:build unix

package agenttest

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

// FakeCLI configures a fake ACP CLI installed on PATH by
// InstallFakeCLI. Every provider's fake shares the same launcher shape; the
// spec captures only what differs.
type FakeCLI struct {
	Binary    string // CLI name placed on PATH, e.g. "goose"
	HelperRun string // TestHelperProcess* the launcher re-execs via -test.run
	WantEnv   string // env var that confirms the re-exec, e.g. "GO_WANT_HELPER_PROCESS_GOOSE"
	// Env entries ("KEY=value") exported inline on the exec line so the helper
	// process can read them (e.g. a per-test scenario).
	Env []string
	// ArgsFile, when set, makes the launcher record its argv ("$@") here so a
	// test can assert the startup flags.
	ArgsFile string
	// ForwardArgs, when true, forwards "$@" to the re-exec'd helper process.
	ForwardArgs bool
}

// Tests in this package call t.Parallel(): they are dominated by subprocess
// spawns (fake ACP CLIs, mock Claude helpers, shells) rather than CPU, and each
// owns its own manager, temp dirs and fake binaries.
//
// InstallFakeCLI is the exception that shapes the rule. It prepends a
// directory to PATH with t.Setenv, which is process-wide, and the testing
// package panics if a test that touched the environment also calls
// t.Parallel. Every test reaching this helper -- directly or through a
// per-provider wrapper such as installFakeCursorCLI -- therefore stays
// serial, and a new one must too.
//
// InstallFakeCLI writes a shell launcher named spec.binary onto PATH that
// re-execs the test binary into spec.HelperRun (the fake ACP server). It is the
// shared core behind each provider's installFake*CLI helper.
func InstallFakeCLI(t *testing.T, spec FakeCLI) {
	t.Helper()

	dir := t.TempDir()
	launcher := filepath.Join(dir, spec.Binary)

	var sb strings.Builder
	sb.WriteString("#!/bin/sh\n")
	if spec.ArgsFile != "" {
		fmt.Fprintf(&sb, "echo \"$@\" > %q\n", spec.ArgsFile)
	}
	for _, kv := range spec.Env {
		k, v, _ := strings.Cut(kv, "=")
		fmt.Fprintf(&sb, "%s=%q ", k, v)
	}
	fmt.Fprintf(&sb, "exec %q -test.run=%s --", os.Args[0], spec.HelperRun)
	if spec.ForwardArgs {
		sb.WriteString(` "$@"`)
	}
	sb.WriteString("\n")
	require.NoError(t, os.WriteFile(launcher, []byte(sb.String()), 0o755))

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(spec.WantEnv, "1")
}

// ServeFakeJSONRPC is the body of a TestHelperProcess* fake ACP server. It
// returns immediately in the parent test process (WantEnv unset) and otherwise
// reads JSON-RPC requests from stdin, asking handle for each method's response.
// handle returns the result/error body, whether it is an error, and whether to
// respond at all (notifications like session/cancel return respond=false).
func ServeFakeJSONRPC(wantEnv string, handle func(method string) (body string, isError, respond bool)) {
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
