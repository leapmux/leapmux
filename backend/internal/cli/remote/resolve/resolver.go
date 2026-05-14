// Package resolve owns the `leapmux remote` CLI's universal entity-ID
// resolver. Every handler that consumes any of {workspace_id, tile_id,
// worker_id, org_id, user_id, working_dir, tab_id} accepts any
// sufficient combination of --tab-id / --tile-id / --workspace-id /
// --worker-id / --org-id / --user-id (with matching LEAPMUX_REMOTE_*_ID
// env-var fallbacks). Resolve walks the supplied inputs, derives the
// missing fields via the hub's LocateTab / LocateTile / GetWorkspace /
// GetWorker / GetUser RPCs (and ListAgents / ListTerminals for
// working_dir), cross-checks every multi-source field for agreement,
// and returns either a populated Resolved struct or a structured
// invalid_request describing the conflict / missing requirement.
//
// The resolver does NOT read environment variables directly. The
// caller is expected to bind flag defaults to the LEAPMUX_REMOTE_*_ID
// env vars via BindEntityFlags (see flags.go); empty flag values are
// treated as "input not supplied" by the resolver.
package resolve

import (
	"context"
	"flag"
	"fmt"
	"sort"
	"strings"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"golang.org/x/sync/errgroup"
)

// Need declares which fields the caller requires. Resolve returns
// invalid_request when any required field is empty after derivation.
type Need struct {
	WorkspaceID bool
	TileID      bool
	WorkerID    bool
	OrgID       bool
	UserID      bool
	TabID       bool
	WorkingDir  bool
}

// Inputs carries the raw flag values + env-var-default-backed input
// for a single command invocation. Empty strings mean "not supplied"
// (the resolver doesn't distinguish between flag-omitted and
// env-empty). FixedTabType, if set, overrides any --tab-type flag
// and short-circuits the discriminator inference — used by handlers
// under the `agent ...` and `terminal ...` subgroups where the
// command path implies the type.
type Inputs struct {
	TabID        string
	TabType      string // "agent" | "terminal" (raw flag / env value)
	TileID       string
	WorkspaceID  string
	WorkerID     string
	OrgID        string
	UserID       string
	FixedTabType leapmuxv1.TabType // set when the command path pins the type (agent / terminal subgroups)

	// Explicit* mark which input came from a user-typed CLI flag as
	// opposed to a default sourced from the LEAPMUX_REMOTE_*_ID env
	// vars. When a derived value clashes with an explicit input for
	// the same field, the explicit input wins and the env-derived
	// value is silently dropped (the "I'm in a terminal spawn but I
	// passed --tile-id elsewhere" case must not trip a conflict
	// error). Two explicit inputs that disagree on the same field
	// still produce a hard conflict — that's a genuine user error.
	//
	// Populated automatically by BindEntityFlags via the FlagSet it
	// records in flagSet below; tests that construct Inputs directly
	// can set these fields explicitly.
	ExplicitTabID       bool
	ExplicitTileID      bool
	ExplicitWorkspaceID bool
	ExplicitWorkerID    bool
	ExplicitOrgID       bool
	ExplicitUserID      bool
	ExplicitTabType     bool

	// flagSet is the FlagSet BindEntityFlags registered the entity
	// flags on. Resolve walks fs.Visit at entry to mark Explicit*
	// without forcing every handler to add a post-parse line. Nil
	// when Inputs was constructed without going through
	// BindEntityFlags (e.g. unit tests).
	flagSet *flag.FlagSet
}

