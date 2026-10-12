package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// helperRegistry registers one provider with one helper that records what it
// received and answers the exit code the test states.
func helperRegistry(t *testing.T, provider leapmuxv1.AgentProvider, name string, run agent.HelperFunc) *agent.Registry {
	t.Helper()
	reg := testRegistration(provider)
	reg.Helpers = map[string]agent.HelperFunc{name: run}
	return agenttest.MustNewRegistry(reg)
}

func writeSpec(t *testing.T, spec agent.HelperSpec) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "helper.json")
	env, err := agent.WriteHelperSpec(path, spec)
	require.NoError(t, err)
	assert.Equal(t, contracts.EnvAgentHelper+"="+path, env, "the entry points a helper run at the file")
	return path
}

func TestWriteHelperSpecRoundTripsThroughReadHelperSpec(t *testing.T) {
	t.Parallel()

	want := agent.HelperSpec{
		Provider: "AGENT_PROVIDER_AMP",
		Helper:   "permission",
		Config:   json.RawMessage(`{"endpoint":"127.0.0.1:1"}`),
	}
	path := writeSpec(t, want)
	got, err := agent.ReadHelperSpec(path)
	require.NoError(t, err)
	assert.Equal(t, want.Provider, got.Provider)
	assert.Equal(t, want.Helper, got.Helper)
	assert.JSONEq(t, string(want.Config), string(got.Config))

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "only the owner reads a spec that can hold a secret")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "the temporary file is renamed, not left behind")
}

func TestWriteHelperSpecReplacesAnEarlierSpec(t *testing.T) {
	t.Parallel()

	path := writeSpec(t, agent.HelperSpec{Provider: "AGENT_PROVIDER_AMP", Helper: "first"})
	_, err := agent.WriteHelperSpec(path, agent.HelperSpec{Provider: "AGENT_PROVIDER_AMP", Helper: "second"})
	require.NoError(t, err)
	got, err := agent.ReadHelperSpec(path)
	require.NoError(t, err)
	assert.Equal(t, "second", got.Helper)
}

func TestWriteHelperSpecRefusesAnUnusableSpec(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for name, tc := range map[string]struct {
		path   string
		spec   agent.HelperSpec
		reason string
	}{
		"a relative path": {"helper.json", agent.HelperSpec{Provider: "P", Helper: "h"}, "not absolute"},
		"no provider":     {filepath.Join(dir, "a.json"), agent.HelperSpec{Helper: "h"}, "no provider"},
		"no helper":       {filepath.Join(dir, "b.json"), agent.HelperSpec{Provider: "P", Helper: "  "}, "no helper"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := agent.WriteHelperSpec(tc.path, tc.spec)
			require.ErrorContains(t, err, tc.reason)
		})
	}
}

func TestReadHelperSpecRefusesAnUnusableFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
		return path
	}
	for name, tc := range map[string]struct {
		path   string
		reason string
	}{
		"an absent file":      {filepath.Join(dir, "absent.json"), "open helper spec"},
		"a file of no JSON":   {write("garbage.json", "not json"), "decode helper spec"},
		"an empty object":     {write("empty.json", "{}"), "no provider"},
		"an oversized file":   {write("big.json", `{"provider":"P","helper":"h","config":"`+strings.Repeat("x", 70<<10)+`"}`), "exceeds"},
		"a spec of no helper": {write("nohelper.json", `{"provider":"P"}`), "no helper"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := agent.ReadHelperSpec(tc.path)
			require.ErrorContains(t, err, tc.reason)
		})
	}
}

func TestRunHelperDispatchesToTheProvidersHelper(t *testing.T) {
	t.Parallel()

	var got agent.HelperInvocation
	registry := helperRegistry(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_AMP, "permission",
		func(_ context.Context, invocation agent.HelperInvocation) int {
			got = invocation
			_, _ = invocation.Stdout.Write([]byte("out"))
			return 7
		})
	path := writeSpec(t, agent.HelperSpec{
		Provider: "AGENT_PROVIDER_AMP",
		Helper:   "permission",
		Config:   json.RawMessage(`{"k":"v"}`),
	})
	var stdout, stderr bytes.Buffer
	stdin := strings.NewReader("in")
	getenv := func(key string) string { return "env:" + key }

	code := registry.RunHelper(context.Background(), path, agent.HelperInvocation{
		Stdin: stdin, Stdout: &stdout, Stderr: &stderr, Getenv: getenv,
	})

	assert.Equal(t, 7, code, "the helper's own exit code is the process's")
	assert.JSONEq(t, `{"k":"v"}`, string(got.Config), "the helper receives the provider's configuration")
	assert.Same(t, stdin, got.Stdin)
	assert.Equal(t, "env:X", got.Getenv("X"))
	assert.Equal(t, "out", stdout.String())
	assert.Empty(t, stderr.String(), "the generic layer writes nothing a CLI could read as a refusal reason")
}

