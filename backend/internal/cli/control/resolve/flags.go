package resolve

import (
	"flag"
	"os"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// FlagOptions tunes which subset of entity flags BindEntityFlags
// registers. Handlers that repurpose a flag for their own semantics
// (e.g., `tab list`, where --tab-type is an output filter rather than
// a resolver constraint) can suppress the resolver's binding. The
// goal is one declaration site per command — the entity flag set is
// uniform, not hand-rolled per handler.
//
// FixedTabType pins the tab discriminator for commands under
// `agent ...` / `terminal ...` subgroups so the resolver can derive
// from --tab-id without an explicit --tab-type flag. The handler
// then passes the same FixedTabType through to resolve.Inputs.
type FlagOptions struct {
	// Hide* suppresses the corresponding flag registration. Use this
	// for commands that must own the flag themselves (e.g., set
	// HideTile=true on `tab move`, which takes its destination via
	// --target-tile-id and must not let --tile-id double as a
	// derivation source for the move's source workspace).
	HideTab       bool
	HideTabType   bool
	HideTile      bool
	HideWorkspace bool
	HideWorker    bool

	// FixedTabType records that the command path implies a tab type;
	// the helper still binds the flag (so the user gets an error
	// envelope on a contradicting --tab-type), but propagates the
	// pin via Inputs.FixedTabType at parse time. Handlers under the
	// `tab ...` subgroup leave this zero.
	FixedTabType leapmuxv1.TabType
}

// BindEntityFlags registers the universal entity-ID flag set on fs
// and stores the parsed values into in.
//
// Two flags take an env-var default so worker-spawned invocations inherit the
// spawn's context without user input: `--worker-id` from
// LEAPMUX_CONTROL_WORKER_ID, and `--tab-id` from LEAPMUX_CONTROL_TAB_ID (gated on
// the _TAB_TYPE pair, below). The others are flag-only. A spawn also exports
// LEAPMUX_CONTROL_WORKSPACE_ID / _TILE_ID / _USER_ID, but nothing defaults from
// them -- workspace and tile are derived from the tab id, and there is no
// user-id axis at all (see Resolve).
//
// `--tab-id` env-var fallback is gated: when FixedTabType is set
// (agent / terminal subgroup), the env-var default fires only if
// LEAPMUX_CONTROL_TAB_TYPE matches. This prevents `tab close --type=agent`
// from auto-targeting the terminal you're running inside.
func BindEntityFlags(fs *flag.FlagSet, in *Inputs, opts FlagOptions) {
	if !opts.HideTab {
		fs.StringVar(&in.TabID, "tab-id", tabIDEnvDefault(opts.FixedTabType), tabIDFlagUsage(opts.FixedTabType, opts.HideTabType))
	}
	if !opts.HideTabType && opts.FixedTabType == leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED {
		// --tab-type defaults to empty so the resolver auto-detects
		// via LocateTab's UNSPECIFIED wildcard. Defaulting from
		// $LEAPMUX_CONTROL_TAB_TYPE would force users to pass
		// --tab-type=agent every time they operate on an agent tab
		// while sitting inside a terminal spawn (or vice versa). The
		// env var still governs the --tab-id env-var fallback for
		// the agent/terminal subgroups (see tabIDEnvDefault).
		fs.StringVar(&in.TabType, "tab-type", "", `tab type ("agent" | "terminal"; auto-detected when omitted)`)
	}
	if !opts.HideTile {
		fs.StringVar(&in.TileID, "tile-id", "", "tile id (derivable from --tab-id)")
	}
	if !opts.HideWorkspace {
		fs.StringVar(&in.WorkspaceID, "workspace-id", "", "workspace id (derivable from --tab-id / --tile-id)")
	}
	if !opts.HideWorker {
		fs.StringVar(&in.WorkerID, "worker-id", os.Getenv("LEAPMUX_CONTROL_WORKER_ID"), "worker id (defaults to $LEAPMUX_CONTROL_WORKER_ID; derivable from --tab-id)")
	}
	in.FixedTabType = opts.FixedTabType
	// Recording the FlagSet here lets Resolve mark Inputs.Explicit*
	// via fs.Visit after the caller's parseFlags has run, with no
	// per-handler boilerplate.
	in.flagSet = fs
}

// tabIDEnvDefault produces the --tab-id default value. For
// commands with a FixedTabType (agent / terminal subgroups), the
// env default only fires when LEAPMUX_CONTROL_TAB_TYPE matches the
// command's pinned type. The env var accepts either spelling
// ("agent"/"terminal" or "TAB_TYPE_AGENT"/"TAB_TYPE_TERMINAL");
// ParseTabType normalises both. For generic `tab ...` commands the
// env var is used regardless and the resolver later validates the
// tab type against the actual LocateTab response.
func tabIDEnvDefault(fixed leapmuxv1.TabType) string {
	if fixed == leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED {
		return os.Getenv("LEAPMUX_CONTROL_TAB_ID")
	}
	envType, _ := ParseTabType(os.Getenv("LEAPMUX_CONTROL_TAB_TYPE"))
	if envType != fixed {
		return ""
	}
	return os.Getenv("LEAPMUX_CONTROL_TAB_ID")
}

// tabIDFlagUsage adapts the help text to the surrounding command:
// agent/terminal subgroups describe the env-var gating, while the
// generic tab subgroup just points at the env var. When tabTypeHidden
// is set the command (e.g. `tab list`) owns the --tab-type flag for
// its own purposes, so we drop the "override with --tab-type" hint to
// avoid contradicting that command's help text.
func tabIDFlagUsage(fixed leapmuxv1.TabType, tabTypeHidden bool) string {
	switch fixed {
	case leapmuxv1.TabType_TAB_TYPE_AGENT:
		return `agent tab id (defaults to $LEAPMUX_CONTROL_TAB_ID when $LEAPMUX_CONTROL_TAB_TYPE="agent")`
	case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
		return `terminal tab id (defaults to $LEAPMUX_CONTROL_TAB_ID when $LEAPMUX_CONTROL_TAB_TYPE="terminal")`
	default:
		if tabTypeHidden {
			return `tab id (defaults to $LEAPMUX_CONTROL_TAB_ID; type auto-detected)`
		}
		return `tab id (defaults to $LEAPMUX_CONTROL_TAB_ID; type auto-detected, override with --tab-type)`
	}
}
