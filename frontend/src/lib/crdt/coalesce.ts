import type {
  CrdtOp,
  OpBatch,
  SetFloatingWindowRegisterOp,
  SetNodeRegisterOp,
  SetTabRegisterOp,
} from '~/generated/proto/leapmux/v1/user_ops_pb'

// ---------------------------------------------------------------------------
// Last-writer-wins coalescing of queued ops
//
// High-frequency gestures (drag, resize, opacity scrub, tile-ratio drag) emit
// the SAME register over and over. Every one of those writes is an LWW register
// set, so within a group of ops about to be sent together, only the LAST write
// to a given register can survive the merge -- on this client, on the hub, and
// on every peer. The earlier ones are pure wire, storage and fan-out cost.
//
// This does NOT replace the gesture overrides in the floating-window and tiling
// stores: an override suppresses ops for a whole GESTURE, while this merges only
// what lands in one flush window, so a gesture emitting per frame still costs
// ~1 op per flush rather than 1 per gesture. What it buys is that the next
// high-frequency register is cheap by default instead of needing a fourth
// bespoke override.
//
// TWO RULES KEEP IT SAFE. Both are deliberately conservative, because the cost
// of being wrong is a rejected batch, and the upside is only saved bandwidth.
//
//  1. WHOLE BATCHES ONLY -- a batch is either sent intact or dropped intact,
//     never trimmed. The hub validates a batch as a unit: a record must be
//     COMPLETE when its creation batch commits (BATCH_REJECTION_INCOMPLETE_RECORD),
//     so removing one op from a batch that creates an entity makes the hub reject
//     the whole thing and the user's new window or tile split silently reverts.
//     Not trimming makes that unreachable without the client having to model the
//     server's completeness rules -- which would be a second source of truth for
//     an invariant the validator already owns.
//
//  2. ONLY EXPLICITLY COALESCABLE FIELDS COUNT (see COALESCABLE_FIELDS). It is
//     tempting to phrase this as "register sets are safe, creations are not",
//     but in this CRDT a creation IS a set of register sets: a floating window is
//     created by writing root_node_id, workspace_id, x, y, width, height and
//     opacity, and a node by writing kind, parent_id and position. A rule keyed on
//     "is this a register op" would therefore exclude nothing at all. Some of
//     those fields are also set-once (parent_id, root_node_id) and so are not
//     LWW-redundant in the first place. An allowlist of the mutable, genuinely
//     high-frequency fields is the honest shape.
//
// Together the rules mean a creation batch can never be dropped: it always
// carries at least one op outside the allowlist, so it is never fully
// superseded.
//
// The dropped ops never reach the hub, so they never enter its journal, and the
// client's own op-log records CONFIRMED frames (what the hub committed) rather
// than pending ops -- so delta-resume replay sees the same history either way.
//
// ONE BEHAVIOUR DIFFERENCE, stated precisely because the earlier draft of this
// comment overclaimed: dropping is equivalent to the un-coalesced stream when
// the surviving batch COMMITS. If the surviving batch is instead rejected while
// an earlier one commits, the register settles at its pre-gesture value rather
// than at the dropped intermediate value. Both are states the hub actually
// holds, and for a gesture the all-or-nothing outcome is the better of the two,
// but it is not literally "as if nothing were dropped".
// ---------------------------------------------------------------------------

/**
 * The oneof case union of an op's `field`, from the GENERATED type.
 *
 * Keying the policy tables on this rather than a `Set<string>` is load-bearing,
 * not stylistic: a string set infers `Set<string>`, so `'positon'` type-checks
 * and a typo or proto rename silently disables coalescing with no compile error
 * and no failing test -- exactly the wrong default for a module whose stated
 * purpose is that the next high-frequency register is cheap BY DEFAULT.
 */
type FieldCase<T extends { field: { case?: string } }> = Exclude<T['field']['case'], undefined>

