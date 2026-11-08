package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/util/procutil"
)

const (
	consoleHelperFlag = "--console-relay"
	consoleFrameBytes = 32 << 10
	consoleDrainTime  = time.Second
	consoleDrainTag   = "foreground-console-drain"
)

const (
	consoleRelayActive int32 = iota
	consoleRelayDraining
	consoleRelayInterrupted
)

// consoleFailure carries a console error without copying any output bytes.
type consoleFailure struct {
	Message string `json:"message"`
	Code    uint64 `json:"code"`
	Closed  bool   `json:"closed"`
}

func (e consoleFailure) Error() string { return "mirror console output: " + e.Message }

func (e consoleFailure) Unwrap() error {
	if e.Closed {
		return os.ErrClosed
	}
	if e.Code != 0 {
		return syscall.Errno(e.Code)
	}
	return nil
}

// runConsoleHelper owns only inherited console writes. Its parent can end
// those writes by ending this process, including on synchronous Windows pipes.
func runConsoleHelper(args []string, input io.Reader, consoles [2]io.Writer) (int, bool) {
	if len(args) == 0 || args[0] != consoleHelperFlag {
		return 0, false
	}
	if len(args) != 3 {
		return 2, true
	}
	reportHandle, reportErr := strconv.ParseUint(args[1], 10, strconv.IntSize)
	lifetimeHandle, lifetimeErr := strconv.ParseUint(args[2], 10, strconv.IntSize)
	if reportErr != nil || lifetimeErr != nil || reportHandle == 0 || lifetimeHandle == 0 {
		return 2, true
	}
	report := os.NewFile(uintptr(reportHandle), "console-error-report")
	lifetime := os.NewFile(uintptr(lifetimeHandle), "console-parent-lifetime")
	if report == nil || lifetime == nil {
		return 2, true
	}
	procutil.KeepRunningOnBrokenPipe()
	// Process-group signals belong to the application. The parent controls
	// this helper through its lifetime pipe and verified process ownership.
	signal.Ignore(procutil.ShutdownSignals()...)
	go func() {
		var marker [1]byte
		_, _ = lifetime.Read(marker[:])
		os.Exit(0)
	}()
	// Immediate process exit releases the lifetime reader. Closing and joining
	// an inherited synchronous Windows read can itself wait indefinitely.
	if err := copyConsoleFrames(input, consoles); err != nil {
		failure := consoleFailure{Message: err.Error(), Closed: errors.Is(err, os.ErrClosed)}
		var native syscall.Errno
		if errors.As(err, &native) {
			failure.Code = uint64(native)
		}
		if err := json.NewEncoder(report).Encode(failure); err != nil {
			return 2, true
		}
		return 1, true
	}
	return 0, true
}

func copyConsoleFrames(input io.Reader, consoles [2]io.Writer) error {
	var header [5]byte
	data := make([]byte, consoleFrameBytes)
	for {
		if _, err := io.ReadFull(input, header[:]); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("read the console frame: %w", err)
		}
		count := binary.LittleEndian.Uint32(header[1:])
		if header[0] >= byte(len(consoles)) || count == 0 || count > consoleFrameBytes {
			return errors.New("the console frame has an invalid stream or size")
		}
		if _, err := io.ReadFull(input, data[:int(count)]); err != nil {
			return fmt.Errorf("read the console output: %w", err)
		}
		if err := writeConsoleBytes(consoleMirror{consoles[header[0]]}, data[:int(count)]); err != nil {
			return err
		}
	}
}

// consoleRelay keeps optional console delivery separate from required log
// writes. All transports have finite capacity; interruption releases every
// parent writer and the owned helper process.
type consoleRelay struct {
	connection       net.Conn
	relay            net.Conn
	lifetime         *os.File
	owner            *procutil.ProcessOwner
	cancel           context.CancelFunc
	context          context.Context
	clock            quartz.Clock
	stop             func() bool
	cancellationDone chan struct{}

	writeMu      sync.Mutex
	failureMu    sync.Mutex
	writeErr     error
	interrupt    sync.Once
	interruptErr error
	phase        atomic.Int32
	drainOnce    sync.Once
	drainDone    chan struct{}
	relayDone    chan error
	reportDone   chan error
	exited       chan struct{}
	waitErr      error
	unexpected   bool
	closeOnce    sync.Once
	closeErr     error
}

