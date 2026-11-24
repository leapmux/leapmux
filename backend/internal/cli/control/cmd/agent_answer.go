package cmd

import (
	"strings"

	"google.golang.org/protobuf/proto"

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

// controlDecider builds the decision of one command for prompt.
type controlDecider func(prompt *leapmuxv1.ControlPrompt) (*leapmuxv1.ControlDecision, error)

// runControlAnswer answers the request that the arguments give with the
// decision that decide builds. It reuses one Noise_NK channel across
// ListControlRequests and AnswerControl.
//
// When [request] is omitted:
//   - If <agent> resolves to an agent with exactly one pending request, that
//     request is selected automatically (2+ fails with ambiguous_target).
//   - When allowGlobalHandle is true and <agent> does not resolve to an agent,
//     it searches pending requests across all agents for a globally unique
//     handle or prefix.
func runControlAnswer(inv *cli.Invocation, allowGlobalHandle bool, decide controlDecider) (any, error) {
	return runControlAnswerWithTextFallback(inv, allowGlobalHandle, nil, false, false, nil, func(prompt *leapmuxv1.ControlPrompt, _ []string) (*leapmuxv1.ControlDecision, error) {
		return decide(prompt)
	})
}

func runControlAnswerWithTextFallback(
	inv *cli.Invocation,
	allowGlobalHandle bool,
	extraTexts []string,
	stdinAlreadyUsed bool,
	allowQuestionTextShift bool,
	preDrainedRequestText *string,
	decide func(prompt *leapmuxv1.ControlPrompt, texts []string) (*leapmuxv1.ControlDecision, error),
) (any, error) {
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
	texts := append([]string(nil), extraTexts...)
	allowRequestAsText := extraTexts != nil
	got, err := s.resolveTab(resolve.KindAgent, agentArg, resolve.Need{WorkerID: true})
	if err != nil {
		if allowGlobalHandle && (!hasRequestArg || allowRequestAsText) {
			code := cli.FromError(err).Code
			if code == cli.CodeNotFound || code == cli.CodeUsage {
				matchedTarget, matchedHandle, gerr := s.resolveGlobalRequestHandle(agentArg)
				if gerr != nil {
					if cli.FromError(gerr).Code == cli.CodeNotFound {
						return nil, err
					}
					return nil, gerr
				}
				got = matchedTarget
				if hasRequestArg && allowRequestAsText {
					// When the first arg was a global request handle on `agent answer`,
					// the second positional arg is the first answer text.
					shifted, serr := resolveShiftedAnswerText(requestArg, preDrainedRequestText, stdinAlreadyUsed)
					if serr != nil {
						return nil, serr
					}
					texts = append([]string{shifted}, texts...)
				}
				requestArg = matchedHandle
				hasRequestArg = true
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
			if err != nil && allowQuestionTextShift && cli.FromError(err).Code == cli.CodeNotFound && len(listed.GetRequests()) == 1 &&
				listed.GetRequests()[0].GetPrompt().GetQuestion() != nil {
				// Treat an unrecognized [request] arg on `agent answer` as the first
				// positional <text> when the agent has a single pending question and
				// no explicit --option/--field flags were given.
				shifted, serr := resolveShiftedAnswerText(requestArg, preDrainedRequestText, stdinAlreadyUsed)
				if serr != nil {
					return serr
				}
				texts = append([]string{shifted}, texts...)
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
		decision, err := decide(request.GetPrompt(), texts)
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

func resolveShiftedAnswerText(raw string, preDrained *string, stdinAlreadyUsed bool) (string, error) {
	if raw != "-" {
		return raw, nil
	}
	if stdinAlreadyUsed {
		return "", cli.Usagef(`only one text can be "-"`)
	}
	if preDrained != nil {
		return *preDrained, nil
	}
	return raw, nil
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
// for a single request whose claim token matches handle or starts with it.
func (s *session) resolveGlobalRequestHandle(handle string) (tabTarget, string, error) {
	handle = strings.TrimSpace(handle)
	if handle == "" {
		return tabTarget{}, "", cli.Newf(cli.CodeNotFound, "empty request handle")
	}
	tabs, err := s.listWorkspaceTabs(leapmuxv1.TabType_TAB_TYPE_AGENT)
	if err != nil {
		return tabTarget{}, "", err
	}
	var targets []tabTarget
	workerOf := map[string]string{}
	for _, t := range tabs {
		if t.GetWorkerId() != "" {
			id := t.GetTab().GetTabId()
			workerOf[id] = t.GetWorkerId()
			targets = append(targets, tabTarget{ID: id, Type: leapmuxv1.TabType_TAB_TYPE_AGENT, WorkerID: t.GetWorkerId()})
		}
	}
	if len(targets) == 0 {
		return tabTarget{}, "", cli.Newf(cli.CodeNotFound, "no pending request matches %q", handle)
	}
	groups, agentIDs := groupAgentsByWorker(targets)
	listed := listControlRequests(s.ctx, func(workerID string, body func(call workerCaller) error) error {
		return withWorkerChannel(s.ctx, s.c, workerID, func(w workerCall) error {
			return body(func(method string, in, out proto.Message) error { return w.Call(s.ctx, method, in, out) })
		})
	}, groups)
	type match struct {
		agentID string
		row     controlRequestRow
	}
	var exact []match
	var prefix []match
	allowPrefix := len(handle) >= minHandlePrefixLength
	for _, agentID := range agentIDs {
		got := listed[agentID]
		if got.err != nil {
			continue
		}
		for _, r := range got.rows {
			if r.ID == handle {
				exact = append(exact, match{agentID: agentID, row: r})
			} else if allowPrefix && strings.HasPrefix(r.ID, handle) {
				prefix = append(prefix, match{agentID: agentID, row: r})
			}
		}
	}
	matches := prefix
	if len(exact) > 0 {
		matches = exact
	}
	switch len(matches) {
	case 0:
		return tabTarget{}, "", cli.Newf(cli.CodeNotFound, "no pending request matches %q", handle)
	case 1:
		m := matches[0]
		return tabTarget{ID: m.agentID, Type: leapmuxv1.TabType_TAB_TYPE_AGENT, WorkerID: workerOf[m.agentID]}, m.row.ID, nil
	default:
		e := cli.Newf(cli.CodeAmbiguousTarget, "%q matches %d pending requests across agents", handle, len(matches)).
			WithHint(`specify both <agent> and <request>, or list requests with "leapmux agent requests"`)
		for _, m := range matches {
			e.Candidates = append(e.Candidates, cli.Candidate{
				ID:    m.row.ID,
				Kind:  m.row.Kind,
				Title: "agent " + cli.ShortID(m.agentID),
			})
		}
		return tabTarget{}, "", e
	}
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
	handle = strings.TrimSpace(handle)
	if rest, ok := strings.CutPrefix(handle, "id:"); ok {
		handle = strings.TrimSpace(rest)
		if handle == "" {
			return nil, cli.Usagef(`"id:" needs a request handle after the colon`)
		}
	}
	if handle == "" {
		return nil, cli.Usagef("the request handle is empty")
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
//   - A failure that left the answer unsent can succeed on a retry.
//   - A failure after the Worker saved the answer cannot, because the Worker
//     never sends a second answer.
func controlAnswerResultOf(agentID, handle string, r *leapmuxv1.AnswerControlResponse) (controlAnswerResult, error) {
	result := controlAnswerResult{ID: handle, AgentID: agentID, State: controlResponseStateToken(r.GetState())}
	switch {
	case r.GetState() == leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_CANCELED:
		return result, cli.Newf(cli.CodeConflict, "request %s is no longer pending: the agent replaced or withdrew it, or another client answered it", handle).
			WithHint("list its requests again with \"leapmux agent requests %s\": a request that the agent replaced has a new handle", agentID)
	case r.GetError() != "" && r.GetState() == leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_READY:
		return result, cli.Newf(cli.CodeUnavailable, "the answer to request %s was not sent: %s", handle, r.GetError())
	case r.GetError() != "":
		return result, cli.Newf(cli.CodeInternal, "the Worker holds the answer to request %s, but its delivery did not finish (%s): %s", handle, result.State, r.GetError()).
			WithHint("check it with \"leapmux agent requests %s\"", agentID)
	}
	return result, nil
}

// requestArgHelp describes the <request> argument.
const requestArgHelp = `The request's handle, as "agent requests" prints it, or a unique prefix of it.`

// answerTexts reads the positional texts of `agent answer` into a fresh slice
// without mutating inv.Args. A text "-" reads standard input once, before any
// Hub or Worker connection is opened.
func answerTexts(inv *cli.Invocation) (texts []string, stdinRead bool, preDrainedRequest *string, err error) {
	rawTexts := inv.Variadic("text")
	texts = make([]string, len(rawTexts))
	copy(texts, rawTexts)
	for i, text := range texts {
		if text != "-" {
			continue
		}
		if stdinRead {
			return nil, false, nil, cli.Usagef(`only one text can be "-"`)
		}
		data, err := readStdinGuarded(inv.Stdin)
		if err != nil {
			return nil, false, nil, err
		}
		texts[i] = string(data)
		stdinRead = true
	}
	if inv.HasArg("request") && inv.Arg("request") == "-" {
		if stdinRead {
			return nil, false, nil, cli.Usagef(`only one text can be "-"`)
		}
		data, err := readStdinGuarded(inv.Stdin)
		if err != nil {
			return nil, false, nil, err
		}
		val := string(data)
		preDrainedRequest = &val
	}
	return texts, stdinRead, preDrainedRequest, nil
}

func runAgentApprove(inv *cli.Invocation) (any, error) {
	settings := planSettingsOf(inv)
	return runControlAnswer(inv, true, func(prompt *leapmuxv1.ControlPrompt) (*leapmuxv1.ControlDecision, error) {
		return approveDecision(prompt, settings)
	})
}

func runAgentDeny(inv *cli.Invocation) (any, error) {
	message := stringFlag(inv, "message")
	return runControlAnswer(inv, true, func(prompt *leapmuxv1.ControlPrompt) (*leapmuxv1.ControlDecision, error) {
		return denyDecision(prompt, message)
	})
}

func runAgentAnswer(inv *cli.Invocation) (any, error) {
	texts, stdinUsed, preDrainedRequest, err := answerTexts(inv)
	if err != nil {
		return nil, err
	}
	if texts == nil {
		texts = []string{}
	}
	options := inv.StringArray("option")
	fields := inv.StringArray("field")
	allowQuestionTextShift := len(options) == 0 && len(fields) == 0
	// answerDecision refuses a command line that gives no answer, except for
	// questions that allow an empty one, which only the prompt can tell.
	base := answerSpec{options: options, fields: fields, message: stringFlag(inv, "message"), settings: planSettingsOf(inv)}
	return runControlAnswerWithTextFallback(inv, true, texts, stdinUsed, allowQuestionTextShift, preDrainedRequest, func(prompt *leapmuxv1.ControlPrompt, finalTexts []string) (*leapmuxv1.ControlDecision, error) {
		spec := base
		spec.texts = finalTexts
		return answerDecision(prompt, spec)
	})
}
