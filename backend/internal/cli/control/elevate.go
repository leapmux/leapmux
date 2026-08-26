package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/leapmux/leapmux/locallisten"
)

// Step-up ("sudo mode") for this CLI's own credential.
//
// The hub refuses a sensitive action from a credential that proved no factor
// recently, and marks that refusal with ElevationRequiredHeader. The marker
// means "prove a factor and retry", and this file is what does it.
//
// The factor is proven IN A BROWSER, and deliberately somewhere this process
// cannot reach: a CLI that could elevate itself from the terminal would hand a
// stolen credential file everything the window exists to withhold. So the
// ceremony is the device-code ceremony -- ask, show a URL and a code, wait for
// a human -- and it works from an SSH session or a container, where the
// browser is on another machine entirely.
//
// What it grants is a WINDOW on the credential this CLI already holds. It
// mints nothing: the file on disk is unchanged, and every gated command inside
// the window lands without another prompt.

const (
	// ElevationRequiredHeader marks a hub refusal whose remedy is "prove a
	// factor and retry". It mirrors the constant the hub sets; the value is
	// always "1" and only its presence is meaningful.
	//
	// Duplicated rather than imported, and the duplication is the point: the
	// CLI must not depend on the hub's internal packages, and this is a WIRE
	// contract between two programs that a user can upgrade separately.
	// service.ElevationRequiredHeader carries the same note.
	ElevationRequiredHeader = "Leapmux-Elevation-Required"

	// elevationPollFallback is the poll cadence used when the hub names none.
	elevationPollFallback = 5 * time.Second
	// elevationSlowDownStep is how much a slow_down answer adds to the
	// cadence, matching what the login flow does with the same signal.
	elevationSlowDownStep = 5 * time.Second
)

// ErrElevationUnsupported reports a credential the hub will not elevate: a
// worker-minted delegation token, or an IPC client with no bearer at all.
// Neither has a browser ceremony to run, so the caller reports the hub's
// original refusal rather than a failed step-up.
var ErrElevationUnsupported = errors.New("this credential cannot be verified in a browser")

// NeedsElevation reports whether a hub error is the marked refusal.
//
// The MARKER, never the message. The wording is user-facing prose that will be
// reworded, and matching it would break on the first edit; the code alone is
// too broad, because a FailedPrecondition also means "this account has no
// password" and half a dozen other things a step-up cannot fix.
func NeedsElevation(err error) bool {
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		return false
	}
	return connectErr.Meta().Get(ElevationRequiredHeader) != ""
}

// Elevate runs one browser step-up for this client's credential and returns
// when the hub reports it granted.
//
// It BLOCKS on a human, for up to the grant's own lifetime, and prints the
// address and the code to this package's Out. A caller that cannot show a
// prompt -- a script, a hook -- should test NeedsElevation and report the
// hub's refusal instead of calling this.
func (c *Client) Elevate(ctx context.Context) error {
	hc, baseURL := c.HTTPClient, c.connectURL
	grant, err := c.startElevation(ctx, hc, baseURL)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(Out, "This command needs you to verify your identity.")
	_, _ = fmt.Fprintln(Out, "  1. Visit", grant.VerificationURI)
	_, _ = fmt.Fprintln(Out, "  2. Enter the code:", grant.UserCode)
	if grant.VerificationURIComplete != "" {
		_, _ = fmt.Fprintln(Out, "Or open:", grant.VerificationURIComplete)
	}

	interval := time.Duration(grant.Interval) * time.Second
	if interval <= 0 {
		interval = elevationPollFallback
	}
	deadline := time.Now().Add(time.Duration(grant.ExpiresIn) * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
		err := c.pollElevation(ctx, hc, baseURL, grant.DeviceCode)
		switch {
		case errors.Is(err, errElevationPending):
			continue
		case errors.Is(err, errElevationSlowDown):
			interval += elevationSlowDownStep
			continue
		case err != nil:
			return err
		}
		return nil
	}
	return errors.New("the verification request expired before it was approved")
}

// elevationGrant is the hub's answer to a step-up request. It is the SAME
// body shape the device-code login returns, because the poll below is the same
// endpoint.
type elevationGrant struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// elevationDeviceName is what the approval page shows the person as the thing
// asking. The hostname is what a login already records as the client name, so
// the two read alike in the credential list and on the page.
func (c *Client) elevationDeviceName() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "a command-line credential"
	}
	return host
}

func (c *Client) startElevation(ctx context.Context, hc *http.Client, baseURL string) (*elevationGrant, error) {
	form := url.Values{"device_name": {c.elevationDeviceName()}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		locallisten.JoinPath(baseURL, "/auth/cli/elevate-authorization"),
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// The credential itself is the right to ASK. What it cannot do is
	// approve, which needs a browser session that proves a factor.
	c.ApplyAuth(req.Header)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrElevationUnsupported
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the hub refused the verification request: %s", resp.Status)
	}
	grant := &elevationGrant{}
	if err := json.NewDecoder(resp.Body).Decode(grant); err != nil {
		return nil, err
	}
	if grant.DeviceCode == "" {
		return nil, errors.New("the hub returned no verification code")
	}
	return grant, nil
}

var (
	errElevationPending  = errors.New("authorization_pending")
	errElevationSlowDown = errors.New("slow_down")
)

// pollElevation performs one /auth/cli/token poll for a step-up grant. It is
// the login flow's poll without the credential handling: a step-up mints
// nothing, so there is nothing to persist. The hub answers `{"elevated":true}`
// once the window is stamped.
func (c *Client) pollElevation(ctx context.Context, hc *http.Client, baseURL, deviceCode string) error {
	form := url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {deviceCode},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		locallisten.JoinPath(baseURL, "/auth/cli/token"),
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	var oerr struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&oerr)
	switch oerr.Error {
	case "authorization_pending":
		return errElevationPending
	case "slow_down":
		return errElevationSlowDown
	default:
		return fmt.Errorf("%s: %s", oerr.Error, oerr.ErrorDescription)
	}
}
