package codewhale

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Codewhale's REST transport: one typed wrapper for each runtime route the
// provider calls. Every wrapper takes its deadline from the agent's API
// timeout, except where the caller states its own.

// HTTP statuses the provider reads.
const (
	httpStatusBadRequest = http.StatusBadRequest
	httpStatusNotFound   = http.StatusNotFound
	httpStatusConflict   = http.StatusConflict
)

// threadRecord is the runtime's ThreadRecord: the persisted state of one
// thread. The store keeps the same shape at `runtime/threads/<id>.json`, which
// sessions.go reads.
type threadRecord struct {
	ID                string `json:"id"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
	Model             string `json:"model"`
	ModelProvider     string `json:"model_provider"`
	ModelProviderID   string `json:"model_provider_id"`
	Workspace         string `json:"workspace"`
	Mode              string `json:"mode"`
	PermissionPosture string `json:"permission_posture"`
	LatestTurnID      string `json:"latest_turn_id"`
	Archived          bool   `json:"archived"`
	Title             string `json:"title"`
}

// turnRecord is the part of the runtime's TurnRecord that the provider reads.
type turnRecord struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	InputSummary string `json:"input_summary"`
}

// createThreadRequest opens a thread. `allow_shell` is always sent: the
// runtime's default comes from the user's configuration, which disables the
// shell unless the user enabled it, and without the shell the agent has no
// `bash` tool at all. The permission posture still controls each command.
type createThreadRequest struct {
	Workspace         string `json:"workspace"`
	Model             string `json:"model,omitempty"`
	ReasoningEffort   string `json:"reasoning_effort,omitempty"`
	Mode              string `json:"mode,omitempty"`
	PermissionPosture string `json:"permission_posture,omitempty"`
	AllowShell        bool   `json:"allow_shell"`
}

// updateThreadRequest changes the persistent state of a thread. A nil field
// means "no change".
type updateThreadRequest struct {
	Model             *string `json:"model,omitempty"`
	Mode              *string `json:"mode,omitempty"`
	PermissionPosture *string `json:"permission_posture,omitempty"`
}

// startTurnRequest starts one turn. The runtime takes the effort per turn only:
// its thread update has no effort field.
type startTurnRequest struct {
	Prompt          string      `json:"prompt"`
	ReasoningEffort string      `json:"reasoning_effort,omitempty"`
	Images          []turnImage `json:"images,omitempty"`
}

// turnImage is one inline image of a turn.
type turnImage struct {
	Mime       string `json:"mime"`
	DataBase64 string `json:"dataBase64"`
}

// turnStartResponse is the reply to a turn start and to a compaction.
type turnStartResponse struct {
	Turn turnRecord `json:"turn"`
}

// threadDetail is the reply to GET /v1/threads/{id}: the snapshot a client
// reads before it subscribes. latest_seq is where the event stream resumes.
type threadDetail struct {
	Thread            threadRecord       `json:"thread"`
	Turns             []turnRecord       `json:"turns"`
	LatestSeq         uint64             `json:"latest_seq"`
	PendingApprovals  []pendingApproval  `json:"pending_approvals"`
	PendingUserInputs []pendingUserInput `json:"pending_user_inputs"`
}

// pendingApproval is one approval the snapshot states as waiting.
type pendingApproval struct {
	ID            string `json:"id"`
	TurnID        string `json:"turn_id"`
	ToolName      string `json:"tool_name"`
	Description   string `json:"description"`
	IntentSummary string `json:"intent_summary"`
	ToolCallID    string `json:"tool_call_id"`
}

// pendingUserInput is one question the snapshot states as waiting.
type pendingUserInput struct {
	ID      string          `json:"id"`
	TurnID  string          `json:"turn_id"`
	Request json.RawMessage `json:"request"`
}

// codewhaleRuntimeInfo is the part of GET /v1/runtime/info that the provider
// reads.
type codewhaleRuntimeInfo struct {
	CodewhaleVersion string `json:"codewhale_version"`
	// hasContextRoute is not on the wire. start.go sets it when the thread's
	// context route answers, which it does from 0.10.0.
	hasContextRoute bool
}

// providerModelsPage is one page of GET /v1/providers/{id}/models.
type providerModelsPage struct {
	Models     []providerModel `json:"models"`
	NextCursor string          `json:"nextCursor"`
}

// providerModel is one model of a provider's catalog.
type providerModel struct {
	ID                    string   `json:"id"`
	ImageInput            string   `json:"image_input"`
	ReasoningEffort       string   `json:"reasoning_effort"`
	ReasoningEffortLevels []string `json:"reasoning_effort_levels"`
}

// providerModelsPageLimit is the largest page the route serves.
const providerModelsPageLimit = 250

// providerModelsMaxPages sets the maximum number of pages that the catalog walk
// reads. The route itself refuses a catalog of more than 10000 models, which is
// 40 pages.
const providerModelsMaxPages = 40

// threadPath is the route of one thread, with its sub-route appended.
func threadPath(threadID, sub string) string {
	return routeThreads + "/" + url.PathEscape(threadID) + sub
}

// shellJobRoute is the route of one background shell job of a thread.
func shellJobRoute(threadID, jobID string) string {
	return threadPath(threadID, threadRouteJobs) + "/" + url.PathEscape(jobID)
}

// turnPath is the route of one turn, with its sub-route appended.
func turnPath(threadID, turnID, sub string) string {
	return threadPath(threadID, threadRouteTurns) + "/" + url.PathEscape(turnID) + sub
}

// call runs one request under the agent's API timeout.
func (a *Agent) call(method, path string, query url.Values, body, out any) error {
	return a.callWithin(a.APITimeout(), method, path, query, body, out)
}

// callWithin runs one request under an explicit timeout. The request also ends
// when the process context ends, so a request cannot outlive the agent.
func (a *Agent) callWithin(timeout time.Duration, method, path string, query url.Values, body, out any) error {
	ctx := a.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	return a.callIn(ctx, timeout, method, path, query, body, out)
}

// callIn runs one request under the agent's API timeout that also ends with
// ctx. A watcher passes its own context: Stop cancels the watchers and then
// waits for them, and a request under the process context would hold that wait
// for the whole timeout, because the process context ends only after it.
func (a *Agent) callIn(ctx context.Context, timeout time.Duration, method, path string, query url.Values, body, out any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return a.endpoint.DoQuery(ctx, method, path, query, body, out)
}

func (a *Agent) createThread(request createThreadRequest) (threadRecord, error) {
	var thread threadRecord
	err := a.call(http.MethodPost, routeThreads, nil, request, &thread)
	return thread, err
}

func (a *Agent) resumeThread(threadID string) (threadRecord, error) {
	var thread threadRecord
	err := a.call(http.MethodPost, threadPath(threadID, threadRouteResume), nil, struct{}{}, &thread)
	return thread, err
}

func (a *Agent) readThread(threadID string) (threadDetail, error) {
	var detail threadDetail
	err := a.call(http.MethodGet, threadPath(threadID, ""), nil, nil, &detail)
	return detail, err
}

func (a *Agent) updateThread(threadID string, request updateThreadRequest) (threadRecord, error) {
	var thread threadRecord
	err := a.call(http.MethodPatch, threadPath(threadID, ""), nil, request, &thread)
	return thread, err
}

func (a *Agent) startTurn(threadID string, request startTurnRequest) (turnRecord, error) {
	var response turnStartResponse
	err := a.call(http.MethodPost, threadPath(threadID, threadRouteTurns), nil, request, &response)
	return response.Turn, err
}

func (a *Agent) steerTurn(threadID, turnID, prompt string) error {
	return a.call(http.MethodPost, turnPath(threadID, turnID, turnRouteSteer), nil, struct {
		Prompt string `json:"prompt"`
	}{Prompt: prompt}, nil)
}

func (a *Agent) interruptTurn(threadID, turnID string, timeout time.Duration) error {
	return a.callWithin(timeout, http.MethodPost, turnPath(threadID, turnID, turnRouteInterrupt), nil, struct{}{}, nil)
}

func (a *Agent) compactThread(threadID string) (turnRecord, error) {
	var response turnStartResponse
	err := a.call(http.MethodPost, threadPath(threadID, threadRouteCompact), nil, struct{}{}, &response)
	return response.Turn, err
}

func (a *Agent) putGoal(threadID, objective string) error {
	return a.call(http.MethodPut, threadPath(threadID, threadRouteGoal), nil, struct {
		Objective string `json:"objective"`
	}{Objective: objective}, nil)
}

func (a *Agent) deleteGoal(threadID string) error {
	return a.call(http.MethodDelete, threadPath(threadID, threadRouteGoal), nil, nil, nil)
}

func (a *Agent) postApproval(approvalID string, body any) error {
	return a.call(http.MethodPost, routeApprovals+"/"+url.PathEscape(approvalID), nil, body, nil)
}

func (a *Agent) postUserInput(threadID, inputID string, body any) error {
	return a.call(http.MethodPost, routeUserInput+"/"+url.PathEscape(threadID)+"/"+url.PathEscape(inputID), nil, body, nil)
}

// readThreadContext reads the thread's context report, which exists from
// 0.10.0. The reply is raw, because usage.go reads one field from it.
func (a *Agent) readThreadContext(threadID string) (json.RawMessage, error) {
	var raw json.RawMessage
	err := a.call(http.MethodGet, threadPath(threadID, threadRouteContext), nil, nil, &raw)
	return raw, err
}

// readAgentRun reads one subagent's record. ctx is the watcher's.
func (a *Agent) readAgentRun(ctx context.Context, runID string, out any) error {
	return a.callIn(ctx, a.APITimeout(), http.MethodGet, routeAgentRuns+"/"+url.PathEscape(runID), nil, nil, out)
}

// listThreadJobs lists the thread's background shell jobs. The route exists
// from 0.10.0, and 0.9.13 answers 404. The poller lists only to learn which of
// the two it talks to: the list drops old finished jobs as it answers, so it
// reads each job by readThreadJob instead. ctx is the poller's.
func (a *Agent) listThreadJobs(ctx context.Context, threadID string) error {
	var response struct {
		Jobs []shellJob `json:"jobs"`
	}
	return a.callIn(ctx, a.APITimeout(), http.MethodGet, threadPath(threadID, threadRouteJobs), nil, nil, &response)
}

// readThreadJob reads one background shell job of the thread. Unlike the list,
// the read drops no finished job. The route exists from 0.10.0. ctx is the
// poller's.
func (a *Agent) readThreadJob(ctx context.Context, threadID, jobID string) (shellJob, error) {
	var response struct {
		Job shellJob `json:"job"`
	}
	err := a.callIn(ctx, a.APITimeout(), http.MethodGet, shellJobRoute(threadID, jobID), nil, nil, &response)
	return response.Job, err
}

// listProviderModels reads a provider's whole catalog, one page at a time.
// providerID narrows a named custom route; it may be empty.
func (a *Agent) listProviderModels(provider, providerID string) ([]providerModel, error) {
	var models []providerModel
	cursor := ""
	for page := 0; page < providerModelsMaxPages; page++ {
		query := url.Values{"limit": {strconv.Itoa(providerModelsPageLimit)}}
		if providerID != "" && providerID != provider {
			query.Set("model_provider_id", providerID)
		}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		var result providerModelsPage
		if err := a.call(http.MethodGet, routeProviders+"/"+url.PathEscape(provider)+providerRouteModels, query, nil, &result); err != nil {
			return nil, err
		}
		models = append(models, result.Models...)
		if result.NextCursor == "" {
			return models, nil
		}
		cursor = result.NextCursor
	}
	return models, nil
}
