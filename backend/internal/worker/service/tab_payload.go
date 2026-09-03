package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/pathutil"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/generated/db"
)

// TabPayloadStore is the worker-local store of (user_id, tab_id) → TabPayload:
// everything a FILE or IMAGE tab needs to resolve what it shows. The hub never
// sees these rows; clients fetch them via WatchWorkerPrivateEvents (which
// carries them over the existing E2EE channel) or one-shot GetTabPayload.
//
// One store for both kinds. A file path and an image's message reference are
// the same class of secret, and they are written, replayed, read and deleted at
// exactly the same points in a tab's life. The kind-specific parts are the
// payload oneof and the directory the worktree link follows; everything else
// here is shared, and a second store would have to restate all of it.
type TabPayloadStore struct {
	q      *db.Queries
	events *PrivateEventsBus
}

// NewTabPayloadStore returns a store bound to the worker's DB queries and the
// bus where snapshot/live events get published.
func NewTabPayloadStore(q *db.Queries, events *PrivateEventsBus) *TabPayloadStore {
	return &TabPayloadStore{q: q, events: events}
}

// Register persists a (user_id, tab_id, tab_type, payload, working_dir) row and
// broadcasts TabPayloadRegistered on the owner's private-event stream.
//
// Every error a bad request produces wraps ErrInvalidTabPayload; a marshal or a
// database failure does not. The two are separate answers on the wire, and only
// the second is worth a retry.
func (s *TabPayloadStore) Register(ctx context.Context, p RegisterTabPayloadParams) error {
	owner, ok := userid.New(p.UserID)
	if !ok || p.TabID == "" || p.Payload == nil {
		return fmt.Errorf("register tab payload: required field empty: %w", ErrInvalidTabPayload)
	}
	tabType, err := tabPayloadType(p.Payload)
	if err != nil {
		return fmt.Errorf("register tab payload: %w: %w", ErrInvalidTabPayload, err)
	}
	stored := proto.Clone(p.Payload).(*leapmuxv1.TabPayload)
	workingDir, err := resolveTabPayloadWorkingDir(stored)
	if err != nil {
		return fmt.Errorf("register tab payload: %w: %w", ErrInvalidTabPayload, err)
	}
	stored.WorkingDir = workingDir
	blob, err := proto.Marshal(stored)
	if err != nil {
		return fmt.Errorf("marshal tab payload: %w", err)
	}
	if err := s.q.UpsertWorkerTabPayload(ctx, db.UpsertWorkerTabPayloadParams{
		UserID:     owner.String(),
		TabID:      p.TabID,
		TabType:    int64(tabType),
		Payload:    blob,
		WorkingDir: workingDir,
	}); err != nil {
		return fmt.Errorf("upsert worker_tab_payload: %w", err)
	}
	// Link the tab to its worktree (if any) BEFORE publishing the
	// TabPayloadRegistered event: consumers that react to the event
	// (orphan reconciler, sibling close paths calling CountWorktreeTabs)
	// would otherwise race the link insert and observe a temporarily-
	// unlinked tab. CountWorktreeTabs underreports by one, the last-tab
	// close path decides "no siblings remain", and `git worktree remove`
	// runs while this tab is still open — the editor then ENOENTs on a dir
	// that was just rm-rf'd. Mirror the agent/terminal worktree-linkage
	// path: probe the directory once via `git rev-parse`, then exact-match
	// the canonical top-level against the tracked worktrees. Best-effort: a
	// dir outside any tracked worktree leaves the tab unbound, matching
	// today's behavior for non-worktree files.
	s.linkTabToWorktree(ctx, owner.String(), tabPayloadLinkDir(stored), tabType, p.TabID)
	if s.events != nil {
		s.events.PublishTabPayloadRegistered(owner, p.TabID, stored)
	}
	return nil
}

