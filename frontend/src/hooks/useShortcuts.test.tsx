import { cleanup, render } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ExternalAppKind } from '~/generated/proto/leapmux/desktop/v1/frame_pb'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { executeCommand, getCommand, resetCommands } from '~/lib/shortcuts/commands'
import { evaluateWhen } from '~/lib/shortcuts/context'
import { registerPanelSend, unregisterPanelSend } from '~/stores/focusedChatSend.store'
import { useShortcuts } from './useShortcuts'

const refreshFileTree = vi.fn()
const toggleHiddenFiles = vi.fn()

const openInExternalAppMock = vi.fn()
const runtimeStateMock = vi.fn()
const loadExternalAppsMock = vi.fn()
const showWarnToastMock = vi.fn()

vi.mock('~/api/platformBridge', () => ({
  getRuntimeState: () => runtimeStateMock(),
  isTauriApp: () => false,
  openWebInspector: vi.fn(),
  platformBridge: {
    openInExternalApp: (...args: unknown[]) => openInExternalAppMock(...args),
  },
  quitApp: vi.fn(),
  resetWebviewZoom: vi.fn(),
  setMenuItemAccelerator: vi.fn(),
  zoomInWebview: vi.fn(),
  zoomOutWebview: vi.fn(),
}))

// Only the DETECTION is mocked. `resolvePreferredExternalApp` takes the pin and
// the writer as arguments, so the real one runs here — a hand-mirrored copy
// of its logic in this file could pass while the real function was wrong.
vi.mock('~/lib/externalApps', async importOriginal => ({
  ...await importOriginal<typeof import('~/lib/externalApps')>(),
  loadExternalApps: () => loadExternalAppsMock(),
}))

