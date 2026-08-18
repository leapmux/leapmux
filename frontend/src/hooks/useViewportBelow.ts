import { createSignal, onCleanup, onMount } from 'solid-js'

/**
 * Whether the viewport is narrower than `px`, as a signal.
 *
 * One hook for every breakpoint the layout switches at, because the listener
 * and its removal must stay together. The Preferences dialog re-implemented
 * this at `sm` and omitted the `onCleanup`, so every reopen added a `change`
 * handler whose owner was already disposed.
 *
 * The initial value comes from `window.innerWidth` rather than from the
 * query, so a caller reads the correct answer before the first mount — and
 * in jsdom, which implements `innerWidth` but not `matchMedia`, that read is
 * the only answer available.
 */
export function useViewportBelow(px: number): () => boolean {
  const [below, setBelow] = createSignal(
    typeof window !== 'undefined' ? window.innerWidth < px : false,
  )

  onMount(() => {
    // jsdom has no matchMedia; the initial innerWidth read above already
    // answered the question there.
    if (typeof window.matchMedia !== 'function')
      return
    const mq = window.matchMedia(`(max-width: ${px - 1}px)`)
    setBelow(mq.matches)
    const handler = (e: MediaQueryListEvent) => setBelow(e.matches)
    mq.addEventListener('change', handler)
    onCleanup(() => mq.removeEventListener('change', handler))
  })

  return below
}
