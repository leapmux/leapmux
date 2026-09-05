import type { Accessor } from 'solid-js'
import type { BusyTab } from './tabBusyProbe'
import type { Tab } from '~/stores/tab.types'
import { createSignal } from 'solid-js'

/**
 * Per-flow plan for one close action (close-tile, close-floating-window,
 * close-grid). `plan(ctx)` builds this once at request time; the dialog's
 * preserve/close-all buttons then call into the closures.
 *
 * `tabs` is the snapshot to iterate during close-all. The list is captured
 * once because per-tab close mutates the source — floating-window auto-
 * cleanup may dispose the source mid-loop.
 *
 * `preserve` and `finalize` close over open-time state. The dialog blocks
 * other UI during its lifetime, so capturing once is safe.
 *
 * `finalize` MUST be idempotent: it runs both for the empty-preflight short-
 * circuit and after the close-all loop, where auto-cleanup may have already
 * disposed the structure being closed.
 */
export interface ClosePlan {
  tabs: Tab[]
  preserve: () => void
  finalize: () => void
}

/**
 * Dialog-facing API for one close flow. `request(ctx)` is the entry point —
 * it builds the plan, short-circuits to `finalize()` if the closeable is
 * empty, otherwise opens the dialog. The remaining methods drive the dialog
 * buttons.
 *
 * `busy` is exposed as a separate accessor (rather than living on `Ctx`) so
 * call-site context types stay clean — only the flow itself owns this state.
 */
export interface CloseFlow<Ctx> {
  signal: Accessor<Ctx | null>
  busy: Accessor<boolean>
  /**
   * The tabs in this plan that are running work, scanned once before the dialog
   * opened. Asked and answered UP FRONT so the user decides about the whole
   * closeable at once, rather than being interrupted per tab partway through a
   * close that has already destroyed some of them.
   */
  busyTabs: Accessor<BusyTab[]>
  request: (ctx: Ctx) => Promise<void>
  cancel: () => void
  primary: () => void
  closeAll: () => Promise<void>
}

export interface CloseFlowOptions<Ctx> {
  /** Build the plan for `ctx`. Called once per `request()`. */
  plan: (ctx: Ctx) => ClosePlan
  /** Per-tab close. Returns false to bail (user cancelled). */
  handleTabClose: (tab: Tab, opts?: { skipBusyConfirm?: boolean }) => Promise<boolean>
  /** Which of these tabs are running work. Awaited before the dialog opens. */
  probeBusy: (tabs: readonly Tab[]) => Promise<BusyTab[]>
}

/**
 * Convenience builder for the "preserve = merge step then dispose;
 * finalize = dispose alone" shape used by close-tile and close-floating-
 * window flows. The grid flow doesn't use this because its preserve path
 * replaces structure rather than merging into existing tiles.
 *
 * `dispose` MUST be idempotent (see {@link ClosePlan.finalize}); it runs
 * once for the empty-preflight short-circuit and again after preserve or
 * the close-all loop completes.
 */
export function closePlanWithDispose(opts: {
  tabs: Tab[]
  merge: () => void
  dispose: () => void
}): ClosePlan {
  return {
    tabs: opts.tabs,
    preserve: () => {
      opts.merge()
      opts.dispose()
    },
    finalize: opts.dispose,
  }
}

export function createCloseFlow<Ctx>(opts: CloseFlowOptions<Ctx>): CloseFlow<Ctx> {
  // Single source of truth: ctx and plan move together. The public `signal`
  // accessor projects out only the ctx so call sites keep their typed view.
  const [active, setActive] = createSignal<{ ctx: Ctx, plan: ClosePlan } | null>(null)
  const [busy, setBusy] = createSignal(false)
  const [busyTabs, setBusyTabs] = createSignal<BusyTab[]>([])

  const clear = () => {
    setActive(null)
    setBusy(false)
    setBusyTabs([])
  }

  return {
    signal: () => active()?.ctx ?? null,
    busy,
    busyTabs,
    async request(ctx) {
      const plan = opts.plan(ctx)
      // Empty closeable: skip the dialog and finalize directly.
      if (plan.tabs.length === 0) {
        plan.finalize()
        return
      }
      // Scan BEFORE opening, so the dialog is complete the moment it appears.
      // Opening first and patching the warning in would need a loading state and
      // a disabled "Close all tabs" -- and would still leave a window where the
      // user could confirm before the warning arrived.
      setBusyTabs(await opts.probeBusy(plan.tabs))
      setActive({ ctx, plan })
    },
    cancel: clear,
    primary() {
      const a = active()
      // Defensive busy-guard: the dialog disables the primary while busy,
      // but a keyboard-activate race could still fire it.
      if (!a || busy())
        return
      a.plan.preserve()
      clear()
    },
    async closeAll() {
      const a = active()
      if (!a || busy())
        return
      setBusy(true)
      // Sequential by design — `handleTabClose` may surface a worktree
      // confirmation dialog (only one can be on-screen at a time) and runs
      // `inspectLastTabClose`, whose "is this the worker's last tab?" answer
      // depends on the running tab count. Closing in parallel would race
      // dialogs and could mis-classify which close is the last-tab one.
      for (const tab of a.plan.tabs) {
        // The dialog already listed every busy tab and the user answered for all
        // of them, so the per-tab prompt would ask the same question twice. The
        // per-tab WORKTREE prompt still runs: that one asks something this
        // dialog never covered.
        const ok = await opts.handleTabClose(tab, { skipBusyConfirm: true })
        if (!ok) {
          // Bail without finalizing so the user can retry; another cancel()
          // could have raced the await, so guard the busy reset on still-active.
          if (active())
            setBusy(false)
          return
        }
      }
      a.plan.finalize()
      clear()
    },
  }
}
