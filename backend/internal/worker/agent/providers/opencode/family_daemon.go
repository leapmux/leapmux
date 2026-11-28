package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// ErrDaemonUnavailable reports that the daemon HTTP server could not be
// discovered or became unavailable.
var ErrDaemonUnavailable = errors.New("the daemon HTTP server is unavailable")

// FamilyDaemon owns the discovered loopback HTTP address, readiness state, and
// route requests for one OpenCode-family daemon (OpenCode or Kilo).
// Its zero value is safe to wait on and lazily initializes its synchronization
// state and HTTP client.
type FamilyDaemon struct {
	mu        sync.Mutex
	baseURL   string
	client    *http.Client
	readyCh   chan struct{}
	readyDone bool
	readyErr  error
}

// WaitReady blocks until daemon discovery completes or ctx ends.
// It returns nil once the daemon address is known, ErrDaemonUnavailable if
// discovery failed, or ctx.Err() if the caller stops waiting first.
func (d *FamilyDaemon) WaitReady(ctx context.Context) error {
	d.mu.Lock()
	if d.readyDone {
		err := d.readyErr
		d.mu.Unlock()
		return err
	}
	if d.readyCh == nil {
		d.readyCh = make(chan struct{})
	}
	ch := d.readyCh
	d.mu.Unlock()

	select {
	case <-ch:
		d.mu.Lock()
		err := d.readyErr
		d.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Get sends a GET request to route on the discovered daemon and returns the
// 200 OK body, capped at openCodeQuestionMaxEvent bytes.
func (d *FamilyDaemon) Get(ctx context.Context, route string) ([]byte, error) {
	raw, _, err := d.request(ctx, http.MethodGet, route, nil)
	return raw, err
}

// GetPage sends a GET request to route on the discovered daemon and returns
// the body along with the daemon's pagination cursor header for the next older page.
func (d *FamilyDaemon) GetPage(ctx context.Context, route string) ([]byte, string, error) {
	raw, headers, err := d.request(ctx, http.MethodGet, route, nil)
	return raw, headers.Get(openCodeNextCursorHeader), err
}

// Post sends a JSON POST request to route on the discovered daemon and returns
// the 200 OK body.
func (d *FamilyDaemon) Post(ctx context.Context, route string, body json.RawMessage) ([]byte, error) {
	raw, _, err := d.request(ctx, http.MethodPost, route, body)
	return raw, err
}

// MarkUnavailableForTest marks daemon discovery as failed so tests can unblock
// callers waiting on WaitReady without starting process discovery.
func (d *FamilyDaemon) MarkUnavailableForTest(err error) {
	d.markUnavailable(err)
}

func (d *FamilyDaemon) base() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.baseURL
}

func (d *FamilyDaemon) setBaseURL(baseURL string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.baseURL = baseURL
}

func (d *FamilyDaemon) httpClient() *http.Client {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.client == nil {
		// No client timeout: the event stream stays open for the life of the
		// session. Individual route requests carry deadlines on their context.
		d.client = &http.Client{}
	}
	return d.client
}

// markReady records baseURL and unblocks any callers waiting in WaitReady.
// It returns true only on the first readiness transition, making repeated
// announcements idempotent.
func (d *FamilyDaemon) markReady(baseURL string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.baseURL = baseURL
	if d.readyDone {
		return false
	}
	d.readyDone = true
	d.readyErr = nil
	if d.readyCh != nil {
		close(d.readyCh)
	}
	return true
}

// markUnavailable records that daemon discovery failed and unblocks any
// callers waiting in WaitReady. It returns true only on the first transition.
func (d *FamilyDaemon) markUnavailable(cause error) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.readyDone {
		return false
	}
	d.readyDone = true
	if cause != nil {
		d.readyErr = fmt.Errorf("%w: %w", ErrDaemonUnavailable, cause)
	} else {
		d.readyErr = ErrDaemonUnavailable
	}
	if d.readyCh != nil {
		close(d.readyCh)
	}
	return true
}

func (d *FamilyDaemon) request(ctx context.Context, method, route string, body json.RawMessage) ([]byte, http.Header, error) {
	base := d.base()
	if base == "" {
		return nil, nil, fmt.Errorf("the OpenCode daemon address is unavailable")
	}
	request, err := http.NewRequestWithContext(ctx, method, base+route, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := d.httpClient().Do(request)
	if err != nil {
		return nil, nil, fmt.Errorf("read the OpenCode route %s: %w", route, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 1<<12))
		return nil, nil, fmt.Errorf("the OpenCode route %s answered %s: %s", route, response.Status, bytes.TrimSpace(detail))
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, openCodeQuestionMaxEvent+1))
	if err != nil {
		return nil, nil, fmt.Errorf("read the OpenCode route %s: %w", route, err)
	}
	if len(raw) > openCodeQuestionMaxEvent {
		return nil, nil, fmt.Errorf("the OpenCode route %s answered more than %d bytes", route, openCodeQuestionMaxEvent)
	}
	return raw, response.Header, nil
}
