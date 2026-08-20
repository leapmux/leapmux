import type { Tab } from '~/stores/tab.types'
import { mruSteerableAgentTab } from '~/stores/tab.helpers'

export interface EditorRef {
  get: () => string
  set: (value: string) => void
  focus: () => void
  insert: (text: string) => void
  /**
   * Whether this composer accepts input at all. A thunk, not a boolean: a
   * subagent tab starts out on its provider plugin's guess and flips once the
   * worker's authoritative `acceptsMessages` hydrates.
   *
   * The EDITOR declares this, rather than each writer looking the tab up,
   * because the registry is the only route to a composer -- so refusing here is
   * what makes a write to a read-only one impossible rather than merely
   * unusual. A read-only subagent's composer is mounted and registered like any
   * other (it is disabled, not absent), so without this every writer would land
   * text in it, and the draft layer would persist that text under the
   * subagent's own key where it survives a reload.
   */
  writable: () => boolean
}

const registry = new Map<string, EditorRef>()
/** Pending inserts to flush when an editor ref is registered. */
const pendingInserts = new Map<string, PendingInsert[]>()

interface PendingInsert {
  text: string
  mode: 'block' | 'inline'
  /**
   * Called when the flush DROPS this entry, because the editor turned out to
   * refuse input. Queueing happens before the destination can answer, so the
   * drop is the only moment the caller learns its target was wrong -- and
   * without a hook the user's text vanished with no message and no fallback.
   */
  onDropped?: () => void
}

/**
 * A ref whose writes are dropped while the editor refuses input, but whose reads
 * and focus still work.
 *
 * The registry STORES this, so every route to a composer is guarded by
 * construction: `getEditorRef`, `insertIntoAgentEditor`, and the deferred
 * flush all read the same guarded handle. Guarding only the accessor left the
 * module's own writers on the raw ref, and the flush retry -- which writes for
 * up to half a second after registration -- never re-checked at all.
 */
function guarded(ref: EditorRef): EditorRef {
  return {
    get: ref.get,
    focus: ref.focus,
    writable: ref.writable,
    set: (value) => {
      if (ref.writable())
        ref.set(value)
    },
    insert: (text) => {
      if (ref.writable())
        ref.insert(text)
    },
  }
}

/** Compute the separator to join `text` after `current` content. */
export function computeSeparator(current: string, mode: 'block' | 'inline'): string {
  if (!current)
    return ''
  if (mode === 'block')
    return '\n\n'
  return current.endsWith('\n') ? '' : ' '
}

export function registerEditorRef(agentId: string, ref: EditorRef): void {
  const editor = guarded(ref)
  registry.set(agentId, editor)
  // Flush any pending inserts that were queued before the component mounted.
  // Milkdown's ProseMirror view may silently reject replaceAll if it isn't
  // fully initialized, so we retry a few times with increasing delays.
  const pending = pendingInserts.get(agentId)
  if (pending != null) {
    pendingInserts.delete(agentId)
    // Writability is unknown when a queue is built (the editor is not mounted
    // yet), so it is checked HERE, once the editor says what it is. Dropping the
    // queue is the whole point: a queued quote must not land in a composer that
    // turned out to be read-only.
    //
    // Each entry is TOLD it was dropped, so the caller can put the text
    // somewhere the user can still send it. The queue is deleted first, so a
    // handler that queues again for this same agent cannot append to a list this
    // loop is walking -- and it will not, because the registry already holds the
    // editor by now, so a re-entrant insert gets 'refused' rather than 'queued'.
    if (!editor.writable()) {
      for (const entry of pending)
        entry.onDropped?.()
      return
    }
    const tryFlush = (attempt: number) => {
      // Re-read on EVERY attempt, and off the registry rather than the captured
      // ref. This loop writes for up to half a second after the check above, and
      // two things can change inside that window: the worker's authoritative
      // acceptsMessages can arrive and turn the composer read-only, and the tab
      // can close, which unregisters it. Either one must stop the flush -- the
      // guarded handle refuses the write, and an absent entry ends the retry
      // instead of setting and focusing an unmounted editor.
      const live = registry.get(agentId)
      if (live === undefined || !live.writable())
        return
      // Read existing content at flush time (not registration time) so draft
      // content loaded by Milkdown is preserved.
      let combined = live.get()
      for (const { text, mode } of pending) {
        const sep = computeSeparator(combined, mode)
        combined = combined ? `${combined}${sep}${text}` : text
      }
      live.set(combined)
      // Verify the text was actually inserted (ref.set may silently fail).
      if (live.get().length === 0 && attempt < 10) {
        setTimeout(tryFlush, 50, attempt + 1)
      }
      else {
        live.focus()
      }
    }
    // Start with a small delay to let the editor settle after mount.
    setTimeout(tryFlush, 50, 0)
  }
}

export function unregisterEditorRef(agentId: string): void {
  registry.delete(agentId)
}

export function getEditorRef(agentId: string): EditorRef | undefined {
  return registry.get(agentId)
}

/**
 * What `insertIntoAgentEditor` did with the text.
 *
 * - `inserted`: the editor took it, and the editor is focused.
 * - `queued`: no editor is mounted, so the text waits for one to register.
 * - `refused`: an editor IS mounted and does not accept input. The text is
 *   dropped, because nothing will ever flush a queue for an editor that already
 *   registered.
 */
