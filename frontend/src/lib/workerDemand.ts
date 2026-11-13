import type { Accessor } from 'solid-js'
import { createSignal } from 'solid-js'

// ---------------------------------------------------------------------------
// Demand on a shared worker request
//
// The reactive worker caches (the Markdown renders and the edit diffs) merge
// equal requests: every consumer of one text shares one worker request. The
// priority gate (createWorkerPriorityGate) asks the request, at each dispatch
// opportunity, whether it may wait behind other jobs. These helpers answer for
// all of its consumers at once, and re-run the consumers when replies land.
// ---------------------------------------------------------------------------

/**
 * The most priority thunks a demand keeps. Each re-run of a consumer's memo notes
 * its thunk again, as a new closure, so the demand keeps only the recent ones. A
 * consumer that still waits re-runs on every reply, so its latest thunk stays
 * among the recent ones while fewer consumers than this share the request.
 */
const MAX_TRACKED_CONSUMERS = 32

/** The demand that the consumers of one shared worker request put on it. */
export interface WorkerDemand {
  /**
   * Record a consumer that waits for the request. A consumer with no thunk needs
   * it now, so the request stops being low for good. A thunk states whether that
   * consumer may wait (its row is offscreen).
   */
  note: (isLowPriority?: () => boolean) => void
  /**
   * Whether the gate may hold the request behind other jobs: no consumer needs
   * it now, and every recent consumer's thunk says it may wait. A request that
   * only a prefetch made has no consumer, so it is low.
   */
  isLow: () => boolean
}

export function createWorkerDemand(): WorkerDemand {
  let urgent = false
  const thunks = new Set<() => boolean>()
  return {
    note(isLowPriority) {
      if (isLowPriority === undefined) {
        urgent = true
        return
      }
      thunks.delete(isLowPriority)
      thunks.add(isLowPriority)
      if (thunks.size > MAX_TRACKED_CONSUMERS) {
        const [oldest] = thunks
        if (oldest !== undefined)
          thunks.delete(oldest)
      }
    },
    isLow() {
      if (urgent)
        return false
      for (const thunk of thunks) {
        if (!thunk())
          return false
      }
      return true
    },
  }
}

/** A version signal whose bumps within one task coalesce into one. */
export interface CoalescedVersion {
  /** The version. Read it in a memo to re-run that memo after each bump. */
  read: Accessor<number>
  /**
   * Bump the version at the next microtask. A burst of replies within one task
   * re-runs each reader once, not once per reply.
   */
  bump: () => void
}

export function createCoalescedVersion(): CoalescedVersion {
  const [read, setVersion] = createSignal(0)
  let scheduled = false
  return {
    read,
    bump() {
      if (scheduled)
        return
      scheduled = true
      queueMicrotask(() => {
        scheduled = false
        setVersion(v => v + 1)
      })
    },
  }
}
