package cli

import (
	"bufio"
	"context"
	"io"
	"strings"
)

// ReadLine reads one line from in and returns it without its line ending. A
// line that ends at the end of input, without a line ending, is incomplete:
// ReadLine returns it with the error of the read.
//
// When ctx ends before the line arrives, ReadLine returns the cause of ctx at
// once, so an interrupt ends a prompt as it ends a network wait. A read from
// a terminal cannot stop, so it goes on in the background until the input has
// a line. Call ReadLine only where the process ends soon after ctx does, as a
// command's prompt does.
func ReadLine(ctx context.Context, in io.Reader) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", context.Cause(ctx)
	}
	type answer struct {
		line string
		err  error
	}
	done := make(chan answer, 1)
	go func() {
		line, err := readLine(in)
		done <- answer{line, err}
	}()
	select {
	case a := <-done:
		return a.line, a.err
	case <-ctx.Done():
		return "", context.Cause(ctx)
	}
}

// readLine reads one line from in without over-reading past '\n' into a
// throwaway buffer, so sequential ReadLine calls on the same reader keep the
// remaining bytes intact.
func readLine(in io.Reader) (string, error) {
	if br, ok := in.(*bufio.Reader); ok {
		line, err := br.ReadString('\n')
		return strings.TrimRight(line, "\r\n"), err
	}
	var buf []byte
	if br, ok := in.(io.ByteReader); ok {
		for {
			b, err := br.ReadByte()
			if err != nil {
				return strings.TrimRight(string(buf), "\r\n"), err
			}
			if b == '\n' {
				return strings.TrimRight(string(buf), "\r\n"), nil
			}
			buf = append(buf, b)
		}
	}
	var one [1]byte
	for {
		n, err := in.Read(one[:])
		if n > 0 {
			if one[0] == '\n' {
				return strings.TrimRight(string(buf), "\r\n"), nil
			}
			buf = append(buf, one[0])
		}
		if err != nil {
			return strings.TrimRight(string(buf), "\r\n"), err
		}
	}
}
