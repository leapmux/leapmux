import { createSignal, onCleanup, untrack } from 'solid-js'

const DEBOUNCE_MS = 1_000
const DEFAULT_TIMEOUT_MS = 10_000

// Reuse the empty result when an agent has no pending axes.
const EMPTY_AXES: ReadonlySet<string> = new Set<string>()

interface PendingAgent {
  count: number
  axes: ReadonlyMap<string, number>
  timer: ReturnType<typeof setTimeout>
}

export function createLoadingSignal(timeoutMs = DEFAULT_TIMEOUT_MS) {
  const [loading, setLoading] = createSignal(false)
  // Each agent owns its pending counts and timeout in one record.
  // Concurrent changes keep the agent pending until the last change completes.
  // Axis counts protect optimistic values without suppressing unrelated status updates.
  const [pendingAgents, setPendingAgents] = createSignal<ReadonlyMap<string, PendingAgent>>(new Map())
  // The global spinner timers never clear an agent's pending state.
  // An unrelated status update must not cancel another agent's timeout.
  let timeoutId: ReturnType<typeof setTimeout> | undefined
  let debounceId: ReturnType<typeof setTimeout> | undefined
  let stopRequested = false

  const clearSpinnerTimers = () => {
    clearTimeout(timeoutId)
    clearTimeout(debounceId)
    timeoutId = undefined
    debounceId = undefined
  }

  const startPending = (key: string, axes: readonly string[]) => {
    setPendingAgents((prev) => {
      const current = prev.get(key)
      clearTimeout(current?.timer)
      const axisCounts = new Map(current?.axes)
      for (const axis of axes)
        axisCounts.set(axis, (axisCounts.get(axis) ?? 0) + 1)
      // Restart this agent's timeout to cover the latest change.
      // A timeout clears every pending change for this agent only.
      const timer = setTimeout(() => {
        setPendingAgents((agents) => {
          if (agents.get(key)?.timer !== timer)
            return agents
          const next = new Map(agents)
          next.delete(key)
          return next
        })
      }, timeoutMs)
      const next = new Map(prev)
      next.set(key, { count: (current?.count ?? 0) + 1, axes: axisCounts, timer })
      return next
    })
  }

  // Return the number of pending agents from the update that decrements the change count.
  // This keeps the spinner decision consistent with the pending state in a batch.
  const stopPending = (key: string, axes: readonly string[]): number => {
    let totalKeys = 0
    setPendingAgents((prev) => {
      const current = prev.get(key)
      if (!current) {
        totalKeys = prev.size
        return prev
      }
      const next = new Map(prev)
      if (current.count === 1) {
        clearTimeout(current.timer)
        // Removing the whole record also clears axes from a mismatched stop call.
        next.delete(key)
      }
      else {
        const axisCounts = new Map(current.axes)
        for (const axis of axes) {
          const count = axisCounts.get(axis) ?? 0
          if (count <= 1)
            axisCounts.delete(axis)
          else
            axisCounts.set(axis, count - 1)
        }
        next.set(key, { ...current, count: current.count - 1, axes: axisCounts })
      }
      totalKeys = next.size
      return next
    })
    return totalKeys
  }

  const start = (key?: string, axes: readonly string[] = []) => {
    stopRequested = false
    setLoading(true)
    if (key !== undefined)
      startPending(key, axes)
    clearSpinnerTimers()
    debounceId = setTimeout(() => {
      debounceId = undefined
      if (stopRequested) {
        setLoading(false)
        clearSpinnerTimers()
        stopRequested = false
      }
    }, DEBOUNCE_MS)
    timeoutId = setTimeout(() => {
      setLoading(false)
      clearSpinnerTimers()
      stopRequested = false
    }, timeoutMs)
  }

  const stop = (key?: string, axes: readonly string[] = []) => {
    // Keep the spinner visible until every agent completes its pending changes.
    // A keyless stop changes no agent record. The global timeout limits the spinner duration.
    const stillPending = key === undefined ? pendingAgents().size : stopPending(key, axes)
    if (stillPending > 0)
      return
    if (debounceId !== undefined) {
      stopRequested = true
    }
    else {
      setLoading(false)
      clearSpinnerTimers()
      stopRequested = false
    }
  }

  /** Return whether this agent has at least one pending settings change. */
  const isPending = (key: string) => pendingAgents().has(key)

  /**
   * Return the axes with pending optimistic changes for this agent.
   * useWorkspaceConnection applies confirmed values only to the other axes.
   * The function reads without a subscription because status handlers consult this state directly.
   */
  const pendingAxes = (key: string): ReadonlySet<string> => {
    const axes = untrack(pendingAgents).get(key)?.axes
    return axes?.size ? new Set(axes.keys()) : EMPTY_AXES
  }

  onCleanup(() => {
    clearSpinnerTimers()
    for (const pending of untrack(pendingAgents).values())
      clearTimeout(pending.timer)
    setPendingAgents(new Map())
  })
  return { loading, isPending, pendingAxes, start, stop }
}
