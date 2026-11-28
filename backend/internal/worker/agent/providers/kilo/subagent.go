package kilo

import (
	"encoding/json"

	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// SteerChildInput admits a steer into the loaded child session and keeps the
// child transcript as the destination for its reply.
func (a *Agent) SteerChildInput(childKey string, request agent.SteerRequest) (agent.NativeInputReceipt, error) {
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
	var nativeSend func(func(json.RawMessage, error)) error
	var nativeFinish func(json.RawMessage, error)
	err = a.SendChildPromptDetachedUsing(child, request.Content, request.Attachments, func(prepared acp.PreparedPrompt) error {
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
		}, prepared, kiloPromptSettings{
			modelID: settings.ModelID, primaryAgent: settings.ModeID, variant: settings.ConfigValues[agent.OptionIDEffort],
		})
		if err != nil {
			return err
		}
		if send == nil {
			receipt = agent.NativeInputReceipt{}
			params, err := prepared.MarshalParams()
			if err != nil {
				return err
			}
			return a.SendDetachedRequest(acp.MethodSessionPrompt, params, providerkit.DetachedRequestOptions{}, finish)
		}
		nativeSend = send
		nativeFinish = finish
		return nil
	})
	if err == nil && nativeSend != nil {
		// Run native HTTP submission outside SendChildPromptDetachedUsing's sessionMu lock.
		err = nativeSend(nativeFinish)
	}
	if err != nil {
		release()
		return agent.NativeInputReceipt{}, err
	}
	return receipt, nil
}
