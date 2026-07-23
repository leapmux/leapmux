package remoteipc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/locallisten"
)

// StreamCancellerTTL is the inactivity threshold the per-server
// janitor uses to drop StreamCancellers entries that survived an
// abnormal stream teardown. Defense-in-depth: streams normally clean
// up via defer Delete inside Router.StreamInner, so under healthy
// operation the janitor finds nothing.
const StreamCancellerTTL = 1 * time.Hour

// streamCancellerSweepInterval drives StreamCancellerTTL. Cheap pass
// (one sync.Map walk) so it can run frequently relative to the TTL.
const streamCancellerSweepInterval = 10 * time.Minute

// AuthHeader is the request header the per-agent socket expects to
// carry the LEAPMUX_REMOTE_TOKEN.
const AuthHeader = "X-Leapmux-Token"

// Server hosts the per-agent RemoteIPCService over a per-agent local
// IPC listener. Each spawned agent gets its own listener+token; the
// server is a thin wrapper that owns the listener lifetime and
// dispatches authenticated requests to a Router.
type Server struct {
	url       string
	listener  net.Listener
	httpSrv   *http.Server
	router    *Router
	tokens    *TokenStore
	wg        sync.WaitGroup
	janitorWG sync.WaitGroup
	stopCh    chan struct{}
	stopOnce  sync.Once
}

// Options configures a per-agent server.
type Options struct {
	// SocketURL is "unix:<path>" on POSIX, "npipe:<name>" on Windows.
	SocketURL string
	// Token is the raw LEAPMUX_REMOTE_TOKEN handed to the spawned process.
	Token string
	// TokenInfo is the scope/identity associated with Token.
	TokenInfo TokenInfo
	// Router is the dispatch layer.
	Router *Router
}

// Listen starts a per-agent local-IPC server. Callers should call
// Close when the agent exits to tear down both the socket and the
// in-memory token registration.
func Listen(opts Options) (*Server, error) {
	if opts.Token == "" || opts.SocketURL == "" || opts.Router == nil {
		return nil, errors.New("remoteipc: Token, SocketURL, Router required")
	}
	tokens := NewTokenStore()
	tokens.Register(opts.Token, opts.TokenInfo)

	ln, err := locallisten.Listen(opts.SocketURL)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", opts.SocketURL, err)
	}
	if err := restrictSocketPermissions(opts.SocketURL); err != nil {
		_ = ln.Close()
		return nil, err
	}

	mux := http.NewServeMux()
	svc := &handler{tokens: tokens, router: opts.Router}
	path, h := leapmuxv1connect.NewRemoteIPCServiceHandler(svc)
	mux.Handle(path, withAuth(svc, h))

	protocols := &http.Protocols{}
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	srv := &http.Server{
		Handler:   mux,
		Protocols: protocols,
	}
	s := &Server{
		url:      opts.SocketURL,
		listener: ln,
		httpSrv:  srv,
		router:   opts.Router,
		tokens:   tokens,
		stopCh:   make(chan struct{}),
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Warn("remoteipc serve error", "url", opts.SocketURL, "error", err)
		}
	}()
	s.janitorWG.Add(1)
	go func() {
		defer s.janitorWG.Done()
		s.runStreamCancellerJanitor()
	}()
	return s, nil
}

// runStreamCancellerJanitor sweeps stale Router.StreamCancellers
// entries on a fixed interval. Exits when stopCh closes (driven by
// Close).
func (s *Server) runStreamCancellerJanitor() {
	ticker := time.NewTicker(streamCancellerSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-StreamCancellerTTL)
			if dropped := s.router.SweepStaleCancellers(cutoff); dropped > 0 {
				slog.Warn("remoteipc: swept stale stream cancellers",
					"url", s.url, "dropped", dropped, "ttl", StreamCancellerTTL)
			}
		}
	}
}

// URL returns the listener URL (unix:<path> / npipe:<name>).
func (s *Server) URL() string { return s.url }

// Close terminates the server and removes the socket file (Unix only).
// Idempotent — double Close (factory teardown + test cleanup both
// invoke it on the same Server) must not panic on close-of-closed.
func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_ = s.httpSrv.Shutdown(ctx)
		locallisten.CloseAccepted(s.listener)
		if scheme, target, err := locallisten.Parse(s.url); err == nil && scheme == locallisten.SchemeUnix {
			_ = os.Remove(target)
		}
		s.wg.Wait()
		s.janitorWG.Wait()
	})
	return nil
}

