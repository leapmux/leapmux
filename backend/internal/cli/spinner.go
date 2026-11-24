package cli

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Spinner renders a single-line animated wait indicator on stderr when stderr is
// an interactive TTY and output is in non-quiet text mode. In non-TTY or
// machine formats (--json, --quiet), it is a no-op so stdout/stderr stay clean.
type Spinner struct {
	w       io.Writer
	active  bool
	started time.Time

	mu      sync.Mutex
	label   string
	stopped bool
	done    chan struct{}
	closed  chan struct{}
}

// StartSpinner begins a live progress spinner on p.Err when appropriate.
// Always call Stop before printing the final stdout result.
func StartSpinner(p *Printer, label string) *Spinner {
	s := &Spinner{label: label, started: time.Now()}
	if p == nil || p.Err == nil || p.Quiet || p.Format != FormatText || !isTerminalWriter(p.Err) {
		return s
	}
	if p.Color == ColorNever || (p.Color == ColorAuto && os.Getenv("NO_COLOR") != "") {
		return s
	}
	s.w = p.Err
	s.active = true
	s.done = make(chan struct{})
	s.closed = make(chan struct{})
	go s.run()
	return s
}

// Update changes the status label shown beside the spinner.
func (s *Spinner) Update(label string) {
	if s == nil || !s.active {
		return
	}
	s.mu.Lock()
	s.label = label
	s.mu.Unlock()
}

// Stop stops the spinner and erases its line from stderr before returning.
func (s *Spinner) Stop() {
	if s == nil || !s.active {
		return
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	close(s.done)
	s.mu.Unlock()
	<-s.closed
}

func (s *Spinner) run() {
	defer close(s.closed)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	frame := 0
	render := func() {
		s.mu.Lock()
		label := SanitizeSingleLine(s.label)
		s.mu.Unlock()
		elapsed := time.Since(s.started).Truncate(time.Second)
		line := fmt.Sprintf("%s %s (%s)", spinnerFrames[frame%len(spinnerFrames)], label, elapsed)
		if width := TerminalWidth(s.w); width > 4 {
			line = truncateTo(line, width-1)
		}
		_, _ = fmt.Fprintf(s.w, "\r\x1b[2K%s", line)
		frame++
	}
	render()
	for {
		select {
		case <-s.done:
			_, _ = fmt.Fprint(s.w, "\r\x1b[2K")
			return
		case <-ticker.C:
			render()
		}
	}
}
