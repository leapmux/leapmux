package junie

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJunieOutputPathReadsOnlyNativeMetadata(t *testing.T) {
	t.Parallel()
	fixture := newJunieOutputPathFixture(t)
	require.NoError(t, os.Remove(fixture.path))
	source := &junieToolSource{home: fixture.home, workingDir: fixture.cwd}
	location := source.Locate(fixture.sessionID)
	extra, err := source.InitialSupplement(t.Context(), location.Path, fixture.original, agent.SpanInfo{SpanID: fixture.callID, SpanType: "execute", Closing: true})
	require.NoError(t, err, "a native output path does not require the output log to exist")
	require.NotEmpty(t, extra)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(extra, &fields))
	path := fields["outputFilePath"]
	require.NotEmpty(t, path)
	assert.Contains(t, string(path), fixture.path)
	assert.NotContains(t, fields, "outputFile")
	assert.NotContains(t, string(path), `"output":`)
	assert.NotContains(t, string(path), `"nativeOutput":`)
}
