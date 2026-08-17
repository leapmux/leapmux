package testutil

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCaptureDefaultLogger_IsReadableWhileTheCodeUnderTestWrites drives the one
// access pattern the buffer exists to support: the test goroutine polls
// String() while the code under test still logs from its own goroutines.
//
// That pattern is not hypothetical. A require.Eventually loop that waits for a
// teardown line reads the buffer on one goroutine while the handler that emits
// the line runs on another. A plain bytes.Buffer loses that race, and the race
// detector reports the read against the write, so this test fails under -race
// if the returned type stops synchronizing.
func TestCaptureDefaultLogger_IsReadableWhileTheCodeUnderTestWrites(t *testing.T) {
	const writers = 8
	const linesPerWriter = 50

	logs := CaptureDefaultLogger(t)

	// The writers park until every goroutine is up, so the writes overlap
	// instead of finishing one at a time as each goroutine starts.
	var start sync.WaitGroup
	start.Add(1)
	var written sync.WaitGroup
	written.Add(writers)
	for w := range writers {
		go func() {
			defer written.Done()
			start.Wait()
			for i := range linesPerWriter {
				slog.Info("capture", "writer", w, "seq", i)
			}
		}()
	}

	stopReading := make(chan struct{})
	var read sync.WaitGroup
	read.Add(1)
	go func() {
		defer read.Done()
		for {
			select {
			case <-stopReading:
				return
			default:
				_ = logs.String()
			}
		}
	}()

	start.Done()
	written.Wait()
	close(stopReading)
	read.Wait()

	// The lock must serialize the writes, not discard them. An unlocked
	// bytes.Buffer can also interleave two Write calls into one corrupt line,
	// which this count catches even when the race detector is off.
	got := logs.String()
	assert.Equal(t, writers*linesPerWriter, strings.Count(got, `msg=capture`),
		"every line each writer emitted must survive intact")
	for w := range writers {
		assert.Contains(t, got, fmt.Sprintf("writer=%d seq=%d", w, linesPerWriter-1),
			"the last line of each writer must reach the buffer")
	}
}

// The handler admits Debug, which is what lets a test assert that a line was
// demoted to Debug rather than deleted.
func TestCaptureDefaultLogger_AdmitsDebug(t *testing.T) {
	logs := CaptureDefaultLogger(t)

	slog.Debug("a demoted line")

	assert.Contains(t, logs.String(), "level=DEBUG")
	assert.Contains(t, logs.String(), "a demoted line")
}

// The capture lasts for the test only. The subtest is the seam: t.Cleanup runs
// when the subtest ends, so the parent can observe the restored logger.
func TestCaptureDefaultLogger_RestoresThePreviousLoggerAfterTheTest(t *testing.T) {
	prev := slog.Default()

	t.Run("captured", func(t *testing.T) {
		logs := CaptureDefaultLogger(t)
		assert.NotSame(t, prev, slog.Default(), "the capture must replace the default logger")

		slog.Info("inside the capture")

		assert.Contains(t, logs.String(), "inside the capture")
	})

	assert.Same(t, prev, slog.Default(), "the previous logger must come back")
}

// Reset drops what the setup logged, so a later assertion reads only what the
// step under test wrote.
func TestCaptureDefaultLogger_ResetDiscardsTheEarlierLines(t *testing.T) {
	logs := CaptureDefaultLogger(t)

	slog.Info("a setup line")
	require.Contains(t, logs.String(), "a setup line")

	logs.Reset()
	assert.Empty(t, logs.String(), "Reset must drop every line captured so far")

	slog.Info("the line under test")

	got := logs.String()
	assert.Contains(t, got, "the line under test")
	assert.NotContains(t, got, "a setup line", "a line from before the Reset must not come back")
}

// Reset takes the same lock as Write, so it can never truncate the buffer in
// the middle of a record. An unguarded Reset leaves a half-written line behind,
// which this test catches by requiring every surviving line to be whole.
func TestCaptureDefaultLogger_ResetKeepsEverySurvivingLineWhole(t *testing.T) {
	const writers = 4
	const linesPerWriter = 50

	logs := CaptureDefaultLogger(t)

	var start sync.WaitGroup
	start.Add(1)
	var written sync.WaitGroup
	written.Add(writers)
	for w := range writers {
		go func() {
			defer written.Done()
			start.Wait()
			for i := range linesPerWriter {
				slog.Info("reset", "writer", w, "seq", i)
			}
		}()
	}

	stopResetting := make(chan struct{})
	var resetting sync.WaitGroup
	resetting.Add(1)
	go func() {
		defer resetting.Done()
		for {
			select {
			case <-stopResetting:
				return
			default:
				logs.Reset()
			}
		}
	}()

	start.Done()
	written.Wait()
	close(stopResetting)
	resetting.Wait()

	for _, line := range strings.Split(strings.TrimSuffix(logs.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		assert.True(t, strings.HasPrefix(line, "time="),
			"a surviving line must start at a record boundary, got %q", line)
		assert.Contains(t, line, `msg=reset`, "a surviving line must be a whole record")
	}
}
