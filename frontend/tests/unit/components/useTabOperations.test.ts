import { createRoot } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useTabOperations } from '~/components/shell/useTabOperations'
import { WorktreeAction } from '~/generated/leapmux/v1/common_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createChatStore, MAX_BACKGROUND_CHAT_MESSAGES } from '~/stores/chat.store'
import { createFloatingWindowStore } from '~/stores/floatingWindow.store'
import { createLayoutStore } from '~/stores/layout.store'
import { createTabStore } from '~/stores/tab.store'
import { installTestBridge } from '../helpers/crdtBridge'

const mockInspectLastTabClose = vi.fn()
const mockPushBranchForClose = vi.fn()
const mockRegisterFileTabPath = vi.fn(() => Promise.resolve({}))
const mockRevokeFileTabPath = vi.fn(() => Promise.resolve({}))
const mockCloseAgent = vi.fn(() => Promise.resolve({ result: undefined }))
const mockCloseTerminal = vi.fn(() => Promise.resolve({ result: undefined }))
const mockShowWarnToast = vi.fn()
const mockShowInfoToast = vi.fn()

vi.mock('~/api/workerRpc', () => ({
  inspectLastTabClose: (...args: unknown[]) => mockInspectLastTabClose(...args),
  pushBranchForClose: (...args: unknown[]) => mockPushBranchForClose(...args),
  registerFileTabPath: (...args: unknown[]) => mockRegisterFileTabPath(...args),
  revokeFileTabPath: (...args: unknown[]) => mockRevokeFileTabPath(...args),
  closeAgent: (...args: unknown[]) => mockCloseAgent(...args),
  closeTerminal: (...args: unknown[]) => mockCloseTerminal(...args),
}))

vi.mock('~/components/common/Toast', () => ({
  showInfoToast: (...args: unknown[]) => mockShowInfoToast(...args),
  showWarnToast: (...args: unknown[]) => mockShowWarnToast(...args),
}))

vi.mock('~/components/terminal/TerminalView', () => ({
  getTerminalInstance: vi.fn(() => undefined),
}))

function makeUserMessage(id: string, seq: bigint) {
  return {
    id,
    seq,
    role: 1,
    content: new TextEncoder().encode('{"content":"test"}'),
  } as Parameters<ReturnType<typeof createChatStore>['addMessage']>[1]
}

function setup() {
  // Override the global test bridge with a known tile id so the
  // projection's root leaf matches what this test wants to address
  // (tabStore.addTab pre-positions tabs onto `tileId`).
  installTestBridge({ rootTileId: 'tile-1' })
  const tabStore = createTabStore()
  const chatStore = createChatStore()
  const layoutStore = createLayoutStore()

  const tileId = 'tile-1'
  layoutStore.setFocusedTile(tileId)

  tabStore.addTab({ type: TabType.AGENT, id: 'agent-a', tileId, workerId: 'w-1' })
  tabStore.addTab({ type: TabType.AGENT, id: 'agent-b', tileId, workerId: 'w-1' }, { activate: false })
  tabStore.setActiveTabForTile(tileId, TabType.AGENT, 'agent-a')

  // handleCloseAgent / handleTerminalClose are now synchronous (void-returning)
  // fire-and-forget handlers. Use plain vi.fn() with no resolved value.
  const handleCloseAgent = vi.fn()
  const handleTerminalClose = vi.fn()

  const ops = useTabOperations({
    tabStore,
    chatStore,
    layoutStore,
    agentOps: {
      handleCloseAgent,
    } as never,
    termOps: {
      handleTerminalClose,
    } as never,
    activeTab: () => tabStore.activeTab() ?? undefined,
    getCurrentTabContext: () => ({ workerId: 'w-1', workingDir: '/tmp', homeDir: '/home/test' }),
    focusEditor: vi.fn(),
    getScrollState: () => ({ distFromBottom: 9999, atBottom: false }),
    setFileTreePath: vi.fn(),
    getOrgId: () => 'org-test',
    getActiveWorkspaceId: () => 'ws-test',
    registry: {
      findContaining: () => undefined,
      removeTab: () => {},
    } as never,
  })

  return { tabStore, chatStore, ops, tileId, handleCloseAgent, handleTerminalClose }
}

