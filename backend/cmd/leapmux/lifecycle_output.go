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

	"github.com/leapmux/leapmux/internal/logging"
	"github.com/leapmux/leapmux/lifecycle"
)

func executeForegroundAttempt(ctx context.Context, cancel context.CancelFunc, mode string, runtime *lifecycle.Runtime, args []string) error {
	writer, err := lifecycle.AttemptLog(runtime.Paths(), runtime.Record().AttemptID)
	if err != nil {
		err = fmt.Errorf("open the attempt log: %w", err)
		return errors.Join(err, runtime.Finish(err))
	}
	capture, err := startOutputCapture(writer, cancel, runtime.Record().Execution)
	if err != nil {
		err = errors.Join(err, writer.Close())
		return errors.Join(err, runtime.Finish(err))
	}
	err = foregroundResult(ctx, dispatchModeForeground(ctx, mode, runtime, args))
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
	once     sync.Once
	err      error
}

func startOutputCapture(attempt io.Writer, cancel context.CancelFunc, execution lifecycle.ExecutionKind) (*outputCapture, error) {
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
	for index, read := range readers {
		destination := attempt
		if execution == lifecycle.ExecutionForeground {
			destination = io.MultiWriter(capture.original[index], attempt)
		}
		go func() {
			_, copyErr := io.Copy(destination, read)
			if copyErr != nil {
				cancel()
				// Writers must still finish when a destination failed. Continue
				// reading until teardown closes this pipe's writing side.
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
		os.Stdout, os.Stderr = c.original[0], c.original[1]
		slog.SetDefault(c.logger)
		log.SetOutput(c.logOut)
		for _, writer := range c.writers {
			c.err = errors.Join(c.err, writer.Close())
		}
		for _, done := range c.done {
			c.err = errors.Join(c.err, <-done)
		}
	})
	return c.err
}
