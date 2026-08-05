package crdt_test

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// Benchmarks for the /ws/userevents connect path.
//
// WHICH NUMBER MATTERS. The FALLBACK baseline is an O(all the user's entities)
// walk plus a proto.Clone of every visible record, and that cost does not go
// away -- the client still needs the bytes. What matters is whether it is paid
// WHILE HOLDING the locks the commit/broadcast pipeline needs. So
// BenchmarkMaterializedFromState and BenchmarkSubscribeWithACL_Fallback are
// recorded as controls that should stay FLAT across the lock rework (a flat
// line there is the expected result, not a null result), and
// BenchmarkCommitLatencyDuringFallbackConnect is the one that must move: it
// reports how long a commit is stalled behind a cold connect.
//
// Sizes mirror the frontend's published checkpoint figures (see
// frontend/src/lib/crdt/checkpointStore.ts) so the two halves of #358 are
// measured against the same two accounts.

// benchAccountSizes are the (nodes, tabs) fixtures every benchmark here runs.
var benchAccountSizes = []struct {
	name  string
	nodes int
	tabs  int
}{
	{"400x600", 400, 600},
	{"2400x4800", 2400, 4800},
}

// benchSeedAccount installs `nodes` live nodes and `tabs` live tabs under
// workspace "w1"'s root, directly into the manager's state.
//
// SeedStateForTest rather than SubmitInternal: this is exactly the case its doc
// reserves -- a fixture that cannot practically be expressed as a valid op
// batch, since 2400 nodes through the validator would take longer to build than
// the benchmark takes to run, and the shape (not the provenance) is what is
// being measured.
//
// The tree is a chain of SPLIT parents each carrying a fanout of LEAF children,
// so buildNodeWorkspaceMap's BFS actually has to descend rather than resolving
// every node from the root in one hop.
func benchSeedAccount(tb testing.TB, mgr *crdt.Manager, rootID string, nodes, tabs int) {
	tb.Helper()
	const fanout = 8
	// A SPLIT is INCOMPLETE without direction + ratios (completenessCheck in
	// validate.go), and every later real batch re-validates the whole live
	// record set -- so a seeded shortcut here would reject the first commit
	// against this fixture, not just the seed.
	split := func() *leapmuxv1.NodeRecord {
		ratios := make([]float64, fanout)
		for i := range ratios {
			ratios[i] = 1.0 / float64(fanout)
		}
		return &leapmuxv1.NodeRecord{
			Kind:      &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_SPLIT},
			Direction: &leapmuxv1.LWWDirection{Value: leapmuxv1.SplitDirection_SPLIT_DIRECTION_HORIZONTAL},
			Ratios:    &leapmuxv1.LWWDoubles{Value: &leapmuxv1.DoubleList{Values: ratios}},
		}
	}
	mgr.SeedStateForTest(func(state *leapmuxv1.UserCrdtState) {
		// The root gains children below, so it can no longer be the LEAF
		// seedRootInternal registered.
		root := split()
		root.NodeId = rootID
		state.Nodes[rootID] = root

		leaves := make([]string, 0, nodes)
		parent := rootID
		for i := range nodes {
			id := fmt.Sprintf("n%05d", i)
			rec := &leapmuxv1.NodeRecord{
				NodeId:   id,
				Kind:     &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_LEAF},
				Position: &leapmuxv1.LWWString{Value: fmt.Sprintf("p%05d", i)},
			}
			// Every `fanout`th node becomes the next SPLIT parent, so the tree
			// has depth ~nodes/fanout instead of being one flat layer and the
			// projection's BFS actually has to descend.
			if i%fanout == fanout-1 {
				rec = split()
				rec.NodeId = id
				rec.Position = &leapmuxv1.LWWString{Value: fmt.Sprintf("p%05d", i)}
			}
			rec.ParentId = parent
			state.Nodes[id] = rec
			if rec.GetKind().GetValue() == leapmuxv1.NodeKind_NODE_KIND_SPLIT {
				parent = id
			} else {
				leaves = append(leaves, id)
			}
		}
		if len(leaves) == 0 {
			leaves = append(leaves, rootID)
		}
		for i := range tabs {
			id := fmt.Sprintf("t%05d", i)
			state.Tabs[id] = &leapmuxv1.TabRecord{
				TabType:  leapmuxv1.TabType_TAB_TYPE_AGENT,
				TabId:    id,
				TileId:   &leapmuxv1.LWWString{Value: leaves[i%len(leaves)]},
				Position: &leapmuxv1.LWWString{Value: fmt.Sprintf("q%05d", i)},
				WorkerId: &leapmuxv1.LWWString{Value: "worker-1"},
			}
		}
	})
}

