package servicelaunch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	pty "github.com/aymanbagabas/go-pty"

	"github.com/leapmux/leapmux/util/procutil"
)

const (
	nativeCLITailBytes  = 64 << 10
	nativeCLITotalBytes = 2 << 20
	nativeCLIMatchBytes = 8 << 10
	nativeCLISecretMax  = 4095
	nativePasswordStart = "Password for "
	nativePasswordEnd   = " (LeapMux does not store it): "
)

type nativeCLICommand struct {
	program     string
	args        []string
	directory   string
	environment []string
	account     string
	password    func(context.Context) ([]byte, error)
}

type nativeCLIResult struct {
	output string
	err    error
}

// nativeCLITranscript retains diagnostics only before protected input. It
// parses prompts incrementally and never puts credential bytes in an error.
type nativeCLITranscript struct {
	mu        sync.Mutex
	account   string
	total     int
	tail      []byte
	match     []byte
	ansi      byte
	prompted  bool
	sensitive bool
	secret    []byte
	echoTail  []byte
	failure   error
}

func (s *nativeCLITranscript) consume(data []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure != nil {
		return false, nil
	}
	s.total += len(data)
	if s.total > nativeCLITotalBytes {
		return false, errors.New("the native CLI exceeded its output limit")
	}
	if len(s.secret) > 0 {
		combined := append(bytes.Clone(s.echoTail), data...)
		echoed := bytes.Contains(combined, s.secret)
		clear(s.echoTail)
		s.echoTail = bytes.Clone(combined[max(0, len(combined)-len(s.secret)+1):])
		clear(combined)
		if echoed {
			return false, errors.New("the native CLI echoed protected input")
		}
	}
	if !s.sensitive {
		s.tail = append(s.tail, data...)
		if len(s.tail) > nativeCLITailBytes {
			s.tail = bytes.Clone(s.tail[len(s.tail)-nativeCLITailBytes:])
		}
	}
	for _, character := range data {
		switch s.ansi {
		case 1:
			switch character {
			case '[':
				s.ansi = 2
			case ']':
				s.ansi = 3
			default:
				s.ansi = 0
			}
		case 2:
			if character >= 0x40 && character <= 0x7e {
				s.ansi = 0
			}
		case 3:
			switch character {
			case 7:
				s.ansi = 0
			case 0x1b:
				s.ansi = 4
			}
		case 4:
			if character == '\\' {
				s.ansi = 0
			} else {
				s.ansi = 3
			}
		default:
			if character == 0x1b {
				s.ansi = 1
			} else if character >= 0x20 || character == '\n' {
				s.match = append(s.match, character)
			}
		}
	}
	ready := false
	for {
		start := bytes.Index(s.match, []byte(nativePasswordStart))
		if start < 0 {
			break
		}
		nameStart := start + len(nativePasswordStart)
		end := bytes.Index(s.match[nameStart:], []byte(nativePasswordEnd))
		if end < 0 {
			break
		}
		if s.account == "" || string(s.match[nameStart:nameStart+end]) != s.account {
			return false, errors.New("the native CLI requested an unexpected account password")
		}
		if s.prompted {
			return false, errors.New("the native CLI requested its account password more than once")
		}
		s.prompted = true
		ready = true
		s.match = bytes.Clone(s.match[nameStart+end+len(nativePasswordEnd):])
	}
	if len(s.match) > nativeCLIMatchBytes {
		s.match = bytes.Clone(s.match[len(s.match)-nativeCLIMatchBytes:])
	}
	return ready, nil
}

func (s *nativeCLITranscript) protect(secret []byte) error {
	if len(secret) == 0 || len(secret) > nativeCLISecretMax || bytes.ContainsAny(secret, "\r\n\x00") {
		return errors.New("the native CLI fixture password cannot form one protected input line")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sensitive = true
	s.secret = bytes.Clone(secret)
	return nil
}

func (s *nativeCLITranscript) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure == nil {
		s.failure = err
	}
}

