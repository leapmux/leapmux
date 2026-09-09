// Package resolve owns the `leapmux control` CLI's universal entity-ID
// resolver. Every handler that consumes any of {workspace_id, tile_id,
// worker_id, tab_id} accepts any sufficient combination of --tab-id /
// --tile-id / --workspace-id / --worker-id (with matching
// LEAPMUX_CONTROL_*_ID env-var fallbacks). Resolve walks the supplied
// inputs, derives the missing fields via the hub's LocateTab / LocateTile
// RPCs, cross-checks every multi-source field for agreement, and returns
// either a populated Resolved struct or a structured invalid_request
// describing the conflict / missing requirement.
//
// `working_dir` is deliberately NOT one of them. A command that needs a
// directory binds its own flag with a $LEAPMUX_CONTROL_WORKING_DIR default --
// see `--path` on the git verbs and `--working-dir` on `terminal quake`. The
// resolver used to carry a best-effort derivation for it that no command ever
// asked for, and an axis with no caller is an axis nobody keeps correct.
//
// There is deliberately no user-id axis: the tenant is implied by the
// authenticated session, so no hub RPC takes one and the CLI has no
// --user-id flag.
//
// --worker-id likewise gets no confirmation RPC. Every worker-spawned
// agent inherits $LEAPMUX_CONTROL_WORKER_ID, so such a call would fire
// on essentially every agent-issued command, over a delegation bearer
// the hub limits with auth.CeilingFor(BearerKindDelegation). The RPCs
// that could serve it -- WorkerManagementService.GetWorker and
// ListWorkers -- need worker:read, which that ceiling does admit, but a
// stage that adds a hub round trip to every agent command buys nothing
// the command does not learn anyway when it opens the channel. The cmd
// package's maybePreflightWorker keeps a best-effort check that
// tolerates a denial instead of failing on it.
//
// The resolver does NOT read environment variables directly. The
// caller is expected to bind flag defaults to the LEAPMUX_CONTROL_*_ID
// env vars via BindEntityFlags (see flags.go); empty flag values are
// treated as "input not supplied" by the resolver.
package resolve

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"golang.org/x/sync/errgroup"
)

// ErrTabNotLocatable reports a tab id the Hub holds no placement for.
//
// It is NOT an ordinary lookup failure, and the resolver treats it as one of
// two very different things depending on what the command actually needs.
//
// Some worker-hosted entities have no CRDT tab at all, and a QUAKE terminal is
// the one that matters: it belongs to a working directory rather than to a tab,
// so nothing in the CRDT describes it and LocateTab can never place it. A
// command that only needs the WORKER -- `terminal send`, `terminal get` -- has
// everything it requires without the derivation, so failing it here would
// refuse a perfectly answerable request purely because a lookup it did not need
// came back empty. That is what used to make every `leapmux control` command
// typed inside a quake panel fail before reaching its own body.
//
// So: an unlocatable tab is tolerated while nothing required is missing, and
// reported as this error the moment something is. A command that needs the
// workspace or the tile still fails, with this message rather than a vaguer
// "missing required ID(s)".
//
// Every OTHER LocateTab failure stays fatal. A network error or a permission
// denial means the Hub could not be ASKED, which says nothing about whether the
// tab exists -- swallowing those would turn an outage into a confusing
// worker-side error much later.
var ErrTabNotLocatable = errors.New("the hub has no placement for this tab")

// Need declares which fields the caller USES, and how strongly.
//
// A `true` field is REQUIRED: Resolve returns invalid_request when it is still
// empty after derivation. A field in `Want` is READ but optional -- the caller
// handles an empty value itself.
//
// Both halves matter, and the second is not decoration. Resolve fires the hub's
// LocateTab whenever a tab id is present, so a command that already holds
// everything it reads paid a round trip whose every output it discarded -- and
// a transport failure on it failed a command whose inputs were complete. It can
// only skip that call if it knows what the handler will read, which is what
// `Want` states. A field read but declared in NEITHER half comes back empty,
// which is why every reader must appear in one of them.
//
// The skip costs one cross-check, and it is a deliberate trade. Two AMBIENT ids
// that disagree -- a stale $LEAPMUX_CONTROL_TAB_ID beside a live
// $LEAPMUX_CONTROL_WORKER_ID -- are no longer reported as `conflicting inputs`,
// because the call that would have noticed is the one being skipped. Inside a
// real spawn they cannot disagree: one EnvVars call writes both, from one
// spawn. A tab id the user TYPED always fires the derivation, so the conflict a
// user can actually make is still reported.
type Need struct {
	WorkspaceID bool
	TileID      bool
	WorkerID    bool
	TabID       bool
	// Want lists the fields the caller reads without requiring. Same field
	// names, no emptiness check.
	Want Wants
}