// ParseTabType converts a user-facing flag / env value to the wire
// enum. Accepts both the short form ("agent" / "terminal" / "file" /
// "") and the proto-canonical form ("TAB_TYPE_AGENT" /
// "TAB_TYPE_TERMINAL" / "TAB_TYPE_FILE" / "TAB_TYPE_UNSPECIFIED") so
// callers can paste a value straight from JSON output back into a
// flag or env var. Unknown strings return ok=false so callers can
// surface invalid_request.
func ParseTabType(s string) (leapmuxv1.TabType, bool) {
	switch s {
	case "", "TAB_TYPE_UNSPECIFIED":
		return leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED, true
	case "agent", "TAB_TYPE_AGENT":
		return leapmuxv1.TabType_TAB_TYPE_AGENT, true
	case "terminal", "TAB_TYPE_TERMINAL":
		return leapmuxv1.TabType_TAB_TYPE_TERMINAL, true
	case "file", "TAB_TYPE_FILE":
		return leapmuxv1.TabType_TAB_TYPE_FILE, true
	default:
		return leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED, false
	}
}

// TabTypeWireName returns the user-facing string ("agent" /
// "terminal" / "") for a wire TabType. Mirrors the ParseTabType
// inverse and is used by env-var comparison and help text.
func TabTypeWireName(t leapmuxv1.TabType) string {
	switch t {
	case leapmuxv1.TabType_TAB_TYPE_AGENT:
		return "agent"
	case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
		return "terminal"
	default:
		return ""
	}
}

// Resolved is the post-derivation snapshot. Every field populated by
// the resolver is the agreed-upon value across all input sources;
// fields not requested in Need (and not derivable from supplied
// inputs) are left empty.
type Resolved struct {
	TabID       string
	TabType     leapmuxv1.TabType
	TileID      string
	WorkspaceID string
	WorkerID    string
	OrgID       string
	UserID      string
	WorkingDir  string
}

// Deps is the dependency surface the resolver needs to issue hub
// RPCs and (best-effort) worker inner-RPCs. Each function returns
// an error only on transport / coding failures — "not found" or
// "permission denied" is signalled via the documented error code.
//
// All functions are required when the corresponding input is
// non-empty; the resolver will call only the ones whose inputs are
// present. Production code wires these to the `cmd` package's
// hubCallUnary / callInnerRPC helpers; tests inject stubs.
type Deps struct {
	// LocateTab resolves a tab id to its (matched type, workspace,
	// tile, worker). When the caller passes TAB_TYPE_UNSPECIFIED the
	// server matches any type and returns the actual type in the
	// first slot; the resolver records that back into Resolved.TabType.
	LocateTab func(ctx context.Context, tabType leapmuxv1.TabType, tabID string) (matchedTabType leapmuxv1.TabType, workspaceID, tileID, workerID string, err error)
	// GetWorkspace resolves a workspace id to its (org, owner user).
	GetWorkspace func(ctx context.Context, workspaceID string) (orgID, userID string, err error)
	// GetWorker resolves a worker id to its (registering user, org).
	GetWorker func(ctx context.Context, workerID string) (userID, orgID string, err error)
	// LocateTile resolves a tile id to its (workspace, org).
	LocateTile func(ctx context.Context, tileID string) (workspaceID, orgID string, err error)
	// GetUser resolves a user id to its org.
	GetUser func(ctx context.Context, userID string) (orgID string, err error)
	// GetWorkingDir is a best-effort inner-RPC to the worker; only
	// invoked when Need.WorkingDir is true AND we have (workerID,
	// tabType, tabID). Failure returns "" — the caller treats
	// working_dir as "unknown" rather than failing the whole
	// invocation, matching the cross-worker orphan-tolerance
	// behaviour of `agent close`.
	GetWorkingDir func(ctx context.Context, workerID string, tabType leapmuxv1.TabType, tabID string) (workingDir string, err error)
}

