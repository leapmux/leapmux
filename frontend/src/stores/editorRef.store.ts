import type { Tab } from '~/stores/tab.store'
import { TabType } from '~/stores/tab.store'

export interface EditorRef {
  get: () => string
  set: (value: string) => void
  focus: () => void
}

const registry = new Map<string, EditorRef>()
/** Pending text to insert when an editor ref is registered. */
const pendingInserts = new Map<string, string>()

export function registerEditorRef(agentId: string, ref: EditorRef): void {
  registry.set(agentId, ref)
  // Flush any pending insert that was queued before the component mounted.
  // Milkdown's ProseMirror view may silently reject replaceAll if it isn't
  // fully initialized, so we retry a few times with increasing delays.
  const pending = pendingInserts.get(agentId)
  if (pending != null) {
    pendingInserts.delete(agentId)
    const tryFlush = (attempt: number) => {
      ref.set(pending)
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
    ref.set(current ? `${current}\n\n${text}` : text)
    ref.focus()
  }
  else {
    // Editor is not mounted yet — queue text for when it registers.
    const existing = pendingInserts.get(agentId)
    pendingInserts.set(agentId, existing ? `${existing}\n\n${text}` : text)
  }

  // Activate the agent tab (global + per-tile).
  tabStore.setActiveTab(TabType.AGENT, agentId)
  const tab = tabStore.state.tabs.find(t => t.type === TabType.AGENT && t.id === agentId)
  if (tab?.tileId) {
    tabStore.setActiveTabForTile(tab.tileId, TabType.AGENT, agentId)
  }
}
