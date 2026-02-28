import type { Tab } from '~/stores/tab.store'
import { TabType } from '~/stores/tab.store'

export interface EditorRef {
  get: () => string
  set: (value: string) => void
  focus: () => void
}

const registry = new Map<string, EditorRef>()
/** Pending inserts to flush when an editor ref is registered. */
const pendingInserts = new Map<string, Array<{ text: string, mode: 'block' | 'inline' }>>()

/** Compute the separator to join `text` after `current` content. */
export function computeSeparator(current: string, mode: 'block' | 'inline'): string {
  if (!current)
    return ''
  if (mode === 'block')
    return '\n\n'
  return current.endsWith('\n') ? '' : ' '
}

export function registerEditorRef(agentId: string, ref: EditorRef): void {
  registry.set(agentId, ref)
  // Flush any pending inserts that were queued before the component mounted.
  // Milkdown's ProseMirror view may silently reject replaceAll if it isn't
  // fully initialized, so we retry a few times with increasing delays.
  const pending = pendingInserts.get(agentId)
  if (pending != null) {
    pendingInserts.delete(agentId)
    const tryFlush = (attempt: number) => {
      // Read existing content at flush time (not registration time) so draft
      // content loaded by Milkdown is preserved.
      let combined = ref.get()
      for (const { text, mode } of pending) {
        const sep = computeSeparator(combined, mode)
        combined = combined ? `${combined}${sep}${text}` : text
      }
      ref.set(combined)
      // Verify the text was actually inserted (ref.set may silently fail).
      if (ref.get().length === 0 && attempt < 10) {
        setTimeout(() => tryFlush(attempt + 1), 50)
      }
      else {
        ref.focus()
      }
    }
    // Start with a small delay to let the editor settle after mount.
    setTimeout(() => tryFlush(0), 50)
  }
}

export function unregisterEditorRef(agentId: string): void {
  registry.delete(agentId)
}

export function getEditorRef(agentId: string): EditorRef | undefined {
  return registry.get(agentId)
}

/** Append text to an editor's existing content as a new paragraph. */
export function appendText(agentId: string, text: string): void {
  const ref = registry.get(agentId)
  if (!ref)
    return
  const current = ref.get()
  const combined = current ? `${current}\n\n${text}` : text
  ref.set(combined)
}

/**
 * Find the MRU agent tab and insert text into its editor.
 * Activates the agent tab and focuses the editor.
 */
export function insertIntoMruAgentEditor(
  tabStore: {
    state: { tabs: Tab[], mruOrder: string[] }
    setActiveTab: (type: TabType, id: string) => void
    setActiveTabForTile: (tileId: string, type: TabType, id: string) => void
  },
  text: string,
  mode: 'block' | 'inline' = 'block',
): void {
  const agentPrefix = `${TabType.AGENT}:`
  const mruKey = tabStore.state.mruOrder.find(k => k.startsWith(agentPrefix))
  if (!mruKey)
    return
  const agentId = mruKey.slice(agentPrefix.length)

  // Try to insert directly if the ref is available (same-tab scenario).
  const ref = registry.get(agentId)
  if (ref) {
    const current = ref.get()
    const sep = computeSeparator(current, mode)
    ref.set(current ? `${current}${sep}${text}` : text)
    ref.focus()
  }
  else {
    // Editor is not mounted yet — queue text for when it registers.
    const existing = pendingInserts.get(agentId) ?? []
    existing.push({ text, mode })
    pendingInserts.set(agentId, existing)
  }

  // Activate the agent tab (global + per-tile).
  tabStore.setActiveTab(TabType.AGENT, agentId)
  const tab = tabStore.state.tabs.find(t => t.type === TabType.AGENT && t.id === agentId)
  if (tab?.tileId) {
    tabStore.setActiveTabForTile(tab.tileId, TabType.AGENT, agentId)
  }
}
