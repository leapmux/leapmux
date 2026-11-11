import type { UserCrdtState } from '~/generated/proto/leapmux/v1/user_crdt_pb'
import type { QuakeTerminalStore } from '~/stores/quakeTerminal.store'
import type { TabMetadataStore } from '~/stores/tabMetadata.store'
import type { TabView } from '~/stores/tabView'
import { createEffect, createMemo, onCleanup } from 'solid-js'
import { TabType } from '~/generated/proto/leapmux/v1/common_pb'
import { QuakePanelAction } from '~/generated/proto/leapmux/v1/worker_private_pb'
import { hlcIsZero } from '~/lib/crdt'
import { sameKeys } from '~/lib/sameKeys'
import { openWorkerPrivateEventStream } from '~/lib/workerPrivateEvents'
import { tabPayloadMetadata } from '~/stores/tabMetadata.store'

/**
 * Open one `WatchWorkerPrivateEvents` subscription per WORKER that hosts a tab
 * -- across EVERY workspace, not just the one on screen.
 *
 * The worker emits a bootstrap reply (one TabPayloadRegistered per
 * `worker_tab_payloads` row the caller owns) before going live; subsequent
 * TabPayload* and TabRenamed events populate the local caches.
 *
 * ONE STREAM PER WORKER, not per (workspace, worker) pair. The worker stores
 * no workspace id, so there is nothing to key a narrower subscription on --
 * and the pair shape was the bug, not just redundancy: the pair set was
 * derived from tabs that already exist, so a workspace with no tabs yet had no
 * stream, and the first tab opened in it (by the `leapmux control` CLI, or by
 * another session) delivered no events until something else forced a re-open.
 *
 * ACCOUNT-WIDE ON PURPOSE. This used to gate on the active workspace, which
 * put it out of step with everything it feeds: the projection spans the
 * account, `tabMetadata` is one flat map, and the sidebar renders every
 * workspace's tabs. Live TabRenamed events keep AGENT and TERMINAL titles
 * current between full Worker reads. Scoping the stream to the active workspace
 * left offscreen titles stale until another full read. Payload bootstrap also
 * supplies FILE paths and IMAGE titles across the account.
 *
 * The fan-out is subscriptions, not sockets: `openWorkerPrivateEventStream`
 * multiplexes over the one E2EE channel per worker (`getOrOpenChannel`).
 *
 * The effect's reactive deps are gated through `workerSnapshot` so a rename or
 * position bump on an unrelated tab doesn't tear down and reopen every stream.
 * Worker membership controls stream changes within an account. An account
 * generation change closes and replaces every stream.
 */
export interface UseWorkerPrivateStreamsOpts {
  acceptsEarlyMetadata: (tabId: string, type: TabType, workerId: string) => boolean
  state: () => UserCrdtState | null
  view: TabView
  metadata: TabMetadataStore
  quakeStore: QuakeTerminalStore
}

export function useWorkerPrivateStreams(opts: UseWorkerPrivateStreamsOpts): void {
  const privateStreamCleanups = new Map<string, () => void>()

  let streamOwner: string | null = null
  let streamGeneration = -1

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
    const owner = opts.metadata.state.userId
    const generation = opts.metadata.state.accountGeneration
    const canonicalOwner = opts.state()?.userId
    if (owner !== streamOwner || generation !== streamGeneration || canonicalOwner !== owner) {
      for (const close of privateStreamCleanups.values())
        close()
      privateStreamCleanups.clear()
      streamOwner = owner
      streamGeneration = generation
    }
    if (!owner || canonicalOwner !== owner)
      return

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
      let active = true
      const isCurrent = () => active && opts.metadata.state.userId === owner
        && opts.metadata.state.accountGeneration === generation && opts.state()?.userId === owner
      const acceptsMetadata = (tabId: string, type: TabType) => {
        if (!isCurrent())
          return false
        const record = opts.state()!.tabs[tabId]
        return record
          ? hlcIsZero(record.tombstoneAt) && record.tabType === type
          && (!record.workerId || record.workerId.value === workerId)
          : opts.acceptsEarlyMetadata(tabId, type, workerId)
      }
      const close = openWorkerPrivateEventStream({
        workerId,
        onTabRenamed: (evt) => {
          if (!acceptsMetadata(evt.tabId, evt.tabType))
            return
          opts.metadata.patch(evt.tabId, { title: evt.title })
        },
        onTabPayloadRevoked: (evt) => {
          if (!isCurrent())
            return
          const record = opts.state()!.tabs[evt.tabId]
          if (!record || !hlcIsZero(record.tombstoneAt))
            opts.metadata.remove(evt.tabId)
        },
        onQuakePanelCommand: (evt) => {
          if (!isCurrent())
            return
          // The event carries a DIRECTORY, and the stream identifies the worker,
          // so together they are the whole address.
          //
          // A directory this client has no tab in is not an error: the command
          // reaches every frontend of the account, and another one may be on a
          // workspace this one does not display.
          const key = { workerId, workingDir: evt.workingDir }
          // CLOSE is answered BEFORE the tab lookup, because it needs only the
          // key. It is the one of the three that can act on a directory whose
          // tabs this client cannot see, and refusing it there left a panel
          // that covers the whole centre area with no way to dismiss it -- on a
          // phone there is no chord, and the CLI is the only other route.
          if (evt.action === QuakePanelAction.CLOSE) {
            opts.quakeStore.close(key)
            return
          }
          // OPEN and TOGGLE do need one: a cold open is refused in the tab's
          // workspace, and both take a tab for exactly that.
          const tab = opts.view.findTabInWorkingDir(workerId, evt.workingDir)
          if (!tab)
            return
          switch (evt.action) {
            case QuakePanelAction.OPEN:
              void opts.quakeStore.open(tab)
              break
            case QuakePanelAction.TOGGLE:
              opts.quakeStore.toggle(tab)
              break
            default:
              // An action a newer CLI knows and this build does not. Doing
              // nothing is the only honest answer -- guessing between show and
              // hide would be worse than ignoring it.
              break
          }
        },
        onTabPayloadRegistered: (evt) => {
          const type = evt.payload.kind === 'file'
            ? TabType.FILE
            : TabType.IMAGE
          if (!acceptsMetadata(evt.tabId, type))
            return
          // Mirror onto the joined tab so existing file-tab rendering (which
          // reads `tab.filePath`) and the branch grouping (which reads
          // `tab.workingDir`) see what arrived on the private-event stream --
          // typically when another client opened the file, or when this client
          // joined after the open.
          //
          // Local pending creation accepts the Worker's normalized payload before
          // canonical publication. Unknown peer IDs wait for canonical publication
          // and the hydrator, so a lost revoke cannot leave unowned private data.
          // The shared mapper marks this complete Worker answer as hydrated.
          opts.metadata.patch(evt.tabId, tabPayloadMetadata(evt.payload))
        },
      })
      privateStreamCleanups.set(workerId, () => {
        active = false
        close()
      })
    }
  })
}