/**
 * What the hub's register semantics make TRUE of one field, in one value.
 *
 * There used to be two boolean tables per op kind -- `*_COALESCABLE` and
 * `*_LWW_MUTABLE` -- declared ~60 lines apart over the identical field set, and
 * the safety relation between them (`coalescable` implies `lwwMutable`) was
 * maintained purely by eye. `Record<FieldCase<...>, boolean>` forces both
 * entries to be PRESENT; nothing forced them to be CONSISTENT, and the illegal
 * combination is the dangerous one: marking a SET-ONCE register coalescable
 * silently drops a write the hub would only ever accept once -- exactly the
 * loss `parentId` / `rootNodeId` are excluded to prevent.
 *
 * Three states, and the fourth is unrepresentable rather than merely absent:
 *
 *   - SET_ONCE           the hub writes the slot only while empty, so a later
 *                        write cannot supersede an earlier one. Never
 *                        coalescable (dropping it drops a write that must
 *                        land), never LWW-mutable.
 *   - LWW_NOT_COALESCED  plain LWW on the hub, so a later write DOES supersede
 *                        it -- but dropping an intermediate is not lossless
 *                        (a move), or simply has not been enabled yet.
 *   - LWW_COALESCABLE    plain LWW, and an intermediate write carries no
 *                        information the next one does not.
 *
 * `tsc` still reports a rename as TS2561 and a NEW field as TS2739, so adding a
 * register forces an explicit policy rather than an accidental opt-out.
 */
type RegisterPolicy = 'SET_ONCE' | 'LWW_NOT_COALESCED' | 'LWW_COALESCABLE'

/** "Is dropping an intermediate write to this register lossless?" — bandwidth. */
function isCoalescable(policy: RegisterPolicy): boolean {
  return policy === 'LWW_COALESCABLE'
}

/**
 * "Does a LATER write to this register supersede this one?" — ORDERING, and a
 * strictly wider set than the above. Reusing the bandwidth answer here left
 * every excluded-but-LWW register unprotected, which is backwards: those are
 * exactly the moves whose inversion is permanent. The hub re-stamps
 * canonical_hlc on a retry (Manager.commit ticks the clock after a dedup MISS),
 * so a parked `tileId` write lands with a FRESHER HLC than the newer move that
 * superseded it and wins LWW on every peer. Cancelling it is not an
 * optimization; it keeps the move the user actually made from being undone
 * minutes later.
 *
 * That `isCoalescable` implies `isLwwMutable` is now a property of the enum --
 * only SET_ONCE is excluded here, and SET_ONCE is never coalescable -- rather
 * than an invariant across two tables that nothing checked.
 */
function isLwwMutable(policy: RegisterPolicy): boolean {
  return policy !== 'SET_ONCE'
}

const NODE_POLICY: Record<FieldCase<SetNodeRegisterOp>, RegisterPolicy> = {
  position: 'LWW_COALESCABLE',
  ratios: 'LWW_COALESCABLE',
  kind: 'LWW_NOT_COALESCED',
  direction: 'LWW_NOT_COALESCED',
  rows: 'LWW_NOT_COALESCED',
  cols: 'LWW_NOT_COALESCED',
  // Grid row/column ratios are dragged exactly like `ratios` and are the obvious
  // next candidates, but enabling them CHANGES what goes on the wire, so they
  // stay LWW_NOT_COALESCED until that is deliberately chosen.
  rowRatios: 'LWW_NOT_COALESCED',
  colRatios: 'LWW_NOT_COALESCED',
  // Set-once on the hub: written only while the slot is empty.
  parentId: 'SET_ONCE',
}

const TAB_POLICY: Record<FieldCase<SetTabRegisterOp>, RegisterPolicy> = {
  position: 'LWW_COALESCABLE',
  // A MOVE, not a redundant write: dropping an intermediate loses the move
  // outright. See supersededParkedBatchIds for why that matters on retry.
  tileId: 'LWW_NOT_COALESCED',
  workerId: 'LWW_NOT_COALESCED',
  displayMode: 'LWW_NOT_COALESCED',
  fileViewMode: 'LWW_NOT_COALESCED',
  fileDiffBase: 'LWW_NOT_COALESCED',
}