// Wants is the optional half of Need. See its doc.
type Wants struct {
	WorkspaceID bool
	TileID      bool
	WorkerID    bool
	// TabType is filled only by LocateTab, and it is not a Need field because
	// no caller can supply it: a command that switches on the tab's kind must
	// say so here or read UNSPECIFIED.
	TabType bool
}

// wantsTabPlacement reports whether the hub's LocateTab has anything left to do
// for this invocation.
//
// It is the whole skip condition, and it answers TRUE in three cases:
//
//   - The user TYPED --tab-id. They typed it to act on that tab, so the hub's
//     refusal of it is the answer to the command rather than a detail of a
//     derivation -- see ErrTabNotLocatable.
//   - A field the caller reads is still EMPTY, so only the hub can fill it.
//   - A field the caller reads was supplied by an EXPLICIT flag, so the
//     derivation is what cross-checks the two and reports `conflicting inputs`.
//     Skipping here would let a typed --worker-id that contradicts the tab win
//     silently.
//
// What is left is the case worth skipping: every field the caller reads is
// already present and came from the ambient environment, which is exactly what
// a command run inside a tab spawn holds. That call's four outputs were
// discarded, and a transport failure on it failed a command whose inputs were
// complete.
func wantsTabPlacement(need Need, in Inputs) bool {
	// TabType comes ONLY from the hub -- no flag supplies it -- so a caller that
	// switches on it always needs the call.
	if in.ExplicitTabID || need.Want.TabType {
		return true
	}
	for _, f := range []struct {
		read     bool
		explicit bool
		value    string
	}{
		{need.WorkspaceID || need.Want.WorkspaceID, in.ExplicitWorkspaceID, in.WorkspaceID},
		{need.TileID || need.Want.TileID, in.ExplicitTileID, in.TileID},
		{need.WorkerID || need.Want.WorkerID, in.ExplicitWorkerID, in.WorkerID},
	} {
		if f.read && (f.value == "" || f.explicit) {
			return true
		}
	}
	return false
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
	FixedTabType leapmuxv1.TabType // set when the command path pins the type (agent / terminal subgroups)

	// Explicit* mark which input came from a user-typed CLI flag as
	// opposed to a default sourced from the LEAPMUX_CONTROL_*_ID env
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
	ExplicitTabType     bool

	// flagSet is the FlagSet BindEntityFlags registered the entity
	// flags on. Resolve walks fs.Visit at entry to mark Explicit*
	// without forcing every handler to add a post-parse line. Nil
	// when Inputs was constructed without going through
	// BindEntityFlags (e.g. unit tests).
	flagSet *flag.FlagSet
}

// ParseTabType converts a user-facing flag / env value to the wire enum.
//
// Both spellings are accepted -- the short token ("agent", "file", "") and the
// proto-canonical name ("TAB_TYPE_FILE") -- so a value pasted straight out of a
// JSON envelope goes back into a flag or an env var unchanged. Unknown strings
// return ok=false so callers can surface invalid_request.
//
// The table is GENERATED from contracts/tab-types.json. It used to be a switch
// here and a second switch in TabTypeWireName, and the two were the inverse of
// each other only by hand: a kind added to one and missed by the other made the
// contradiction message below print two empty strings.
func ParseTabType(s string) (leapmuxv1.TabType, bool) {
	t, ok := contracts.TabTypeParseAliases[s]
	return t, ok
}