// benchManager builds a started manager seeded with `nodes`/`tabs` under "w1".
func benchManager(tb testing.TB, nodes, tabs int) *crdt.Manager {
	tb.Helper()
	mgr, _, _ := runManager(tb, "user-1", allowAll{}, 230_000)
	seedRootInternal(tb, mgr, "w1", "root1")
	benchSeedAccount(tb, mgr, "root1", nodes, tabs)
	return mgr
}

// BenchmarkMaterializedFromState measures the projection build alone.
//
// CONTROL, expected to stay flat: the walk and the per-record clone are the
// cost the client's snapshot is made of, and moving them off the manager's
// locks does not make them cheaper.
func BenchmarkMaterializedFromState(b *testing.B) {
	for _, size := range benchAccountSizes {
		b.Run(size.name, func(b *testing.B) {
			mgr := benchManager(b, size.nodes, size.tabs)
			filter := crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"w1": true}}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				_ = mgr.Materialized(filter)
			}
		})
	}
}

// BenchmarkSubscribeWithACL_Fallback measures one full cold connect (no
// cursor). CONTROL, expected to stay flat -- same reason as above.
func BenchmarkSubscribeWithACL_Fallback(b *testing.B) {
	for _, size := range benchAccountSizes {
		b.Run(size.name, func(b *testing.B) {
			mgr := benchManager(b, size.nodes, size.tabs)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				sub := &crdt.Subscriber{UserID: "user-1", Send: (&captureSubscriber{}).send}
				out, err := mgr.SubscribeWithACL(context.Background(), sub, nil, 0, resolveAll)
				if err != nil {
					b.Fatalf("subscribe: %v", err)
				}
				out.Unsub()()
			}
		})
	}
}

// BenchmarkSubscribeWithACL_Resume is the contrast: the same connect with a
// live cursor, which ships a delta instead of a snapshot. It is what a seeded
// new tab (issue #358's client half) pays instead of the fallback above.
func BenchmarkSubscribeWithACL_Resume(b *testing.B) {
	for _, size := range benchAccountSizes {
		b.Run(size.name, func(b *testing.B) {
			mgr := benchManager(b, size.nodes, size.tabs)
			submitNodePositionBatch(b, mgr, "cursor-batch", "n00000", "c0")
			snap := mgr.Materialized(crdt.SubscriberFilter{})
			cursor, epoch := snap.GetMaxHlc(), snap.GetCurrentEpoch()
			submitNodePositionBatch(b, mgr, "tail-batch", "n00000", "c1")
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				sub := &crdt.Subscriber{UserID: "user-1", Send: (&captureSubscriber{}).send}
				out, err := mgr.SubscribeWithACL(context.Background(), sub, crdt.HLCClone(cursor), epoch, resolveAll)
				if err != nil {
					b.Fatalf("subscribe: %v", err)
				}
				if out.Mode() != crdt.SubscribeDelta {
					b.Fatalf("wanted a delta, got %v", out.Mode())
				}
				out.Unsub()()
			}
		})
	}
}