// Resolve walks the supplied Inputs, runs every derivation whose
// input is non-empty (in parallel), validates cross-source
// agreement, and returns the consolidated Resolved struct. Conflict
// errors and missing-required errors both surface as invalid_request
// with the structured fields named in the message so scripts can
// grep for the conflicting flag.
//
// Tab-type handling:
//   - Inputs.FixedTabType wins when set (agent/terminal subgroups
//     pin it via the command path).
//   - Otherwise Inputs.TabType is parsed; "" + tab id supplied is
//     rejected unless FixedTabType is set, because LocateTab needs
//     a tab_type to disambiguate the (tab_id) namespace.
func Resolve(ctx context.Context, deps Deps, need Need, in Inputs) (Resolved, error) {
	// Trim inputs to normalise leading/trailing whitespace from
	// shell substitutions; an unset env var defaulting to "" already
	// passes through, but a quoted "$LEAPMUX_REMOTE_TAB_ID" with a
	// trailing newline shouldn't bypass conflict detection.
	in.TabID = strings.TrimSpace(in.TabID)
	in.TabType = strings.TrimSpace(in.TabType)
	in.TileID = strings.TrimSpace(in.TileID)
	in.WorkspaceID = strings.TrimSpace(in.WorkspaceID)
	in.WorkerID = strings.TrimSpace(in.WorkerID)
	in.OrgID = strings.TrimSpace(in.OrgID)
	in.UserID = strings.TrimSpace(in.UserID)

	// Mark which inputs the user typed on the command line (vs which
	// inherited their value from $LEAPMUX_REMOTE_*_ID env defaults).
	// fs.Visit only fires for flags whose value the user actually set,
	// so a bare invocation inside a worker spawn (where every entity
	// flag still defaults from env) leaves every Explicit* false. The
	// aggregator uses these to break "explicit flag vs. env-derived"
	// conflicts in favour of the explicit flag.
	if in.flagSet != nil {
		in.flagSet.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "tab-id":
				in.ExplicitTabID = true
			case "tile-id":
				in.ExplicitTileID = true
			case "workspace-id":
				in.ExplicitWorkspaceID = true
			case "worker-id":
				in.ExplicitWorkerID = true
			case "org-id":
				in.ExplicitOrgID = true
			case "user-id":
				in.ExplicitUserID = true
			case "tab-type":
				in.ExplicitTabType = true
			}
		})
	}

	// Determine the effective tab type.
	tabType := in.FixedTabType
	if tabType == leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED {
		parsed, ok := ParseTabType(in.TabType)
		if !ok {
			return Resolved{}, invalidArg(
				"unknown --tab-type value %q; want \"agent\" or \"terminal\"",
				in.TabType,
			)
		}
		tabType = parsed
	} else if in.TabType != "" {
		// FixedTabType is set by the command path; if the user also
		// passed --tab-type, the two must agree.
		parsed, ok := ParseTabType(in.TabType)
		if !ok {
			return Resolved{}, invalidArg(
				"unknown --tab-type value %q; want \"agent\" or \"terminal\"",
				in.TabType,
			)
		}
		if parsed != tabType {
			return Resolved{}, invalidArg(
				"--tab-type %q contradicts this command's implicit type %q",
				TabTypeWireName(parsed), TabTypeWireName(tabType),
			)
		}
	}
	// A bare --tab-id with no tab type is OK — LocateTab on the
	// server treats TAB_TYPE_UNSPECIFIED as "match any type" and
	// returns the matched type, which the resolver records below.

	// agg accumulates derivations from every source. A field with
	// more than one source must have consistent values across all
	// of them; otherwise the resolver returns a conflict.
	agg := newAggregator()
	// Seed with the user-supplied inputs (each its own source). The
	// final argument records whether the user typed the flag (priority
	// in conflict resolution) versus inherited it from env.
	agg.put(fieldTabID, in.TabID, sourceFlagTab, in.ExplicitTabID)
	agg.put(fieldTileID, in.TileID, sourceFlagTile, in.ExplicitTileID)
	agg.put(fieldWorkspaceID, in.WorkspaceID, sourceFlagWorkspace, in.ExplicitWorkspaceID)
	agg.put(fieldWorkerID, in.WorkerID, sourceFlagWorker, in.ExplicitWorkerID)
	agg.put(fieldOrgID, in.OrgID, sourceFlagOrg, in.ExplicitOrgID)
	agg.put(fieldUserID, in.UserID, sourceFlagUser, in.ExplicitUserID)

	// Issue every derivation whose input is supplied. Run them in
	// parallel — the RPCs are independent and the resolver's worst-
	// case wall-clock latency is dominated by the slowest hop, not
	// the sum.
	g, gctx := errgroup.WithContext(ctx)

	// Each derivation fires only when (a) its input is supplied AND
	// (b) the corresponding Deps function is wired. Missing deps are
	// silent — the resolver leans on the final missingRequired check
	// to surface "couldn't fill required field X". This lets the
	// production cmd package always wire every dep (zero overhead
	// for un-used inputs) while keeping tests free to pass only the
	// deps they need.
	// resolvedTabTypeFromLocate captures the type LocateTab returns
	// when the caller didn't pin one. We backfill the outer tabType
	// after the errgroup wait so the Resolved struct carries the
	// authoritative discriminator. Only one goroutine writes this
	// field (the LocateTab goroutine below) and the post-Wait read
	// is happens-after the goroutine's exit, so no mutex is needed.
	var resolvedTabTypeFromLocate leapmuxv1.TabType
	if in.TabID != "" && deps.LocateTab != nil {
		g.Go(func() error {
			matched, ws, tile, worker, err := deps.LocateTab(gctx, tabType, in.TabID)
			if err != nil {
				return fmt.Errorf("locate tab %s: %w", in.TabID, err)
			}
			resolvedTabTypeFromLocate = matched
			// Each derivation inherits the priority of its source
			// input: when the user typed --tab-id, downstream
			// workspace_id / tile_id / worker_id share that priority
			// and win over env-derived values; otherwise the whole
			// chain is env-derived and yields to any explicit flag.
			agg.put(fieldWorkspaceID, ws, sourceTabID, in.ExplicitTabID)
			agg.put(fieldTileID, tile, sourceTabID, in.ExplicitTabID)
			agg.put(fieldWorkerID, worker, sourceTabID, in.ExplicitTabID)
			return nil
		})
	}

	if in.WorkspaceID != "" && deps.GetWorkspace != nil {
		g.Go(func() error {
			org, owner, err := deps.GetWorkspace(gctx, in.WorkspaceID)
			if err != nil {
				return fmt.Errorf("get workspace %s: %w", in.WorkspaceID, err)
			}
			agg.put(fieldOrgID, org, sourceWorkspaceID, in.ExplicitWorkspaceID)
			agg.put(fieldUserID, owner, sourceWorkspaceID, in.ExplicitWorkspaceID)
			return nil
		})
	}

	if in.WorkerID != "" && deps.GetWorker != nil {
		g.Go(func() error {
			owner, org, err := deps.GetWorker(gctx, in.WorkerID)
			if err != nil {
				return fmt.Errorf("get worker %s: %w", in.WorkerID, err)
			}
			agg.put(fieldUserID, owner, sourceWorkerID, in.ExplicitWorkerID)
			agg.put(fieldOrgID, org, sourceWorkerID, in.ExplicitWorkerID)
			return nil
		})
	}

	if in.TileID != "" && deps.LocateTile != nil {
		g.Go(func() error {
			ws, org, err := deps.LocateTile(gctx, in.TileID)
			if err != nil {
				return fmt.Errorf("locate tile %s: %w", in.TileID, err)
			}
			agg.put(fieldWorkspaceID, ws, sourceTileID, in.ExplicitTileID)
			agg.put(fieldOrgID, org, sourceTileID, in.ExplicitTileID)
			return nil
		})
	}

	if in.UserID != "" && deps.GetUser != nil {
		g.Go(func() error {
			org, err := deps.GetUser(gctx, in.UserID)
			if err != nil {
				return fmt.Errorf("get user %s: %w", in.UserID, err)
			}
			agg.put(fieldOrgID, org, sourceUserID, in.ExplicitUserID)
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return Resolved{}, err
	}

	// Cross-check every multi-source field for agreement. A
	// disagreement means the user supplied contradicting flags (or
	// the hub's state is genuinely inconsistent across RPCs, in
	// which case surfacing the conflict is still the right move).
	if conflicts := agg.conflicts(); len(conflicts) > 0 {
		return Resolved{}, invalidArg("conflicting inputs: %s", strings.Join(conflicts, "; "))
	}

	// If LocateTab matched a tab_id-only lookup, the response's
	// type wins over the (still-unspecified) caller hint.
	outTabType := tabType
	if outTabType == leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED {
		outTabType = resolvedTabTypeFromLocate
	}
	out := Resolved{
		TabID:       in.TabID,
		TabType:     outTabType,
		TileID:      agg.value(fieldTileID),
		WorkspaceID: agg.value(fieldWorkspaceID),
		WorkerID:    agg.value(fieldWorkerID),
		OrgID:       agg.value(fieldOrgID),
		UserID:      agg.value(fieldUserID),
	}

	// Working dir is fetched only on demand and only when we have a
	// (worker, tab, tab type) triple. Failure is non-fatal — orphan
	// tabs / dead workers must still resolve everything else.
	if need.WorkingDir && out.WorkerID != "" && out.TabID != "" && tabType != leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED && deps.GetWorkingDir != nil {
		if wd, err := deps.GetWorkingDir(ctx, out.WorkerID, tabType, out.TabID); err == nil {
			out.WorkingDir = wd
		}
	}

	// Validate required fields are populated. Each missing slot
	// surfaces the names of the flags that would have satisfied it,
	// so the user sees one error envelope listing every fix.
	if missing := missingRequired(need, out); len(missing) > 0 {
		return Resolved{}, invalidArg("missing required ID(s): %s", strings.Join(missing, "; "))
	}

	return out, nil
}

// invalidArg wraps an error message in a stable shape; the cmd
// package's EmitErrorWith will surface it as
// `{"error":{"code":"invalid_request",...}}`.
func invalidArg(format string, args ...any) error {
	return &ResolveError{Code: "invalid_request", Message: fmt.Sprintf(format, args...)}
}

// ResolveError is the structured error every resolver failure
// surfaces. The Code is a stable identifier the cmd package emits
// in the JSON envelope; Message is the human-facing detail.
type ResolveError struct {
	Code    string
	Message string
}

func (e *ResolveError) Error() string { return e.Message }

// --- internals: aggregation and conflict detection ---

// field is an enum of derivable fields. We track sources per-field
// so the conflict error can name the contradicting inputs.
type field int

const (
	fieldTabID field = iota
	fieldTileID
	fieldWorkspaceID
	fieldWorkerID
	fieldOrgID
	fieldUserID
)

func (f field) String() string {
	switch f {
	case fieldTabID:
		return "tab_id"
	case fieldTileID:
		return "tile_id"
	case fieldWorkspaceID:
		return "workspace_id"
	case fieldWorkerID:
		return "worker_id"
	case fieldOrgID:
		return "org_id"
	case fieldUserID:
		return "user_id"
	default:
		return fmt.Sprintf("field(%d)", f)
	}
}

// source labels each origin so conflict messages can attribute
// every value to the input flag that produced it.
type source string

const (
	sourceFlagTab       source = "--tab-id"
	sourceFlagTile      source = "--tile-id"
	sourceFlagWorkspace source = "--workspace-id"
	sourceFlagWorker    source = "--worker-id"
	sourceFlagOrg       source = "--org-id"
	sourceFlagUser      source = "--user-id"
	sourceTabID         source = "derived from --tab-id"
	sourceTileID        source = "derived from --tile-id"
	sourceWorkspaceID   source = "derived from --workspace-id"
	sourceWorkerID      source = "derived from --worker-id"
	sourceUserID        source = "derived from --user-id"
)

// sourceEntry pairs a (value, source) record with the priority it
// inherits from the input flag that produced it. `explicit=true`
// means the user typed the flag on the command line; false means it
// inherited from a $LEAPMUX_REMOTE_*_ID env default.
type sourceEntry struct {
	src      source
	explicit bool
}

type aggregator struct {
	mu      sync.Mutex
	sources map[field]map[string]sourceEntry // field -> value -> first reporter
}

func newAggregator() *aggregator {
	return &aggregator{sources: make(map[field]map[string]sourceEntry, 6)}
}

// put records that `src` resolved `f` to `value`. When the same
// (field, value) pair already exists, an explicit re-report upgrades
// the entry so a later value() / conflicts() pass treats the value
// as explicit (in case the same value arrived first from an env-
// derived source).
func (a *aggregator) put(f field, value string, src source, explicit bool) {
	if value == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sources[f] == nil {
		a.sources[f] = make(map[string]sourceEntry, 2)
	}
	existing, present := a.sources[f][value]
	if !present {
		a.sources[f][value] = sourceEntry{src: src, explicit: explicit}
		return
	}
	if explicit && !existing.explicit {
		a.sources[f][value] = sourceEntry{src: src, explicit: true}
	}
}

// value returns the consolidated value for a field. Explicit values
// win over env-derived ones; among env-derived values (when no
// explicit exists), conflicts() already rejected disagreements so
// any remaining entry is unambiguous.
func (a *aggregator) value(f field) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	values := a.sources[f]
	for v, info := range values {
		if info.explicit {
			return v
		}
	}
	for v := range values {
		return v
	}
	return ""
}

