import type { TerminalInstance } from './terminal'
import { createLogger } from './logger'
import { attachWebgl, detachWebgl } from './terminal'

const log = createLogger('webgl-pool')

/**
 * Maximum number of terminals that may hold a live WebGL context at once.
 *
 * Browsers cap simultaneous WebGL contexts (~16 on Chrome/Blink, stricter on
 * WKWebView/Safari) and force-drop the oldest past the cap, which corrupts the
 * evicted terminal's glyphs. We keep well under that ceiling to leave headroom
 * for the transient double-context that exists during an eviction hand-off and
 * for any other WebGL in the app.
 *
 * Only on-screen (`active && visible`) terminals ever compete for a slot, so
 * the realistic contender set is the number of visible tiles showing a
 * terminal -- almost always <= K. When it does exceed K (a pathological large
 * grid), the overflow terminals fall back to xterm's DOM renderer, which is
 * fully correct just not GPU-accelerated: eviction is a performance event,
 * never a corruption event.
 */
export const MAX_WEBGL_TERMINAL_CONTEXTS = 8

/**
 * How many times a terminal may have its WebGL context force-dropped by the
 * browser before the pool stops re-attaching and leaves it on the DOM
 * renderer. Without this bound, a machine under system-wide GPU pressure
 * (other tabs/apps holding contexts) would thrash: lose -> re-attach -> lose,
 * spamming warnings and burning CPU instead of settling on the correct DOM
 * fallback. The counter resets when the terminal is released and re-acquired
 * (e.g. a tab switch) OR when a context survives CONTEXT_LOSS_BUDGET_RESET_MS
 * before failing, so recovery is automatic once pressure eases even for a
 * terminal that is never switched away.
 */
const MAX_CONTEXT_LOSS_RETRIES = 2

/**
 * How long (ms) a WebGL context must stay continuously attached before a
 * subsequent loss is treated as a fresh storm rather than a continuation of the
 * previous one. Past this window the loss-retry budget resets. Without it, a
 * persistently-visible terminal (a split-tile pane never released to reset the
 * count) that loses its context more than MAX_CONTEXT_LOSS_RETRIES times over
 * its whole life -- even with hours of healthy uptime between losses -- would be
 * pinned to the DOM renderer forever. The window keeps the anti-thrash bound for
 * genuinely rapid lose -> re-attach -> lose loops (each loss lands well inside
 * it) while letting a terminal recover after transient pressure eases.
 */
const CONTEXT_LOSS_BUDGET_RESET_MS = 30_000

export interface WebglTerminalPool {
  /**
   * Mark a terminal as on-screen and wanting a WebGL context. Idempotent.
   * `focused` pins the terminal to the top priority so the one the user is
   * typing in is never the eviction victim, even on an initial grid render
   * where mount order would otherwise decide.
   */
  acquire: (id: string, instance: TerminalInstance, opts?: { focused?: boolean }) => void
  /** Relinquish a terminal's claim on a WebGL context (off-screen or disposed). */
  release: (id: string) => void
  /** True when the terminal currently holds a live WebGL context. */
  has: (id: string) => boolean
  /** Number of terminals currently holding a live WebGL context. */
  size: () => number
  /** Detach every context and reset all state. For HMR teardown and tests. */
  disposeAll: () => void
}

export interface WebglTerminalPoolDeps {
  capacity: number
  /**
   * Attach a WebGL renderer to the instance, wiring `onContextLoss` to the
   * supplied callback. Returns whether the attach succeeded (false when WebGL
   * is unavailable, e.g. jsdom). Must be synchronous -- the pool has already
   * awaited font readiness before calling it.
   */
  attach: (instance: TerminalInstance, onContextLoss: () => void) => boolean
  /** Detach the WebGL renderer, reverting the instance to the DOM renderer. */
  detach: (instance: TerminalInstance) => void
  /**
   * Wall-clock source (ms) for the context-loss decay window. Injected so tests
   * can drive it deterministically; defaults to Date.now.
   */
  now?: () => number
}

type SlotState = 'dom' | 'pending' | 'webgl'

