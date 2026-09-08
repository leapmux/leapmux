import type { AgentInputQueueSnapshot } from '~/generated/proto/leapmux/v1/agent_pb'
import { cleanup, render } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { executeCommand, getCommand, resetCommands } from '~/lib/shortcuts/commands'
import { evaluateWhen, getContext } from '~/lib/shortcuts/context'
import { registerChatPanel, unregisterChatPanel } from '~/stores/focusedChatPanel.store'
import { editorApp, fileManagerApp } from '~/test-support/externalAppFixtures'
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
  showWarnToastWithLoggedCause: (...args: unknown[]) => showWarnToastMock(...args),
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
  // Reset to a RESOLVED promise, not to undefined: `openInExternalApp` is
  // declared `Promise<void>`, and the launch path attaches a `.catch` to it.
  openInExternalAppMock.mockReset()
  openInExternalAppMock.mockResolvedValue(undefined)
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
    getAgentInputQueue: (): AgentInputQueueSnapshot | undefined => undefined,
    steerQueueItem: vi.fn(async () => {}),
    quakePanel: { open: vi.fn(), close: vi.fn(), toggle: vi.fn() },
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
      registerChatPanel(panel, { send, hasPendingInput: () => true })
      input.focus()

      executeCommand('chat.sendMessage')
      expect(send).toHaveBeenCalledOnce()

      unregisterChatPanel(panel)
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
      registerChatPanel(panel, { send, hasPendingInput: () => true })

      const outside = document.createElement('input')
      document.body.appendChild(outside)
      outside.focus()

      executeCommand('chat.sendMessage')
      expect(send).not.toHaveBeenCalled()

      unregisterChatPanel(panel)
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

  describe('chat.steerQueuedInput', () => {
    afterEach(() => {
      document.body.innerHTML = ''
    })

    /** Props for an agent tab with a queue whose head reports `canSteer`. */
    function steerProps(over: {
      supportsSteering?: boolean
      items?: { canSteer: boolean, id: string }[]
      snapshot?: boolean
      tab?: unknown
    } = {}) {
      const props = makeProps()
      const items = over.items ?? [{ canSteer: true, id: 'q1' }]
      const tab = 'tab' in over
        ? over.tab
        : { type: TabType.AGENT, id: 'a1', supportsSteering: over.supportsSteering ?? true }
      return {
        ...props,
        resolveFocusedTab: () => tab,
        getAgentInputQueue: () => (over.snapshot === false ? undefined : { items }),
        steerQueueItem: vi.fn(async () => {}),
      }
    }

    // The parameter is deliberately not called `props`: these are plain test
    // values, and the reactivity lint reads any `props.x` as a prop access that
    // must sit in a tracked scope.
    function run(bag: ReturnType<typeof steerProps>) {
      render(() => {
        useShortcuts(bag as any)
        return null
      })
      executeCommand('chat.steerQueuedInput')
      return bag.steerQueueItem
    }

    it('steers the queue head of the focused agent tab', () => {
      const props = steerProps()
      const steer = run(props)
      expect(steer).toHaveBeenCalledWith({ canSteer: true, id: 'q1' })
    })

    it('does nothing when the agent provider does not accept a steer', () => {
      expect(run(steerProps({ supportsSteering: false }))).not.toHaveBeenCalled()
    })

    it('does nothing when the worker refuses to steer the head', () => {
      expect(run(steerProps({ items: [{ canSteer: false, id: 'q1' }] }))).not.toHaveBeenCalled()
    })

    it('does nothing when the queue is empty', () => {
      expect(run(steerProps({ items: [] }))).not.toHaveBeenCalled()
    })

    it('does nothing before the first queue snapshot arrives', () => {
      expect(run(steerProps({ snapshot: false }))).not.toHaveBeenCalled()
    })

    it('does nothing when no tab is focused', () => {
      expect(run(steerProps({ tab: null }))).not.toHaveBeenCalled()
    })

    it('does nothing when the focused tab is a terminal', () => {
      expect(run(steerProps({ tab: { type: TabType.TERMINAL, id: 't1' } }))).not.toHaveBeenCalled()
    })

    // The worker marks `canSteer` on index 0 alone, so a scan would answer a
    // different question than the one the worker will enforce.
    it('steers the head alone, although a later item reports canSteer', () => {
      const steer = run(steerProps({ items: [{ canSteer: false, id: 'q1' }, { canSteer: true, id: 'q2' }] }))
      expect(steer).not.toHaveBeenCalled()
    })
  })

  describe('chatInputEmpty', () => {
    afterEach(() => {
      document.body.innerHTML = ''
    })

    function mountPanel(hasPendingInput: () => boolean) {
      const panel = document.createElement('div')
      panel.setAttribute('data-chat-panel', '')
      document.body.appendChild(panel)
      registerChatPanel(panel, { send: vi.fn(), hasPendingInput })
      return panel
    }

    it('is true when the current composer has nothing to submit', () => {
      const props = makeProps()
      render(() => {
        useShortcuts(props as any)
        return null
      })
      const panel = mountPanel(() => false)
      expect(evaluateWhen('chatInputEmpty')).toBe(true)
      unregisterChatPanel(panel)
    })

    // Text, an attachment, or a control request awaiting approval all count as
    // something to submit -- that is what leaves the chord to the composer.
    it('is false while the current composer has something to submit', () => {
      const props = makeProps()
      render(() => {
        useShortcuts(props as any)
        return null
      })
      const panel = mountPanel(() => true)
      expect(evaluateWhen('chatInputEmpty')).toBe(false)
      unregisterChatPanel(panel)
    })

    // Focus is deliberately NOT consulted: the action is about the current tab,
    // so it works from the transcript too.
    it('answers for the current composer even when focus is elsewhere', () => {
      const props = makeProps()
      render(() => {
        useShortcuts(props as any)
        return null
      })
      const panel = mountPanel(() => false)
      const outside = document.createElement('input')
      document.body.appendChild(outside)
      outside.focus()
      expect(evaluateWhen('chatInputEmpty')).toBe(true)
      unregisterChatPanel(panel)
    })

    // `!undefined?.hasPendingInput()` is true, so without the explicit check a
    // missing composer would read as an empty one and claim the chord where
    // there is no chat at all.
    it('is false when no chat panel is mounted', () => {
      const props = makeProps()
      render(() => {
        useShortcuts(props as any)
        return null
      })
      expect(evaluateWhen('chatInputEmpty')).toBe(false)
    })

    it('stops answering once the hook tears down', () => {
      const props = makeProps()
      render(() => {
        useShortcuts(props as any)
        return null
      })
      const panel = mountPanel(() => false)
      cleanup()
      expect(getContext('chatInputEmpty')).toBeUndefined()
      unregisterChatPanel(panel)
    })
  })

  describe('the quake terminal commands', () => {
    function quakeProps(tab: unknown) {
      return {
        ...makeProps(),
        resolveFocusedTab: () => tab,
        quakePanel: { open: vi.fn(), close: vi.fn(), toggle: vi.fn() },
      }
    }

    function run(props: ReturnType<typeof quakeProps>, command: string) {
      render(() => {
        useShortcuts(props as any)
        return null
      })
      executeCommand(command)
      return props.quakePanel
    }

    const agentTab = { type: TabType.AGENT, id: 'a1' }

    it('toggles the panel of the focused agent tab', () => {
      const panel = run(quakeProps(agentTab), 'terminal.toggleQuake')
      expect(panel.toggle).toHaveBeenCalledWith(agentTab)
    })

    it('opens and closes through their own commands', () => {
      const opened = run(quakeProps(agentTab), 'terminal.openQuake')
      expect(opened.open).toHaveBeenCalledWith(agentTab)
      cleanup()
      const closed = run(quakeProps(agentTab), 'terminal.closeQuake')
      expect(closed.close).toHaveBeenCalledWith('a1')
    })

    it('does nothing when the focused tab is not an agent', () => {
      const panel = run(quakeProps({ type: TabType.TERMINAL, id: 't1' }), 'terminal.toggleQuake')
      expect(panel.toggle).not.toHaveBeenCalled()
    })

    it('does nothing when no tab is focused', () => {
      const panel = run(quakeProps(null), 'terminal.toggleQuake')
      expect(panel.toggle).not.toHaveBeenCalled()
    })

    /**
     * A subagent transcript owns no process, so it owns no companion shell.
     *
     * The worker refuses `SetQuakePanel` for one, and the keyboard must not be
     * the one route around that: a companion owned by a child agent would also
     * outlive its tab, because only a ROOT close tears one down.
     */
    it('does nothing for a subagent tab', () => {
      const panel = run(quakeProps({ type: TabType.AGENT, id: 'c1', parentAgentId: 'a1' }), 'terminal.toggleQuake')
      expect(panel.toggle).not.toHaveBeenCalled()
    })

    // The archived-workspace refusal is NOT here. It belongs to the opening
    // direction alone and lives in the store, which the Control CLI reaches
    // too -- a refusal at this level also refused the CLOSE half of the toggle.
    it('leaves the archived-workspace refusal to the store', () => {
      const panel = run(quakeProps(agentTab), 'terminal.toggleQuake')
      expect(panel.toggle).toHaveBeenCalledWith(agentTab)
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
        fileManagerApp('Finder'),
        editorApp('vscode', 'VS Code'),
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
        fileManagerApp('Finder'),
        editorApp('vscode', 'VS Code'),
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
        editorApp('vscode', 'VS Code'),
      ])
      const refused = new Error('launch Visual Studio Code: exit status 1')
      openInExternalAppMock.mockRejectedValue(refused)

      render(() => {
        useShortcuts(props as any)
        return null
      })

      await getCommand('app.openInExternalApp')!.handler()
      await vi.waitFor(() => expect(showWarnToastMock).toHaveBeenCalledTimes(1))
      expect(showWarnToastMock.mock.calls[0]![0]).toContain('VS Code')
      expect(showWarnToastMock.mock.calls[0]![1]).toBe(refused)
    })

    // Tauri rejects with the `Err(String)` the Rust command returned, NOT with
    // an Error, and that string is the only place the sidecar's reason lives.
    // A toast built from `formatErrorMessage` dropped it and showed the
    // caller's sentence alone.
    it('puts the sidecar reason in the toast when the rejection is a plain string', async () => {
      const props = makeSoloProps('/p')
      runtimeStateMock.mockResolvedValue(soloRuntime(true))
      loadExternalAppsMock.mockResolvedValue([
        editorApp('zed', 'Zed'),
      ])
      openInExternalAppMock.mockRejectedValue('launch Zed: exit status 1: bundle is missing')

      render(() => {
        useShortcuts(props as any)
        return null
      })

      await getCommand('app.openInExternalApp')!.handler()
      await vi.waitFor(() => expect(showWarnToastMock).toHaveBeenCalledTimes(1))
      expect(showWarnToastMock.mock.calls[0]![0]).toBe(
        'Could not open Zed: launch Zed: exit status 1: bundle is missing',
      )
    })
  })
})