describe('useTabOperations', () => {
  afterEach(() => {
    mockInspectLastTabClose.mockReset()
    mockPushBranchForClose.mockReset()
    mockRegisterFileTabPath.mockReset()
    mockRevokeFileTabPath.mockReset()
    mockRegisterFileTabPath.mockImplementation(() => Promise.resolve({}))
    mockRevokeFileTabPath.mockImplementation(() => Promise.resolve({}))
    mockShowWarnToast.mockReset()
    mockShowInfoToast.mockReset()
  })

  describe('file-tab E2EE worker round-trip', () => {
    it('handleFileOpen calls RegisterFileTabPath with the local path', async () => {
      await createRoot(async (dispose) => {
        try {
          const { ops } = setup()
          ops.handleFileOpen('/tmp/myfile.go')
          // Allow the fire-and-forget E2EE call to dispatch.
          await Promise.resolve()
          expect(mockRegisterFileTabPath).toHaveBeenCalledTimes(1)
          const [workerId, req] = mockRegisterFileTabPath.mock.calls[0]
          expect(workerId).toBe('w-1')
          expect((req as { filePath: string }).filePath).toBe('/tmp/myfile.go')
          expect((req as { orgId: string }).orgId).toBe('org-test')
          expect((req as { workspaceId: string }).workspaceId).toBe('ws-test')
        }
        finally {
          dispose()
        }
      })
    })

    it('handleTabClose on a FILE tab calls RevokeFileTabPath', async () => {
      await createRoot(async (dispose) => {
        try {
          const { tabStore, ops } = setup()
          tabStore.addTab({
            type: TabType.FILE,
            id: 'file-1',
            filePath: '/tmp/myfile.go',
            workerId: 'w-1',
            tileId: 'tile-1',
          }, { activate: false })
          const tab = tabStore.state.tabs.find(t => t.id === 'file-1')!
          const ok = await ops.handleTabClose(tab)
          expect(ok).toBe(true)
          await Promise.resolve()
          expect(mockRevokeFileTabPath).toHaveBeenCalledTimes(1)
          const [workerId, req] = mockRevokeFileTabPath.mock.calls[0]
          expect(workerId).toBe('w-1')
          expect((req as { tabId: string }).tabId).toBe('file-1')
          expect((req as { orgId: string }).orgId).toBe('org-test')
        }
        finally {
          dispose()
        }
      })
    })

    it('handleFileOpen rolls back the optimistic tab on RegisterFileTabPath failure', async () => {
      await createRoot(async (dispose) => {
        try {
          mockRegisterFileTabPath.mockImplementationOnce(() => Promise.reject(new Error('e2ee failure')))
          const { tabStore, ops } = setup()
          ops.handleFileOpen('/tmp/myfile.go')
          // Tab added optimistically.
          expect(tabStore.state.tabs.some(t => t.type === TabType.FILE)).toBe(true)
          // Wait for the rejection microtask to fire the rollback.
          await new Promise(r => setTimeout(r, 0))
          expect(tabStore.state.tabs.some(t => t.type === TabType.FILE)).toBe(false)
        }
        finally {
          dispose()
        }
      })
    })
  })

  it('marks a tab as closing during decide phase, then clears once inspect resolves', async () => {
    await createRoot(async (dispose) => {
      try {
        const { tabStore, ops, handleCloseAgent } = setup()
        const tab = tabStore.state.tabs.find(t => t.id === 'agent-a')!
        const key = `${TabType.AGENT}:agent-a`

        let resolveInspect!: (value: { shouldPrompt: boolean }) => void
        mockInspectLastTabClose.mockImplementationOnce(() => new Promise((resolve) => {
          resolveInspect = resolve as typeof resolveInspect
        }))

        const closePromise = ops.handleTabClose(tab)
        expect(ops.closingTabKeys().has(key)).toBe(true)
        expect(handleCloseAgent).not.toHaveBeenCalled()

        resolveInspect({ shouldPrompt: false })
        await closePromise

        // Decide phase done: spinner cleared, commit phase already ran.
        expect(ops.closingTabKeys().has(key)).toBe(false)
        expect(handleCloseAgent).toHaveBeenCalledTimes(1)
      }
      finally {
        dispose()
      }
    })
  })

  it('no-prompt path: tab is removed from the store synchronously with KEEP action', async () => {
    await createRoot(async (dispose) => {
      try {
        const { tabStore, ops, handleCloseAgent } = setup()
        const tab = tabStore.state.tabs.find(t => t.id === 'agent-a')!
        mockInspectLastTabClose.mockResolvedValueOnce({ shouldPrompt: false })

        await ops.handleTabClose(tab)

        expect(handleCloseAgent).toHaveBeenCalledWith('agent-a', WorktreeAction.KEEP)
      }
      finally {
        dispose()
      }
    })
  })

  it('dialog cancel path: tab stays, handler not invoked, spinner cleared', async () => {
    await createRoot(async (dispose) => {
      try {
        const { tabStore, ops, handleCloseAgent, handleTerminalClose } = setup()
        const tab = tabStore.state.tabs.find(t => t.id === 'agent-a')!
        const key = `${TabType.AGENT}:agent-a`
        mockInspectLastTabClose.mockResolvedValueOnce({ shouldPrompt: true })

        // Simulate the dialog resolving cancel as soon as it's opened.
        const closePromise = ops.handleTabClose(tab)
        // Wait for the handler to reach setLastTabConfirm and open the
        // dialog; the signal holds the resolve fn.
        await Promise.resolve()
        await Promise.resolve()
        const dlg = ops.lastTabConfirm()
        expect(dlg).not.toBeNull()
        dlg!.resolve('cancel')
        await closePromise

        expect(tabStore.state.tabs.some(t => t.id === 'agent-a')).toBe(true)
        expect(handleCloseAgent).not.toHaveBeenCalled()
        expect(handleTerminalClose).not.toHaveBeenCalled()
        expect(ops.closingTabKeys().has(key)).toBe(false)
      }
      finally {
        dispose()
      }
    })
  })

  it('dialog close-anyway path: commit runs with KEEP', async () => {
    await createRoot(async (dispose) => {
      try {
        const { tabStore, ops, handleCloseAgent } = setup()
        const tab = tabStore.state.tabs.find(t => t.id === 'agent-a')!
        mockInspectLastTabClose.mockResolvedValueOnce({ shouldPrompt: true })

        const closePromise = ops.handleTabClose(tab)
        await Promise.resolve()
        await Promise.resolve()
        ops.lastTabConfirm()!.resolve('close-anyway')
        await closePromise

        expect(handleCloseAgent).toHaveBeenCalledWith('agent-a', WorktreeAction.KEEP)
      }
      finally {
        dispose()
      }
    })
  })

  it('dialog schedule-delete path: commit runs with REMOVE', async () => {
    await createRoot(async (dispose) => {
      try {
        const { tabStore, ops, handleCloseAgent } = setup()
        const tab = tabStore.state.tabs.find(t => t.id === 'agent-a')!
        mockInspectLastTabClose.mockResolvedValueOnce({ shouldPrompt: true })

        const closePromise = ops.handleTabClose(tab)
        await Promise.resolve()
        await Promise.resolve()
        ops.lastTabConfirm()!.resolve('schedule-delete')
        await closePromise

        expect(handleCloseAgent).toHaveBeenCalledWith('agent-a', WorktreeAction.REMOVE)
        expect(mockShowInfoToast).toHaveBeenCalledWith('Worktree will be removed')
      }
      finally {
        dispose()
      }
    })
  })

  it('inspect error: toast shown, handler not invoked, spinner cleared', async () => {
    await createRoot(async (dispose) => {
      try {
        const { tabStore, ops, handleCloseAgent } = setup()
        const agentTab = tabStore.state.tabs.find(t => t.id === 'agent-a')!
        const err = new Error('boom')
        mockInspectLastTabClose.mockRejectedValueOnce(err)

        await ops.handleTabClose(agentTab)

        expect(ops.closingTabKeys().has(`${TabType.AGENT}:agent-a`)).toBe(false)
        expect(mockShowWarnToast).toHaveBeenCalledWith('Failed to prepare tab close', err)
        expect(handleCloseAgent).not.toHaveBeenCalled()
      }
      finally {
        dispose()
      }
    })
  })

  it('rapid double-close dedupes during decide phase', async () => {
    await createRoot(async (dispose) => {
      try {
        const { tabStore, ops, handleCloseAgent } = setup()
        const tab = tabStore.state.tabs.find(t => t.id === 'agent-a')!

        let resolveInspect!: (value: { shouldPrompt: boolean }) => void
        mockInspectLastTabClose.mockImplementationOnce(() => new Promise((resolve) => {
          resolveInspect = resolve as typeof resolveInspect
        }))

        const p1 = ops.handleTabClose(tab)
        const p2 = ops.handleTabClose(tab)

        // Only one inspect should have been dispatched.
        expect(mockInspectLastTabClose).toHaveBeenCalledTimes(1)

        resolveInspect({ shouldPrompt: false })
        await Promise.all([p1, p2])

        expect(handleCloseAgent).toHaveBeenCalledTimes(1)
      }
      finally {
        dispose()
      }
    })
  })

  it('trims the previous agent when switching to another tab in the same tile', () => {
    createRoot((dispose) => {
      const { tabStore, chatStore, ops, tileId } = setup()
      const initial = Array.from({ length: MAX_BACKGROUND_CHAT_MESSAGES + 10 }, (_, i) =>
        makeUserMessage(`m${i + 1}`, BigInt(i + 1)))
      chatStore.setMessages('agent-a', initial)

      const nextTab = tabStore.state.tabs.find(t => t.id === 'agent-b')!
      ops.handleTabSelect(nextTab)
      tabStore.setActiveTabForTile(tileId, nextTab.type, nextTab.id)

      const trimmed = chatStore.getMessages('agent-a')
      expect(trimmed).toHaveLength(MAX_BACKGROUND_CHAT_MESSAGES)
      expect(trimmed[0].seq).toBe(11n)
      expect(trimmed.at(-1)?.seq).toBe(60n)
      expect(chatStore.hasOlderMessages('agent-a')).toBe(true)
      dispose()
    })
  })

  it('does not trim when switching focus to a tab in a different tile', () => {
    createRoot((dispose) => {
      const { tabStore, chatStore, ops } = setup()
      const initial = Array.from({ length: MAX_BACKGROUND_CHAT_MESSAGES + 10 }, (_, i) =>
        makeUserMessage(`m${i + 1}`, BigInt(i + 1)))
      chatStore.setMessages('agent-a', initial)

      tabStore.addTab({ type: TabType.AGENT, id: 'agent-c', tileId: 'tile-2' }, { activate: false })
      const nextTab = tabStore.state.tabs.find(t => t.id === 'agent-c')!
      ops.handleTabSelect(nextTab)
      tabStore.setActiveTabForTile('tile-2', nextTab.type, nextTab.id)

      const messages = chatStore.getMessages('agent-a')
      expect(messages).toHaveLength(MAX_BACKGROUND_CHAT_MESSAGES + 10)
      expect(messages[0].seq).toBe(1n)
      expect(messages.at(-1)?.seq).toBe(60n)
      dispose()
    })
  })
})

