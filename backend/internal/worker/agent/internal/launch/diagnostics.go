package launch

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrNoCandidate reports that the launch shell completed selection without finding a command.
var ErrNoCandidate = errors.New("none of the provider commands were found")

// Diagnostics retains bounded stderr and recognizes the launch record even
// after the display buffer fills. Its caller serializes access.
type Diagnostics struct {
	text        bytes.Buffer
	limit       int
	marker      []byte
	candidates  []string
	pending     []byte
	passthrough bool
	matches     int
}

// NewDiagnostics prepares a separate stderr reader for this invocation.
func (p *PreparedLaunch) NewDiagnostics(limit int) *Diagnostics {
	return &Diagnostics{limit: limit, marker: []byte(p.exhaustedMarker), candidates: p.candidates}
}

func (d *Diagnostics) keep(data []byte) {
	length := min(len(data), max(0, d.limit-d.text.Len()))
	_, _ = d.text.Write(data[:length])
}

func (d *Diagnostics) Write(data []byte) (int, error) {
	size := len(data)
	if len(d.candidates) == 0 {
		d.keep(data)
		return size, nil
	}
	for len(data) != 0 {
		fragment, rest, newline := bytes.Cut(data, []byte{'\n'})
		if !d.passthrough && len(d.pending)+len(fragment) <= len(d.marker)+1 {
			d.pending = append(d.pending, fragment...)
		} else {
			d.keep(d.pending)
			d.pending = d.pending[:0]
			d.keep(fragment)
			d.passthrough = true
		}
		if newline {
			if !d.passthrough && bytes.Equal(bytes.TrimSuffix(d.pending, []byte{'\r'}), d.marker) {
				d.matches++
			} else {
				d.keep(d.pending)
				d.keep([]byte{'\n'})
			}
			d.pending = d.pending[:0]
			d.passthrough = false
		}
		data = rest
	}
	return size, nil
}

// Finish flushes the final unterminated diagnostic after stderr closes.
func (d *Diagnostics) Finish() {
	d.keep(d.pending)
	d.pending = d.pending[:0]
}

func (d *Diagnostics) String() string { return d.text.String() }

// ExitError adds the attempted command names only for a confirmed
// exhaustion record and keeps the original process status available to callers.
func (d *Diagnostics) ExitError(err error, ready bool) error {
	var exit *exec.ExitError
	if !ready && d.matches == 1 && errors.As(err, &exit) && exit.ExitCode() == 127 {
		return fmt.Errorf("%w (%s): %w", ErrNoCandidate, strings.Join(d.candidates, ", "), err)
	}
	return err
}
