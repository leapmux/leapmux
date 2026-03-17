package agent

import (
	"context"
	"fmt"
)

// StartCodex starts a Codex agent process. Not implemented yet.
func StartCodex(_ context.Context, _ Options, _ OutputSink) (Provider, error) {
	return nil, fmt.Errorf("codex provider is not implemented yet")
}
