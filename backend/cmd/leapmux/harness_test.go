package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli"
	hubconfig "github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/servicelaunch"
	"github.com/leapmux/leapmux/internal/worker/config"
)

// The helpers in this file run a command through the command tree of the
// shipped binary, so a test exercises the parse, the help and the checks that
// a person's command line goes through.

// runLeapmux runs `leapmux <args...>` with runners and returns the error of
// the command, unprinted. The command writes to os.Stdin, os.Stdout and
// os.Stderr as they are when the call starts, so a test that replaces
// os.Stdout captures what the command prints.
func runLeapmux(ctx context.Context, runners cliRunners, args ...string) error {
	return cli.Run(ctx, newRootCommand(runners), args, os.Stdin, os.Stdout, os.Stderr)
}

// testCLIRunners returns the runners of the shipped binary with a fixed
// version and a native service manager that fails the test, so a test of the
// command line never reaches the service manager of this machine.
func testCLIRunners(t *testing.T) cliRunners {
	t.Helper()
	runners := defaultCLIRunners()
	runners.version = func() string { return "test-version" }
	runners.modes.controller = func(string, string) (*servicelaunch.Controller, error) {
		t.Error("the command reached the native service manager")
		return nil, errors.New("the test forbids the native service manager")
	}
	return runners
}

// cliOutcome is what one run of the binary's command line printed, and its
// exit status.
type cliOutcome struct {
	code   int
	stdout string
	stderr string
}

// runTestCLI runs `leapmux <args...>` through runCLI with testCLIRunners.
func runTestCLI(t *testing.T, args ...string) cliOutcome {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runCLI(context.Background(), args, strings.NewReader(""), &stdout, &stderr, testCLIRunners(t))
	return cliOutcome{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// captureStdout runs fn with os.Stdout replaced by a pipe, and returns what
// fn printed there with the error of fn.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	old := os.Stdout
	os.Stdout = w
	fnErr := fn()
	require.NoError(t, w.Close())
	os.Stdout = old
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(out), fnErr
}

// runModeCommand runs `leapmux <mode> <args...>` with the shipped
// dependencies.
func runModeCommand(mode string, args []string) error {
	return runLeapmux(context.Background(), defaultCLIRunners(), append([]string{mode}, args...)...)
}

// runLifecycleWithController runs `leapmux <mode> <verb> <args...>` with
// factory as the native service manager.
func runLifecycleWithController(mode, verb string, factory nativeControllerFactory, args ...string) error {
	runners := defaultCLIRunners()
	runners.modes.controller = factory
	return runLeapmux(context.Background(), runners, append([]string{mode, verb}, args...)...)
}

// runForeground runs `leapmux <mode> run <args...>` under invocation.
func runForeground(mode string, invocation runInvocation, args ...string) error {
	runners := defaultCLIRunners()
	runners.modes.foreground = invocation
	return runLeapmux(invocation.Context, runners, append([]string{mode, "run"}, args...)...)
}

// runWorkerRegister runs `leapmux worker register <args...>`.
func runWorkerRegister(args []string) error {
	return runWorkerRegisterWithFiles(context.Background(), args, configuredWorkerRegistrationFiles)
}

// runWorkerRegisterWithFiles runs `leapmux worker register <args...>` with
// filesFor as the files of the registration.
func runWorkerRegisterWithFiles(ctx context.Context, args []string, filesFor func(*config.Config) workerRegistrationFiles) error {
	runners := defaultCLIRunners()
	runners.modes.registrationFiles = filesFor
	return runLeapmux(ctx, runners, append([]string{"worker", "register"}, args...)...)
}

// runWorkerDeregister runs `leapmux worker deregister <args...>`.
func runWorkerDeregister(args []string) error {
	return runWorkerDeregisterWithFiles(context.Background(), args, configuredWorkerRegistrationFiles)
}

// runWorkerDeregisterWithFiles runs `leapmux worker deregister <args...>`
// with filesFor as the files of the registration.
func runWorkerDeregisterWithFiles(ctx context.Context, args []string, filesFor func(*config.Config) workerRegistrationFiles) error {
	runners := defaultCLIRunners()
	runners.modes.registrationFiles = filesFor
	return runLeapmux(ctx, runners, append([]string{"worker", "deregister"}, args...)...)
}

// parseTestFlags parses args into a fresh flag set that register fills, as
// the leaf of a command does, without the checks of the command tree.
func parseTestFlags[T any](args []string, register func(*pflag.FlagSet) T) (*pflag.FlagSet, T, error) {
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	flags := register(fs)
	return fs, flags, fs.Parse(args)
}

// loadWorkerRegisterCommand loads the options of `leapmux worker register
// <args...>` through the registration of its flags.
func loadWorkerRegisterCommand(args []string) (*config.Config, workerRegisterOptions, error) {
	fs, flags, err := parseTestFlags(args, registerWorkerRegisterFlags)
	if err != nil {
		return nil, workerRegisterOptions{}, err
	}
	return flags.load(fs)
}

// loadWorkerDeregisterCommand loads the options of `leapmux worker
// deregister <args...>` through the registration of its flags.
func loadWorkerDeregisterCommand(args []string) (*config.Config, workerCredentialOptions, error) {
	fs, flags, err := parseTestFlags(args, func(fs *pflag.FlagSet) *workerCredentialFlags {
		return registerWorkerCredentialFlags(fs, defaultDeregisterTimeout)
	})
	if err != nil {
		return nil, workerCredentialOptions{}, err
	}
	return flags.load(fs)
}

// runRecover runs `leapmux recover <args...>` and returns the error of the
// command.
func runRecover(t *testing.T, args ...string) error {
	t.Helper()
	_, err := runRecoverOutput(t, args...)
	return err
}

// runRecoverOutput runs `leapmux recover <args...>` and returns what it
// printed on stdout.
func runRecoverOutput(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var stdout bytes.Buffer
	err := cli.Run(context.Background(), newRootCommand(testCLIRunners(t)), append([]string{"recover"}, args...), strings.NewReader(""), &stdout, io.Discard)
	return stdout.String(), err
}

// testHubConfig is the configuration of a Hub on SQLite in dataDir, for a
// test that opens the database of a recover command itself.
func testHubConfig(dataDir string) *hubconfig.Config {
	return &hubconfig.Config{DataDir: dataDir}
}

// statusFromJSON reads what `status --json` printed: one {"data": <status>}
// envelope on one line, the envelope of every --json result, and no prose.
func statusFromJSON[T any](t *testing.T, stdout string) T {
	t.Helper()
	require.Equal(t, 1, strings.Count(stdout, "\n"), "status --json prints one line and no prose: %s", stdout)
	var envelope struct {
		Data *T `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &envelope), stdout)
	require.NotNil(t, envelope.Data, "the status is under the data key: %s", stdout)
	return *envelope.Data
}
