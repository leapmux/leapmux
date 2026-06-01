import { createRoot } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useTabOperations } from '~/components/shell/useTabOperations'
import { WorktreeAction, WorktreeRemovalOutcome } from '~/generated/leapmux/v1/common_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createChatStore, MAX_BACKGROUND_CHAT_MESSAGES } from '~/stores/chat.store'
import { createFloatingWindowStore } from '~/stores/floatingWindow.store'
import { createLayoutStore } from '~/stores/layout.store'
import { createTabStore } from '~/stores/tab.store'
import { flush } from '../helpers/async'
import { installTestBridge } from '../helpers/crdtBridge'

// Default: every FILE / AGENT / TERMINAL close path now routes through
// inspectLastTabClose, so a vi.fn() with no implementation would resolve
// to `undefined` and trip the `status.shouldPrompt` access. Default to
// the no-prompt happy path; per-test overrides use mockResolvedValueOnce.
const mockInspectLastTabClose = vi.fn(() => Promise.resolve({ shouldPrompt: false } as unknown))
const mockPushBranch = vi.fn()
const mockRegisterFileTabPath = vi.fn(() => Promise.resolve({}))
const mockRevokeFileTabPath = vi.fn(() => Promise.resolve({}))
const mockCloseAgent = vi.fn(() => Promise.resolve({ result: undefined }))
const mockCloseTerminal = vi.fn(() => Promise.resolve({ result: undefined }))
const mockShowWarnToast = vi.fn()
const mockShowInfoToast = vi.fn()

