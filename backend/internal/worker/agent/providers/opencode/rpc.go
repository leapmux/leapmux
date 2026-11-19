package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

const openCodeEventReadLimit = 64 << 20

// openCodeRPC addresses one OpenCode v2 server. Every ordinary request has a
// deadline. Event streams take their lifetime from the caller's context.
type openCodeRPC struct {
	endpoint *providerkit.HTTPEndpoint
	timeout  time.Duration
}

type openCodeModelRef struct {
	ProviderID string `json:"providerID"`
	ID         string `json:"id"`
	Variant    string `json:"variant,omitempty"`
}

type openCodeLocation struct {
	Directory string `json:"directory"`
}

type openCodePermissionRule struct {
	Action   string `json:"action"`
	Resource string `json:"resource"`
	Effect   string `json:"effect"`
}

type openCodeSessionInfo struct {
	Title    string            `json:"title"`
	ID       string            `json:"id"`
	ParentID string            `json:"parentID"`
	Agent    string            `json:"agent"`
	Model    *openCodeModelRef `json:"model"`
	Location openCodeLocation  `json:"location"`
	Outcome  string            `json:"outcome"`
	Time     struct {
		Created int64  `json:"created"`
		Updated int64  `json:"updated"`
		Busy    *int64 `json:"busy"`
		Idle    *int64 `json:"idle"`
	} `json:"time"`
}

type openCodeSessionCreate struct {
	Location    *openCodeLocation        `json:"location,omitempty"`
	ParentID    string                   `json:"parentID,omitempty"`
	Agent       string                   `json:"agent,omitempty"`
	Model       *openCodeModelRef        `json:"model,omitempty"`
	Permissions []openCodePermissionRule `json:"permissions,omitempty"`
}

type openCodePromptFile struct {
	URI  string `json:"uri"`
	Name string `json:"name,omitempty"`
}

type openCodePrompt struct {
	ID       string               `json:"id,omitempty"`
	Text     string               `json:"text"`
	Files    []openCodePromptFile `json:"files,omitempty"`
	Delivery string               `json:"delivery"`
}

type openCodeInboxReceipt struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionID"`
	Type      string `json:"type"`
}

func (r openCodeRPC) call(ctx context.Context, method, path string, body, out any) error {
	return r.callQuery(ctx, method, path, nil, body, out)
}

func (r openCodeRPC) callQuery(ctx context.Context, method, path string, query url.Values, body, out any) error {
	if r.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.timeout)
		defer cancel()
	}
	return r.endpoint.DoQuery(ctx, method, path, query, body, out)
}

func openCodeData[T any](ctx context.Context, r openCodeRPC, method, path string, body any) (T, error) {
	var response struct {
		Data *T `json:"data"`
	}
	if err := r.call(ctx, method, path, body, &response); err != nil {
		var zero T
		return zero, err
	}
	if response.Data == nil {
		var zero T
		return zero, fmt.Errorf("%s %s: the OpenCode reply has no data", method, path)
	}
	return *response.Data, nil
}

// openCodeRouteID accepts native IDs without allowing a path or query.
func openCodeRouteID(id string) error {
	if len(id) == 0 || len(id) > 512 {
		return errors.New("an OpenCode route ID requires between 1 and 512 bytes")
	}
	for _, c := range id {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' && c != '-' {
			return errors.New("an OpenCode route ID contains an invalid character")
		}
	}
	return nil
}

func openCodeSessionRoute(id, suffix string) (string, error) {
	if err := openCodeRouteID(id); err != nil {
		return "", err
	}
	return "/api/session/" + id + suffix, nil
}

func (r openCodeRPC) createSession(ctx context.Context, request openCodeSessionCreate) (openCodeSessionInfo, error) {
	session, err := openCodeData[openCodeSessionInfo](ctx, r, http.MethodPost, "/api/session", request)
	if err == nil {
		err = openCodeRouteID(session.ID)
	}
	return session, err
}

