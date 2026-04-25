import { cleanup, render } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { Dialog } from '~/components/common/Dialog'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { executeCommand, getCommand, resetCommands } from '~/lib/shortcuts/commands'
import { useShortcuts } from './useShortcuts'

const refreshFileTree = vi.fn()
const toggleHiddenFiles = vi.fn()

const openInEditorMock = vi.fn()
const runtimeStateMock = vi.fn()
const loadDetectedEditorsMock = vi.fn()

vi.mock('~/api/platformBridge', () => ({
  getRuntimeState: () => runtimeStateMock(),
  isTauriApp: () => false,
  openWebInspector: vi.fn(),
  platformBridge: {
    openInEditor: (...args: unknown[]) => openInEditorMock(...args),
  },
  quitApp: vi.fn(),
  resetWebviewZoom: vi.fn(),
  setMenuItemAccelerator: vi.fn(),
  zoomInWebview: vi.fn(),
  zoomOutWebview: vi.fn(),
}))

vi.mock('~/lib/externalEditors', () => {
  // vi.mock replaces the module from importers' perspective, but does NOT
  // rewrite intra-module references — so the real `resolvePreferredEditor`
  // would call the real `getPreferredEditorId`, bypassing the test mocks.
  // Mirror the (tiny) prod logic here against the mocked getters/setters.
  // The real implementation is covered separately in `externalEditors.test.ts`.
  const getPreferredEditorId = vi.fn<() => string | undefined>()
  const setPreferredEditorId = vi.fn<(id: string) => void>()
  return {
    getPreferredEditorId,
    setPreferredEditorId,
    loadDetectedEditors: () => loadDetectedEditorsMock(),
    resolvePreferredEditor: <T extends { id: string }>(editors: readonly T[]): T | undefined => {
      if (editors.length === 0)
        return undefined
      const mru = getPreferredEditorId()
      const target = editors.find(e => e.id === mru) ?? editors[0]
      if (target.id !== mru)
        setPreferredEditorId(target.id)
      return target
    },
  }
})

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
  openInEditorMock.mockReset()
  runtimeStateMock.mockReset()
  loadDetectedEditorsMock.mockReset()
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
    hasActiveWorkspace: () => true,
    toggleFloatingTab: vi.fn(),
    toggleLeftSidebar: vi.fn(),
    toggleRightSidebar: vi.fn(),
    activeTabType: () => null,
    resolveFocusedTab: () => null,
    splitFocusedTile: vi.fn(),
    scrollFocusedTabPage: vi.fn(),
    writeToFocusedTerminal: vi.fn(),
    getCurrentTabContext: () => ({ workerId: '', workingDir: '', homeDir: '' }),
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

  describe('without an active workspace', () => {
    function makeNoWorkspaceProps() {
      const props = makeProps() as any
      props.hasActiveWorkspace = () => false
      props.setShowNewWorkspace = vi.fn()
      return props
    }

    it('redirects newAgent and newAgentDialog to the new-workspace dialog', () => {
      const props = makeNoWorkspaceProps()

      render(() => {
        useShortcuts(props)
        return null
      })

      executeCommand('app.newAgent')
      expect(props.setShowNewWorkspace).toHaveBeenCalledWith(true)
      expect(props.agentOps.handleOpenAgent).not.toHaveBeenCalled()

      executeCommand('app.newAgentDialog')
      expect(props.setShowNewWorkspace).toHaveBeenCalledTimes(2)
      expect(props.setShowNewAgentDialog).not.toHaveBeenCalled()
    })

    it('makes newTerminal and newTerminalDialog a no-op', () => {
      const props = makeNoWorkspaceProps()

      render(() => {
        useShortcuts(props)
        return null
      })

      executeCommand('app.newTerminal')
      executeCommand('app.newTerminalDialog')

      expect(props.termOps.handleOpenTerminal).not.toHaveBeenCalled()
      expect(props.setShowNewTerminalDialog).not.toHaveBeenCalled()
      expect(props.setShowNewWorkspace).not.toHaveBeenCalled()
    })
  })

  describe('app.openInExternalEditor', () => {
    // Don't use a default parameter — JS treats `makeSoloProps(undefined)` as
    // "no argument supplied" and substitutes the default, which is the
    // opposite of what we want for the no-workingDir case.
    function makeSoloProps(workingDir: string | undefined) {
      const props = makeProps()
      props.getCurrentTabContext = () => ({
        workerId: '',
        workingDir: workingDir ?? '',
        homeDir: '',
      })
      return props
    }

    function soloRuntime(localSolo = true) {
      return {
        shellMode: localSolo ? 'solo' : 'distributed',
        connected: true,
        hubUrl: '',
        capabilities: {
          mode: 'tauri-desktop-solo',
          hubTransport: 'proxy',
          tunnels: true,
          appControl: true,
          windowControl: true,
          systemPermissions: true,
          localSolo,
        },
      }
    }

    it('does nothing when there is no active working dir', async () => {
      const props = makeSoloProps(undefined)
      runtimeStateMock.mockResolvedValue(soloRuntime(true))
      loadDetectedEditorsMock.mockResolvedValue([{ id: 'vscode', displayName: 'VS Code' }])

      render(() => {
        useShortcuts(props as any)
        return null
      })

      await getCommand('app.openInExternalEditor')!.handler()
      expect(openInEditorMock).not.toHaveBeenCalled()
    })

    it('does nothing when not in solo mode', async () => {
      const props = makeSoloProps('/p')
      runtimeStateMock.mockResolvedValue(soloRuntime(false))
      loadDetectedEditorsMock.mockResolvedValue([{ id: 'vscode', displayName: 'VS Code' }])

      render(() => {
        useShortcuts(props as any)
        return null
      })

      await getCommand('app.openInExternalEditor')!.handler()
      expect(openInEditorMock).not.toHaveBeenCalled()
    })

    it('does nothing when no editors are detected', async () => {
      const props = makeSoloProps('/p')
      runtimeStateMock.mockResolvedValue(soloRuntime(true))
      loadDetectedEditorsMock.mockResolvedValue([])

      render(() => {
        useShortcuts(props as any)
        return null
      })

      await getCommand('app.openInExternalEditor')!.handler()
      expect(openInEditorMock).not.toHaveBeenCalled()
    })

    it('opens the MRU editor when set', async () => {
      const props = makeSoloProps('/p')
      runtimeStateMock.mockResolvedValue(soloRuntime(true))
      loadDetectedEditorsMock.mockResolvedValue([
        { id: 'vscode', displayName: 'VS Code' },
        { id: 'zed', displayName: 'Zed' },
      ])
      const editors = await import('~/lib/externalEditors')
      vi.mocked(editors.getPreferredEditorId).mockReturnValue('zed')

      render(() => {
        useShortcuts(props as any)
        return null
      })

      await getCommand('app.openInExternalEditor')!.handler()
      expect(openInEditorMock).toHaveBeenCalledWith('zed', '/p')
    })

    it('falls back to first detected editor when MRU is unset', async () => {
      const props = makeSoloProps('/p')
      runtimeStateMock.mockResolvedValue(soloRuntime(true))
      loadDetectedEditorsMock.mockResolvedValue([
        { id: 'vscode', displayName: 'VS Code' },
        { id: 'zed', displayName: 'Zed' },
      ])
      const editors = await import('~/lib/externalEditors')
      vi.mocked(editors.getPreferredEditorId).mockReturnValue(undefined)

      render(() => {
        useShortcuts(props as any)
        return null
      })

      await getCommand('app.openInExternalEditor')!.handler()
      expect(openInEditorMock).toHaveBeenCalledWith('vscode', '/p')
      expect(editors.setPreferredEditorId).toHaveBeenCalledWith('vscode')
    })

    it('falls back to first detected when MRU points at an uninstalled editor', async () => {
      const props = makeSoloProps('/p')
      runtimeStateMock.mockResolvedValue(soloRuntime(true))
      loadDetectedEditorsMock.mockResolvedValue([
        { id: 'vscode', displayName: 'VS Code' },
      ])
      const editors = await import('~/lib/externalEditors')
      vi.mocked(editors.getPreferredEditorId).mockReturnValue('zed')

      render(() => {
        useShortcuts(props as any)
        return null
      })

      await getCommand('app.openInExternalEditor')!.handler()
      expect(openInEditorMock).toHaveBeenCalledWith('vscode', '/p')
      expect(editors.setPreferredEditorId).toHaveBeenCalledWith('vscode')
    })
  })
})
