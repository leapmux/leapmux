package deepseekharness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

type nativeImageObserverFixture struct {
	directory, program, receipts, temporary string
	config                                  map[string]any
}

// The fixture installs the same native image observer and source as production Start.
func prepareNativeImageObserverFixture(t *testing.T) nativeImageObserverFixture {
	t.Helper()
	directory := t.TempDir()
	files, err := prepareImageHook(directory)
	require.NoError(t, err)
	raw, err := os.ReadFile(files.Overlay)
	require.NoError(t, err)
	var rows []struct {
		Insert []struct {
			Name   string         `json:"name"`
			Config map[string]any `json:"config"`
		} `json:"insert"`
	}
	require.NoError(t, json.Unmarshal(raw, &rows))
	require.Len(t, rows, 1)
	require.Len(t, rows[0].Insert, 1)
	return nativeImageObserverFixture{directory: directory, program: rows[0].Insert[0].Name, receipts: files.Receipts, temporary: files.Temporary, config: rows[0].Insert[0].Config}
}
func installNativeImageObserverFixture(t *testing.T, a *Agent, f nativeImageObserverFixture) {
	t.Helper()
	a.imageReceipts = imageHookFiles{Receipts: f.receipts, Temporary: f.temporary, Overlay: filepath.Join(f.directory, "images.patch.yml")}
	a.sink = newToolTranscript(a.Context(), a.sink, a)
	a.sink.UpdateSessionID(a.sessionID)
}