func (s *nativeCLITranscript) finish() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	output := string(s.tail)
	if s.sensitive {
		output = "Native CLI output withheld after protected input."
	}
	clear(s.tail)
	clear(s.match)
	clear(s.secret)
	clear(s.echoTail)
	return output, s.failure
}

func runNativeCLICommand(ctx context.Context, command nativeCLICommand) nativeCLIResult {
	if err := ctx.Err(); err != nil {
		return nativeCLIResult{err: err}
	}
	terminal, err := pty.New()
	if err != nil {
		return nativeCLIResult{err: err}
	}
	if err := terminal.Resize(4096, 30); err != nil {
		return nativeCLIResult{err: errors.Join(err, closeNativeCLIChild(terminal), closeNativeCLIPipes(terminal))}
	}
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	owner, err := procutil.PreparePTYProcess(childCtx, terminal, procutil.PTYLaunch{
		Program: command.program, Args: command.args, Dir: command.directory, Env: command.environment,
	})
	if err != nil {
		return nativeCLIResult{err: errors.Join(err, closeNativeCLIChild(terminal), closeNativeCLIPipes(terminal))}
	}
	transcript := &nativeCLITranscript{account: command.account}
	fail := func(err error) { transcript.fail(err); cancel() }
	observedPrompt := make(chan struct{}, 1)
	readDone := make(chan error, 1)
	go func() {
		buffer := make([]byte, 32<<10)
		for {
			n, readErr := terminal.Read(buffer)
			if n > 0 {
				ready, parseErr := transcript.consume(buffer[:n])
				clear(buffer[:n])
				if parseErr != nil {
					fail(parseErr)
				}
				if ready {
					observedPrompt <- struct{}{}
				}
			}
			if readErr != nil {
				if nativeCLIEOF(readErr) {
					readErr = nil
				} else {
					fail(fmt.Errorf("read native CLI output: %w", readErr))
				}
				readDone <- readErr
				return
			}
		}
	}()
	if err := owner.Start(); err != nil {
		childCloseErr := closeNativeCLIChild(terminal)
		readErr := <-readDone
		output, parseErr := transcript.finish()
		return nativeCLIResult{output: output, err: errors.Join(err, childCloseErr, readErr, parseErr, owner.Close(), closeNativeCLIPipes(terminal))}
	}
	childDone := make(chan struct{})
	inputDone := make(chan struct{})
	go func() {
		defer close(inputDone)
		select {
		case <-childCtx.Done():
			return
		case <-childDone:
			return
		case <-observedPrompt:
		}
		if command.password == nil {
			err := errors.New("the native CLI requested protected input without a credential fixture")
			fail(err)
			return
		}
		secret, readErr := command.password(childCtx)
		if readErr != nil {
			clear(secret)
			fail(errors.New("the native CLI fixture credential could not be read"))
			return
		}
		defer clear(secret)
		if err := transcript.protect(secret); err != nil {
			fail(err)
			return
		}
		response := append(bytes.Clone(secret), nativeCLIEnter()...)
		defer clear(response)
		for len(response) > 0 {
			n, writeErr := terminal.Write(response)
			if writeErr != nil {
				fail(fmt.Errorf("write protected native CLI input: %w", writeErr))
				return
			}
			if n <= 0 || n > len(response) {
				fail(io.ErrShortWrite)
				return
			}
			response = response[n:]
		}
	}()
	waitErr := owner.Wait()
	close(childDone)
	childCloseErr := closeNativeCLIChild(terminal)
	<-inputDone
	readErr := <-readDone
	cleanupErr := errors.Join(owner.Close(), closeNativeCLIPipes(terminal))
	output, parseErr := transcript.finish()
	return nativeCLIResult{output: output, err: errors.Join(waitErr, childCloseErr, readErr, parseErr, cleanupErr, ctx.Err())}
}