// tabPayloadType maps a payload's oneof case to the TabType it belongs to, and
// validates that case's own required fields.
//
// The type is stored in its own column rather than re-derived per read: SQL
// needs it (tab_locations projects it), and a row whose payload a future binary
// cannot parse still reports which kind of tab it is.
func tabPayloadType(payload *leapmuxv1.TabPayload) (leapmuxv1.TabType, error) {
	switch kind := payload.GetKind().(type) {
	case *leapmuxv1.TabPayload_File:
		path := kind.File.GetFilePath()
		if path == "" {
			return leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED, errors.New("file_path must not be empty")
		}
		// Absolute, and refused here rather than normalized: a relative path
		// has no meaningful base on this side. The worker's own CWD is the only
		// one available and it is never the client's, so `filepath.Dir("a.txt")`
		// would store "." and hand every reader below -- linkTabToWorktree,
		// getTabWorkingDir, the branch-sibling scan -- a `git -C .` that answers
		// for whatever repo the worker process happens to sit in. Both callers
		// already promise absolute (`leapmux control tab open --path` documents
		// it, and the UI sends a tree path), so this refuses a caller that broke
		// its contract instead of silently binding the tab to the wrong
		// repository.
		if !filepath.IsAbs(path) {
			return leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED, fmt.Errorf("file_path must be absolute, got %q", path)
		}
		return leapmuxv1.TabType_TAB_TYPE_FILE, nil
	case *leapmuxv1.TabPayload_Image:
		// agent_id scopes the GetAgentMessage the client will make; without it
		// the tab can never resolve, so a row that cannot resolve is refused at
		// the door rather than stored and puzzled over later.
		if kind.Image.GetAgentId() == "" {
			return leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED, errors.New("image agent_id must not be empty")
		}
		// Seqs are 1-based; 0 is the sentinel for an unpersisted optimistic row,
		// which no worker message ever has.
		if kind.Image.GetSeq() <= 0 {
			return leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED, fmt.Errorf("image seq must be positive, got %d", kind.Image.GetSeq())
		}
		if kind.Image.GetImageIndex() < 0 {
			return leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED, fmt.Errorf("image index must not be negative, got %d", kind.Image.GetImageIndex())
		}
		return leapmuxv1.TabType_TAB_TYPE_IMAGE, nil
	default:
		return leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED, errors.New("payload specifies no tab kind")
	}
}

// tabPayloadLinkDir is the directory whose worktree this tab ref-counts.
//
// It differs by kind because the question does. For a FILE tab the link is a
// ref-count on a directory that can be deleted -- it decides whether `git
// worktree remove` may rm-rf a tree while an editor is still mounted inside it
// -- so only PHYSICAL CONTAINMENT answers it, and the file's own directory is
// the answer. An IMAGE tab holds no file open; it shows bytes that already
// arrived over the wire. What it does have is the branch group it was opened
// in, so it ref-counts that, which is what keeps the last-tab-close prompt from
// offering to delete a worktree that still has a visible tab on it.
func tabPayloadLinkDir(payload *leapmuxv1.TabPayload) string {
	if file := payload.GetFile(); file != nil {
		return filepath.Dir(file.GetFilePath())
	}
	return payload.GetWorkingDir()
}