func TestRunHelperDefaultsTheEnvironmentToTheProcess(t *testing.T) {
	t.Parallel()

	var getenv func(string) string
	registry := helperRegistry(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_AMP, "permission",
		func(_ context.Context, invocation agent.HelperInvocation) int {
			getenv = invocation.Getenv
			return 0
		})
	path := writeSpec(t, agent.HelperSpec{Provider: "AGENT_PROVIDER_AMP", Helper: "permission"})
	require.Equal(t, 0, registry.RunHelper(context.Background(), path, agent.HelperInvocation{
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
	}))
	require.NotNil(t, getenv)
	assert.Equal(t, os.Getenv("PATH"), getenv("PATH"))
}

func TestRunHelperRefusesASpecItCannotServe(t *testing.T) {
	t.Parallel()

	registry := helperRegistry(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_AMP, "permission",
		func(context.Context, agent.HelperInvocation) int {
			t.Error("no helper runs for a spec the registry cannot serve")
			return 0
		})
	for name, tc := range map[string]struct {
		spec   *agent.HelperSpec
		reason string
	}{
		"an absent spec":           {nil, "open helper spec"},
		"an unknown provider":      {&agent.HelperSpec{Provider: "AGENT_PROVIDER_NOPE", Helper: "permission"}, `unknown provider "AGENT_PROVIDER_NOPE"`},
		"the unspecified provider": {&agent.HelperSpec{Provider: "AGENT_PROVIDER_UNSPECIFIED", Helper: "permission"}, "unknown provider"},
		"an unregistered provider": {&agent.HelperSpec{Provider: "AGENT_PROVIDER_PI", Helper: "permission"}, `AGENT_PROVIDER_PI registers no helper "permission"`},
		"an unknown helper":        {&agent.HelperSpec{Provider: "AGENT_PROVIDER_AMP", Helper: "question"}, `registers no helper "question"`},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "absent.json")
			if tc.spec != nil {
				path = writeSpec(t, *tc.spec)
			}
			var stderr bytes.Buffer
			code := registry.RunHelper(context.Background(), path, agent.HelperInvocation{
				Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &stderr,
			})
			assert.Equal(t, agent.HelperExitUnusable, code)
			assert.Contains(t, stderr.String(), tc.reason, "the reason reaches stderr, where the CLI shows it")
		})
	}
}

func TestRegistryHelperAnswersNothingForAnUnknownProvider(t *testing.T) {
	t.Parallel()

	registry := helperRegistry(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_AMP, "permission",
		func(context.Context, agent.HelperInvocation) int { return 0 })
	_, ok := registry.Helper(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, "permission")
	assert.False(t, ok)
	_, ok = registry.Helper(leapmuxv1.AgentProvider_AGENT_PROVIDER_AMP, "permission")
	assert.True(t, ok)
}

// The cap on the spec file is inclusive: a spec of exactly 64 KiB is one that
// the worker could write, and one byte more is not.
func TestReadHelperSpecAcceptsASpecThatTakesTheWholeCap(t *testing.T) {
	t.Parallel()

	const specCap = 64 << 10
	dir := t.TempDir()
	write := func(name string, size int) string {
		prefix, suffix := `{"provider":"P","helper":"h","config":"`, `"}`
		content := prefix + strings.Repeat("x", size-len(prefix)-len(suffix)) + suffix
		require.Len(t, content, size)
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
		return path
	}

	spec, err := agent.ReadHelperSpec(write("exact.json", specCap))
	require.NoError(t, err)
	assert.Equal(t, "h", spec.Helper)
	_, err = agent.ReadHelperSpec(write("over.json", specCap+1))
	require.ErrorContains(t, err, "exceeds")
}

// The helper receives the context of the run, which ends at a signal (see
// worker.RunAgentHelper).
func TestRunHelperPassesTheContextOn(t *testing.T) {
	t.Parallel()

	type key struct{}
	var got context.Context
	registry := helperRegistry(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_AMP, "permission",
		func(ctx context.Context, _ agent.HelperInvocation) int {
			got = ctx
			return 0
		})
	path := writeSpec(t, agent.HelperSpec{Provider: "AGENT_PROVIDER_AMP", Helper: "permission"})
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), key{}, "run"))
	cancel()
	require.Equal(t, 0, registry.RunHelper(ctx, path, agent.HelperInvocation{
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
	}))
	require.NotNil(t, got)
	assert.Equal(t, "run", got.Value(key{}))
	assert.ErrorIs(t, got.Err(), context.Canceled, "the helper sees that its run ended")
}
