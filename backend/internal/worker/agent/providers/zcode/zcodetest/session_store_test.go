package zcodetest

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/leapmux/leapmux/util/procutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeSessionStorePeer(t *testing.T) { RunSessionStorePeer() }

func TestSessionStorePeerRequiresTheNativeReadOnlyAcknowledgement(t *testing.T) {
	t.Parallel()
	args := []string{"app-server", "--stdio", "--prepare-storage"}
	for _, reply := range []string{"", "{invalid}", `{}`, `{"method":"startup/storagePathReady","reuse":false}`, `{"method":"foreign","reuse":true}`, `{"method":"startup/storagePathReady","reuse":true,"extra":1}`} {
		t.Run(reply, func(t *testing.T) {
			t.Parallel()
			recorded := false
			err := serveSessionStorePeer(args, t.TempDir(), strings.NewReader(reply), io.Discard, func() error { recorded = true; return nil })
			require.Error(t, err)
			assert.False(t, recorded)
		})
	}
}

func TestSessionStorePeerRejectsInvalidArgumentsAndPreservesReceiptFailure(t *testing.T) {
	t.Parallel()
	want := errors.New("controlled receipt failure")
	for _, args := range [][]string{nil, {"app-server", "--stdio"}, {"history", "--json"}} {
		err := serveSessionStorePeer(args, t.TempDir(), strings.NewReader(`{"method":"startup/storagePathReady","reuse":true}`), io.Discard, func() error { return want })
		require.Error(t, err)
		assert.NotErrorIs(t, err, want)
	}
	err := serveSessionStorePeer([]string{"app-server", "--stdio", "--prepare-storage"}, "relative-home", strings.NewReader(`{}`), io.Discard, func() error { return nil })
	require.Error(t, err)
	err = serveSessionStorePeer([]string{"app-server", "--stdio", "--prepare-storage"}, t.TempDir(), strings.NewReader(`{"method":"startup/storagePathReady","reuse":true}`), io.Discard, func() error { return want })
	require.ErrorIs(t, err, want)
	reader, writer := io.Pipe()
	require.NoError(t, reader.CloseWithError(want))
	t.Cleanup(func() { require.NoError(t, writer.Close()) })
	err = serveSessionStorePeer([]string{"app-server", "--stdio", "--prepare-storage"}, t.TempDir(), strings.NewReader(`{}`), writer, func() error { return nil })
	require.ErrorIs(t, err, want)
}

func TestSessionRuntimeFixtureUsesThePortablePreparationProtocolWithoutCreatingAStore(t *testing.T) {
	t.Parallel()
	home, directory := t.TempDir(), t.TempDir()
	fixture := NewSessionRuntimeFixture(t, home, directory, "^TestNativeSessionStorePeer$")
	spec, err := fixture.Locator.Resolve(t.Context(), terminal.ResolveDefaultShell(), false, "ZCode")
	require.NoError(t, err)
	cmd := exec.CommandContext(t.Context(), spec.Program, append(spec.PrefixArgs, "app-server", "--stdio", "--prepare-storage")...)
	cmd.Env, cmd.Dir = fixture.EnvEntries, directory
	cmd.Stdin = strings.NewReader("{\"method\":\"startup/storagePathReady\",\"reuse\":true}\n")
	var output bytes.Buffer
	cmd.Stdout = &output
	owner := procutil.PrepareProcess(cmd)
	require.NoError(t, owner.Start())
	require.NoError(t, owner.Wait())
	require.NoError(t, owner.Close())
	decoder := json.NewDecoder(&output)
	var pathReply struct {
		Method string `json:"method"`
		Params struct {
			Path string `json:"path"`
		} `json:"params"`
	}
	require.NoError(t, decoder.Decode(&pathReply))
	assert.Equal(t, "startup/storagePath", pathReply.Method)
	assert.Equal(t, filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"), pathReply.Params.Path)
	var ready struct {
		Method string `json:"method"`
	}
	require.NoError(t, decoder.Decode(&ready))
	assert.Equal(t, "startup/storagePrepared", ready.Method)
	require.ErrorIs(t, decoder.Decode(new(any)), io.EOF)
	fixture.AssertInvoked(t)
	entries, err := os.ReadDir(home)
	require.NoError(t, err)
	assert.Empty(t, entries, "the read-only peer creates no database")
}
