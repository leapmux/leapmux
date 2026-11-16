package qoder

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
)

// prepareSDKSettings keeps MCP tools eager in this SDK session. Qoder's
// mcp_status lists active tool definitions only. Deferred mode omits them,
// so a host cannot distinguish an admitted server from an inactive pooled
// connection before attaching an elicitation handler. Eager loading retains
// Qoder's trust and allow-list checks, but sends more tool schemas to the model.
func prepareSDKSettings(ctx context.Context, dirs *agentdir.Dirs) (*agentdir.Dir, string, error) {
	if dirs == nil {
		return nil, "", fmt.Errorf("qoder requires the Worker's private agent directories")
	}
	directory, err := dirs.New(ctx, agentDirSpec())
	if err != nil {
		return nil, "", fmt.Errorf("prepare the Qoder agent directory: %w", err)
	}
	path := filepath.Join(directory.Path(), "sdk-settings.json")
	if err := os.WriteFile(path, []byte(`{"mcp":{"lazyLoad":false}}`), 0o600); err != nil {
		return nil, "", errors.Join(fmt.Errorf("write Qoder SDK settings: %w", err), directory.Close())
	}
	return directory, path, nil
}
