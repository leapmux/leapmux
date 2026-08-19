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
const pendingInserts = new Map<string, Array<{ text: string, mode: 'block' | 'inline' }>>()

/**
 * A ref whose writes are dropped while the editor refuses input, but whose reads
 * and focus still work.
 *
 * The registry STORES this, so every route to a composer is guarded by
 * construction: `getEditorRef`, `appendText`, the MRU insert, and the deferred
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
    if (!editor.writable())
      return
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
 * Append text to an editor's existing content as a new paragraph. Reports
 * whether it landed, so a caller can skip following up with `focus()` on a
 * composer that refused the text and would only sit there inert.
 */
export function appendText(agentId: string, text: string): boolean {
  const ref = registry.get(agentId)
  if (ref === undefined || !ref.writable())
    return false
  const current = ref.get()
  const combined = current ? `${current}\n\n${text}` : text
  ref.set(combined)
  return true
}

/**
 * Park text for an editor that is not mounted yet, to be flushed when it
 * registers.
 *
 * Queueing is only correct while the editor is ABSENT. A mounted one has already
 * registered, so nothing would ever flush a queue for it and the text would sit
 * until some later re-registration wrote it in late. The flush re-checks
 * writability, so a queue for an editor that turns out to be read-only is
 * dropped rather than delivered.
 */
export function queueInsertForAgent(agentId: string, text: string, mode: 'block' | 'inline' = 'block'): void {
  const existing = pendingInserts.get(agentId) ?? []
  existing.push({ text, mode })
  pendingInserts.set(agentId, existing)
}

/**
 * Find the MRU agent tab and insert text into its editor.
 * Activates the agent tab and focuses the editor.
 */
export function insertIntoMruAgentEditor(
  deps: {
    /**
     * EVERY tab in the workspace on screen, most-recently-used first — not
     * pre-filtered to agents. The narrowing below is the only one.
     */
    mruTabs: () => Tab[]
    activate: (tab: Tab) => void
  },
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

  // Three cases, and the middle one is why they are spelled out rather than
  // folded into one `isWritable` test: queueing is only right when the editor is
  // ABSENT. A mounted editor that refuses input has already registered, so
  // nothing will flush a queue for it -- the text would sit there until some
  // later re-registration wrote it in late.
  const ref = registry.get(agentId)
  if (ref === undefined) {
    // Editor is not mounted yet — queue text for when it registers. The flush
    // re-checks writability then, once the editor can answer.
    queueInsertForAgent(agentId, text, mode)
  }
  else if (ref.writable()) {
    const current = ref.get()
    const sep = computeSeparator(current, mode)
    ref.set(current ? `${current}${sep}${text}` : text)
    ref.focus()
  }
  // Mounted and read-only: drop it. The tab passed mruSteerableAgentTab, so this
  // is the two sources disagreeing (tab state vs the live editor) and the editor
  // is the one that knows. Activating below still brings the tab forward.

  // Activate the agent tab (workspace + per-tile).
  deps.activate(target)
}
