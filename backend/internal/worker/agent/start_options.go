package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
	"github.com/leapmux/leapmux/internal/worker/config"
)

// DefaultAPITimeout is the fallback timeout for JSON-RPC requests to the
// agent process, used when no configured value is provided.
const DefaultAPITimeout = config.DefaultAPITimeout

// ErrStartupCanceled reports that a provider stopped startup before spawning
// because a required startup confirmation was canceled, unavailable, or denied.
// Session-preserving restart paths keep any stored native session ID when they
// receive an error wrapping ErrStartupCanceled.
var ErrStartupCanceled = errors.New("agent startup canceled")

// StartupPrompt describes a one-time user confirmation that a provider asks
// for before continuing subprocess startup.
type StartupPrompt struct {
	Title          string
	Description    string
	Steps          []string
	Note           string
	ConfirmLabel   string
	CancelLabel    string
	WaitingMessage string
	ProceedMessage string
}

// StartupPromptConfirmer presents a StartupPrompt to the user and blocks until
// the user proceeds, cancels, or the launch context/lifecycle ends.
type StartupPromptConfirmer func(ctx context.Context, prompt StartupPrompt) (bool, error)

// SessionInitKind identifies how an agent initializes its native provider
// session at launch.
type SessionInitKind uint8

const (
	// SessionInitFresh starts a brand-new native provider session.
	SessionInitFresh SessionInitKind = iota
	// SessionInitResume resumes an existing native provider session.
	SessionInitResume
	// SessionInitFork forks conversation history from an existing native
	// provider session into a new independent session.
	SessionInitFork
)

// SessionInit is the mutually exclusive session origin for an agent launch.
type SessionInit struct {
	Kind             SessionInitKind
	SessionID        string
	SourceWorkingDir string // Source agent's working directory; only valid when Kind == SessionInitFork.
}

// FreshSession returns a SessionInit that starts a new session.
func FreshSession() SessionInit { return SessionInit{} }

// ResumeSession returns a SessionInit that resumes sessionID (or a fresh
// session when sessionID is empty).
func ResumeSession(sessionID string) SessionInit {
	if sessionID == "" {
		return SessionInit{}
	}
	return SessionInit{Kind: SessionInitResume, SessionID: sessionID}
}

// ForkSession returns a SessionInit that forks conversation history from
// sourceSessionID (optionally reading cwd-keyed provider stores from
// sourceWorkingDir). Returns a fresh session when sourceSessionID is empty.
func ForkSession(sourceSessionID, sourceWorkingDir string) SessionInit {
	if sourceSessionID == "" {
		return SessionInit{}
	}
	return SessionInit{
		Kind:             SessionInitFork,
		SessionID:        sourceSessionID,
		SourceWorkingDir: sourceWorkingDir,
	}
}

// IsResume reports whether the launch resumes an existing native session.
func (s SessionInit) IsResume() bool {
	return s.Kind == SessionInitResume && s.SessionID != ""
}

// IsFork reports whether the launch forks an existing native session.
func (s SessionInit) IsFork() bool {
	return s.Kind == SessionInitFork && s.SessionID != ""
}

// ResumeID returns the resumed session ID when Kind is SessionInitResume, or "".
func (s SessionInit) ResumeID() string {
	if s.Kind == SessionInitResume {
		return s.SessionID
	}
	return ""
}

// ForkID returns the source session ID when Kind is SessionInitFork, or "".
func (s SessionInit) ForkID() string {
	if s.Kind == SessionInitFork {
		return s.SessionID
	}
	return ""
}

// ForkSourceWorkingDir returns the source working directory when Kind is
// SessionInitFork, or "".
func (s SessionInit) ForkSourceWorkingDir() string {
	if s.Kind == SessionInitFork {
		return s.SourceWorkingDir
	}
	return ""
}