// BenchmarkCommitLatencyDuringFallbackConnect is THE number.
//
// A background goroutine commits 1-op batches continuously while the benchmark
// body runs cold connects, and the reported metrics are that committer's
// latency -- p99 and max. A commit that lands while a FALLBACK baseline is
// being built waits for it: `commitState` needs m.mu.Lock and `broadcastBatch`
// needs m.projection, and today the baseline holds both.
//
// So `max-commit-ms` is the stall this change exists to remove. It should fall
// from roughly the baseline-build time to roughly a 1-op commit.
func BenchmarkCommitLatencyDuringFallbackConnect(b *testing.B) {
	for _, size := range benchAccountSizes {
		b.Run(size.name, func(b *testing.B) {
			mgr := benchManager(b, size.nodes, size.tabs)

			var (
				mu       sync.Mutex
				samples  []time.Duration
				firstErr error
			)
			stop := make(chan struct{})
			done := make(chan struct{})
			go func() {
				defer close(done)
				for i := 0; ; i++ {
					select {
					case <-stop:
						return
					default:
					}
					start := time.Now()
					_, err := mgr.SubmitInternal(context.Background(), crdt.SubmitInput{
						Batches: []*leapmuxv1.OpBatch{{
							BatchId: fmt.Sprintf("bench-commit-%d", i),
							Ops: []*leapmuxv1.CrdtOp{{
								OpId: fmt.Sprintf("bench-op-%d", i),
								Body: &leapmuxv1.CrdtOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
									NodeId: "n00000",
									Field:  &leapmuxv1.SetNodeRegisterOp_Position{Position: fmt.Sprintf("z%06d", i)},
								}},
							}},
						}},
					})
					elapsed := time.Since(start)
					mu.Lock()
					if err != nil {
						// Remembered, not swallowed: a committer that fails on
						// every call would otherwise produce zero samples and be
						// reported as a SKIP, quietly deleting the one number
						// this benchmark exists for.
						if firstErr == nil {
							firstErr = err
						}
					} else {
						samples = append(samples, elapsed)
					}
					mu.Unlock()
				}
			}()

			b.ResetTimer()
			for range b.N {
				sub := &crdt.Subscriber{UserID: "user-1", Send: (&captureSubscriber{}).send}
				out, err := mgr.SubscribeWithACL(context.Background(), sub, nil, 0, resolveAll)
				if err != nil {
					b.Fatalf("subscribe: %v", err)
				}
				out.Unsub()()
			}
			b.StopTimer()

			close(stop)
			<-done
			mu.Lock()
			collected := append([]time.Duration(nil), samples...)
			commitErr := firstErr
			mu.Unlock()
			// FAIL rather than skip. max-commit-ms is the number the whole
			// server-side change is justified by, and a skip reads like an
			// environment problem -- so a change that made every background
			// commit invalid (a mid-run epoch bump, a tightened validator, a
			// batch-id collision) would silently remove the measurement instead
			// of showing a regression.
			if len(collected) == 0 {
				b.Fatalf("the background committer produced no samples; first error: %v", commitErr)
			}
			sort.Slice(collected, func(i, j int) bool { return collected[i] < collected[j] })
			// NEAREST-RANK, and only once there are enough samples for a 99th
			// percentile to mean anything. `collected[(n*99)/100]` is n-1 --
			// the maximum -- for every n <= 100, so on a short run (a low b.N,
			// or the -benchtime=1x sanity check) the p99 and max columns were
			// the same number with nothing saying so, and the tail-vs-typical
			// contrast the reader is invited to draw did not exist.
			if len(collected) >= 100 {
				p99 := collected[(len(collected)*99+99)/100-1]
				b.ReportMetric(float64(p99.Microseconds())/1000, "p99-commit-ms")
			}
			b.ReportMetric(float64(collected[len(collected)-1].Microseconds())/1000, "max-commit-ms")
			b.ReportMetric(float64(len(collected)), "commits")
		})
	}
}
