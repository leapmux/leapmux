package lifecycle

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/leapmux/leapmux/locallisten"
)

// The control protocol's two operations and their shapes. Every request
// authenticates with the per-run bearer token in control.token; the
// lifecycle operations never ride the Hub's network listeners.
const (
	controlStatusPath = "/v1/status"
	controlStopPath   = "/v1/stop"
	// controlMaxBody limits a stop request; anything larger is a
	// refusal before the body is parsed.
	controlMaxBody = 4 << 10
)

// StopRequest is the exact body a stop client sends. The instance and
// attempt identities must match the live runtime, so an old client
// cannot stop a newer process that replaced the one it inspected.
type StopRequest struct {
	InstanceID string `json:"instance_id"`
	AttemptID  string `json:"attempt_id"`
}

// StopResponse is the accepted reply. Acceptance means the shutdown was
// requested; the client still waits for the actual shutdown.
type StopResponse struct {
	Accepted  bool   `json:"accepted"`
	AttemptID string `json:"attempt_id"`
}

// controlServer serves the private local control endpoint of one
// running instance.
type controlServer struct {
	reporter Reporter
	token    string

	// liveID is the identity this server's process runs as; a stop
	// request that identifies any other instance or attempt is stale, and
	// refusing it makes zero shutdown calls.
	liveID RuntimeRecord

	ln      net.Listener
	httpSrv *http.Server

	closeOnce sync.Once
	closeErr  error
}

// serveControl starts the control endpoint for the instance the record
// describes. token is the fresh per-run bearer; the endpoint is private
// to the account by its socket or pipe security, and private to this
// instance by the identity a stop request must echo.
func serveControl(record RuntimeRecord, token string, reporter Reporter) (*controlServer, error) {
	if token == "" {
		return nil, errors.New("lifecycle: the control endpoint requires a token")
	}
	if reporter == nil {
		return nil, errors.New("lifecycle: the control endpoint requires a reporter")
	}
	// A short, account-owned socket directory keeps the path inside the
	// kernel's sockaddr_un limit whatever the data directory's depth.
	if dir, err := runtimeDir(); err == nil {
		if err := preparePrivateDir(dir); err != nil {
			return nil, err
		}
	}
	ln, err := locallisten.Listen(record.ControlURL)
	if err != nil {
		// A socket path the kernel cannot hold is a named, actionable
		// failure rather than a raw bind error.
		if isSocketPathTooLong(record.ControlURL) {
			return nil, controlSocketPathError(strings.TrimPrefix(record.ControlURL, "unix:"))
		}
		return nil, fmt.Errorf("lifecycle: open the control endpoint %s: %w", record.ControlURL, err)
	}
	s := &controlServer{reporter: reporter, token: token, liveID: record, ln: ln}
	mux := http.NewServeMux()
	mux.HandleFunc(controlStatusPath, s.handleStatus)
	mux.HandleFunc(controlStopPath, s.handleStop)
	s.httpSrv = &http.Server{Handler: mux, ReadHeaderTimeout: controlReadHeaderTimeout}
	go func() { _ = s.httpSrv.Serve(ln) }()
	return s, nil
}

// controlReadHeaderTimeout limits a request whose headers never finish;
// the endpoint is local, and a stuck header must not hold a handler.
const controlReadHeaderTimeout = 5 * time.Second

// handleStatus answers the authenticated status snapshot.
func (s *controlServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(r) {
		controlRefuse(w, http.StatusUnauthorized, "a valid bearer token is required")
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		controlRefuse(w, http.StatusMethodNotAllowed, "status requires GET")
		return
	}
	writeControlJSON(w, http.StatusOK, s.reporter.Status())
}

// handleStop authenticates, then matches the request's instance and
// attempt against the live runtime before any shutdown call happens.
func (s *controlServer) handleStop(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(r) {
		controlRefuse(w, http.StatusUnauthorized, "a valid bearer token is required")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		controlRefuse(w, http.StatusMethodNotAllowed, "stop requires POST")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, controlMaxBody+1))
	if err != nil || len(body) > controlMaxBody {
		controlRefuse(w, http.StatusBadRequest, "a stop request must contain at most 4096 bytes")
		return
	}
	var req StopRequest
	if err := json.Unmarshal(body, &req); err != nil {
		controlRefuse(w, http.StatusBadRequest, "a stop request must contain one JSON object")
		return
	}
	if req.InstanceID != s.liveID.InstanceID || req.AttemptID != s.liveID.AttemptID {
		controlRefuse(w, http.StatusConflict,
			"the request identifies another attempt; the inspected process was replaced")
		return
	}
	accepted := s.reporter.RequestStop()
	select {
	case <-accepted:
		writeControlJSON(w, http.StatusOK, StopResponse{Accepted: true, AttemptID: req.AttemptID})
	default:
		// A shutdown already in flight accepts the request too: the
		// outcome the caller waits for is the same.
		writeControlJSON(w, http.StatusOK, StopResponse{Accepted: true, AttemptID: req.AttemptID})
	}
}

// authorize checks the bearer token in constant time.
func (s *controlServer) authorize(r *http.Request) bool {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return false
	}
	got, bearer := strings.CutPrefix(values[0], "Bearer ")
	if !bearer || got == "" || len(got) != len(s.token) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

// Close stops the endpoint and releases its socket or pipe. The
// listener that http.Server.Close already closed is not an error: one
// endpoint, one release.
func (s *controlServer) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = errors.Join(ignoreClosed(s.httpSrv.Close()), ignoreClosed(s.ln.Close()))
		locallisten.CloseAccepted(s.ln)
		s.removeSocket()
	})
	return s.closeErr
}

// ignoreClosed maps the network stack's "already closed" to nil.
func ignoreClosed(err error) error {
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// removeSocket deletes the Unix socket file; a Windows named pipe needs
// no removal. A stale socket file would make the next bind fail.
func (s *controlServer) removeSocket() {
	if path, ok := strings.CutPrefix(s.liveID.ControlURL, "unix:"); ok {
		_ = os.Remove(filepath.Clean(path))
	}
}

// isSocketPathTooLong reports whether a unix URL's path cannot fit the
// kernel's sockaddr_un.
func isSocketPathTooLong(url string) bool {
	path, ok := strings.CutPrefix(url, "unix:")
	if !ok {
		return false
	}
	return len(path) >= maxUnixSocketPath
}

// writeControlJSON emits one JSON document with the content type the
// protocol states.
func writeControlJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

// controlRefuse emits the error shape every refusal shares.
func controlRefuse(w http.ResponseWriter, code int, message string) {
	writeControlJSON(w, code, map[string]string{"error": message})
}
