import { createSignal } from 'solid-js'

/**
 * createFileTabPathsStore is the (tab_id → path) cache fed by the
 * private-event stream and one-shot `GetFileTabPath` E2EE worker
 * RPCs. The hub never sees these paths; everything flows over the
 * existing WatchWorkerPrivateEvents channel.
 */
export function createFileTabPathsStore() {
  const [byTabId, setByTabId] = createSignal<Map<string, string>>(new Map())

  return {
    /** Reactive accessor for components. */
    snapshot: byTabId,

    /** Path for `tabId`, or `undefined` if not yet known. */
    pathFor(tabId: string): string | undefined {
      return byTabId().get(tabId)
    },

    /**
     * Apply a `FileTabPathRegistered` event (or a one-shot
     * `GetFileTabPath` reply). Idempotent.
     */
    register(tabId: string, path: string): void {
      if (byTabId().get(tabId) === path)
        return
      const next = new Map(byTabId())
      next.set(tabId, path)
      setByTabId(next)
    },

    /** Apply a `FileTabPathRevoked` event. */
    revoke(tabId: string): void {
      const cur = byTabId()
      if (!cur.has(tabId))
        return
      const next = new Map(cur)
      next.delete(tabId)
      setByTabId(next)
    },

    /** Drop every entry (logout / empty account). */
    clear(): void {
      setByTabId(new Map())
    },
  }
}

export type FileTabPathsStore = ReturnType<typeof createFileTabPathsStore>
