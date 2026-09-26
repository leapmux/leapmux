package acp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fs host reads a file's text and writes it back, and reports a missing
// path as an error rather than an empty success. Dirac's edit_file needs these
// two round trips (with the negotiated capability) to run at all.
func TestFSReadWriteTextFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	require.NoError(t, os.WriteFile(path, []byte("before\n"), 0o644))

	content, err := fsReadTextFile(path)
	require.NoError(t, err)
	assert.Equal(t, "before\n", content)

	require.NoError(t, fsWriteTextFile(path, "after\n"))
	written, err := fsReadTextFile(path)
	require.NoError(t, err)
	assert.Equal(t, "after\n", written)

	_, err = fsReadTextFile(filepath.Join(dir, "missing.txt"))
	assert.Error(t, err)
	assert.Error(t, fsWriteTextFile("", "x"))
	_, err = fsReadTextFile("")
	assert.Error(t, err)
}

// The fs host folds the request's arguments into the open tool call that states
// none: a filesystem runtime opens the call and then asks the host to read or
// write, so the request is where the path first appears.
func TestLatestOpenToolWithoutInput_FlagsTheInputlessCall(t *testing.T) {
	t.Parallel()

	var out acpTurnOutput
	out.rememberIncompleteTool("with-input", map[string]json.RawMessage{
		"rawInput": json.RawMessage(`{"path":"a"}`),
	}, []byte(`{}`))
	assert.Equal(t, "", out.latestOpenToolWithoutInput(), "a call that states its input is not the one the fs request enriches")

	out.rememberIncompleteTool("without-input", map[string]json.RawMessage{
		"kind": json.RawMessage(`"read"`),
	}, []byte(`{}`))
	assert.Equal(t, "without-input", out.latestOpenToolWithoutInput())

	out.rememberIncompleteTool("later-without-input", map[string]json.RawMessage{
		"rawInput": json.RawMessage(`{}`),
	}, []byte(`{}`))
	assert.Equal(t, "later-without-input", out.latestOpenToolWithoutInput(),
		"an empty object states no input, and the most recently opened call wins")
}
