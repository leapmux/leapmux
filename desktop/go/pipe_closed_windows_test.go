//go:build windows

package main

import (
	"fmt"
	"testing"

	"github.com/Microsoft/go-winio"
)

func TestIsBenignSessionReadError_WrapsWinioErrFileClosed(t *testing.T) {
	err := fmt.Errorf("read frame: %w", winio.ErrFileClosed)
	if !isBenignSessionReadError(err) {
		t.Fatal("expected wrapped winio.ErrFileClosed to be benign")
	}
}
