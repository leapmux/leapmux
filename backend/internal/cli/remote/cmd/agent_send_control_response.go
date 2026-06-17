package cmd

import (
	"context"
	"flag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/remote"
)

// RunAgentSendControlResponse forwards a raw control_response payload
// to a Claude-Code-style agent.
func RunAgentSendControlResponse(rawCtx any, args []string) error {
	var content string
	return withResolvedAgent(rawCtx, args, agentScaffoldOpts{
		setup: func(fs *flag.FlagSet) {
			fs.StringVar(&content, "content", "", "raw control_response JSON (required)")
		},
		validate: func() error {
			if content == "" {
				return remote.EmitError("invalid_request", "--content is required")
			}
			return nil
		},
		body: func(ctx context.Context, c *remote.Client, workerID, agentID, _ string) error {
			if err := callInnerRPC(ctx, c, workerID, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
				AgentId: agentID,
				Content: []byte(content),
			}, nil); err != nil {
				return err
			}
			return remote.EmitData(map[string]string{"agent_id": agentID})
		},
	})
}
