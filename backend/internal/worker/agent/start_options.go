package agent

import (
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
	"github.com/leapmux/leapmux/internal/worker/config"
)

// DefaultAPITimeout is the fallback timeout for JSON-RPC requests to the
// agent process, used when no configured value is provided.
const DefaultAPITimeout = config.DefaultAPITimeout

// Options configures one agent process start.
type Options struct {
	AgentID         string
	WorkingDir      string
	ResumeSessionID string // If set, uses --resume to resume a previous session
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
	// ExtraEnv is appended verbatim to the spawned process's
	// environment after the provider-specific env-var setup. The
	// service.Service populates this with LEAPMUX_CONTROL_* so the
	// running agent can drive the worker via the leapmux control CLI.
	ExtraEnv []string
	// AgentDirs is where an agent creates its private directory, with the
	// spec that its Registration states. The Manager sets it to the
	// directories that PrepareAgentDirs prepared. nil when the worker prepared
	// none; a provider that needs a directory then refuses to start.
	AgentDirs *agentdir.Dirs
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
