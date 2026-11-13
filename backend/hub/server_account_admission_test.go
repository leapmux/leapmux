package hub

import (
	"context"
	"testing"

	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAccountInitializationRefusesUsersWithoutAnAdministratorAndReleasesLeases(t *testing.T) {
	dataDir := t.TempDir()
	cfg := &config.Config{
		DataDir: dataDir,
		Listen:  []string{locallistentest.UniqueListenURL(t, "account-init")},
		Storage: config.StorageConfig{Type: config.StorageTypeSQLite},
	}
	srv, err := NewServer(cfg, testHubLease(t, dataDir))
	require.NoError(t, err)
	storetest.SeedUser(t, srv.store, "missing-administrator")
	ctx := testutil.DeadlineContext(t)
	err = srv.Serve(ctx)
	require.ErrorContains(t, err, "users but no administrator")
	assert.Contains(t, err.Error(), "leapmux recover bootstrap create-admin")
	select {
	case <-srv.Ready():
		t.Fatal("invalid account state must not signal readiness")
	default:
	}
	select {
	case <-srv.LeaseHeld():
		t.Fatal("failed account initialization must not signal initialized admission")
	default:
	}
	require.True(t, srv.directory.Closed())

	// Reopening the same directory and serving after repair requires both
	// the directory lease and the database runtime lease to be released.
	repaired, err := NewServer(cfg, testHubLease(t, dataDir))
	require.NoError(t, err)
	hubtestutil.CreateTestAdmin(t, repaired.store)
	serveCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- repaired.Serve(serveCtx) }()
	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-done)
	})
	select {
	case <-repaired.Ready():
	case <-ctx.Done():
		t.Fatal("the repaired Hub did not become ready:", ctx.Err())
	}
}