// TabTypeWireName returns the token a user types and reads for a wire TabType,
// or "" for the unspecified zero value. It is the inverse of ParseTabType by
// CONSTRUCTION now: both read the one generated table, so neither can name a
// kind the other does not.
func TabTypeWireName(t leapmuxv1.TabType) string {
	return contracts.TabTypeWireToken[t]
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
	//
	// It must report ErrTabNotLocatable for an id the Hub holds no
	// placement for, so the resolver can tell that apart from "the Hub
	// could not be asked" -- see that error for why the difference decides
	// whether the command proceeds.
	LocateTab func(ctx context.Context, tabType leapmuxv1.TabType, tabID string) (matchedTabType leapmuxv1.TabType, workspaceID, tileID, workerID string, err error)
	// GetWorkspace confirms the supplied workspace id identifies a
	// workspace the caller can read. The hub conflates "no such
	// workspace" with "not yours", so a bogus --workspace-id comes
	// back as an error rather than a negative answer -- there is no
	// "resolved fine, but no such workspace" outcome to report.
	GetWorkspace func(ctx context.Context, workspaceID string) error
	// LocateTile resolves a tile id to its workspace.
	LocateTile func(ctx context.Context, tileID string) (workspaceID string, err error)
}

// Resolve walks the supplied Inputs, runs every derivation whose
// input is non-empty (in parallel), validates cross-source
// agreement, and returns the consolidated Resolved struct. Conflict
// errors and missing-required errors both surface as invalid_request
// with the structured fields listed in the message so scripts can
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
	// passes through, but a quoted "$LEAPMUX_CONTROL_TAB_ID" with a
	// trailing newline shouldn't bypass conflict detection.
	in.TabID = strings.TrimSpace(in.TabID)
	in.TabType = strings.TrimSpace(in.TabType)
	in.TileID = strings.TrimSpace(in.TileID)
	in.WorkspaceID = strings.TrimSpace(in.WorkspaceID)
	in.WorkerID = strings.TrimSpace(in.WorkerID)

	// Mark which inputs the user typed on the command line (vs which
	// inherited their value from $LEAPMUX_CONTROL_*_ID env defaults).
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
				"unknown --tab-type value %q; want one of %s",
				in.TabType, contracts.TabTypeAcceptedTokens,
			)
		}
		tabType = parsed
	} else if in.TabType != "" {
		// FixedTabType is set by the command path; if the user also
		// passed --tab-type, the two must agree.
		parsed, ok := ParseTabType(in.TabType)
		if !ok {
			return Resolved{}, invalidArg(
				"unknown --tab-type value %q; want one of %s",
				in.TabType, contracts.TabTypeAcceptedTokens,
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
	// unlocatableTabErr holds a LocateTab miss that has not been judged yet.
	// Written by the one goroutine below and read after Wait, so it needs no
	// mutex for the same reason resolvedTabTypeFromLocate does not.
	//
	// It is deliberately NOT returned from the goroutine: doing that cancels
	// the errgroup's context and kills the sibling derivations, and the whole
	// point is that this miss may turn out not to matter. See
	// ErrTabNotLocatable.
	var unlocatableTabErr error
	var resolvedTabTypeFromLocate leapmuxv1.TabType
	if in.TabID != "" && deps.LocateTab != nil && wantsTabPlacement(need, in) {
		g.Go(func() error {
			matched, ws, tile, worker, err := deps.LocateTab(gctx, tabType, in.TabID)
			// Tolerated for an AMBIENT tab id only. The miss this exists for is
			// the quake panel's: its shell has no CRDT tab, so the id it
			// inherits from the environment cannot be placed, and a command
			// that needs nothing from the lookup must still run.
			//
			// A tab id the user TYPED is a different statement. It says "act on
			// this tab", so a hub that answers not_found or permission_denied
			// has refused the request the user made, and continuing would run
			// the command against whatever worker the ambient environment names
			// -- silently, on the wrong machine, reporting success.
			if errors.Is(err, ErrTabNotLocatable) && !in.ExplicitTabID {
				unlocatableTabErr = fmt.Errorf("locate tab %s: %w", in.TabID, err)
				return nil
			}
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

	// GetWorkspace derives nothing -- it exists so a bogus
	// --workspace-id fails here with a clear "get workspace X:
	// not_found" instead of surfacing later as an opaque CRDT no-op.
	// It is the ONLY workspace-existence check the resolver-driven
	// CRDT commands get: GetMaterialized returns an empty projection
	// rather than an error for a workspace the caller can't see.
	// There is no --worker-id counterpart; see the package doc.
	if in.WorkspaceID != "" && deps.GetWorkspace != nil {
		g.Go(func() error {
			if err := deps.GetWorkspace(gctx, in.WorkspaceID); err != nil {
				return fmt.Errorf("get workspace %s: %w", in.WorkspaceID, err)
			}
			return nil
		})
	}

	if in.TileID != "" && deps.LocateTile != nil {
		g.Go(func() error {
			ws, err := deps.LocateTile(gctx, in.TileID)
			if err != nil {
				return fmt.Errorf("locate tile %s: %w", in.TileID, err)
			}
			agg.put(fieldWorkspaceID, ws, sourceTileID, in.ExplicitTileID)
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
	}

	// Validate required fields are populated. Each missing slot
	// surfaces the names of the flags that satisfy it,
	// so the user sees one error envelope listing every fix.
	if missing := missingRequired(need, out); len(missing) > 0 {
		// A tolerated LocateTab miss becomes fatal HERE, and only here: the
		// fields it would have filled are the ones now reported missing, so
		// reporting the miss explains the gap that "missing required ID(s):
		// workspace_id" only describes. See ErrTabNotLocatable.
		if unlocatableTabErr != nil {
			return Resolved{}, invalidArgWrapping(unlocatableTabErr,
				"%s (needed for: %s)", unlocatableTabErr.Error(), strings.Join(missing, "; "))
		}
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

// invalidArgWrapping is invalidArg that keeps `cause` reachable through
// errors.Is, so a caller (and a test) can ask WHICH failure produced the
// envelope rather than matching on its message text.
func invalidArgWrapping(cause error, format string, args ...any) error {
	return &ResolveError{Code: "invalid_request", Message: fmt.Sprintf(format, args...), cause: cause}
}

// ResolveError is the structured error every resolver failure
// surfaces. The Code is a stable identifier the cmd package emits
// in the JSON envelope; Message is the human-facing detail.
type ResolveError struct {
	Code    string
	Message string
	// cause, when set, is the underlying failure this envelope reports. It is
	// unexported because it is NOT part of the envelope -- the CLI emits Code
	// and Message and nothing else -- but it keeps errors.Is working, so a
	// caller can ask whether a resolve failed because a tab had no hub
	// placement without matching on the message text.
	cause error
}

func (e *ResolveError) Error() string { return e.Message }

func (e *ResolveError) Unwrap() error { return e.cause }

// --- internals: aggregation and conflict detection ---

// field is an enum of derivable fields. We track sources per-field
// so the conflict error can name the contradicting inputs.
type field int

const (
	fieldTabID field = iota
	fieldTileID
	fieldWorkspaceID
	fieldWorkerID
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
	sourceTabID         source = "derived from --tab-id"
	sourceTileID        source = "derived from --tile-id"
)

// sourceEntry pairs a (value, source) record with the priority it
// inherits from the input flag that produced it. `explicit=true`
// means the user typed the flag on the command line; false means it
// inherited from a $LEAPMUX_CONTROL_*_ID env default.
type sourceEntry struct {
	src      source
	explicit bool
}

type aggregator struct {
	mu      sync.Mutex
	sources map[field]map[string]sourceEntry // field -> value -> first reporter
}

func newAggregator() *aggregator {
	return &aggregator{sources: make(map[field]map[string]sourceEntry, 4)}
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
//   - no explicit input specifies it and two or more env-derived sources
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
// that satisfy them. Empty when every required field
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
	if need.TabID && r.TabID == "" {
		out = append(out, "--tab-id (with --tab-type, or via LEAPMUX_CONTROL_TAB_ID + LEAPMUX_CONTROL_TAB_TYPE)")
	}
	return out
}
