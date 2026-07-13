package channelmgr

import (
	"time"

	"github.com/leapmux/leapmux/internal/hub/auth"
)

type channelExpiryHeap []*channel

func (h channelExpiryHeap) Len() int { return len(h) }

func (h channelExpiryHeap) Less(i, j int) bool {
	// Heap entries are always At deadlines -- never/unset channels are never
	// pushed -- so order by the finite instant. A defensive stray non-At entry
	// (from a future invariant break) reads as zero and simply sorts first, where
	// expireDueChannels drops it.
	ai, _ := h[i].expiresAt.At()
	aj, _ := h[j].expiresAt.At()
	return ai.Before(aj)
}

func (h channelExpiryHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].expiryIndex = i
	h[j].expiryIndex = j
}

func (h *channelExpiryHeap) Push(value any) {
	ch := value.(*channel)
	ch.expiryIndex = len(*h)
	*h = append(*h, ch)
}

func (h *channelExpiryHeap) Pop() any {
	old := *h
	last := len(old) - 1
	ch := old[last]
	old[last] = nil
	ch.expiryIndex = -1
	*h = old[:last]
	return ch
}

func expiryDelay(d auth.CredentialDeadline) time.Duration {
	at, ok := d.At()
	if !ok {
		return 0
	}
	return max(time.Until(at), 0)
}
