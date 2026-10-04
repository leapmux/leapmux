package mimo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The REST routes of `mimo serve` that this provider calls. The browser never
// calls the server, so the routes stay in Go and out of the protocol contract.
const (
	routeHealth          = "/global/health"
	routeEvents          = "/event"
	routeSessions        = "/session"
	routeSessionStatus   = "/session/status"
	routeConfig          = "/config"
	routeConfigProviders = "/config/providers"
	routeAgents          = "/agent"
	routePermissions     = "/permission"
	routeSkipAll         = "/permission/skip-all"
	routeAutoApproveDel  = "/permission/auto-approve-delete"
	routeQuestions       = "/question"
	routeBashInteractive = "/bash-interactive"
)

// directoryHeader states the directory a request belongs to. The server keeps
// one instance for each directory, and the event stream of one instance carries
// only that instance's sessions, so every request names the agent's working
// directory rather than relying on the server's own working directory. A login
// shell profile that changes the directory would otherwise move every session
// to the wrong project.
const directoryHeader = "x-mimocode-directory"

// serverUser is the Basic-auth user name. MiMo reads it from
// MIMOCODE_SERVER_USERNAME, and the worker pins that variable to this value.
const serverUser = "mimocode"

// mimoSession is the session record that the create and get routes return.
type mimoSession struct {
	ID        string `json:"id"`
	ParentID  string `json:"parentID"`
	Directory string `json:"directory"`
	Title     string `json:"title"`
}

// mimoModelRef selects a model in a prompt or a compaction.
type mimoModelRef struct {
	ProviderID string `json:"providerID"`
	ModelID    string `json:"modelID"`
}

// mimoPromptPart is one part of a prompt: a text part or a file part.
type mimoPromptPart struct {
	Type     string
	Text     string
	URL      string
	Mime     string
	Filename string
}

// MarshalJSON writes the fields of the part's own type and no other. A text
// part states its text even when it is empty, because the server requires the
// field, and a file part carries no text field at all.
func (p mimoPromptPart) MarshalJSON() ([]byte, error) {
	if p.Type == promptPartFile {
		return json.Marshal(struct {
			Type     string `json:"type"`
			URL      string `json:"url"`
			Mime     string `json:"mime"`
			Filename string `json:"filename,omitempty"`
		}{p.Type, p.URL, p.Mime, p.Filename})
	}
	return json.Marshal(struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{p.Type, p.Text})
}

// Prompt part types. The server reads these on the way in, and the browser
// never sees them, so they stay in Go.
const (
	promptPartText = "text"
	promptPartFile = "file"
)

// mimoPromptRequest is the body of POST /session/:id/prompt_async.
//
// The client states the agent, the model and the variant on EVERY prompt: the
// server keeps no current selection for a session, and a prompt that omits one
// falls back to the configuration's default rather than to the previous prompt's
// choice. AgentID addresses a subagent; empty addresses the main agent.
type mimoPromptRequest struct {
	Parts   []mimoPromptPart `json:"parts"`
	Agent   string           `json:"agent,omitempty"`
	AgentID string           `json:"agentID,omitempty"`
	Model   *mimoModelRef    `json:"model,omitempty"`
	Variant string           `json:"variant,omitempty"`
}

// mimoCommandRequest is the body of POST /session/:id/command.
type mimoCommandRequest struct {
	Command   string `json:"command"`
	Arguments string `json:"arguments"`
	Agent     string `json:"agent,omitempty"`
	Model     string `json:"model,omitempty"`
	Variant   string `json:"variant,omitempty"`
}

// mimoPermissionReplyBody is the body of POST /permission/:id/reply.
type mimoPermissionReplyBody struct {
	Reply   string `json:"reply"`
	Message string `json:"message,omitempty"`
}

// mimoQuestionReplyBody is the body of POST /question/:id/reply.
type mimoQuestionReplyBody struct {
	Answers [][]string `json:"answers"`
}

// mimoBashReplyBody is the body of POST /bash-interactive/:id/reply.
type mimoBashReplyBody struct {
	Output   string `json:"output"`
	ExitCode int    `json:"exitCode"`
}

// mimoSwitchBody is the body of a runtime switch route.
type mimoSwitchBody struct {
	Enabled bool `json:"enabled"`
}

// mimoHealth is the reply of GET /global/health.
type mimoHealth struct {
	Healthy bool   `json:"healthy"`
	Version string `json:"version"`
}

// mimoRPC calls the REST routes of one server.
//
// Each call takes its own deadline from timeout, because the endpoint's client
// has none: the event stream shares it and stays open for the whole session.
type mimoRPC struct {
	endpoint *providerkit.HTTPEndpoint
	timeout  time.Duration
}

