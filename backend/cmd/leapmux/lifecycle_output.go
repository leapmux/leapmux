package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"sync"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/internal/logging"
	"github.com/leapmux/leapmux/lifecycle"
)

type foregroundExecution func(context.Context, *lifecycle.Runtime) error

func executeForegroundAttempt(ctx context.Context, cancel context.CancelFunc, runtime *lifecycle.Runtime, execute foregroundExecution) error {
	writer, err := lifecycle.AttemptLog(runtime.Paths(), runtime.Record().AttemptID)
	if err != nil {
		err = fmt.Errorf("open the attempt log: %w", err)
		return errors.Join(err, runtime.Finish(err))
	}
	capture, err := startOutputCapture(ctx, writer, cancel, runtime.Record().Execution)
	if err != nil {
		err = errors.Join(err, writer.Close())
		return errors.Join(err, runtime.Finish(err))
	}
	err = foregroundResult(ctx, execute(ctx, runtime))
	err = errors.Join(err, capture.Close())
	if err != nil {
		_, writeErr := fmt.Fprintf(writer, "error: %v\n", err)
		err = errors.Join(err, writeErr)
	}
	err = errors.Join(err, writer.Close(), lifecycle.RotateAttemptLogs(runtime.Paths(), ""))
	return errors.Join(err, runtime.Finish(err))
}

// outputCapture covers legacy printers and loggers that use process streams.
// Resetting only the logger would miss banners and later logging.Setup calls.
type outputCapture struct {
	original [2]*os.File
	writers  [2]*os.File
	done     [2]chan error
	logger   *slog.Logger
	logOut   io.Writer
	console  *consoleRelay
	once     sync.Once
	err      error
}

func startOutputCapture(ctx context.Context, attempt io.Writer, cancel context.CancelFunc, execution lifecycle.ExecutionKind) (*outputCapture, error) {
	capture := &outputCapture{
		original: [2]*os.File{os.Stdout, os.Stderr}, logger: slog.Default(), logOut: log.Writer(),
	}
	var readers [2]*os.File
	for index := range readers {
		read, write, err := os.Pipe()
		if err != nil {
			for prior := 0; prior < index; prior++ {
				err = errors.Join(err, readers[prior].Close(), capture.writers[prior].Close())
			}
			return nil, fmt.Errorf("capture foreground output: %w", err)
		}
		readers[index], capture.writers[index] = read, write
		capture.done[index] = make(chan error, 1)
	}
	if execution == lifecycle.ExecutionForeground {
		console, err := startConsoleRelay(ctx, cancel, capture.original, quartz.NewReal())
		if err != nil {
			for index, reader := range readers {
				err = errors.Join(err, reader.Close(), capture.writers[index].Close())
			}
			return nil, err
		}
		capture.console = console
	}
	for index, read := range readers {
		destination := capturedOutput{attempt: attempt, console: capture.console, stream: byte(index), cancel: cancel}
		go func() {
			_, copyErr := io.Copy(destination, read)
			if copyErr != nil {
				cancel()
				// A required log failure ends the attempt. Drain pending writes
				// so application cleanup can still finish.
				_, _ = io.Copy(io.Discard, read)
			}
			capture.done[index] <- errors.Join(copyErr, read.Close())
		}()
	}
	os.Stdout, os.Stderr = capture.writers[0], capture.writers[1]
	logging.Setup()
	return capture, nil
}

func (c *outputCapture) Close() error {
	c.once.Do(func() {
		if c.console != nil {
			c.console.BeginDrain()
		}
		for _, writer := range c.writers {
			c.err = errors.Join(c.err, writer.Close())
		}
		for _, done := range c.done {
			c.err = errors.Join(c.err, <-done)
		}
		if c.console != nil {
			c.err = errors.Join(c.err, c.console.Close())
		}
		os.Stdout, os.Stderr = c.original[0], c.original[1]
		slog.SetDefault(c.logger)
		log.SetOutput(c.logOut)
	})
	return c.err
}

type capturedOutput struct {
	attempt io.Writer
	console *consoleRelay
	stream  byte
	cancel  context.CancelFunc
}

func (w capturedOutput) Write(data []byte) (int, error) {
	n, err := w.attempt.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return n, err
	}
	if w.console != nil {
		if err := w.console.Write(w.stream, data); err != nil {
			w.cancel()
		}
	}
	// Console failure must not stop subsequent required log writes.
	return len(data), nil
}

// consoleMirror tolerates a closed pipe while retaining every other write
// failure. The attempt log remains the required output destination.
type consoleMirror struct{ io.Writer }

func (w consoleMirror) Write(data []byte) (int, error) {
	n, err := w.Writer.Write(data)
	if isClosedConsolePipe(err) {
		return len(data), nil
	}
	return n, err
}
