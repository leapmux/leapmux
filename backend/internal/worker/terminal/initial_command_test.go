package terminal

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

type initialCommandDeadlineClock struct{ *quartz.Mock }

func (clock initialCommandDeadlineClock) NewTicker(_ time.Duration, tags ...string) *quartz.Ticker {
	return clock.Mock.NewTicker(2*initialCommandWait, tags...)
}

func TestInitialCommandAcceptsAcknowledgementPublishedBeforeDeadline(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	clock := initialCommandDeadlineClock{Mock: testutil.NewQuartzMock(t)}
	bootstrap, err := newInitialCommandBootstrap(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(bootstrap.close)
	path := bootstrap.ackPath()
	expected := "123 " + bootstrap.nonce
	firstRead := make(chan struct{})
	resume := make(chan struct{})
	resumeRead := sync.OnceFunc(func() { close(resume) })
	defer resumeRead()
	var holdFirstRead sync.Once
	result := make(chan error, 1)
	go func() {
		term := &Terminal{exitCh: make(chan struct{})}
		result <- waitForInitialCommand(ctx, term, clock, errInitialCommandUnproved, func() (bool, error) {
			ready := bootstrap.markerMatches(path, expected)
			holdFirstRead.Do(func() {
				close(firstRead)
				<-resume
			})
			return ready, nil
		})
	}()
	select {
	case <-firstRead:
	case <-ctx.Done():
		t.Fatal("the initial probe did not finish")
	}
	// Publish after the probe misses. The Worker only resumes once the
	// deadline is ready, which models a delayed poll under load.
	require.NoError(t, os.WriteFile(path, []byte(expected), 0o600))
	clock.Advance(initialCommandWait).MustWait(ctx)
	resumeRead()
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("the handoff wait did not return")
	}
}

func TestInitialCommandAcknowledgmentRequiresTheProcessAndNonce(t *testing.T) {
	t.Parallel()
	bootstrap, err := newInitialCommandBootstrap(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(bootstrap.close)
	assert.NoFileExists(t, bootstrap.commandPath())
	path := bootstrap.ackPath()
	assert.False(t, bootstrap.markerMatches(path, "124 expected"), "no startup acknowledgement exists yet")
	for _, content := range []string{"", "124", "123 expected", "124 old", "124 expected extra"} {
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
		assert.False(t, bootstrap.markerMatches(path, "124 expected"), "refuse %q", content)
	}
	require.NoError(t, os.WriteFile(path, []byte("124 expected\n"), 0o600))
	assert.True(t, bootstrap.markerMatches(path, "124 expected"))
	bootstrap.close()
	assert.NoDirExists(t, bootstrap.dir)
}

func TestCheckInitialCommandRejectsAnUnavailableExecutionPoint(t *testing.T) {
	t.Parallel()
	assert.NoError(t, CheckInitialCommand("custom", ""), "ordinary shells can start without an initial command")
	assert.ErrorContains(t, CheckInitialCommand("custom", "printf command"), "verified initial command handoff")
	assert.ErrorContains(t, CheckInitialCommand("pwsh", "before\x00after"), "null byte")
	assert.NoError(t, CheckInitialCommand(filepath.Join(t.TempDir(), "pwsh"), "Write-Output 'ready'"))
}

func TestReapInitialCommandLaunchesRemovesOnlyAbandonedLaunches(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	bootstrap, err := newInitialCommandBootstrap(parent)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(bootstrap.commandPath(), []byte("an abandoned command"), 0o600))
	unrelated := filepath.Join(parent, "unrelated")
	require.NoError(t, os.WriteFile(unrelated, []byte("keep"), 0o600))

	require.NoError(t, ReapInitialCommandLaunches(parent))
	assert.NoDirExists(t, bootstrap.dir)
	assert.FileExists(t, unrelated)
}