export type InsertOutcome = 'inserted' | 'queued' | 'refused'

/**
 * Put text into one agent's composer, and report which of the three cases the
 * caller got.
 *
 * This is the ONE router. Every caller used to spell the three cases out again,
 * and the two spellings already disagreed: the boolean the quote handler read
 * reported absent and refusing alike, so it took every failure for "not
 * mounted" and queued -- into an editor that already registered, where nothing
 * would ever flush it. Reporting the case removes the guess.
 *
 * The caller activates the tab AFTER this returns, never before. A tab
 * activation mounts the destination editor synchronously, and an editor that
 * registers before the text is parked finds an empty queue and leaves the quote
 * to be replayed into some later re-registration.
 */
export function insertIntoAgentEditor(
  agentId: string,
  text: string,
  mode: 'block' | 'inline' = 'block',
  onDropped?: () => void,
): InsertOutcome {
  const ref = registry.get(agentId)
  if (ref === undefined) {
    queueInsertForAgent(agentId, text, mode, onDropped)
    return 'queued'
  }
  if (!ref.writable())
    return 'refused'
  const current = ref.get()
  const sep = computeSeparator(current, mode)
  ref.set(current ? `${current}${sep}${text}` : text)
  ref.focus()
  return 'inserted'
}

/**
 * Park text for an editor that is not mounted yet, to be flushed when it
 * registers.
 *
 * Queueing is only correct while the editor is ABSENT. A mounted one already
 * registered, so nothing would ever flush a queue for it and the text would sit
 * until some later re-registration wrote it in late. `insertIntoAgentEditor`
 * makes that distinction for every caller; reach for this directly only when the
 * editor is known to be absent. The flush re-checks writability, so a queue for
 * an editor that turns out to be read-only is dropped rather than delivered.
 */
export function queueInsertForAgent(
  agentId: string,
  text: string,
  mode: 'block' | 'inline' = 'block',
  onDropped?: () => void,
): void {
  const existing = pendingInserts.get(agentId) ?? []
  existing.push({ text, mode, onDropped })
  pendingInserts.set(agentId, existing)
}

/**
 * Find the MRU agent tab and insert text into its editor.
 * Activates the agent tab and focuses the editor.
 */
export interface MruAgentEditorDeps {
  /**
   * EVERY tab in the workspace on screen, most-recently-used first — not
   * pre-filtered to agents. The narrowing below is the only one.
   */
  mruTabs: () => Tab[]
  activate: (tab: Tab) => void
  /**
   * Tell the user the text did not go where the click implied. A thunk supplied
   * by the shell, so this module keeps no UI import: it reports, and the shell
   * decides whether that is a toast.
   *
   * Two moments need it, and both are invisible otherwise. A queued insert whose
   * destination turns out to be read-only is RE-ROUTED here, up to half a second
   * after the click and with the tab moving under the user. And a destination
   * that is mounted and refuses input drops the text outright.
   */
  notify?: (message: string) => void
}

export function insertIntoMruAgentEditor(
  deps: MruAgentEditorDeps,
  text: string,
  mode: 'block' | 'inline' = 'block',
): void {
  // Find the most-recent agent tab that is STEERABLE — a non-steerable child
  // (a read-only subagent transcript) must never receive an inserted mention or
  // quote. The chat quote reaches this too, for a message read in such a
  // transcript: the tab it was read in is filtered out here, so the text goes to
  // the nearest writable agent instead, and `activate` brings that tab forward
  // so it is never inserted out of sight.
  const target = mruSteerableAgentTab(deps.mruTabs())
  if (!target)
    return
  const agentId = target.id

  // `onDropped` re-runs this whole resolution. The destination was chosen from
  // TAB state, which is optimistic: a subagent tab opened from the sidebar has
  // no parentAgentId until listAgents hydrates it, so it looks steerable and
  // wins the MRU until its composer mounts and says otherwise. Re-resolving is
  // completing the original request, not inventing a new one -- and the same
  // answer this function already gives when the target is read-only up front.
  //
  // The recursion is bounded: registerEditorRef puts the editor in the registry
  // BEFORE it walks the queue, so a re-entrant call for the same agent finds a
  // registered editor and returns 'refused' rather than queueing again.
  const outcome = insertIntoAgentEditor(agentId, text, mode, () => {
    insertIntoMruAgentEditor(deps, text, mode)
    deps.notify?.('That subagent\'s composer is read-only. The text went to the nearest agent that accepts messages.')
  })
  // Mounted and refusing: the two sources disagree (tab state said steerable,
  // the live editor says otherwise) and the editor is the one that knows. The
  // text is dropped rather than queued, because nothing would ever flush a queue
  // for an editor that already registered -- so the user has to be told.
  if (outcome === 'refused')
    deps.notify?.('That agent\'s composer does not accept input. The text was not inserted.')

  // Activate the agent tab (workspace + per-tile). AFTER the insert, never
  // before: activation mounts the destination editor, and an editor that
  // registers ahead of the queue finds it empty.
  deps.activate(target)
}