// resolveTabPayloadWorkingDir picks the working dir a tab is stored with: the
// originating tab's, or -- for a FILE tab only -- the file's own directory when
// the caller has no originating tab to give. The UI always gives one; `leapmux
// control tab open --type=file` gives the spawning tab's dir when it runs inside
// one ($LEAPMUX_CONTROL_WORKING_DIR) and nothing when run from a plain shell,
// which is the case the fallback exists for. An IMAGE tab has no file to fall
// back to, so an unspecified one stays blank and simply joins no branch group.
//
// Normalizing HERE, once at write time, is what lets every reader --
// getTabWorkingDir, linkTabToWorktree, the branch-sibling scan -- just read the
// column. A fallback evaluated per read is the same rule stated in three places,
// and three places is where they drift: the close path would then be able to
// answer for a different directory than the link that decides whether that close
// removes a worktree.
//
// It is literally the same normalizer agents.working_dir and terminals.working_dir
// go through at their own write points (OpenAgent, OpenTerminal) -- only the
// fallback differs, so that is the parameter. Without it a `--working-dir
// '~/proj'` -- which the CLI forwards raw -- is stored literally, and `git -C
// '~/proj'` cannot resolve it, dropping the tab straight back into the "not
// readable as a git repository" degraded close this column exists to eliminate.
// Three writers of the same fact have to agree on what it means, and sharing the
// function is what makes them agree rather than a comment asking them to.
//
// The home directory comes from the process rather than from a Service field,
// because a TabPayloadStore holds no Service. That is the same home expandTilde
// resolved before this normalizer took an explicit one, so a `~/proj` still
// expands exactly as it did.
func resolveTabPayloadWorkingDir(payload *leapmuxv1.TabPayload) (string, error) {
	raw := payload.GetWorkingDir()
	fallback := ""
	if file := payload.GetFile(); file != nil {
		fallback = filepath.Dir(file.GetFilePath())
	}
	// Nothing stated and nothing to fall back to: an IMAGE tab opened before
	// its originating tab's working dir was known. Store the blank rather than
	// refusing -- the tab is perfectly resolvable without one, it simply joins
	// no branch group, and refusing would make the open fail intermittently on
	// a field that decides only where the tab is FILED. A FILE tab never
	// reaches this: its fallback is the file's own directory, and the file path
	// is required and absolute.
	if strings.TrimSpace(raw) == "" && fallback == "" {
		return "", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return normalizeWorkingDir(raw, fallback, home)
}

// linkTabToWorktree associates a payload-backed tab with the worktree the
// directory it is given sits in, if one is tracked. Failure here is non-fatal —
// the tab is still registered, it just won't count toward sibling-tab checks in
// the last-tab close path.
//
// See tabPayloadLinkDir for which directory each kind passes and why it is not
// always the tab's working_dir. working_dir is the tab's git IDENTITY: which
// branch group it renders in, which branch a push targets — inherited from the
// tab it was opened from, because that is the context the user opened it in. For
// a FILE tab the link answers a different question, and keying it on working_dir
// instead silently unlinks every file opened from one checkout into another — an
// agent in the main repo opening a file inside a linked worktree — and
// CountWorktreeTabs then underreports, which is exactly the "editor ENOENTs on a
// dir that was just rm-rf'd" hazard Register's comment above says this linkage
// exists to prevent.
//
// userID is stamped onto the worktree_tabs row so worktree_tab_liveness can
// scope its payload-tab join by user: a FILE or IMAGE tab id is only unique
// within a user (worker_tab_payloads is keyed by (user_id, tab_id)), so without
// it a multi-user worker could match a different user's live tab and mark a
// strand live.
func (s *TabPayloadStore) linkTabToWorktree(ctx context.Context, userID, linkDir string, tabType leapmuxv1.TabType, tabID string) {
	if linkDir == "" {
		return
	}
	info, err := queryGitPathInfo(ctx, linkDir)
	if err != nil || info == nil || !info.IsWorktree {
		return
	}
	wt, err := s.q.GetWorktreeByPath(ctx, pathutil.Canonicalize(info.TopLevel))
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("link tab to worktree: lookup failed",
				"tab_id", tabID, "worktree_path", info.TopLevel, "error", err)
		}
		return
	}
	if err := s.q.AddWorktreeTab(ctx, db.AddWorktreeTabParams{
		WorktreeID: wt.ID,
		TabType:    tabType,
		TabID:      tabID,
		UserID:     userID,
	}); err != nil {
		slog.Warn("link tab to worktree: insert failed",
			"tab_id", tabID, "worktree_id", wt.ID, "error", err)
	}
}

// BackfillWorktreeLinks links any already-open payload-backed tabs that live
// under worktreePath to a freshly-created worktree row. Such tabs only acquire
// their worktree_tabs link at registration time, and only when the worktree row
// already exists (see linkTabToWorktree). A worktree adopted AFTER a file under
// it was opened — e.g. created with `git worktree add` outside LeapMux, opened
// as a FILE tab first, then an agent/terminal opens inside it and creates the
// row — would otherwise leave that tab unlinked. It then wouldn't count toward
// the worktree's ref-count, so a sibling AGENT/TERMINAL close could
// `git worktree remove` the dir while the editor is still mounted.
//
// Lexically pre-filter to tabs whose link dir sits under worktreePath so we
// don't probe every tab on the worker, then reuse linkTabToWorktree, which
// re-probes git and links only dirs that genuinely resolve to a tracked worktree
// (so a path in a nested submodule/worktree isn't mis-linked). Best-effort:
// errors are logged, never surfaced — an un-backfilled link degrades to today's
// behavior (the tab just doesn't ref-count).
func (s *TabPayloadStore) BackfillWorktreeLinks(ctx context.Context, worktreePath string) {
	rows, err := s.q.ListAllWorkerTabPayloads(ctx)
	if err != nil {
		slog.Warn("backfill worktree links: list tab payloads", "worktree_path", worktreePath, "error", err)
		return
	}
	canonicalWorktree := pathutil.Canonicalize(worktreePath)
	for _, row := range rows {
		payload, err := decodeTabPayload(row.Payload)
		if err != nil {
			slog.Warn("backfill worktree links: decode payload", "tab_id", row.TabID, "error", err)
			continue
		}
		// The tab's LINK dir on both the filter and the probe, matching what
		// Register links on -- for a FILE tab that is the file's own directory,
		// because this is a ref-count on a deletable directory and follows where
		// the file physically is, not the working_dir the tab inherited from
		// whatever opened it (see tabPayloadLinkDir). Canonicalize both sides:
		// the row stores the raw client path, which may differ from the
		// symlink-resolved worktree path.
		linkDir := tabPayloadLinkDir(payload)
		if linkDir == "" || !pathutil.HasPathPrefix(pathutil.Canonicalize(linkDir), canonicalWorktree) {
			continue
		}
		s.linkTabToWorktree(ctx, row.UserID, linkDir, leapmuxv1.TabType(row.TabType), row.TabID)
	}
}

