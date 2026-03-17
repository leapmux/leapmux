package agent

import (
	"context"
	"fmt"
)

// StartOpenCode starts an OpenCode agent process. Not implemented yet.
func StartOpenCode(_ context.Context, _ Options, _ OutputSink) (Provider, error) {
	return nil, fmt.Errorf("opencode provider is not implemented yet")
}