func startConsoleRelay(ctx context.Context, cancelApplication context.CancelFunc, consoles [2]*os.File, clock quartz.Clock) (_ *consoleRelay, result error) {
	for _, console := range consoles {
		if _, err := console.Stat(); err != nil {
			return nil, fmt.Errorf("inspect the console: %w", err)
		}
	}
	var pipes [3][2]*os.File
	var files []*os.File
	var owner *procutil.ProcessOwner
	childCtx, cancelChild := context.WithCancel(context.WithoutCancel(ctx))
	started, committed := false, false
	defer func() {
		if committed {
			return
		}
		cancelChild()
		for _, file := range files {
			result = errors.Join(result, closeConsoleFile(file))
		}
		if owner != nil {
			result = errors.Join(result, owner.Close())
			if started {
				result = errors.Join(result, owner.Wait())
			}
		}
	}()
	for index := range pipes {
		read, write, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		pipes[index] = [2]*os.File{read, write}
		files = append(files, read, write)
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(childCtx, executable)
	command.Stdin, command.Stdout, command.Stderr = pipes[0][0], consoles[0], consoles[1]
	reportHandle, err := inheritConsoleFile(command, pipes[1][1])
	if err != nil {
		return nil, err
	}
	lifetimeHandle, err := inheritConsoleFile(command, pipes[2][0])
	if err != nil {
		return nil, err
	}
	command.Args = append(command.Args, consoleHelperFlag, reportHandle, lifetimeHandle)
	owner = procutil.PrepareProcess(command)
	if err := owner.Start(); err != nil {
		return nil, fmt.Errorf("start the console relay: %w", err)
	}
	started = true
	for _, file := range []*os.File{pipes[0][0], pipes[1][1], pipes[2][0]} {
		if err := file.Close(); err != nil {
			return nil, err
		}
	}
	connection, relay := net.Pipe()
	r := &consoleRelay{
		connection: connection, relay: relay, lifetime: pipes[2][1], owner: owner,
		cancel: cancelChild, context: ctx, clock: clock, relayDone: make(chan error, 1),
		reportDone: make(chan error, 1), exited: make(chan struct{}),
	}
	go func() {
		_, copyErr := io.Copy(pipes[0][1], relay)
		if r.phase.Load() == consoleRelayInterrupted && (errors.Is(copyErr, io.ErrClosedPipe) || errors.Is(copyErr, net.ErrClosed) || errors.Is(copyErr, os.ErrClosed) || isClosedConsolePipe(copyErr)) {
			copyErr = nil
		}
		r.relayDone <- errors.Join(copyErr, closeConsoleFile(pipes[0][1]), closeConsoleConnection(relay))
	}()
	go func() {
		reportErr := readConsoleFailure(pipes[1][0])
		reportErr = errors.Join(reportErr, closeConsoleFile(pipes[1][0]))
		if reportErr != nil {
			r.Interrupt()
			cancelApplication()
		}
		r.reportDone <- reportErr
	}()
	go r.waitForHelper(cancelApplication)
	r.watchCancellation(ctx, cancelApplication)
	committed = true
	return r, nil
}

func (r *consoleRelay) waitForHelper(cancelApplication context.CancelFunc) {
	defer close(r.exited)
	r.waitErr = r.owner.Wait()
	phase := r.phase.Load()
	r.unexpected = phase == consoleRelayActive || phase != consoleRelayInterrupted && r.waitErr != nil
	if r.unexpected && r.waitErr == nil {
		r.waitErr = errors.New("the console relay ended before delivery finished")
	}
	if r.unexpected {
		r.Interrupt()
		cancelApplication()
	}
}

func (r *consoleRelay) watchCancellation(ctx context.Context, cancelApplication context.CancelFunc) {
	r.cancellationDone = make(chan struct{})
	r.stop = context.AfterFunc(ctx, func() {
		defer close(r.cancellationDone)
		if err := r.setCancellationDeadline(); err != nil {
			r.recordFailure(err)
			r.Interrupt()
			cancelApplication()
		}
	})
}

func readConsoleFailure(input io.Reader) error {
	const maxReportBytes = 4096
	data, err := io.ReadAll(io.LimitReader(input, maxReportBytes+1))
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	if len(data) > maxReportBytes {
		return errors.New("the console error report exceeds 4096 bytes")
	}
	var failure consoleFailure
	if err := json.Unmarshal(data, &failure); err != nil || failure.Message == "" || failure.Code > uint64(^uintptr(0)) {
		return errors.New("the console error report is invalid")
	}
	return failure
}

func (r *consoleRelay) Write(stream byte, data []byte) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	if r.phase.Load() == consoleRelayInterrupted {
		return nil
	}
	if r.context.Err() != nil {
		if err := r.setCancellationDeadline(); err != nil {
			r.recordFailure(err)
			r.Interrupt()
			return err
		}
	}
	for len(data) != 0 {
		if r.phase.Load() == consoleRelayInterrupted {
			return nil
		}
		count := min(len(data), consoleFrameBytes)
		var header [5]byte
		header[0] = stream
		binary.LittleEndian.PutUint32(header[1:], uint32(count))
		for _, part := range [][]byte{header[:], data[:count]} {
			if err := writeConsoleBytes(r.connection, part); err != nil {
				if r.phase.Load() == consoleRelayInterrupted {
					return nil
				}
				if r.context.Err() != nil && errors.Is(err, os.ErrDeadlineExceeded) {
					r.Interrupt()
					return nil
				}
				r.recordFailure(err)
				r.Interrupt()
				return err
			}
		}
		data = data[count:]
	}
	return nil
}

