import { cleanup, render } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { executeCommand, getCommand, resetCommands } from '~/lib/shortcuts/commands'
import { evaluateWhen } from '~/lib/shortcuts/context'
import { registerPanelSend, unregisterPanelSend } from '~/stores/focusedChatSend.store'
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

// Only the DETECTION is mocked. `resolvePreferredEditor` takes the pin and
// the writer as arguments, so the real one runs here — a hand-mirrored copy
// of its logic in this file could pass while the real function was wrong.
vi.mock('~/lib/externalEditors', async importOriginal => ({
  ...await importOriginal<typeof import('~/lib/externalEditors')>(),
  loadDetectedEditors: () => loadDetectedEditorsMock(),
}))

vi.mock('~/components/shell/UserMenuState', () => ({
  openPreferences: vi.fn(),
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
  openInEditorMock.mockReset()
  runtimeStateMock.mockReset()
  loadDetectedEditorsMock.mockReset()
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
      splitTile: vi.fn(),
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
    newAgentDialog: { open: vi.fn(), close: vi.fn(), isOpen: () => false },
    newTerminalDialog: { open: vi.fn(), close: vi.fn(), isOpen: () => false },
    newWorkspaceDialog: { open: vi.fn(), close: vi.fn(), value: () => null },
    hasActiveWorkspace: () => true,
    toggleFloatingTab: vi.fn(),
    toggleLeftSidebar: vi.fn(),
    toggleRightSidebar: vi.fn(),
    activeTabType: () => null,
    resolveFocusedTab: () => null,
    splitFocusedTile: vi.fn(),
    scrollFocusedTabPage: vi.fn(),
    writeToFocusedTerminal: vi.fn(),
    getCurrentTabContext: () => ({ workerId: '', workingDir: '', homeDir: '', gitToplevel: '' }),
    customKeybindings: () => [],
    preferredEditorId: (): string | undefined => undefined,
    setPreferredEditorId: vi.fn(),
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

  describe('without an active workspace', () => {
    function makeNoWorkspaceProps() {
      const props = makeProps() as any
      props.hasActiveWorkspace = () => false
      props.newWorkspaceDialog = { open: vi.fn(), close: vi.fn(), value: () => null }
      return props
    }

    it('redirects newAgent and newAgentDialog to the new-workspace dialog', () => {
      const props = makeNoWorkspaceProps()

      render(() => {
        useShortcuts(props)
        return null
      })

      executeCommand('app.newAgent')
      expect(props.newWorkspaceDialog.open).toHaveBeenCalledWith({})
      expect(props.agentOps.handleOpenAgent).not.toHaveBeenCalled()

      executeCommand('app.newAgentDialog')
      expect(props.newWorkspaceDialog.open).toHaveBeenCalledTimes(2)
      expect(props.newAgentDialog.open).not.toHaveBeenCalled()
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
      expect(props.newTerminalDialog.open).not.toHaveBeenCalled()
      expect(props.newWorkspaceDialog.open).not.toHaveBeenCalled()
    })
  })

  describe('chat.sendMessage', () => {
    afterEach(() => {
      document.body.innerHTML = ''
    })

    it('invokes the registered send fn for the panel containing focused element', () => {
      const props = makeProps()

      render(() => {
        useShortcuts(props as any)
        return null
      })

      const panel = document.createElement('div')
      panel.setAttribute('data-chat-panel', '')
      const input = document.createElement('input')
      panel.appendChild(input)
      document.body.appendChild(panel)

      const send = vi.fn()
      registerPanelSend(panel, send)
      input.focus()

      executeCommand('chat.sendMessage')
      expect(send).toHaveBeenCalledOnce()

      unregisterPanelSend(panel)
    })

    it('is a no-op when focus is not inside a chat panel', () => {
      const props = makeProps()

      render(() => {
        useShortcuts(props as any)
        return null
      })

      const send = vi.fn()
      const panel = document.createElement('div')
      panel.setAttribute('data-chat-panel', '')
      document.body.appendChild(panel)
      registerPanelSend(panel, send)

      const outside = document.createElement('input')
      document.body.appendChild(outside)
      outside.focus()

      executeCommand('chat.sendMessage')
      expect(send).not.toHaveBeenCalled()

      unregisterPanelSend(panel)
    })

    it('chatInputFocused is true only when focus is inside [data-chat-input]', () => {
      const props = makeProps()

      render(() => {
        useShortcuts(props as any)
        return null
      })

      const editor = document.createElement('div')
      editor.setAttribute('data-chat-input', '')
      const inside = document.createElement('input')
      editor.appendChild(inside)

      const outside = document.createElement('input')
      document.body.append(editor, outside)

      inside.focus()
      expect(evaluateWhen('chatInputFocused')).toBe(true)

      outside.focus()
      expect(evaluateWhen('chatInputFocused')).toBe(false)
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
        gitToplevel: '',
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
      props.preferredEditorId = () => 'zed'

      render(() => {
        useShortcuts(props as any)
        return null
      })

      await getCommand('app.openInExternalEditor')!.handler()
      expect(openInEditorMock).toHaveBeenCalledWith('zed', '/p')
      expect(props.setPreferredEditorId).not.toHaveBeenCalled()
    })

    it('falls back to first detected editor when MRU is unset', async () => {
      const props = makeSoloProps('/p')
      runtimeStateMock.mockResolvedValue(soloRuntime(true))
      loadDetectedEditorsMock.mockResolvedValue([
        { id: 'vscode', displayName: 'VS Code' },
        { id: 'zed', displayName: 'Zed' },
      ])
      props.preferredEditorId = () => undefined

      render(() => {
        useShortcuts(props as any)
        return null
      })

      await getCommand('app.openInExternalEditor')!.handler()
      expect(openInEditorMock).toHaveBeenCalledWith('vscode', '/p')
      // The fallback persists through the REACTIVE preference the props
      // carry, so the editor menu and the settings row see it too.
      expect(props.setPreferredEditorId).toHaveBeenCalledWith('vscode')
    })

    it('falls back to first detected when MRU points at an uninstalled editor', async () => {
      const props = makeSoloProps('/p')
      runtimeStateMock.mockResolvedValue(soloRuntime(true))
      loadDetectedEditorsMock.mockResolvedValue([
        { id: 'vscode', displayName: 'VS Code' },
      ])
      props.preferredEditorId = () => 'zed'

      render(() => {
        useShortcuts(props as any)
        return null
      })

      await getCommand('app.openInExternalEditor')!.handler()
      expect(openInEditorMock).toHaveBeenCalledWith('vscode', '/p')
      expect(props.setPreferredEditorId).toHaveBeenCalledWith('vscode')
    })
  })
})
