package worker_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/worker"
)

// The executable that RunConfig states reaches the Worker: a running Worker
// holds one link to it in the run directory of its data directory, and the
// link goes when Run returns.
func TestRunLinksTheControlCLIWhileItRuns(t *testing.T) {
	hubURL := startOfflineHub(t)
	lease := workerLeaseForTest(t)
	dataDir := lease.Path()
	key, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)
	executable := filepath.Join(t.TempDir(), "leapmux")
	testutil.WriteExecutable(t, executable, "#!/bin/sh\necho worker-cli\n")
	linkName := "leapmux"
	if runtime.GOOS == "windows" {
		linkName = "leapmux.exe"
	}

	ready := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() {
		done <- worker.Run(ctx, worker.RunConfig{
			HubURL:         hubURL,
			Directory:      lease,
			AuthToken:      "token",
			CompositeKey:   key,
			WorkerID:       "worker-1",
			EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
			ControlCLI:     executable,
			OnReady:        func() { ready <- struct{}{} },
		})
	}()
	select {
	case <-ready:
	case <-time.After(30 * time.Second):
		cancel()
		t.Fatal("the worker never became locally ready")
	}

	links, err := filepath.Glob(filepath.Join(dataDir, "run", "cli-*", linkName))
	require.NoError(t, err)
	require.Len(t, links, 1, "a running Worker holds one link")
	linkInfo, err := os.Stat(links[0])
	require.NoError(t, err)
	targetInfo, err := os.Stat(executable)
	require.NoError(t, err)
	assert.True(t, os.SameFile(targetInfo, linkInfo), "the link stands for the stated executable")

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("the worker did not stop after the cancellation")
	}
	assert.NoFileExists(t, links[0], "the Worker removes its link when it stops")
}
