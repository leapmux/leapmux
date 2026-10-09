package muse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

func storedSessions(ctx context.Context, q agent.StoredSessionQuery) (sessions []agent.StoredSession, err error) {
	registration := Registration()
	if q.RuntimeLocator != nil {
		registration.Locator = *q.RuntimeLocator
	}
	opts := agent.Options{WorkingDir: q.WorkingDir, HomeDir: q.Home(), Shell: q.Shell, LoginShell: q.LoginShell}
	c, err := openConnection(ctx, opts, registration, q.Environ(), nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := c.close(); closeErr != nil {
			sessions = nil
			err = errors.Join(err, fmt.Errorf("close the Muse stored session host: %w", closeErr))
		}
	}()
	result := make([]agent.StoredSession, 0)
	cursor := ""
	seen := map[string]bool{"": true}
	for len(result) < q.EffectiveLimit() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		params := map[string]any{"workspaceRoot": q.WorkingDir, "limit": min(200, q.EffectiveLimit()-len(result))}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.request(methodSessionList, params, opts.EffectiveAPITimeout(), nil)
		if err != nil {
			return nil, err
		}
		var page struct {
			Sessions   *[]nativeSession `json:"sessions"`
			NextCursor json.RawMessage  `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &page); err != nil || page.Sessions == nil || len(page.NextCursor) == 0 {
			return nil, fmt.Errorf("the Muse host returned an invalid stored session page")
		}
		var next *string
		if json.Unmarshal(page.NextCursor, &next) != nil {
			return nil, fmt.Errorf("the Muse host returned an invalid stored session page cursor")
		}
		for _, session := range *page.Sessions {
			if session.ID == "" || session.Kind == "subagent" {
				continue
			}
			title := session.Title
			if title == "" {
				title = session.Name
			}
			if title == "" {
				title = session.FirstUserPrompt
			}
			created, err := time.Parse(time.RFC3339Nano, session.CreatedAt)
			if err != nil {
				return nil, fmt.Errorf("the Muse stored session %q has an invalid creation timestamp: %w", session.ID, err)
			}
			stamp, err := time.Parse(time.RFC3339Nano, session.UpdatedAt)
			if err != nil {
				return nil, fmt.Errorf("the Muse stored session %q has an invalid update timestamp: %w", session.ID, err)
			}
			if stamp.Before(created) {
				return nil, fmt.Errorf("the Muse stored session %q updates before its creation timestamp", session.ID)
			}
			if len(session.LastActivityAt) != 0 {
				var activity *string
				if json.Unmarshal(session.LastActivityAt, &activity) != nil || activity == nil {
					return nil, fmt.Errorf("the Muse stored session %q has an invalid activity timestamp", session.ID)
				}
				stamp, err = time.Parse(time.RFC3339Nano, *activity)
				if err != nil {
					return nil, fmt.Errorf("the Muse stored session %q has an invalid activity timestamp: %w", session.ID, err)
				}
				if stamp.Before(created) {
					return nil, fmt.Errorf("the Muse stored session %q reports activity before its creation timestamp", session.ID)
				}
			}
			result = append(result, agent.StoredSession{Handle: session.ID, Title: title, UpdatedAt: stamp})
		}
		if next == nil {
			break
		}
		cursor = *next
		if seen[cursor] {
			return nil, fmt.Errorf("the Muse host repeated a stored session page cursor")
		}
		seen[cursor] = true
	}
	return agent.SortAndCapSessions(result, q.EffectiveLimit()), nil
}
