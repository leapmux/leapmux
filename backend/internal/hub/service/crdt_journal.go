package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
	"google.golang.org/protobuf/proto"
)

// errBlankTenant is returned by every journal method whose crdt-side
// `userID string` will not mint. This adapter is the boundary at which the
// store's typed owner params are minted: crdt keeps its actor ids string-keyed
// (see internal/audit/typing.go), so the conversion happens here, once per
// call, and refuses rather than writing a row whose owner no ownership
// predicate could ever bind.
//
// Nothing reaches it today. crdt.Registry.Get refuses a blank tenancy key, so a
// Manager's userID is non-empty by construction, and Manager.requireOwnState
// refuses a state payload naming any other tenant, so the payload-derived id
// CompactBatch mints agrees with it. That is precisely why this is an error
// rather than a silent no-op: reaching it means one of those invariants broke,
// and a silent skip would hide that.
var errBlankTenant = errors.New("crdt journal: blank user id; refusing to write a row no ownership predicate could reach")

// crdtJournal adapts store.Store to the crdt.Journal contract. It owns
// the per-batch transaction boundary so the manager's commit step lands
// AppendBatch + InsertRecentBatchID + ApplyDiff atomically.
type crdtJournal struct {
	store store.Store
}

// NewCRDTJournal returns a Journal backed by the supplied store.
func NewCRDTJournal(st store.Store) crdt.Journal {
	return &crdtJournal{store: st}
}

// mintTenant binds a journal method's untyped tenancy key into the owner every
// store call beneath it takes, refusing a blank one with errBlankTenant.
//
// The refusal is the substance here, not the conversion. A blank key is not a
// benign "no tenant": it unwraps to "" and MATCHES every blank-owner row, so
// each entry point has to reject it before the first query rather than let the
// store answer (see userid.OwnerFilter). Naming that rule once keeps the five
// call sites from drifting on which error they raise, though it cannot make
// them remember to call it -- a new journal method still has to, which is why
// the audit in internal/audit, not this helper, is what enforces the guard.
//
// Callers differ only in how many zero values they pair with the error, so this
// returns the error rather than an ok bool.
func mintTenant(userID string) (userid.UserID, error) {
	owner, ok := userid.New(userID)
	if !ok {
		return userid.UserID{}, errBlankTenant
	}
	return owner, nil
}

func (j *crdtJournal) LoadState(ctx context.Context, userID string) (*leapmuxv1.UserCrdtState, []*leapmuxv1.OpBatch, error) {
	owner, err := mintTenant(userID)
	if err != nil {
		return nil, nil, err
	}
	row, err := j.store.UserState().Get(ctx, owner)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, nil, fmt.Errorf("get user_state: %w", err)
	}
	var state *leapmuxv1.UserCrdtState
	if err == nil && row != nil {
		state = &leapmuxv1.UserCrdtState{}
		if uerr := proto.Unmarshal(row.StatePayload, state); uerr != nil {
			return nil, nil, fmt.Errorf("unmarshal state_payload: %w", uerr)
		}
	}
	var watermark *leapmuxv1.HLC
	if state != nil {
		watermark = state.GetCompactionWatermark()
	}
	// The boot policy (no budgets, no transitions, corrupt = fatal) is named by
	// scanForBoot rather than spelled as trailing booleans here; see its doc and
	// journalScan for why each half is what it is.
	tail, err := j.scanForBoot(ctx, owner, watermark)
	if err != nil {
		return nil, nil, err
	}
	batches := make([]*leapmuxv1.OpBatch, len(tail))
	for i, rb := range tail {
		batches[i] = rb.Batch
	}
	return state, batches, nil
}

