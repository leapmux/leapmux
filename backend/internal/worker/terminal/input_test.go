package terminal

import (
	"sync"
	"testing"

	pty "github.com/aymanbagabas/go-pty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/terminput"
)

// recordingPTY records each write that reaches the PTY, so a test reads the
// exact bytes and the count of writes.
type recordingPTY struct {
	pty.Pty
	mu     sync.Mutex
	writes []string
}

func (p *recordingPTY) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.writes = append(p.writes, string(b))
	return len(b), nil
}

func (p *recordingPTY) recorded() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.writes...)
}

// newInputTestManager installs one terminal whose PTY records its writes, and
// whose program printed output. The mode tracker reads the modes from it.
func newInputTestManager(t *testing.T, id string, output ...string) (*Manager, *recordingPTY) {
	t.Helper()
	rec := &recordingPTY{}
	term := &Terminal{id: id, ptmx: rec, screenBuf: NewScreenBuffer(), exitCh: make(chan struct{})}
	for _, chunk := range output {
		term.screenBuf.Write([]byte(chunk))
	}
	m := NewManager()
	m.current[id] = &terminalRegistration{id: id, terminal: term, done: make(chan struct{})}
	return m, rec
}

func mustKeys(t *testing.T, names ...string) []terminput.Key {
	t.Helper()
	keys, err := terminput.ParseKeys(names)
	require.NoError(t, err)
	return keys
}

// The manager encodes the keys and the paste for the modes that the program
// printed, and writes the text and the keys in one write.
func TestManagerSendInput_EncodesForTheTerminalModes(t *testing.T) {
	in := terminput.Input{Data: []byte("ls\n"), Paste: true, Keys: mustKeys(t, "up", "enter")}
	for name, tc := range map[string]struct {
		output []string
		want   string
	}{
		"default modes": {nil, "ls\r\x1b[A\r"},
		"application cursor keys": {
			[]string{"\x1b[?1h"}, "ls\r\x1bOA\r",
		},
		"bracketed paste": {
			[]string{"\x1b[?2004h"}, "\x1b[200~ls\r\x1b[201~\x1b[A\r",
		},
		"both, set in one sequence": {
			[]string{"\x1b[?1;2004h"}, "\x1b[200~ls\r\x1b[201~\x1bOA\r",
		},
		"a mode set and reset again": {
			[]string{"\x1b[?1h\x1b[?2004h", "text\x1b[?1l\x1b[?2004l"}, "ls\r\x1b[A\r",
		},
		"a mode sequence split across two reads": {
			[]string{"\x1b[?20", "04h"}, "\x1b[200~ls\r\x1b[201~\x1b[A\r",
		},
	} {
		t.Run(name, func(t *testing.T) {
			m, rec := newInputTestManager(t, "term-1", tc.output...)
			require.NoError(t, m.SendInput("term-1", in))
			assert.Equal(t, []string{tc.want}, rec.recorded(), "one write holds the text and every key")
		})
	}
}

// A raw text reaches the PTY as it is, in any mode.
func TestManagerSendInput_RawDataKeepsItsBytes(t *testing.T) {
	m, rec := newInputTestManager(t, "term-1", "\x1b[?1h\x1b[?2004h")
	require.NoError(t, m.SendInput("term-1", terminput.Input{Data: []byte("a\nb\x1b[201~")}))
	assert.Equal(t, []string{"a\nb\x1b[201~"}, rec.recorded())
}

// An input that holds nothing writes nothing, and so does an exited or an
// unknown terminal.
func TestManagerSendInput_WritesNothing(t *testing.T) {
	m, rec := newInputTestManager(t, "term-1")
	require.NoError(t, m.SendInput("term-1", terminput.Input{}))
	require.NoError(t, m.SendInput("term-1", terminput.Input{Paste: true}))
	assert.Empty(t, rec.recorded())

	err := m.SendInput("no-such-terminal", terminput.Input{Keys: mustKeys(t, "enter")})
	require.ErrorIs(t, err, ErrTerminalNotFound)

	close(m.current["term-1"].terminal.exitCh)
	require.Error(t, m.SendInput("term-1", terminput.Input{Keys: mustKeys(t, "enter")}))
	assert.Empty(t, rec.recorded(), "an exited terminal takes no input")
}

// Input that races the output of a mode change encodes for one mode or the
// other, whole, and each input stays one write.
func TestManagerSendInput_ConcurrentWithModeChanges(t *testing.T) {
	m, rec := newInputTestManager(t, "term-1")
	term := m.current["term-1"].terminal
	const senders, sends = 8, 50
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range senders * sends {
			if i%2 == 0 {
				term.screenBuf.Write([]byte("\x1b[?1h"))
			} else {
				term.screenBuf.Write([]byte("\x1b[?1l"))
			}
		}
	})
	up := mustKeys(t, "up")
	for range senders {
		wg.Go(func() {
			for range sends {
				assert.NoError(t, m.SendInput("term-1", terminput.Input{Data: []byte("x"), Keys: up}))
			}
		})
	}
	wg.Wait()

	writes := rec.recorded()
	require.Len(t, writes, senders*sends)
	for _, w := range writes {
		assert.Contains(t, []string{"x\x1b[A", "x\x1bOA"}, w)
	}
}

func TestScreenBufferInputModes(t *testing.T) {
	sb := NewScreenBuffer()
	assert.Equal(t, terminput.Modes{}, sb.InputModes(), "a new terminal is in the default modes")

	sb.Write([]byte("\x1b[?1h"))
	assert.Equal(t, terminput.Modes{ApplicationCursorKeys: true}, sb.InputModes())
	sb.Write([]byte("\x1b[?2004h"))
	assert.Equal(t, terminput.Modes{ApplicationCursorKeys: true, BracketedPaste: true}, sb.InputModes())
	sb.Write([]byte("\x1b[?1l"))
	assert.Equal(t, terminput.Modes{BracketedPaste: true}, sb.InputModes())
	sb.Write([]byte("\x1b[1h\x1b[2004h"))
	assert.Equal(t, terminput.Modes{BracketedPaste: true}, sb.InputModes(), "an ANSI mode is not the DEC private mode")
	sb.Write([]byte("\x1b[?2004l"))
	assert.Equal(t, terminput.Modes{}, sb.InputModes())
}