/**
 * All per-id bookkeeping for one terminal, held in a single record so the
 * facts that must describe the same terminal -- its instance, renderer state,
 * in-flight-attach token, and eligibility -- can't fall out of sync across the
 * detach/attach/loss paths that mutate them. The genuinely cross-cutting
 * structures (recency `order`, the `desired` on-screen set, `focusedId`) stay
 * separate because they relate ids to each other, not to one id's own state.
 */
interface Slot {
  instance: TerminalInstance
  state: SlotState
  /**
   * Monotonic token. Bumped on every detach / context-loss / instance swap so
   * an async attach that was in flight across the change becomes a no-op
   * instead of attaching to a terminal that no longer wants it.
   */
  epoch: number
  /**
   * True once the attach threw (WebGL genuinely unavailable) or the terminal
   * exhausted its context-loss retries. Skipped by reconcile until the id is
   * released, so we don't retry a doomed attach every tick.
   */
  ineligible: boolean
  /** Count of browser-forced context losses, for the retry bound. */
  contextLossCount: number
  /**
   * Wall-clock time (ms) this slot last reached the 'webgl' state, or undefined
   * while on DOM / mid-attach. Used to decay the context-loss budget: a loss
   * that arrives after the context survived CONTEXT_LOSS_BUDGET_RESET_MS resets
   * the count. Cleared by doDetach so a re-attach re-stamps it.
   */
  attachedAt?: number
}

/**
 * Bounds the number of live WebGL contexts across an unbounded number of
 * terminals. Terminals render with xterm's DOM renderer by default; the pool
 * hands out at most `capacity` WebGL contexts to the most-recently-used
 * on-screen terminals and detaches the rest.
 *
 * `attach`/`detach` are injected so the reconciliation logic is unit-testable
 * without a real GPU; the production singleton (`webglPool`) wires them to
 * `attachWebgl`/`detachWebgl` in `./terminal`.
 *
 * Reconciliation is coalesced onto a microtask: `acquire`/`release` only
 * mutate bookkeeping synchronously, and a single `reconcile()` runs after the
 * current reactive flush settles. This is what makes a cross-tile move safe --
 * the source tile's `release` and the destination tile's `acquire` for the
 * same id land in the same tick and net to "still desired", so the terminal
 * never briefly loses (and re-rasterizes) its renderer.
 */
