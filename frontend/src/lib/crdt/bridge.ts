import type { HLCClock } from './hlc'
import type { UserCrdtState } from '~/generated/leapmux/v1/user_crdt_pb'
import type { OpBatch } from '~/generated/leapmux/v1/user_ops_pb'
import { createSignal } from 'solid-js'

/**
 * Op-builder context — origin client / HLC clock — threaded
 * through every op-construction helper in `~/lib/crdt/ops`. Lives
 * here (next to the bridge accessors that produce it) so the
 * `withBridgeAndState` helper can return the constructed ctx without
 * creating a runtime cycle between bridge.ts and ops.ts.
 */
export interface OpBuilderCtx {
  originClientId: string
  clock: HLCClock
}

/**
 * CRDT bridge — singleton wired by AppShell.tsx so the imperative
 * stores (layout / tab / floatingWindow) can read the speculatively-
 * applied UserCrdtState and emit op batches without having to receive
 * the OpsSubmitter / PendingOpsManager / HLCClock through every
 * constructor.
 *
 * The bridge is unset before AppShell mounts and remains unset in
 * the test harness — every accessor returns `null` (or a no-op
 * default for `enqueue`) when no bridge is registered, so single-
 * store tests don't need the full reactive wiring.
 *
 * Plan-aligned design: the layout / floating-window stores read their
 * tree from `project(speculativeState)[workspaceId]` rather than
 * maintaining a parallel local tree, and emit op batches via
 * `enqueue` for every mutation. The hub re-broadcasts canonical-HLC-
 * tagged ops, the local PendingOpsManager folds them into
 * `confirmedState`, and the speculative state is recomputed —
 * Solid's reactivity dispatches the rerender via the `state` accessor
 * subscribing to a version signal.
 */
export interface CRDTBridge {
  workspaceId: () => string | null
  /**
   * Enqueue a fresh batch — speculatively applies + ships in 16ms.
   * Returns the batch_id so callers (e.g. cross-workspace move
   * rollback) can correlate the eventual commit/reject outcome with
   * the local action.
   */
  enqueue: (batch: OpBatch) => string
  /**
   * Mint advisory client_hlcs from the same monotonic stream the
   * pending manager uses.
   *
   * Never null: a bridge is only installed alongside a live
   * `PendingOpsManager`, whose `clock` is a non-optional field. The
   * "not wired yet" state is the absence of the bridge itself
   * (`getCRDTBridge()` returning null), not a half-built one.
   */
  clock: () => HLCClock
  /** Stable client id for op origin (the tab's session token). */
  originClientId: () => string
  /**
   * Speculatively-applied UserCrdtState (confirmedState + every still-
   * pending optimistic batch folded on top). Reactive: every call to
   * a state-mutating method on the underlying PendingOpsManager bumps
   * a version signal, so memoized projections derived from this
   * accessor re-derive on the next reactive tick.
   *
   * Returns null before bootstrap (initial UserMaterialized hasn't
   * landed yet) or in test harnesses without a wired bridge.
   */
  speculativeState: () => UserCrdtState | null
}

// Module-level Solid signal so reactive consumers (layout.store /
// floatingWindow.store memos) re-run when the bridge transitions
// from null → wired. Without this, a memo that early-returns on
// `bridge === null` would never re-derive once the bridge is set,
// because its dependency set wouldn't include the bridge slot.
const [bridgeSignal, setBridgeSignal] = createSignal<CRDTBridge | null>(null)

/** AppShell calls this once after constructing PendingOpsManager + OpsSubmitter. */
export function setCRDTBridge(b: CRDTBridge | null): void {
  setBridgeSignal(b)
}

/**
 * Stores read this on every mutator. Returns null when AppShell hasn't
 * wired the bridge yet. Read inside a Solid reactive scope (e.g.
 * createMemo) to subscribe to bridge wiring transitions.
 */
export function getCRDTBridge(): CRDTBridge | null {
  return bridgeSignal()
}

/**
 * Common "if the bridge isn't wired yet, return a benign no-op result"
 * preamble for every CRDT-emitting helper across the layout /
 * floatingWindow stores. Collapses the repeated
 * `const bridge = getCRDTBridge(); if (!bridge) return X` boilerplate
 * so call sites read as just the emit call plus their own pre-checks.
 */
export function withBridge<T>(fn: (bridge: CRDTBridge) => T, fallback: T): T {
  const bridge = getCRDTBridge()
  return bridge ? fn(bridge) : fallback
}

/**
 * Build an OpBuilderCtx from a bridge. Total -- a wired bridge always has
 * everything the ctx needs. Lives next to `withBridgeAndState` so both read
 * the bridge through one accessor pair.
 *
 * There is no user-id check: ops stopped carrying a user id when the
 * tenancy collapsed onto the session, and the installer already refuses
 * to set a bridge without an authenticated user -- so "a bridge exists"
 * IS "the user is known", and a second field restating it could only
 * fall out of sync with it. The same argument retired the clock guard:
 * `clock` is a non-optional field of the `PendingOpsManager` the installer
 * builds the bridge from, so a null there was unreachable, and a guard for
 * an unreachable state is an untested branch pretending to be safety.
 */
export function ctxFromBridge(bridge: CRDTBridge): OpBuilderCtx {
  return { originClientId: bridge.originClientId(), clock: bridge.clock() }
}

/**
 * Collapse the `ctxFromBridge(bridge) + bridge.speculativeState() +
 * null-guard` preamble that every op-emitter in the layout and
 * floating-window stores repeats. Returns `fallback` when
 * speculativeState hasn't landed yet (the pre-bootstrap window, before
 * the initial UserMaterialized frame); otherwise invokes `fn(ctx, state)`
 * and returns its result.
 *
 * Note: when `fn` needs to enqueue ops it should close over `bridge`
 * from the outer scope — this helper deliberately doesn't pass the
 * bridge in, to keep `fn`'s signature focused on the two values it
 * almost always needs (the op-builder ctx and the speculative state).
 */
export function withBridgeAndState<T>(
  bridge: CRDTBridge,
  fn: (ctx: OpBuilderCtx, state: UserCrdtState) => T,
  fallback: T,
): T {
  const state = bridge.speculativeState()
  if (!state)
    return fallback
  return fn(ctxFromBridge(bridge), state)
}
