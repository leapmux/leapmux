import { createEffect, on, onCleanup } from 'solid-js'

export interface DragTeardownHandle {
  /**
   * Store the in-flight drag's teardown. Pass `null` when a drag finishes
   * naturally (its own `onDone` already cleaned up — we just drop the
   * dangling reference).
   */
  set: (teardown: (() => void) | null) => void
}

/**
 * Track a single in-flight drag's teardown and auto-cancel it when any
 * of `structuralKeys` changes or the owner unmounts.
 *
 * `SplitRenderer` and `GridRenderer` use this to abort a live ratio drag
 * if the underlying tree mutates (a sibling closes, the split flips
 * direction, …) — the drag's captured `startRatios` would be stale, so
 * we drop the gesture without committing.
 *
 * Caller usage:
 *
 *     const handle = createDragTeardownHandle(() => [s().id, s().ratios])
 *     const cancel = startPairRebalanceDrag({
 *       ...,
 *       onDone: () => handle.set(null),
 *     })
 *     handle.set(cancel)
 *
 * `defer: true` on the structural-keys effect keeps the initial
 * subscription from firing on mount; the only valid trigger is a real
 * post-mount mutation while a drag is in flight.
 */
export function createDragTeardownHandle(
  structuralKeys: () => unknown[],
): DragTeardownHandle {
  let active: (() => void) | null = null

  const cancel = () => {
    active?.()
    active = null
  }

  createEffect(on(structuralKeys, cancel, { defer: true }))
  onCleanup(cancel)

  return {
    set: (teardown) => { active = teardown },
  }
}
