package cmd

import (
	"io"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

// `agent approve`, `agent deny` and `agent answer` answer one pending
// control request. Each command builds a neutral decision from the request's
// prompt and its own flags, and the Worker's provider package turns the
// decision into the provider's answer (AnswerControl). The command only maps
// its flags onto the prompt: the Worker checks that the decision answers the
// prompt, so the CLI and the browser cannot disagree about what is valid.

// controlAnswerResult is the result of an answer.
type controlAnswerResult struct {
	// ID is the handle of the answered request.
	ID      string `json:"id"`
	AgentID string `json:"agent_id"`
	// State is the delivery of the answer: "completed" when the agent has it
	// and the transcript records it, "delivered" when the agent has it and
	// the record follows, and "pending" when the Worker holds it and its
	// delivery follows.
	State string `json:"state"`
}

// controlAnswerRequest configures one control-answer command invocation.
type controlAnswerRequest struct {
	// extraTexts, when non-nil, carries raw answer texts for `agent answer`
	// (from positional [text]... and --text flags) and allows a second
	// positional arg to shift into texts when the first arg resolved as a
	// global request handle. Any "-" entry is read lazily from stdin once the
	// target request is confirmed answerable.
	extraTexts []string
	// allowQuestionTextShift permits an unrecognized [request] arg to shift
	// into texts when the agent has a single pending question and no explicit
	// --option, --field, or --text flag was given.
	allowQuestionTextShift bool
	// stdin lazily reads "-" at most once when an answer text consumes stdin.
	stdin *lazyStdin
	// decide builds the neutral decision for prompt and the final answer texts.
	decide func(prompt *leapmuxv1.ControlPrompt, texts []string) (*leapmuxv1.ControlDecision, error)
}

// lazyStdin reads standard input at most once when an answer text uses "-".
type lazyStdin struct {
	r    io.Reader
	used bool
}

func (l *lazyStdin) read() (string, error) {
	if l.used {
		return "", cli.Usagef(`only one text can be "-"`)
	}
	data, err := readStdinGuarded(l.r)
	if err != nil {
		return "", err
	}
	l.used = true
	return string(data), nil
}

// runControlAnswer answers the request that the arguments give with the
// decision that req.decide builds. It reuses one Noise_NK channel across
// ListControlRequests and AnswerControl when resolving through an agent.
//
// When [request] is omitted:
//   - If <agent> resolves to an agent with exactly one pending request, that
//     request is selected automatically (2+ fails with ambiguous_target).
//   - If <agent> does not resolve to an agent, it searches pending requests
//     across all agents for a globally unique handle or prefix.
func runControlAnswer(inv *cli.Invocation, req controlAnswerRequest) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	agentArg := inv.Arg("agent")
	requestArg := ""
	hasRequestArg := inv.HasArg("request")
	if hasRequestArg {
		requestArg = inv.Arg("request")
	}
	texts := append([]string(nil), req.extraTexts...)
	allowRequestAsText := req.extraTexts != nil
	var preMatchedRequest *leapmuxv1.AgentControlRequest
	got, err := s.resolveTab(resolve.KindAgent, agentArg, resolve.Need{WorkerID: true})
	if err != nil {
		if !hasRequestArg || allowRequestAsText {
			code := cli.FromError(err).Code
			if code == cli.CodeNotFound || code == cli.CodeUsage {
				matchedTarget, matchedReq, gerr := s.resolveGlobalRequestHandle(agentArg)
				if gerr != nil {
					if cli.FromError(gerr).Code == cli.CodeNotFound {
						return nil, err
					}
					return nil, gerr
				}
				got = matchedTarget
				preMatchedRequest = matchedReq
				if hasRequestArg && allowRequestAsText {
					// When the first arg was a global request handle on `agent answer`,
					// the second positional arg is the first answer text.
					texts = append([]string{requestArg}, texts...)
				}
				err = nil
			}
		}
		if err != nil {
			return nil, err
		}
	}
	if err := refuseSelfAnswer(got); err != nil {
		return nil, err
	}
	if err := maybePreflightWorker(s.ctx, s.c, got.WorkerID); err != nil {
		return nil, err
	}
	if preMatchedRequest != nil {
		if err := requireAnswerable(got.ID, preMatchedRequest); err != nil {
			return nil, err
		}
		resolvedTexts, err := readAnswerTexts(texts, req.stdin)
		if err != nil {
			return nil, err
		}
		decision, err := req.decide(preMatchedRequest.GetPrompt(), resolvedTexts)
		if err != nil {
			return nil, err
		}
		var answered leapmuxv1.AnswerControlResponse
		if err := callInnerRPC(s.ctx, s.c, got.WorkerID, leapmuxv1connect.WorkerAgentServiceAnswerControlProcedure, &leapmuxv1.AnswerControlRequest{
			AgentId: got.ID, RequestId: preMatchedRequest.GetRequestId(), ClaimToken: preMatchedRequest.GetClaimToken(), Decision: decision,
		}, &answered); err != nil {
			return nil, err
		}
		return controlAnswerResultOf(got.ID, preMatchedRequest.GetClaimToken(), &answered)
	}
	var result controlAnswerResult
	err = withWorkerChannel(s.ctx, s.c, got.WorkerID, func(w workerCall) error {
		var listed leapmuxv1.ListControlRequestsResponse
		if err := w.Call(s.ctx, leapmuxv1connect.WorkerAgentServiceListControlRequestsProcedure, &leapmuxv1.ListControlRequestsRequest{AgentId: got.ID}, &listed); err != nil {
			return err
		}
		var request *leapmuxv1.AgentControlRequest
		if !hasRequestArg {
			request, err = findSoleControlRequest(got.ID, listed.GetRequests())
		} else {
			request, err = findControlRequest(got.ID, listed.GetRequests(), requestArg)
			if err != nil && req.allowQuestionTextShift && cli.FromError(err).Code == cli.CodeNotFound && len(listed.GetRequests()) == 1 &&
				listed.GetRequests()[0].GetPrompt().GetQuestion() != nil {
				// Treat an unrecognized [request] arg on `agent answer` as the first
				// positional <text> when the agent has a single pending question and
				// no explicit --option, --field, or --text flag was given.
				texts = append([]string{requestArg}, texts...)
				request = listed.GetRequests()[0]
				err = nil
			}
		}
		if err != nil {
			return err
		}
		if err := requireAnswerable(got.ID, request); err != nil {
			return err
		}
		resolvedTexts, err := readAnswerTexts(texts, req.stdin)
		if err != nil {
			return err
		}
		decision, err := req.decide(request.GetPrompt(), resolvedTexts)
		if err != nil {
			return err
		}
		var answered leapmuxv1.AnswerControlResponse
		if err := w.Call(s.ctx, leapmuxv1connect.WorkerAgentServiceAnswerControlProcedure, &leapmuxv1.AnswerControlRequest{
			AgentId: got.ID, RequestId: request.GetRequestId(), ClaimToken: request.GetClaimToken(), Decision: decision,
		}, &answered); err != nil {
			return err
		}
		res, err := controlAnswerResultOf(got.ID, request.GetClaimToken(), &answered)
		result = res
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func resolveAnswerText(raw string, stdin *lazyStdin) (string, error) {
	if raw != "-" || stdin == nil {
		return raw, nil
	}
	return stdin.read()
}

// findSoleControlRequest picks the only pending request of agentID when the
// caller omitted [request], or fails with not_found / ambiguous_target.
func findSoleControlRequest(agentID string, requests []*leapmuxv1.AgentControlRequest) (*leapmuxv1.AgentControlRequest, error) {
	switch len(requests) {
	case 0:
		return nil, cli.Newf(cli.CodeNotFound, "agent %s has no pending request", agentID)
	case 1:
		return requests[0], nil
	default:
		e := cli.Newf(cli.CodeAmbiguousTarget, "agent %s has %d pending requests; specify a request handle", agentID, len(requests)).
			WithHint(`list its requests with "leapmux agent requests %s"`, agentID)
		for _, r := range requests {
			e.Candidates = append(e.Candidates, cli.Candidate{ID: r.GetClaimToken(), Kind: controlPromptKind(r.GetPrompt())})
		}
		return nil, e
	}
}

// resolveGlobalRequestHandle scans all agents (scoped by --workspace if set)
// for a single request whose claim token matches handle or starts with it,
// returning the owning tabTarget and matched request proto directly.
func (s *session) resolveGlobalRequestHandle(handle string) (tabTarget, *leapmuxv1.AgentControlRequest, error) {
	if strings.TrimSpace(handle) == "" {
		return tabTarget{}, nil, cli.Newf(cli.CodeNotFound, "empty request handle")
	}
	handle, err := normalizeIDHandle(handle, "request handle")
	if err != nil {
		return tabTarget{}, nil, err
	}
	targets, err := s.listWorkspaceAgentTargets()
	if err != nil {
		return tabTarget{}, nil, err
	}
	if len(targets) == 0 {
		return tabTarget{}, nil, cli.Newf(cli.CodeNotFound, "no pending request matches %q", handle)
	}
	listed, agentIDs := s.listAgentControlRequestProtos(targets)
	return matchGlobalRequestHandle(handle, targets, agentIDs, listed)
}

type globalRequestMatch struct {
	agentID string
	req     *leapmuxv1.AgentControlRequest
}

// matchGlobalRequestHandle selects the globally unique request matching handle
// from raw per-agent request listings. Exact full-ID matches succeed even when
// another worker failed to answer, while short prefix matches require every
// worker to answer so global uniqueness is guaranteed.
func matchGlobalRequestHandle(
	handle string,
	targets []tabTarget,
	agentIDs []string,
	listed map[string]listedRequestProtos,
) (tabTarget, *leapmuxv1.AgentControlRequest, error) {
	workerOf := make(map[string]string, len(targets))
	for _, t := range targets {
		workerOf[t.ID] = t.WorkerID
	}
	var exact []globalRequestMatch
	var prefix []globalRequestMatch
	var failedWorkers []string
	failedWorkerSeen := map[string]bool{}
	var firstErr error
	allowPrefix := len(handle) >= minHandlePrefixLength
	for _, agentID := range agentIDs {
		got := listed[agentID]
		if got.err != nil {
			if firstErr == nil {
				firstErr = got.err
			}
			wk := workerOf[agentID]
			if wk != "" && !failedWorkerSeen[wk] {
				failedWorkerSeen[wk] = true
				failedWorkers = append(failedWorkers, cli.ShortID(wk))
			}
			continue
		}
		for _, r := range got.requests {
			token := r.GetClaimToken()
			if token == handle {
				exact = append(exact, globalRequestMatch{agentID: agentID, req: r})
			} else if allowPrefix && strings.HasPrefix(token, handle) {
				prefix = append(prefix, globalRequestMatch{agentID: agentID, req: r})
			}
		}
	}
	if len(exact) == 1 {
		m := exact[0]
		return tabTarget{ID: m.agentID, Type: leapmuxv1.TabType_TAB_TYPE_AGENT, WorkerID: workerOf[m.agentID]}, m.req, nil
	}
	if len(exact) > 1 {
		return tabTarget{}, nil, ambiguousGlobalRequestError(handle, exact)
	}
	switch len(prefix) {
	case 0:
		if firstErr != nil {
			return tabTarget{}, nil, firstErr
		}
		return tabTarget{}, nil, cli.Newf(cli.CodeNotFound, "no pending request matches %q", handle)
	case 1:
		if firstErr != nil {
			detail := cli.FromError(firstErr).Message
			if len(failedWorkers) > 0 {
				return tabTarget{}, nil, cli.Newf(cli.CodeUnavailable,
					"cannot confirm that prefix %q is globally unique because worker %s could not be queried: %s",
					handle, strings.Join(failedWorkers, ", "), detail).
					WithHint(`specify both <agent> and <request>, or pass the full request handle`)
			}
			return tabTarget{}, nil, cli.Newf(cli.CodeUnavailable,
				"cannot confirm that prefix %q is globally unique because another agent could not be queried: %s",
				handle, detail).
				WithHint(`specify both <agent> and <request>, or pass the full request handle`)
		}
		m := prefix[0]
		return tabTarget{ID: m.agentID, Type: leapmuxv1.TabType_TAB_TYPE_AGENT, WorkerID: workerOf[m.agentID]}, m.req, nil
	default:
		return tabTarget{}, nil, ambiguousGlobalRequestError(handle, prefix)
	}
}

func ambiguousGlobalRequestError(handle string, matches []globalRequestMatch) error {
	e := cli.Newf(cli.CodeAmbiguousTarget, "%q matches %d pending requests across agents", handle, len(matches)).
		WithHint(`specify both <agent> and <request>, or list requests with "leapmux agent requests"`)
	for _, m := range matches {
		e.Candidates = append(e.Candidates, cli.Candidate{
			ID:    m.req.GetClaimToken(),
			Kind:  controlPromptKind(m.req.GetPrompt()),
			Title: "agent " + cli.ShortID(m.agentID),
		})
	}
	return e
}

// refuseSelfAnswer refuses an answer to a request of the calling agent. The
// user answers an agent's own prompts: an agent that approved its own
// permission prompt would remove the reason for the prompt.
func refuseSelfAnswer(t tabTarget) error {
	if !t.Self && t.ID != callingTabID() {
		return nil
	}
	return cli.Usagef("agent %s is the calling agent, which cannot answer its own requests", t.ID).
		WithHint("the user answers them, in LeapMux or from another terminal")
}

// findControlRequest returns the request of agentID whose handle is handle,
// or the one request whose handle starts with it.
func findControlRequest(agentID string, requests []*leapmuxv1.AgentControlRequest, handle string) (*leapmuxv1.AgentControlRequest, error) {
	handle, err := normalizeIDHandle(handle, "request handle")
	if err != nil {
		return nil, err
	}
	request, matches, found := lookupByIDPrefix(requests, handle, (*leapmuxv1.AgentControlRequest).GetClaimToken)
	if found {
		return request, nil
	}
	if len(matches) == 0 {
		return nil, cli.Newf(cli.CodeNotFound, "agent %s has no pending request %s", agentID, handle).
			WithHint("list its requests with \"leapmux agent requests %s\"", agentID)
	}
	e := cli.Newf(cli.CodeAmbiguousTarget, "%q matches %d requests of agent %s", handle, len(matches), agentID).
		WithHint("give more characters of the handle")
	for _, r := range matches {
		e.Candidates = append(e.Candidates, cli.Candidate{ID: r.GetClaimToken(), Kind: controlPromptKind(r.GetPrompt())})
	}
	return nil, e
}

// requireAnswerable refuses a request that takes no answer now: one that
// LeapMux cannot read, and one whose answer the Worker already holds.
func requireAnswerable(agentID string, r *leapmuxv1.AgentControlRequest) error {
	if fault := r.GetPrompt().GetUnreadable(); fault != leapmuxv1.ControlPayloadFault_CONTROL_PAYLOAD_FAULT_UNSPECIFIED {
		return cli.Newf(cli.CodeConflict, "LeapMux cannot read request %s (%s), so it takes no answer", r.GetClaimToken(), enumToken(fault)).
			WithHint("stop the agent's turn with \"leapmux agent interrupt %s\"", agentID)
	}
	if kind := controlPromptKind(r.GetPrompt()); kind == controlKindUnknown {
		return cli.Newf(cli.CodeVersionMismatch, "request %s asks something that this CLI cannot answer", r.GetClaimToken()).
			WithHint("use a leapmux CLI of the Worker's version")
	}
	if state := r.GetResponseState(); state != leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_READY {
		return cli.Newf(cli.CodeConflict, "request %s already has an answer (%s)", r.GetClaimToken(), controlResponseStateToken(state))
	}
	return nil
}

// controlAnswerResultOf reads the Worker's response to an answer.
//
//   - A canceled instance is a conflict, with or without the Worker's error
//     text: the agent replaced or withdrew the request, or another client
//     answered it, and the Worker holds no answer of this client. A replaced
//     request waits under a new handle, so the hint lists the requests again.
//   - A failure that left the request ready (unsent) can succeed on a retry.
//   - Only COMPLETED, DELIVERED, and PENDING with no error text confirm that
//     the Worker accepted the answer; UNCERTAIN, UNSPECIFIED, or any response
//     with error text fails without marking the answer retryable because the
//     Worker never sends a second answer.
func controlAnswerResultOf(agentID, handle string, r *leapmuxv1.AnswerControlResponse) (controlAnswerResult, error) {
	state := r.GetState()
	errText := r.GetError()
	result := controlAnswerResult{ID: handle, AgentID: agentID, State: controlResponseStateToken(state)}
	if state == leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_CANCELED {
		return result, cli.Newf(cli.CodeConflict, "request %s is no longer pending: the agent replaced or withdrew it, or another client answered it", handle).
			WithHint("list its requests again with \"leapmux agent requests %s\": a request that the agent replaced has a new handle", agentID)
	}
	if state == leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_READY {
		if errText != "" {
			return result, cli.Newf(cli.CodeUnavailable, "the answer to request %s was not sent: %s", handle, errText)
		}
		return result, cli.Newf(cli.CodeUnavailable, "the answer to request %s was not sent (request is still ready)", handle)
	}
	if errText != "" {
		return result, cli.Newf(cli.CodeInternal, "the Worker holds the answer to request %s, but its delivery did not finish (%s): %s", handle, result.State, errText).
			WithHint("check it with \"leapmux agent requests %s\"", agentID)
	}
	switch state {
	case leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED,
		leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_DELIVERED,
		leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_PENDING:
		return result, nil
	case leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_UNCERTAIN:
		return result, cli.Newf(cli.CodeInternal, "the Worker holds the answer to request %s, but its delivery is uncertain", handle).
			WithHint("check it with \"leapmux agent requests %s\"", agentID)
	default:
		return result, cli.Newf(cli.CodeInternal, "the Worker returned an unconfirmed state (%s) for request %s", result.State, handle).
			WithHint("check it with \"leapmux agent requests %s\"", agentID)
	}
}

// requestArgHelp describes the <request> argument.
const requestArgHelp = `The request's handle, as "agent requests" prints it, or a unique prefix of it.`

// checkAtMostOneStdinText refuses command lines that pass "-" more than once.
func checkAtMostOneStdinText(rawTexts []string) error {
	dashes := 0
	for _, text := range rawTexts {
		if text == "-" {
			dashes++
		}
	}
	if dashes > 1 {
		return cli.Usagef(`only one text can be "-"`)
	}
	return nil
}

// readAnswerTexts resolves answer texts into a fresh slice without mutating
// the caller's slice. A text "-" reads stdin lazily at most once via stdin.
func readAnswerTexts(rawTexts []string, stdin *lazyStdin) ([]string, error) {
	if len(rawTexts) == 0 {
		return nil, nil
	}
	if err := checkAtMostOneStdinText(rawTexts); err != nil {
		return nil, err
	}
	texts := make([]string, len(rawTexts))
	for i, text := range rawTexts {
		resolved, err := resolveAnswerText(text, stdin)
		if err != nil {
			return nil, err
		}
		texts[i] = resolved
	}
	return texts, nil
}

func runAgentApprove(inv *cli.Invocation) (any, error) {
	settings := planSettingsOf(inv)
	return runControlAnswer(inv, controlAnswerRequest{
		decide: func(prompt *leapmuxv1.ControlPrompt, _ []string) (*leapmuxv1.ControlDecision, error) {
			return approveDecision(prompt, settings)
		},
	})
}

func runAgentDeny(inv *cli.Invocation) (any, error) {
	message := stringFlag(inv, "message")
	return runControlAnswer(inv, controlAnswerRequest{
		decide: func(prompt *leapmuxv1.ControlPrompt, _ []string) (*leapmuxv1.ControlDecision, error) {
			return denyDecision(prompt, message)
		},
	})
}

func runAgentAnswer(inv *cli.Invocation) (any, error) {
	posTexts := inv.Variadic("text")
	flagTexts := inv.StringArray("text")
	rawTexts := make([]string, 0, len(posTexts)+len(flagTexts))
	rawTexts = append(rawTexts, posTexts...)
	rawTexts = append(rawTexts, flagTexts...)
	if err := checkAtMostOneStdinText(rawTexts); err != nil {
		return nil, err
	}
	stdin := &lazyStdin{r: inv.Stdin}
	options := inv.StringArray("option")
	fields := inv.StringArray("field")
	allowQuestionTextShift := len(options) == 0 && len(fields) == 0 && len(flagTexts) == 0
	// answerDecision refuses a command line that gives no answer, except for
	// questions that allow an empty one, which only the prompt can tell.
	base := answerSpec{options: options, fields: fields, message: stringFlag(inv, "message"), settings: planSettingsOf(inv)}
	return runControlAnswer(inv, controlAnswerRequest{
		extraTexts:             rawTexts,
		allowQuestionTextShift: allowQuestionTextShift,
		stdin:                  stdin,
		decide: func(prompt *leapmuxv1.ControlPrompt, finalTexts []string) (*leapmuxv1.ControlDecision, error) {
			spec := base
			spec.texts = finalTexts
			return answerDecision(prompt, spec)
		},
	})
}
