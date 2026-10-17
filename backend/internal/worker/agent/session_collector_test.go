package agent

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoredSessionCollectorMatchesStableSelection(t *testing.T) {
	t.Parallel()
	stamp := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	random := rand.New(rand.NewPCG(42, 91))
	input := []StoredSession{{Handle: ""}, {Handle: " \t"}, {Handle: " x "}, {Handle: "same", Title: "first"}, {Handle: "same", Title: "second"}}
	for i := range 2000 {
		input = append(input, StoredSession{Handle: fmt.Sprintf("id-%02d", random.IntN(50)), Title: fmt.Sprint(i), UpdatedAt: stamp.Add(time.Duration(random.IntN(30)))})
	}
	for _, count := range []int{0, 5, len(input)} {
		for _, limit := range []int{-1, 0, 1, 2, 50, 5000} {
			t.Run(fmt.Sprintf("records %d limit %d", count, limit), func(t *testing.T) {
				t.Parallel()
				for _, reverse := range []bool{false, true} {
					values := slices.Clone(input[:count])
					if reverse {
						slices.Reverse(values)
					}
					want := make([]StoredSession, 0, count)
					collector := NewStoredSessionCollector(limit)
					for _, value := range values {
						collector.Add(value)
						if limit > 0 {
							require.LessOrEqual(t, collector.Len(), limit)
						}
						if strings.TrimSpace(value.Handle) != "" {
							want = append(want, value)
						}
					}
					sort.SliceStable(want, func(i, j int) bool {
						if want[i].UpdatedAt.Equal(want[j].UpdatedAt) {
							return want[i].Handle < want[j].Handle
						}
						return want[i].UpdatedAt.After(want[j].UpdatedAt)
					})
					if limit > 0 && len(want) > limit {
						want = want[:limit]
					}
					got := collector.Sessions()
					assert.Equal(t, want, got)
					assert.Equal(t, len(got), cap(got))
					if len(got) > 0 {
						got[0].Title = "changed by caller"
						assert.Equal(t, want, collector.Sessions())
					}
				}
			})
		}
	}
}