// conflicts returns a sorted list of human-readable conflict
// descriptions. A field is in conflict when:
//   - two or more explicit inputs disagree on it (genuine user error
//     — two flags pointing at different things), OR
//   - no explicit input names it and two or more env-derived sources
//     disagree.
//
// An explicit input that disagrees with an env-derived one for the
// same field is NOT a conflict — the explicit wins and the env input
// is silently shadowed. That's the "I'm sitting in a terminal spawn
// but I want to operate on a different tile" case.
func (a *aggregator) conflicts() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for f, values := range a.sources {
		if len(values) <= 1 {
			continue
		}
		var explicitVals, envVals []string
		for v, info := range values {
			if info.explicit {
				explicitVals = append(explicitVals, v)
			} else {
				envVals = append(envVals, v)
			}
		}
		switch {
		case len(explicitVals) > 1:
			parts := make([]string, 0, len(explicitVals))
			for _, v := range explicitVals {
				parts = append(parts, fmt.Sprintf("%s=%q via %s", f.String(), v, values[v].src))
			}
			sort.Strings(parts)
			out = append(out, strings.Join(parts, " vs "))
		case len(explicitVals) == 1:
			// Explicit shadows every env-derived disagreement.
		default:
			parts := make([]string, 0, len(envVals))
			for _, v := range envVals {
				parts = append(parts, fmt.Sprintf("%s=%q via %s", f.String(), v, values[v].src))
			}
			sort.Strings(parts)
			out = append(out, strings.Join(parts, " vs "))
		}
	}
	sort.Strings(out)
	return out
}

// missingRequired returns the names of Need.* fields that the
// resolver couldn't populate, paired with a hint listing the flags
// that would have satisfied them. Empty when every required field
// is set.
func missingRequired(need Need, r Resolved) []string {
	var out []string
	if need.WorkspaceID && r.WorkspaceID == "" {
		out = append(out, "--workspace-id (or pass --tab-id / --tile-id to derive it)")
	}
	if need.TileID && r.TileID == "" {
		out = append(out, "--tile-id (or pass --tab-id to derive it)")
	}
	if need.WorkerID && r.WorkerID == "" {
		out = append(out, "--worker-id (or pass --tab-id to derive it)")
	}
	if need.OrgID && r.OrgID == "" {
		out = append(out, "--org-id (or pass --workspace-id / --tab-id / --tile-id / --worker-id / --user-id to derive it)")
	}
	if need.UserID && r.UserID == "" {
		out = append(out, "--user-id (or pass --workspace-id / --tab-id / --worker-id to derive it)")
	}
	if need.TabID && r.TabID == "" {
		out = append(out, "--tab-id (with --tab-type, or via LEAPMUX_REMOTE_TAB_ID + LEAPMUX_REMOTE_TAB_TYPE)")
	}
	return out
}
