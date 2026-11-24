package kilo

import (
	"encoding/json"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

var _ agent.ChildSteerReceiptSender = (*Agent)(nil)

func (a *Agent) SteerChildInput(childKey, content string, attachments []*leapmuxv1.Attachment) error {
	_, err := a.SteerChildInputWithReceipt(childKey, content, attachments)
	return err
}

// SteerChildInputWithReceipt admits a steer into the loaded child session and
// keeps the child transcript as the destination for its reply.
func (a *Agent) SteerChildInputWithReceipt(childKey, content string, attachments []*leapmuxv1.Attachment) (agent.NativeInputReceipt, error) {
	child, settings, err := a.LoadedChildSteerTarget(childKey)
	if err != nil {
		return agent.NativeInputReceipt{}, err
	}
	release, err := a.TrackRootPromptCallback()
	if err != nil {
		return agent.NativeInputReceipt{}, err
	}
	inputID := id.Generate()
	receipt := agent.NativeInputReceipt{SessionID: child.SessionID, InputID: inputID, TurnID: inputID}
	err = a.SendChildPromptDetachedUsing(child, content, attachments, func(params json.RawMessage) error {
		owner, found := a.SessionOwner(child.SessionID)
		if !found {
			return agent.ErrChildRouteNotReady
		}
		if err := owner.BindChildSessionID(child.SessionID); err != nil {
			return err
		}
		finish := func(_ json.RawMessage, err error) {
			defer release()
			a.ReportChildSteerFailure(child, owner, err)
		}
		send, err := a.prepareNativePromptFor(kiloReplyTarget{
			receipt: receipt, rootSessionID: child.RootSessionID, transcript: owner,
			flush: func(ids map[string]bool) { a.FlushChildNativeReplyOutput(child, ids) },
		}, params, kiloPromptSettings{
			modelID: settings.ModelID, primaryAgent: settings.ModeID, variant: settings.ConfigValues[agent.OptionIDEffort],
		})
		if err != nil {
			return err
		}
		if send == nil {
			receipt = agent.NativeInputReceipt{}
			return a.SendDetachedRequest(acp.MethodSessionPrompt, params, finish)
		}
		return send(finish)
	})
	if err != nil {
		release()
		return agent.NativeInputReceipt{}, err
	}
	return receipt, nil
}