func (r openCodeRPC) session(ctx context.Context, id string) (openCodeSessionInfo, error) {
	path, err := openCodeSessionRoute(id, "")
	if err != nil {
		return openCodeSessionInfo{}, err
	}
	session, err := openCodeData[openCodeSessionInfo](ctx, r, http.MethodGet, path, nil)
	if err == nil && session.ID != id {
		err = errors.New("the OpenCode reply belongs to another session")
	}
	return session, err
}

func (r openCodeRPC) prompt(ctx context.Context, id string, request openCodePrompt) (openCodeInboxReceipt, error) {
	if request.ID != "" {
		if !strings.HasPrefix(request.ID, "msg_") {
			return openCodeInboxReceipt{}, errors.New("an OpenCode input ID must start with msg_")
		}
		if err := openCodeRouteID(request.ID); err != nil {
			return openCodeInboxReceipt{}, err
		}
	}
	if request.Delivery != "queue" && request.Delivery != "steer" {
		return openCodeInboxReceipt{}, errors.New("an OpenCode prompt requires queue or steer delivery")
	}
	path, err := openCodeSessionRoute(id, "/prompt")
	if err != nil {
		return openCodeInboxReceipt{}, err
	}
	receipt, err := openCodeData[openCodeInboxReceipt](ctx, r, http.MethodPost, path, request)
	if err == nil && (receipt.ID == "" || receipt.SessionID != id || receipt.Type != "user" || request.ID != "" && receipt.ID != request.ID) {
		err = errors.New("the OpenCode prompt reply has no matching inbox receipt")
	}
	return receipt, err
}

func (r openCodeRPC) sessionCall(ctx context.Context, method, id, suffix string, body, out any) error {
	path, err := openCodeSessionRoute(id, suffix)
	if err != nil {
		return err
	}
	return r.call(ctx, method, path, body, out)
}

func (r openCodeRPC) interrupt(ctx context.Context, id string) (bool, error) {
	var response struct {
		Interrupted *bool `json:"interrupted"`
	}
	err := r.sessionCall(ctx, http.MethodPost, id, "/interrupt", nil, &response)
	if err != nil {
		return false, err
	}
	if response.Interrupted == nil {
		return false, errors.New("the OpenCode reply has no interrupt outcome")
	}
	return *response.Interrupted, nil
}

func (r openCodeRPC) replyPermission(ctx context.Context, sessionID, requestID, decision, message string) error {
	if err := openCodeRouteID(requestID); err != nil {
		return err
	}
	if decision != "once" && decision != "always" && decision != "reject" {
		return errors.New("an OpenCode permission reply requires once, always, or reject")
	}
	request := struct {
		Decision string `json:"decision"`
		Message  string `json:"message,omitempty"`
	}{Decision: decision, Message: message}
	return r.sessionCall(ctx, http.MethodPost, sessionID, "/permission/"+requestID+"/reply", request, nil)
}

func (r openCodeRPC) replyForm(ctx context.Context, sessionID, formID string, answer map[string]any) error {
	if answer == nil {
		return errors.New("an OpenCode form reply requires an answer object")
	}
	if err := openCodeRouteID(formID); err != nil {
		return err
	}
	return r.sessionCall(ctx, http.MethodPost, sessionID, "/form/"+formID+"/reply", map[string]any{"answer": answer}, nil)
}

func (r openCodeRPC) cancelForm(ctx context.Context, sessionID, formID string) error {
	if err := openCodeRouteID(formID); err != nil {
		return err
	}
	return r.sessionCall(ctx, http.MethodDelete, sessionID, "/form/"+formID, nil, nil)
}

