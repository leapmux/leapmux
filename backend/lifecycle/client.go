package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/leapmux/leapmux/locallisten"
)

// ErrUnresponsive reports an instance whose runtime lock is held while
// its control endpoint does not answer: evidence says the process still
// exists, so "stopped" would be a guess, and a guess is what this
// package must not make.
var ErrUnresponsive = errors.New("the instance holds its runtime lock but does not answer its control endpoint")

// controlClient talks to one instance's control endpoint.
type controlClient struct {
	url   string
	token string
	http  *http.Client
}

// newControlClient builds a client for the endpoint at url, reachable
// through the local transport the URL identifies.
func newControlClient(url, token string) (*controlClient, error) {
	dial, err := locallisten.Dialer(url)
	if err != nil {
		return nil, err
	}
	return &controlClient{
		url:   url,
		token: token,
		http: &http.Client{
			Transport: &http.Transport{
				DialContext: locallisten.HTTPDialContext(dial),
			},
		},
	}, nil
}

// Status fetches the live snapshot.
func (c *controlClient) Status(ctx context.Context) (Status, error) {
	var status Status
	err := c.do(ctx, http.MethodGet, controlStatusPath, nil, &status)
	return status, err
}

// RequestStop posts the stop request with the expected instance and
// attempt identities, so the server can refuse a client that inspected
// a process this one replaced.
func (c *controlClient) RequestStop(ctx context.Context, req StopRequest) (StopResponse, error) {
	var resp StopResponse
	err := c.do(ctx, http.MethodPost, controlStopPath, req, &resp)
	return resp, err
}

// do performs one authenticated request against the endpoint.
func (c *controlClient) do(ctx context.Context, method, path string, body, out any) error {
	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, "http://leapmux.local"+path, reader)
	if err != nil {
		return err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		var refusal struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&refusal)
		return fmt.Errorf("lifecycle: the control endpoint refused (%d): %s", resp.StatusCode, refusal.Error)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Observe reconciles the evidence about one instance into a Status:
// the persisted runtime record, the runtime lock, the authenticated
// endpoint, and the saved specification. A missing endpoint alone is
// never evidence that a process stopped.
//
// dataDirectory is the instance's canonical root. The returned Status
// identifies the process state the evidence supports, and holds
// ProcessUnresponsive with ErrUnresponsive when the lock is held and
// the endpoint stays silent.
func Observe(ctx context.Context, dataDirectory string) (Status, error) {
	canonical, err := CanonicalDir(dataDirectory)
	if err != nil {
		return Status{}, err
	}
	paths := pathsFor(canonical)

	status := Status{
		Process:      ProcessStatus{State: ProcessStopped},
		Registration: RegistrationStatus{State: RegistrationNotApplicable},
		Connection:   ConnectionStatus{State: ConnectionNotApplicable},
		Diagnostics:  []Diagnostic{},
	}

	record, recordErr := readRuntimeRecordOf(paths)
	spec, specErr := ReadServiceSpecOf(paths)
	if specErr == nil {
		status.Startup = &spec.Startup
		status.Configured = true
		status.Mode = new(string(spec.Mode))
		status.InstanceID = new(spec.InstanceID)
	}
	if recordErr == nil {
		status.InstanceID = new(record.InstanceID)
		status.Mode = new(string(record.Mode))
		status.Execution = new(record.Execution)
		started := record.StartedAt
		status.Attempt = AttemptStatus{
			ID:        new(record.AttemptID),
			StartedAt: &started,
			LogPath:   new(paths.AttemptLogPath(record.AttemptID)),
		}
		pid := record.PID
		status.Process = ProcessStatus{State: ProcessStopped, PID: &pid, StartedAt: &started}
	}

	lockHeld, probeErr := probeExclusiveLock(paths.RuntimeLock)
	if probeErr != nil {
		return unresponsiveStatus(status, fmt.Errorf("inspect the runtime lock: %w", probeErr))
	}

	// No record and no lock: nothing claims this directory.
	if recordErr != nil {
		if lockHeld {
			return unresponsiveStatus(status, fmt.Errorf("read the runtime record: %w", recordErr))
		}
		if errors.Is(recordErr, os.ErrNotExist) {
			status.Process.State = ProcessStopped
			return status, nil
		}
		return status, fmt.Errorf("read the runtime record: %w", recordErr)
	}

	if !lockHeld {
		if record.Status != nil {
			status = *record.Status
			if !status.matchesAttempt(record) {
				return status, errors.New("the saved status belongs to another process attempt")
			}
			if status.Attempt.EndedAt == nil {
				status.Process.State = ProcessFailed
				status.Readiness = new(ReadinessFailed)
				reason := "the process exited without recording its shutdown outcome"
				status.Attempt.Error = &reason
			}
			return status, nil
		}
		status.Process.State = ProcessStopped
		return status, nil
	}

	// The lock is held. Ask the endpoint; a silent one under a held
	// lock is an unresponsive instance, never a stopped one.
	token, tokenErr := ReadControlToken(paths.ControlToken)
	if tokenErr != nil {
		return unresponsiveStatus(status, fmt.Errorf("read the control token: %w", tokenErr))
	}
	if token == "" {
		return unresponsiveStatus(status, errors.New("the control token is missing or empty"))
	}
	client, clientErr := newControlClient(record.ControlURL, token)
	if clientErr != nil {
		return unresponsiveStatus(status, fmt.Errorf("create the local control client: %w", clientErr))
	}
	defer client.http.CloseIdleConnections()
	live, liveErr := client.Status(ctx)
	if liveErr != nil {
		return unresponsiveStatus(status, fmt.Errorf("request local status: %w", liveErr))
	}
	if !live.matchesAttempt(record) {
		return unresponsiveStatus(status, errors.New("the response belongs to another instance or process attempt"))
	}
	return live, nil
}

func unresponsiveStatus(status Status, cause error) (Status, error) {
	status.Process.State = ProcessUnresponsive
	status.Diagnostics = append(status.Diagnostics, Diagnostic{Kind: "error", Title: "Local control", Body: cause.Error()})
	return status, fmt.Errorf("%w: %w", ErrUnresponsive, cause)
}

// RequestStop asks the running instance to stop and reports whether the
// request was accepted. The caller supplies the record it inspected, so
// the server can detect a client that raced with a replacement.
func RequestStop(ctx context.Context, record RuntimeRecord) (StopResponse, error) {
	token, err := ReadControlToken(pathsFor(record.DataDir).ControlToken)
	if err != nil {
		return StopResponse{}, fmt.Errorf("read the control token: %w", err)
	}
	if token == "" {
		return StopResponse{}, ErrUnresponsive
	}
	client, err := newControlClient(record.ControlURL, token)
	if err != nil {
		return StopResponse{}, err
	}
	defer client.http.CloseIdleConnections()
	return client.RequestStop(ctx, StopRequest{InstanceID: record.InstanceID, AttemptID: record.AttemptID})
}

// WaitForLockRelease waits until the runtime lock of dataDirectory is
// free, the context ends, or the deadline passes. It reports false when
// the lock was still held at the end; the caller then reports that
// shutdown continues.
func WaitForLockRelease(ctx context.Context, dataDirectory string, timeout time.Duration) (bool, error) {
	canonical, err := CanonicalDir(dataDirectory)
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return waitForLockRelease(ctx, canonical)
}

func waitForLockRelease(ctx context.Context, canonical string) (bool, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		held, err := probeExclusiveLock(pathsFor(canonical).RuntimeLock)
		if err != nil {
			return false, err
		}
		if !held {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ticker.C:
		}
	}
}
