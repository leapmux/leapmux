//go:build !windows

package main

import (
	"os"
	"os/exec"
	"strconv"
)

func inheritConsoleFile(command *exec.Cmd, file *os.File) (string, error) {
	handle := 3 + len(command.ExtraFiles)
	command.ExtraFiles = append(command.ExtraFiles, file)
	return strconv.Itoa(handle), nil
}
