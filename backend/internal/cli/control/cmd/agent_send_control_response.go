package cmd

import (
	"context"
	"flag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/control"
)

// RunAgentSendControlResponse submits a provider response or completes its saved recording.
func RunAgentSendControlResponse(rawCtx any, args []string) error {
	var content, requestID, claimToken string
	var recordOnly bool
	var planSettings leapmuxv1.PlanApprovalSettings
	var planApproval *leapmuxv1.PlanApprovalSettings
	var flags *flag.FlagSet
	return withResolvedAgent(rawCtx, args, agentScaffoldOpts{
		setup: func(fs *flag.FlagSet) {
			flags = fs
			fs.StringVar(&content, "content", "", "provider response JSON")
			fs.StringVar(&requestID, "request-id", "", "the exact pending request ID")
			fs.StringVar(&claimToken, "claim-token", "", "the exact pending request claim token")
			fs.BoolVar(&recordOnly, "record-only", false, "check or save the response without sending to the provider")
			fs.StringVar(&planSettings.PermissionMode, "plan-permission-mode", "", "the permission mode to apply with a plan approval")
			fs.BoolVar(&planSettings.ClearContext, "plan-clear-context", false, "clear the context when the approved plan starts")
		},
		validate: func() error {
			flags.Visit(func(value *flag.Flag) {
				if value.Name == "plan-permission-mode" || value.Name == "plan-clear-context" {
					planApproval = &planSettings
				}
			})
			if !recordOnly && content == "" {
				return control.EmitError("invalid_request", "--content is required")
			}
			if recordOnly && content != "" {
				return control.EmitError("invalid_request", "--record-only cannot include --content")
			}
			if recordOnly && planApproval != nil {
				return control.EmitError("invalid_request", "--record-only cannot include plan settings")
			}
			if requestID == "" || claimToken == "" {
				return control.EmitError("invalid_request", "--request-id and --claim-token are required")
			}
			return nil
		},
		body: func(ctx context.Context, c *control.Client, workerID, agentID string) error {
			var response leapmuxv1.SendControlResponseResponse
			if err := callInnerRPC(ctx, c, workerID, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
				AgentId: agentID, RequestId: requestID, ClaimToken: claimToken,
				Content: []byte(content), RecordOnly: recordOnly, PlanApproval: planApproval,
			}, &response); err != nil {
				return err
			}
			if response.Error != "" {
				return control.EmitError("control_response_incomplete", response.Error)
			}
			if !recordOnly && response.State != leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED {
				return control.EmitError("control_response_incomplete", "the worker did not confirm response completion")
			}
			return control.EmitData(map[string]string{"agent_id": agentID, "state": response.State.String()})
		},
	})
}
