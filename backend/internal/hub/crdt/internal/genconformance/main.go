// Command genconformance writes a GENERATED cross-language conformance corpus.
//
// WHY THIS EXISTS. `frontend/src/lib/crdt/{apply,project}.ts` and
// `backend/internal/hub/crdt/{state,project}.go` are two hand-written
// implementations of one specification. `testdata/crdt_projection_conformance.json`
// pins them against each other, but only for cases a human thought to write
// down -- and this repo has now seen a case whose stated rationale was wrong for
// its entire life (case 9 claimed to pin a drift it provably cannot detect).
// Nothing forces a new branch in `project.ts` to gain coverage.
//
// This searches the space instead of listing it: N pseudo-random op logs,
// replayed through BOTH implementations by their existing conformance suites.
//
// WHAT IS AND IS NOT AN ORACLE. The Go projection RECORDS the expectations. It
// is not thereby correct -- a Go bug lands here as an accepted expectation, and
// only a reviewer reading the regeneration diff will catch it. What the corpus
// does guarantee is AGREEMENT: if the two implementations diverge on any
// generated log, one of the two suites goes red. That is the drift class this
// exists for, and it is the class a hand-written fixture cannot cover.
//
// DETERMINISM. The seed is fixed, so regenerating without changing the
// generator produces a byte-identical file and an empty diff. Regeneration is a
// deliberate step (`task generate-conformance-corpus`), never part of a test
// run -- a corpus that regenerates itself would rubber-stamp every drift it was
// built to catch.
package main

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"

	"google.golang.org/protobuf/encoding/protojson"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

const (
	// Fixed so regeneration is byte-stable; bump only when deliberately
	// resampling the space, and review the resulting expectation diff.
	seed1, seed2 = 0x5EED_C0DE, 0x1DEA_F00D
	caseCount    = 120
	userID       = "corpus-user"
)

// tabRow mirrors the hand-written fixture's row shape exactly, so both existing
// loaders can replay this file with no new comparison code.
type tabRow struct {
	TabType     string `json:"tabType"`
	TabID       string `json:"tabId"`
	WorkspaceID string `json:"workspaceId"`
	TileID      string `json:"tileId"`
	WorkerID    string `json:"workerId"`
	Position    string `json:"position"`
}

type corpusCase struct {
	Name   string            `json:"name"`
	Why    string            `json:"why"`
	Ops    []json.RawMessage `json:"ops"`
	Expect struct {
		Owned    []tabRow `json:"owned"`
		Rendered []tabRow `json:"rendered"`
	} `json:"expect"`
}

type corpus struct {
	Readme []string     `json:"_readme"`
	UserID string       `json:"userId"`
	Cases  []corpusCase `json:"cases"`
}

// gen builds one random op log. It deliberately emits INVALID-ish shapes too --
// a tab on a tombstoned tile, a node whose parent never existed, a cycle -- so
// the corpus exercises the projection's repair rules, not just its happy path.
// The projection is total (it drops what it cannot resolve), so there is no
// such thing as an op log it may not be handed.
type gen struct {
	rnd      *rand.Rand
	ops      []json.RawMessage
	physical int64
	nodes    []string
	tabs     []string
	spaces   []string
	windows  []string
}

func (g *gen) emit(body string) {
	g.physical++
	raw := fmt.Sprintf(`{"canonicalHlc":{"physical":"%d","logical":"0","clientId":"g"},%s}`, g.physical, body)
	// Fail loudly at generation time rather than writing a corpus entry that
	// both suites would silently no-op on.
	var op leapmuxv1.CrdtOp
	if err := protojson.Unmarshal([]byte(raw), &op); err != nil {
		panic(fmt.Sprintf("generated an op that is not valid protobuf JSON: %v\n%s", err, raw))
	}
	g.ops = append(g.ops, json.RawMessage(raw))
}

func (g *gen) pick(xs []string) string {
	if len(xs) == 0 {
		return ""
	}
	return xs[g.rnd.IntN(len(xs))]
}

