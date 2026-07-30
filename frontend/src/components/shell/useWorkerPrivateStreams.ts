import type { createFileTabPathsStore } from '~/lib/fileTabPaths'
import type { TabMetadataStore } from '~/stores/tabMetadata.store'
import type { TabView } from '~/stores/tabView'
import { createEffect, createMemo, onCleanup } from 'solid-js'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { sameKeys } from '~/lib/sameKeys'
import { openWorkerPrivateEventStream } from '~/lib/workerPrivateEvents'
import { isFileTab } from '~/stores/tab.types'

/**
 * Open one `WatchWorkerPrivateEvents` subscription per WORKER that hosts a tab
 * -- across EVERY workspace, not just the one on screen.
 *
 * The worker emits a bootstrap reply (one FileTabPathRegistered per
 * `worker_file_tabs` row the caller owns) before going live; subsequent
 * FileTabPath* and TabRenamed events populate the local caches.
 *
 * ONE STREAM PER WORKER, not per (workspace, worker) pair. The worker stores
 * no workspace id, so there is nothing to key a narrower subscription on --
 * and the pair shape was the bug, not just redundancy: the pair set was
 * derived from tabs that already exist, so a workspace with no tabs yet had no
 * stream, and the first tab opened in it (by the `leapmux remote` CLI, or by
 * another session) delivered no events until something else forced a re-open.
 *
 * ACCOUNT-WIDE ON PURPOSE. This used to gate on the active workspace, which
 * put it out of step with everything it feeds: the projection spans the
 * account, `tabMetadata` is one flat map, and the sidebar renders every
 * workspace's tabs. `TabRenamed` is published only on the worker's private bus
 * and this hook is its only consumer, so a peer renaming a tab in a workspace
 * the user was not looking at left the sidebar title stale until a reload --
 * `useTabHydrators` will not re-ask, because `hydrated` is write-once, and the
 * stream's bootstrap carries file paths but no titles.
 *
 * The fan-out is subscriptions, not sockets: `openWorkerPrivateEventStream`
 * multiplexes over the one E2EE channel per worker (`getOrOpenChannel`).
 *
 * The effect's reactive deps are gated through `workerSnapshot` so a rename or
 * position bump on an unrelated tab doesn't tear down and reopen every stream.
 * Only a change to the SET of worker ids reaches the effect.
 */
export interface UseWorkerPrivateStreamsOpts {
  view: TabView
  metadata: TabMetadataStore
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

  // Compared on the Set's members, not on a sorted joined string: the join paid
  // an O(n log n) sort plus a string allocation per tick, and a separator that
  // appears in a worker id would make two different sets compare equal.
  // `sameKeys` reads a Set's members directly.
  const workerSnapshot = createMemo<Set<string>>(() => {
    const workers = new Set<string>()
    for (const tab of opts.view.all()) {
      if (!tab.workerId || !tab.tileId)
        continue
      workers.add(tab.workerId)
    }
    return workers
  }, new Set(), { equals: sameKeys })

  createEffect(() => {
    const workers = workerSnapshot()

    // No workers at all means no tab anywhere is hosted -- logout, or an empty
    // account. That, not "no active workspace", is when the path cache is dead:
    // it is keyed by tab id and spans every workspace, so clearing it on a
    // workspace switch would have thrown away paths still on screen elsewhere.
    if (workers.size === 0) {
      for (const close of privateStreamCleanups.values())
        close()
      privateStreamCleanups.clear()
      opts.fileTabPaths.clear()
      return
    }

    for (const [workerId, close] of privateStreamCleanups.entries()) {
      if (!workers.has(workerId)) {
        close()
        privateStreamCleanups.delete(workerId)
      }
    }

    for (const workerId of workers) {
      if (privateStreamCleanups.has(workerId))
        continue
      const close = openWorkerPrivateEventStream({
        workerId,
        onTabRenamed: (evt) => {
          opts.metadata.patch(evt.tabId, { title: evt.title })
        },
        onFileTabPathRegistered: (evt) => {
          opts.fileTabPaths.register(evt.tabId, evt.filePath)
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
      privateStreamCleanups.set(workerId, close)
    }
  })
}