// withAuth enforces the X-Leapmux-Token header and stashes the
// authenticated TokenInfo on the request context for handlers to read.
func withAuth(h *handler, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get(AuthHeader)
		if token == "" {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}
		info, err := h.tokens.Lookup(token)
		if err != nil {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(contextWithToken(r.Context(), info, token)))
	})
}

// restrictSocketPermissions chmods the unix socket to 0600 so only the
// owning uid can connect. On Windows the named pipe inherits the
// process's default DACL — no additional restriction needed since
// LeapMux assumes the worker process and the spawned agent run as the
// same user.
func restrictSocketPermissions(socketURL string) error {
	scheme, target, err := locallisten.Parse(socketURL)
	if err != nil {
		return err
	}
	if scheme != locallisten.SchemeUnix {
		return nil
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	return os.Chmod(target, 0o600)
}

// SocketKind tags the spawned-entity flavour folded into a per-spawn
// socket path. Splitting the agent / terminal namespaces keeps the
// path collision-free even when two truncated IDs share their first 8
// chars (and lets the file name flag the spawn kind at a glance).
type SocketKind string

const (
	SocketKindAgent    SocketKind = "agent"
	SocketKindTerminal SocketKind = "terminal"
)

// shortPrefix returns the first n chars of s, or s if it's shorter.
// Used to keep Unix socket paths under the 104-byte sun_path limit
// on macOS / *BSD when the underlying IDs are 48-char nanoids.
// 8 chars of the 62-symbol alphanumeric alphabet give ~47 bits of
// entropy per ID — collisions across the spawns of a single
// worker's process lifetime are not a practical concern.
func shortPrefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// DefaultSocketPath returns the canonical per-spawn socket URL the
// worker uses when spawning agents / terminals. workerID + (kind,
// entityID) are folded in so concurrent spawns on the same worker
// get distinct sockets.
//
// Path budget (Unix): macOS's sun_path is 104 bytes including NUL,
// and the default $TMPDIR is ~49 chars (`/var/folders/<2>/<32>/T/`).
// With 48-char nanoid IDs the full path would be 170+ chars and
// `bind: invalid argument` rejects it, so the IDs are truncated to
// 8-char prefixes here. Windows named-pipe names have no comparable
// constraint and keep the full IDs for readability.
func DefaultSocketPath(workerID string, kind SocketKind, entityID string) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("npipe:leapmux-worker-%s-%s-%s", workerID, kind, entityID)
	}
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	dir = filepath.Join(dir, "lmx-"+shortPrefix(workerID, 8))
	_ = os.MkdirAll(dir, 0o700)
	// Single-letter kind tag ('a' / 't') keeps the agent and terminal
	// namespaces disjoint inside one worker dir without spending
	// path budget on the full word.
	file := string(kind[0]) + "-" + shortPrefix(entityID, 8) + ".sock"
	return "unix:" + filepath.Join(dir, file)
}

// MintToken returns a fresh raw token suitable for the
// LEAPMUX_REMOTE_TOKEN env var. Re-uses the project's nanoid generator
// for ~286 bits of entropy.
func MintToken() string {
	return id.Generate()
}

// --- Context plumbing ---

type ctxKey struct{}

type ctxValue struct {
	Info  TokenInfo
	Token string
}

func contextWithToken(ctx context.Context, info TokenInfo, token string) context.Context {
	return context.WithValue(ctx, ctxKey{}, ctxValue{Info: info, Token: token})
}

func tokenFromContext(ctx context.Context) (TokenInfo, string, bool) {
	v, ok := ctx.Value(ctxKey{}).(ctxValue)
	if !ok {
		return TokenInfo{}, "", false
	}
	return v.Info, v.Token, true
}

// --- ConnectRPC handler ---

type handler struct {
	leapmuxv1connect.UnimplementedRemoteIPCServiceHandler
	tokens *TokenStore
	router *Router
}

func (h *handler) Whoami(ctx context.Context, req *connect.Request[leapmuxv1.WhoamiRequest]) (*connect.Response[leapmuxv1.WhoamiResponse], error) {
	info, _, ok := tokenFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("no token"))
	}
	scope := &leapmuxv1.RemoteScope{
		WorkspaceIds: []string{info.WorkspaceID},
	}
	return connect.NewResponse(&leapmuxv1.WhoamiResponse{
		UserId:      info.UserID.String(),
		OrgId:       info.OrgID,
		WorkspaceId: info.WorkspaceID,
		WorkerId:    info.WorkerID,
		TabId:       info.TabID,
		TabType:     info.TabType,
		Scope:       scope,
	}), nil
}

