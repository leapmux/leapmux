package service

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// readRetryResolutionTail reads consecutive notifications from the transcript's end.
// A resolution updates a retry row. It does not combine ordinary notifications from different sources.
// The writer compares these rows again inside its transaction before it applies the resolution.
func readRetryResolutionTail(queries *db.Queries, agentID string, provider leapmuxv1.AgentProvider, sessionID string, owner *transcriptOwner) ([]db.Message, error) {
	if owner != nil && !owner.IsCurrent() {
		return nil, nil
	}
	const pageSize = 64
	var tail []db.Message
	var before int64
	for {
		var rows []db.Message
		var err error
		if before == 0 {
			rows, err = queries.ListLatestMessagesByAgentID(bgCtx(), db.ListLatestMessagesByAgentIDParams{AgentID: agentID, Limit: pageSize})
		} else {
			rows, err = queries.ListMessagesByAgentIDReverse(bgCtx(), db.ListMessagesByAgentIDReverseParams{AgentID: agentID, Seq: before, Limit: pageSize})
		}
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.AgentProvider != provider || row.AgentSessionID != sessionID || row.TranscriptOnly {
				return tail, nil
			}
			data, err := msgcodec.Decompress(row.Content, row.ContentCompression)
			if err != nil {
				return nil, err
			}
			wrapper, err := unwrapNotifContent(data)
			if err != nil || wrapper.Type != notifThreadWrapperType {
				return tail, nil
			}
			tail = append(tail, row)
			before = row.Seq
		}
		if len(rows) < pageSize {
			return tail, nil
		}
	}
}

// retryResolutionParent calls the provider outside the notification mutex and database transaction.
func retryResolutionParent(tail []db.Message, plugin agent.Provider) (*db.Message, error) {
	for i := range tail {
		data, err := msgcodec.Decompress(tail[i].Content, tail[i].ContentCompression)
		if err != nil {
			return nil, err
		}
		wrapper, err := unwrapNotifContent(data)
		if err != nil {
			return nil, err
		}
		for _, raw := range wrapper.Messages {
			var envelope struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(raw, &envelope) == nil {
				if envelope.Type == contracts.NotificationTypeApiRetryResolved {
					return nil, nil
				}
				if envelope.Type == contracts.NotificationTypeApiRetry {
					return &tail[i], nil
				}
			}
			if plugin != nil && plugin.Classify(raw).Kind() == agent.NotificationKindAPIRetry {
				return &tail[i], nil
			}
		}
	}
	return nil, nil
}
