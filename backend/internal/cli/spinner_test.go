package cli

import (
	"bytes"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type gatedEraseWriter struct {
	mu               sync.Mutex
	rendered         chan struct{}
	eraseStarted     chan struct{}
	allowEraseFinish chan struct{}
	eraseFinished    atomic.Bool
	output           bytes.Buffer
}

func (w *gatedEraseWriter) Write(p []byte) (int, error) {
	s := string(p)
	if s == "\r\x1b[2K" {
		select {
		case w.eraseStarted <- struct{}{}:
		default:
		}
		<-w.allowEraseFinish
		w.mu.Lock()
		n, err := w.output.Write(p)
		w.mu.Unlock()
		w.eraseFinished.Store(true)
		return n, err
	}
	w.mu.Lock()
	n, err := w.output.Write(p)
	w.mu.Unlock()
	select {
	case w.rendered <- struct{}{}:
	default:
	}
	return n, err
}

func TestSpinner_ConcurrentStopWaitsForErase(t *testing.T) {
	w := &gatedEraseWriter{
		rendered:         make(chan struct{}, 1),
		eraseStarted:     make(chan struct{}, 1),
		allowEraseFinish: make(chan struct{}),
	}
	s := &Spinner{
		w:       w,
		active:  true,
		label:   SanitizeSingleLine("waiting\x1b[31m\nnow"),
		started: time.Now(),
		done:    make(chan struct{}),
		closed:  make(chan struct{}),
	}
	go s.run()

	select {
	case <-w.rendered:
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for initial spinner render")
	}

	s.Update("updated\x1b[32m\tlabel")
	s.mu.Lock()
	assert.Equal(t, "updated label", s.label)
	s.mu.Unlock()

	stop1Done := make(chan struct{})
	go func() {
		s.Stop()
		close(stop1Done)
	}()

	select {
	case <-w.eraseStarted:
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for erase write to start")
	}

	// s.stopped is already true while the erase write is still blocked.
	// A second concurrent Stop() must wait until the erase write completes.
	stop2Entered := make(chan struct{})
	stop2SawFinished := make(chan bool, 1)
	go func() {
		close(stop2Entered)
		s.Stop()
		stop2SawFinished <- w.eraseFinished.Load()
	}()

	<-stop2Entered
	runtime.Gosched()

	select {
	case finished := <-stop2SawFinished:
		close(w.allowEraseFinish)
		require.True(t, finished, "concurrent Stop() returned before erase finished")
	default:
		close(w.allowEraseFinish)
		select {
		case finished := <-stop2SawFinished:
			assert.True(t, finished, "concurrent Stop() must wait for erase to finish")
		case <-time.After(30 * time.Second):
			t.Fatal("timed out waiting for second Stop()")
		}
	}

	select {
	case <-stop1Done:
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for first Stop()")
	}

	w.mu.Lock()
	out := w.output.String()
	w.mu.Unlock()
	assert.Contains(t, out, "waiting now")
	assert.NotContains(t, out, "\x1b[31m")
	assert.True(t, strings.HasSuffix(out, "\r\x1b[2K"))
}

func TestStartSpinner_InactiveWhenColorDisabled(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	p := &Printer{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, Format: FormatText, Color: ColorAuto}
	assert.False(t, ColorEnabled(p.Err, p.Color))
	s := StartSpinner(p, "loading")
	assert.False(t, s.active)

	pNever := &Printer{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, Format: FormatText, Color: ColorNever}
	assert.False(t, ColorEnabled(pNever.Err, pNever.Color))
	sNever := StartSpinner(pNever, "loading")
	assert.False(t, sNever.active)

	for _, format := range []Format{FormatQuiet, FormatJSON, FormatNDJSON, FormatTSV} {
		pFmt := &Printer{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, Format: format, Color: ColorAlways}
		sFmt := StartSpinner(pFmt, "loading")
		assert.False(t, sFmt.active, "format=%v must keep spinner inactive", format)
	}
}

func TestStartSpinner_NilSafety(t *testing.T) {
	sNilPrinter := StartSpinner(nil, "loading")
	assert.False(t, sNilPrinter.active)
	assert.NotPanics(t, func() {
		sNilPrinter.Update("next")
		sNilPrinter.Stop()
	})

	sNilErr := StartSpinner(&Printer{Format: FormatText, Color: ColorAlways}, "loading")
	assert.False(t, sNilErr.active)

	var nilSpinner *Spinner
	assert.NotPanics(t, func() {
		nilSpinner.Update("next")
		nilSpinner.Stop()
	})
}

func TestStartSpinner_InactiveOnDumbTerm(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR_FORCE", "1")
	t.Setenv("TERM", "dumb")
	p := &Printer{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, Format: FormatText, Color: ColorAuto}
	assert.False(t, ColorEnabled(p.Err, p.Color))
	s := StartSpinner(p, "loading")
	assert.False(t, s.active)
}