// replay reads retained events after an optional exclusive sequence.
// An absent cursor includes the first native event, whose sequence is zero.
// The completion marker proves the body did not end between native events.
func (r openCodeRPC) replay(ctx context.Context, sessionID string, after *int64, handle func(json.RawMessage) error) error {
	if handle == nil {
		return errors.New("an OpenCode replay requires an event handler")
	}
	if err := openCodeRouteID(sessionID); err != nil {
		return err
	}
	if after != nil && *after < 0 {
		return errors.New("an OpenCode replay sequence cannot be negative")
	}
	if r.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.timeout)
		defer cancel()
	}
	query := url.Values{"follow": {"false"}}
	if after != nil {
		query.Set("after", strconv.FormatInt(*after, 10))
	}
	response, err := r.endpoint.OpenStreamQuery(ctx, http.MethodGet, "/api/experimental/session/"+sessionID+"/log", query, nil)
	if err != nil {
		return err
	}
	synced := false
	err = readOpenCodeSSE(response.Body, func(event providerkit.SSEEvent) error {
		var envelope struct {
			Type        string `json:"type"`
			AggregateID string `json:"aggregateID"`
		}
		if err := json.Unmarshal(event.Data, &envelope); err != nil {
			return fmt.Errorf("decode an OpenCode replay event: %w", err)
		}
		if envelope.Type == "log.synced" {
			if envelope.AggregateID != sessionID {
				return errors.New("the OpenCode replay marker belongs to another session")
			}
			synced = true
			return nil
		}
		return handle(append(json.RawMessage(nil), event.Data...))
	})
	if err != nil {
		return err
	}
	if !synced {
		return errors.New("the OpenCode replay ended without a completion marker")
	}
	return nil
}

// switchAgent selects the agent for subsequent native turns.
func (r openCodeRPC) switchAgent(ctx context.Context, sessionID, primaryAgent string) error {
	if strings.TrimSpace(primaryAgent) == "" {
		return errors.New("an OpenCode agent selection must not be empty")
	}
	return r.sessionCall(ctx, http.MethodPost, sessionID, "/agent", struct {
		Agent string `json:"agent"`
	}{primaryAgent}, nil)
}

// switchModel sends a complete model reference, so an omitted variant clears the old override.
func (r openCodeRPC) switchModel(ctx context.Context, sessionID string, model openCodeModelRef) error {
	if strings.TrimSpace(model.ProviderID) == "" || strings.TrimSpace(model.ID) == "" {
		return errors.New("an OpenCode model selection requires a provider and model")
	}
	return r.sessionCall(ctx, http.MethodPost, sessionID, "/model", struct {
		Model openCodeModelRef `json:"model"`
	}{model}, nil)
}

type openCodeCompactionRequest struct {
	ID       string `json:"id,omitempty"`
	Delivery string `json:"delivery,omitempty"`
}

// compact returns when the server durably admits the request, before summarization finishes.
func (r openCodeRPC) compact(ctx context.Context, sessionID string, request openCodeCompactionRequest) (openCodeInboxReceipt, error) {
	if request.ID != "" {
		if err := openCodeRouteID(request.ID); err != nil {
			return openCodeInboxReceipt{}, err
		}
	}
	if request.Delivery != "" && request.Delivery != "queue" && request.Delivery != "steer" {
		return openCodeInboxReceipt{}, errors.New("an OpenCode compaction delivery must be queue or steer")
	}
	path, err := openCodeSessionRoute(sessionID, "/compact")
	if err != nil {
		return openCodeInboxReceipt{}, err
	}
	receipt, err := openCodeData[openCodeInboxReceipt](ctx, r, http.MethodPost, path, request)
	if err != nil {
		return openCodeInboxReceipt{}, err
	}
	if openCodeRouteID(receipt.ID) != nil || receipt.SessionID != sessionID || receipt.Type != "compaction" || (request.ID != "" && receipt.ID != request.ID) {
		return openCodeInboxReceipt{}, errors.New("the OpenCode compaction receipt does not identify the admitted request")
	}
	return receipt, nil
}

func (r openCodeRPC) pendingPermissions(ctx context.Context, sessionID string) ([]json.RawMessage, error) {
	return r.pendingControls(ctx, sessionID, "permission")
}

// pendingForms also accepts the native global owner used by location-scoped MCP forms.
func (r openCodeRPC) pendingForms(ctx context.Context, sessionID string) ([]json.RawMessage, error) {
	return r.pendingControls(ctx, sessionID, "form")
}

func (r openCodeRPC) pendingControls(ctx context.Context, sessionID, resource string) ([]json.RawMessage, error) {
	path, err := openCodeSessionRoute(sessionID, "/"+resource)
	if err != nil {
		return nil, err
	}
	records, err := openCodeData[[]json.RawMessage](ctx, r, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(records))
	for _, record := range records {
		var identity struct {
			ID        string `json:"id"`
			SessionID string `json:"sessionID"`
		}
		if json.Unmarshal(record, &identity) != nil || openCodeRouteID(identity.ID) != nil || identity.SessionID != sessionID || seen[identity.ID] {
			return nil, fmt.Errorf("the OpenCode %s snapshot contains an invalid or foreign request", resource)
		}
		seen[identity.ID] = true
	}
	return records, nil
}

