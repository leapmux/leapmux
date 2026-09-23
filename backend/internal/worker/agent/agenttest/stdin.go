package agenttest

import (
	"io"
	"strings"
	"sync"

	"github.com/stretchr/testify/assert"
)

// Stdin records what the agent wrote to the process, and stands in for stdin
// where a test inspects the frames.
//
// The reads take a lock because `Process` performs every write on ONE goroutine
// of its own, and a DETACHED write returns before that goroutine has run. A test
// that held a plain `bytes.Buffer` raced its own agent, and the race detector then
// reported it against whichever test ran at that moment.
type Stdin struct {
	mu   sync.Mutex
	text strings.Builder
}

func (b *Stdin) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.Write(data)
}

func (b *Stdin) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.String()
}

// Close satisfies io.WriteCloser, so this stands in for a process's stdin.
func (b *Stdin) Close() error { return nil }

// Reset drops what the agent wrote, for a case that asserts about the frames after
// a point rather than about all of them.
func (b *Stdin) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.text.Reset()
}

// NopStdin returns w as a process stdin whose Close does nothing, for a test
// that reads what the agent wrote from its own writer.
func NopStdin(w io.Writer) io.WriteCloser {
	return nopStdin{w}
}

// nopStdin is the stdin that NopStdin returns.
type nopStdin struct{ io.Writer }

func (nopStdin) Close() error { return nil }

// FailingStdin is a process stdin that refuses every write, as a closed pipe
// does.
type FailingStdin struct{}

func (FailingStdin) Write([]byte) (int, error) { return 0, assert.AnError }

func (FailingStdin) Close() error { return nil }
