import { createEffect, createSignal, onCleanup, untrack } from 'solid-js'
import { createRafResizeObserver } from '~/lib/resizeObserver'

/**
 * Track whether a box clips something while its row is collapsed, so the row offers
 * Expand for it. The header title (useHeaderTitleOverflow) and the command summary
 * (useCollapsedSummaryOverflow) each give it the test of their own box; the rules live
 * here, once:
 *
 *  - It measures the COLLAPSED box only. An expanded box clips nothing, and reporting
 *    `false` then would remove the fact that made the row expandable, so the control
 *    would disappear while the row stays expanded. It keeps the last collapsed answer
 *    until the box collapses again.
 *  - A box that MOUNTS expanded has no collapsed answer yet -- the expand state of a row
 *    outlives a remount -- so it reports a clip until the box collapses: the row then
 *    keeps the control that collapses it.
 *  - It measures again when the box resizes, and when `watch` asks: each caller knows
 *    what else changes its box.
 *  - It reports a change only.
 *
 * Call it within the row's reactive owner.
 */
export function useCollapsedOverflow(props: {
  collapsed: () => boolean
  /** Whether the collapsed box clips anything. */
  clips: (element: HTMLElement) => boolean
  /**
   * Start what else measures the collapsed box again. It runs in a reactive scope of its
   * own while the box is collapsed, so it can track what changes the box and register
   * its cleanup with `onCleanup`, and a change that it tracks keeps the size observer.
   */
  watch?: (element: HTMLElement, measure: () => void) => void
  onOverflowChange?: (overflowing: boolean) => void
}): {
  overflowing: () => boolean
  ref: (element: HTMLElement) => void
} {
  const [box, setBox] = createSignal<HTMLElement>()
  const [overflowing, setOverflowing] = createSignal(false)
  // The functional setter reads the previous value without tracking it, so a scope that
  // measures does not depend on the signal that it writes.
  const report = (next: boolean): void => {
    setOverflowing((previous) => {
      if (previous !== next)
        props.onOverflowChange?.(next)
      return next
    })
  }
  // Whether the collapsed box was measured since the hook mounted.
  let measured = false
  const measure = (): void => {
    const element = box()
    if (element === undefined || !props.collapsed())
      return
    measured = true
    report(props.clips(element))
  }
  createEffect(() => {
    const element = box()
    if (element === undefined)
      return
    if (!props.collapsed()) {
      if (!measured)
        report(true)
      return
    }
    untrack(measure)
    const resize = createRafResizeObserver(() => measure())
    resize?.observe(element)
    onCleanup(() => resize?.disconnect())
    const watch = props.watch
    if (watch !== undefined)
      createEffect(() => watch(element, measure))
  })
  return { overflowing, ref: setBox }
}