// Get returns the stored payload for a tab, or ErrTabPayloadNotFound if absent.
//
// The whole payload comes back at once rather than through a path-only read plus
// a second working-dir read: it is one fact about one tab, and the two callers
// (the GetTabPayload handler answering a client, getTabWorkingDir answering the
// git paths) would otherwise be able to observe halves of it from different rows
// across a concurrent re-register.
//
// A blank userID is refused rather than bound: worker_tab_payloads is keyed by
// (user_id, tab_id), so the owner is half the identity of the row being read.
// Register never writes a blank owner, so no legitimate row can be reached with
// one -- but binding it would turn a caller that lost track of the tenant into a
// silent lookup against whatever rows a future blank-owner write left behind.
// Mirrors Register's own required-field guard.
//
// The refusal goes through userid.New rather than a hand-written `== ""` so it
// is the SAME guard the hub dialects bind their owner columns through -- the
// repo-wide store-bind rule in internal/audit recognises a minted-or-refused id
// and nothing else. Callers hold the owner as a plain string here (the handler
// projects its userid.UserID, the reconciler reads a column), so New is the
// spelling of that guard on this side; owner.String() is the unwrap.
//
// The tabID check folds into the same disjunct: the audit rule walks `||`
// operands, so one refusal reads as one statement. (It did not always -- the
// rule once matched a bare `!ok` only, and this guard was split in two purely
// to stay visible to it.)
func (s *TabPayloadStore) Get(ctx context.Context, userID, tabID string) (*leapmuxv1.TabPayload, error) {
	owner, ok := userid.New(userID)
	if !ok || tabID == "" {
		return nil, fmt.Errorf("get tab payload: required field empty")
	}
	row, err := s.q.GetWorkerTabPayload(ctx, db.GetWorkerTabPayloadParams{UserID: owner.String(), TabID: tabID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTabPayloadNotFound
		}
		return nil, err
	}
	return decodeTabPayload(row.Payload)
}

// TabTypeOf reports which kind of tab a stored payload belongs to, without
// decoding the payload itself. The read still fetches the blob, because
// GetWorkerTabPayload is a `SELECT *`; what the column saves is the
// proto.Unmarshal, and the ability to answer for a row this binary cannot
// parse.
//
// Returns ErrTabPayloadNotFound when no row exists, so a caller can tell "this
// tab is an IMAGE tab" from "this tab is already closed".
func (s *TabPayloadStore) TabTypeOf(ctx context.Context, userID, tabID string) (leapmuxv1.TabType, error) {
	owner, ok := userid.New(userID)
	if !ok || tabID == "" {
		return leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED, fmt.Errorf("tab type of: required field empty")
	}
	row, err := s.q.GetWorkerTabPayload(ctx, db.GetWorkerTabPayloadParams{UserID: owner.String(), TabID: tabID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED, ErrTabPayloadNotFound
		}
		return leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED, err
	}
	return leapmuxv1.TabType(row.TabType), nil
}

// decodeTabPayload unmarshals a stored blob. A row that fails to parse is a
// corrupt row, not an absent one, so it surfaces as an error rather than
// ErrTabPayloadNotFound: a client told "no such tab" would drop the tab, while
// an error leaves it in place for the next read.
func decodeTabPayload(blob []byte) (*leapmuxv1.TabPayload, error) {
	payload := &leapmuxv1.TabPayload{}
	if err := proto.Unmarshal(blob, payload); err != nil {
		return nil, fmt.Errorf("unmarshal tab payload: %w", err)
	}
	return payload, nil
}

