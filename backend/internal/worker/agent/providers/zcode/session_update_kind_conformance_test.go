package zcode

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// zcodeSessionUpdateKindWords maps the corpus words onto the Go vocabulary.
// The browser replay maps the same words onto its own union, so the corpus is
// the one place the two vocabularies are pinned together.
var zcodeSessionUpdateKindWords = map[string]zcodeSessionUpdateKind{
	"drop":             zcodeSessionUpdateDrop,
	"networkStatus":    zcodeSessionUpdateNetworkStatus,
	"backgroundTask":   zcodeSessionUpdateBackgroundTask,
	"subagent":         zcodeSessionUpdateSubagent,
	"modelResponse":    zcodeSessionUpdateModelResponse,
	"goalVerification": zcodeSessionUpdateGoalVerification,
	"goalChange":       zcodeSessionUpdateGoalChange,
	"modeChange":       zcodeSessionUpdateModeChange,
	"compaction":       zcodeSessionUpdateCompaction,
	"microcompact":     zcodeSessionUpdateMicrocompact,
	"modelSelected":    zcodeSessionUpdateModelSelected,
	"hookRun":          zcodeSessionUpdateHookRun,
	"hookAdmission":    zcodeSessionUpdateHookAdmission,
	"steerDiscarded":   zcodeSessionUpdateSteerDiscarded,
}

// TestZCodeSessionUpdateKindConformance replays the shared kind corpus.
//
// The browser replays the same file against `zcodeSessionUpdateKind` in the
// provider plugin. A divergence between the two copies classifies a stored row
// differently on the two sides: the worker persists one kind of notification
// while the browser draws another.
func TestZCodeSessionUpdateKindConformance(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(testutil.RepoPath(t, "testdata", "zcode_session_update_kind_conformance.json"))
	require.NoError(t, err)
	var fixture struct {
		Cases []struct {
			Name    string          `json:"name"`
			Payload json.RawMessage `json:"payload"`
			Kind    string          `json:"kind"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))
	// Every kind in the vocabulary needs a corpus witness. A corpus that loses
	// one lets a divergence on that kind pass both suites: the worker persists
	// one kind of notification while the browser draws another.
	seenKinds := make(map[string]bool, len(fixture.Cases))
	for _, tc := range fixture.Cases {
		seenKinds[tc.Kind] = true
	}
	for word := range zcodeSessionUpdateKindWords {
		assert.Truef(t, seenKinds[word], "no corpus case pins the %s kind", word)
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			want, known := zcodeSessionUpdateKindWords[tc.Kind]
			require.True(t, known, "the corpus states a kind that is not in the vocabulary: %s", tc.Kind)
			var payload zcodeSessionUpdatedPayload
			require.NoError(t, json.Unmarshal(tc.Payload, &payload))
			assert.Equal(t, want, payload.kind())
		})
	}
}
