package agenttest

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// supplementConformanceCase is one row of a shared corpus: the frame the agent sent,
// the envelope the worker stored beside it, and the object BOTH implementations must
// produce. The browser plugin replays the same file.
type supplementConformanceCase struct {
	Name         string          `json:"name"`
	Original     json.RawMessage `json:"original"`
	Supplemental json.RawMessage `json:"supplemental"`
	Expected     json.RawMessage `json:"expected"`
}

func loadSupplementConformance(t *testing.T, name string) []supplementConformanceCase {
	t.Helper()
	data, err := os.ReadFile(testutil.RepoPath(t, "testdata", name))
	require.NoError(t, err)
	var fixture struct {
		Cases []supplementConformanceCase `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.NotEmpty(t, fixture.Cases)
	return fixture.Cases
}

// RunSupplementConformance replays one corpus against one resolver.
//
// Each case is checked three ways. The resolve must produce the shared expectation,
// it must be IDEMPOTENT -- a row the store resolves twice must not grow a second copy
// of the join -- and it must leave both inputs byte for byte as they were, because the
// original bytes are what the Raw JSON view shows the reader.
func RunSupplementConformance(t *testing.T, name string, resolve func(agent.MessageContent) []byte) {
	t.Helper()
	for _, tc := range loadSupplementConformance(t, name) {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			original := append([]byte(nil), tc.Original...)
			supplemental := append([]byte(nil), tc.Supplemental...)
			resolved := resolve(agent.MessageContent{Original: tc.Original, Supplemental: tc.Supplemental})
			require.NotEmpty(t, resolved)
			assert.JSONEq(t, string(tc.Expected), string(resolved))
			again := resolve(agent.MessageContent{Original: resolved, Supplemental: tc.Supplemental})
			assert.JSONEq(t, string(tc.Expected), string(again), "the resolve must be idempotent")
			assert.Equal(t, original, []byte(tc.Original), "the original bytes must not change")
			assert.Equal(t, supplemental, []byte(tc.Supplemental), "the supplement bytes must not change")
		})
	}
}

// StoredSupplementCase is one row of a WRITER corpus.
//
// The ZCode and Cursor supplements are never resolved back into the frame: the worker
// writes them and the browser plugin reads them, so there is no shared resolve to
// replay. The STORED BYTES are the contract instead. This half asserts the worker
// writes exactly those bytes; the plugin's own suite asserts it reads the expected
// fields out of the same file.
type StoredSupplementCase struct {
	Name       string          `json:"name"`
	Original   json.RawMessage `json:"original"`
	Supplement json.RawMessage `json:"supplement"`
}
