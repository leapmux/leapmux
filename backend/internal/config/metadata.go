package config

import "errors"

// ErrVersionRequested reports a version query instead of a runtime configuration.
var ErrVersionRequested = errors.New("configuration requests a version query")
