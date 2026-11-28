package solo

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/worker"
)

// The launcher states the executable that serves `leapmux control`, and the
// embedded Worker receives it unchanged through the prepared startup, which is
// the route of `leapmux solo run` and of the desktop. An empty value stays
// empty: the desktop sidecar's own executable does not serve the CLI.
func TestStartPreparedHandsTheControlCLIToTheWorker(t *testing.T) {
	for name, executable := range map[string]string{"a stated executable": "/opt/leapmux/bin/leapmux", "no executable": ""} {
		t.Run(name, func(t *testing.T) {
			cfg := soloStartEnv(t, false)
			cfg.ControlCLI = executable
			d := newTestDeps()
			kill := serveUntilKilled(t, d, nil)
			t.Cleanup(kill)
			ctx, cancel := context.WithTimeout(context.Background(), soloTestTimeout)
			t.Cleanup(cancel)

			received := make(chan worker.RunConfig, 1)
			d.runWorker = func(ctx context.Context, rc worker.RunConfig) error {
				defer func() { _ = rc.Directory.Close() }()
				received <- rc
				rc.OnReady()
				rc.OnConnectionState(worker.ConnectionState{Connected: true})
				<-ctx.Done()
				return nil
			}
			prepared, err := Prepare(cfg)
			require.NoError(t, err)
			root, err := lifecycle.AcquireDirectory(lifecycle.NewDirectoryOwner(prepared.role, prepared.dataDir), prepared.role, prepared.dataDir)
			require.NoError(t, err)

			inst, err := startPrepared(ctx, prepared, root, d.deps)
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, inst.Stop()) })

			rc := <-received
			assert.Equal(t, executable, rc.ControlCLI)
		})
	}
}
