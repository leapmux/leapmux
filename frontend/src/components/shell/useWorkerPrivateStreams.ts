import type { TabMetadataStore } from '~/stores/tabMetadata.store'
import type { TabView } from '~/stores/tabView'
import { createEffect, createMemo, onCleanup } from 'solid-js'
import { sameKeys } from '~/lib/sameKeys'
import { openWorkerPrivateEventStream } from '~/lib/workerPrivateEvents'

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
          // Mirror onto the joined tab so existing file-tab rendering (which
          // reads `tab.filePath`) and the branch grouping (which reads
          // `tab.workingDir`) see what arrived on the private-event stream --
          // typically when another client opened the file, or when this client
          // joined after the open.
          //
          // NOT gated on the tab already existing. The event races the CRDT row
          // it describes -- they travel worker->client and worker->hub->client
          // respectively. Skipping the patch when the row has not landed yet
          // used to lose the payload for good: `register` above still recorded
          // the path, which is exactly what the FILE hydrator's predicate treats
          // as "already answered", so nothing ever asked again and the tab kept
          // an empty path forever. `tabMetadata` is keyed by tab id
          // independently of the projection, so writing early is safe -- the
          // join picks it up when the row appears.
          //
          // And NOT gated on the field being missing locally. The worker is the
          // resolver: it normalizes the working dir (tilde expansion, the
          // no-originating-tab fallback) and its answer is the one every
          // branch-context operation will use, so a local guess must not outrank
          // it. A "fill only what is MISSING" gate could by construction never
          // correct a value that was present but wrong -- which is the common
          // case for `workingDir`, since the local open path seeds it from
          // `getCurrentTabContext()` and then marks the tab hydrated. Writing
          // unconditionally costs nothing when the values agree: `patch` drops a
          // write equal to what is stored (see `sameStoredValue`), which is the
          // single place that rule lives now.
          //
          // `|| undefined` because these arrive as proto3 strings: an absent
          // field is `''`, and `mergeDefined` treats a real `''` as a CLEARING
          // write rather than as "no opinion".
          //
          // `hydrated` because this event IS a worker answer for this exact
          // tab, carrying the same payload `GetFileTabPath` returns -- so the
          // FILE hydrator has nothing left to ask. That flag is the one place
          // "the worker has answered for this tab" lives; a second cache
          // holding the same fact could outlive the row it describes and strand
          // the tab (see `retainOnly`).
          opts.metadata.patch(evt.tabId, {
            filePath: evt.filePath || undefined,
            workingDir: evt.workingDir || undefined,
            hydrated: true,
          })
        },
      })
      privateStreamCleanups.set(workerId, close)
    }
  })
}
