package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requireStartupsReleased fails when a startup still holds the registry. It
// checks the three places that a leaked startup shows: the entry map, which
// reports STARTING for ever; the in-flight map, which a later archive waits on;
// and the in-flight count, which blocks Shutdown's WaitForInFlight.
func requireStartupsReleased(t *testing.T, core *startupCore) {
	t.Helper()
	drained := make(chan struct{})
	go func() {
		core.WaitForInFlight()
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(inputQueueWait):
		require.FailNow(t, "a startup that nobody released still holds the in-flight count")
	}
	core.mu.Lock()
	defer core.mu.Unlock()
	assert.Empty(t, core.entries, "a startup that nobody released still reports STARTING")
	assert.Empty(t, core.inflight, "a startup that nobody released is still reachable from its tab id")
}

// requireNoCleanupClaim fails when the cleanup registry still claims id.
func requireNoCleanupClaim(t *testing.T, registry *cleanupRegistry, id string) {
	t.Helper()
	registry.mu.Lock()
	defer registry.mu.Unlock()
	assert.NotContains(t, registry.claimed, id, "a claim that nobody abandoned stays for the life of the Worker")
}