export function createWebglTerminalPool(deps: WebglTerminalPoolDeps): WebglTerminalPool {
  const { capacity, attach, detach } = deps
  const now = deps.now ?? (() => Date.now())

  // The per-id record for every terminal the pool knows about. A slot is
  // created on the first `acquire` for an id and dropped by `reconcile` once
  // the id is released and has settled back on the DOM renderer.
  const slots = new Map<string, Slot>()
  // On-screen ids, ordered cold -> hot (hot end == most recently touched).
  const order: string[] = []
  const desired = new Set<string>()
  let focusedId: string | null = null
  let reconcileScheduled = false

  // A slot "holds or is claiming a context" when it is attached ('webgl') or
  // mid-attach ('pending') -- both own a teardown obligation. The three paths
  // that must tear a context down (reconcile eviction, instance swap, and
  // disposeAll) key off this one predicate rather than re-listing the states,
  // so a future fourth state can't silently slip past one of them.
  function holdsContext(slot: Slot): boolean {
    return slot.state === 'webgl' || slot.state === 'pending'
  }

  // Detach the addon backing `id`, swallowing (and logging) any teardown
  // error. Centralizes the guard the three detach paths share so they can't
  // drift on what a failed detach means.
  function safeDetach(id: string) {
    const slot = slots.get(id)
    if (!slot)
      return
    try {
      detach(slot.instance)
    }
    catch (error) {
      log.warn('webgl_detach_failed', { error })
    }
  }

  function removeFromOrder(id: string) {
    const i = order.indexOf(id)
    if (i !== -1)
      order.splice(i, 1)
  }

  function touch(id: string) {
    removeFromOrder(id)
    order.push(id)
  }

  // An id turns ineligible once its attach threw (WebGL unavailable) or its
  // context-loss retries were exhausted; the mark stays until the id is
  // released. Eligible ids are the only ones `computeTarget` hands a context.
  function isEligible(id: string): boolean {
    return !slots.get(id)?.ineligible
  }

  // The set of ids that should hold a WebGL context right now: the focused
  // terminal (pinned) plus the hottest desired ids until `capacity` is full.
  function computeTarget(): Set<string> {
    const target = new Set<string>()
    if (focusedId && desired.has(focusedId) && isEligible(focusedId))
      target.add(focusedId)
    for (let i = order.length - 1; i >= 0 && target.size < capacity; i--) {
      const id = order[i]
      if (isEligible(id))
        target.add(id)
    }
    return target
  }

  function scheduleReconcile() {
    if (reconcileScheduled)
      return
    reconcileScheduled = true
    queueMicrotask(() => {
      reconcileScheduled = false
      reconcile()
    })
  }

  function reconcile() {
    const target = computeTarget()

    // Detach anything attached or mid-attach that no longer belongs.
    for (const [id, slot] of slots) {
      if (holdsContext(slot) && !target.has(id))
        doDetach(id)
    }

    // Attach any target id not already attached or mid-attach.
    for (const id of target) {
      if ((slots.get(id)?.state ?? 'dom') === 'dom')
        doAttach(id)
    }

    // Drop the slot for ids that are fully released and settled on DOM.
    for (const [id, slot] of [...slots]) {
      if (!desired.has(id) && slot.state === 'dom')
        slots.delete(id)
    }
  }

  function doDetach(id: string) {
    const slot = slots.get(id)
    if (!slot)
      return
    slot.epoch++ // invalidate any in-flight attach for this id
    safeDetach(id)
    slot.state = 'dom'
    slot.attachedAt = undefined
  }

  function doAttach(id: string) {
    const slot = slots.get(id)
    if (!slot || !slot.instance.webglAllowed || slot.ineligible) {
      if (slot)
        slot.state = 'dom'
      return
    }
    const instance = slot.instance
    slot.state = 'pending'
    const myEpoch = slot.epoch
    void (async () => {
      try {
        // Wait until fonts have loaded before building the atlas, so the WebGL
        // renderer never caches fallback glyphs. Re-read `fontsReady` after
        // each await: a font-family swap replaces it mid-wait and we must
        // rasterize with the family that will actually be shown.
        let ready = instance.fontsReady
        await ready
        while (instance.fontsReady !== ready) {
          ready = instance.fontsReady
          await ready
        }

        // Bail if anything changed while we awaited: the slot was dropped, the
        // id was detached or re-prioritized (epoch bumped), the instance was
        // swapped, or a concurrent reconcile already moved this id off
        // `pending`. Re-read the live slot -- a swap may have replaced it.
        const live = slots.get(id)
        if (!live || live.epoch !== myEpoch || live.instance !== instance || live.state !== 'pending')
          return
        // Attaching a WebGL renderer to a terminal whose element is detached
        // corrupts its dimensions; leave it on DOM until a later reconcile.
        if (!instance.terminal.element?.isConnected) {
          live.state = 'dom'
          return
        }
        if (instance.webglAddon) {
          live.state = 'webgl'
          return
        }

        if (attach(instance, () => onContextLoss(id))) {
          live.state = 'webgl'
          live.attachedAt = now()
        }
        else {
          // WebGL genuinely unavailable -- stop retrying until release.
          live.state = 'dom'
          live.ineligible = true
        }
      }
      catch (error) {
        // Never leave the id wedged in `pending` on an unexpected throw --
        // reset it to DOM so a later reconcile can retry.
        log.warn('webgl_attach_failed', { error })
        const live = slots.get(id)
        if (live && live.state === 'pending')
          live.state = 'dom'
      }
    })()
  }

  // The browser force-dropped this terminal's GPU context. Treat it as an
  // involuntary detach and let reconcile re-attach if the terminal is still
  // desired and within capacity -- NOT a user-level release, which would
  // permanently demote a still-visible terminal to the DOM renderer. Bound the
  // re-attach so a machine under system-wide GPU pressure settles on DOM
  // instead of thrashing lose -> re-attach -> lose forever.
  function onContextLoss(id: string) {
    const slot = slots.get(id)
    // A context that stayed attached past the reset window before the browser
    // dropped it signals the earlier loss storm has passed, so clear the retry
    // budget before counting this loss. Read attachedAt now -- doDetach below
    // clears it. Without this, a persistently-visible terminal (never released
    // to reset the count) would accumulate losses spread over hours and stay
    // pinned to DOM for the rest of its life even after the pressure eased.
    if (slot?.attachedAt !== undefined && now() - slot.attachedAt > CONTEXT_LOSS_BUDGET_RESET_MS)
      slot.contextLossCount = 0

    // Same teardown as a voluntary eviction (dispose the addon, invalidate any
    // in-flight attach, reset the slot to 'dom'); the loss-count bookkeeping and
    // reschedule below are what make it involuntary.
    doDetach(id)
    if (!slot)
      return
    slot.contextLossCount++
    if (slot.contextLossCount > MAX_CONTEXT_LOSS_RETRIES) {
      slot.ineligible = true
      log.warn('terminal_renderer_webgl_giving_up', { losses: slot.contextLossCount })
    }
    scheduleReconcile()
  }

  return {
    acquire(id, instance, opts) {
      const existing = slots.get(id)
      if (!existing) {
        // First time this id is on-screen: give it a slot on the DOM renderer.
        slots.set(id, { instance, state: 'dom', epoch: 0, ineligible: false, contextLossCount: 0 })
      }
      else if (existing.instance !== instance) {
        // A fresh instance object for a reused id (e.g. the terminal was
        // disposed and recreated before this id's slot settled). Tear down any
        // context the OLD instance still holds and reset its slot to 'dom' --
        // otherwise the stale 'webgl'/'pending' state wedges the new instance
        // out of an attach (reconcile's attach loop only acts on 'dom' slots)
        // and the old context is never detached, leaking a slot and diverging
        // the pool's accounting from reality. doDetach runs against the
        // still-registered old instance, so swap it in only afterwards. Reset
        // eligibility too so the new terminal gets its own attempt.
        if (holdsContext(existing))
          doDetach(id) // detaches the old instance, bumps epoch, resets to 'dom'
        else
          existing.epoch++ // still invalidate any in-flight attach for the old instance
        existing.instance = instance
        existing.ineligible = false
        existing.contextLossCount = 0
      }
      desired.add(id)
      touch(id)
      if (opts?.focused)
        focusedId = id
      else if (focusedId === id)
        focusedId = null
      scheduleReconcile()
    },

    release(id) {
      desired.delete(id)
      removeFromOrder(id)
      // Reset the retry budget so a later re-acquire (e.g. a tab switch back)
      // gets a fresh attempt. The slot itself is dropped by the next reconcile
      // once it has settled back on DOM.
      const slot = slots.get(id)
      if (slot) {
        slot.ineligible = false
        slot.contextLossCount = 0
      }
      if (focusedId === id)
        focusedId = null
      scheduleReconcile()
    },

    has(id) {
      return slots.get(id)?.state === 'webgl'
    },

    size() {
      let count = 0
      for (const slot of slots.values()) {
        if (slot.state === 'webgl')
          count++
      }
      return count
    },

    disposeAll() {
      for (const [id, slot] of slots) {
        if (holdsContext(slot))
          safeDetach(id)
      }
      slots.clear()
      order.length = 0
      desired.clear()
      focusedId = null
      reconcileScheduled = false
    },
  }
}

/**
 * Process-wide pool wired to the real xterm WebGL attach/detach. Shared by
 * every mounted TerminalView so the WebGL context budget is enforced across
 * all tiles and workspaces, not per-view.
 */
export const webglPool: WebglTerminalPool = createWebglTerminalPool({
  capacity: MAX_WEBGL_TERMINAL_CONTEXTS,
  attach: attachWebgl,
  detach: detachWebgl,
})

// During Vite HMR the module is re-evaluated with fresh closure state; detach
// every live context first so the old Terminal objects' renderers are torn
// down cleanly instead of leaking WebGL contexts and firing stray callbacks
// against a half-disposed renderer (mirrors the `instances`-map HMR hook in
// TerminalView).
if (import.meta.hot) {
  import.meta.hot.dispose(() => {
    webglPool.disposeAll()
  })
}