// Regression: sidebar middle-click / close on a tab in workspace B
// while the UI is on workspace A used to be a silent no-op locally:
// `tabStore.removeTab` filters by id but the tab isn't in the active
// store, so it just emits the CRDT TombstoneTab op; meanwhile
// `agentOps.handleCloseAgent` resolves `workerId` via the active
// `agentStore` which doesn't have the cross-workspace agent and so
// skips the worker close RPC. The agent kept running on the worker
// and the sidebar kept showing the row from the stale registry
// snapshot.
//
// The fixed path:
//   - `handleTabClose` detects the cross-workspace case via
//     `registry.findContaining` and takes a direct branch that:
//     * calls `workerRpc.closeAgent` / `workerRpc.closeTerminal`
//       with the tab's own `workerId` (always populated on
//       sidebar tabs);
//     * still emits the CRDT tombstone via `tabStore.removeTab`;
//     * calls `registry.removeTab(ownerWorkspaceId, tab)` so the
//       sidebar drops the row immediately.
describe('useTabOperations.handleTabClose cross-workspace', () => {
  afterEach(() => {
    mockInspectLastTabClose.mockReset()
    mockCloseAgent.mockReset()
    mockCloseTerminal.mockReset()
  })

  it('closes a non-active workspace agent via direct worker RPC + registry removeTab', async () => {
    await createRoot(async (dispose) => {
      installTestBridge({ rootTileId: 'tile-active' })
      const tabStore = createTabStore()
      const chatStore = createChatStore()
      const layoutStore = createLayoutStore()
      const handleCloseAgent = vi.fn()
      const handleTerminalClose = vi.fn()

      // The tab being closed lives in workspace B; the snapshot is
      // returned by the registry stub. The active stores (workspace A)
      // intentionally don't know about it.
      const crossWorkspaceTab = {
        type: TabType.AGENT,
        id: 'agent-cross',
        tileId: 'tile-cross',
        workerId: 'w-other',
      } as const
      const removedTabs: Array<{ wsId: string, tabId: string }> = []
      const registryStub = {
        findContaining: () => ({ workspaceId: 'ws-other', tabs: [crossWorkspaceTab] } as never),
        removeTab: (wsId: string, tab: { id: string }) => { removedTabs.push({ wsId, tabId: tab.id }) },
      } as never

      mockInspectLastTabClose.mockResolvedValueOnce({ shouldPrompt: false })

      const ops = useTabOperations({
        tabStore,
        chatStore,
        layoutStore,
        agentOps: { handleCloseAgent } as never,
        termOps: { handleTerminalClose } as never,
        activeTab: () => tabStore.activeTab() ?? undefined,
        getCurrentTabContext: () => ({ workerId: 'w-1', workingDir: '/tmp', homeDir: '/home/test' }),
        focusEditor: vi.fn(),
        getScrollState: () => ({ distFromBottom: 9999, atBottom: false }),
        setFileTreePath: vi.fn(),
        getOrgId: () => 'org-test',
        getActiveWorkspaceId: () => 'ws-active',
        registry: registryStub,
      })

      const result = await ops.handleTabClose(crossWorkspaceTab as never)
      expect(result).toBe(true)

      // Active-store handler is bypassed; direct worker RPC fires
      // with the tab's own workerId.
      expect(handleCloseAgent).not.toHaveBeenCalled()
      expect(mockCloseAgent).toHaveBeenCalledTimes(1)
      expect(mockCloseAgent.mock.calls[0][0]).toBe('w-other')
      expect(mockCloseAgent.mock.calls[0][1]).toMatchObject({
        agentId: 'agent-cross',
        worktreeAction: WorktreeAction.KEEP,
      })
      // Registry snapshot for ws-other gets the tab removed so the
      // sidebar can drop the row right away.
      expect(removedTabs).toEqual([{ wsId: 'ws-other', tabId: 'agent-cross' }])
      dispose()
    })
  })

  it('closes a non-active workspace terminal via direct worker RPC + registry removeTab', async () => {
    await createRoot(async (dispose) => {
      installTestBridge({ rootTileId: 'tile-active' })
      const tabStore = createTabStore()
      const chatStore = createChatStore()
      const layoutStore = createLayoutStore()
      const handleCloseAgent = vi.fn()
      const handleTerminalClose = vi.fn()

      const crossTab = {
        type: TabType.TERMINAL,
        id: 'term-cross',
        tileId: 'tile-cross',
        workerId: 'w-other',
      } as const
      const removedTabs: Array<{ wsId: string, tabId: string }> = []
      const registryStub = {
        findContaining: () => ({ workspaceId: 'ws-other', tabs: [crossTab] } as never),
        removeTab: (wsId: string, tab: { id: string }) => { removedTabs.push({ wsId, tabId: tab.id }) },
      } as never

      mockInspectLastTabClose.mockResolvedValueOnce({ shouldPrompt: false })

      const ops = useTabOperations({
        tabStore,
        chatStore,
        layoutStore,
        agentOps: { handleCloseAgent } as never,
        termOps: { handleTerminalClose } as never,
        activeTab: () => tabStore.activeTab() ?? undefined,
        getCurrentTabContext: () => ({ workerId: 'w-1', workingDir: '/tmp', homeDir: '/home/test' }),
        focusEditor: vi.fn(),
        getScrollState: () => ({ distFromBottom: 9999, atBottom: false }),
        setFileTreePath: vi.fn(),
        getOrgId: () => 'org-test',
        getActiveWorkspaceId: () => 'ws-active',
        registry: registryStub,
      })

      const result = await ops.handleTabClose(crossTab as never)
      expect(result).toBe(true)
      expect(handleTerminalClose).not.toHaveBeenCalled()
      expect(mockCloseTerminal).toHaveBeenCalledTimes(1)
      expect(mockCloseTerminal.mock.calls[0][0]).toBe('w-other')
      expect(mockCloseTerminal.mock.calls[0][1]).toMatchObject({
        terminalId: 'term-cross',
        workspaceId: 'ws-other',
        worktreeAction: WorktreeAction.KEEP,
      })
      expect(removedTabs).toEqual([{ wsId: 'ws-other', tabId: 'term-cross' }])
      dispose()
    })
  })
})

