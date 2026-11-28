import type { TabStatusView } from '~/lib/tabStatus'
import { createStore, produce, unwrap } from 'solid-js/store'

/** One tab's status, and the Worker whose stream reported it. */
interface TabStatusEntry {
  workerId: string
  status: TabStatusView
}

interface TabStatusStoreState {
  byTabId: Record<string, TabStatusEntry>
}

/**
 * The status that each tab shows beside its title, as the tabs' Workers report
 * it on their private-event streams (`TabStatusChanged`).
 *
 * The Worker holds each status in memory and is its one source. This store is a
 * copy that flows one way, from the stream, and every rule below rebuilds it
 * from that source:
 *
 * - A new stream to a Worker drops that Worker's statuses first
 *   (`forgetWorker`), because its bootstrap replays every status that is still
 *   set. A status cleared while the stream was down would otherwise stay.
 * - A closed stream drops its Worker's statuses: nothing reports their changes
 *   any more. An account switch closes every stream, and so drops every status.
 * - A closed tab drops its status (`dropTabs`, from the metadata sweep), and so
 *   does a cleared one (`apply` with no status).
 *
 * Keyed by tab id rather than joined into the tab metadata, because a status
 * can arrive BEFORE the tab does: a script that opens a terminal and sets its
 * status at once can beat the CRDT operation that adds the tab. The join in
 * `tabView` then finds the status the moment the tab appears.
 */
export function createTabStatusStore() {
  const [state, setState] = createStore<TabStatusStoreState>({ byTabId: {} })

  /**
   * Delete every entry for which `drop` answers true, in one write.
   *
   * It reads the UNWRAPPED state. The metadata sweep calls `dropTabs` from
   * inside its effect, and a tracked read of every key here would subscribe
   * that effect to each status change.
   */
  const dropWhere = (drop: (tabId: string, entry: TabStatusEntry) => boolean) => {
    const doomed = Object.entries(unwrap(state).byTabId)
      .filter(([tabId, entry]) => drop(tabId, entry))
      .map(([tabId]) => tabId)
    if (doomed.length === 0)
      return
    setState(produce((s) => {
      for (const tabId of doomed)
        delete s.byTabId[tabId]
    }))
  }

  return {
    /** The status of one tab, or undefined when it shows none. Reactive. */
    get(tabId: string): TabStatusView | undefined {
      return state.byTabId[tabId]?.status
    },

    /**
     * Apply what a Worker reported for one tab: a status replaces the old one
     * whole, and undefined clears it.
     */
    apply(workerId: string, tabId: string, status: TabStatusView | undefined) {
      if (status === undefined) {
        if (unwrap(state).byTabId[tabId] !== undefined) {
          setState(produce((s) => {
            delete s.byTabId[tabId]
          }))
        }
        return
      }
      // Assigned whole through produce: a path write would MERGE into the old
      // object, and a key that the new status leaves out would survive it.
      setState(produce((s) => {
        s.byTabId[tabId] = { workerId, status }
      }))
    },

    /** Drop every status that one Worker reported. */
    forgetWorker(workerId: string) {
      dropWhere((_, entry) => entry.workerId === workerId)
    },

    /** Drop the statuses of tabs that closed. */
    dropTabs(tabIds: ReadonlySet<string>) {
      dropWhere(tabId => tabIds.has(tabId))
    },
  }
}

export type TabStatusStore = ReturnType<typeof createTabStatusStore>
