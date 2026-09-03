import type { SavedViewportScroll } from '~/stores/chatTypes'
import { createRoot } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useTabOperations } from '~/components/shell/useTabOperations'
import { MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { WorktreeAction, WorktreeRemovalOutcome } from '~/generated/proto/leapmux/v1/common_pb'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { ChannelError } from '~/lib/channelError'
import { createChatStore, MAX_BACKGROUND_CHAT_MESSAGES } from '~/stores/chat.store'
import { createRepoGitStore } from '~/stores/repoGit.store'
import { emitAddTab, emitRemoveTab } from '~/stores/tabOps'
import { flush } from '~/test-support/async'
import { installTestBridge } from '~/test-support/crdtBridge'
import { makeMessage } from '~/test-support/messageFactory'
import { createTestFloatingWindowStore, createTestTabStores } from '~/test-support/tabStores'

// Default: every FILE / AGENT / TERMINAL close path now routes through
// inspectLastTabClose, so a vi.fn() with no implementation would resolve
// to `undefined` and trip the `status.shouldPrompt` access. Default to
// the no-prompt happy path; per-test overrides use mockResolvedValueOnce.
// Each mock forwards the worker RPC's (workerId, req, ...) arguments, so
// the param list is typed as `unknown[]`; tests read them back via
// `mock.calls[0]` and narrow `req` with a per-call `as { ... }` cast.
const mockInspectLastTabClose = vi.fn((..._args: unknown[]) => Promise.resolve({ shouldPrompt: false } as unknown))
const mockPushBranch = vi.fn((..._args: unknown[]) => {})
const mockRegisterTabPayload = vi.fn((..._args: unknown[]) => Promise.resolve({}))
const mockRevokeTabPayload = vi.fn((..._args: unknown[]) => Promise.resolve({}))
const mockCloseAgent = vi.fn((..._args: unknown[]) => Promise.resolve({ result: undefined }))
const mockCloseTerminal = vi.fn((..._args: unknown[]) => Promise.resolve({ result: undefined }))
const mockShowWarnToast = vi.fn()
const mockShowInfoToast = vi.fn()

vi.mock('~/api/workerRpc', () => ({
  inspectLastTabClose: (...args: unknown[]) => mockInspectLastTabClose(...args),
  pushBranch: (...args: unknown[]) => mockPushBranch(...args),
  registerTabPayload: (...args: unknown[]) => mockRegisterTabPayload(...args),
  revokeTabPayload: (...args: unknown[]) => mockRevokeTabPayload(...args),
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
  return makeMessage({
    id,
    seq,
    source: MessageSource.USER,
    content: new TextEncoder().encode('{"content":"test"}'),
  })
}

const DEFAULT_SCROLL_STATE: () => SavedViewportScroll | undefined = () => ({ atBottom: false, hasMoreNewer: false })

let nextPosition = 0

/** Place a FILE tab through the op path, with its path in metadata. */
function addFile(
  stores: ReturnType<typeof createTestTabStores>,
  id: string,
  tileId: string,
  filePath: string,
) {
  nextPosition += 1
  emitAddTab({ type: TabType.FILE, id, tileId, position: `p${nextPosition}`, workerId: 'w-1' })
  stores.metadata.patch(id, { filePath })
}

/** Place an agent tab through the op path. */
function addAgent(
  stores: ReturnType<typeof createTestTabStores>,
  id: string,
  tileId: string,
  position?: string,
  meta: Record<string, unknown> = {},
) {
  nextPosition += 1
  emitAddTab({ type: TabType.AGENT, id, tileId, position: position ?? `p${nextPosition}`, workerId: 'w-1' })
  if (Object.keys(meta).length > 0)
    stores.metadata.patch(id, meta)
}

function setup(
  getScrollState: () => SavedViewportScroll | undefined = DEFAULT_SCROLL_STATE,
  // Default ONLINE, because that is the normal state and it is the state in
  // which a transport failure must NOT be read as "the worker is gone".
  workerOnlineState: (workerId: string) => boolean | undefined = () => true,
  // `unbootstrapped` keys the stores at a workspace the bridge never
  // delivered, so placement has no projected tree to resolve to.
  opts: { unbootstrapped?: boolean } = {},
) {
  // Override the global test bridge with a known tile id so the
  // projection's root leaf is the tile these tests address.
  installTestBridge({ rootTileId: 'tile-1' })
  const stores = createTestTabStores(opts.unbootstrapped ? 'ws-never-bootstrapped' : 'ws-test')
  const { view, metadata, selection, layoutStore } = stores
  const chatStore = createChatStore()

  const tileId = 'tile-1'
  layoutStore.setFocusedTile(tileId)

  addAgent(stores, 'agent-a', tileId, 'a')
  addAgent(stores, 'agent-b', tileId, 'b')
  selection.setActiveById(TabType.AGENT, 'agent-a')

  // handleAgentClose / handleTerminalClose return Promise<CloseTabResult |
  // undefined>, but these tests only assert how they're invoked (call args),
  // not the resolved outcome, so a plain vi.fn() (resolving undefined) is enough.
  const handleAgentClose = vi.fn()
  const handleTerminalClose = vi.fn()
  // The descendant path retires locally instead of firing a per-child RPC, so
  // this is the seam that records which subagents went, and in what order.
  const retireAgentTabLocally = vi.fn()

  const ops = useTabOperations({
    view,
    metadata,
    selection,
    chatStore,
    layoutStore,
    agentOps: {
      handleAgentClose,
      retireAgentTabLocally,
    } as never,
    termOps: {
      handleTerminalClose,
    } as never,
    activeTab: () => selection.activeTabForWorkspace('ws-test'),
    getCurrentTabContext: () => ({ workerId: 'w-1', workingDir: '/tmp', homeDir: '/home/test', gitToplevel: '' }),
    focusEditor: vi.fn(),
    getScrollState,
    setFileTreePath: vi.fn(),
    getActiveWorkspaceId: () => 'ws-test',
    workerOnlineState,
    repoGitStore: createRepoGitStore(),
  })

  return {
    ...stores,
    chatStore,
    ops,
    tileId,
    handleAgentClose,
    handleTerminalClose,
    retireAgentTabLocally,
    addFile: (id: string, filePath: string, onTile = tileId) => addFile(stores, id, onTile, filePath),
    addAgent: (id: string, onTile = tileId, meta: Record<string, unknown> = {}) =>
      addAgent(stores, id, onTile, undefined, meta),
    addTerminal: (id: string, onTile = tileId) => {
      nextPosition += 1
      emitAddTab({ type: TabType.TERMINAL, id, tileId: onTile, position: `p${nextPosition}`, workerId: 'w-1' })
    },
  }
}

// The worktree outcome report crosses several awaits before it toasts: the
// per-tab close guard, the Promise.all over the group, the fold, then the
// message. A fixed tick count is brittle across all of them, so yield to the
// macrotask queue once, which drains every pending microtask first.
function settle() {
  return new Promise(resolve => setTimeout(resolve, 0))
}

describe('useTabOperations', () => {
  afterEach(() => {
    mockInspectLastTabClose.mockReset()
    mockInspectLastTabClose.mockImplementation(() => Promise.resolve({ shouldPrompt: false } as unknown))
    mockPushBranch.mockReset()
    mockRegisterTabPayload.mockReset()
    mockRevokeTabPayload.mockReset()
    mockRegisterTabPayload.mockImplementation(() => Promise.resolve({}))
    mockRevokeTabPayload.mockImplementation(() => Promise.resolve({}))
    mockShowWarnToast.mockReset()
    mockShowInfoToast.mockReset()
  })

  describe('file-tab E2EE worker round-trip', () => {
    it('handleFileOpen calls RegisterTabPayload with the local path and the originating tab\'s working dir', async () => {
      await createRoot(async (dispose) => {
        try {
          const { ops } = setup()
          ops.handleFileOpen('/tmp/nested/myfile.go')
          // Allow the fire-and-forget E2EE call to dispatch.
          await Promise.resolve()
          expect(mockRegisterTabPayload).toHaveBeenCalledTimes(1)
          const [workerId, req] = mockRegisterTabPayload.mock.calls[0]
          expect(workerId).toBe('w-1')
          // The working dir is the CONTEXT's, not the file's own directory:
          // it is what the worker answers this tab's branch-context questions
          // from (last-tab close, sibling scan, push), so it has to name the
          // repo the user is working in rather than wherever the file sits.
          expect(req).toMatchObject({
            payload: {
              workingDir: '/tmp',
              kind: { case: 'file', value: { filePath: '/tmp/nested/myfile.go' } },
            },
          })
        }
        finally {
          dispose()
        }
      })
    })

    it('handleImageOpen registers a message reference, never the pixels', async () => {
      await createRoot(async (dispose) => {
        try {
          const { ops } = setup()
          ops.handleImageOpen({ agentId: 'agent-a', seq: 42n, imageIndex: 1, title: 'Read' })
          await Promise.resolve()
          expect(mockRegisterTabPayload).toHaveBeenCalledTimes(1)
          const [workerId, req] = mockRegisterTabPayload.mock.calls[0] as [string, Record<string, unknown>]
          expect(workerId).toBe('w-1')
          expect(req).toMatchObject({
            payload: {
              workingDir: '/tmp',
              kind: {
                case: 'image',
                value: { agentId: 'agent-a', seq: 42n, imageIndex: 1, title: 'Read' },
              },
            },
          })
          // The whole point of the reference: a screenshot must not reach the
          // tab layer, which the hub sees the identity half of.
          expect(JSON.stringify(req, (_k, v) => typeof v === 'bigint' ? String(v) : v))
            .not
            .toContain('base64')
        }
        finally {
          dispose()
        }
      })
    })

    it('handleImageOpen focuses the existing tab instead of opening a second one', async () => {
      await createRoot(async (dispose) => {
        try {
          const { ops, view } = setup()
          ops.handleImageOpen({ agentId: 'agent-a', seq: 42n, imageIndex: 1, title: 'Read' })
          await flush()
          const opened = view.forWorkspace('ws-test').filter(t => t.type === TabType.IMAGE)
          expect(opened).toHaveLength(1)

          mockRegisterTabPayload.mockClear()
          ops.handleImageOpen({ agentId: 'agent-a', seq: 42n, imageIndex: 1, title: 'Read' })
          await flush()
          expect(view.forWorkspace('ws-test').filter(t => t.type === TabType.IMAGE)).toHaveLength(1)
          expect(mockRegisterTabPayload).not.toHaveBeenCalled()
        }
        finally {
          dispose()
        }
      })
    })

    it('handleImageOpen opens a separate tab for a different image of the same message', async () => {
      await createRoot(async (dispose) => {
        try {
          const { ops, view } = setup()
          ops.handleImageOpen({ agentId: 'agent-a', seq: 42n, imageIndex: 0, title: 'Read' })
          ops.handleImageOpen({ agentId: 'agent-a', seq: 42n, imageIndex: 1, title: 'Read' })
          await flush()
          expect(view.forWorkspace('ws-test').filter(t => t.type === TabType.IMAGE)).toHaveLength(2)
        }
        finally {
          dispose()
        }
      })
    })

    it('handleImageOpen rolls the tab back when the payload registration fails', async () => {
      await createRoot(async (dispose) => {
        try {
          const { ops, view } = setup()
          mockRegisterTabPayload.mockRejectedValueOnce(new Error('channel closed'))
          ops.handleImageOpen({ agentId: 'agent-a', seq: 42n, imageIndex: 0, title: 'Read' })
          await flush()
          // Leaving it would strand a tab no peer -- and no reload -- can
          // resolve, because nothing on the worker describes it.
          expect(view.forWorkspace('ws-test').filter(t => t.type === TabType.IMAGE)).toHaveLength(0)
        }
        finally {
          dispose()
        }
      })
    })

    // The routing rule the whole click path rests on. The transcript's copy of
    // an image the agent read from disk is a downsample it sent to its model, so
    // a named path has to reach the FILE viewer instead -- same picture, full
    // resolution, straight off the Worker.
    it('handleChatImageOpen opens the FILE itself when the provider named a path', async () => {
      await createRoot(async (dispose) => {
        try {
          const { ops, view } = setup()
          ops.handleChatImageOpen({
            agentId: 'agent-a',
            seq: 42n,
            index: 0,
            filePath: '/repo/shot.png',
            title: 'Read',
          })
          await flush()

          const tabs = view.forWorkspace('ws-test')
          expect(tabs.filter(t => t.type === TabType.IMAGE)).toHaveLength(0)
          const files = tabs.filter(t => t.type === TabType.FILE)
          expect(files).toHaveLength(1)
          expect(files[0]).toMatchObject({ filePath: '/repo/shot.png' })
          // A FILE payload, so a peer resolves it as a path and not as a
          // message reference it would have to fetch an agent message for.
          expect(mockRegisterTabPayload).toHaveBeenCalledTimes(1)
          const [, req] = mockRegisterTabPayload.mock.calls[0] as [string, Record<string, unknown>]
          expect(req).toMatchObject({ payload: { kind: { case: 'file' } } })
        }
        finally {
          dispose()
        }
      })
    })

    it('handleChatImageOpen opens an IMAGE tab when the image exists only in the transcript', async () => {
      await createRoot(async (dispose) => {
        try {
          const { ops, view } = setup()
          // No filePath: an MCP screenshot, or a Bash whose stdout was a data
          // URI. Nothing on disk to open, so the reference is the only route.
          ops.handleChatImageOpen({ agentId: 'agent-a', seq: 42n, index: 3, title: 'screenshot' })
          await flush()

          const tabs = view.forWorkspace('ws-test')
          expect(tabs.filter(t => t.type === TabType.FILE)).toHaveLength(0)
          const images = tabs.filter(t => t.type === TabType.IMAGE)
          expect(images).toHaveLength(1)
          // `index` becomes `imageIndex`, which is what the viewer resolves by.
          expect(images[0]).toMatchObject({ imageAgentId: 'agent-a', imageSeq: 42n, imageIndex: 3 })
        }
        finally {
          dispose()
        }
      })
    })

    // `''` is "no path", not a path. An extractor that fills `filePath` from a
    // field the wire left blank produces one, and routing it to the FILE branch
    // would open a viewer on the empty path -- which the worker refuses, so the
    // user would get a broken tab instead of the picture they clicked.
    it('handleChatImageOpen treats an empty path as no path', async () => {
      await createRoot(async (dispose) => {
        try {
          const { ops, view } = setup()
          ops.handleChatImageOpen({ agentId: 'agent-a', seq: 42n, index: 0, filePath: '', title: 'shot' })
          await flush()

          const tabs = view.forWorkspace('ws-test')
          expect(tabs.filter(t => t.type === TabType.FILE)).toHaveLength(0)
          expect(tabs.filter(t => t.type === TabType.IMAGE)).toHaveLength(1)
        }
        finally {
          dispose()
        }
      })
    })

    it('handleFileOpen seeds the new tab with the originating tab\'s git group', async () => {
      await createRoot(async (dispose) => {
        try {
          const { ops, view, metadata } = setup()
          // The active agent knows its repo. Stamp the fields the sidebar
          // groups on; without the seed the file tab renders ungrouped until
          // the next git-status refresh reaches it.
          metadata.patch('agent-a', {
            workingDir: '/tmp',
            gitToplevel: '/tmp',
          })
          ops.handleFileOpen('/tmp/myfile.go')
          const opened = view.forWorkspace('ws-test').find(t => t.type === TabType.FILE)
          expect(opened).toMatchObject({
            gitToplevel: '/tmp',
            workingDir: '/tmp',
          })
        }
        finally {
          dispose()
        }
      })
    })

    // A refused placement added no tab, so the worker must not persist a
    // file-tab path for the id either: the row (and its broadcast to peers)
    // would name a tab no tree holds, and only the hourly reconciler sweep
    // would remove it.
    it('handleFileOpen registers no path and warns when placement refuses', async () => {
      await createRoot(async (dispose) => {
        try {
          const { ops, view } = setup(DEFAULT_SCROLL_STATE, () => true, { unbootstrapped: true })

          ops.handleFileOpen('/tmp/orphan.go')
          await Promise.resolve()

          // The seeded agent tabs are outside this workspace's tree; the
          // claim is that no FILE tab joined them.
          expect(view.all().some(t => t.type === TabType.FILE), 'nothing was placed').toBe(false)
          expect(mockRegisterTabPayload, 'no phantom row for an unplaced tab id').not.toHaveBeenCalled()
          expect(mockShowWarnToast).toHaveBeenCalled()
        }
        finally {
          dispose()
        }
      })
    })

    // Opening a file from a git filter tab starts it in the diff that tab shows.
    // The mode is what puts the diff-mode toolbar on screen at all, so losing it
    // silently downgrades the open to a plain read of the working copy.
    it('opens a file from a git filter tab in the diff view that tab names', async () => {
      await createRoot(async (dispose) => {
        try {
          const { ops, view } = setup()
          ops.handleFileOpen('/tmp/staged.go', 'staged')
          const opened = view.forWorkspace('ws-test').find(t => t.type === TabType.FILE)
          expect(opened).toMatchObject({
            fileViewMode: 'unified-diff',
            fileDiffBase: 'head-vs-staged',
            fileOpenSource: 'staged',
          })
        }
        finally {
          dispose()
        }
      })
    })

    it('handleTabClose on a FILE tab inspects then calls RevokeTabPayload with KEEP', async () => {
      await createRoot(async (dispose) => {
        try {
          const { view, ops, addFile } = setup()
          addFile('file-1', '/tmp/myfile.go')
          const tab = view.getById(TabType.FILE, 'file-1')!
          // Worker reports the FILE close has siblings (or is not in a
          // worktree), so shouldPrompt=false and we commit straight to
          // KEEP — same shape as the AGENT / TERMINAL no-prompt path.
          mockInspectLastTabClose.mockResolvedValueOnce({ shouldPrompt: false })

          const ok = await ops.handleTabClose(tab)
          expect(ok).toBe(true)
          await Promise.resolve()
          expect(mockInspectLastTabClose).toHaveBeenCalledTimes(1)
          expect(mockRevokeTabPayload).toHaveBeenCalledTimes(1)
          const [workerId, req] = mockRevokeTabPayload.mock.calls[0]
          expect(workerId).toBe('w-1')
          expect((req as { tabId: string }).tabId).toBe('file-1')
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
          const { view, ops, addFile } = setup()
          addFile('file-last', '/tmp/myfile.go')
          const tab = view.getById(TabType.FILE, 'file-last')!
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
          expect(view.all().some(t => t.id === 'file-last')).toBe(true)
          expect(mockRevokeTabPayload).not.toHaveBeenCalled()
        }
        finally {
          dispose()
        }
      })
    })

    it('handleTabClose FILE tab close-anyway forwards WorktreeAction.KEEP to RevokeTabPayload', async () => {
      // Counterpart to the schedule-delete test below: the user chose
      // to close the last FILE tab but keep the worktree on disk.
      // RevokeTabPayload must still fire (the row + worktree link
      // need to come down) with KEEP so the worker side leaves the
      // worktree alone — same shape as the AGENT/TERMINAL close-anyway
      // path.
      await createRoot(async (dispose) => {
        try {
          const { view, ops, addFile } = setup()
          addFile('file-keep', '/tmp/myfile.go')
          const tab = view.getById(TabType.FILE, 'file-keep')!
          mockInspectLastTabClose.mockResolvedValueOnce({ shouldPrompt: true })

          const closePromise = ops.handleTabClose(tab)
          await flush()
          ops.lastTabConfirmDialog.value()!.resolve('close-anyway')
          const ok = await closePromise
          expect(ok).toBe(true)
          await Promise.resolve()
          expect(mockRevokeTabPayload).toHaveBeenCalledTimes(1)
          const [, req] = mockRevokeTabPayload.mock.calls[0]
          expect((req as { worktreeAction: WorktreeAction }).worktreeAction).toBe(WorktreeAction.KEEP)
          expect(mockShowInfoToast).not.toHaveBeenCalledWith('Worktree will be removed')
        }
        finally {
          dispose()
        }
      })
    })

    it('handleTabClose FILE tab schedule-delete forwards WorktreeAction.REMOVE to RevokeTabPayload', async () => {
      await createRoot(async (dispose) => {
        try {
          const { view, ops, addFile } = setup()
          addFile('file-delete', '/tmp/myfile.go')
          const tab = view.getById(TabType.FILE, 'file-delete')!
          mockInspectLastTabClose.mockResolvedValueOnce({ shouldPrompt: true })

          const closePromise = ops.handleTabClose(tab)
          await flush()
          ops.lastTabConfirmDialog.value()!.resolve('schedule-delete')
          const ok = await closePromise
          expect(ok).toBe(true)
          await Promise.resolve()
          expect(mockRevokeTabPayload).toHaveBeenCalledTimes(1)
          const [, req] = mockRevokeTabPayload.mock.calls[0]
          expect((req as { worktreeAction: WorktreeAction }).worktreeAction).toBe(WorktreeAction.REMOVE)
          // The report is the worker's verdict, not an optimistic promise at
          // click time. This mock resolves no result, so the honest answer is
          // "could not confirm".
          await settle()
          expect(mockShowInfoToast).toHaveBeenCalledWith('Could not confirm the worktree removal')
          expect(mockShowInfoToast).not.toHaveBeenCalledWith('Worktree will be removed')
        }
        finally {
          dispose()
        }
      })
    })

    it('handleFileOpen rolls back the optimistic tab on RegisterTabPayload failure', async () => {
      await createRoot(async (dispose) => {
        try {
          mockRegisterTabPayload.mockImplementationOnce(() => Promise.reject(new Error('e2ee failure')))
          const { view, ops } = setup()
          ops.handleFileOpen('/tmp/myfile.go')
          // Tab added optimistically.
          expect(view.all().some(t => t.type === TabType.FILE)).toBe(true)
          // Wait for the rejection microtask to fire the rollback.
          await new Promise(r => setTimeout(r, 0))
          expect(view.all().some(t => t.type === TabType.FILE)).toBe(false)
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
        const { view, ops, handleAgentClose } = setup()
        const tab = view.getById(TabType.AGENT, 'agent-a')!
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
        const { view, ops, handleAgentClose } = setup()
        const tab = view.getById(TabType.AGENT, 'agent-a')!
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
        const { view, ops, handleAgentClose, handleTerminalClose } = setup()
        const tab = view.getById(TabType.AGENT, 'agent-a')!
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

        expect(view.all().some(t => t.id === 'agent-a')).toBe(true)
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
        const { view, ops, handleAgentClose } = setup()
        const tab = view.getById(TabType.AGENT, 'agent-a')!
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
        const { view, ops, handleAgentClose } = setup()
        const tab = view.getById(TabType.AGENT, 'agent-a')!
        mockInspectLastTabClose.mockResolvedValueOnce({ shouldPrompt: true, worktreeId: 'wt-1' })
        handleAgentClose.mockResolvedValue({ worktreeRemoval: WorktreeRemovalOutcome.REMOVED })

        const closePromise = ops.handleTabClose(tab)
        await flush()
        ops.lastTabConfirmDialog.value()!.resolve('schedule-delete')
        await closePromise

        expect(handleAgentClose).toHaveBeenCalledWith('agent-a', WorktreeAction.REMOVE)
        // The single-tab close reports the worker's REAL outcome when it lands,
        // through the same mapper the Delete branch dialog uses. It no longer
        // promises a removal at click time -- which left STILL_REFERENCED and a
        // degrade-to-KEEP silently contradicting what the user was told.
        await settle()
        expect(mockShowInfoToast).toHaveBeenCalledWith('Worktree removed')
        expect(mockShowInfoToast).not.toHaveBeenCalledWith('Worktree will be removed')
      }
      finally {
        dispose()
      }
    })
  })

  it('inspect error: toast shown, handler not invoked, spinner cleared', async () => {
    await createRoot(async (dispose) => {
      try {
        const { view, ops, handleAgentClose } = setup()
        const agentTab = view.getById(TabType.AGENT, 'agent-a')!
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

  /**
   * The offline close. A registered-but-OFFLINE worker never reaches a connect
   * code here: the hub tears down the channels its stream was carrying, so the
   * inspect rejects with a transport `ChannelError`. Before `isWorkerUnreachable`
   * matched that shape the close was refused with "Failed to prepare tab close",
   * and a user whose laptop was asleep could not retire the tab at all.
   *
   * The transport shape alone is NOT enough -- see the two tests below. It
   * takes a positive offline reading from the worker list, which is what
   * `workerOnlineState` supplies here.
   */
  it('offline worker: tombstones the tab with KEEP instead of refusing the close', async () => {
    await createRoot(async (dispose) => {
      try {
        const { view, ops, handleAgentClose } = setup(DEFAULT_SCROLL_STATE, () => false)
        const tab = view.getById(TabType.AGENT, 'agent-a')!
        mockInspectLastTabClose.mockRejectedValueOnce(
          new ChannelError('transport', 'channel closed by server'),
        )

        expect(await ops.handleTabClose(tab)).toBe(true)

        expect(mockShowWarnToast).not.toHaveBeenCalledWith('Failed to prepare tab close', expect.anything())
        expect(mockShowInfoToast).toHaveBeenCalledWith('Worker is unreachable; removing the tab without closing it.')
        // KEEP, not the dialog's choice: there is no worker to run
        // `git worktree remove`, so the removal cannot be honoured.
        // (`handleAgentClose` is the seam that emits the CRDT tombstone; it is
        // stubbed here, so the assertion is that the commit phase RAN at all.)
        expect(handleAgentClose).toHaveBeenCalledWith('agent-a', WorktreeAction.KEEP)
      }
      finally {
        dispose()
      }
    })
  })

  /**
   * The bug this pair guards. `source: 'transport'` covers far more than "the
   * worker is offline": our own hub WebSocket dropping, a WS-open timeout, an
   * E2EE rekey timeout, the session key passing its hard ceiling, and any
   * non-ChannelError thrown on the send path. The CRDT leg rides a different
   * transport, so it stays healthy and the tombstone would commit -- silently
   * retiring a tab whose worktree may hold uncommitted work, against a machine
   * that is up and answering. Refusing the close (the user can retry) is the
   * only safe answer when the worker is not positively known to be offline.
   */
  it('online worker: a transport failure refuses the close instead of retiring the tab', async () => {
    await createRoot(async (dispose) => {
      try {
        const { view, ops, handleAgentClose } = setup(DEFAULT_SCROLL_STATE, () => true)
        const tab = view.getById(TabType.AGENT, 'agent-a')!
        mockInspectLastTabClose.mockRejectedValueOnce(
          new ChannelError('transport', 'session key past hard ceiling'),
        )

        expect(await ops.handleTabClose(tab)).toBe(false)

        expect(mockShowWarnToast).toHaveBeenCalledWith('Failed to prepare tab close', expect.anything())
        expect(mockShowInfoToast).not.toHaveBeenCalledWith('Worker is unreachable; removing the tab without closing it.')
        expect(handleAgentClose).not.toHaveBeenCalled()
      }
      finally {
        dispose()
      }
    })
  })

  it('unknown liveness: a transport failure refuses the close (fails closed)', async () => {
    await createRoot(async (dispose) => {
      try {
        const { view, ops, handleAgentClose } = setup(DEFAULT_SCROLL_STATE, () => undefined)
        const tab = view.getById(TabType.AGENT, 'agent-a')!
        mockInspectLastTabClose.mockRejectedValueOnce(
          new ChannelError('transport', 'channel closed by server'),
        )

        expect(await ops.handleTabClose(tab)).toBe(false)
        expect(handleAgentClose).not.toHaveBeenCalled()
      }
      finally {
        dispose()
      }
    })
  })

  it('degraded close: error_hint surfaces a warn toast but the close still proceeds', async () => {
    await createRoot(async (dispose) => {
      try {
        const { view, ops, handleAgentClose } = setup()
        const tab = view.getById(TabType.AGENT, 'agent-a')!
        // Worker let the close proceed without a prompt only because git
        // was unavailable — shouldPrompt is false but error_hint is set.
        // The close must still win (KEEP), and the user must be warned.
        mockInspectLastTabClose.mockResolvedValueOnce({
          shouldPrompt: false,
          errorHint: 'git state unavailable; closed without checking for uncommitted or unpushed changes',
        })

        await ops.handleTabClose(tab)

        expect(mockShowWarnToast).toHaveBeenCalledWith('git state unavailable; closed without checking for uncommitted or unpushed changes')
        expect(handleAgentClose).toHaveBeenCalledWith('agent-a', WorktreeAction.KEEP)
      }
      finally {
        dispose()
      }
    })
  })

  it('rapid double-close dedupes during decide phase', async () => {
    await createRoot(async (dispose) => {
      try {
        const { view, ops, handleAgentClose } = setup()
        const tab = view.getById(TabType.AGENT, 'agent-a')!

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
      const { view, chatStore, ops, selection } = setup()
      const initial = Array.from({ length: MAX_BACKGROUND_CHAT_MESSAGES + 10 }, (_, i) =>
        makeUserMessage(`m${i + 1}`, BigInt(i + 1)))
      chatStore.setMessages('agent-a', initial)

      const nextTab = view.getById(TabType.AGENT, 'agent-b')!
      ops.handleTabSelect(nextTab)
      selection.setActiveById(nextTab.type, nextTab.id)

      const trimmed = chatStore.getMessages('agent-a')
      expect(trimmed).toHaveLength(MAX_BACKGROUND_CHAT_MESSAGES)
      expect(trimmed[0].seq).toBe(11n)
      expect(trimmed.at(-1)?.seq).toBe(60n)
      expect(chatStore.hasOlderMessages('agent-a')).toBe(true)
      dispose()
    })
  })

  it('clears a stale saved viewport when the outgoing tab has no restorable position', () => {
    createRoot((dispose) => {
      // The outgoing view is empty / all-hidden and scrolled away from the
      // bottom, so getScrollState() returns undefined.
      const { view, chatStore, ops, selection } = setup(() => undefined)
      // A prior visit left a saved anchor for agent-a.
      chatStore.viewportScroll.set('agent-a', { anchor: { id: 'old', offsetWithinRow: 40 }, atBottom: false, hasMoreNewer: false })
      expect(chatStore.viewportScroll.get('agent-a')).toBeDefined()

      const nextTab = view.getById(TabType.AGENT, 'agent-b')!
      ops.handleTabSelect(nextTab)
      selection.setActiveById(nextTab.type, nextTab.id)

      // The stale save is cleared rather than left to restore a wrong position.
      expect(chatStore.viewportScroll.get('agent-a')).toBeUndefined()
      dispose()
    })
  })

  it('saves the viewport when the outgoing tab has a restorable position', () => {
    createRoot((dispose) => {
      const saved = { anchor: { id: 'm5', offsetWithinRow: 12 }, atBottom: false, hasMoreNewer: false }
      const { view, chatStore, ops, selection } = setup(() => saved)

      const nextTab = view.getById(TabType.AGENT, 'agent-b')!
      ops.handleTabSelect(nextTab)
      selection.setActiveById(nextTab.type, nextTab.id)

      expect(chatStore.viewportScroll.get('agent-a')).toEqual(saved)
      dispose()
    })
  })

  it('does not trim when switching focus to a tab in a different tile', () => {
    createRoot((dispose) => {
      const { view, chatStore, ops, selection, layoutStore, tileId, addAgent } = setup()
      const initial = Array.from({ length: MAX_BACKGROUND_CHAT_MESSAGES + 10 }, (_, i) =>
        makeUserMessage(`m${i + 1}`, BigInt(i + 1)))
      chatStore.setMessages('agent-a', initial)

      // A real second leaf: a tab parked on a tile id that isn't in the
      // projected tree would never resolve into the view at all.
      const otherTile = layoutStore.splitTile(tileId, 'horizontal')!
      addAgent('agent-c', otherTile)
      const nextTab = view.getById(TabType.AGENT, 'agent-c')!
      ops.handleTabSelect(nextTab)
      selection.setActiveById(nextTab.type, nextTab.id)

      const messages = chatStore.getMessages('agent-a')
      expect(messages).toHaveLength(MAX_BACKGROUND_CHAT_MESSAGES + 10)
      expect(messages[0].seq).toBe(1n)
      expect(messages.at(-1)?.seq).toBe(60n)
      dispose()
    })
  })
})

/**
 * Closing an agent tab closes the subagent tabs under it.
 *
 * A child agent tab is a transcript its parent's provider feeds; it owns no
 * process of its own. With the parent gone nothing can add to it, and the
 * sidebar promotes it to a top-level row claiming a lineage the user can no
 * longer see -- so the subtree goes with the tab the user closed.
 */
describe('useTabOperations.handleTabClose subagent sweep', () => {
  afterEach(() => {
    mockInspectLastTabClose.mockReset()
    mockInspectLastTabClose.mockImplementation(() => Promise.resolve({ shouldPrompt: false } as unknown))
  })

  /** Ids handleAgentClose was called with, in call order. */
  const closedIds = (fn: ReturnType<typeof vi.fn>) => fn.mock.calls.map(c => c[0] as string)

  it('closes every descendant, deepest first, when a root agent closes', async () => {
    await createRoot(async (dispose) => {
      try {
        const { view, ops, addAgent, handleAgentClose, retireAgentTabLocally } = setup()
        addAgent('child-1', 'tile-1', { parentAgentId: 'agent-a' })
        addAgent('child-2', 'tile-1', { parentAgentId: 'agent-a' })
        addAgent('grandchild', 'tile-1', { parentAgentId: 'child-1' })

        const ok = await ops.handleTabClose(view.getById(TabType.AGENT, 'agent-a')!)
        expect(ok).toBe(true)

        // The descendants leave the view here and now: their tombstones are
        // this function's own, not the mocked RPC's. `agent-a` is not checked
        // because `handleAgentClose` is the seam that emits its tombstone, and
        // it is a mock in these tests.
        for (const id of ['grandchild', 'child-1', 'child-2'])
          expect(view.getById(TabType.AGENT, id), `${id} is gone`).toBeUndefined()

        const ids = closedIds(retireAgentTabLocally)
        expect(new Set(ids)).toEqual(new Set(['grandchild', 'child-1', 'child-2']))
        // A tab goes before the one that placed it: each close prunes an
        // emptied floating window, and the parent going first would prune the
        // window its own children still sit in.
        expect(ids.indexOf('grandchild')).toBeLessThan(ids.indexOf('child-1'))
        // ...and the tab the user clicked is the LAST to go, so it is the one
        // that finds the tile empty.
        expect(closedIds(handleAgentClose)).toEqual(['agent-a'])
      }
      finally {
        dispose()
      }
    })
  })

  /**
   * A child close is UI-only on the worker, and the ONE RPC the clicked tab
   * fires already stamps `closed_at` over the whole subtree -- so a call per
   * child would spend a round trip each to do nothing, and each response would
   * re-report a subtree the parent's answer already covered.
   */
  it('retires each subagent locally, with no RPC and no inspection of its own', async () => {
    await createRoot(async (dispose) => {
      try {
        const { view, ops, addAgent, handleAgentClose, retireAgentTabLocally } = setup()
        addAgent('child-1', 'tile-1', { parentAgentId: 'agent-a' })

        await ops.handleTabClose(view.getById(TabType.AGENT, 'agent-a')!)

        expect(mockInspectLastTabClose).toHaveBeenCalledTimes(1)
        expect(retireAgentTabLocally).toHaveBeenCalledWith('child-1')
        expect(
          handleAgentClose.mock.calls.map(c => c[0]),
          'only the clicked tab reaches the worker',
        ).toEqual(['agent-a'])
      }
      finally {
        dispose()
      }
    })
  })

  // The sweep runs in the COMMIT phase. A cancelled prompt leaves the whole
  // subtree open -- closing the children of a tab that survives would be the
  // worst possible outcome of saying "cancel".
  it('leaves the subtree open when the user cancels the close', async () => {
    await createRoot(async (dispose) => {
      try {
        const { view, ops, addAgent, handleAgentClose } = setup()
        addAgent('child-1', 'tile-1', { parentAgentId: 'agent-a' })
        mockInspectLastTabClose.mockResolvedValueOnce({ shouldPrompt: true })

        const closePromise = ops.handleTabClose(view.getById(TabType.AGENT, 'agent-a')!)
        await flush()
        ops.lastTabConfirmDialog.value()!.resolve('cancel')

        expect(await closePromise).toBe(false)
        expect(handleAgentClose).not.toHaveBeenCalled()
        expect(view.getById(TabType.AGENT, 'child-1')).toBeDefined()
      }
      finally {
        dispose()
      }
    })
  })

  it('closes a subagent tab\'s own subagents when the subagent tab closes', async () => {
    await createRoot(async (dispose) => {
      try {
        const { view, ops, addAgent, handleAgentClose, retireAgentTabLocally } = setup()
        addAgent('child-1', 'tile-1', { parentAgentId: 'agent-a' })
        addAgent('grandchild', 'tile-1', { parentAgentId: 'child-1' })

        const ok = await ops.handleTabClose(view.getById(TabType.AGENT, 'child-1')!)
        expect(ok).toBe(true)

        // A child close is UI-only, so it skips the inspection entirely.
        expect(mockInspectLastTabClose).not.toHaveBeenCalled()
        expect(closedIds(retireAgentTabLocally)).toEqual(['grandchild'])
        expect(closedIds(handleAgentClose)).toEqual(['child-1'])
        expect(view.getById(TabType.AGENT, 'grandchild')).toBeUndefined()
        // ...and the SIBLING branch is untouched: only what is below this tab goes.
        expect(view.getById(TabType.AGENT, 'agent-b')).toBeDefined()
      }
      finally {
        dispose()
      }
    })
  })

  it('leaves an unrelated agent tab alone', async () => {
    await createRoot(async (dispose) => {
      try {
        const { view, ops, addAgent, handleAgentClose } = setup()
        addAgent('child-of-b', 'tile-1', { parentAgentId: 'agent-b' })

        await ops.handleTabClose(view.getById(TabType.AGENT, 'agent-a')!)

        expect(closedIds(handleAgentClose)).toEqual(['agent-a'])
      }
      finally {
        dispose()
      }
    })
  })

  // A terminal and an agent tab can share an id, and only an AGENT is ever a
  // parent -- so the sweep must not reach a same-named tab of another kind.
  it('never closes a terminal tab that shares an id with a subagent', async () => {
    await createRoot(async (dispose) => {
      try {
        const { view, ops, addAgent, addTerminal, handleAgentClose, handleTerminalClose, retireAgentTabLocally } = setup()
        addAgent('child-1', 'tile-1', { parentAgentId: 'agent-a' })
        addTerminal('child-1')

        await ops.handleTabClose(view.getById(TabType.AGENT, 'agent-a')!)

        expect(closedIds(retireAgentTabLocally)).toEqual(['child-1'])
        expect(closedIds(handleAgentClose)).toEqual(['agent-a'])
        expect(handleTerminalClose).not.toHaveBeenCalled()
      }
      finally {
        dispose()
      }
    })
  })
})

/**
 * Closing a tab that belongs to a workspace other than the one on screen.
 *
 * This used to be a genuine special case with a silent-failure bug: the tab
 * existed ONLY in `workspaceStoreRegistry`'s frozen snapshot for workspace B,
 * so `tabStore.removeTab` (which filtered the ACTIVE store by id) found
 * nothing and merely emitted the tombstone, while `handleAgentClose` resolved
 * the worker id from the active store and skipped the close RPC entirely. The
 * agent kept running and the sidebar kept rendering the stale snapshot row.
 *
 * Now every workspace is in the one projection, so the tab is fully visible and
 * the tombstone removes it everywhere. What survives is narrower: the
 * agent/terminal helpers drive the ACTIVE workspace's worker context, so a tab
 * from elsewhere still needs its close RPC addressed to its own worker.
 */
describe('useTabOperations.handleTabClose cross-workspace', () => {
  afterEach(() => {
    mockInspectLastTabClose.mockReset()
    mockInspectLastTabClose.mockImplementation(() => Promise.resolve({ shouldPrompt: false } as unknown))
    mockCloseAgent.mockReset()
    mockCloseTerminal.mockReset()
    mockRevokeTabPayload.mockReset()
    mockRevokeTabPayload.mockImplementation(() => Promise.resolve({}))
  })

  /**
   * Wire ops for a client viewing `ws-active`, with one tab seeded into a
   * DIFFERENT workspace's tile. Both workspaces live in the same projection —
   * which is the point — so the seeded tab is readable through `view`.
   */
  function setupCrossWorkspace(type: TabType, id: string, meta: Record<string, unknown> = {}) {
    const harness = installTestBridge({ workspaceId: 'ws-other', rootTileId: 'tile-cross' })
    const stores = createTestTabStores('ws-other')
    const chatStore = createChatStore()
    const handleAgentClose = vi.fn()
    const handleTerminalClose = vi.fn()

    emitAddTab({ type, id, tileId: harness.rootTileId, position: 'a', workerId: 'w-other' })
    if (Object.keys(meta).length > 0)
      stores.metadata.patch(id, meta)

    const ops = useTabOperations({
      view: stores.view,
      metadata: stores.metadata,
      selection: stores.selection,
      chatStore,
      layoutStore: stores.layoutStore,
      agentOps: { handleAgentClose } as never,
      termOps: { handleTerminalClose } as never,
      activeTab: () => stores.selection.activeTabForWorkspace('ws-active'),
      getCurrentTabContext: () => ({ workerId: 'w-1', workingDir: '/tmp', homeDir: '/home/test', gitToplevel: '' }),
      focusEditor: vi.fn(),
      getScrollState: () => ({ atBottom: false, hasMoreNewer: false }),
      setFileTreePath: vi.fn(),
      // The client is on ws-active; the seeded tab is on ws-other.
      getActiveWorkspaceId: () => 'ws-active',
      workerOnlineState: () => true,
      repoGitStore: createRepoGitStore(),
    })

    const tab = stores.view.getById(type, id)!
    expect(tab, 'the other workspace\'s tab is visible in the shared projection').toBeTruthy()
    expect(tab.workspaceId).toBe('ws-other')
    return { ...stores, ops, tab, handleAgentClose, handleTerminalClose }
  }

  /**
   * A tab in an inactive workspace closes through the SHARED handler, not a
   * bespoke RPC call. That is the fix, and the assertion: the bespoke branch
   * skipped `clearAgent` / `clearAttachments` / `chatStore.forgetAgent`, so
   * closing an agent tab in an inactive workspace stranded its loaded window,
   * live tail, command streams and span index. Routing through the handler is
   * what makes those steps unskippable.
   */
  it('closes an agent in another workspace through the shared handler', async () => {
    await createRoot(async (dispose) => {
      const { ops, tab, handleAgentClose } = setupCrossWorkspace(TabType.AGENT, 'agent-cross')
      mockInspectLastTabClose.mockResolvedValueOnce({ shouldPrompt: false })

      expect(await ops.handleTabClose(tab)).toBe(true)

      expect(handleAgentClose).toHaveBeenCalledWith('agent-cross', WorktreeAction.KEEP)
      // And NOT by hand: a direct RPC here is exactly the duplicate that drifted.
      expect(mockCloseAgent).not.toHaveBeenCalled()
      // No tombstone assertion: emitting it is the shared handler's job, and the
      // handler is stubbed here. Asserting it would only check that the stub was
      // told to do something. Routing IS the property under test -- the old
      // branch emitted the tombstone itself, which is precisely why it could skip
      // the handler's other cleanup steps unnoticed.
      dispose()
    })
  })

  // Terminal half of the same contract. The bespoke branch skipped
  // `disposeTerminalInstance`, which after a cross-workspace move is the last
  // chance to reclaim the terminal's pooled WebGL slot.
  it('closes a terminal in another workspace through the shared handler', async () => {
    await createRoot(async (dispose) => {
      const { ops, tab, handleTerminalClose } = setupCrossWorkspace(TabType.TERMINAL, 'term-cross')
      mockInspectLastTabClose.mockResolvedValueOnce({ shouldPrompt: false })

      expect(await ops.handleTabClose(tab)).toBe(true)

      expect(handleTerminalClose).toHaveBeenCalledWith('term-cross', WorktreeAction.KEEP)
      expect(mockCloseTerminal).not.toHaveBeenCalled()
      dispose()
    })
  })

  it('closes a FILE tab in another workspace via revokeTabPayload', async () => {
    await createRoot(async (dispose) => {
      const { ops, tab, view, handleAgentClose, handleTerminalClose }
        = setupCrossWorkspace(TabType.FILE, 'file-cross', { filePath: '/repo/x.md' })
      mockInspectLastTabClose.mockResolvedValueOnce({ shouldPrompt: false })

      expect(await ops.handleTabClose(tab)).toBe(true)

      // FILE follows the same contract as AGENT / TERMINAL: bypass the
      // active-workspace handlers, fire the worker RPC with the tab's own
      // workerId.
      expect(handleAgentClose).not.toHaveBeenCalled()
      expect(handleTerminalClose).not.toHaveBeenCalled()
      expect(mockRevokeTabPayload).toHaveBeenCalledTimes(1)
      expect(mockRevokeTabPayload.mock.calls[0][0]).toBe('w-other')
      expect(mockRevokeTabPayload.mock.calls[0][1]).toMatchObject({
        tabId: 'file-cross',
        worktreeAction: WorktreeAction.KEEP,
      })
      expect(view.getById(TabType.FILE, 'file-cross')).toBeUndefined()
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
    mockRevokeTabPayload.mockReset()
    mockRevokeTabPayload.mockImplementation(() => Promise.resolve({}))
  })

  it('moves focusedTileId to the surviving active tab\'s tile when the focused tile empties', async () => {
    await createRoot(async (dispose) => {
      installTestBridge({ rootTileId: 'root-leaf' })
      const stores = createTestTabStores('ws-test')
      const { view, metadata, selection, layoutStore } = stores
      const chatStore = createChatStore()

      // Real split so both tile ids exist in the projected tree.
      // `containsTileId` matches LEAF nodes only, and `useFocusInvariant`
      // (a layer above the layout store, which deliberately does not enforce
      // this itself) resets focus to firstLeaf when the focused tile id isn't
      // a live leaf. Use the actual leaf ids produced by `splitTile`, not the
      // pre-split root id.
      const otherTileId = layoutStore.splitTile('root-leaf', 'horizontal')!
      const [tileA, tileB] = layoutStore.getAllTileIds()
      // The split keeps both children as leaves; we just need to know
      // which one is the new childB so we can target the other.
      const focusTile = tileB === otherTileId ? tileA : tileB
      const otherLeafTile = otherTileId

      addFile(stores, 'file-a', focusTile, '/a')
      addFile(stores, 'file-b', otherLeafTile, '/b')
      layoutStore.setFocusedTile(focusTile)

      const ops = useTabOperations({
        view,
        metadata,
        selection,
        chatStore,
        layoutStore,
        agentOps: { handleAgentClose: vi.fn() } as never,
        termOps: { handleTerminalClose: vi.fn() } as never,
        activeTab: () => selection.activeTabForWorkspace('ws-test'),
        getCurrentTabContext: () => ({ workerId: 'w-1', workingDir: '/tmp', homeDir: '/home/test', gitToplevel: '' }),
        focusEditor: vi.fn(),
        getScrollState: () => ({ atBottom: false, hasMoreNewer: false }),
        setFileTreePath: vi.fn(),
        getActiveWorkspaceId: () => 'ws-test',
        workerOnlineState: () => true,
        repoGitStore: createRepoGitStore(),
      })

      await ops.handleTabClose(view.getById(TabType.FILE, 'file-a')!)

      // Focus follows the surviving tab. Asserted against file-b's CURRENT
      // tile rather than the id captured before the close: emptying one child
      // of the split leaves it single-child, and the projection collapses it,
      // re-keying the survivor to the SPLIT's node id.
      const survivingTile = view.getById(TabType.FILE, 'file-b')?.tileId
      expect(survivingTile).toBeTruthy()
      expect(layoutStore.focusedTileId()).toBe(survivingTile)
      dispose()
    })
  })

  it('leaves focus alone when other tabs remain on the focused tile', async () => {
    await createRoot(async (dispose) => {
      installTestBridge({ rootTileId: 'root-leaf' })
      const stores = createTestTabStores('ws-test')
      const { view, metadata, selection, layoutStore } = stores
      const chatStore = createChatStore()

      const otherTileId = layoutStore.splitTile('root-leaf', 'horizontal')!
      const [tileA, tileB] = layoutStore.getAllTileIds()
      const focusTile = tileB === otherTileId ? tileA : tileB
      const otherLeafTile = otherTileId

      addFile(stores, 'file-a', focusTile, '/a')
      addFile(stores, 'file-b', focusTile, '/b')
      addFile(stores, 'file-c', otherLeafTile, '/c')
      layoutStore.setFocusedTile(focusTile)

      const ops = useTabOperations({
        view,
        metadata,
        selection,
        chatStore,
        layoutStore,
        agentOps: { handleAgentClose: vi.fn() } as never,
        termOps: { handleTerminalClose: vi.fn() } as never,
        activeTab: () => selection.activeTabForWorkspace('ws-test'),
        getCurrentTabContext: () => ({ workerId: 'w-1', workingDir: '/tmp', homeDir: '/home/test', gitToplevel: '' }),
        focusEditor: vi.fn(),
        getScrollState: () => ({ atBottom: false, hasMoreNewer: false }),
        setFileTreePath: vi.fn(),
        getActiveWorkspaceId: () => 'ws-test',
        workerOnlineState: () => true,
        repoGitStore: createRepoGitStore(),
      })

      await ops.handleTabClose(view.getById(TabType.FILE, 'file-a')!)

      // focusTile still has file-b, focus stays.
      expect(layoutStore.focusedTileId()).toBe(focusTile)
      dispose()
    })
  })
})

// --- Floating-window auto-cleanup ---
//
// We use FILE tabs to drive these tests because handleTabClose tombstones
// FILE tabs synchronously, so the removeIfEmpty cleanup runs against real
// projected state. AGENT/TERMINAL closes go through agentOps/termOps mocks
// that emit nothing.

function setupWithFloatingWindow() {
  // The default test bridge already seeds 'main-tile' as the root.
  // We rely on that to keep `mainTileId` stable across this test.
  const stores = createTestTabStores('ws-test')
  const { view, metadata, selection, layoutStore } = stores
  const chatStore = createChatStore()
  const floatingWindowStore = createTestFloatingWindowStore()

  const mainTileId = 'main-tile'
  layoutStore.setFocusedTile(mainTileId)

  const handleAgentClose = vi.fn()
  const handleTerminalClose = vi.fn()

  const ops = useTabOperations({
    view,
    metadata,
    selection,
    chatStore,
    layoutStore,
    floatingWindowStore,
    agentOps: { handleAgentClose } as never,
    termOps: { handleTerminalClose } as never,
    activeTab: () => selection.activeTabForWorkspace('ws-test'),
    getCurrentTabContext: () => ({ workerId: 'w-1', workingDir: '/tmp', homeDir: '/home/test', gitToplevel: '' }),
    focusEditor: vi.fn(),
    getScrollState: () => ({ atBottom: false, hasMoreNewer: false }),
    setFileTreePath: vi.fn(),
    getActiveWorkspaceId: () => 'ws-test',
    workerOnlineState: () => true,
    repoGitStore: createRepoGitStore(),
  })

  return {
    ...stores,
    floatingWindowStore,
    ops,
    mainTileId,
    addFile: (id: string, onTile: string, filePath: string) => addFile(stores, id, onTile, filePath),
    addAgent: (id: string, onTile: string) => addAgent(stores, id, onTile),
  }
}

describe('useTabOperations.handleTabClose floating-window cleanup', () => {
  afterEach(() => {
    mockInspectLastTabClose.mockReset()
    mockInspectLastTabClose.mockImplementation(() => Promise.resolve({ shouldPrompt: false } as unknown))
    mockRevokeTabPayload.mockReset()
    mockRevokeTabPayload.mockImplementation(() => Promise.resolve({}))
  })

  it('closing the last FILE tab in a single-tile floating window auto-removes the window', async () => {
    await createRoot(async (dispose) => {
      try {
        const { view, floatingWindowStore, ops, addFile } = setupWithFloatingWindow()
        // The test bridge is installed, so addWindow() always returns a window.
        const { windowId, tileId } = floatingWindowStore.addWindow()!
        addFile('f1', tileId, '/a.txt')

        expect(floatingWindowStore.state.windows).toHaveLength(1)
        const tab = view.getById(TabType.FILE, 'f1')!

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
        const { view, floatingWindowStore, ops, addFile } = setupWithFloatingWindow()
        // The test bridge is installed, so addWindow() always returns a window.
        const { windowId, tileId } = floatingWindowStore.addWindow()!
        // Splitting flips the pre-split tile's kind from LEAF to SPLIT and
        // mints two new leaves, so `tileId` is no longer somewhere a tab can
        // live -- take both leaf ids from the window after the split.
        const newTileId = floatingWindowStore.splitTile(windowId, tileId, 'horizontal')!
        const leaves = [...floatingWindowStore.getWindowTileIdSet(windowId) ?? []]
        const otherLeaf = leaves.find(id => id !== newTileId)!
        addFile('f1', otherLeaf, '/a.txt')
        addFile('f2', newTileId, '/b.txt')

        const tab = view.getById(TabType.FILE, 'f1')!
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
        const { view, floatingWindowStore, ops, addFile, mainTileId } = setupWithFloatingWindow()
        // Add an unrelated empty floating window — it should NOT be touched
        // by closes against the main layout.
        floatingWindowStore.addWindow()
        const startWindowCount = floatingWindowStore.state.windows.length
        addFile('main-file', mainTileId, '/a.txt')

        const tab = view.getById(TabType.FILE, 'main-file')!
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
    mockRevokeTabPayload.mockReset()
    mockRevokeTabPayload.mockImplementation(() => Promise.resolve({}))
    mockCloseAgent.mockImplementation(() => Promise.resolve({ result: undefined }))
    mockCloseTerminal.mockImplementation(() => Promise.resolve({ result: undefined }))
  })

  it('dispatches AGENT tab to agentOps.handleAgentClose with the supplied action', () => {
    createRoot((dispose) => {
      const { view, ops, handleAgentClose, handleTerminalClose } = setup()
      const agent = view.getById(TabType.AGENT, 'agent-a')!

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
      const { view, ops, handleAgentClose } = setup()
      const agent = view.getById(TabType.AGENT, 'agent-a')!

      ops.closeTabWithAction(agent, WorktreeAction.REMOVE)

      expect(handleAgentClose).toHaveBeenCalledTimes(1)
      expect(ops.closingTabKeys().has(`${TabType.AGENT}:agent-a`)).toBe(false)
      dispose()
    })
  })

  it('dispatches TERMINAL tab to termOps.handleTerminalClose with the supplied action', () => {
    createRoot((dispose) => {
      const { view, ops, handleAgentClose, handleTerminalClose, addTerminal } = setup()
      addTerminal('term-1')
      const term = view.getById(TabType.TERMINAL, 'term-1')!

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
      const { view, ops, handleAgentClose, handleTerminalClose, addFile } = setup()
      addFile('f1', '/x')
      const file = view.getById(TabType.FILE, 'f1')!

      ops.closeTabWithAction(file, WorktreeAction.REMOVE)

      expect(handleAgentClose).not.toHaveBeenCalled()
      expect(handleTerminalClose).not.toHaveBeenCalled()
      expect(view.all().some(t => t.id === 'f1')).toBe(false)
      dispose()
    })
  })

  it('removes the parent floating window when closing its last tab', async () => {
    await createRoot(async (dispose) => {
      try {
        const { view, floatingWindowStore, ops, addAgent } = setupWithFloatingWindow()
        // The test bridge is installed, so addWindow() always returns a window.
        const { windowId, tileId } = floatingWindowStore.addWindow()!
        // Add an AGENT tab to the floating tile; the floating window
        // now contains exactly one tab in one tile.
        addAgent('agent-float', tileId)

        // Simulate the worker close having already torn the tab off the
        // store (agentOps.handleAgentClose is mocked here, so we must
        // remove it ourselves to model the post-close state).
        const tab = view.getById(TabType.AGENT, 'agent-float')!
        emitRemoveTab(TabType.AGENT, 'agent-float')

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
      const { view, ops, selection, layoutStore, homeTileId, otherTileId } = setupForFocusMigration()
      // The home tile has agent-a (active); the other tile has agent-other.
      // Focus starts on the home tile; after closing agent-a (plus the
      // tombstone the worker close would have emitted) the home tile is empty
      // and focus must follow the active tab to the other one.
      const closed = view.getById(TabType.AGENT, 'agent-a')!
      selection.setActiveById(TabType.AGENT, 'agent-other')
      emitRemoveTab(TabType.AGENT, 'agent-a')
      layoutStore.setFocusedTile(homeTileId)

      ops.closeTabWithAction(closed, WorktreeAction.REMOVE)

      expect(layoutStore.focusedTileId()).toBe(otherTileId)
      dispose()
    })
  })

  it('closeWorktreeTabsAndReport toasts the worker REMOVED outcome', async () => {
    await createRoot(async (dispose) => {
      const { view, ops, handleAgentClose } = setup()
      handleAgentClose.mockResolvedValue({ worktreeRemoval: WorktreeRemovalOutcome.REMOVED })
      const agent = view.getById(TabType.AGENT, 'agent-a')!

      ops.closeWorktreeTabsAndReport([agent], WorktreeAction.REMOVE, true)
      await settle()

      expect(handleAgentClose).toHaveBeenCalledWith('agent-a', WorktreeAction.REMOVE)
      expect(mockShowInfoToast).toHaveBeenCalledWith('Worktree removed')
      dispose()
    })
  })

  it('closeWorktreeTabsAndReport reports a tab with no definitive result as unconfirmed', async () => {
    // awaitCloseResult resolves undefined when the close RPC rejects; the
    // fold must treat that as "outcome unknown" — NOT a clean no-op — so the
    // report says it could not confirm the removal rather than "not removed".
    await createRoot(async (dispose) => {
      const { view, ops, handleAgentClose } = setup()
      handleAgentClose.mockResolvedValue(undefined)
      const agent = view.getById(TabType.AGENT, 'agent-a')!

      ops.closeWorktreeTabsAndReport([agent], WorktreeAction.REMOVE, true)
      await settle()

      expect(mockShowInfoToast).toHaveBeenCalledWith('Could not confirm the worktree removal')
      dispose()
    })
  })

  it('closeWorktreeTabsAndReport lets a definitive REMOVED win over a sibling with no result', async () => {
    // The fold is an OR across the whole group, not a single per-group verdict:
    // one tab's close removed the worktree while another's was indeterminate
    // (rejected RPC). Both flags are recorded, and the reporter's precedence
    // (removed wins) then decides the toast.
    await createRoot(async (dispose) => {
      const { view, ops, handleAgentClose, addAgent } = setup()
      handleAgentClose
        .mockResolvedValueOnce({ worktreeRemoval: WorktreeRemovalOutcome.REMOVED })
        .mockResolvedValueOnce(undefined)
      addAgent('agent-b')
      const a = view.getById(TabType.AGENT, 'agent-a')!
      const b = view.getById(TabType.AGENT, 'agent-b')!

      ops.closeWorktreeTabsAndReport([a, b], WorktreeAction.REMOVE, true)
      await settle()

      expect(mockShowInfoToast).toHaveBeenCalledWith('Worktree removed')
      dispose()
    })
  })

  it('closeWorktreeTabsAndReport with KEEP closes the tabs and states the worktree stays', async () => {
    // The Delete branch dialog's escape hatch for a refused removal. KEEP asks
    // for no removal, so there is no removal outcome to report -- saying
    // "Worktree not removed" here would read as a failure rather than the
    // choice the user made.
    await createRoot(async (dispose) => {
      const { view, ops, handleAgentClose } = setup()
      handleAgentClose.mockResolvedValue({ worktreeRemoval: WorktreeRemovalOutcome.UNSPECIFIED })
      const agent = view.getById(TabType.AGENT, 'agent-a')!

      ops.closeWorktreeTabsAndReport([agent], WorktreeAction.KEEP, false)
      await settle()

      expect(handleAgentClose).toHaveBeenCalledWith('agent-a', WorktreeAction.KEEP)
      expect(mockShowInfoToast).toHaveBeenCalledWith('Tabs closed; worktree kept on disk')
      dispose()
    })
  })

  /**
   * `closeTabWithAction` skips the inspect+confirm prompt (the delete-branch
   * flow already asked once for the whole group), but takes the same
   * cross-workspace branch as `handleTabClose`: the RPC is addressed to the
   * tab's own worker and workspace rather than the active one's.
   *
   * DeleteBranchDialog opened against another workspace's branch row is how
   * this is reached. Before the projection unified them, the tab existed only
   * in that workspace's registry snapshot, `agentOps.handleAgentClose`
   * resolved its worker from the ACTIVE store, found nothing, and the agent
   * survived the "delete branch" it was supposed to be closed by.
   */
  function setupCross(type: TabType, id: string, meta: Record<string, unknown> = {}) {
    const harness = installTestBridge({ workspaceId: 'ws-other', rootTileId: 'tile-cross' })
    const stores = createTestTabStores('ws-other')
    const chatStore = createChatStore()
    const handleAgentClose = vi.fn()
    const handleTerminalClose = vi.fn()

    emitAddTab({ type, id, tileId: harness.rootTileId, position: 'a', workerId: 'w-cross' })
    if (Object.keys(meta).length > 0)
      stores.metadata.patch(id, meta)

    const ops = useTabOperations({
      view: stores.view,
      metadata: stores.metadata,
      selection: stores.selection,
      chatStore,
      layoutStore: stores.layoutStore,
      agentOps: { handleAgentClose } as never,
      termOps: { handleTerminalClose } as never,
      activeTab: () => stores.selection.activeTabForWorkspace('ws-active'),
      getCurrentTabContext: () => ({ workerId: 'w-active', workingDir: '/tmp', homeDir: '/home/test', gitToplevel: '' }),
      focusEditor: vi.fn(),
      getScrollState: () => ({ atBottom: false, hasMoreNewer: false }),
      setFileTreePath: vi.fn(),
      getActiveWorkspaceId: () => 'ws-active',
      workerOnlineState: () => true,
      repoGitStore: createRepoGitStore(),
    })
    return { ...stores, ops, tab: stores.view.getById(type, id)!, handleAgentClose, handleTerminalClose }
  }

  it('cross-workspace AGENT: routes through the shared handler and tombstones the tab', async () => {
    await createRoot(async (dispose) => {
      const { ops, tab, handleAgentClose } = setupCross(TabType.AGENT, 'agent-cross')

      ops.closeTabWithAction(tab, WorktreeAction.KEEP)

      expect(handleAgentClose).toHaveBeenCalledWith('agent-cross', WorktreeAction.KEEP)
      expect(mockCloseAgent).not.toHaveBeenCalled()
      dispose()
    })
  })

  it('cross-workspace TERMINAL: routes through the shared handler', async () => {
    await createRoot(async (dispose) => {
      const { ops, tab, handleTerminalClose } = setupCross(TabType.TERMINAL, 'term-cross')

      ops.closeTabWithAction(tab, WorktreeAction.REMOVE)

      expect(handleTerminalClose).toHaveBeenCalledWith('term-cross', WorktreeAction.REMOVE)
      expect(mockCloseTerminal).not.toHaveBeenCalled()
      dispose()
    })
  })

  it('cross-workspace FILE: revokes the path and skips the agent/terminal handlers', async () => {
    await createRoot(async (dispose) => {
      const { ops, tab, view, handleAgentClose, handleTerminalClose }
        = setupCross(TabType.FILE, 'file-cross', { filePath: '/repo/x.md' })

      ops.closeTabWithAction(tab, WorktreeAction.KEEP)

      expect(handleAgentClose).not.toHaveBeenCalled()
      expect(handleTerminalClose).not.toHaveBeenCalled()
      expect(mockRevokeTabPayload).toHaveBeenCalledTimes(1)
      expect(mockRevokeTabPayload.mock.calls[0][0]).toBe('w-cross')
      expect(mockRevokeTabPayload.mock.calls[0][1]).toMatchObject({
        tabId: 'file-cross',
        worktreeAction: WorktreeAction.KEEP,
      })
      expect(view.getById(TabType.FILE, 'file-cross')).toBeUndefined()
      dispose()
    })
  })
})

// Variant of setup() that exposes the layoutStore so focus-migration
// behavior is directly assertable. The shared setup() seeds two AGENT
// tabs on tile-1 the same way; this variant adds a tile-other tab so
// migrateFocusAfterTabClose has somewhere to move focus to.
function setupForFocusMigration() {
  installTestBridge({ rootTileId: 'root-leaf' })
  const stores = createTestTabStores('ws-test')
  const { view, metadata, selection, layoutStore } = stores
  const chatStore = createChatStore()

  // Two real leaves. A tab parked on an id absent from the projected tree
  // never resolves into the view, so the focus target has to actually exist.
  const otherTileId = layoutStore.splitTile('root-leaf', 'horizontal')!
  const [tileA, tileB] = layoutStore.getAllTileIds()
  const homeTileId = tileB === otherTileId ? tileA : tileB
  layoutStore.setFocusedTile(homeTileId)

  addAgent(stores, 'agent-a', homeTileId)
  addAgent(stores, 'agent-other', otherTileId)
  selection.setActiveById(TabType.AGENT, 'agent-a')

  const handleAgentClose = vi.fn()
  const handleTerminalClose = vi.fn()
  const ops = useTabOperations({
    view,
    metadata,
    selection,
    chatStore,
    layoutStore,
    agentOps: { handleAgentClose } as never,
    termOps: { handleTerminalClose } as never,
    activeTab: () => selection.activeTabForWorkspace('ws-test'),
    getCurrentTabContext: () => ({ workerId: 'w-1', workingDir: '/tmp', homeDir: '/home/test', gitToplevel: '' }),
    focusEditor: vi.fn(),
    getScrollState: () => ({ atBottom: false, hasMoreNewer: false }),
    setFileTreePath: vi.fn(),
    getActiveWorkspaceId: () => 'ws-test',
    workerOnlineState: () => true,
    repoGitStore: createRepoGitStore(),
  })
  return { ...stores, ops, homeTileId, otherTileId, handleAgentClose, handleTerminalClose }
}