// Regression: closing the last tab on the focused tile used to
// leave focus on the now-empty tile, so the user saw the empty-tile
// placeholder while the surviving work lived on another tile.
// `migrateFocusAfterTabClose` follows the MRU-promoted active tab.
describe('useTabOperations.handleTabClose focus migration', () => {
  afterEach(() => {
    mockInspectLastTabClose.mockReset()
  })

  it('moves focusedTileId to the surviving active tab\'s tile when the focused tile empties', async () => {
    await createRoot(async (dispose) => {
      installTestBridge({ rootTileId: 'root-leaf' })
      const tabStore = createTabStore()
      const chatStore = createChatStore()
      const layoutStore = createLayoutStore()

      // Real split so both tile ids exist in the projected tree.
      // `containsTileId` matches LEAF nodes only — the layout store's
      // focus invariant effect resets focus to firstLeaf when the
      // focused tile id isn't a live leaf. Use the actual leaf ids
      // produced by `splitTile`, not the pre-split root id.
      const otherTileId = layoutStore.splitTile('root-leaf', 'horizontal')!
      const [tileA, tileB] = layoutStore.getAllTileIds()
      // The split keeps both children as leaves; we just need to know
      // which one is the new childB so we can target the other.
      const focusTile = tileB === otherTileId ? tileA : tileB
      const otherLeafTile = otherTileId

      tabStore.addTab({ type: TabType.FILE, id: 'file-a', tileId: focusTile, workerId: 'w-1', filePath: '/a' })
      tabStore.addTab({ type: TabType.FILE, id: 'file-b', tileId: otherLeafTile, workerId: 'w-1', filePath: '/b' }, { activate: false })
      layoutStore.setFocusedTile(focusTile)

      const ops = useTabOperations({
        tabStore,
        chatStore,
        layoutStore,
        agentOps: { handleCloseAgent: vi.fn() } as never,
        termOps: { handleTerminalClose: vi.fn() } as never,
        activeTab: () => tabStore.activeTab() ?? undefined,
        getCurrentTabContext: () => ({ workerId: 'w-1', workingDir: '/tmp', homeDir: '/home/test' }),
        focusEditor: vi.fn(),
        getScrollState: () => ({ distFromBottom: 9999, atBottom: false }),
        setFileTreePath: vi.fn(),
        getOrgId: () => 'org-test',
        getActiveWorkspaceId: () => 'ws-test',
        registry: {
          findContaining: () => undefined,
          removeTab: () => {},
        } as never,
      })

      await ops.handleTabClose({ type: TabType.FILE, id: 'file-a', tileId: focusTile, workerId: 'w-1', filePath: '/a' } as never)

      expect(layoutStore.focusedTileId()).toBe(otherLeafTile)
      dispose()
    })
  })

  it('leaves focus alone when other tabs remain on the focused tile', async () => {
    await createRoot(async (dispose) => {
      installTestBridge({ rootTileId: 'root-leaf' })
      const tabStore = createTabStore()
      const chatStore = createChatStore()
      const layoutStore = createLayoutStore()

      const otherTileId = layoutStore.splitTile('root-leaf', 'horizontal')!
      const [tileA, tileB] = layoutStore.getAllTileIds()
      const focusTile = tileB === otherTileId ? tileA : tileB
      const otherLeafTile = otherTileId

      tabStore.addTab({ type: TabType.FILE, id: 'file-a', tileId: focusTile, workerId: 'w-1', filePath: '/a' })
      tabStore.addTab({ type: TabType.FILE, id: 'file-b', tileId: focusTile, workerId: 'w-1', filePath: '/b' }, { activate: false })
      tabStore.addTab({ type: TabType.FILE, id: 'file-c', tileId: otherLeafTile, workerId: 'w-1', filePath: '/c' }, { activate: false })
      layoutStore.setFocusedTile(focusTile)

      const ops = useTabOperations({
        tabStore,
        chatStore,
        layoutStore,
        agentOps: { handleCloseAgent: vi.fn() } as never,
        termOps: { handleTerminalClose: vi.fn() } as never,
        activeTab: () => tabStore.activeTab() ?? undefined,
        getCurrentTabContext: () => ({ workerId: 'w-1', workingDir: '/tmp', homeDir: '/home/test' }),
        focusEditor: vi.fn(),
        getScrollState: () => ({ distFromBottom: 9999, atBottom: false }),
        setFileTreePath: vi.fn(),
        getOrgId: () => 'org-test',
        getActiveWorkspaceId: () => 'ws-test',
        registry: {
          findContaining: () => undefined,
          removeTab: () => {},
        } as never,
      })

      await ops.handleTabClose({ type: TabType.FILE, id: 'file-a', tileId: focusTile, workerId: 'w-1', filePath: '/a' } as never)

      // focusTile still has file-b, focus stays.
      expect(layoutStore.focusedTileId()).toBe(focusTile)
      dispose()
    })
  })
})

