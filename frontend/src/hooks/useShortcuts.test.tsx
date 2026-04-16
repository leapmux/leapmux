import { cleanup, render } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { Dialog } from '~/components/common/Dialog'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
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

vi.mock('~/components/shell/UserMenuState', () => ({
  setShowPreferencesDialog: vi.fn(),
}))

vi.mock('~/lib/fileTreeOps', () => ({
  refreshFileTree: () => refreshFileTree(),
  toggleHiddenFiles: () => toggleHiddenFiles(),
}))

let originalShowModal: typeof HTMLDialogElement.prototype.showModal | undefined
let originalClose: typeof HTMLDialogElement.prototype.close

beforeAll(() => {
  if (!HTMLDialogElement.prototype.showModal) {
    originalShowModal = undefined
    HTMLDialogElement.prototype.showModal = vi.fn(function (this: HTMLDialogElement) {
      this.setAttribute('open', '')
    })
  }
  else {
    originalShowModal = HTMLDialogElement.prototype.showModal
  }

  originalClose = HTMLDialogElement.prototype.close
  HTMLDialogElement.prototype.close = function () {
    this.removeAttribute('open')
    this.dispatchEvent(new Event('close'))
  }
})

afterEach(() => {
  cleanup()
  resetCommands()
  refreshFileTree.mockReset()
  toggleHiddenFiles.mockReset()
})

afterAll(() => {
  if (originalShowModal) {
    HTMLDialogElement.prototype.showModal = originalShowModal
  }
  HTMLDialogElement.prototype.close = originalClose
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
    resolveFocusedTab: () => null,
    splitFocusedTile: vi.fn(),
    scrollFocusedTabPage: vi.fn(),
    writeToFocusedTerminal: vi.fn(),
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

  it('routes page scroll commands through the focused-tile dispatcher for chat and terminal tabs', () => {
    const props = makeProps() as any
    let activeTabType: TabType | null = TabType.AGENT
    props.activeTabType = () => activeTabType

    render(() => {
      useShortcuts(props as any)
      return null
    })

    executeCommand('app.scrollActiveTabPageUp')
    activeTabType = TabType.TERMINAL
    executeCommand('app.scrollActiveTabPageDown')

    expect(props.scrollFocusedTabPage).toHaveBeenNthCalledWith(1, -1)
    expect(props.scrollFocusedTabPage).toHaveBeenNthCalledWith(2, 1)
  })

  it('routes terminal write commands through the focused terminal dispatcher', () => {
    const props = makeProps()

    render(() => {
      useShortcuts(props as any)
      return null
    })

    executeCommand('terminal.lineStart')
    executeCommand('terminal.lineEnd')
    executeCommand('terminal.wordLeft')
    executeCommand('terminal.wordRight')

    expect(props.writeToFocusedTerminal).toHaveBeenNthCalledWith(1, '\x01')
    expect(props.writeToFocusedTerminal).toHaveBeenNthCalledWith(2, '\x05')
    expect(props.writeToFocusedTerminal).toHaveBeenNthCalledWith(3, '\x1Bb')
    expect(props.writeToFocusedTerminal).toHaveBeenNthCalledWith(4, '\x1Bf')
  })

  it('closes the active tab from the focused tile', () => {
    const props = makeProps() as any
    const tab = { type: TabType.TERMINAL, id: 'term-1', tileId: 'tile-1' }
    props.resolveFocusedTab = () => tab

    render(() => {
      useShortcuts(props as any)
      return null
    })

    executeCommand('app.closeActiveTab')

    expect(props.tabOps.handleTabClose).toHaveBeenCalledWith(tab)
  })

  it('closes the topmost open dialog without redispatching Escape', () => {
    const props = makeProps()
    const onClose = vi.fn()

    render(() => {
      useShortcuts(props as any)
      return <Dialog title="Test" onClose={onClose}><p>Content</p></Dialog>
    })

    const dialog = document.querySelector('dialog') as HTMLDialogElement
    const dispatchSpy = vi.spyOn(dialog, 'dispatchEvent')

    executeCommand('dialog.close')

    expect(dialog.hasAttribute('open')).toBe(false)
    expect(onClose).toHaveBeenCalledOnce()
    expect(dispatchSpy).not.toHaveBeenCalledWith(expect.objectContaining({ type: 'keydown' }))
  })

  it('does not close a busy dialog from the global dialog.close command', () => {
    const props = makeProps()
    const onClose = vi.fn()

    render(() => {
      useShortcuts(props as any)
      return <Dialog title="Test" busy onClose={onClose}><p>Content</p></Dialog>
    })

    const dialog = document.querySelector('dialog') as HTMLDialogElement

    executeCommand('dialog.close')

    expect(dialog.hasAttribute('open')).toBe(true)
    expect(onClose).not.toHaveBeenCalled()
  })
})
