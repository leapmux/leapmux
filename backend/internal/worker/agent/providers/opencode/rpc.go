package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
)

type openCodeServiceState uint8

const (
	openCodeServiceReady openCodeServiceState = iota
	openCodeServiceStarting
)

// openCodeRPC owns a client connection pool, never the native server process.
type openCodeRPC struct {
	registration openCodeServiceRegistration
	client       *http.Client
	transport    *http.Transport
}

func newOpenCodeRPC(registration openCodeServiceRegistration) *openCodeRPC {
	transport := &http.Transport{
		DialContext:  (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns: 16, MaxIdleConnsPerHost: 8, IdleConnTimeout: 90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	return &openCodeRPC{registration: registration, transport: transport, client: &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (rpc *openCodeRPC) close() { rpc.transport.CloseIdleConnections() }

// request keeps credentials in the header and rejects redirects. The caller
// owns the response body and supplies a context for its operation lifetime.
func (rpc *openCodeRPC) request(ctx context.Context, method, route string, body []byte) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(rpc.registration.URL, "/")+route, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("construct the OpenCode HTTP request")
	}
	request.SetBasicAuth("opencode", rpc.registration.Password)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := rpc.client.Do(request)
	if err != nil {
		// A transport error can contain the complete URL. Keep private service
		// addresses and any provider-controlled response text out of diagnostics.
		return nil, fmt.Errorf("the OpenCode HTTP request failed: %w", sanitizeOpenCodeRequestError(ctx, err))
	}
	return response, nil
}

func sanitizeOpenCodeRequestError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return errors.New("the service could not be reached")
}

func readOpenCodeResponse(response *http.Response) ([]byte, error) {
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, contracts.MaxMessageSize+1))
	if err != nil {
		return nil, errors.New("read the OpenCode HTTP response")
	}
	if len(raw) > contracts.MaxMessageSize {
		return nil, errors.New("the OpenCode HTTP response exceeds the message limit")
	}
	return raw, nil
}

func (rpc *openCodeRPC) probe(ctx context.Context) (openCodeServiceState, error) {
	response, err := rpc.request(ctx, http.MethodGet, "/api/info", nil)
	if err != nil {
		return 0, err
	}
	raw, err := readOpenCodeResponse(response)
	if err != nil {
		return 0, err
	}
	var info struct {
		Version string `json:"version"`
		PID     int    `json:"pid"`
	}
	if json.Unmarshal(raw, &info) != nil || info.PID <= 0 || info.PID != rpc.registration.PID || info.Version != rpc.registration.Version {
		return 0, errors.New("the OpenCode service did not prove its registered PID and version")
	}
	switch response.StatusCode {
	case http.StatusOK:
		return openCodeServiceReady, nil
	case http.StatusServiceUnavailable:
		return openCodeServiceStarting, nil
	case http.StatusInternalServerError:
		return 0, errors.New("the OpenCode service failed to start")
	default:
		return 0, fmt.Errorf("the OpenCode health endpoint returned HTTP %d", response.StatusCode)
	}
}

// openCodeHTTPError retains native failure evidence without putting provider
// content or service credentials in an error string.
type openCodeHTTPError struct {
	StatusCode int
	Body       json.RawMessage
}

func (failure *openCodeHTTPError) Error() string {
	return fmt.Sprintf("the OpenCode endpoint returned HTTP %d", failure.StatusCode)
}

func (rpc *openCodeRPC) json(ctx context.Context, method, route string, input any) (json.RawMessage, error) {
	var body []byte
	if input != nil {
		var err error
		body, err = json.Marshal(input)
		if err != nil {
			return nil, errors.New("encode the OpenCode request")
		}
	}
	response, err := rpc.request(ctx, method, route, body)
	if err != nil {
		return nil, err
	}
	raw, err := readOpenCodeResponse(response)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &openCodeHTTPError{StatusCode: response.StatusCode, Body: bytes.Clone(raw)}
	}
	if response.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if !json.Valid(raw) {
		return nil, errors.New("the OpenCode endpoint returned invalid JSON")
	}
	return json.RawMessage(raw), nil
}