const FLOATING_WINDOW_POLICY: Record<FieldCase<SetFloatingWindowRegisterOp>, RegisterPolicy> = {
  x: 'LWW_COALESCABLE',
  y: 'LWW_COALESCABLE',
  width: 'LWW_COALESCABLE',
  height: 'LWW_COALESCABLE',
  opacity: 'LWW_COALESCABLE',
  // Also a move between workspaces, not a redundant write.
  workspaceId: 'LWW_NOT_COALESCED',
  // Set-once on the hub.
  rootNodeId: 'SET_ONCE',
}

/**
 * The policy tables, exported for the co-located suite ONLY so it can assert
 * the relation between the two questions across every field at once. Nothing
 * else imports them; the two key functions are the public surface.
 */
export const REGISTER_POLICIES = { node: NODE_POLICY, tab: TAB_POLICY, fw: FLOATING_WINDOW_POLICY } as const

/**
 * Identify the LWW register an op writes, as a stable string key, or null when
 * the op is not an eligible coalescing target (see COALESCABLE_FIELDS).
 *
 * The key includes the entity identity AND the field, so two ops touching
 * different fields of one entity never coalesce with each other.
 */
export function registerKey(op: CrdtOp): string | null {
  return registerKeyIn(op, isCoalescable)
}

/**
 * Same key, but keyed on the LWW-MUTABLE tables: "a later write to this register
 * supersedes this one", regardless of whether dropping it would be lossless.
 * See the comment above the tables for why the two questions need two answers.
 */
export function lwwRegisterKey(op: CrdtOp): string | null {
  return registerKeyIn(op, isLwwMutable)
}

/**
 * `admits` selects WHICH question is being asked of each field's policy. One
 * predicate argument, not three positionally-interchangeable tables: the old
 * signature took `(op, nodeTable, tabTable, floatingWindowTable)`, all typed
 * `Record<string, boolean>`, so transposing two of them compiled cleanly and
 * silently swapped the bandwidth rule for the ordering rule.
 */
function registerKeyIn(op: CrdtOp, admits: (policy: RegisterPolicy) => boolean): string | null {
  const body = op.body
  switch (body.case) {
    case 'setNodeRegister':
      return policyKey(NODE_POLICY, admits, body.value.field.case, `node:${body.value.nodeId}`)
    case 'setTabRegister':
      return policyKey(TAB_POLICY, admits, body.value.field.case, `tab:${body.value.tabType}:${body.value.tabId}`)
    case 'setFloatingWindowRegister':
      return policyKey(FLOATING_WINDOW_POLICY, admits, body.value.field.case, `fw:${body.value.windowId}`)
    default:
      // Tombstones and workspace wiring are never LWW-redundant.
      return null
  }
}

/**
 * The one policy-lookup + key-format rule the three op kinds share. They differ
 * ONLY in their table and in how their identity spells itself, so the caller
 * pre-formats the entity part and this owns the rest.
 *
 * The key format is free to change: it never leaves the process and is only
 * ever compared to other keys, never to a literal.
 */
function policyKey(
  table: Record<string, RegisterPolicy>,
  admits: (policy: RegisterPolicy) => boolean,
  field: string | undefined,
  entity: string,
): string | null {
  if (!field)
    return null
  const policy = table[field]
  return policy && admits(policy) ? `${entity}:${field}` : null
}

