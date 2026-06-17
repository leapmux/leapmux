import { createSignal, onCleanup } from 'solid-js'

const DEBOUNCE_MS = 1_000
const DEFAULT_TIMEOUT_MS = 10_000

// Shared empty set returned by pendingAxes when an agent has no in-flight axes, so the common
// not-pending path allocates nothing.
const EMPTY_AXES: ReadonlySet<string> = new Set<string>()

export function createLoadingSignal(timeoutMs = DEFAULT_TIMEOUT_MS) {
  const [loading, setLoading] = createSignal(false)
  // Per-agent count of settings changes currently in flight. Tracked separately from
  // the global `loading` boolean (which drives the debounced spinner) so the
  // optimistic-update suppression in useWorkspaceConnection can be scoped per-agent:
  // a change pending on agent A must NOT make agent B's unrelated status push drop
  // B's confirmed current values. `start`/`stop` take the owning agent id; callers
  // that only want the global spinner may omit it.
  //
  // A COUNT, not a set: a single user action can fan out into several concurrent
  // changes for the SAME agent (Codex's "Bypass permissions" button fires three
  // updateAgentSettings RPCs at once), and the user can fire rapid back-to-back
  // changes. With set membership, the FIRST resolving change cleared the shared
  // marker while the others were still in flight, letting a status push overwrite
  // the still-optimistic values mid-batch -- the exact churn the per-agent scoping
  // exists to prevent. Refcounting keeps the agent pending until the LAST change
  // settles.
  const [pendingCounts, setPendingCounts] = createSignal<ReadonlyMap<string, number>>(new Map())
  // Global spinner debounce/timeout. These drive `loading` (the aggregate spinner)
  // ONLY -- they must never touch per-agent pending tracking, or one agent's stop()
  // would cancel another agent's safety net (see pendingTimers).
  let timeoutId: ReturnType<typeof setTimeout> | undefined
  let debounceId: ReturnType<typeof setTimeout> | undefined
  let stopRequested = false
  // Per-agent safety-net timers. Each pending key owns its OWN timeout, keyed by
  // agent id, so a hung RPC for one agent clears only that agent -- never another's
  // pending state. A single shared timer here was wrong: a keyless stop() (fired on
  // every unrelated agent's status push) cancelled it, and the timer's fire wiped
  // EVERY pending key at once, both of which defeated the per-agent scoping.
  const pendingTimers = new Map<string, ReturnType<typeof setTimeout>>()
  // Per-agent, per-axis pending counts: which option axes each agent currently has an in-flight
  // optimistic change for. Parallel to pendingCounts (the per-agent change refcount that drives the
  // spinner and isPending): the spinner only needs "is this agent pending at all", while the
  // optimistic-update suppression in useWorkspaceConnection needs to know WHICH axes are pending so a
  // server-initiated change to an UNRELATED axis on the same agent isn't suppressed. Kept in lockstep
  // with pendingCounts -- start/stop pass the change's axis ids and bump/drop both. A plain Map (not
  // a signal): it is read imperatively at status-push time, never inside a reactive tracking scope.
  const pendingAxisCounts = new Map<string, Map<string, number>>()

  const clearSpinnerTimers = () => {
    clearTimeout(timeoutId)
    clearTimeout(debounceId)
    timeoutId = undefined
    debounceId = undefined
  }

  const clearPendingTimer = (key: string) => {
    const existing = pendingTimers.get(key)
    if (existing !== undefined) {
      clearTimeout(existing)
      pendingTimers.delete(key)
    }
  }

  const incrementPending = (key: string) => setPendingCounts((prev) => {
    const next = new Map(prev)
    next.set(key, (next.get(key) ?? 0) + 1)
    return next
  })

  // Decrement one in-flight change for `key`, deleting the entry at zero. Returns the
  // remaining count for `key` AND the total number of still-pending keys, BOTH computed
  // inside the signal-write updater (which Solid runs synchronously to produce the new
  // value). `stop` decides from these returned values rather than a follow-up
  // pendingCounts() read -- a read-after-write a Solid batch() could leave observing the
  // stale pre-decrement map, hiding the spinner while a change is still pending or never
  // clearing it after the last one settles.
  const decrementPending = (key: string): { remaining: number, totalKeys: number } => {
    let remaining = 0
    let totalKeys = 0
    setPendingCounts((prev) => {
      const cur = prev.get(key) ?? 0
      if (cur <= 0) {
        totalKeys = prev.size
        return prev
      }
      const next = new Map(prev)
      if (cur === 1)
        next.delete(key)
      else
        next.set(key, cur - 1)
      remaining = cur - 1
      totalKeys = next.size
      return next
    })
    return { remaining, totalKeys }
  }

  // Force-clear ALL in-flight changes for `key` (the safety-net timeout path): a
  // hung RPC must not strand the agent as pending forever, so the timeout abandons
  // the whole refcount rather than decrementing one.
  const clearPending = (key: string) => {
    pendingAxisCounts.delete(key)
    setPendingCounts((prev) => {
      if (!prev.has(key))
        return prev
      const next = new Map(prev)
      next.delete(key)
      return next
    })
  }

  // Bump each axis's pending count for `key`. Mutates the per-agent inner map in place (no
  // reactivity: pendingAxes is read imperatively, not tracked).
  const incrementAxes = (key: string, axes: readonly string[]) => {
    if (axes.length === 0)
      return
    const counts = pendingAxisCounts.get(key) ?? new Map<string, number>()
    for (const axis of axes)
      counts.set(axis, (counts.get(axis) ?? 0) + 1)
    pendingAxisCounts.set(key, counts)
  }

  // Drop one pending count per axis for `key`, removing an axis at zero and the agent's whole
  // entry once it has no pending axes left.
  const decrementAxes = (key: string, axes: readonly string[]) => {
    const counts = pendingAxisCounts.get(key)
    if (!counts)
      return
    for (const axis of axes) {
      const n = counts.get(axis) ?? 0
      if (n <= 1)
        counts.delete(axis)
      else
        counts.set(axis, n - 1)
    }
    if (counts.size === 0)
      pendingAxisCounts.delete(key)
  }

  const start = (key?: string, axes: readonly string[] = []) => {
    stopRequested = false
    setLoading(true)
    if (key !== undefined) {
      incrementPending(key)
      incrementAxes(key, axes)
      // (Re)arm THIS agent's safety net: a change whose stop() never arrives (hung
      // RPC) must not strand its agent as permanently pending. Scoped to `key` so it
      // can't clear another agent that is legitimately still in flight. Re-arming on
      // each start extends the window to cover the latest in-flight change.
      clearPendingTimer(key)
      pendingTimers.set(key, setTimeout(() => {
        pendingTimers.delete(key)
        clearPending(key)
      }, timeoutMs))
    }
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
    // Keep the aggregate spinner alive while ANY agent still has a change in flight. The
    // spinner timers are shared across keys, so without this guard an unrelated agent's
    // stop() -- or a keyless status-push stop -- would arm stopRequested (or clear the
    // spinner outright) while another agent's change is still pending, hiding the spinner
    // early. The global timeoutId remains the backstop if a count were ever stranded.
    // pendingCounts deletes a key at zero, so a non-zero key count means some agent is
    // still pending.
    let stillPending: number
    if (key !== undefined) {
      const { remaining, totalKeys } = decrementPending(key)
      decrementAxes(key, axes)
      // Clear the safety net only once the LAST in-flight change for this agent settles;
      // while other concurrent changes for it remain, the agent stays pending and its
      // timer keeps guarding them.
      if (remaining === 0) {
        clearPendingTimer(key)
        // No changes remain for this agent, so no axes should either; drop any straggler entry
        // defensively (a mismatched start/stop axis list would otherwise strand an axis as pending).
        pendingAxisCounts.delete(key)
      }
      // totalKeys comes from the same write that decremented, so it is correct even when
      // stop() runs inside a Solid batch() (which would defer a follow-up read).
      stillPending = totalKeys
    }
    else {
      // A keyless stop performs no write, so reading the current map size is safe.
      stillPending = pendingCounts().size
    }
    if (stillPending > 0)
      return
    if (debounceId) {
      stopRequested = true
    }
    else {
      setLoading(false)
      clearSpinnerTimers()
      stopRequested = false
    }
  }

  /** Whether at least one settings change is in flight for `key` (a specific agent). */
  const isPending = (key: string) => (pendingCounts().get(key) ?? 0) > 0

  /**
   * The option axes `key` (a specific agent) currently has an in-flight optimistic change for.
   * useWorkspaceConnection consults this so a server status push applies the confirmed current
   * value for every NON-pending axis while leaving the pending ones for the in-flight RPC to
   * resolve -- a per-axis refinement of isPending's per-agent suppression. Returns an empty set
   * when the agent has none.
   */
  const pendingAxes = (key: string): ReadonlySet<string> => {
    const counts = pendingAxisCounts.get(key)
    return counts ? new Set(counts.keys()) : EMPTY_AXES
  }

  onCleanup(() => {
    clearSpinnerTimers()
    for (const timer of pendingTimers.values())
      clearTimeout(timer)
    pendingTimers.clear()
    pendingAxisCounts.clear()
  })
  return { loading, isPending, pendingAxes, start, stop }
}
