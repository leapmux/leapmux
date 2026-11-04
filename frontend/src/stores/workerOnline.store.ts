import { createSignal, untrack } from 'solid-js'

/**
 * The workers whose watch stream this client lost.
 *
 * `useWatchEventsStreams` reports each change, and `useWorkspaceConnection`
 * writes it here. A reader of a tab combines this answer with the status that
 * the worker reported (see `terminalDisplayStatus`), so no tab holds a copy
 * that can outlive an outage.
 *
 * This is the view of THIS client. `~/lib/workerLiveness` holds the view of the
 * hub, from the worker list. The two can differ: a client can lose its stream
 * to a worker that stays connected to the hub.
 *
 * AppShell creates one store for each shell mount, so a sign-out drops it.
 */
export function createWorkerOnlineStore() {
  const [offlineIds, setOfflineIds] = createSignal<ReadonlySet<string>>(new Set())

  return {
    /** The workers that this client reported offline. Reactive. */
    offlineIds,
    /**
     * Whether this client receives the events of `workerId`. Reactive.
     *
     * Only a report takes a worker offline, so a worker that nothing reported
     * and the empty id of a tab with no worker both read as online.
     */
    isOnline: (workerId: string): boolean => !offlineIds().has(workerId),
    /**
     * Record that `workerId` went offline or came back, and return whether its
     * state changed.
     *
     * A report of the current state keeps the same set. Every live agent event
     * and every agent status reports its worker online, and a new set for each
     * of them would run each reader again for every event while any worker is
     * offline.
     *
     * A removed tab reports an empty id, which stays outside the set.
     */
    setOnline: (workerId: string, online: boolean): boolean => {
      if (!workerId)
        return false
      if (online !== untrack(offlineIds).has(workerId))
        return false
      setOfflineIds((prev) => {
        const next = new Set(prev)
        if (online)
          next.delete(workerId)
        else
          next.add(workerId)
        return next
      })
      return true
    },
  }
}

export type WorkerOnlineStore = ReturnType<typeof createWorkerOnlineStore>
