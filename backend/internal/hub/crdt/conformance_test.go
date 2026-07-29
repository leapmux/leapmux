package crdt_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// This file is one half of a cross-language contract. The other half is
// frontend/src/lib/crdt/conformance.test.ts, and both read
// testdata/crdt_projection_conformance.json -- read that file's _readme first.
//
// `frontend/src/lib/crdt/{apply,project}.ts` re-implements this package's merge
// and tab-projection rules, because the client has to project optimistically
// before the hub has seen an op. Two hand-written implementations of one
// specification drift, and this one did: the sides came to disagree about which
// tile a collapsed split's tabs report, with both suites green throughout. A
// shared fixture is what makes that visible -- change the rules on one side
// only, and the other side's suite fails on the same case.
//
// If a case here goes red, "update the expectation" is almost never the fix.

// conformanceFixture mirrors the JSON exactly. `Why` is documentation, not
// asserted on -- the same convention as bindAddrConformanceFixture in
// desktop/go/tunnel_test.go.
type conformanceFixture struct {
	UserID string `json:"userId"`
	Cases  []struct {
		Name string `json:"name"`
		Why  string `json:"why"`
		// Ops stay raw here: protojson, not encoding/json, has to decode them,
		// because a CrdtOp is a protobuf message with a oneof body.
		Ops    []json.RawMessage `json:"ops"`
		Expect struct {
			Owned    []conformanceTab `json:"owned"`
			Rendered []conformanceTab `json:"rendered"`
		} `json:"expect"`
	} `json:"cases"`
}

// conformanceTab is the projected row as the fixture spells it.
type conformanceTab struct {
	TabType     string `json:"tabType"`
	TabID       string `json:"tabId"`
	WorkspaceID string `json:"workspaceId"`
	TileID      string `json:"tileId"`
	WorkerID    string `json:"workerId"`
	Position    string `json:"position"`
}

// loadProjectionConformance reads the fixture shared with the frontend suite.
//
// go test runs with CWD = the package dir, so the repo-level testdata/ dir --
// the one home reachable from both this package and frontend/src/lib/crdt -- is
// four levels up. A directory the go tool ignores by name but both languages
// can read is exactly the point.
func loadProjectionConformance(t *testing.T, path string) conformanceFixture {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "the conformance fixture is shared with frontend/src/lib/crdt/conformance.test.ts")

	var fixture conformanceFixture
	require.NoError(t, json.Unmarshal(raw, &fixture))

	// A fixture that silently loads nothing would make this test pass while
	// asserting nothing -- the one failure mode a shared fixture must not have.
	require.NotEmpty(t, fixture.Cases, "fixture %s loaded no cases", path)
	require.NotEmpty(t, fixture.UserID, "fixture %s has no userId", path)

	// And the version of that failure specific to THIS fixture: a case whose
	// `ops` key is misspelled parses as an empty op log, produces an empty
	// projection, and matches an `expect` the same typo left empty. Both suites
	// would then agree on nothing, in perfect sync.
	seen := make(map[string]bool, len(fixture.Cases))
	for i, c := range fixture.Cases {
		require.NotEmpty(t, c.Name, "case %d has no name", i)
		require.False(t, seen[c.Name], "duplicate case name %q", c.Name)
		seen[c.Name] = true
		require.NotEmpty(t, c.Ops, "case %q has no ops -- check the key spelling", c.Name)
	}
	return fixture
}

// projectedTabs converts a projection slice into the fixture's shape so the
// comparison reads as data rather than as a field-by-field walk.
func projectedTabs(tabs []*crdt.RenderedTab) []conformanceTab {
	out := make([]conformanceTab, 0, len(tabs))
	for _, t := range tabs {
		out = append(out, conformanceTab{
			TabType:     t.TabType.String(),
			TabID:       t.TabID,
			WorkspaceID: t.WorkspaceID,
			TileID:      t.TileID,
			WorkerID:    t.WorkerID,
			Position:    t.Position,
		})
	}
	return out
}

// The two shared corpora, both replayed by replayConformance below.
const (
	// Curated: every case is named for the scenario it pins and is worth reading.
	conformancePath = "../../../../testdata/crdt_projection_conformance.json"
	// Generated: coverage rather than documentation. See its own _readme, and
	// `task generate-conformance-corpus`.
	conformanceCorpusPath = "../../../../testdata/crdt_projection_corpus.json"
)

// TestProjectionConformance replays each curated case and compares the
// projection against the fixture's expectation.
func TestProjectionConformance(t *testing.T) {
	replayConformance(t, conformancePath)
}

// TestProjectionConformanceCorpus replays the GENERATED corpus.
//
// Its expectations were recorded by this very implementation, so on its own it
// only pins Go against its past self -- worth having, but not the point. The
// point is that `frontend/src/lib/crdt/conformance.test.ts` replays the same
// file: any rule one side gains and the other does not turns that suite red on
// a log nobody had to think to write down.
func TestProjectionConformanceCorpus(t *testing.T) {
	replayConformance(t, conformanceCorpusPath)
}

func replayConformance(t *testing.T, path string) {
	t.Helper()
	fixture := loadProjectionConformance(t, path)

	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			state := crdt.NewState(fixture.UserID)
			for i, rawOp := range c.Ops {
				var op leapmuxv1.CrdtOp
				require.NoError(t, protojson.Unmarshal(rawOp, &op),
					"case %q op %d is not valid protobuf JSON: %s", c.Name, i, rawOp)
				// Both Apply implementations read canonical_hlc and silently
				// no-op without it, so an op missing one would assert nothing
				// on both sides at once.
				require.NotNil(t, op.GetCanonicalHlc(),
					"case %q op %d has no canonicalHlc, so Apply would ignore it", c.Name, i)
				crdt.Apply(state, &op)
			}

			proj := crdt.Project(state)
			owned := projectedTabs(proj.OwnedTabs)
			rendered := projectedTabs(proj.RenderedTabs)

			// A case that expects rows must actually produce some; otherwise a
			// silently-dropped op log would satisfy an empty-vs-empty compare.
			if len(c.Expect.Owned) > 0 {
				require.NotEmpty(t, owned, "case %q expects owned tabs but the projection produced none (%s)", c.Name, c.Why)
			}

			assert.Equal(t, c.Expect.Owned, owned, "owned tabs disagree with the shared fixture (%s)", c.Why)
			assert.Equal(t, c.Expect.Rendered, rendered, "rendered tabs disagree with the shared fixture (%s)", c.Why)
		})
	}
}