func (g *gen) build() {
	// Always start from at least one workspace, or almost every log projects
	// to nothing and the corpus asserts emptiness 120 times over.
	nSpaces := 1 + g.rnd.IntN(2)
	for i := range nSpaces {
		ws := fmt.Sprintf("w%d", i)
		root := fmt.Sprintf("r%d", i)
		g.spaces = append(g.spaces, ws)
		g.nodes = append(g.nodes, root)
		g.emit(fmt.Sprintf(`"setWorkspaceRootNode":{"workspaceId":%q,"rootNodeId":%q}`, ws, root))
		g.emit(fmt.Sprintf(`"setNodeRegister":{"nodeId":%q,"kind":"NODE_KIND_LEAF"}`, root))
	}

	// PLANTED SHAPES.
	//
	// Pure random walking does not reliably reach some shapes, and a corpus that
	// cannot reach a rule cannot pin it. Verified concretely: with only the
	// random walk below, a mutation that walks THROUGH a tombstoned intermediate
	// ancestor instead of stopping at it passed all 120 cases. The walk rarely
	// produces "a tab on a live leaf whose chain to a live root passes through a
	// dead node" -- it needs three specific nodes in a specific state at once.
	//
	// So plant that shape (and its controls) outright in a third of the cases,
	// then let the random walk mutate the result. This is a hybrid on purpose:
	// planting alone would just be a hand-written fixture with worse names.
	plant := g.rnd.IntN(3) == 0

	steps := 6 + g.rnd.IntN(18)
	for range steps {
		switch g.rnd.IntN(10) {
		case 0, 1: // create a node under an existing one
			id := fmt.Sprintf("n%d", len(g.nodes))
			// Half the time hang it off the MOST RECENT node rather than a
			// uniformly-random one. Uniform picks keep the tree shallow, and a
			// shallow tree has no INTERMEDIATE ancestors -- so a whole class of
			// chain rules (a tombstone between a tab's tile and its root) would
			// never appear in any generated log. Verified: without this, a
			// mutation that skips intermediate tombstones passes every case.
			parent := g.pick(g.nodes)
			if g.rnd.IntN(2) == 0 && len(g.nodes) > 0 {
				parent = g.nodes[len(g.nodes)-1]
			}
			g.nodes = append(g.nodes, id)
			kind := "NODE_KIND_LEAF"
			if g.rnd.IntN(3) == 0 {
				kind = "NODE_KIND_SPLIT"
			}
			g.emit(fmt.Sprintf(`"setNodeRegister":{"nodeId":%q,"kind":%q}`, id, kind))
			g.emit(fmt.Sprintf(`"setNodeRegister":{"nodeId":%q,"parentId":%q}`, id, parent))
			g.emit(fmt.Sprintf(`"setNodeRegister":{"nodeId":%q,"position":%q}`, id, string(rune('a'+g.rnd.IntN(26)))))
		case 2, 3, 4: // place or move a tab
			id := fmt.Sprintf("t%d", g.rnd.IntN(6))
			if !contains(g.tabs, id) {
				g.tabs = append(g.tabs, id)
			}
			tile := g.pick(g.nodes)
			g.emit(fmt.Sprintf(`"setTabRegister":{"tabType":"TAB_TYPE_AGENT","tabId":%q,"tileId":%q}`, id, tile))
			g.emit(fmt.Sprintf(`"setTabRegister":{"tabType":"TAB_TYPE_AGENT","tabId":%q,"position":%q}`, id, string(rune('a'+g.rnd.IntN(26)))))
			if g.rnd.IntN(2) == 0 {
				g.emit(fmt.Sprintf(`"setTabRegister":{"tabType":"TAB_TYPE_AGENT","tabId":%q,"workerId":"wk%d"}`, id, g.rnd.IntN(3)))
			}
		case 5: // reparent a node -- can create a cycle, which the projection must break
			if len(g.nodes) > 2 {
				g.emit(fmt.Sprintf(`"setNodeRegister":{"nodeId":%q,"parentId":%q}`, g.pick(g.nodes), g.pick(g.nodes)))
			}
		case 6: // tombstone a node, orphaning whatever hangs off it
			// Prefer an INTERMEDIATE node -- neither a workspace root nor the
			// newest leaf -- so the log actually exercises "the chain from a
			// tab's tile to its root passes through a dead ancestor". Picking
			// uniformly almost always hits a root or a leaf.
			if len(g.nodes) > 2 {
				mid := g.nodes[1+g.rnd.IntN(len(g.nodes)-2)]
				if g.rnd.IntN(4) == 0 {
					mid = g.pick(g.nodes)
				}
				g.emit(fmt.Sprintf(`"tombstoneNode":{"nodeId":%q}`, mid))
			} else if len(g.nodes) > 1 {
				g.emit(fmt.Sprintf(`"tombstoneNode":{"nodeId":%q}`, g.pick(g.nodes)))
			}
		case 7: // tombstone a tab, then sometimes try to resurrect it (remove-wins)
			if len(g.tabs) > 0 {
				id := g.pick(g.tabs)
				g.emit(fmt.Sprintf(`"tombstoneTab":{"tabType":"TAB_TYPE_AGENT","tabId":%q}`, id))
				if g.rnd.IntN(3) == 0 {
					g.emit(fmt.Sprintf(`"setTabRegister":{"tabType":"TAB_TYPE_AGENT","tabId":%q,"tileId":%q}`, id, g.pick(g.nodes)))
				}
			}
		case 8: // a floating window with its own root
			id := fmt.Sprintf("fw%d", len(g.windows))
			root := fmt.Sprintf("fwr%d", len(g.windows))
			g.windows = append(g.windows, id)
			g.nodes = append(g.nodes, root)
			g.emit(fmt.Sprintf(`"setNodeRegister":{"nodeId":%q,"kind":"NODE_KIND_LEAF"}`, root))
			g.emit(fmt.Sprintf(`"setFloatingWindowRegister":{"windowId":%q,"workspaceId":%q}`, id, g.pick(g.spaces)))
			g.emit(fmt.Sprintf(`"setFloatingWindowRegister":{"windowId":%q,"rootNodeId":%q}`, id, root))
		case 9: // tombstone a workspace or a window -- ownership must follow
			if g.rnd.IntN(2) == 0 && len(g.windows) > 0 {
				g.emit(fmt.Sprintf(`"tombstoneFloatingWindow":{"windowId":%q}`, g.pick(g.windows)))
			} else if len(g.spaces) > 1 {
				g.emit(fmt.Sprintf(`"tombstoneWorkspace":{"workspaceId":%q}`, g.pick(g.spaces)))
			}
		}
	}

	// Planted LAST, deliberately. Planting before the walk let the walk destroy
	// the shape again -- reparenting one of the three nodes, or tombstoning the
	// workspace under them -- and a shape that does not survive into the final
	// state pins nothing. Emitting it afterwards means these ops have the
	// highest HLCs in the log, so nothing overwrites them.
	if plant {
		g.plantChain()
	}
}