/**
 * Of the batches PARKED in a retry backoff, which are fully superseded by the
 * batches about to be sent.
 *
 * A parked batch is invisible to the flush queue, and that is a hazard on its
 * own: it left the queue on a transport failure, so the hub never saw it, so
 * dedup will not apply when its timer fires. It is then committed with a FRESH
 * canonical HLC -- newer than the write that superseded it in the meantime --
 * and the stale value wins LWW on the hub and every peer. A drag that hit one
 * dropped request lands the window back at a mid-gesture position, permanently.
 *
 * A parked batch is strictly OLDER than this flush, so an outgoing op on the
 * same register supersedes it. Cancelling is only sound when EVERY op is
 * superseded (a partially-superseded batch still carries writes nothing else
 * will make), which the whole-batch rule already guarantees.
 *
 * Keyed on lwwRegisterKey, NOT registerKey: this is an ordering question, and
 * the move-defining registers the coalescing table excludes are precisely the
 * ones whose inversion is permanent. See the LWW-MUTABLE tables above.
 */
export function supersededParkedBatchIds(parked: Iterable<OpBatch>, outgoing: OpBatch[]): string[] {
  const outgoingKeys = new Set<string>()
  for (const batch of outgoing) {
    for (const op of batch.ops) {
      const key = lwwRegisterKey(op)
      if (key !== null)
        outgoingKeys.add(key)
    }
  }
  if (outgoingKeys.size === 0)
    return []
  const out: string[] = []
  for (const batch of parked) {
    if (batchFullySuperseded(batch, lwwRegisterKey, key => outgoingKeys.has(key)))
      out.push(batch.batchId)
  }
  return out
}

/**
 * Whether EVERY op in `batch` writes a coalescable register that `isSuperseded`
 * says something later already rewrote.
 *
 * This is rule 1 from the module header — whole batches only — and it lives in
 * one place because it is the safety property, not a convenience: a batch is
 * dropped intact or kept intact, never trimmed, so the hub's
 * record-completeness validation cannot be broken by a partial drop. Both
 * callers apply the identical rule and differ ONLY in which register table they
 * key on (coalescable vs LWW-mutable) and in what counts as superseded (a later
 * batch index vs membership in the outgoing key set).
 *
 * An EMPTY batch is never superseded: `every` is vacuously true on it, which
 * would drop a batch carrying no ops at all rather than passing it through.
 */
function batchFullySuperseded(
  batch: OpBatch,
  keyOf: (op: CrdtOp) => string | null,
  isSuperseded: (key: string) => boolean,
): boolean {
  return batch.ops.length > 0 && batch.ops.every((op) => {
    const key = keyOf(op)
    return key !== null && isSuperseded(key)
  })
}

export interface CoalesceResult {
  /** The batches to send, in the original order, each intact. */
  batches: OpBatch[]
  /**
   * Batch ids dropped entirely and not being sent. The caller MUST drop each
   * one's pending entry: no BatchResult will ever arrive for a batch the hub
   * never saw, so the optimistic overlay would otherwise wait on it forever.
   */
  droppedBatchIds: string[]
  /** How many ops were removed, for diagnostics. */
  droppedOps: number
}

/**
 * Drop batches whose every op is overwritten by a later op in the same flush.
 *
 * Order is preserved and nothing is rewritten: a batch is passed through by
 * reference or omitted. A batch survives unless ALL of its ops are coalescable
 * (rule 2) and each is superseded by a later op writing the same register.
 */
export function coalesceQueuedBatches(batches: OpBatch[]): CoalesceResult {
  // Last position of each register key across the whole flush. Positions are
  // batch indices: an op is superseded only by an op in a LATER batch, since
  // within one batch every op is kept or dropped together.
  const lastBatchFor = new Map<string, number>()
  batches.forEach((batch, i) => {
    for (const op of batch.ops) {
      const key = registerKey(op)
      if (key !== null)
        lastBatchFor.set(key, i)
    }
  })

  const out: OpBatch[] = []
  const droppedBatchIds: string[] = []
  let droppedOps = 0
  batches.forEach((batch, i) => {
    if (batchFullySuperseded(batch, registerKey, key => lastBatchFor.get(key) !== i)) {
      droppedBatchIds.push(batch.batchId)
      droppedOps += batch.ops.length
      return
    }
    out.push(batch)
  })
  return { batches: out, droppedBatchIds, droppedOps }
}