// ListBatchesAfter is the resume entry point: it mints the tenant and delegates
// to the shared paged scan with the caller's maxOps/maxBytes budgets and the
// optional register-time until high-water. maxOps or maxBytes <= 0 means that
// ceiling is disabled; until nil means unbounded (LoadState). Each ResumeBatch
// pairs the batch with its persisted transitions so buildResumeDelta can
// replay them. A corrupt row stops the scan and returns crdt.ErrResumeCorrupt
// plus the CorruptRow list for logging; the caller FALLBACKs to a full snapshot
// (see scan's `recoverable` contract).
func (j *crdtJournal) ListBatchesAfter(ctx context.Context, userID string, after, until *leapmuxv1.HLC, maxOps, maxBytes int) ([]crdt.ResumeBatch, []crdt.CorruptRow, error) {
	owner, err := mintTenant(userID)
	if err != nil {
		return nil, nil, err
	}
	return j.scanForResume(ctx, owner, after, until, maxOps, maxBytes)
}

// wrapCorruptRow wraps a per-row decode failure (batch_payload or
// transitions_payload) in the sentinel that matches THIS scan's verdict:
// crdt.ErrResumeCorrupt when the caller can fall back to a full snapshot,
// crdt.ErrBootJournalCorrupt when it cannot.
//
// The sentinel is the verdict, so it has to follow `recoverable` rather than
// being fixed at the wrap site. buildResumeDelta resolves ErrResumeCorrupt via
// errors.Is to route a corrupt row to FALLBACK instead of failing the
// connection, so the `%w` verb is load-bearing: `%v` / `%s` would make
// errors.Is return false and fail the connect outright. Handing that same
// sentinel to the boot path labelled a FATAL error as recoverable — see
// crdt.ErrBootJournalCorrupt.
//
// Centralized so both decode sites share one wrap form and the errors.Is
// contract has one home (and one test).
func wrapCorruptRow(recoverable bool, what, batchID string, cause error) error {
	sentinel := crdt.ErrBootJournalCorrupt
	if recoverable {
		sentinel = crdt.ErrResumeCorrupt
	}
	return fmt.Errorf("%w: %s %s: %v", sentinel, what, batchID, cause)
}

// journalScan names one paged scan's parameters.
//
// A struct rather than eight positionals because the last two were BOOLEANS OF
// THE SAME TYPE encoding two unrelated policies -- `j.listBatchesAfter(ctx,
// owner, watermark, nil, 0, 0, false, false)` said nothing at the call site
// about which corruption policy it had picked, and transposing them silently
// swapped "fatal at boot" for "recoverable at resume". The same reasoning
// already produced crdt.resumeRequest one layer up.
type journalScan struct {
	owner userid.UserID
	// cursor is the exclusive lower bound; nil scans from the beginning.
	cursor *leapmuxv1.HLC
	// until is the register-time high-water bound; nil is unbounded.
	until *leapmuxv1.HLC
	// maxOps / maxBytes cap the scan. <= 0 disables that ceiling.
	maxOps, maxBytes int
	// mode names WHICH scan this is, and both per-policy answers derive from it.
	//
	// It replaces a `decodeTransitions bool` + `recoverable bool` pair whose
	// product had four states and only two legal ones. That pair was introduced
	// to stop two same-typed booleans being transposed positionally -- but a
	// struct only stops the transposition at the CALL SITE; it still let an
	// illegal combination be constructed, and one already was: a test built
	// `{decodeTransitions: true}` (leaving recoverable false), and the
	// transitions arms of `scan` return crdt.ErrResumeCorrupt ignoring
	// `recoverable` entirely -- so that fourth state's behaviour contradicted the
	// field's own documentation. One enum makes both illegal pairings
	// unrepresentable instead of merely unconstructed.
	mode scanMode
}

// scanMode is which of the two journal scans is running. Both answers a scan
// needs -- whether to decode transitions, and what a corrupt row means -- are
// properties OF the scan, so they are derived here rather than passed
// independently.
type scanMode int

const (
	// scanBoot rebuilds state by replaying ops (Bootstrap). No budgets, no
	// transitions, and a corrupt row is fatal: there is no snapshot to fall back
	// to, because the snapshot is what this scan is building.
	scanBoot scanMode = iota
	// scanResume reads a post-cursor tail for a delta-resume. Budgets apply,
	// transitions are decoded (only resume consumes them), and a corrupt row
	// degrades that one catch-up to a full snapshot rather than aborting the
	// reconnect.
	scanResume
)