// RevokeRow deletes the worker_tab_payloads row and broadcasts
// TabPayloadRevoked. It is the payload-backed analog of the per-type DB close
// performed by Queries.CloseAgent / Queries.CloseTerminal — the
// worktree-association drop is intentionally NOT done here so the
// RevokeTabPayload handler can drive the unified closeTabCommon flow that
// handles the worktree-tab link (and optional `git worktree remove`)
// consistently across AGENT, TERMINAL, FILE and IMAGE.
//
// Returns ErrTabPayloadNotFound when no row exists. A blank userID is refused
// for the same reason as Get -- and here the stakes are higher, since the bound
// predicate drives a DELETE.
func (s *TabPayloadStore) RevokeRow(ctx context.Context, userID, tabID string) error {
	owner, ok := userid.New(userID)
	if !ok || tabID == "" {
		return fmt.Errorf("revoke tab payload: required field empty")
	}
	// One statement, and its affected-row count IS the existence answer. This used
	// to SELECT the row first, which was load-bearing only while the revoke event
	// carried the row's workspace_id; now it would be a second round trip whose
	// only job is a check the DELETE already reports -- and a TOCTOU window, since
	// a concurrent revoke between the two made this a no-op that still reported
	// success and published a duplicate TabPayloadRevoked.
	res, err := s.q.DeleteWorkerTabPayload(ctx, db.DeleteWorkerTabPayloadParams{UserID: owner.String(), TabID: tabID})
	if err != nil {
		return fmt.Errorf("delete worker_tab_payload: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete worker_tab_payload: %w", err)
	}
	if affected == 0 {
		return ErrTabPayloadNotFound
	}
	if s.events != nil {
		s.events.PublishTabPayloadRevoked(owner, tabID)
	}
	return nil
}

// SnapshotForOwner returns the TabPayloadRegistered events the private-event
// subscribe path replays before going live, so a late-joining client always sees
// the current payload set.
//
// The owner is the whole predicate, and it is bound in SQL rather than filtered
// in Go: every sibling (Get / RevokeRow, the worktree_tabs deletes, and
// OrphanReconciler.reconcileTabPayloads) binds it, because the
// (user_id, tab_id) key exists precisely for it -- a FILE or IMAGE tab id is
// unique only within a user (see the worker_tab_payloads / worktree_tabs DDL).
func (s *TabPayloadStore) SnapshotForOwner(ctx context.Context, owner userid.UserID) ([]*leapmuxv1.WorkerPrivateEvent, error) {
	ownerID, ok := userid.OwnerFilter(owner)
	if !ok {
		// An unminted owner reaches no row. Answer an empty snapshot rather
		// than a whole-table read the caller would then have to filter.
		return nil, nil
	}
	rows, err := s.q.ListWorkerTabPayloadsByUser(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	out := make([]*leapmuxv1.WorkerPrivateEvent, 0, len(rows))
	for _, r := range rows {
		payload, err := decodeTabPayload(r.Payload)
		if err != nil {
			// One unreadable row must not cost the client every other tab's
			// payload, so it is skipped and the rest replay.
			slog.Warn("tab payload snapshot: decode failed", "tab_id", r.TabID, "error", err)
			continue
		}
		out = append(out, &leapmuxv1.WorkerPrivateEvent{
			Event: &leapmuxv1.WorkerPrivateEvent_TabPayloadRegistered{
				TabPayloadRegistered: &leapmuxv1.TabPayloadRegistered{
					TabId:   r.TabID,
					Payload: payload,
				},
			},
		})
	}
	return out, nil
}

// RegisterTabPayloadParams is the input shape for Register. Payload.WorkingDir
// is optional; see resolveTabPayloadWorkingDir for what an empty one resolves
// to.
type RegisterTabPayloadParams struct {
	UserID  string
	TabID   string
	Payload *leapmuxv1.TabPayload
}

// ErrTabPayloadNotFound is returned when the requested tab has no row in
// worker_tab_payloads.
var ErrTabPayloadNotFound = errors.New("tab_payload: not found")

// ErrInvalidTabPayload marks a Register refusal the CALLER caused: a missing
// required field, a payload whose oneof states no kind, or a working directory
// that is not absolute. Every other Register failure is a worker-side fault --
// a marshal error or a failed upsert -- and the handler must not report those
// as a bad request, because the caller has nothing to correct and a retry is
// exactly what it should do.
var ErrInvalidTabPayload = errors.New("tab_payload: invalid")
