package kimi

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type kimiConversationMediaPlan struct {
	receipt contracts.KimiConversationMediaReceipt
	ids     []string
	maximum int
}

func prepareKimiConversationMedia(record contracts.KimiConversationRecord, originalSize int) (*kimiConversationMediaPlan, error) {
	if record.Type != contracts.KimiConversationRecordKindItem || record.Role != contracts.KimiConversationRecordRoleResult {
		return nil, nil
	}
	var item kimiConversationItem
	if err := json.Unmarshal(record.Item, &item); err != nil {
		return nil, err
	}
	if item.Type != contracts.KimiConversationItemKindToolCall {
		return nil, nil
	}
	plan := &kimiConversationMediaPlan{receipt: contracts.KimiConversationMediaReceipt{
		SessionID: record.SessionID, AgentID: record.AgentID, TurnID: record.TurnID, ItemID: item.ItemID,
	}, maximum: agent.LiveMaxMessageSize() - originalSize - 1024}
	seen := make(map[string]struct{})
	for _, media := range item.Media {
		if media.Kind != "image" || media.FileID == "" {
			continue
		}
		if !kimiMediaID.MatchString(media.FileID) {
			return nil, fmt.Errorf("the Kimi conversation image has an invalid media ID")
		}
		if _, exists := seen[media.FileID]; exists {
			continue
		}
		seen[media.FileID] = struct{}{}
		plan.ids = append(plan.ids, media.FileID)
	}
	if len(plan.ids) == 0 {
		return nil, nil
	}
	if plan.receipt.SessionID == "" || plan.receipt.AgentID == "" || plan.receipt.TurnID == "" || plan.receipt.ItemID == "" || plan.maximum <= 0 {
		return nil, fmt.Errorf("the Kimi conversation image has no complete owner or available message space")
	}
	if err := kimiCheckID("session", plan.receipt.SessionID); err != nil {
		return nil, err
	}
	return plan, nil
}

func (plan *kimiConversationMediaPlan) read(ctx context.Context, api *kimiClient, permits chan struct{}) ([]byte, error) {
	files, err := api.readMediaFiles(ctx, plan.receipt.SessionID, plan.ids, plan.maximum, permits)
	if err != nil {
		return nil, err
	}
	receipt := plan.receipt
	receipt.Files = files
	raw, err := json.Marshal(map[string]any{contracts.KimiConversationMediaRoot: receipt})
	if err != nil {
		return nil, err
	}
	if len(raw) > plan.maximum {
		return nil, fmt.Errorf("the recovered Kimi image exceeds the message size limit")
	}
	return raw, nil
}
