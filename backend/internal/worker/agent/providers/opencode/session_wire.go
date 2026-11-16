package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// openCodeStoredMessage preserves the native projected message for transcript recovery.
// Event sequence numbers are not message IDs, and raw event retention is off in the CLI.
type openCodeStoredMessage struct {
	ID   string          `json:"id"`
	Type string          `json:"type"`
	Raw  json.RawMessage `json:"-"`
}

func (m *openCodeStoredMessage) UnmarshalJSON(data []byte) error {
	type header openCodeStoredMessage
	var decoded header
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if decoded.ID == "" || decoded.Type == "" {
		return errors.New("the OpenCode message requires an ID and type")
	}
	*m = openCodeStoredMessage(decoded)
	m.Raw = append(json.RawMessage(nil), data...)
	return nil
}

// visitMessages validates and visits each page before fetching the next one.
// Raw message bodies live for one page; IDs and cursors detect invalid pagination.
func (r openCodeRPC) visitMessages(ctx context.Context, sessionID string, visit func(openCodeStoredMessage) error) error {
	if visit == nil {
		return errors.New("OpenCode message recovery requires a visitor")
	}
	return r.scanMessages(ctx, sessionID, url.Values{"order": {"asc"}}, func(message openCodeStoredMessage) (bool, error) {
		return false, visit(message)
	})
}

// scanMessages validates each page before visiting it. A true result stops the scan.
func (r openCodeRPC) scanMessages(ctx context.Context, sessionID string, selector url.Values, visit func(openCodeStoredMessage) (bool, error)) error {
	path, err := openCodeSessionRoute(sessionID, "/message")
	if err != nil {
		return err
	}
	seenIDs := make(map[string]struct{})
	seenCursors := make(map[string]struct{})
	cursor := ""
	limit := 200
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		query := make(url.Values, len(selector)+2)
		for key, values := range selector {
			query[key] = append([]string(nil), values...)
		}
		query.Set("limit", strconv.Itoa(limit))
		if cursor != "" {
			query.Del("order")
			query.Set("cursor", cursor)
		}
		var page struct {
			Data   *[]openCodeStoredMessage `json:"data"`
			Cursor *struct {
				Next string `json:"next"`
			} `json:"cursor"`
		}
		if err := r.callQuery(ctx, http.MethodGet, path, query, nil, &page); err != nil {
			if errors.Is(err, providerkit.ErrEndpointResponseTooLarge) && limit > 1 {
				limit = max(1, limit/2)
				continue
			}
			return err
		}
		if page.Data == nil || page.Cursor == nil {
			return errors.New("the OpenCode message page requires data and a cursor")
		}
		next := page.Cursor.Next
		if next != "" {
			if _, duplicate := seenCursors[next]; duplicate {
				return errors.New("OpenCode repeated a message cursor during recovery")
			}
			seenCursors[next] = struct{}{}
		}
		// Validate the complete page before the visitor applies any of its records.
		for _, message := range *page.Data {
			if _, duplicate := seenIDs[message.ID]; duplicate {
				return fmt.Errorf("OpenCode repeated message %q during recovery", message.ID)
			}
			seenIDs[message.ID] = struct{}{}
		}
		for _, message := range *page.Data {
			if err := ctx.Err(); err != nil {
				return err
			}
			stop, err := visit(message)
			if err != nil {
				return err
			}
			if stop {
				return nil
			}
		}
		if next == "" {
			return nil
		}
		cursor = next
	}
}

func (r openCodeRPC) activeSessions(ctx context.Context) (map[string]bool, error) {
	type activity struct {
		Type string `json:"type"`
	}
	entries, err := openCodeData[map[string]activity](ctx, r, http.MethodGet, "/api/session/active", nil)
	if err != nil {
		return nil, err
	}
	active := make(map[string]bool, len(entries))
	for id, entry := range entries {
		if err := openCodeRouteID(id); err != nil {
			return nil, fmt.Errorf("invalid OpenCode active session ID: %w", err)
		}
		if entry.Type != "running" {
			return nil, fmt.Errorf("unexpected OpenCode activity type %q", entry.Type)
		}
		active[id] = true
	}
	return active, nil
}

// pendingInbox lists durable queued input without interpreting its payload.
func (r openCodeRPC) pendingInbox(ctx context.Context, sessionID string) ([]openCodeInboxReceipt, error) {
	path, err := openCodeSessionRoute(sessionID, "/inbox")
	if err != nil {
		return nil, err
	}
	items, err := openCodeData[[]openCodeInboxReceipt](ctx, r, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if openCodeRouteID(item.ID) != nil || item.SessionID != sessionID || seen[item.ID] {
			return nil, errors.New("the OpenCode inbox snapshot contains an invalid or foreign input")
		}
		switch item.Type {
		case contracts.OpenCodeNativeMessageTypeUser, "synthetic", contracts.OpenCodeNativeMessageTypeCompaction, "move":
		default:
			return nil, fmt.Errorf("unexpected OpenCode inbox type %q", item.Type)
		}
		seen[item.ID] = true
	}
	return items, nil
}

type openCodeAssistantSnapshot struct {
	Content *[]json.RawMessage `json:"content"`
	Error   json.RawMessage    `json:"error"`
	Retry   json.RawMessage    `json:"retry"`
	Tokens  json.RawMessage    `json:"tokens"`
	Model   *openCodeModelRef  `json:"model"`
	Time    struct {
		Completed *float64 `json:"completed"`
	} `json:"time"`
}

func readOpenCodeAssistantSnapshot(message openCodeStoredMessage) (*openCodeAssistantSnapshot, error) {
	if message.Type != "assistant" {
		return nil, nil
	}
	var snapshot openCodeAssistantSnapshot
	if err := json.Unmarshal(message.Raw, &snapshot); err != nil {
		return nil, err
	}
	if snapshot.Content == nil {
		return nil, errors.New("the OpenCode assistant message has no content array")
	}
	return &snapshot, nil
}

// modelVariantPreference reads committed native selections, including explicit default choices.
func (r openCodeRPC) modelVariantPreference(ctx context.Context, sessionID string, target openCodeModelRef) (string, bool, error) {
	var variant string
	var found bool
	err := r.scanMessages(ctx, sessionID, url.Values{"type": {contracts.OpenCodeNativeMessageTypeModelSwitched}, "order": {"desc"}}, func(message openCodeStoredMessage) (bool, error) {
		if message.Type != contracts.OpenCodeNativeMessageTypeModelSwitched {
			return false, errors.New("the native model history contains another message type")
		}
		var record struct {
			Model    *openCodeModelRef `json:"model"`
			Previous *openCodeModelRef `json:"previous"`
		}
		if err := json.Unmarshal(message.Raw, &record); err != nil {
			return false, err
		}
		if record.Model == nil {
			return false, errors.New("the native model history has no selected model")
		}
		for _, ref := range []*openCodeModelRef{record.Model, record.Previous} {
			if ref == nil {
				continue
			}
			if openCodeModelKey(*ref) == "" {
				return false, errors.New("the native model history has an incomplete model reference")
			}
			if ref.ProviderID == target.ProviderID && ref.ID == target.ID {
				variant, found = ref.Variant, true
				return true, nil
			}
		}
		return false, nil
	})
	return variant, found, err
}
