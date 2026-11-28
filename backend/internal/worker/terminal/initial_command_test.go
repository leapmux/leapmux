package terminal

import (
	"context"
	"errors"
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

func assertInitialCommandExits(t *testing.T, opts Options, wantCode int, label string) {
	t.Helper()
	ctx := testutil.DeadlineContext(t)
	var output testutil.LogBuffer
	term, err := Start(ctx, opts, quartz.NewReal(), func(chunk Output) {
		_, _ = output.Write(chunk.Data)
	})
	if err != nil {
		t.Fatalf("%s startup failed: %v\n%s", label, err, output.String())
	}
	t.Cleanup(func() {
		term.Stop()
		term.Wait()
	})
	select {
	case <-term.exitCh:
		assert.Equal(t, wantCode, term.Wait())
	case <-ctx.Done():
		screen, _ := term.ScreenSnapshot()
		t.Fatalf("%s did not exit. PTY output %q", label, screen)
	}
}

func assertInitialCommandRefusalKeepsPrivate(t *testing.T, opts Options, witnessPath string, checkBeforeRefusal func()) {
	t.Helper()
	refused := errors.New("the creation claim was refused")
	called := false
	opts.ReleaseInitialCommand = func(context.Context, func() error) error {
		called = true
		if checkBeforeRefusal != nil {
			checkBeforeRefusal()
		}
		return refused
	}
	term, err := Start(testutil.DeadlineContext(t), opts, quartz.NewReal(), func(Output) {})
	if term != nil {
		t.Cleanup(func() {
			term.Stop()
			term.Wait()
		})
	}
	require.ErrorIs(t, err, refused)
	assert.True(t, called)
	assert.Nil(t, term)
	if witnessPath != "" {
		assert.NoFileExists(t, witnessPath)
	}
	entries, err := os.ReadDir(filepath.Join(opts.InitialCommandDir, initialCommandLaunchRoot))
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestSpawnEnvStripsInheritedInitialCommandVariables(t *testing.T) {
	t.Parallel()
	env := spawnEnv([]string{
		"PATH=/usr/bin",
		initialCommandEnvFile + "=/tmp/stale",
		initialCommandEnvCshStartup + "=/tmp/csh",
		initialCommandEnvKshLogin + "=/tmp/ksh",
		"KEEP_ME=1",
	}, nil)
	for _, entry := range env {
		assert.NotContains(t, entry, initialCommandEnvPrefix)
	}
	assert.Contains(t, env, "KEEP_ME=1")
}
