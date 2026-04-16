package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
)

func TestIsBenignSessionReadError(t *testing.T) {
	t.Run("eof", func(t *testing.T) {
		if !isBenignSessionReadError(io.EOF) {
			t.Fatal("expected io.EOF to be benign")
		}
	})

	t.Run("unexpected eof", func(t *testing.T) {
		if !isBenignSessionReadError(io.ErrUnexpectedEOF) {
			t.Fatal("expected io.ErrUnexpectedEOF to be benign")
		}
	})

	t.Run("wrapped net err closed", func(t *testing.T) {
		err := fmt.Errorf("read frame: %w", net.ErrClosed)
		if !isBenignSessionReadError(err) {
			t.Fatal("expected wrapped net.ErrClosed to be benign")
		}
	})

	t.Run("closed connection string", func(t *testing.T) {
		err := errors.New("read unix /tmp/test.sock->: use of closed network connection")
		if !isBenignSessionReadError(err) {
			t.Fatal("expected closed network connection string to be benign")
		}
	})

	t.Run("other error", func(t *testing.T) {
		if isBenignSessionReadError(errors.New("boom")) {
			t.Fatal("did not expect arbitrary error to be benign")
		}
	})
}