func (r mimoRPC) do(ctx context.Context, method, path string, body, out any) error {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	return r.endpoint.Do(ctx, method, path, body, out)
}

// idPattern matches an id that is safe as one path segment as it stands: every
// id the server issues does (`ses_…`, `msg_…`, `per_…`, `que_…`), and so does
// every generated id of MiMo's own ID scheme.
//
// An id is checked rather than escaped. HTTPEndpoint escapes the path that it
// builds, so an escape here would be escaped a second time, and an id with a
// slash or a dot segment would address another route.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// pathSegment returns id as one path segment, or an error that states kind
// when the id cannot be one. A session id comes from a stored handle, which the
// resume path accepts with a wider rule.
func pathSegment(kind, id string) (string, error) {
	if !idPattern.MatchString(id) {
		return "", fmt.Errorf("%q is not a MiMo %s id", id, kind)
	}
	return id, nil
}

// sessionPath returns the path of one session, or of a route below it when
// suffix is not empty.
func sessionPath(sessionID string, suffix ...string) (string, error) {
	segment, err := pathSegment("session", sessionID)
	if err != nil {
		return "", err
	}
	path := routeSessions + "/" + segment
	for _, part := range suffix {
		path += "/" + part
	}
	return path, nil
}

// requestPath returns the path of a route below one pending request.
func requestPath(route, kind, requestID, action string) (string, error) {
	segment, err := pathSegment(kind, requestID)
	if err != nil {
		return "", err
	}
	return route + "/" + segment + "/" + action, nil
}

func (r mimoRPC) health(ctx context.Context) (mimoHealth, error) {
	var health mimoHealth
	err := r.do(ctx, http.MethodGet, routeHealth, nil, &health)
	return health, err
}

func (r mimoRPC) createSession(ctx context.Context) (mimoSession, error) {
	var session mimoSession
	if err := r.do(ctx, http.MethodPost, routeSessions, struct{}{}, &session); err != nil {
		return mimoSession{}, err
	}
	if session.ID == "" {
		return mimoSession{}, fmt.Errorf("POST %s: the new session has no id", routeSessions)
	}
	return session, nil
}

func (r mimoRPC) getSession(ctx context.Context, sessionID string) (mimoSession, error) {
	path, err := sessionPath(sessionID)
	if err != nil {
		return mimoSession{}, err
	}
	var session mimoSession
	if err := r.do(ctx, http.MethodGet, path, nil, &session); err != nil {
		return mimoSession{}, err
	}
	if session.ID == "" {
		return mimoSession{}, fmt.Errorf("GET %s: the session record has no id", path)
	}
	return session, nil
}

// promptAsync sends a prompt and returns when the server accepted it. The turn
// runs afterwards, and its events arrive on the event stream.
func (r mimoRPC) promptAsync(ctx context.Context, sessionID string, request mimoPromptRequest) error {
	path, err := sessionPath(sessionID, "prompt_async")
	if err != nil {
		return err
	}
	return r.do(ctx, http.MethodPost, path, request, nil)
}

func (r mimoRPC) abort(ctx context.Context, sessionID string) error {
	path, err := sessionPath(sessionID, "abort")
	if err != nil {
		return err
	}
	return r.do(ctx, http.MethodPost, path, nil, nil)
}

// summarize runs a compaction. The server answers only after the compaction
// and the turn that follows it finish, so a caller runs it on its own goroutine
// with a context that outlives one API timeout.
func (r mimoRPC) summarize(ctx context.Context, sessionID string, model mimoModelRef) error {
	path, err := sessionPath(sessionID, "summarize")
	if err != nil {
		return err
	}
	return r.endpoint.Do(ctx, http.MethodPost, path, model, nil)
}

// command runs a slash command. Like summarize, the server answers after the
// turn that the command starts, so the caller decides the deadline.
func (r mimoRPC) command(ctx context.Context, sessionID string, request mimoCommandRequest) error {
	path, err := sessionPath(sessionID, "command")
	if err != nil {
		return err
	}
	return r.endpoint.Do(ctx, http.MethodPost, path, request, nil)
}

// sessionStatuses returns the status of every session that is not idle. An idle
// session has no entry.
func (r mimoRPC) sessionStatuses(ctx context.Context) (map[string]mimoStatus, error) {
	statuses := map[string]mimoStatus{}
	err := r.do(ctx, http.MethodGet, routeSessionStatus, nil, &statuses)
	return statuses, err
}

// messages returns the main agent's messages of a session. A subagent's
// messages are in another slice, which the route returns only when a query
// names the actor.
func (r mimoRPC) messages(ctx context.Context, sessionID string) ([]mimoMessageWithParts, error) {
	path, err := sessionPath(sessionID, "message")
	if err != nil {
		return nil, err
	}
	var messages []mimoMessageWithParts
	err = r.do(ctx, http.MethodGet, path, nil, &messages)
	return messages, err
}