// plantChain builds root -> mid -> leaf with a tab on the leaf, then tombstones
// `mid`. The tab's tile is alive and IS a leaf, and the chain still reaches a
// registered root -- so the only thing standing between it and the projection
// is the dead ancestor. That makes the case sensitive to exactly one rule:
// whether an intermediate tombstone stops the walk.
func (g *gen) plantChain() {
	base := len(g.nodes)
	// Plant under a workspace root created at the very start and never
	// tombstoned by the walk (it only ever tombstones a workspace when there is
	// more than one, and picks randomly) -- so re-register it here to be sure.
	ws, root := "pw", "pr"
	g.emit(fmt.Sprintf(`"setWorkspaceRootNode":{"workspaceId":%q,"rootNodeId":%q}`, ws, root))
	g.emit(fmt.Sprintf(`"setNodeRegister":{"nodeId":%q,"kind":"NODE_KIND_SPLIT"}`, root))
	mid := fmt.Sprintf("pm%d", base)
	leaf := fmt.Sprintf("pl%d", base)
	tab := fmt.Sprintf("pt%d", base)

	g.emit(fmt.Sprintf(`"setNodeRegister":{"nodeId":%q,"kind":"NODE_KIND_SPLIT"}`, mid))
	g.emit(fmt.Sprintf(`"setNodeRegister":{"nodeId":%q,"parentId":%q}`, mid, root))
	g.emit(fmt.Sprintf(`"setNodeRegister":{"nodeId":%q,"position":"m"}`, mid))
	g.emit(fmt.Sprintf(`"setNodeRegister":{"nodeId":%q,"kind":"NODE_KIND_LEAF"}`, leaf))
	g.emit(fmt.Sprintf(`"setNodeRegister":{"nodeId":%q,"parentId":%q}`, leaf, mid))
	g.emit(fmt.Sprintf(`"setNodeRegister":{"nodeId":%q,"position":"m"}`, leaf))
	g.emit(fmt.Sprintf(`"setTabRegister":{"tabType":"TAB_TYPE_AGENT","tabId":%q,"tileId":%q}`, tab, leaf))
	g.emit(fmt.Sprintf(`"setTabRegister":{"tabType":"TAB_TYPE_AGENT","tabId":%q,"position":"m"}`, tab))
	g.emit(fmt.Sprintf(`"setTabRegister":{"tabType":"TAB_TYPE_AGENT","tabId":%q,"workerId":"wk0"}`, tab))
	// Half the cases leave the chain intact, so the corpus carries the control
	// as well as the subject -- a rule change that drops BOTH is distinguishable
	// from one that drops only the tombstoned variant.
	if g.rnd.IntN(2) == 0 {
		g.emit(fmt.Sprintf(`"tombstoneNode":{"nodeId":%q}`, mid))
	}
	g.nodes = append(g.nodes, root, mid, leaf)
	g.spaces = append(g.spaces, ws)
	g.tabs = append(g.tabs, tab)
}