func (h *handler) CallInner(ctx context.Context, req *connect.Request[leapmuxv1.CallInnerRequest]) (*connect.Response[leapmuxv1.CallInnerResponse], error) {
	info, _, ok := tokenFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("no token"))
	}
	resp, err := h.router.CallInner(ctx, info,
		req.Msg.GetMethod(), req.Msg.GetPayload(),
		req.Msg.GetTargetWorkerId(), req.Msg.GetWorkspaceId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *handler) StreamInner(ctx context.Context, req *connect.Request[leapmuxv1.StreamInnerRequest], stream *connect.ServerStream[leapmuxv1.StreamInnerEnvelope]) error {
	info, _, ok := tokenFromContext(ctx)
	if !ok {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("no token"))
	}
	return h.router.StreamInner(ctx, info,
		req.Msg.GetMethod(), req.Msg.GetPayload(),
		req.Msg.GetTargetWorkerId(), req.Msg.GetWorkspaceId(),
		req.Msg.GetClientRequestId(),
		func(env *leapmuxv1.StreamInnerEnvelope) error {
			return stream.Send(env)
		})
}

func (h *handler) Cancel(ctx context.Context, req *connect.Request[leapmuxv1.CancelRequest]) (*connect.Response[leapmuxv1.CancelResponse], error) {
	if id := req.Msg.GetClientRequestId(); id != "" {
		h.router.CancelStream(id)
	}
	return connect.NewResponse(&leapmuxv1.CancelResponse{}), nil
}

// tabTypeWireName projects a TabType enum to the canonical lowercase
// wire string ("agent" / "terminal" / "file"). Returns "" for the
// unspecified zero value so callers can guard the env-var emit with
// the same `if !="" then add` shape they had against the old string
// field. Keeping the projection here (rather than reusing the CLI's
// tabTypeName) avoids a worker→cli import direction.
func tabTypeWireName(t leapmuxv1.TabType) string {
	switch t {
	case leapmuxv1.TabType_TAB_TYPE_AGENT:
		return "agent"
	case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
		return "terminal"
	case leapmuxv1.TabType_TAB_TYPE_FILE:
		return "file"
	default:
		return ""
	}
}

// EnvVars assembles the LEAPMUX_REMOTE_* env vars to inject into the
// spawned process. The caller appends them to cmd.Env. Keeping this
// here means the env-var schema lives next to the server and changes
// to either side stay in sync.
//
// Only the small set of values the CLI can't cheaply derive from the
// tab id is injected. Every entity-ID variable carries an explicit
// _ID suffix so scripts can grep for `LEAPMUX_REMOTE_*_ID` to list
// every identifier in the spawn's context:
//
//   - SOCK / TOKEN — bound to this socket, immutable.
//   - USER_ID / ORG_ID / WORKER_ID — the spawn's principal, org, and
//     host worker. Org is included because workspaces don't move
//     between orgs, so it's safe and avoids a round-trip.
//   - TAB_ID + TAB_TYPE — the spawned tab's id plus "agent"/"terminal"
//     discriminator. This is the canonical anchor everything else
//     hangs off via the hub LocateTab RPC.
//   - WORKING_DIR / AGENT_PROVIDER — set at spawn, immutable for the
//     lifetime of the agent; saved here so the CLI doesn't have to
//     round-trip the worker on every invocation.
//
// Workspace id and tile id are deliberately NOT injected — they're
// derivable from TAB_ID via a single hub LocateTab call, and deriving
// avoids both the staleness window (tab moves) and any way for a
// misbehaving client to inject a lying value.
func EnvVars(socketURL, token string, info TokenInfo) []string {
	envs := []string{
		"LEAPMUX_REMOTE_SOCK=" + socketURL,
		"LEAPMUX_REMOTE_TOKEN=" + token,
		"LEAPMUX_REMOTE_USER_ID=" + info.UserID.String(),
		"LEAPMUX_REMOTE_WORKER_ID=" + info.WorkerID,
	}
	if info.OrgID != "" {
		envs = append(envs, "LEAPMUX_REMOTE_ORG_ID="+info.OrgID)
	}
	if info.TabID != "" {
		envs = append(envs, "LEAPMUX_REMOTE_TAB_ID="+info.TabID)
	}
	if tt := tabTypeWireName(info.TabType); tt != "" {
		envs = append(envs, "LEAPMUX_REMOTE_TAB_TYPE="+tt)
	}
	if info.WorkingDir != "" {
		envs = append(envs, "LEAPMUX_REMOTE_WORKING_DIR="+info.WorkingDir)
	}
	if info.AgentProvider != "" {
		envs = append(envs, "LEAPMUX_REMOTE_AGENT_PROVIDER="+info.AgentProvider)
	}
	return envs
}