// message returns one message of a session, from any actor's slice.
func (r mimoRPC) message(ctx context.Context, sessionID, messageID string) (mimoMessageInfo, error) {
	segment, err := pathSegment("message", messageID)
	if err != nil {
		return mimoMessageInfo{}, err
	}
	path, err := sessionPath(sessionID, "message", segment)
	if err != nil {
		return mimoMessageInfo{}, err
	}
	var message mimoMessageWithParts
	if err := r.do(ctx, http.MethodGet, path, nil, &message); err != nil {
		return mimoMessageInfo{}, err
	}
	return message.Info, nil
}

func (r mimoRPC) configProviders(ctx context.Context) (mimoConfigProviders, error) {
	var providers mimoConfigProviders
	err := r.do(ctx, http.MethodGet, routeConfigProviders, nil, &providers)
	return providers, err
}

func (r mimoRPC) config(ctx context.Context) (mimoConfig, error) {
	var config mimoConfig
	err := r.do(ctx, http.MethodGet, routeConfig, nil, &config)
	return config, err
}

func (r mimoRPC) agents(ctx context.Context) ([]mimoAgentInfo, error) {
	var agents []mimoAgentInfo
	err := r.do(ctx, http.MethodGet, routeAgents, nil, &agents)
	return agents, err
}

func (r mimoRPC) setSkipAll(ctx context.Context, enabled bool) error {
	return r.do(ctx, http.MethodPost, routeSkipAll, mimoSwitchBody{Enabled: enabled}, nil)
}

func (r mimoRPC) setAutoApproveDelete(ctx context.Context, enabled bool) error {
	return r.do(ctx, http.MethodPost, routeAutoApproveDel, mimoSwitchBody{Enabled: enabled}, nil)
}

func (r mimoRPC) pendingPermissions(ctx context.Context) ([]json.RawMessage, error) {
	var pending []json.RawMessage
	err := r.do(ctx, http.MethodGet, routePermissions, nil, &pending)
	return pending, err
}

func (r mimoRPC) pendingQuestions(ctx context.Context) ([]json.RawMessage, error) {
	var pending []json.RawMessage
	err := r.do(ctx, http.MethodGet, routeQuestions, nil, &pending)
	return pending, err
}

func (r mimoRPC) pendingBashInteractive(ctx context.Context) ([]json.RawMessage, error) {
	var pending []json.RawMessage
	err := r.do(ctx, http.MethodGet, routeBashInteractive, nil, &pending)
	return pending, err
}

func (r mimoRPC) replyPermission(ctx context.Context, permissionID string, body mimoPermissionReplyBody) error {
	path, err := requestPath(routePermissions, "permission", permissionID, "reply")
	if err != nil {
		return err
	}
	return r.do(ctx, http.MethodPost, path, body, nil)
}

func (r mimoRPC) replyQuestion(ctx context.Context, questionID string, answers [][]string) error {
	path, err := requestPath(routeQuestions, "question", questionID, "reply")
	if err != nil {
		return err
	}
	return r.do(ctx, http.MethodPost, path, mimoQuestionReplyBody{Answers: answers}, nil)
}

func (r mimoRPC) rejectQuestion(ctx context.Context, questionID string) error {
	path, err := requestPath(routeQuestions, "question", questionID, "reject")
	if err != nil {
		return err
	}
	return r.do(ctx, http.MethodPost, path, nil, nil)
}

func (r mimoRPC) replyBashInteractive(ctx context.Context, requestID string, body mimoBashReplyBody) error {
	path, err := requestPath(routeBashInteractive, "interactive command", requestID, "reply")
	if err != nil {
		return err
	}
	return r.do(ctx, http.MethodPost, path, body, nil)
}

// splitModelID splits a LeapMux model id, `<provider>/<model>`, into the pair a
// prompt carries. The model half may hold more slashes (an OpenRouter model id
// does), so the split is at the FIRST slash. ok is false for an id without a
// provider or without a model.
func splitModelID(id string) (mimoModelRef, bool) {
	provider, model, found := strings.Cut(id, "/")
	if !found || provider == "" || model == "" {
		return mimoModelRef{}, false
	}
	return mimoModelRef{ProviderID: provider, ModelID: model}, true
}

// joinModelID is the inverse of splitModelID.
func joinModelID(ref mimoModelRef) string {
	if ref.ProviderID == "" || ref.ModelID == "" {
		return ""
	}
	return ref.ProviderID + "/" + ref.ModelID
}
