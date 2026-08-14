import type { Accessor } from 'solid-js'
import { createSignal, onCleanup, onMount } from 'solid-js'

export interface SharedTicker {
  /**
   * Subscribe the calling component to the ticker for its lifetime. Call it in
   * the component body; it registers its own `onMount` / `onCleanup`.
   */
  subscribe: () => void
  /** Read inside a tracking scope to re-run on each tick. */
  tick: Accessor<number>
}

/**
 * ONE interval shared by every subscriber, instead of one timer each.
 *
 * Components that re-render on a cadence — a relative timestamp, an elapsed
 * counter — all recompute the same way at the same moment, so a timer apiece
 * only multiplies the callbacks. A chat view renders a timestamp per message,
 * and the file tree keeps a three-dot menu mounted per row.
 *
 * The interval runs only while at least one subscriber is mounted. A subscriber
 * whose input is not yet displayable subscribes too: its prop is reactive and
 * can become valid later, and one shared timer costs far less than making the
 * subscription itself reactive.
 *
 * Each ticker owns an independent counter and timer, so two cadences never
 * share state.
 */
export function createSharedTicker(intervalMs: number): SharedTicker {
  const [tick, setTick] = createSignal(0)
  let timer: ReturnType<typeof setInterval> | undefined
  let subscribers = 0

  const subscribe = (): void => {
    // `subscribed` pairs the decrement to its own increment. Solid runs
    // `onCleanup` whenever it disposes the owner — including before the effect
    // queue ever flushes `onMount` — and an unpaired decrement would drive this
    // counter negative, after which `=== 1` never matches again and no
    // subscriber gets a timer for the rest of the session.
    let subscribed = false
    onMount(() => {
      subscribed = true
      subscribers++
      if (subscribers === 1)
        timer = setInterval(() => setTick(t => t + 1), intervalMs)
    })
    onCleanup(() => {
      if (!subscribed)
        return
      subscribed = false
      subscribers--
      if (subscribers === 0 && timer !== undefined) {
        clearInterval(timer)
        timer = undefined
      }
    })
  }

  return { subscribe, tick }
}