// openCodeDeliveryUncertain separates transport ambiguity from a native refusal.
func openCodeDeliveryUncertain(err error) bool {
	if err == nil {
		return false
	}
	var refused *providerkit.HTTPStatusError
	return !errors.As(err, &refused) || refused.StatusCode < http.StatusBadRequest || refused.StatusCode >= http.StatusInternalServerError
}

// readOpenCodeSSE owns the body and gives the first handler failure precedence.
// Closing the body stops a blocked read; buffered events still need suppression.
func readOpenCodeSSE(body io.ReadCloser, handle func(providerkit.SSEEvent) error) error {
	defer func() { _ = body.Close() }()
	var handlerErr error
	readErr := providerkit.ReadSSE(body, openCodeEventReadLimit, func(event providerkit.SSEEvent) {
		if handlerErr != nil {
			return
		}
		handlerErr = handle(event)
		if handlerErr != nil {
			_ = body.Close()
		}
	})
	if handlerErr != nil {
		return handlerErr
	}
	return readErr
}

type openCodeShellInfo struct {
	ID       string `json:"id"`
	Metadata struct {
		SessionID string `json:"sessionID"`
	} `json:"metadata"`
}

type openCodeShellOutput struct {
	Output    string `json:"output"`
	Cursor    int64  `json:"cursor"`
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated"`
}

func (page *openCodeShellOutput) UnmarshalJSON(raw []byte) error {
	var wire struct {
		Output    *string `json:"output"`
		Cursor    *int64  `json:"cursor"`
		Size      *int64  `json:"size"`
		Truncated *bool   `json:"truncated"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	if wire.Output == nil || wire.Cursor == nil || wire.Size == nil || wire.Truncated == nil {
		return errors.New("the native shell output requires output, cursor, size, and a truncated flag")
	}
	if *wire.Cursor < 0 || *wire.Size < *wire.Cursor {
		return errors.New("the native shell output has an invalid byte cursor")
	}
	*page = openCodeShellOutput{Output: *wire.Output, Cursor: *wire.Cursor, Size: *wire.Size, Truncated: *wire.Truncated}
	return nil
}

func openCodeShellQuery(location openCodeLocation) url.Values {
	query := make(url.Values)
	if location.Directory != "" {
		query.Set("location[directory]", location.Directory)
	}
	return query
}

func (r openCodeRPC) shellInfo(ctx context.Context, id string, location openCodeLocation) (openCodeShellInfo, error) {
	var response struct {
		Data *openCodeShellInfo `json:"data"`
	}
	if err := openCodeRouteID(id); err != nil {
		return openCodeShellInfo{}, err
	}
	if err := r.callQuery(ctx, http.MethodGet, "/api/shell/"+id, openCodeShellQuery(location), nil, &response); err != nil {
		return openCodeShellInfo{}, err
	}
	if response.Data == nil {
		return openCodeShellInfo{}, errors.New("the native shell response has no data")
	}
	return *response.Data, nil
}

func (r openCodeRPC) shellOutput(ctx context.Context, id string, location openCodeLocation, cursor int64) (openCodeShellOutput, error) {
	var response struct {
		Data *openCodeShellOutput `json:"data"`
	}
	if err := openCodeRouteID(id); err != nil {
		return openCodeShellOutput{}, err
	}
	query := openCodeShellQuery(location)
	query.Set("cursor", strconv.FormatInt(cursor, 10))
	query.Set("limit", strconv.Itoa(openCodeShellPageSize))
	if err := r.callQuery(ctx, http.MethodGet, "/api/shell/"+id+"/output", query, nil, &response); err != nil {
		return openCodeShellOutput{}, err
	}
	if response.Data == nil {
		return openCodeShellOutput{}, errors.New("the native shell output response has no data")
	}
	return *response.Data, nil
}
