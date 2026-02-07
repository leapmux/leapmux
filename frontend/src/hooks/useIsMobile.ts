import { createSignal, onCleanup, onMount } from 'solid-js'
import { breakpoints } from '~/styles/tokens'

export function useIsMobile() {
  const [isMobile, setIsMobile] = createSignal(
    typeof window !== 'undefined'
      ? window.innerWidth < breakpoints.mobile
      : false,
  )

  onMount(() => {
    const mq = window.matchMedia(`(max-width: ${breakpoints.mobile - 1}px)`)
    setIsMobile(mq.matches)

    const handler = (e: MediaQueryListEvent) => setIsMobile(e.matches)
    mq.addEventListener('change', handler)
    onCleanup(() => mq.removeEventListener('change', handler))
  })

  return isMobile
}
