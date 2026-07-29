import type { createFileTabPathsStore } from '~/lib/fileTabPaths'
import type { TabMetadataStore } from '~/stores/tabMetadata.store'
import type { TabView } from '~/stores/tabView'
import { createEffect, createMemo, onCleanup } from 'solid-js'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { sameKeys } from '~/lib/sameKeys'
import { openWorkerPrivateEventStream } from '~/lib/workspacePrivateEvents'
import { isFileTab } from '~/stores/tab.types'

/**
 * Open one WatchWorkspacePrivateEvents subscription per (workspace × worker)
 * pair that actually hosts a tab -- across EVERY workspace, not just the one on
 * screen.
 *
 * The worker emits a bootstrap reply (one FileTabPathRegistered per existing
 * `worker_file_tabs` row) before going live; subsequent FileTabPath* and
 * TabRenamed events populate the local caches.
 *
 * ACCOUNT-WIDE ON PURPOSE. This used to gate on the active workspace, which put
 * it out of step with everything it feeds: the projection spans the account,
 * `tabMetadata` is one flat map, and the sidebar renders every workspace's tabs.
 * `TabRenamed` is published only on the worker's per-workspace private bus and
 * this hook is its only consumer, so a peer renaming a tab in a workspace the
 * user was not looking at left the sidebar title stale until a reload --
 * `useTabHydrators` will not re-ask, because `hydrated` is write-once, and the
 * stream's bootstrap carries file paths but no titles.
 *
 * The fan-out is subscriptions, not sockets: `openWorkerPrivateEventStream`
 * multiplexes over one E2EE channel per WORKER (`getOrOpenChannel`), so N pairs
 * on one worker share a single connection. And `desired` is built from tabs
 * that exist, so it is the set of pairs actually in use -- never a cartesian
 * product of workspaces and workers.
 *
 * The effect's reactive deps are gated through `pairSnapshot` so a rename or
 * position bump on an unrelated tab doesn't tear down and reopen every stream.
 * Only a change to the SET of (workspace, worker) pairs reaches the effect.
 */
export interface UseWorkerPrivateStreamsOpts {
  view: TabView
  metadata: TabMetadataStore
  fileTabPaths: ReturnType<typeof createFileTabPathsStore>
}

/** Stream key, and the only place the pair's encoding is defined. */
function pairKey(workspaceId: string, workerId: string): string {
  return `${workspaceId}::${workerId}`
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

  // Compared on the Map's KEYS, not on a sorted joined string: the join paid an
  // O(n log n) sort plus a string allocation per tick, and a separator that
  // appears in a workspace or worker id would make two different pair sets
  // compare equal. `sameKeys` reads a Map's keys directly.
  const pairSnapshot = createMemo<Map<string, { wsId: string, workerId: string }>>(() => {
    const pairs = new Map<string, { wsId: string, workerId: string }>()
    for (const tab of opts.view.all()) {
      if (!tab.workerId || !tab.tileId || !tab.workspaceId)
        continue
      pairs.set(pairKey(tab.workspaceId, tab.workerId), { wsId: tab.workspaceId, workerId: tab.workerId })
    }
    return pairs
  }, new Map(), { equals: sameKeys })

  createEffect(() => {
    const pairs = pairSnapshot()

    // No pairs at all means no tab anywhere is hosted -- logout, or an empty
    // account. That, not "no active workspace", is when the path cache is dead:
    // it is keyed by tab id and spans every workspace, so clearing it on a
    // workspace switch would have thrown away paths still on screen elsewhere.
    if (pairs.size === 0) {
      for (const close of privateStreamCleanups.values())
        close()
      privateStreamCleanups.clear()
      opts.fileTabPaths.clear()
      return
    }

    for (const [key, close] of privateStreamCleanups.entries()) {
      if (!pairs.has(key)) {
        close()
        privateStreamCleanups.delete(key)
      }
    }

    for (const [key, { wsId, workerId }] of pairs) {
      if (privateStreamCleanups.has(key))
        continue
      const close = openWorkerPrivateEventStream({
        workspaceId: wsId,
        workerId,
        onTabRenamed: (evt) => {
          opts.metadata.patch(evt.tabId, { title: evt.title })
        },
        onFileTabPathRegistered: (evt) => {
          opts.fileTabPaths.register(evt.tabId, evt.workspaceId, evt.filePath)
          // Mirror the path onto the joined tab so existing file-tab title
          // rendering (which reads `tab.filePath`) sees a path arriving via the
          // private-event stream -- typically when another client opened the
          // file, or when this client joined after the open.
          const existing = opts.view.getById(TabType.FILE, evt.tabId)
          // The key is FILE-scoped so the lookup can only ever yield a FileTab;
          // narrow with the guard so `filePath` is accessible.
          if (existing && isFileTab(existing) && !existing.filePath) {
            opts.metadata.patch(evt.tabId, { filePath: evt.filePath })
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