// --- Floating-window auto-cleanup ---
//
// We use FILE tabs to drive these tests because handleTabClose removes
// FILE tabs synchronously via tabStore.removeTab, so the
// removeIfEmpty cleanup runs against real tab state. AGENT/TERMINAL
// closes go through agentOps/termOps mocks that don't update tabStore.

function setupWithFloatingWindow() {
  // The default test bridge already seeds 'main-tile' as the root.
  // We rely on that to keep `mainTileId` stable across this test.
  const tabStore = createTabStore()
  const chatStore = createChatStore()
  const layoutStore = createLayoutStore()
  const floatingWindowStore = createFloatingWindowStore()

  const mainTileId = 'main-tile'
  layoutStore.setFocusedTile(mainTileId)

  const handleCloseAgent = vi.fn()
  const handleTerminalClose = vi.fn()

  const ops = useTabOperations({
    tabStore,
    chatStore,
    layoutStore,
    floatingWindowStore,
    agentOps: { handleCloseAgent } as never,
    termOps: { handleTerminalClose } as never,
    activeTab: () => tabStore.activeTab() ?? undefined,
    getCurrentTabContext: () => ({ workerId: 'w-1', workingDir: '/tmp', homeDir: '/home/test' }),
    focusEditor: vi.fn(),
    getScrollState: () => ({ distFromBottom: 9999, atBottom: false }),
    setFileTreePath: vi.fn(),
    getOrgId: () => 'org-test',
    getActiveWorkspaceId: () => 'ws-test',
    registry: {
      findContaining: () => undefined,
      removeTab: () => {},
    } as never,
  })

  return { tabStore, layoutStore, floatingWindowStore, ops, mainTileId }
}

