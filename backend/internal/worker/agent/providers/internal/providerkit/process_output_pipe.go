package providerkit

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/leapmux/leapmux/util/procutil"
)

var errInheritedPipeDrain = errors.New("an inherited output pipe stayed open past the drain limit")

// DrainLimitReached identifies a read terminated by the owner's drain timer.
func DrainLimitReached(err error) bool { return errors.Is(err, errInheritedPipeDrain) }

// processOutputPipe can stop accepting new writes without discarding bytes
// already queued for its reader.
type processOutputPipe struct {
	file      *os.File
	closeOnce sync.Once
	closeErr  error

	mu      sync.Mutex
	frozen  chan struct{}
	tail    []byte
	tailErr error
}

func newProcessOutputPipe(file *os.File) *processOutputPipe {
	return &processOutputPipe{file: file}
}

func (p *processOutputPipe) Close() error {
	p.closeOnce.Do(func() { p.closeErr = closeOwnedPipe(p.file) })
	return p.closeErr
}

func (p *processOutputPipe) Read(buffer []byte) (int, error) {
	p.mu.Lock()
	frozen := p.frozen
	p.mu.Unlock()
	if frozen == nil {
		size, err := p.file.Read(buffer)
		if !errors.Is(err, os.ErrClosed) {
			return size, err
		}
		p.mu.Lock()
		frozen = p.frozen
		p.mu.Unlock()
		if frozen == nil {
			return size, err
		}
		if size > 0 {
			return size, nil
		}
	}
	<-frozen
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.tail) == 0 {
		return 0, p.tailErr
	}
	size := copy(buffer, p.tail)
	p.tail = p.tail[size:]
	return size, nil
}

func (p *processOutputPipe) stopDraining() {
	p.closeOnce.Do(p.snapshot)
}

func (p *processOutputPipe) snapshot() {
	p.mu.Lock()
	frozen := make(chan struct{})
	p.frozen = frozen
	p.mu.Unlock()

	reader, err := procutil.DuplicatePipeReader(p.file, func() error {
		p.closeErr = closeOwnedPipe(p.file)
		return p.closeErr
	})
	var tail []byte
	if reader != nil {
		var readErr error
		tail, readErr = readPendingPipeBytes(reader)
		closeErr := reader.Close()
		if err != nil || closeErr != nil {
			if errors.Is(readErr, io.EOF) || DrainLimitReached(readErr) {
				readErr = nil
			}
			err = errors.Join(err, closeErr, readErr)
		} else {
			err = readErr
		}
	}
	p.mu.Lock()
	p.tail = tail
	p.tailErr = err
	close(frozen)
	p.mu.Unlock()
}

func readPendingPipeBytes(reader *os.File) ([]byte, error) {
	count, _, err := procutil.PipeReadState(reader)
	if err != nil {
		return nil, fmt.Errorf("inspect queued process output: %w", err)
	}
	tail := make([]byte, count)
	size, err := io.ReadFull(reader, tail)
	if err != nil {
		return tail[:size], fmt.Errorf("read queued process output: %w", err)
	}
	queued, atEOF, err := procutil.PipeReadState(reader)
	if err != nil {
		return tail, fmt.Errorf("inspect the drained process pipe: %w", err)
	}
	if atEOF && queued == 0 {
		return tail, io.EOF
	}
	return tail, errInheritedPipeDrain
}