// contains reports whether xs holds x.
//
// A plain linear scan, deliberately: the only caller passes `g.tabs`, which is
// appended in walk order and never sorted. The previous implementation ANDed
// this scan with `sort.SearchStrings(xs, x) < len(xs)`, which is only
// meaningful on sorted input -- on `["t3","t1"]` the binary search for "t3"
// returns 2, so the guard was false and `contains` answered NO for an element
// that is present. Every such false negative let the caller append a duplicate
// id, skewing the later `pick(g.tabs)` draws toward already-seen tabs.
func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func rows(tabs []*crdt.RenderedTab) []tabRow {
	out := make([]tabRow, 0, len(tabs))
	for _, t := range tabs {
		out = append(out, tabRow{
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

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: genconformance <output.json>")
		os.Exit(2)
	}
	rnd := rand.New(rand.NewPCG(seed1, seed2))

	out := corpus{
		Readme: []string{
			"GENERATED -- do not hand-edit. Regenerate with `task generate-conformance-corpus`.",
			"",
			"A differential corpus for the two hand-written CRDT implementations:",
			"  - backend/internal/hub/crdt/{state,project}.go",
			"  - frontend/src/lib/crdt/{apply,project}.ts",
			"Both conformance suites replay every case below. If the two ever disagree",
			"on a generated op log, one of them goes red -- without anyone having",
			"thought to write that case down.",
			"",
			"This COMPLEMENTS testdata/crdt_projection_conformance.json rather than",
			"replacing it. That file is the curated corpus: each case is named for the",
			"scenario it pins and is worth reading. This one is coverage, not",
			"documentation -- the logs are random and the names are indices.",
			"",
			"THE EXPECTATIONS WERE RECORDED BY THE GO PROJECTION. That makes Go the",
			"recorder, NOT the oracle: a Go-side bug lands here as an accepted",
			"expectation, and only a reviewer reading the regeneration diff catches",
			"it. What this file does guarantee is AGREEMENT between the two sides.",
			"",
			"Regeneration is deliberate and never part of a test run. A corpus that",
			"regenerated itself would rubber-stamp exactly the drift it exists to catch.",
		},
		UserID: userID,
	}

	for i := range caseCount {
		g := &gen{rnd: rnd}
		g.build()

		state := crdt.NewState(userID)
		for _, raw := range g.ops {
			var op leapmuxv1.CrdtOp
			if err := protojson.Unmarshal(raw, &op); err != nil {
				panic(err)
			}
			crdt.Apply(state, &op)
		}
		proj := crdt.Project(state)

		c := corpusCase{
			Name: fmt.Sprintf("generated case %03d", i),
			Why:  "generated differential coverage; see this file's _readme",
			Ops:  g.ops,
		}
		c.Expect.Owned = rows(proj.OwnedTabs)
		c.Expect.Rendered = rows(proj.RenderedTabs)
		out.Cases = append(out.Cases, c)
	}

	buf, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		panic(err)
	}
	buf = append(buf, '\n')
	if err := os.MkdirAll(filepath.Dir(os.Args[1]), 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(os.Args[1], buf, 0o644); err != nil {
		panic(err)
	}
	fmt.Printf("wrote %s (%d cases)\n", os.Args[1], len(out.Cases))
}