vi.mock('~/api/workerRpc', () => ({
  inspectLastTabClose: (...args: unknown[]) => mockInspectLastTabClose(...args),
  pushBranch: (...args: unknown[]) => mockPushBranch(...args),
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

  // handleAgentClose / handleTerminalClose return Promise<CloseTabResult |
  // undefined>, but these tests only assert how they're invoked (call args),
  // not the resolved outcome, so a plain vi.fn() (resolving undefined) is enough.
  const handleAgentClose = vi.fn()
  const handleTerminalClose = vi.fn()

  const ops = useTabOperations({
    tabStore,
    chatStore,
    layoutStore,
    agentOps: {
      handleAgentClose,
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

  return { tabStore, chatStore, ops, tileId, handleAgentClose, handleTerminalClose }
}

describe('useTabOperations', () => {
  afterEach(() => {
    mockInspectLastTabClose.mockReset()
    mockInspectLastTabClose.mockImplementation(() => Promise.resolve({ shouldPrompt: false } as unknown))
    mockPushBranch.mockReset()
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

    it('handleTabClose on a FILE tab inspects then calls RevokeFileTabPath with KEEP', async () => {
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
          // Worker reports the FILE close has siblings (or is not in a
          // worktree), so shouldPrompt=false and we commit straight to
          // KEEP — same shape as the AGENT / TERMINAL no-prompt path.
          mockInspectLastTabClose.mockResolvedValueOnce({ shouldPrompt: false })

          const ok = await ops.handleTabClose(tab)
          expect(ok).toBe(true)
          await Promise.resolve()
          expect(mockInspectLastTabClose).toHaveBeenCalledTimes(1)
          expect(mockRevokeFileTabPath).toHaveBeenCalledTimes(1)
          const [workerId, req] = mockRevokeFileTabPath.mock.calls[0]
          expect(workerId).toBe('w-1')
          expect((req as { tabId: string }).tabId).toBe('file-1')
          expect((req as { orgId: string }).orgId).toBe('org-test')
          expect((req as { worktreeAction: WorktreeAction }).worktreeAction).toBe(WorktreeAction.KEEP)
        }
        finally {
          dispose()
        }
      })
    })

    it('handleTabClose on a FILE tab opens the last-tab dialog when shouldPrompt=true', async () => {
      await createRoot(async (dispose) => {
        try {
          const { tabStore, ops } = setup()
          tabStore.addTab({
            type: TabType.FILE,
            id: 'file-last',
            filePath: '/tmp/myfile.go',
            workerId: 'w-1',
            tileId: 'tile-1',
          }, { activate: false })
          const tab = tabStore.state.tabs.find(t => t.id === 'file-last')!
          mockInspectLastTabClose.mockResolvedValueOnce({ shouldPrompt: true })

          // Cancel the dialog. The FILE tab must remain and no worker
          // revoke must fire — regression guard for the original bug
          // where FILE closes skipped the inspect+confirm entirely and
          // there was nothing to cancel.
          const closePromise = ops.handleTabClose(tab)
          await flush()
          const dlg = ops.lastTabConfirmDialog.value()
          expect(dlg).not.toBeNull()
          dlg!.resolve('cancel')
          const ok = await closePromise
          expect(ok).toBe(false)
          expect(tabStore.state.tabs.some(t => t.id === 'file-last')).toBe(true)
          expect(mockRevokeFileTabPath).not.toHaveBeenCalled()
        }
        finally {
          dispose()
        }
      })
    })

    it('handleTabClose FILE tab close-anyway forwards WorktreeAction.KEEP to RevokeFileTabPath', async () => {
      // Counterpart to the schedule-delete test below: the user chose
      // to close the last FILE tab but keep the worktree on disk.
      // RevokeFileTabPath must still fire (the row + worktree link
      // need to come down) with KEEP so the worker side leaves the
      // worktree alone — same shape as the AGENT/TERMINAL close-anyway
      // path.
      await createRoot(async (dispose) => {
        try {
          const { tabStore, ops } = setup()
          tabStore.addTab({
            type: TabType.FILE,
            id: 'file-keep',
            filePath: '/tmp/myfile.go',
            workerId: 'w-1',
            tileId: 'tile-1',
          }, { activate: false })
          const tab = tabStore.state.tabs.find(t => t.id === 'file-keep')!
          mockInspectLastTabClose.mockResolvedValueOnce({ shouldPrompt: true })

          const closePromise = ops.handleTabClose(tab)
          await flush()
          ops.lastTabConfirmDialog.value()!.resolve('close-anyway')
          const ok = await closePromise
          expect(ok).toBe(true)
          await Promise.resolve()
          expect(mockRevokeFileTabPath).toHaveBeenCalledTimes(1)
          const [, req] = mockRevokeFileTabPath.mock.calls[0]
          expect((req as { worktreeAction: WorktreeAction }).worktreeAction).toBe(WorktreeAction.KEEP)
          expect(mockShowInfoToast).not.toHaveBeenCalledWith('Worktree will be removed')
        }
        finally {
          dispose()
        }
      })
    })

    it('handleTabClose FILE tab schedule-delete forwards WorktreeAction.REMOVE to RevokeFileTabPath', async () => {
      await createRoot(async (dispose) => {
        try {
          const { tabStore, ops } = setup()
          tabStore.addTab({
            type: TabType.FILE,
            id: 'file-delete',
            filePath: '/tmp/myfile.go',
            workerId: 'w-1',
            tileId: 'tile-1',
          }, { activate: false })
          const tab = tabStore.state.tabs.find(t => t.id === 'file-delete')!
          mockInspectLastTabClose.mockResolvedValueOnce({ shouldPrompt: true })

          const closePromise = ops.handleTabClose(tab)
          await flush()
          ops.lastTabConfirmDialog.value()!.resolve('schedule-delete')
          const ok = await closePromise
          expect(ok).toBe(true)
          await Promise.resolve()
          expect(mockRevokeFileTabPath).toHaveBeenCalledTimes(1)
          const [, req] = mockRevokeFileTabPath.mock.calls[0]
          expect((req as { worktreeAction: WorktreeAction }).worktreeAction).toBe(WorktreeAction.REMOVE)
          expect(mockShowInfoToast).toHaveBeenCalledWith('Worktree will be removed')
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
        const { tabStore, ops, handleAgentClose } = setup()
        const tab = tabStore.state.tabs.find(t => t.id === 'agent-a')!
        const key = `${TabType.AGENT}:agent-a`

        let resolveInspect!: (value: { shouldPrompt: boolean }) => void
        mockInspectLastTabClose.mockImplementationOnce(() => new Promise((resolve) => {
          resolveInspect = resolve as typeof resolveInspect
        }))

        const closePromise = ops.handleTabClose(tab)
        expect(ops.closingTabKeys().has(key)).toBe(true)
        expect(handleAgentClose).not.toHaveBeenCalled()

        resolveInspect({ shouldPrompt: false })
        await closePromise

        // Decide phase done: spinner cleared, commit phase already ran.
        expect(ops.closingTabKeys().has(key)).toBe(false)
        expect(handleAgentClose).toHaveBeenCalledTimes(1)
      }
      finally {
        dispose()
      }
    })
  })

  it('no-prompt path: tab is removed from the store synchronously with KEEP action', async () => {
    await createRoot(async (dispose) => {
      try {
        const { tabStore, ops, handleAgentClose } = setup()
        const tab = tabStore.state.tabs.find(t => t.id === 'agent-a')!
        mockInspectLastTabClose.mockResolvedValueOnce({ shouldPrompt: false })

        await ops.handleTabClose(tab)

        expect(handleAgentClose).toHaveBeenCalledWith('agent-a', WorktreeAction.KEEP)
      }
      finally {
        dispose()
      }
    })
  })

  it('dialog cancel path: tab stays, handler not invoked, spinner cleared', async () => {
    await createRoot(async (dispose) => {
      try {
        const { tabStore, ops, handleAgentClose, handleTerminalClose } = setup()
        const tab = tabStore.state.tabs.find(t => t.id === 'agent-a')!
        const key = `${TabType.AGENT}:agent-a`
        mockInspectLastTabClose.mockResolvedValueOnce({ shouldPrompt: true })

        // Simulate the dialog resolving cancel as soon as it's opened.
        const closePromise = ops.handleTabClose(tab)
        // Wait for the handler to open the dialog; the dialog handle's
        // value() holds the resolve fn.
        await flush()
        const dlg = ops.lastTabConfirmDialog.value()
        expect(dlg).not.toBeNull()
        dlg!.resolve('cancel')
        await closePromise

        expect(tabStore.state.tabs.some(t => t.id === 'agent-a')).toBe(true)
        expect(handleAgentClose).not.toHaveBeenCalled()
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
        const { tabStore, ops, handleAgentClose } = setup()
        const tab = tabStore.state.tabs.find(t => t.id === 'agent-a')!
        mockInspectLastTabClose.mockResolvedValueOnce({ shouldPrompt: true })

        const closePromise = ops.handleTabClose(tab)
        await flush()
        ops.lastTabConfirmDialog.value()!.resolve('close-anyway')
        await closePromise

        expect(handleAgentClose).toHaveBeenCalledWith('agent-a', WorktreeAction.KEEP)
      }
      finally {
        dispose()
      }
    })
  })

  it('dialog schedule-delete path: commit runs with REMOVE', async () => {
    await createRoot(async (dispose) => {
      try {
        const { tabStore, ops, handleAgentClose } = setup()
        const tab = tabStore.state.tabs.find(t => t.id === 'agent-a')!
        mockInspectLastTabClose.mockResolvedValueOnce({ shouldPrompt: true })

        const closePromise = ops.handleTabClose(tab)
        await flush()
        ops.lastTabConfirmDialog.value()!.resolve('schedule-delete')
        await closePromise

        expect(handleAgentClose).toHaveBeenCalledWith('agent-a', WorktreeAction.REMOVE)
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
        const { tabStore, ops, handleAgentClose } = setup()
        const agentTab = tabStore.state.tabs.find(t => t.id === 'agent-a')!
        const err = new Error('boom')
        mockInspectLastTabClose.mockRejectedValueOnce(err)

        await ops.handleTabClose(agentTab)

        expect(ops.closingTabKeys().has(`${TabType.AGENT}:agent-a`)).toBe(false)
        expect(mockShowWarnToast).toHaveBeenCalledWith('Failed to prepare tab close', err)
        expect(handleAgentClose).not.toHaveBeenCalled()
      }
      finally {
        dispose()
      }
    })
  })

  it('rapid double-close dedupes during decide phase', async () => {
    await createRoot(async (dispose) => {
      try {
        const { tabStore, ops, handleAgentClose } = setup()
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

        expect(handleAgentClose).toHaveBeenCalledTimes(1)
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
// `agentOps.handleAgentClose` resolves `workerId` via the active
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
    mockInspectLastTabClose.mockImplementation(() => Promise.resolve({ shouldPrompt: false } as unknown))
    mockCloseAgent.mockReset()
    mockCloseTerminal.mockReset()
    mockRevokeFileTabPath.mockReset()
    mockRevokeFileTabPath.mockImplementation(() => Promise.resolve({}))
  })

  it('closes a non-active workspace agent via direct worker RPC + registry removeTab', async () => {
    await createRoot(async (dispose) => {
      installTestBridge({ rootTileId: 'tile-active' })
      const tabStore = createTabStore()
      const chatStore = createChatStore()
      const layoutStore = createLayoutStore()
      const handleAgentClose = vi.fn()
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
        agentOps: { handleAgentClose } as never,
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
      expect(handleAgentClose).not.toHaveBeenCalled()
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
      const handleAgentClose = vi.fn()
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
        agentOps: { handleAgentClose } as never,
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

  it('closes a non-active workspace FILE tab via revokeFileTabPath + registry removeTab', async () => {
    await createRoot(async (dispose) => {
      installTestBridge({ rootTileId: 'tile-active' })
      const tabStore = createTabStore()
      const chatStore = createChatStore()
      const layoutStore = createLayoutStore()
      const handleAgentClose = vi.fn()
      const handleTerminalClose = vi.fn()

      const crossFileTab = {
        type: TabType.FILE,
        id: 'file-cross',
        tileId: 'tile-cross',
        workerId: 'w-other',
        filePath: '/repo/x.md',
      } as const
      const removedTabs: Array<{ wsId: string, tabId: string }> = []
      const registryStub = {
        findContaining: () => ({ workspaceId: 'ws-other', tabs: [crossFileTab] } as never),
        removeTab: (wsId: string, tab: { id: string }) => { removedTabs.push({ wsId, tabId: tab.id }) },
      } as never

      mockInspectLastTabClose.mockResolvedValueOnce({ shouldPrompt: false })

      const ops = useTabOperations({
        tabStore,
        chatStore,
        layoutStore,
        agentOps: { handleAgentClose } as never,
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

      const result = await ops.handleTabClose(crossFileTab as never)
      expect(result).toBe(true)
      // FILE follows the same cross-workspace contract as AGENT /
      // TERMINAL: bypass the active-store handlers, fire the worker
      // RPC directly with the tab's own workerId.
      expect(handleAgentClose).not.toHaveBeenCalled()
      expect(handleTerminalClose).not.toHaveBeenCalled()
      expect(mockRevokeFileTabPath).toHaveBeenCalledTimes(1)
      expect(mockRevokeFileTabPath.mock.calls[0][0]).toBe('w-other')
      expect(mockRevokeFileTabPath.mock.calls[0][1]).toMatchObject({
        tabId: 'file-cross',
        orgId: 'org-test',
        worktreeAction: WorktreeAction.KEEP,
      })
      expect(removedTabs).toEqual([{ wsId: 'ws-other', tabId: 'file-cross' }])
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
    mockInspectLastTabClose.mockImplementation(() => Promise.resolve({ shouldPrompt: false } as unknown))
    mockRevokeFileTabPath.mockReset()
    mockRevokeFileTabPath.mockImplementation(() => Promise.resolve({}))
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
        agentOps: { handleAgentClose: vi.fn() } as never,
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
        agentOps: { handleAgentClose: vi.fn() } as never,
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

  const handleAgentClose = vi.fn()
  const handleTerminalClose = vi.fn()

  const ops = useTabOperations({
    tabStore,
    chatStore,
    layoutStore,
    floatingWindowStore,
    agentOps: { handleAgentClose } as never,
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
  afterEach(() => {
    mockInspectLastTabClose.mockReset()
    mockInspectLastTabClose.mockImplementation(() => Promise.resolve({ shouldPrompt: false } as unknown))
    mockRevokeFileTabPath.mockReset()
    mockRevokeFileTabPath.mockImplementation(() => Promise.resolve({}))
  })

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

// closeTabWithAction is the dialog-driven companion to handleTabClose:
// the user has already decided the worktree fate for an entire branch
// group, so per-tab last-tab inspection / confirmation prompts must NOT
// fire. The helper still runs the focus migration + empty-floating-
// window prune that an ad-hoc inline switch would skip.
describe('useTabOperations.closeTabWithAction', () => {
  afterEach(() => {
    mockInspectLastTabClose.mockReset()
    mockInspectLastTabClose.mockImplementation(() => Promise.resolve({ shouldPrompt: false } as unknown))
    mockCloseAgent.mockReset()
    mockCloseTerminal.mockReset()
    mockRevokeFileTabPath.mockReset()
    mockRevokeFileTabPath.mockImplementation(() => Promise.resolve({}))
    mockCloseAgent.mockImplementation(() => Promise.resolve({ result: undefined }))
    mockCloseTerminal.mockImplementation(() => Promise.resolve({ result: undefined }))
  })

  it('dispatches AGENT tab to agentOps.handleAgentClose with the supplied action', () => {
    createRoot((dispose) => {
      const { tabStore, ops, handleAgentClose, handleTerminalClose } = setup()
      const agent = tabStore.state.tabs.find(t => t.id === 'agent-a')!

      ops.closeTabWithAction(agent, WorktreeAction.REMOVE)

      expect(handleAgentClose).toHaveBeenCalledTimes(1)
      expect(handleAgentClose).toHaveBeenCalledWith('agent-a', WorktreeAction.REMOVE)
      expect(handleTerminalClose).not.toHaveBeenCalled()
      // Never goes through the inspect path — the dialog already chose
      // the worktree action.
      expect(mockInspectLastTabClose).not.toHaveBeenCalled()
      dispose()
    })
  })

  it('does NOT add the closed tab to closingTabKeys (handleTabClose owns that signal)', () => {
    // closeTabWithAction is invoked from handleTabClose's commit phase
    // AFTER `finally { removeClosingTabKey(key) }` has fired. Re-adding
    // the key here would leak it for the entire close lifetime and
    // break the existing "spinner clears once inspect resolves"
    // contract. The dialog-driven flow leans on the worker's
    // idempotent close-agent / close-terminal handlers for dedup
    // instead of a client-side guard.
    createRoot((dispose) => {
      const { tabStore, ops, handleAgentClose } = setup()
      const agent = tabStore.state.tabs.find(t => t.id === 'agent-a')!

      ops.closeTabWithAction(agent, WorktreeAction.REMOVE)

      expect(handleAgentClose).toHaveBeenCalledTimes(1)
      expect(ops.closingTabKeys().has(`${TabType.AGENT}:agent-a`)).toBe(false)
      dispose()
    })
  })

  it('dispatches TERMINAL tab to termOps.handleTerminalClose with the supplied action', () => {
    createRoot((dispose) => {
      const { tabStore, ops, handleAgentClose, handleTerminalClose } = setup()
      tabStore.addTab({ type: TabType.TERMINAL, id: 'term-1', tileId: 'tile-1', workerId: 'w-1' }, { activate: false })
      const term = tabStore.state.tabs.find(t => t.id === 'term-1')!

      ops.closeTabWithAction(term, WorktreeAction.KEEP)

      expect(handleTerminalClose).toHaveBeenCalledTimes(1)
      expect(handleTerminalClose).toHaveBeenCalledWith('term-1', WorktreeAction.KEEP)
      expect(handleAgentClose).not.toHaveBeenCalled()
      dispose()
    })
  })

  it('removes FILE tabs locally (worktree-delete loop hands every tab in the group here)', () => {
    // The DeleteBranchDialog worktree-variant iterates every tab in the
    // branch group and calls closeTabWithAction(REMOVE). Leaving FILE
    // tabs in place would orphan them at paths the worker is about to
    // delete — opening them again would 404. The helper now mirrors
    // handleTabClose's FILE branch: remove the tab locally and let the
    // CRDT tombstone fan out. The worktreeAction is meaningless for
    // FILE tabs since they don't pin the worktree on the worker.
    createRoot((dispose) => {
      const { tabStore, ops, handleAgentClose, handleTerminalClose } = setup()
      tabStore.addTab({
        type: TabType.FILE,
        id: 'f1',
        tileId: 'tile-1',
        filePath: '/x',
        workerId: 'w-1',
      }, { activate: false })
      const file = tabStore.state.tabs.find(t => t.id === 'f1')!

      ops.closeTabWithAction(file, WorktreeAction.REMOVE)

      expect(handleAgentClose).not.toHaveBeenCalled()
      expect(handleTerminalClose).not.toHaveBeenCalled()
      expect(tabStore.state.tabs.some(t => t.id === 'f1')).toBe(false)
      dispose()
    })
  })

  it('removes the parent floating window when closing its last tab', async () => {
    await createRoot(async (dispose) => {
      try {
        const { tabStore, floatingWindowStore, ops } = setupWithFloatingWindow()
        const { windowId, tileId } = floatingWindowStore.addWindow()
        // Add an AGENT tab to the floating tile; the floating window
        // now contains exactly one tab in one tile.
        tabStore.addTab({ type: TabType.AGENT, id: 'agent-float', tileId, workerId: 'w-1' })

        // Simulate the worker close having already torn the tab off the
        // store (agentOps.handleAgentClose is mocked here, so we must
        // remove it ourselves to model the post-close state).
        const tab = tabStore.state.tabs.find(t => t.id === 'agent-float')!
        tabStore.removeTab(TabType.AGENT, 'agent-float')

        ops.closeTabWithAction(tab, WorktreeAction.REMOVE)

        expect(floatingWindowStore.state.windows).toHaveLength(0)
        expect(floatingWindowStore.getWindow(windowId)).toBeNull()
      }
      finally {
        dispose()
      }
    })
  })

  it('migrates focus to the surviving active tab\'s tile when the closed tab\'s tile is now empty', () => {
    createRoot((dispose) => {
      const { tabStore, ops, layoutStore } = setupForFocusMigration()
      // Tile-1 has agent-a (active). Tile-other has agent-other.
      // Focus starts on tile-1; after closing agent-a (and the store
      // remove that the worker close would have done), tile-1 is empty
      // and focus must follow the active tab to tile-other.
      tabStore.setActiveTabForTile('tile-other', TabType.AGENT, 'agent-other')
      tabStore.removeTab(TabType.AGENT, 'agent-a')
      layoutStore.setFocusedTile('tile-1')

      const closed = { type: TabType.AGENT, id: 'agent-a', tileId: 'tile-1', workerId: 'w-1' } as never
      ops.closeTabWithAction(closed, WorktreeAction.REMOVE)

      expect(layoutStore.focusedTileId()).toBe('tile-other')
      dispose()
    })
  })

  it('closeWorktreeTabs folds a per-tab REMOVED outcome into removed=true', async () => {
    await createRoot(async (dispose) => {
      const { tabStore, ops, handleAgentClose } = setup()
      handleAgentClose.mockResolvedValue({ worktreeRemoval: WorktreeRemovalOutcome.REMOVED })
      const agent = tabStore.state.tabs.find(t => t.id === 'agent-a')!

      const summary = await ops.closeWorktreeTabs([agent])

      expect(summary).toEqual({ removed: true, failed: false, stillReferenced: false, unknown: false })
      dispose()
    })
  })

  it('closeWorktreeTabs folds a tab with no definitive result (rejected RPC / unreachable / threw) into unknown=true', async () => {
    // awaitCloseResult resolves undefined when the close RPC rejects; the
    // fold must treat that as "outcome unknown" — NOT a clean no-op — so the
    // dialog can say it couldn't confirm removal rather than "not removed".
    await createRoot(async (dispose) => {
      const { tabStore, ops, handleAgentClose } = setup()
      handleAgentClose.mockResolvedValue(undefined)
      const agent = tabStore.state.tabs.find(t => t.id === 'agent-a')!

      const summary = await ops.closeWorktreeTabs([agent])

      expect(summary).toEqual({ removed: false, failed: false, stillReferenced: false, unknown: true })
      dispose()
    })
  })

  it('closeWorktreeTabs accumulates flags across a mixed group (REMOVED + indeterminate)', async () => {
    // The fold is an OR across the whole group, not a single per-group verdict:
    // one tab's close removed the worktree while another's was indeterminate
    // (rejected RPC). Both flags must be recorded — the dialog's own precedence
    // (removed wins) then decides the toast.
    await createRoot(async (dispose) => {
      const { tabStore, ops, handleAgentClose } = setup()
      handleAgentClose
        .mockResolvedValueOnce({ worktreeRemoval: WorktreeRemovalOutcome.REMOVED })
        .mockResolvedValueOnce(undefined)
      tabStore.addTab({ type: TabType.AGENT, id: 'agent-b', tileId: 'tile-1', workerId: 'w-1' }, { activate: false })
      const a = tabStore.state.tabs.find(t => t.id === 'agent-a')!
      const b = tabStore.state.tabs.find(t => t.id === 'agent-b')!

      const summary = await ops.closeWorktreeTabs([a, b])

      expect(summary).toEqual({ removed: true, failed: false, stillReferenced: false, unknown: true })
      dispose()
    })
  })

  it('cross-workspace AGENT: fires closeAgent RPC directly + removes from registry snapshot', async () => {
    // DeleteBranchDialog opened against an INACTIVE workspace's branch
    // row hands tabs from that workspace's registry snapshot to
    // closeTabWithAction. Before the fix, the helper routed AGENT
    // closes through agentOps.handleAgentClose — which operates on the
    // active tabStore — so the cross-workspace tab's worker process
    // never received CloseAgent and the inactive workspace's sidebar
    // tree kept showing the row until the user switched into it.
    await createRoot(async (dispose) => {
      installTestBridge({ rootTileId: 'tile-active' })
      const tabStore = createTabStore()
      const chatStore = createChatStore()
      const layoutStore = createLayoutStore()
      const handleAgentClose = vi.fn()
      const handleTerminalClose = vi.fn()

      const crossTab = {
        type: TabType.AGENT,
        id: 'agent-cross',
        tileId: 'tile-cross',
        workerId: 'w-cross',
      } as const
      const removedTabs: Array<{ wsId: string, tabId: string }> = []
      const ops = useTabOperations({
        tabStore,
        chatStore,
        layoutStore,
        agentOps: { handleAgentClose } as never,
        termOps: { handleTerminalClose } as never,
        activeTab: () => tabStore.activeTab() ?? undefined,
        getCurrentTabContext: () => ({ workerId: 'w-active', workingDir: '/tmp', homeDir: '/home/test' }),
        focusEditor: vi.fn(),
        getScrollState: () => ({ distFromBottom: 9999, atBottom: false }),
        setFileTreePath: vi.fn(),
        getOrgId: () => 'org-test',
        getActiveWorkspaceId: () => 'ws-active',
        registry: {
          findContaining: () => ({ workspaceId: 'ws-other', tabs: [crossTab] } as never),
          removeTab: (wsId: string, tab: { id: string }) => { removedTabs.push({ wsId, tabId: tab.id }) },
        } as never,
      })

      ops.closeTabWithAction(crossTab as never, WorktreeAction.KEEP)
      // Direct worker RPC, not via active agentOps.
      expect(handleAgentClose).not.toHaveBeenCalled()
      expect(mockCloseAgent).toHaveBeenCalledTimes(1)
      expect(mockCloseAgent.mock.calls[0][0]).toBe('w-cross')
      expect(mockCloseAgent.mock.calls[0][1]).toMatchObject({
        agentId: 'agent-cross',
        worktreeAction: WorktreeAction.KEEP,
      })
      // Registry snapshot for ws-other drops the row.
      expect(removedTabs).toEqual([{ wsId: 'ws-other', tabId: 'agent-cross' }])
      dispose()
    })
  })

  it('cross-workspace TERMINAL: fires closeTerminal RPC with the owning workspaceId', async () => {
    await createRoot(async (dispose) => {
      installTestBridge({ rootTileId: 'tile-active' })
      const tabStore = createTabStore()
      const chatStore = createChatStore()
      const layoutStore = createLayoutStore()

      const crossTab = {
        type: TabType.TERMINAL,
        id: 'term-cross',
        tileId: 'tile-cross',
        workerId: 'w-cross',
      } as const
      const removedTabs: Array<{ wsId: string, tabId: string }> = []
      const ops = useTabOperations({
        tabStore,
        chatStore,
        layoutStore,
        agentOps: { handleAgentClose: vi.fn() } as never,
        termOps: { handleTerminalClose: vi.fn() } as never,
        activeTab: () => tabStore.activeTab() ?? undefined,
        getCurrentTabContext: () => ({ workerId: 'w-active', workingDir: '/tmp', homeDir: '/home/test' }),
        focusEditor: vi.fn(),
        getScrollState: () => ({ distFromBottom: 9999, atBottom: false }),
        setFileTreePath: vi.fn(),
        getOrgId: () => 'org-test',
        getActiveWorkspaceId: () => 'ws-active',
        registry: {
          findContaining: () => ({ workspaceId: 'ws-other', tabs: [crossTab] } as never),
          removeTab: (wsId: string, tab: { id: string }) => { removedTabs.push({ wsId, tabId: tab.id }) },
        } as never,
      })

      ops.closeTabWithAction(crossTab as never, WorktreeAction.REMOVE)
      expect(mockCloseTerminal).toHaveBeenCalledTimes(1)
      expect(mockCloseTerminal.mock.calls[0][0]).toBe('w-cross')
      expect(mockCloseTerminal.mock.calls[0][1]).toMatchObject({
        terminalId: 'term-cross',
        workspaceId: 'ws-other',
        worktreeAction: WorktreeAction.REMOVE,
        orgId: 'org-test',
      })
      expect(removedTabs).toEqual([{ wsId: 'ws-other', tabId: 'term-cross' }])
      dispose()
    })
  })

  it('cross-workspace FILE: revokes path + removes from registry, skips agent/terminal handlers', async () => {
    await createRoot(async (dispose) => {
      installTestBridge({ rootTileId: 'tile-active' })
      const tabStore = createTabStore()
      const chatStore = createChatStore()
      const layoutStore = createLayoutStore()

      const crossTab = {
        type: TabType.FILE,
        id: 'file-cross',
        tileId: 'tile-cross',
        filePath: '/repo-wts/feature/x.ts',
        workerId: 'w-cross',
      } as const
      const removedTabs: Array<{ wsId: string, tabId: string }> = []
      const ops = useTabOperations({
        tabStore,
        chatStore,
        layoutStore,
        agentOps: { handleAgentClose: vi.fn() } as never,
        termOps: { handleTerminalClose: vi.fn() } as never,
        activeTab: () => tabStore.activeTab() ?? undefined,
        getCurrentTabContext: () => ({ workerId: 'w-active', workingDir: '/tmp', homeDir: '/home/test' }),
        focusEditor: vi.fn(),
        getScrollState: () => ({ distFromBottom: 9999, atBottom: false }),
        setFileTreePath: vi.fn(),
        getOrgId: () => 'org-test',
        getActiveWorkspaceId: () => 'ws-active',
        registry: {
          findContaining: () => ({ workspaceId: 'ws-other', tabs: [crossTab] } as never),
          removeTab: (wsId: string, tab: { id: string }) => { removedTabs.push({ wsId, tabId: tab.id }) },
        } as never,
      })

      ops.closeTabWithAction(crossTab as never, WorktreeAction.KEEP)
      expect(mockRevokeFileTabPath).toHaveBeenCalledTimes(1)
      expect(mockRevokeFileTabPath.mock.calls[0][0]).toBe('w-cross')
      expect(mockRevokeFileTabPath.mock.calls[0][1]).toMatchObject({
        orgId: 'org-test',
        tabId: 'file-cross',
      })
      expect(removedTabs).toEqual([{ wsId: 'ws-other', tabId: 'file-cross' }])
      dispose()
    })
  })
})

// Variant of setup() that exposes the layoutStore so focus-migration
// behavior is directly assertable. The shared setup() seeds two AGENT
// tabs on tile-1 the same way; this variant adds a tile-other tab so
// migrateFocusAfterTabClose has somewhere to move focus to.
function setupForFocusMigration() {
  installTestBridge({ rootTileId: 'tile-1' })
  const tabStore = createTabStore()
  const chatStore = createChatStore()
  const layoutStore = createLayoutStore()
  layoutStore.setFocusedTile('tile-1')

  tabStore.addTab({ type: TabType.AGENT, id: 'agent-a', tileId: 'tile-1', workerId: 'w-1' })
  tabStore.addTab({ type: TabType.AGENT, id: 'agent-other', tileId: 'tile-other', workerId: 'w-1' }, { activate: false })

  const handleAgentClose = vi.fn()
  const handleTerminalClose = vi.fn()
  const ops = useTabOperations({
    tabStore,
    chatStore,
    layoutStore,
    agentOps: { handleAgentClose } as never,
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
  return { tabStore, layoutStore, ops, handleAgentClose, handleTerminalClose }
}