// Validate checks internal consistency of the SessionInit value.
func (s SessionInit) Validate() error {
	switch s.Kind {
	case SessionInitFresh:
		if s.SessionID != "" || s.SourceWorkingDir != "" {
			return fmt.Errorf("fresh session must not set session ID %q or source working dir %q", s.SessionID, s.SourceWorkingDir)
		}
	case SessionInitResume:
		if s.SessionID == "" {
			return errors.New("resume session requires a non-empty session ID")
		}
		if s.SourceWorkingDir != "" {
			return fmt.Errorf("resume session must not set fork source working dir %q", s.SourceWorkingDir)
		}
	case SessionInitFork:
		if s.SessionID == "" {
			return errors.New("fork session requires a non-empty source session ID")
		}
	default:
		return fmt.Errorf("unknown session init kind %d", s.Kind)
	}
	return nil
}

// Options configures one agent process start.
type Options struct {
	AgentID    string
	WorkingDir string
	// Session specifies whether this launch starts fresh, resumes an existing
	// native session, or forks conversation history from one.
	Session SessionInit
	// Options is the COMPLETE resolved option set keyed by option-group id
	// (model, effort, permissionMode, primaryAgent, and every provider option).
	// It is the single source of truth -- there are no shadow scalar fields. Read
	// a specific axis via Model()/Effort()/PermissionMode()/Get(id). It is the same
	// optionmap.Map type the service layer (OptionMap) and the Agent interface use,
	// so a launch option set flows to/from those boundaries without a conversion.
	Options optionmap.Map
	// NewSessionDefaultOptionIDs records which option values came from the
	// safe defaults for a new session. A provider can fall back when its local
	// CLI does not support one of these defaults. Explicit values are absent.
	NewSessionDefaultOptionIDs map[string]bool
	StartupTimeout             time.Duration           // Timeout for the startup handshake (default: 5m)
	APITimeout                 time.Duration           // Timeout for JSON-RPC requests (default: 10s)
	Shell                      string                  // Default shell path (always set when using shell wrapper)
	LoginShell                 bool                    // If true, use interactive+login shell flags
	HomeDir                    string                  // User's home directory (reads Claude Code settings; expands `~` when the Pi rule CHECKS a resume handle -- Pi expands it again itself)
	AgentProvider              leapmuxv1.AgentProvider // Coding agent provider (default: CLAUDE_CODE)
	// ExtraEnv holds the LEAPMUX_CONTROL_* variables of the spawn. The
	// service.Service populates this so the running agent can drive the
	// worker via the leapmux control CLI. FinalizeAgentEnv applies them
	// through controlenv.Apply, after the provider-specific env-var setup:
	// they land last, and the directory of LEAPMUX_CONTROL_CLI goes first on
	// PATH.
	ExtraEnv []string
	// AgentDirs is where an agent creates its private directory, with the
	// spec that its Registration states. The Manager sets it to the
	// directories that PrepareAgentDirs prepared. nil when the worker prepared
	// none; a provider that needs a directory then refuses to start.
	AgentDirs *agentdir.Dirs
	// ConfirmStartupPrompt presents a provider-supplied confirmation guide
	// before continuing startup. nil on non-interactive launches such as
	// boot-time background resume.
	ConfirmStartupPrompt StartupPromptConfirmer
}

// Get returns the resolved value of an option-group id, or "" if absent. The
// Model/Effort/PermissionMode helpers are by-id readers, not assignable fields --
// the option map remains the single representation.
func (o Options) Get(id string) string { return o.Options[id] }

func (o Options) Model() string { return o.Options[OptionIDModel] }

func (o Options) Effort() string { return o.Options[OptionIDEffort] }

func (o Options) PermissionMode() string { return o.Options[OptionIDPermissionMode] }

func (o Options) EffectiveStartupTimeout() time.Duration {
	if o.StartupTimeout > 0 {
		return o.StartupTimeout
	}
	return config.DefaultAgentStartupTimeout
}

func (o Options) EffectiveAPITimeout() time.Duration {
	if o.APITimeout > 0 {
		return o.APITimeout
	}
	return DefaultAPITimeout
}

// ValidateSessionSource checks that Session is internally consistent.
func (o Options) ValidateSessionSource() error {
	return o.Session.Validate()
}
