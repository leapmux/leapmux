package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
)

// cliOutcome is what one run of `leapmux control ...` printed, and its exit
// status.
type cliOutcome struct {
	Stdout string
	Stderr string
	Code   int
}

// runControl runs `leapmux control <args...> --json` through the real
// command tree and cli.Execute, the same path that the binary takes, and
// returns what it printed. --json makes every result and every failure one
// envelope line that the outcome's accessors decode.
func runControl(t *testing.T, args ...string) cliOutcome {
	t.Helper()
	return runControlIn(t, "", append(args, "--json")...)
}

// runControlText is runControl without --json, for a test of the text form.
func runControlText(t *testing.T, args ...string) cliOutcome {
	t.Helper()
	return runControlIn(t, "", args...)
}

// testHubArgs selects the fake Hub explicitly. An empty URL leaves the flag
// out so a dual-transport test can exercise the local tab socket.
func testHubArgs(url string) []string {
	if url == "" {
		return nil
	}
	return []string{"--hub", url}
}

// runControlIn runs `leapmux control <args...>` with stdin as its input.
// Each run builds a new tree, so no flag value survives from one run to
// the next.
func runControlIn(t *testing.T, stdin string, args ...string) cliOutcome {
	t.Helper()
	return runTree(t, NewCommand(), stdin, append([]string{"control"}, args...)...)
}

// runTree runs `leapmux <args...>` through cli.Execute, with tree as the one
// command under the root. A test that adds a probe leaf to the control tree
// runs it through here.
func runTree(t *testing.T, tree *cobra.Command, stdin string, args ...string) cliOutcome {
	t.Helper()
	root := cli.Group("leapmux", "the binary under test")
	root.AddCommand(tree)
	var out, errOut bytes.Buffer
	prevErr := control.Err
	control.Err = &errOut
	defer func() { control.Err = prevErr }()
	code := cli.Execute(context.Background(), root, args, strings.NewReader(stdin), &out, &errOut)
	return cliOutcome{Stdout: out.String(), Stderr: errOut.String(), Code: code}
}

// lines returns the envelope lines that the run printed on stdout.
func (o cliOutcome) lines() []string {
	var out []string
	for _, line := range strings.Split(o.Stdout, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

// Data decodes the data of the last envelope, and fails the test when the
// run failed.
func (o cliOutcome) Data(t *testing.T) map[string]any {
	t.Helper()
	require.Equal(t, cli.ExitOK, o.Code, "the command failed:\nstdout: %s\nstderr: %s", o.Stdout, o.Stderr)
	lines := o.lines()
	require.NotEmpty(t, lines, "the command printed no result")
	var env struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &env), "stdout: %s", o.Stdout)
	return env.Data
}

// Events decodes the data of every envelope line, for a command that
// streams.
func (o cliOutcome) Events(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range o.lines() {
		var env struct {
			Data  map[string]any `json:"data"`
			Error map[string]any `json:"error"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &env), "line: %s", line)
		if env.Data != nil {
			out = append(out, env.Data)
		}
	}
	return out
}

// Failure decodes the error envelope of a failed run.
func (o cliOutcome) Failure(t *testing.T) cliFailure {
	t.Helper()
	require.NotEqual(t, cli.ExitOK, o.Code, "the command succeeded:\nstdout: %s", o.Stdout)
	lines := o.lines()
	require.NotEmpty(t, lines, "the command printed no envelope; stderr: %s", o.Stderr)
	var env struct {
		Error cliFailure `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &env), "stdout: %s", o.Stdout)
	require.NotEmpty(t, env.Error.Code, "the last line is not an error envelope: %s", o.Stdout)
	return env.Error
}

// cliFailure is the body of an {"error": ...} envelope.
type cliFailure struct {
	Code       cli.Code        `json:"code"`
	Message    string          `json:"message"`
	Hints      []string        `json:"hints"`
	Retryable  bool            `json:"retryable"`
	Candidates []cli.Candidate `json:"candidates"`
}

// requireFailure asserts that the run failed with code, and returns the
// failure.
func (o cliOutcome) requireFailure(t *testing.T, code cli.Code) cliFailure {
	t.Helper()
	f := o.Failure(t)
	require.Equal(t, code, f.Code, "message: %s", f.Message)
	require.Equal(t, cli.ExitCode(code), o.Code)
	return f
}

// failureEnvelope is the error envelope of an error that a helper returned,
// in the shape that the tests of the helpers read: env.Error["code"] and
// env.Error["message"].
type failureEnvelope struct {
	Error map[string]string
}

// failureOf returns the envelope that the root prints for err.
func failureOf(t *testing.T, err error) failureEnvelope {
	t.Helper()
	require.Error(t, err)
	e := cli.FromError(err)
	return failureEnvelope{Error: map[string]string{
		"code":    string(e.Code),
		"message": e.Message,
		"hints":   strings.Join(e.Hints, "\n"),
	}}
}

// fullTestID pads a readable test id to the length of a full id, so a
// command takes it as a full id and lists nothing to resolve it.
func fullTestID(id string) string {
	return id + strings.Repeat("0", 40-len(id))
}

// captureEmit runs fn and returns the code and the message of the envelope
// that the root prints for its error, or two empty strings when it
// succeeds.
func captureEmit(t *testing.T, fn func() error) (code string, message string) {
	t.Helper()
	err := fn()
	if err == nil {
		return "", ""
	}
	e := cli.FromError(err)
	return string(e.Code), e.Message
}

// decodedEnvelope is one envelope line of a run, with either half decoded.
type decodedEnvelope struct {
	Data  map[string]any `json:"data"`
	Error map[string]any `json:"error"`
}

// envelope decodes the last envelope line that the run printed.
func (o cliOutcome) envelope(t *testing.T) decodedEnvelope {
	t.Helper()
	lines := o.lines()
	require.NotEmpty(t, lines, "the command printed no envelope; stderr: %s", o.Stderr)
	var env decodedEnvelope
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &env), "stdout: %s", o.Stdout)
	return env
}

// jsonMap encodes v as a result does and decodes it to a map, so a test
// reads the keys that a script reads.
func jsonMap(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

// ok asserts that the run succeeded and returns what it printed on stdout.
func (o cliOutcome) ok(t *testing.T) []byte {
	t.Helper()
	require.Equal(t, cli.ExitOK, o.Code, "the command failed:\nstdout: %s\nstderr: %s", o.Stdout, o.Stderr)
	return []byte(o.Stdout)
}

// failed asserts that the run failed and returns what it printed on stdout:
// the error envelope.
func (o cliOutcome) failed(t *testing.T) []byte {
	t.Helper()
	require.NotEqual(t, cli.ExitOK, o.Code, "the command succeeded:\nstdout: %s", o.Stdout)
	return []byte(o.Stdout)
}
