import { createSignal, onCleanup } from 'solid-js'

const DEBOUNCE_MS = 1_000
const DEFAULT_TIMEOUT_MS = 10_000

export function createLoadingSignal(timeoutMs = DEFAULT_TIMEOUT_MS) {
  const [loading, setLoading] = createSignal(false)
  let timeoutId: ReturnType<typeof setTimeout> | undefined
  let debounceId: ReturnType<typeof setTimeout> | undefined
  let stopRequested = false

  const clearTimers = () => {
    clearTimeout(timeoutId)
    clearTimeout(debounceId)
    timeoutId = undefined
    debounceId = undefined
  }

  const start = () => {
    stopRequested = false
    setLoading(true)
    clearTimers()
    debounceId = setTimeout(() => {
      debounceId = undefined
      if (stopRequested) {
        setLoading(false)
        clearTimers()
        stopRequested = false
      }
    }, DEBOUNCE_MS)
    timeoutId = setTimeout(() => {
      setLoading(false)
      clearTimers()
      stopRequested = false
    }, timeoutMs)
  }

  const stop = () => {
    if (debounceId) {
      stopRequested = true
    }
    else {
      setLoading(false)
      clearTimers()
      stopRequested = false
    }
  }

  onCleanup(clearTimers)
  return { loading, start, stop }
}
