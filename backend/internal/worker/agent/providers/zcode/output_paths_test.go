package zcode

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestZCodeNativeSerializationPathDoesNotReadTheTextArtifact(t *testing.T) {
	t.Parallel()
	fixture := newZCodeSerializationFixture(t)
	record, err := fixture.read(t, &zcodeOutputFileCache{})
	require.NoError(t, err)
	assert.Contains(t, string(record.native.Data), fixture.path)
	var native struct {
		State struct {
			Output string `json:"output"`
		} `json:"state"`
	}
	require.NoError(t, json.Unmarshal(record.native.Data, &native))
	assert.Equal(t, "<persisted-output>native preview</persisted-output>", native.State.Output)
	assert.Empty(t, record.outputFiles, "a text serialization path must not open or store external output bytes")
	assert.True(t, record.ready, "native path metadata is ready without text file recovery")
}
