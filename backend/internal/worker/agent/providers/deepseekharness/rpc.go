package deepseekharness

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// remoteRPC owns unary Connection messages on the native Web server.
type remoteRPC struct {
	endpoint *providerkit.HTTPEndpoint
	timeout  time.Duration
}

type remoteFailure struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Details json.RawMessage `json:"details"`
}

func (e *remoteFailure) Error() string { return e.Code + ": " + e.Message }

// call validates the native response and its exact request identity.
func (r remoteRPC) call(ctx context.Context, method string, args map[string]any, out any) error {
	raw, err := r.value(ctx, method, args)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if len(raw) == 0 {
		return fmt.Errorf("DeepSeek Harness RPC response has no value")
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode the DeepSeek Harness RPC value: %w", err)
	}
	return nil
}

// value preserves an omitted native value. Each method decides whether omission is valid.
func (r remoteRPC) value(ctx context.Context, method string, args map[string]any) (json.RawMessage, error) {
	if r.endpoint == nil {
		return nil, fmt.Errorf("DeepSeek Harness has no connected endpoint")
	}
	if method == "" || strings.ContainsAny(method, "?\x00#") {
		return nil, fmt.Errorf("DeepSeek Harness RPC method is invalid")
	}
	if args == nil {
		args = map[string]any{}
	}
	id := uuid.NewString()
	request := struct {
		Type    string `json:"type"`
		ID      string `json:"rpcId"`
		Method  string `json:"method"`
		Payload struct {
			Args map[string]any `json:"args"`
		} `json:"payload"`
	}{Type: "client-request", ID: id, Method: method}
	request.Payload.Args = args
	var response struct {
		Type   string `json:"type"`
		ID     string `json:"rpcId"`
		Result *struct {
			OK    *bool           `json:"ok"`
			Value json.RawMessage `json:"value"`
			Error *remoteFailure  `json:"error"`
		} `json:"result"`
	}
	if r.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.timeout)
		defer cancel()
	}
	if err := r.endpoint.Do(ctx, http.MethodPost, "/api/"+method, request, &response); err != nil {
		return nil, err
	}
	if response.Type != "server-response" || response.ID != id || response.Result == nil || response.Result.OK == nil {
		return nil, fmt.Errorf("DeepSeek Harness RPC response has invalid fields or correlation")
	}
	if !*response.Result.OK {
		if response.Result.Error == nil || response.Result.Error.Code == "" || response.Result.Error.Message == "" {
			return nil, fmt.Errorf("DeepSeek Harness RPC failure has no cause")
		}
		return nil, response.Result.Error
	}
	if response.Result.Error != nil {
		return nil, fmt.Errorf("DeepSeek Harness RPC success carries an error")
	}
	return response.Result.Value, nil
}

func (r remoteRPC) request(ctx context.Context, method string, request, out any) error {
	return r.call(ctx, method, map[string]any{"request": request}, out)
}