// decodesTransitions reports whether this scan unmarshals transitions_payload.
// Only the resume path consumes transitions, so boot skips the work.
func (m scanMode) decodesTransitions() bool { return m == scanResume }

// corruptRecoverable reports what a per-row decode failure MEANS to the caller.
// BOTH modes stop the scan at the bad row -- nothing is skipped and nothing is
// quarantined; they differ only in what the caller can do next.
func (m scanMode) corruptRecoverable() bool { return m == scanResume }

// scanForBoot reads the whole post-watermark tail for Bootstrap: no budgets, no
// transitions, and a corrupt row is FATAL because Bootstrap rebuilds state by
// replaying these ops and has nothing to fall back to.
func (j *crdtJournal) scanForBoot(ctx context.Context, owner userid.UserID, watermark *leapmuxv1.HLC) ([]crdt.ResumeBatch, error) {
	tail, _, err := j.scan(ctx, journalScan{owner: owner, cursor: watermark, mode: scanBoot})
	return tail, err
}

// scanForResume reads the post-cursor tail for a delta-resume: budgets apply,
// transitions are decoded (only resume consumes them), and a corrupt row
// degrades one batch's catch-up rather than aborting the reconnect.
func (j *crdtJournal) scanForResume(ctx context.Context, owner userid.UserID, after, until *leapmuxv1.HLC, maxOps, maxBytes int) ([]crdt.ResumeBatch, []crdt.CorruptRow, error) {
	return j.scan(ctx, journalScan{
		owner: owner, cursor: after, until: until,
		maxOps: maxOps, maxBytes: maxBytes,
		mode: scanResume,
	})
}

// logCorruptBootRow reports a boot-fatal journal row at ERROR with everything
// an operator needs to act, because nothing in the system can clear it on its
// own.
//
// The row is above compaction_physical_ms, so the retention sweep's join
// excludes it by construction, and DeleteThrough needs a manager this user
// cannot bootstrap. Without this line the only symptom is an HTTP 500 on
// /ws/userevents and a failing SubmitOps, repeating forever with no indication
// of which row or which user.
func (p journalScan) logCorruptBootRow(batchID, field string, cause error) {
	slog.Error("crdt boot journal row is corrupt; this user cannot bootstrap until the row is removed",
		"user_id", p.owner.String(),
		"batch_id", batchID,
		"field", field,
		"err", cause,
		"remedy", "delete the named user_op_batches row after capturing it for diagnosis")
}