describe('useTabOperations.handleTabClose floating-window cleanup', () => {
  it('closing the last FILE tab in a single-tile floating window auto-removes the window', async () => {
    await createRoot(async (dispose) => {
      try {
        const { tabStore, floatingWindowStore, ops } = setupWithFloatingWindow()
        const { windowId, tileId } = floatingWindowStore.addWindow()
        tabStore.addTab({ type: TabType.FILE, id: 'f1', tileId, filePath: '/a.txt', workerId: 'w-1' })

        expect(floatingWindowStore.state.windows).toHaveLength(1)
        const tab = tabStore.state.tabs.find(t => t.id === 'f1')!

        const ok = await ops.handleTabClose(tab)
        expect(ok).toBe(true)
        // Auto-cleanup removed the now-empty floating window.
        expect(floatingWindowStore.state.windows).toHaveLength(0)
        expect(floatingWindowStore.getWindow(windowId)).toBeNull()
      }
      finally {
        dispose()
      }
    })
  })

  it('closing a FILE tab when sibling tiles in the same window still have tabs leaves the window intact', async () => {
    await createRoot(async (dispose) => {
      try {
        const { tabStore, floatingWindowStore, ops } = setupWithFloatingWindow()
        const { windowId, tileId } = floatingWindowStore.addWindow()
        const newTileId = floatingWindowStore.splitTile(windowId, tileId, 'horizontal')!
        tabStore.addTab({ type: TabType.FILE, id: 'f1', tileId, filePath: '/a.txt', workerId: 'w-1' })
        tabStore.addTab({ type: TabType.FILE, id: 'f2', tileId: newTileId, filePath: '/b.txt', workerId: 'w-1' })

        const tab = tabStore.state.tabs.find(t => t.id === 'f1')!
        const ok = await ops.handleTabClose(tab)
        expect(ok).toBe(true)
        // Sibling tile still has a tab → window stays.
        expect(floatingWindowStore.state.windows).toHaveLength(1)
        expect(floatingWindowStore.getWindow(windowId)).toBeDefined()
      }
      finally {
        dispose()
      }
    })
  })

  it('closing a FILE tab in the main layout never touches floating-window state', async () => {
    await createRoot(async (dispose) => {
      try {
        const { tabStore, floatingWindowStore, ops, mainTileId } = setupWithFloatingWindow()
        // Add an unrelated empty floating window — it should NOT be touched
        // by closes against the main layout.
        floatingWindowStore.addWindow()
        const startWindowCount = floatingWindowStore.state.windows.length
        tabStore.addTab({ type: TabType.FILE, id: 'main-file', tileId: mainTileId, filePath: '/a.txt', workerId: 'w-1' })

        const tab = tabStore.state.tabs.find(t => t.id === 'main-file')!
        const ok = await ops.handleTabClose(tab)
        expect(ok).toBe(true)
        expect(floatingWindowStore.state.windows).toHaveLength(startWindowCount)
      }
      finally {
        dispose()
      }
    })
  })
})
