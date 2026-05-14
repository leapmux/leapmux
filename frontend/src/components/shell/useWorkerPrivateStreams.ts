import type { createFileTabPathsStore } from '~/lib/fileTabPaths'
import type { createTabStore } from '~/stores/tab.store'
import { createEffect, createMemo, onCleanup } from 'solid-js'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { openWorkerPrivateEventStream } from '~/lib/workspacePrivateEvents'
import { isFileTab } from '~/stores/tab.types'

/**
 * Open one WatchWorkspacePrivateEvents subscription per (worker
 * hosting a tab in the active workspace × workspace). The worker
 * emits a bootstrap reply (one FileTabPathRegistered per existing
 * worker_file_tabs row) before going live; subsequent FileTabPath*
 * events populate the local file-tab path cache. Streams are torn
 * down when the active workspace changes or a worker stops hosting
 * any tab.
 *
 * The effect's reactive deps are gated through `activeWorkerSnapshot`
 * so a rename / position bump on an unrelated tab doesn't tear down
 * and reopen every worker's private-event stream. Only changes to
 * (activeWorkspaceId, set-of-active-worker-ids) reach the effect.
 */
export interface UseWorkerPrivateStreamsOpts {
  getActiveWorkspaceId: () => string | null | undefined
  tabStore: ReturnType<typeof createTabStore>
  fileTabPaths: ReturnType<typeof createFileTabPathsStore>
}

export function useWorkerPrivateStreams(opts: UseWorkerPrivateStreamsOpts): void {
  const privateStreamCleanups = new Map<string, () => void>()

  // Tear down on owner-component dispose (HMR, route teardown). Without
  // this the WebSocket streams outlive the AppShell that created them.
  onCleanup(() => {
    for (const close of privateStreamCleanups.values())
      close()
    privateStreamCleanups.clear()
  })

  const activeWorkerSnapshot = createMemo<{ wsId: string, desired: Set<string>, key: string } | null>(() => {
    const wsId = opts.getActiveWorkspaceId()
    if (!wsId)
      return null
    const desired = new Set<string>()
    for (const tab of opts.tabStore.state.tabs) {
      if (tab.workerId && tab.tileId)
        desired.add(tab.workerId)
    }
    const key = Array.from(desired).sort().join('')
    return { wsId, desired, key }
  }, null, {
    equals: (prev, next) => {
      if (!prev || !next)
        return prev === next
      return prev.wsId === next.wsId && prev.key === next.key
    },
  })

  createEffect(() => {
    const snap = activeWorkerSnapshot()
    if (!snap) {
      for (const close of privateStreamCleanups.values())
        close()
      privateStreamCleanups.clear()
      opts.fileTabPaths.clear()
      return
    }
    const { wsId, desired } = snap
    // Drop streams for workers no longer hosting tabs.
    const prefix = `${wsId}::`
    for (const [key, close] of privateStreamCleanups.entries()) {
      if (!key.startsWith(prefix))
        continue
      const workerId = key.slice(prefix.length)
      if (!desired.has(workerId)) {
        close()
        privateStreamCleanups.delete(key)
      }
    }
    // Open streams for newly-hosting workers.
    for (const workerId of desired) {
      const key = `${prefix}${workerId}`
      if (privateStreamCleanups.has(key))
        continue
      const close = openWorkerPrivateEventStream({
        workspaceId: wsId,
        workerId,
        onTabRenamed: (evt) => {
          opts.tabStore.updateTabTitle(evt.tabType, evt.tabId, evt.title)
        },
        onFileTabPathRegistered: (evt) => {
          opts.fileTabPaths.register(evt.tabId, evt.workspaceId, evt.filePath)
          // Mirror the path onto the local Tab record so existing
          // file-tab title rendering (which reads `tab.filePath`) sees
          // the path arriving via the private-event stream — typically
          // when another client opened the file or when this client
          // joined after the open.
          const existing = opts.tabStore.getTabByKey(`${TabType.FILE}:${evt.tabId}`)
          // The key is FILE-scoped so the lookup can only ever yield a
          // FileTab; narrow with the guard so `filePath` is accessible.
          if (existing && isFileTab(existing) && !existing.filePath) {
            opts.tabStore.updateTab(TabType.FILE, evt.tabId, { filePath: evt.filePath })
          }
        },
        onFileTabPathRevoked: (evt) => {
          opts.fileTabPaths.revoke(evt.tabId)
        },
      })
      privateStreamCleanups.set(key, close)
    }
  })
}
