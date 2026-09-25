package amp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// The permission helper: the program that Amp's `delegate` permission rule
// starts for each tool call its local executor runs.
//
// Amp starts the worker's own executable with NO argument (see
// agent.HelperFunc for how the executable finds this function), writes the
// call's input as JSON to stdin, sets AGENT_TOOL_NAME, and reads the exit code:
//
//   - 0 allows the call.
//   - 2 refuses it. With text on stderr, Amp hands the model
//     "Plugin error: <text>". With no text, Amp refuses in its own wording.
//
// The helper never exits with code 1: it means "ask", which execute mode turns
// into a refusal that drops the reason. Every failure of the helper itself
// refuses with the reason on stderr, so the model and the user can see it.
//
// The helper writes NOTHING else to stderr, and it logs nothing: Amp reads
// stderr as the refusal reason.

// helperConfig is the helper's part of the spec file, which only the owner can
// read. It holds the agent's bridge secret, which therefore reaches neither
// argv nor the environment.
type helperConfig struct {
	// Endpoint is the path of the bridge's socket.
	Endpoint string `json:"endpoint"`
	Secret   string `json:"secret"`
}

const (
	helperExitAllow  = 0
	helperExitReject = 2
)

// helperDialTimeout limits the connection to the bridge. The bridge is a
// local socket, so a connection that takes longer finds no agent.
const helperDialTimeout = 10 * time.Second

// runPermissionHelper asks the agent whether one tool call may run, and exits
// with Amp's code for the answer. It waits for as long as the user needs, and
// it refuses the call when the agent goes away or the process receives a
// termination signal.
func runPermissionHelper(ctx context.Context, invocation agent.HelperInvocation) int {
	refuse := func(format string, args ...any) int {
		_, _ = fmt.Fprintf(invocation.Stderr, format+"\n", args...)
		return helperExitReject
	}

	var config helperConfig
	if err := json.Unmarshal(invocation.Config, &config); err != nil || config.Endpoint == "" || config.Secret == "" {
		return refuse("LeapMux could not read the configuration of its permission helper.")
	}
	tool := invocation.Getenv(envToolName)
	if tool == "" {
		return refuse("This LeapMux helper answers only Amp's delegate permission rule, which states the tool in %s.", envToolName)
	}
	input, err := io.ReadAll(io.LimitReader(invocation.Stdin, maxHelperMessageBytes+1))
	if err != nil {
		return refuse("LeapMux could not read the input of the %s call: %v", tool, err)
	}
	if len(input) > maxHelperMessageBytes {
		return refuse("The input of the %s call is too large for LeapMux to show.", tool)
	}
	input = bytes.TrimSpace(input)
	if len(input) == 0 {
		input = []byte("{}")
	}
	if !json.Valid(input) {
		return refuse("The input of the %s call is not valid JSON.", tool)
	}

	decision, err := askBridge(ctx, config, helperRequest{
		Tool:   tool,
		Thread: invocation.Getenv(envThreadID),
		Input:  input,
	})
	if err != nil {
		return refuse("LeapMux ended the permission request before an answer came: %v", err)
	}
	switch decision.Decision {
	case decisionAllow:
		return helperExitAllow
	case decisionReject:
		if message := strings.TrimSpace(decision.Message); message != "" {
			_, _ = fmt.Fprintln(invocation.Stderr, message)
		}
		return helperExitReject
	default:
		return refuse("LeapMux sent an answer this helper does not know: %q", decision.Decision)
	}
}

// askBridge sends one request to the agent's bridge and waits for the answer.
// ctx ends the wait: the connection closes, and the bridge withdraws the
// request.
//
// It checks first that no other user could have put the socket in place, and
// it sends the secret only then (see checkPrivateSocket).
func askBridge(ctx context.Context, config helperConfig, request helperRequest) (helperDecision, error) {
	if err := checkPrivateSocket(config.Endpoint); err != nil {
		return helperDecision{}, fmt.Errorf("the agent's socket is not private: %w", err)
	}
	dialer := net.Dialer{Timeout: helperDialTimeout}
	conn, err := dialer.DialContext(ctx, bridgeNetwork, config.Endpoint)
	if err != nil {
		return helperDecision{}, fmt.Errorf("the agent is not running: %w", err)
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	encoded, err := json.Marshal(request)
	if err != nil {
		return helperDecision{}, err
	}
	// The secret goes on a line of its own, first, so the bridge reads nothing
	// large before it checks the secret. The connection stays open in both
	// directions until the answer arrives: the bridge reads a close of this
	// side as the helper going away.
	message := make([]byte, 0, len(config.Secret)+len(encoded)+2)
	message = append(append(append(message, config.Secret...), '\n'), encoded...)
	if _, err := conn.Write(append(message, '\n')); err != nil {
		return helperDecision{}, err
	}
	decision, err := readHelperLine[helperDecision](bufio.NewReader(io.LimitReader(conn, maxHelperMessageBytes+1)))
	if err != nil {
		if ctx.Err() != nil {
			return helperDecision{}, errors.New("the helper received a termination signal")
		}
		if errors.Is(err, io.EOF) {
			return helperDecision{}, errors.New("the agent closed the connection")
		}
		return helperDecision{}, err
	}
	return decision, nil
}
