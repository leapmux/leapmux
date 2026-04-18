//go:build windows

package main

import (
	"errors"

	"github.com/Microsoft/go-winio"
)

func isPipeClosed(err error) bool {
	return errors.Is(err, winio.ErrFileClosed)
}