// Spread the original: this module has other exports, and the graph under test
// reaches them through modules this file never names.
vi.mock('~/components/common/Toast', async importOriginal => ({
  ...await importOriginal<typeof import('~/components/common/Toast')>(),
  showWarnToast: (...args: unknown[]) => showWarnToastMock(...args),
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
  openInExternalAppMock.mockReset()
  runtimeStateMock.mockReset()
  loadExternalAppsMock.mockReset()
  showWarnToastMock.mockReset()
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
    isActiveWorkspaceArchived: () => false,
    splitFocusedTile: vi.fn(),
    scrollFocusedTabPage: vi.fn(),
    writeToFocusedTerminal: vi.fn(),
    getCurrentTabContext: () => ({ workerId: '', workingDir: '', homeDir: '', gitToplevel: '' }),
    customKeybindings: () => [],
    preferredExternalAppId: (): string | undefined => undefined,
    setPreferredExternalAppId: vi.fn(),
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

  it.each([TabType.AGENT, TabType.TERMINAL, TabType.FILE, TabType.IMAGE])(
    'refuses to close tab type %s in an archived workspace',
    (type) => {
      const props = makeProps() as any
      props.resolveFocusedTab = () => ({ type, id: 'tab-1', tileId: 'tile-1' })
      props.isActiveWorkspaceArchived = () => true

      render(() => {
        useShortcuts(props as any)
        return null
      })

      executeCommand('app.closeActiveTab')

      expect(props.tabOps.handleTabClose).not.toHaveBeenCalled()
    },
  )

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

  describe('app.openInExternalApp', () => {
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
      loadExternalAppsMock.mockResolvedValue([{ id: 'vscode', displayName: 'VS Code' }])

      render(() => {
        useShortcuts(props as any)
        return null
      })

      await getCommand('app.openInExternalApp')!.handler()
      expect(openInExternalAppMock).not.toHaveBeenCalled()
    })

    it('does nothing when not in solo mode', async () => {
      const props = makeSoloProps('/p')
      runtimeStateMock.mockResolvedValue(soloRuntime(false))
      loadExternalAppsMock.mockResolvedValue([{ id: 'vscode', displayName: 'VS Code' }])

      render(() => {
        useShortcuts(props as any)
        return null
      })

      await getCommand('app.openInExternalApp')!.handler()
      expect(openInExternalAppMock).not.toHaveBeenCalled()
    })

    it('does nothing when no applications are detected', async () => {
      const props = makeSoloProps('/p')
      runtimeStateMock.mockResolvedValue(soloRuntime(true))
      loadExternalAppsMock.mockResolvedValue([])

      render(() => {
        useShortcuts(props as any)
        return null
      })

      await getCommand('app.openInExternalApp')!.handler()
      expect(openInExternalAppMock).not.toHaveBeenCalled()
    })

    it('opens the remembered application when set', async () => {
      const props = makeSoloProps('/p')
      runtimeStateMock.mockResolvedValue(soloRuntime(true))
      loadExternalAppsMock.mockResolvedValue([
        { id: 'vscode', displayName: 'VS Code' },
        { id: 'zed', displayName: 'Zed' },
      ])
      props.preferredExternalAppId = () => 'zed'

      render(() => {
        useShortcuts(props as any)
        return null
      })

      await getCommand('app.openInExternalApp')!.handler()
      expect(openInExternalAppMock).toHaveBeenCalledWith('zed', '/p')
      expect(props.setPreferredExternalAppId).not.toHaveBeenCalled()
    })

    it('falls back to first detected application when MRU is unset', async () => {
      const props = makeSoloProps('/p')
      runtimeStateMock.mockResolvedValue(soloRuntime(true))
      loadExternalAppsMock.mockResolvedValue([
        { id: 'vscode', displayName: 'VS Code' },
        { id: 'zed', displayName: 'Zed' },
      ])
      props.preferredExternalAppId = () => undefined

      render(() => {
        useShortcuts(props as any)
        return null
      })

      await getCommand('app.openInExternalApp')!.handler()
      expect(openInExternalAppMock).toHaveBeenCalledWith('vscode', '/p')
      // The fallback persists through the REACTIVE preference the props
      // carry, so the app menu and the settings row see it too.
      expect(props.setPreferredExternalAppId).toHaveBeenCalledWith('vscode')
    })

    it('falls back to first detected when MRU points at an uninstalled application', async () => {
      const props = makeSoloProps('/p')
      runtimeStateMock.mockResolvedValue(soloRuntime(true))
      loadExternalAppsMock.mockResolvedValue([
        { id: 'vscode', displayName: 'VS Code' },
      ])
      props.preferredExternalAppId = () => 'zed'

      render(() => {
        useShortcuts(props as any)
        return null
      })

      await getCommand('app.openInExternalApp')!.handler()
      expect(openInExternalAppMock).toHaveBeenCalledWith('vscode', '/p')
      expect(props.setPreferredExternalAppId).toHaveBeenCalledWith('vscode')
    })

    // The file manager is a first-class choice, so the shortcut opens it like
    // any other application rather than treating it as a target it must skip.
    it('opens the file manager when that is the remembered application', async () => {
      const props = makeSoloProps('/p')
      runtimeStateMock.mockResolvedValue(soloRuntime(true))
      loadExternalAppsMock.mockResolvedValue([
        { id: 'file-manager', displayName: 'Finder', kind: ExternalAppKind.FILE_MANAGER },
        { id: 'vscode', displayName: 'VS Code', kind: ExternalAppKind.EDITOR },
      ])
      props.preferredExternalAppId = () => 'file-manager'

      render(() => {
        useShortcuts(props as any)
        return null
      })

      await getCommand('app.openInExternalApp')!.handler()
      expect(openInExternalAppMock).toHaveBeenCalledWith('file-manager', '/p')
      expect(props.setPreferredExternalAppId).not.toHaveBeenCalled()
    })

    // ...but it is NOT the implicit default. It leads the detected list on
    // every platform and is always present, so an unset pin that took the
    // first entry would open Finder on a machine with an editor installed.
    it('prefers an editor over the file manager when nothing is remembered', async () => {
      const props = makeSoloProps('/p')
      runtimeStateMock.mockResolvedValue(soloRuntime(true))
      loadExternalAppsMock.mockResolvedValue([
        { id: 'file-manager', displayName: 'Finder', kind: ExternalAppKind.FILE_MANAGER },
        { id: 'vscode', displayName: 'VS Code', kind: ExternalAppKind.EDITOR },
      ])
      props.preferredExternalAppId = () => undefined

      render(() => {
        useShortcuts(props as any)
        return null
      })

      await getCommand('app.openInExternalApp')!.handler()
      expect(openInExternalAppMock).toHaveBeenCalledWith('vscode', '/p')
      expect(props.setPreferredExternalAppId).toHaveBeenCalledWith('vscode')
    })

    // A refused launch is indistinguishable from an application opening behind
    // this window, so it is reported rather than only logged.
    it('reports a refused launch, naming the application', async () => {
      const props = makeSoloProps('/p')
      runtimeStateMock.mockResolvedValue(soloRuntime(true))
      loadExternalAppsMock.mockResolvedValue([
        { id: 'vscode', displayName: 'VS Code', kind: ExternalAppKind.EDITOR },
      ])
      const refused = new Error('launch Visual Studio Code: exit status 1')
      openInExternalAppMock.mockRejectedValue(refused)

      render(() => {
        useShortcuts(props as any)
        return null
      })

      await getCommand('app.openInExternalApp')!.handler()
      expect(showWarnToastMock).toHaveBeenCalledTimes(1)
      expect(showWarnToastMock.mock.calls[0]![0]).toContain('VS Code')
      expect(showWarnToastMock.mock.calls[0]![1]).toBe(refused)
    })
  })
})