func (r *consoleRelay) setCancellationDeadline() error {
	err := r.connection.SetWriteDeadline(r.clock.Now().Add(consoleDrainTime))
	if errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (r *consoleRelay) recordFailure(err error) {
	r.failureMu.Lock()
	defer r.failureMu.Unlock()
	r.writeErr = errors.Join(r.writeErr, err)
}

func writeConsoleBytes(output io.Writer, data []byte) error {
	for len(data) != 0 {
		n, err := output.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(data) {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func (r *consoleRelay) Interrupt() {
	r.interrupt.Do(func() {
		r.phase.Store(consoleRelayInterrupted)
		r.interruptErr = errors.Join(closeConsoleConnection(r.connection), closeConsoleConnection(r.relay), closeConsoleFile(r.lifetime))
		r.cancel()
	})
}

func (r *consoleRelay) BeginDrain() {
	r.drainOnce.Do(func() {
		r.drainDone = make(chan struct{})
		if !r.phase.CompareAndSwap(consoleRelayActive, consoleRelayDraining) {
			close(r.drainDone)
			return
		}
		timer := r.clock.NewTimer(consoleDrainTime, consoleDrainTag)
		go func() {
			defer close(r.drainDone)
			defer timer.Stop()
			select {
			case <-timer.C:
				r.Interrupt()
			case <-r.exited:
			}
		}()
	})
}

func (r *consoleRelay) Close() error {
	r.closeOnce.Do(func() {
		r.BeginDrain()
		connectionErr := closeConsoleConnection(r.connection)
		relayErr := <-r.relayDone
		<-r.exited
		reportErr := <-r.reportDone
		if !r.stop() {
			<-r.cancellationDone
		}
		r.Interrupt()
		<-r.drainDone
		var waitErr error
		if r.unexpected && reportErr == nil {
			waitErr = fmt.Errorf("the console relay exited unexpectedly: %w", r.waitErr)
		}
		r.failureMu.Lock()
		writeErr := r.writeErr
		r.failureMu.Unlock()
		r.closeErr = errors.Join(connectionErr, relayErr, reportErr, waitErr, writeErr, r.interruptErr, r.owner.Close())
	})
	return r.closeErr
}

func closeConsoleFile(file *os.File) error {
	err := file.Close()
	if errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}

func closeConsoleConnection(connection net.Conn) error {
	err := connection.Close()
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}
