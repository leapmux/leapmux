import type { createFloatingWindowStore } from '~/stores/floatingWindow.store'
import type { createLayoutStore } from '~/stores/layout.store'
import type { Tab } from '~/stores/tab.types'
import type { TabSelectionStore } from '~/stores/tabSelection.store'
import type { TabView } from '~/stores/tabView'
import { focusTile } from './tileLifecycle'

/**
 * The dependency pair `insertIntoMruAgentEditor` needs, built once.
 *
 * `editorRef.store` takes these as callbacks so it can stay free of store
 * imports, which is right — but the three call sites (`TileRenderer`'s quote and
 * mention handlers, and the sidebar's file mention) had each hand-copied the
 * same object literal, and two of them were byte-identical six lines apart.
 * Three copies of a wiring decision is a worse coupling than one named binding.
 *
 * `activate` selects the tab AND focuses its tile. Selecting alone is not
 * enough: `AppShell.activeTab` reads `activeTabForTile(focusedTileId())`, so an
 * agent promoted in a tile the user is not focused on would receive the inserted
 * text while the centre pane, `getCurrentTabContext` and every tab shortcut kept
 * answering for a different tab. `insertIntoMruAgentEditor` already calls
 * `ref.focus()` on the editor itself, so DOM focus moves there regardless —
 * carrying tile focus with it is what makes the two agree.
 */
export function mruAgentEditorDeps(deps: {
  view: TabView
  selection: TabSelectionStore
  layoutStore: ReturnType<typeof createLayoutStore>
  floatingWindowStore: ReturnType<typeof createFloatingWindowStore> | undefined
  getWorkspaceId: () => string | undefined
}): { mruTabs: () => Tab[], activate: (tab: Tab) => void } {
  return {
    // EVERY tab type in MRU order, not just agents. `insertIntoMruAgentEditor`
    // owns the AGENT narrowing; calling this `mruAgentTabs` made that filter
    // look redundant, which is how it ended up with no test.
    mruTabs: () => deps.view.mruOrder(deps.getWorkspaceId() ?? ''),
    activate: (tab) => {
      deps.selection.setActive(tab)
      if (tab.tileId)
        focusTile(deps.layoutStore, deps.floatingWindowStore, tab.tileId, tab.workspaceId)
    },
  }
}
