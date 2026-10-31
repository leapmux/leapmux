package crdt_test

import (
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/internal/hub/crdt"
)

var benchmarkProjectionDiff crdt.IndexDiff

func BenchmarkDiffProjection(b *testing.B) {
	for _, count := range []int{0, 1, 1000, 10000} {
		for _, scenario := range []string{"unchanged", "rendered change", "removed"} {
			b.Run(fmt.Sprintf("%d/%s", count, scenario), func(b *testing.B) {
				previous := &crdt.Projection{UserID: "user-1"}
				next := &crdt.Projection{UserID: "user-1"}
				for i := range count {
					row := projectionWithTab("workspace", fmt.Sprintf("tab-%d", i), "tile", "position").OwnedTabs[0]
					previous.OwnedTabs = append(previous.OwnedTabs, row)
					previous.RenderedTabs = append(previous.RenderedTabs, row)
					next.OwnedTabs = append(next.OwnedTabs, row)
					if scenario != "rendered change" || i != 0 {
						next.RenderedTabs = append(next.RenderedTabs, row)
					}
				}
				if scenario == "removed" {
					next = &crdt.Projection{UserID: "user-1"}
				}
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					benchmarkProjectionDiff = crdt.DiffProjection(previous, next)
				}
			})
		}
	}
}