// scan pages through the journal forward by HLC tuple, starting strictly after
// `p.cursor`. p.until, when non-nil, stops the scan once a row's first-op HLC is
// > until (register-time high-water). p.maxOps / p.maxBytes <= 0 disables that
// budget; otherwise the scan aborts with ErrDeltaTooLarge as soon as the
// accumulated op count OR payload bytes would exceed it.
// scanBoot skips the Unmarshal of transitions_payload entirely.
//
// p.mode.corruptRecoverable() governs what a per-row decode failure means to the caller.
// Either way the scan STOPS at the bad row -- a delta that silently omits a
// committed batch is the one outcome neither caller can tolerate, because the
// frames after it would still advance the client's watermark past the hole and
// no later resume would ever re-request it.
//
//   - scanResume: return the rows read so far, the CorruptRow
//     list (for logging), and crdt.ErrResumeCorrupt. buildResumeDelta treats
//     that exactly like ErrDeltaTooLarge and FALLBACKs to a full snapshot,
//     which is always complete.
//   - scanBoot: fatal. Bootstrap replays ops to rebuild state,
//     so there is no snapshot to fall back to.
//
// This applies to a corrupt transitions_payload too, NOT just batch_payload:
// empty transitions make visibilityFor see {Pre:"", Post:""} for every op, so
// filterVisibleOps drops the entire batch and emitCatchUpFrames emits nothing
// for it -- indistinguishable, on the wire, from the batch never having existed.
func (j *crdtJournal) scan(ctx context.Context, p journalScan) ([]crdt.ResumeBatch, []crdt.CorruptRow, error) {
	owner, cursor, until := p.owner, p.cursor, p.until
	maxOps, maxBytes := p.maxOps, p.maxBytes
	decodeTransitions, recoverable := p.mode.decodesTransitions(), p.mode.corruptRecoverable()
	out := []crdt.ResumeBatch{}
	var corrupt []crdt.CorruptRow
	ops := 0
	bytes := 0
	cur := cursor
	for {
		rows, err := j.store.UserOpBatches().ListAfter(ctx, store.ListUserOpBatchesAfterParams{
			UserID:            owner,
			AfterPhysicalMs:   cur.GetPhysical(),
			AfterLogical:      cur.GetLogical(),
			AfterOriginClient: cur.GetClientId(),
			Limit:             store.CRDTBatchPageLimit,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("list user_op_batches after watermark: %w", err)
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			rowHLC := &leapmuxv1.HLC{
				Physical: r.PhysicalMs,
				Logical:  r.Logical,
				ClientId: r.OriginClient,
			}
			// Register-time high-water: batches with first-op HLC > until were
			// committed after the subscriber registered and are delivered via
			// live broadcast — do not also ship them in the delta.
			if until != nil && crdt.HLCCmp(rowHLC, until) > 0 {
				return out, corrupt, nil
			}
			rowBytes := len(r.BatchPayload) + len(r.TransitionsPayload)
			if maxBytes > 0 && bytes+rowBytes > maxBytes {
				return out, corrupt, crdt.ErrDeltaTooLarge
			}
			batch := &leapmuxv1.OpBatch{}
			if uerr := proto.Unmarshal(r.BatchPayload, batch); uerr != nil {
				corruptCause := wrapCorruptRow(p.mode.corruptRecoverable(), "unmarshal user_op_batch", r.BatchID, uerr)
				if !recoverable {
					// Boot path: fatal — Bootstrap replays ops, so skipping a
					// batch would rebuild a diverged state and there is no
					// snapshot to fall back to. See logCorruptBootRow for why
					// this is unrecoverable WITHOUT operator action.
					p.logCorruptBootRow(r.BatchID, "batch_payload", corruptCause)
					return nil, nil, corruptCause
				}
				// Resume path: STOP and signal FALLBACK. Continuing past the row
				// would ship a delta missing this batch entirely while later
				// batches still advance the client's max_hlc past it — the
				// client would persist a watermark above ops it never received
				// and never re-request them.
				corrupt = append(corrupt, crdt.CorruptRow{
					BatchID: r.BatchID,
					Field:   "batch_payload",
					Cause:   corruptCause,
				})
				return out, corrupt, crdt.ErrResumeCorrupt
			}
			// Decoding cleanly is not a completeness witness. A batch_payload
			// truncated at a repeated-`ops` element boundary -- including to
			// zero bytes -- unmarshals WITHOUT error into a short OpBatch, and
			// nothing downstream can tell that from a batch that legitimately
			// carried fewer ops: filterVisibleOps just emits the survivors, and
			// the BatchEnd still advances the client past the whole row, which
			// ListAfter's strictly-greater cursor then guarantees is never
			// re-sent. op_count is the independent witness that closes it --
			// written from len(ops) at commit and DB-constrained > 0 -- and this
			// is the batch_payload half of the same gate MissingTransitionOp
			// applies to transitions_payload below.
			//
			// TWO PER-COLUMN WITNESSES, NOT ONE ROW DIGEST -- a deliberate
			// choice, so it does not get re-litigated. A commit-time hash over
			// the marshalled bytes would cover every payload column at once and
			// also catch count-PRESERVING corruption, which neither witness
			// does. It was weighed and declined: it costs a column across
			// sqlite/postgres/mysql plus sqlc and the shared store suite, and
			// per-row hashing on the very resume read path #267 exists to speed
			// up -- to catch bit-rot inside a decoded field, which is what the
			// database's own page checksums are for. It also would NOT replace
			// MissingTransitionOp, since a commit-time incompleteness hashes
			// clean. Revisit only if real corruption is ever observed that these
			// two miss.
			nOps := len(batch.GetOps())
			if r.OpCount != int64(nOps) {
				corruptCause := wrapCorruptRow(p.mode.corruptRecoverable(), "incomplete batch_payload", r.BatchID,
					fmt.Errorf("op_count=%d but batch_payload decoded %d ops", r.OpCount, nOps))
				if !recoverable {
					// Boot path: fatal, for the same reason a failed unmarshal
					// is. Bootstrap replays these ops into the authoritative
					// state and the next maybeCompact persists the result, so a
					// short batch would bake the divergence in permanently.
					//
					// And fatal here is PERMANENT, not transient: this row sits
					// strictly ABOVE compaction_physical_ms, which is exactly
					// what the retention sweep refuses to delete below, and the
					// only other delete needs a bootstrapped manager -- which is
					// what cannot start. So every reconnect and every SubmitOps
					// for this user fails identically until an operator removes
					// the row. Nothing here can self-heal it (skipping would be
					// the silent divergence the gate exists to catch), so the
					// least this owes is a log that names the row and says so.
					p.logCorruptBootRow(r.BatchID, "batch_payload", corruptCause)
					return nil, nil, corruptCause
				}
				corrupt = append(corrupt, crdt.CorruptRow{
					BatchID: r.BatchID,
					Field:   "batch_payload",
					Cause:   corruptCause,
				})
				return out, corrupt, crdt.ErrResumeCorrupt
			}
			// Budget against the decoded op count, not the stored OpCount
			// column: the two are now known equal, and keeping the budget on
			// what was actually decoded keeps it honest if this gate ever moves.
			if maxOps > 0 && ops+nOps > maxOps {
				return out, corrupt, crdt.ErrDeltaTooLarge
			}
			transitions := &leapmuxv1.BatchTransitions{}
			if decodeTransitions {
				if uerr := proto.Unmarshal(r.TransitionsPayload, transitions); uerr != nil {
					// STOP and signal FALLBACK, same as a corrupt batch_payload.
					// Substituting empty transitions here is NOT the safe
					// degradation it looks like: visibilityFor would read
					// {Pre:"", Post:""} for every op, IsAllowed("") is false on
					// both sides, so filterVisibleOps returns nil and NO frame
					// (batch, materialized, or removed) is emitted for this
					// batch — the client cannot tell that from "this batch never
					// happened", yet its watermark still advances past it.
					corrupt = append(corrupt, crdt.CorruptRow{
						BatchID: r.BatchID,
						Field:   "transitions_payload",
						Cause:   wrapCorruptRow(p.mode.corruptRecoverable(), "unmarshal transitions_payload", r.BatchID, uerr),
					})
					return out, corrupt, crdt.ErrResumeCorrupt
				}
				// Decoding cleanly is not enough. A payload truncated at an
				// entry boundary -- or to zero bytes -- unmarshals without error
				// into a SHORT entry list, and the ops whose entries are missing
				// then hit exactly the invisible-on-both-sides path described
				// above. Only comparing the entries against the batch's own ops
				// distinguishes "this batch legitimately moved nothing" from
				// "this row lost the entries that said what it moved", so the
				// completeness check is what actually closes the hole the
				// unmarshal guard only half-covers.
				if ref, missing := crdt.MissingTransitionOp(batch, transitions); missing {
					corrupt = append(corrupt, crdt.CorruptRow{
						BatchID: r.BatchID,
						Field:   "transitions_payload",
						Cause: wrapCorruptRow(p.mode.corruptRecoverable(), "incomplete transitions_payload", r.BatchID,
							fmt.Errorf("no transition entry for %v", ref)),
					})
					return out, corrupt, crdt.ErrResumeCorrupt
				}
			}
			ops += nOps
			bytes += rowBytes
			out = append(out, crdt.ResumeBatch{Batch: batch, Transitions: transitions})
		}
		if len(rows) < store.CRDTBatchPageLimit {
			break
		}
		last := rows[len(rows)-1]
		cur = &leapmuxv1.HLC{
			Physical: last.PhysicalMs,
			Logical:  last.LastLogical,
			ClientId: last.OriginClient,
		}
	}
	return out, corrupt, nil
}

func (j *crdtJournal) CommitBatch(ctx context.Context, c crdt.CommitBatch) error {
	// One mint for the committing tenant, minted here rather than per journal
	// row: every row a batch writes -- the journal row, the dedup row and the
	// index-view rows -- belongs to this one transaction's user, which
	// crdt.CommitBatch states exactly once.
	owner, err := mintTenant(c.UserID)
	if err != nil {
		return err
	}
	// Serialize BEFORE opening the transaction. Neither payload depends on
	// anything the transaction reads, and the transitions blob can be sizeable
	// (it carries a full record snapshot per visibility-crossing entity), so
	// marshalling inside the closure held the write transaction — and whatever
	// locks and connection it owns — open across CPU work for no reason.
	payload, err := proto.Marshal(c.Batch)
	if err != nil {
		return fmt.Errorf("marshal batch %s: %w", c.Batch.GetBatchId(), err)
	}
	// A nil proto (a caller with no transitions) marshals to the empty
	// BatchTransitions, so the NOT NULL column always has a valid value.
	transitions := c.Transitions
	if transitions == nil {
		transitions = &leapmuxv1.BatchTransitions{}
	}
	transitionsPayload, err := proto.Marshal(transitions)
	if err != nil {
		return fmt.Errorf("marshal transitions %s: %w", c.Batch.GetBatchId(), err)
	}
	return j.store.RunInTransaction(ctx, func(tx store.Store) error {
		ops := c.Batch.GetOps()
		opCount := int64(len(ops))
		first := ops[0].GetCanonicalHlc()
		last := ops[opCount-1].GetCanonicalHlc()
		if err := tx.UserOpBatches().Insert(ctx, store.InsertUserOpBatchParams{
			UserID:             owner,
			PhysicalMs:         first.GetPhysical(),
			Logical:            first.GetLogical(),
			LastLogical:        last.GetLogical(),
			OriginClient:       first.GetClientId(),
			PrincipalID:        c.PrincipalID,
			BatchID:            c.Batch.GetBatchId(),
			BodyHash:           c.Dedup.BodyHash,
			BatchPayload:       payload,
			TransitionsPayload: transitionsPayload,
			OpCount:            opCount,
			Epoch:              c.Epoch,
		}); err != nil {
			return fmt.Errorf("insert user_op_batch %s: %w", c.Batch.GetBatchId(), err)
		}
		// The dedup row's tenant, principal and epoch come off the commit
		// envelope, not off the entry: it is the same transaction's row.
		d := c.Dedup
		dCanon := d.CanonicalFirstHLC
		if err := tx.UserRecentBatchIDs().Insert(ctx, store.InsertUserRecentBatchIDParams{
			UserID:              owner,
			BatchID:             d.BatchID,
			BodyHash:            d.BodyHash,
			PrincipalID:         c.PrincipalID,
			CanonicalPhysicalMs: dCanon.GetPhysical(),
			CanonicalLogical:    dCanon.GetLogical(),
			CanonicalClient:     dCanon.GetClientId(),
			OpCount:             d.OpCount,
			Epoch:               c.Epoch,
			ExpiresAt:           d.ExpiresAt,
		}); err != nil {
			return fmt.Errorf("insert dedup row %s: %w", d.BatchID, err)
		}
		idx := txTabIndexWriter{tx: tx, owner: owner}
		return crdt.ApplyDiff(ctx, idx, c.IndexDiff)
	})
}

func (j *crdtJournal) LookupRecentBatchID(ctx context.Context, userID, batchID string) (*crdt.RecentBatchRecord, error) {
	// A blank tenant is uniquely dangerous HERE, which is why this refuses
	// rather than letting the store answer. The store maps an unminted owner to
	// ErrNotFound, and this method translates that to crdt.ErrNotFound -- which
	// crdt.Manager.runDedup reads as "no prior commit for this batch id,
	// proceed". A broken tenancy invariant would therefore be indistinguishable
	// from a legitimate dedup miss, and would silently DISABLE retry
	// idempotence (re-applying an already-committed batch) instead of surfacing.
	owner, err := mintTenant(userID)
	if err != nil {
		return nil, err
	}
	row, err := j.store.UserRecentBatchIDs().Get(ctx, owner, batchID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, crdt.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &crdt.RecentBatchRecord{
		UserID:            row.UserID,
		BatchID:           row.BatchID,
		BodyHash:          row.BodyHash,
		PrincipalID:       row.PrincipalID,
		CanonicalFirstHLC: &leapmuxv1.HLC{Physical: row.CanonicalPhysicalMs, Logical: row.CanonicalLogical, ClientId: row.CanonicalClient},
		OpCount:           row.OpCount,
		Epoch:             row.Epoch,
		ExpiresAt:         row.ExpiresAt,
	}, nil
}

func (j *crdtJournal) AdvanceEpoch(ctx context.Context, userID string, epoch int64, startedAt time.Time) error {
	owner, err := mintTenant(userID)
	if err != nil {
		return err
	}
	return j.store.UserState().AdvanceEpoch(ctx, store.AdvanceUserEpochParams{
		UserID:         owner,
		Epoch:          epoch,
		EpochStartedAt: startedAt,
		UpdatedAt:      startedAt,
	})
}

func (j *crdtJournal) CompactBatch(ctx context.Context, c crdt.CompactBatch) error {
	// The state payload's own user_id keys both writes below, exactly as it did
	// before the retype -- crdt.Manager.requireOwnState has already refused a
	// payload naming any tenant other than the manager's, so this is that id.
	owner, err := mintTenant(c.State.GetUserId())
	if err != nil {
		return err
	}
	return j.store.RunInTransaction(ctx, func(tx store.Store) error {
		payload, err := proto.Marshal(c.State)
		if err != nil {
			return fmt.Errorf("marshal state: %w", err)
		}
		now := time.Now()
		if err := tx.UserState().Upsert(ctx, store.UpsertUserStateParams{
			UserID:       owner,
			StatePayload: payload,
			// Taken from the same c.State just marshalled above, so the column
			// and the blob can never disagree about how far this user has
			// compacted. The retention sweep deletes only strictly below it.
			CompactionPhysicalMs: c.State.GetCompactionWatermark().GetPhysical(),
			CurrentEpoch:         c.State.GetCurrentEpoch(),
			EpochStartedAt:       c.State.GetEpochStartedAt().AsTime(),
			UpdatedAt:            now,
		}); err != nil {
			return fmt.Errorf("upsert user_state: %w", err)
		}
		if c.DropThrough != nil {
			if err := tx.UserOpBatches().DeleteThrough(ctx, store.DeleteUserOpBatchesThroughParams{
				UserID:              owner,
				ThroughPhysicalMs:   c.DropThrough.GetPhysical(),
				ThroughLogical:      c.DropThrough.GetLogical(),
				ThroughOriginClient: c.DropThrough.GetClientId(),
			}); err != nil {
				return fmt.Errorf("delete user_op_batches through: %w", err)
			}
		}
		return nil
	})
}

func (j *crdtJournal) CleanupExpiredRecentBatchIDs(ctx context.Context, before time.Time) (int64, error) {
	return j.store.UserRecentBatchIDs().DeleteExpired(ctx, before)
}

// txTabIndexWriter is a thin adapter from crdt.TabIndexWriter to the
// transactional store.WorkspaceTabIndexStore. All four methods are
// bulk: crdt.ApplyDiff hands the writer the full per-batch slices in
// one call, and the underlying store chunks internally when the
// backend's parameter limit demands it.
//
// owner is the committing tenant, minted once by CommitBatch. It is the SOLE
// source of the owner column on the UPSERT paths: crdt.TabIndexRow carries no
// owner of its own, so there is nothing for this to disagree with. That is what
// makes "a commit only ever writes its own user's index rows" structural rather
// than data-dependent -- a projected row is derived from state.GetUserId() and
// crdt.Manager.requireOwnState refuses a state payload naming any other tenant,
// so the row's owner was always this one, and dropping the field removes the
// only way for the two to drift.
//
// The DELETE paths are the asymmetry: crdt.TabKey DOES carry an owner, because
// a key names an EXISTING row whose owner is half the primary key. Those bind
// each key's own owner, so one unusable key does not cancel the deletes queued
// for its neighbours.
type txTabIndexWriter struct {
	tx    store.Store
	owner userid.UserID
}

func (w txTabIndexWriter) BulkUpsertOwned(ctx context.Context, rows []crdt.TabIndexRow) error {
	if len(rows) == 0 {
		return nil
	}
	return w.tx.WorkspaceTabIndex().BulkUpsertOwned(ctx, w.tabParams(rows))
}

func (w txTabIndexWriter) BulkDeleteOwned(ctx context.Context, keys []crdt.TabKey) error {
	storeKeys := tabIndexKeys(keys)
	if len(storeKeys) == 0 {
		return nil
	}
	return w.tx.WorkspaceTabIndex().BulkDeleteOwned(ctx, storeKeys)
}

func (w txTabIndexWriter) BulkUpsertRendered(ctx context.Context, rows []crdt.TabIndexRow) error {
	if len(rows) == 0 {
		return nil
	}
	return w.tx.WorkspaceTabIndex().BulkUpsertRendered(ctx, w.tabParams(rows))
}

func (w txTabIndexWriter) BulkDeleteRendered(ctx context.Context, keys []crdt.TabKey) error {
	storeKeys := tabIndexKeys(keys)
	if len(storeKeys) == 0 {
		return nil
	}
	return w.tx.WorkspaceTabIndex().BulkDeleteRendered(ctx, storeKeys)
}

// tabParams converts a diff's rows to store params, supplying the committing
// tenant as the owner column (a crdt.TabIndexRow carries none).
// store.UpsertRenderedTabParams is an alias of store.UpsertOwnedTabParams (the
// two views share one column set), so the owned and rendered upserts share this
// single conversion instead of repeating it.
func (w txTabIndexWriter) tabParams(rows []crdt.TabIndexRow) []store.UpsertOwnedTabParams {
	params := make([]store.UpsertOwnedTabParams, len(rows))
	for i, row := range rows {
		params[i] = store.UpsertOwnedTabParams{
			UserID:      w.owner,
			WorkspaceID: row.WorkspaceID,
			TabType:     row.TabType,
			TabID:       row.TabID,
			WorkerID:    row.WorkerID,
			TileID:      row.TileID,
			Position:    row.Position,
		}
	}
	return params
}

// tabIndexKeys adapts CRDT tab keys to store keys. It is a pure shape
// conversion, and the mint is part of that shape: a crdt key carries an untyped
// `UserID string`, a store key carries a userid.UserID, and a blank one mints
// to the ZERO UserID rather than being refused here. Blank-owner refusal
// belongs to the store, which applies store.FilterTabIndexKeys at every site
// that binds an owner column (sqlutil.BulkDeleteTabs for sqlite/mysql, the
// postgres adapter directly) and SKIPS the zero keys there. Keeping the rule
// there rather than here means a future non-CRDT caller of BulkDeleteOwned
// inherits it instead of having to know to re-copy it -- and means one unusable
// key never cancels its neighbours, which an early refusal here would.
func tabIndexKeys(keys []crdt.TabKey) []store.TabIndexKey {
	out := make([]store.TabIndexKey, len(keys))
	for i, k := range keys {
		// The discarded ok is the point: the zero UserID IS the "unbindable"
		// marker store.FilterTabIndexKeys drops, so there is nothing to refuse.
		owner, _ := userid.New(k.UserID)
		out[i] = store.TabIndexKey{UserID: owner, TabID: k.TabID}
	}
	return out
}
