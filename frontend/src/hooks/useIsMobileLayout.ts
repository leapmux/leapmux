import { createSignal, onCleanup, onMount } from 'solid-js'
import { breakpoints } from '~/styles/tokens'

export function useIsMobileLayout() {
  const [isMobileLayout, setIsMobileLayout] = createSignal(
    typeof window !== 'undefined'
      ? window.innerWidth < breakpoints.md
      : false,
  )

  onMount(() => {
    const mq = window.matchMedia(`(max-width: ${breakpoints.md - 1}px)`)
    setIsMobileLayout(mq.matches)

    const handler = (e: MediaQueryListEvent) => setIsMobileLayout(e.matches)
    mq.addEventListener('change', handler)
    onCleanup(() => mq.removeEventListener('change', handler))
  })

  return isMobileLayout
}
