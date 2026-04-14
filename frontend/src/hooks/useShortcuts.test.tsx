import { cleanup, render } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { executeCommand, getCommand, resetCommands } from '~/lib/shortcuts/commands'
import { useShortcuts } from './useShortcuts'

const refreshFileTree = vi.fn()
const toggleHiddenFiles = vi.fn()

vi.mock('~/api/platformBridge', () => ({
  isTauriApp: () => false,
  openWebInspector: vi.fn(),
  quitApp: vi.fn(),
  resetWebviewZoom: vi.fn(),
  zoomInWebview: vi.fn(),
  zoomOutWebview: vi.fn(),
}))

vi.mock('~/components/chat/ChatView', () => ({
  scrollActiveChatPage: vi.fn(),
}))

vi.mock('~/components/shell/UserMenu', () => ({
  setShowPreferencesDialog: vi.fn(),
}))

vi.mock('~/components/terminal/TerminalView', () => ({
  scrollActiveTerminalPage: vi.fn(),
  writeToActiveTerminal: vi.fn(),
}))

vi.mock('~/lib/fileTreeOps', () => ({
  refreshFileTree: () => refreshFileTree(),
  toggleHiddenFiles: () => toggleHiddenFiles(),
}))

afterEach(() => {
  cleanup()
  resetCommands()
  refreshFileTree.mockReset()
  toggleHiddenFiles.mockReset()
})

function makeProps() {
  return {
    tabStore: {
      state: { tabs: [], activeTabKey: null },
      activeTab: () => null,
      getTabsForTile: () => [],
      getActiveTabKeyForTile: () => null,
    },
    layoutStore: {
      focusedTileId: () => null,
      splitTileHorizontal: vi.fn(),
      splitTileVertical: vi.fn(),
    },
    tabOps: {
      handleTabClose: vi.fn(),
      handleTabSelect: vi.fn(),
    },
    agentOps: {
      handleOpenAgent: vi.fn(),
    },
    termOps: {
      handleOpenTerminal: vi.fn(),
    },
    setShowNewAgentDialog: vi.fn(),
    setShowNewTerminalDialog: vi.fn(),
    setShowNewWorkspace: vi.fn(),
    toggleFloatingTab: vi.fn(),
    toggleLeftSidebar: vi.fn(),
    toggleRightSidebar: vi.fn(),
    activeTabType: () => null,
    customKeybindings: () => [],
  }
}

describe('useShortcuts', () => {
  it('registers file-tree shortcut commands that call the direct helpers', () => {
    const props = makeProps()

    render(() => {
      useShortcuts(props as any)
      return null
    })

    expect(getCommand('app.refreshDirectoryTree')).toBeDefined()
    expect(getCommand('app.toggleHiddenFiles')).toBeDefined()

    executeCommand('app.refreshDirectoryTree')
    executeCommand('app.toggleHiddenFiles')

    expect(refreshFileTree).toHaveBeenCalledOnce()
    expect(toggleHiddenFiles).toHaveBeenCalledOnce()
  })
})
