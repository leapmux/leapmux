import { createSignal } from 'solid-js'

/**
 * createFileTabPathsStore is the (tab_id → path) cache fed by the
 * private-event stream and one-shot `GetFileTabPath` E2EE worker
 * RPCs. The hub never sees these paths; everything flows over the
 * existing WatchWorkspacePrivateEvents channel.
 */
export function createFileTabPathsStore() {
  const [byTabId, setByTabId] = createSignal<Map<string, FileTabPathEntry>>(new Map())

  return {
    /** Reactive accessor for components. */
    snapshot: byTabId,

    /** Path for `tabId`, or `undefined` if not yet known. */
    pathFor(tabId: string): string | undefined {
      return byTabId().get(tabId)?.path
    },

    /** Workspace_id the worker associated with the tab. */
    workspaceFor(tabId: string): string | undefined {
      return byTabId().get(tabId)?.workspaceId
    },

    /**
     * Apply a `FileTabPathRegistered` event (or a one-shot
     * `GetFileTabPath` reply). Idempotent.
     */
    register(tabId: string, workspaceId: string, path: string): void {
      const cur = byTabId().get(tabId)
      if (cur && cur.path === path && cur.workspaceId === workspaceId)
        return
      const next = new Map(byTabId())
      next.set(tabId, { workspaceId, path })
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

    /** Drop every entry (workspace switch / org switch). */
    clear(): void {
      setByTabId(new Map())
    },
  }
}

export interface FileTabPathEntry {
  workspaceId: string
  path: string
}

export type FileTabPathsStore = ReturnType<typeof createFileTabPathsStore>
