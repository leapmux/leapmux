package kimi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Kimi Code's REST transport.
//
// Every reply is an envelope `{code, msg, data, request_id}`, and the HTTP
// status says almost nothing: the server answers a refused request with 200 and
// a non-zero `code`. So the envelope is decoded first, and a non-zero code is an
// error even on a 2xx reply. A non-2xx reply usually carries the same envelope,
// which states the reason better than the status line does.

// kimiEnvelope is one REST reply.
type kimiEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// kimiAPIError is a request the server refused with a non-zero envelope code.
type kimiAPIError struct {
	Method string
	Path   string
	Code   int
	Msg    string
	// Status is the HTTP status of the reply, or 0 for a 2xx reply.
	Status int
}

func (e *kimiAPIError) Error() string {
	msg := e.Msg
	if msg == "" {
		msg = "the request was refused"
	}
	return fmt.Sprintf("%s %s: %s (code %d)", e.Method, e.Path, msg, e.Code)
}

// kimiErrorCode reports the envelope code of err, or false when err is not a
// refusal the server stated (a transport failure, a timeout).
func kimiErrorCode(err error) (int, bool) {
	var apiErr *kimiAPIError
	if errors.As(err, &apiErr) {
		return apiErr.Code, true
	}
	return 0, false
}

// kimiClient sends REST requests to one kap-server.
type kimiClient struct {
	endpoint *providerkit.HTTPEndpoint
	// timeout limits each request that states no deadline of its own.
	timeout time.Duration
}

// call sends one request and decodes the envelope's `data` into out, which may
// be nil. A reply whose code is 0, or one of accept, succeeds; every other code
// returns a *kimiAPIError. A request whose context has no deadline takes the
// client's timeout. path carries no query: no route the provider calls takes one.
func (c *kimiClient) call(ctx context.Context, method, path string, body, out any, accept ...int) error {
	if _, ok := ctx.Deadline(); !ok && c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	var env kimiEnvelope
	err := c.endpoint.Do(ctx, method, path, body, &env)
	if err != nil {
		var statusErr *providerkit.HTTPStatusError
		if errors.As(err, &statusErr) {
			// The body of a refused request is usually the envelope, and its
			// message is the reason worth reporting.
			var refused kimiEnvelope
			if json.Unmarshal([]byte(statusErr.Body), &refused) == nil && refused.Code != kimiCodeOK {
				return &kimiAPIError{Method: method, Path: path, Code: refused.Code, Msg: refused.Msg, Status: statusErr.StatusCode}
			}
		}
		return err
	}
	if env.Code != kimiCodeOK && !slices.Contains(accept, env.Code) {
		return &kimiAPIError{Method: method, Path: path, Code: env.Code, Msg: strings.TrimSpace(env.Msg)}
	}
	if out == nil || len(env.Data) == 0 || string(env.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("%s %s: decode the reply data: %w", method, path, err)
	}
	return nil
}

// get sends a GET.
func (c *kimiClient) get(ctx context.Context, path string, out any) error {
	return c.call(ctx, "GET", path, nil, out)
}

// post sends a POST with body, which is sent as an empty object when nil: the
// action routes (`:abort`, `:dismiss`) refuse a request with no body at all.
func (c *kimiClient) post(ctx context.Context, path string, body, out any, accept ...int) error {
	if body == nil {
		body = struct{}{}
	}
	return c.call(ctx, "POST", path, body, out, accept...)
}
