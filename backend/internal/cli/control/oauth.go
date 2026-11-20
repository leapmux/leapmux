package control

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/loopbackauth"
	"github.com/leapmux/leapmux/machinelabel"
)

// The pieces every `/oauth/...` stage shares.
//
// Four flows post a form to that surface: the login (both the local-redirect
// exchange and the device-code poll), the token rotation, the revoke, and
// the step-up. They live in two packages -- the flows a user starts are in
// `cmd`, and the ones the transport starts by itself are here -- and they
// used to restate the same wire shapes in both. The wire helpers now live in
// loopbackauth beside the ceremony, where the desktop sidecar reaches them
// too; what stays here is the CLI's own protocol state: the grant shape, the
// poll cadence, and the answers it maps.

// OAuth 2.0 grant-type wire identifiers (RFC 6749 sections 4.1.3 and 6,
// RFC 8628 section 3.4), from oauthapp beside the built-in ids -- one home
// for the hub's server and this client, rather than a copy per side.
const (
	GrantTypeAuthorizationCode = oauthapp.GrantTypeAuthorizationCode
	GrantTypeDeviceCode        = oauthapp.GrantTypeDeviceCode
	GrantTypeRefreshToken      = oauthapp.GrantTypeRefreshToken
)

// ControlCLIClientID is the app this CLI authenticates as.
//
// It is read from internal/hub/oauthapp, the one package every reader of the
// built-in ids shares: the hub's migrations seed the row under the same
// constant, and a local literal here made a rename a silent runtime mismatch
// between the CLI's login and the row it authenticates against. The boundary
// the copy used to defend -- "the CLI must not depend on hub internals" -- was
// already crossed elsewhere (cmd imports internal/hub/service and hub/ratelimit),
// and oauthapp is a dependency-free constants package.
const ControlCLIClientID = oauthapp.ControlCLIClientID

// ControlCLIRedirectURI is the registered loopback redirect, re-exported for
// the same reason and from the same one home: the local-redirect login binds
// an ephemeral port of THIS address, and a local literal could drift from the
// row the hub seeds.
const ControlCLIRedirectURI = oauthapp.ControlCLIRedirectURI

// CLIApp is this CLI's loopback ceremony identity for the app a credential is
// bound to. The login and the step-up both call it: a credential minted to
// another registration must redeem its code through that registration's own
// id, or the hub answers "this grant was issued to a different app".
func CLIApp(clientID string) loopbackauth.App {
	app := loopbackauth.ControlCLI
	app.ClientID = clientID
	return app
}

// OpenBrowser opens the system browser at url. It is a variable so a test of
// the loopback ceremony replaces it: nothing may launch a real browser
// mid-test.
var OpenBrowser func(url string) error = openBrowser

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

const (
	// DeviceCodePollFallback is the poll cadence a device-flow client uses
	// when the hub specifies none.
	DeviceCodePollFallback = 5 * time.Second
	// DeviceCodeSlowDownStep is how much a slow_down answer adds to the
	// cadence. RFC 8628 section 3.5 makes the increase the client's choice
	// and requires only that it grows.
	DeviceCodeSlowDownStep = 5 * time.Second
)

// DeviceGrant is the hub's answer to a device-authorization request.
//
// ONE shape for the login and for the step-up, because it is one endpoint
// family and one poll: /oauth/device-authorization and /oauth/step-up return
// the same body, and the CLI polls both at /oauth/token with the same device
// code.
type DeviceGrant struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// PollInterval is the cadence to poll this grant at: the hub's own interval
// when it specified one, and the fallback otherwise.
func (g DeviceGrant) PollInterval() time.Duration {
	if interval := time.Duration(g.Interval) * time.Second; interval > 0 {
		return interval
	}
	return DeviceCodePollFallback
}

// Deadline is when this grant expires, measured from now.
func (g DeviceGrant) Deadline(now time.Time) time.Time {
	return now.Add(time.Duration(g.ExpiresIn) * time.Second)
}

// ErrDeviceGrantExpired reports a poll loop that reached the grant's deadline
// without a final answer. The two ceremonies word their user-facing expiry
// messages differently, so they translate this sentinel rather than sharing
// its text.
var ErrDeviceGrantExpired = errors.New("device grant expired")

// Poll runs the RFC 8628 section 3.5 cadence for this grant: sleep the
// interval, call poll, honour slow_down by growing the cadence, stop at the
// deadline.
//
// ONE loop for the login and the step-up, because the two ceremonies poll the
// same endpoint family under the same throttle and the same two interim
// answers -- the loop is protocol state, not per-ceremony choice, and the two
// hand-kept copies it replaces were line-for-line identical. What stays at
// each call site is everything ceremony-specific: which function performs one
// poll, and how each outcome reads to the user.
//
// The expiry error is ErrDeviceGrantExpired; context cancellation returns
// ctx.Err(); any other error from poll is returned as it came.
func (g DeviceGrant) Poll(ctx context.Context, poll func(context.Context) error) error {
	interval := g.PollInterval()
	deadline := g.Deadline(time.Now())
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
		err := poll(ctx)
		switch {
		case errors.Is(err, ErrAuthorizationPending):
			continue
		case errors.Is(err, ErrSlowDown):
			interval += DeviceCodeSlowDownStep
			continue
		case err != nil:
			return err
		}
		return nil
	}
	return ErrDeviceGrantExpired
}

// The two device-flow answers that mean "the person is not finished; poll
// again". Every other answer ends the poll.
var (
	ErrAuthorizationPending = errors.New("authorization_pending")
	ErrSlowDown             = errors.New("slow_down")
)

// DeviceFlowError maps a refused /oauth/token poll onto the error to
// report. The login and the step-up poll the same endpoint, so they read
// its refusals the same way.
func DeviceFlowError(resp *http.Response) error {
	body := loopbackauth.ReadOAuthError(resp)
	switch body.Error {
	case "authorization_pending":
		return ErrAuthorizationPending
	case "slow_down":
		return ErrSlowDown
	case "":
		return fmt.Errorf("the hub refused the device-code poll: %s", resp.Status)
	default:
		return errors.New(body.Message())
	}
}

// DefaultDeviceName is the label this machine records on a grant, and the
// label the approval page shows the person as the thing that asks.
//
// ONE answer for the login and for the step-up. The two used to compute it
// differently -- "user@host" for a login and a bare hostname for a step-up
// -- so the page that asked a user to approve a step-up specified a device
// that matched nothing in their credential list.
func DefaultDeviceName() string {
	return machinelabel.InstallationName()
}

// MissingScopes reports which requested permissions a grant did not include.
//
// The device flow is where this matters: the request and the consent happen on
// two machines, and the person at the browser may hold an account that cannot
// grant part of the ask. Saying so at the end of the login is what stops the
// first call that needs one from failing with a permission error the user
// cannot connect to anything they did.
//
// An EMPTY request asks for the hub's default grant, so it can miss nothing.
func MissingScopes(requested, granted string) []string {
	if strings.TrimSpace(requested) == "" {
		return nil
	}
	have := make(map[string]bool)
	for _, scope := range strings.Fields(granted) {
		have[scope] = true
	}
	var missing []string
	for _, scope := range strings.Fields(requested) {
		if !have[scope] {
			missing = append(missing, scope)
		}
	}
	return missing
}
