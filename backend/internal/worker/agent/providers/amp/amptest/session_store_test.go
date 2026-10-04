package amptest

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/leapmux/leapmux/util/procutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeSessionStorePeer(t *testing.T) { RunSessionStorePeer() }

func TestSessionStorePeerValidatesArgumentsAndCredentialsBeforeItsReceipt(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args []string
		env  map[string]string
	}{
		{name: "missing arguments"},
		{name: "wrong native command", args: []string{"history", "--json"}},
		{name: "inherited API key", args: []string{"threads", "list", "--json", "--limit", "500"}, env: map[string]string{"AMP_API_KEY": "controlled-forbidden-key"}},
		{name: "inherited endpoint", args: []string{"threads", "list", "--json", "--limit", "500"}, env: map[string]string{"AMP_URL": "https://controlled.invalid"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			recorded := false
			err := serveSessionStorePeer(tc.args, io.Discard, func(key string) string { return tc.env[key] }, func() error { recorded = true; return nil })
			require.Error(t, err)
			assert.False(t, recorded)
		})
	}
}

func TestSessionStorePeerPreservesReceiptAndOutputFailures(t *testing.T) {
	t.Parallel()
	args := []string{"threads", "list", "--json", "--limit", "500"}
	getenv := func(string) string { return "" }
	want := errors.New("controlled receipt failure")
	require.ErrorIs(t, serveSessionStorePeer(args, io.Discard, getenv, func() error { return want }), want)
	reader, writer := io.Pipe()
	require.NoError(t, reader.CloseWithError(want))
	t.Cleanup(func() { require.NoError(t, writer.Close()) })
	require.ErrorIs(t, serveSessionStorePeer(args, writer, getenv, func() error { return nil }), want)
}

func TestSessionRuntimeFixtureInvokesOnlyThePortablePeer(t *testing.T) {
	t.Parallel()
	home, directory := t.TempDir(), t.TempDir()
	fixture := NewSessionRuntimeFixture(t, home, directory, "^TestNativeSessionStorePeer$")
	spec, err := fixture.Locator.Resolve(t.Context(), terminal.ResolveDefaultShell(), false, "Amp")
	require.NoError(t, err)
	cmd := exec.CommandContext(t.Context(), spec.Program, append(spec.PrefixArgs, "threads", "list", "--json", "--limit", "500")...)
	cmd.Env, cmd.Dir = fixture.EnvEntries, directory
	var output bytes.Buffer
	cmd.Stdout = &output
	owner := procutil.PrepareProcess(cmd)
	require.NoError(t, owner.Start())
	require.NoError(t, owner.Wait())
	require.NoError(t, owner.Close())
	assert.JSONEq(t, `[]`, output.String())
	fixture.AssertInvoked(t)
	entries, err := os.ReadDir(home)
	require.NoError(t, err)
	assert.Empty(t, entries, "the portable reader creates no native store")
}
