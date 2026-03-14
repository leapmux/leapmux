package main

import "errors"

var errAlreadyRunning = errors.New("another instance is already running")
