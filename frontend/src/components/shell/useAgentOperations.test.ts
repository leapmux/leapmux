import type { CloseAgentResponse } from '~/generated/proto/leapmux/v1/agent_pb'
import type { Workspace } from '~/generated/proto/leapmux/v1/workspace_pb'
import type { ControlRequest } from '~/stores/control.store'

import { create } from '@bufbuild/protobuf'
import { Code } from '@connectrpc/connect'
import { createRoot } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import * as workerRpc from '~/api/workerRpc'
import { useAgentOperations } from '~/components/shell/useAgentOperations'
import { AgentInfoSchema, AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { GitRepoStatusSchema, WorktreeAction } from '~/generated/proto/leapmux/v1/common_pb'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { KEY_MRU_AGENT_PROVIDERS, localStorageClearForTests, localStorageGet, localStorageSet } from '~/lib/browserStorage'
import { ChannelError, channelNotOpenError } from '~/lib/channelError'
import { createAgentSessionStore } from '~/stores/agentSession.store'
import { createControlStore } from '~/stores/control.store'
import { repoKey } from '~/stores/repoGit'
import { createRepoGitStore } from '~/stores/repoGit.store'
import { protoToAgentTabFields } from '~/stores/tab.helpers'
import { emitAddTab } from '~/stores/tabOps'
import { deferred, flush } from '~/test-support/async'
import { installTestBridge } from '~/test-support/crdtBridge'
import { createTestTabStores } from '~/test-support/tabStores'

const mockCloseAgent = vi.fn<(workerId: string, req: { agentId: string, worktreeAction?: WorktreeAction }) => Promise<CloseAgentResponse>>()
const mockOpenAgent = vi.fn()
const mockSendAgentRawMessage = vi.fn()
const mockInterruptAgent = vi.fn()
const mockUpdateAgentSettings = vi.fn()
const mockListAvailableProviders = vi.fn().mockResolvedValue({ providers: [] })
const mockShowWarnToast = vi.fn()

vi.mock('~/api/workerRpc', async importOriginal => ({
  ...await importOriginal<typeof import('~/api/workerRpc')>(),
  closeAgent: (...args: unknown[]) => mockCloseAgent(...args as [string, { agentId: string, worktreeAction?: WorktreeAction }]),
  openAgent: (...args: unknown[]) => mockOpenAgent(...args),
  sendAgentRawMessage: (...args: unknown[]) => mockSendAgentRawMessage(...args),
  interruptAgent: (...args: unknown[]) => mockInterruptAgent(...args),
  sendControlResponse: vi.fn(),
  updateAgentSettings: (...args: unknown[]) => mockUpdateAgentSettings(...args),
  listAvailableProviders: (...args: unknown[]) => mockListAvailableProviders(...args),
}))

vi.mock('~/api/clients', () => ({
  workspaceClient: {
    addTab: vi.fn().mockResolvedValue({}),
    removeTab: vi.fn().mockResolvedValue({}),
  },
}))

vi.mock('~/components/common/Toast', async () => {
  // The disconnect-aware helper keeps its REAL rule and reports through the same
  // spy: what these tests care about is whether the user was told, and a stub
  // that always forwarded would pass whether the rule held or not.
  const { isDisconnectError } = await import('~/api/workerErrors')
  return {
    showWarnToast: (...args: unknown[]) => mockShowWarnToast(...args),
    showWarnToastUnlessDisconnected: (message: string, err: unknown) => {
      if (!isDisconnectError(err))
        mockShowWarnToast(message, err)
    },
  }
})

// A throwaway store for the fixture mapper. `protoToAgentTabFields` writes the
// repo entry that matches the `gitToplevel` it returns, so it takes one; these
// fixtures only want the row fields.
const fixtureStore = createRepoGitStore()

let nextPosition = 0

/**
 * Place an agent tab: placement via the op path, everything else into
 * metadata. `protoToAgentTabFields` returns a mix of both, so it is split at
 * the call boundary here rather than in each test.
 */
function addAgent(
  stores: ReturnType<typeof createTestTabStores>,
  tileId: string,
  fields: { id: string } & Record<string, unknown>,
) {
  const { id, type: _type, tileId: _tileId, position: _position, workerId, ...meta } = fields as Record<string, any>
  nextPosition += 1
  emitAddTab({
    type: TabType.AGENT,
    id,
    tileId,
    position: `p${nextPosition}`,
    workerId: workerId ?? '',
  })
  stores.metadata.patch(id, meta)
  // The old `tabStore.addTab` activated by default, and several tests here
  // depend on the newest tab being the active one (quick-create reads the
  // active agent's provider).
  stores.selection.setActiveById(TabType.AGENT, id)
}

/**
 * `storeWorkspaceId` keys the stores the shell answers for. The bridge
 * always delivers ws-1's tree, so a DIFFERENT id produces the
 * never-bootstrapped wedge (no projected tree for the active workspace).
 */
function setup(storeWorkspaceId: string = 'ws-1', getWorkerId: () => string = () => 'w-1') {
  const agentSessionStore = createAgentSessionStore()
  const controlStore = createControlStore()
  const harness = installTestBridge({ workspaceId: 'ws-1' })
  const stores = createTestTabStores(storeWorkspaceId)

  const chatStore = {
    getMessages: vi.fn().mockReturnValue([]),
    forgetAgent: vi.fn(),
    clearToolProgress: vi.fn(),
    streamingText: { clear: vi.fn() },
  } as any

  const repoGitStore = createRepoGitStore()
  const agentInputQueueStore = { clearAgent: vi.fn() } as any
  const ops = useAgentOperations({
    agentSessionStore,
    agentInputQueueStore,
    chatStore,
    controlStore,
    view: stores.view,
    metadata: stores.metadata,
    selection: stores.selection,
    getActiveWorkspaceId: () => storeWorkspaceId,
    layoutStore: stores.layoutStore,
    settingsLoading: { start: vi.fn(), stop: vi.fn() },
    isActiveWorkspaceMutatable: () => true,
    activeWorkspace: () => ({ id: storeWorkspaceId } as Workspace),
    getCurrentTabContext: () => ({ workerId: getWorkerId(), workingDir: '/tmp' }),
    newAgentDialog: { open: vi.fn(), close: vi.fn(), value: () => null },
    setNewAgentLoadingProvider: vi.fn(),
    repoGitStore,
  })

  return {
    ...stores,
    harness,
    agentSessionStore,
    controlStore,
    chatStore,
    agentInputQueueStore,
    repoGitStore,
    ops,
    /** Place an agent on the seeded root tile — the only tile that exists. */
    add: (fields: { id: string } & Record<string, unknown>) =>
      addAgent(stores, harness.rootTileId, fields),
    /** Same, for a TERMINAL tab (the non-agent active-tab path). */
    addTerminal: (id: string, meta: Record<string, unknown> = {}) => {
      nextPosition += 1
      emitAddTab({
        type: TabType.TERMINAL,
        id,
        tileId: harness.rootTileId,
        position: `p${nextPosition}`,
        workerId: (meta.workerId as string | undefined) ?? '',
      })
      stores.metadata.patch(id, meta)
      stores.selection.setActiveById(TabType.TERMINAL, id)
    },
  }
}

describe('useAgentOperations', () => {
  beforeEach(() => {
    mockOpenAgent.mockReset()
    mockListAvailableProviders.mockReset()
    mockListAvailableProviders.mockResolvedValue({ providers: [] })
    localStorageClearForTests()
  })

  describe('handleOpenAgent', () => {
    it('uses the active agent tab provider for quick create', async () => {
      await createRoot(async (dispose) => {
        try {
          mockListAvailableProviders.mockResolvedValue({ providers: [AgentProvider.CLAUDE_CODE, AgentProvider.CODEX] })
          mockOpenAgent.mockResolvedValue({
            agent: create(AgentInfoSchema, {
              id: 'new-agent',
              workerId: 'w-1',
              workingDir: '/tmp',
              agentProvider: AgentProvider.CODEX,
            }),
          })
          localStorageSet(KEY_MRU_AGENT_PROVIDERS, [AgentProvider.CLAUDE_CODE])

          const { ops, add } = setup()
          add({
            id: 'active-agent',
            workerId: 'w-1',
            workingDir: '/tmp',
            agentProvider: AgentProvider.CODEX,
          })

          await flush()
          await ops.handleOpenAgent()

          expect(mockOpenAgent).toHaveBeenCalledWith('w-1', expect.objectContaining({
            agentProvider: AgentProvider.CODEX,
            workingDir: '/tmp',
          }))
        }
        finally {
          dispose()
        }
      })
    })

    // The branch context menu states WHERE the agent runs. Without the
    // override, an agent started from a branch row on another machine would
    // open on whichever worker the focused tab happens to sit on.
    it('opens on the target\'s worker and directory when one is given', async () => {
      await createRoot(async (dispose) => {
        try {
          mockListAvailableProviders.mockResolvedValue({ providers: [AgentProvider.CLAUDE_CODE] })
          mockOpenAgent.mockResolvedValue({
            agent: create(AgentInfoSchema, { id: 'new-agent', workerId: 'w-2', workingDir: '/other/worktree' }),
          })

          const { ops } = setup()
          await flush()
          // The fixture's tab context is w-1 at /tmp, so a leak from it would
          // be visible in either field.
          await ops.handleOpenAgent(AgentProvider.CLAUDE_CODE, { workerId: 'w-2', workingDir: '/other/worktree' })

          expect(mockOpenAgent).toHaveBeenCalledWith('w-2', expect.objectContaining({
            workerId: 'w-2',
            workingDir: '/other/worktree',
          }))
        }
        finally {
          dispose()
        }
      })
    })

    it('falls back to the current tab context when no target is given', async () => {
      await createRoot(async (dispose) => {
        try {
          mockListAvailableProviders.mockResolvedValue({ providers: [AgentProvider.CLAUDE_CODE] })
          mockOpenAgent.mockResolvedValue({
            agent: create(AgentInfoSchema, { id: 'new-agent', workerId: 'w-1', workingDir: '/tmp' }),
          })

          const { ops } = setup()
          await flush()
          await ops.handleOpenAgent(AgentProvider.CLAUDE_CODE)

          expect(mockOpenAgent).toHaveBeenCalledWith('w-1', expect.objectContaining({ workingDir: '/tmp' }))
        }
        finally {
          dispose()
        }
      })
    })

    /**
     * What a freshly opened agent really shows in the sidebar.
     *
     * The OpenAgent response carries NO git status -- the worker computes it in
     * startup phase 1 and sends it on the STARTING broadcast, so the
     * `git status` shell-out does not block the RPC
     * (`TestOpenAgent_ResponseHasNilGitStatus`). The row therefore gets no
     * `gitToplevel` from the response, and the optimistic seed is the tab's
     * only repo identity until the broadcast lands.
     *
     * That seed comes from the ACTIVE tab, which can be on a DIFFERENT worker:
     * `resolveOptimisticGitInfo` compares working directories, never worker
     * ids. So the new tab shows the other machine's branch for the length of
     * the agent's startup. This test states that, so a change to it is a
     * decision and not an accident.
     */
    it('shows the active tab\'s branch until the worker reports, even across workers', async () => {
      await createRoot(async (dispose) => {
        try {
          mockListAvailableProviders.mockResolvedValue({ providers: [AgentProvider.CLAUDE_CODE] })
          mockOpenAgent.mockResolvedValue({
            agent: create(AgentInfoSchema, {
              id: 'new-agent',
              workerId: 'w-1',
              // Matched to the active tab's dir on purpose: `resolveOptimisticGitInfo`
              // compares the RESPONSE's working dir against the active tab's, and
              // refuses to copy anything when they differ.
              workingDir: '/repo',
              agentProvider: AgentProvider.CLAUDE_CODE,
              // No gitStatus: the shape the worker really sends.
            }),
          })

          const { ops, view, add, repoGitStore } = setup()
          add({ id: 'active', workerId: 'w-other', workingDir: '/repo', gitToplevel: '/repo' })
          repoGitStore.upsert(repoKey('w-other', '/repo'), {
            workerId: 'w-other',
            toplevel: '/repo',
            branch: 'on-another-machine',
            gitStatusSeen: true,
          })

          await flush()
          await ops.handleOpenAgent()
          await flush()

          const tab = view.getAgentTab('new-agent')
          expect(tab?.gitToplevel, 'the row identity comes from the seed, not the response').toBe('/repo')
          expect(
            repoGitStore.get(repoKey('w-1', '/repo'))?.branch,
            'the guess stands in until the STARTING broadcast replaces it',
          ).toBe('on-another-machine')
        }
        finally {
          dispose()
        }
      })
    })

    /**
     * The row and the store must key the tab to the SAME repo.
     *
     * `seed` is spread BEFORE `agentFields` so the worker's own answer wins on
     * the row, exactly as it wins in the store. Reverse the two and a response
     * that resolves a different toplevel than the active tab's guess leaves the
     * row pointing at one key while the authoritative entry sits under another,
     * which is the divergence that shows a tab under the wrong repo group.
     */
    it('prefers the response\'s own toplevel over the active tab\'s guess on the row', async () => {
      await createRoot(async (dispose) => {
        try {
          mockListAvailableProviders.mockResolvedValue({ providers: [AgentProvider.CLAUDE_CODE] })
          mockOpenAgent.mockResolvedValue({
            agent: create(AgentInfoSchema, {
              id: 'new-agent',
              workerId: 'w-1',
              workingDir: '/repo',
              agentProvider: AgentProvider.CLAUDE_CODE,
              // A linked worktree: the same working dir resolves to a DEEPER
              // toplevel here than the active tab recorded for its own worker.
              gitStatus: create(GitRepoStatusSchema, { toplevel: '/repo/wt' }),
            }),
          })

          const { ops, view, add } = setup()
          add({ id: 'active', workerId: 'w-other', workingDir: '/repo', gitToplevel: '/repo' })

          await flush()
          await ops.handleOpenAgent()
          await flush()

          expect(
            view.getAgentTab('new-agent')?.gitToplevel,
            'the worker that owns this agent answered; the guess must not outrank it',
          ).toBe('/repo/wt')
        }
        finally {
          dispose()
        }
      })
    })

    it('falls back to the MRU provider when the active tab is not an agent tab', async () => {
      await createRoot(async (dispose) => {
        try {
          mockListAvailableProviders.mockResolvedValue({ providers: [AgentProvider.CLAUDE_CODE, AgentProvider.CODEX] })
          mockOpenAgent.mockResolvedValue({
            agent: create(AgentInfoSchema, {
              id: 'new-agent',
              workerId: 'w-1',
              workingDir: '/tmp',
              agentProvider: AgentProvider.CODEX,
            }),
          })
          localStorageSet(KEY_MRU_AGENT_PROVIDERS, [AgentProvider.CODEX, AgentProvider.CLAUDE_CODE])

          const { ops, addTerminal } = setup()
          addTerminal('terminal-1', { workerId: 'w-1', workingDir: '/tmp' })

          await flush()
          await ops.handleOpenAgent()

          expect(mockOpenAgent).toHaveBeenCalledWith('w-1', expect.objectContaining({
            agentProvider: AgentProvider.CODEX,
          }))
        }
        finally {
          dispose()
        }
      })
    })

    /**
     * `useAgentOperations` over a tab context the caller states, with a spy for
     * the dialog. `setup()` above pins its context at w-1 / /tmp, and both
     * dialog cases below need one that resolves no complete pair.
     */
    function opsWithContext(ctx: { workerId: string, workingDir: string }) {
      const newAgentDialog = { open: vi.fn(), close: vi.fn(), value: () => null }
      installTestBridge({ workspaceId: 'ws-1' })
      const stores = createTestTabStores('ws-1')
      const chatStore = {
        getMessages: vi.fn().mockReturnValue([]),
        forgetAgent: vi.fn(),
      } as any

      const ops = useAgentOperations({
        agentSessionStore: createAgentSessionStore(),
        agentInputQueueStore: { clearAgent: vi.fn() } as any,
        chatStore,
        controlStore: createControlStore(),
        view: stores.view,
        metadata: stores.metadata,
        selection: stores.selection,
        getActiveWorkspaceId: () => 'ws-1',
        layoutStore: stores.layoutStore,
        settingsLoading: { start: vi.fn(), stop: vi.fn() },
        isActiveWorkspaceMutatable: () => true,
        activeWorkspace: () => ({ id: 'ws-1' } as Workspace),
        getCurrentTabContext: () => ctx,
        newAgentDialog,
        setNewAgentLoadingProvider: vi.fn(),
        repoGitStore: createRepoGitStore(),
      })
      return { ops, newAgentDialog }
    }

    it('opens the dialog when the working directory is unknown', async () => {
      await createRoot(async (dispose) => {
        try {
          const { ops, newAgentDialog } = opsWithContext({ workerId: 'w-1', workingDir: '' })

          await ops.handleOpenAgent()

          expect(newAgentDialog.open).toHaveBeenCalled()
          expect(mockOpenAgent).not.toHaveBeenCalled()
        }
        finally {
          dispose()
        }
      })
    })

    // The dialog is the fallback when neither the target nor the context
    // resolves a whole (worker, directory) pair -- and it has to open ON the
    // target. An `open({})` here would drop what the caller did know and ask
    // the user for a Worker and a path the branch row already stated.
    it('opens the dialog on the target when the pair is incomplete', async () => {
      await createRoot(async (dispose) => {
        try {
          const { ops, newAgentDialog } = opsWithContext({ workerId: '', workingDir: '' })

          await ops.handleOpenAgent(undefined, { workingDir: '/other/worktree' })

          expect(mockOpenAgent).not.toHaveBeenCalled()
          expect(newAgentDialog.open).toHaveBeenCalledTimes(1)
          expect(newAgentDialog.open.mock.calls[0][0]).toEqual({ workingDir: '/other/worktree' })
        }
        finally {
          dispose()
        }
      })
    })

    // The worker RPC is the step that cannot be taken back: a placement
    // refusal AFTER it strands an orphaned agent on the worker with no tab
    // to reach it by. When the workspace's tree never arrived, the open
    // must refuse BEFORE the RPC.
    it('refuses before the worker RPC when the workspace has no projected tree', async () => {
      await createRoot(async (dispose) => {
        try {
          mockListAvailableProviders.mockResolvedValue({ providers: [AgentProvider.CLAUDE_CODE] })
          // The bridge delivers ws-1's tree; the shell answers for a
          // workspace the bootstrap never delivered, so placement has no
          // projected tile to resolve to.
          const { ops } = setup('ws-never-bootstrapped')

          await flush()
          await ops.handleOpenAgent()

          expect(mockOpenAgent).not.toHaveBeenCalled()
          expect(mockShowWarnToast).toHaveBeenCalled()
        }
        finally {
          dispose()
        }
      })
    })

    // A refused open is not a use: recording it would promote a provider
    // the user never successfully opened, and the next quick-create pick
    // (MRU order) would follow a phantom.
    it('does not record an MRU provider use when the open is refused', async () => {
      await createRoot(async (dispose) => {
        try {
          mockListAvailableProviders.mockResolvedValue({ providers: [AgentProvider.CLAUDE_CODE, AgentProvider.CODEX] })
          const { ops } = setup('ws-never-bootstrapped')

          await flush()
          await ops.handleOpenAgent(AgentProvider.CODEX)

          expect(mockOpenAgent).not.toHaveBeenCalled()
          expect(localStorageGet(KEY_MRU_AGENT_PROVIDERS), 'a refused open is not a use').toBeUndefined()
        }
        finally {
          dispose()
        }
      })
    })
  })

  describe('handleInterrupt', () => {
    it('leaves live state to the authoritative completion event', async () => {
      await createRoot(async (dispose) => {
        try {
          const { ops, add, agentSessionStore, chatStore } = setup()
          const agent = create(AgentInfoSchema, {
            id: 'codex-1',
            workerId: 'w-1',
            agentProvider: AgentProvider.CODEX,
            agentSessionId: 'thread-1',
          })
          add({ id: agent.id, ...protoToAgentTabFields(fixtureStore, agent.workerId, agent) })
          mockInterruptAgent.mockResolvedValue({})
          agentSessionStore.updateInfo('codex-1', { codexTurnId: 'turn-1', thinkingTokens: 100 })

          await ops.handleInterrupt('codex-1')

          expect(mockInterruptAgent).toHaveBeenCalledWith('w-1', {
            agentId: 'codex-1',
          })
          expect(agentSessionStore.getInfo('codex-1').codexTurnId).toBe('turn-1')
          expect(agentSessionStore.getInfo('codex-1').thinkingTokens).toBe(100)
          expect(chatStore.streamingText.clear).not.toHaveBeenCalled()
          expect(chatStore.clearToolProgress).not.toHaveBeenCalled()
        }
        finally {
          dispose()
        }
      })
    })

    it('keeps live state when the worker rejects the interrupt', async () => {
      await createRoot(async (dispose) => {
        try {
          const { ops, add, agentSessionStore, chatStore } = setup()
          const agent = create(AgentInfoSchema, {
            id: 'codex-1',
            workerId: 'w-1',
            agentProvider: AgentProvider.CODEX,
            agentSessionId: 'thread-1',
          })
          add({ id: agent.id, ...protoToAgentTabFields(fixtureStore, agent.workerId, agent) })
          mockInterruptAgent.mockRejectedValue(new Error('interrupt failed'))
          agentSessionStore.updateInfo('codex-1', { codexTurnId: 'turn-1', thinkingTokens: 100 })

          await ops.handleInterrupt('codex-1')

          expect(agentSessionStore.getInfo('codex-1').codexTurnId).toBe('turn-1')
          expect(agentSessionStore.getInfo('codex-1').thinkingTokens).toBe(100)
          expect(chatStore.streamingText.clear).not.toHaveBeenCalled()
          expect(chatStore.clearToolProgress).not.toHaveBeenCalled()
        }
        finally {
          dispose()
        }
      })
    })
  })

  describe('handleAgentSettingChange', () => {
    it('rolls back to the prior current value and labels the toast from the option group', async () => {
      await createRoot(async (dispose) => {
        try {
          const { view, ops, add } = setup()
          // The current value of an axis lives on its option group's
          // `currentValue`; the tab derives `optionValues.opencode_mode` from it.
          const agent = create(AgentInfoSchema, {
            id: 'a-1',
            workerId: 'w-1',
            optionGroups: [{
              id: 'opencode_mode',
              label: 'Execution Mode',
              currentValue: 'safe',
              defaultValue: 'safe',
              options: [
                { id: 'safe', name: 'Safe' },
                { id: 'fast', name: 'Fast' },
              ],
            }],
          })
          add({ id: agent.id, ...protoToAgentTabFields(fixtureStore, agent.workerId, agent) })
          mockUpdateAgentSettings.mockRejectedValueOnce(new Error('boom'))

          await ops.handleAgentSettingChange('a-1', { sets: { opencode_mode: 'fast' } })

          // One RPC carrying the uniform `{ options: { [groupKey]: value } }` payload.
          expect(mockUpdateAgentSettings).toHaveBeenCalledWith('w-1', {
            agentId: 'a-1',
            settings: { options: { opencode_mode: 'fast' } },
          })
          // The failed change rolls back to the prior current value ('safe').
          expect(view.getAgentTab('a-1')?.optionValues?.opencode_mode).toBe('safe')
          expect(mockShowWarnToast).toHaveBeenCalledWith('Failed to change Execution Mode', expect.any(Error))
        }
        finally {
          dispose()
        }
      })
    })

    it('rollback re-reads current state to avoid clobbering concurrent changes', async () => {
      await createRoot(async (dispose) => {
        try {
          const { view, ops, add } = setup()
          const agent = create(AgentInfoSchema, {
            id: 'a-concurrent',
            workerId: 'w-1',
            optionGroups: [
              {
                id: 'sandbox_policy',
                label: 'Sandbox Policy',
                currentValue: 'workspace-write',
                defaultValue: 'workspace-write',
                options: [
                  { id: 'workspace-write', name: 'Workspace Write' },
                  { id: 'danger-full-access', name: 'Full Access' },
                ],
              },
              {
                id: 'network_access',
                label: 'Network Access',
                currentValue: 'restricted',
                defaultValue: 'restricted',
                options: [
                  { id: 'restricted', name: 'Restricted' },
                  { id: 'enabled', name: 'Enabled' },
                ],
              },
            ],
          })
          add({ id: agent.id, ...protoToAgentTabFields(fixtureStore, agent.workerId, agent) })

          // First call will fail; second succeeds.
          let rejectFirst!: (err: Error) => void
          mockUpdateAgentSettings.mockImplementationOnce(() => new Promise((_resolve, reject) => {
            rejectFirst = reject
          }))
          mockUpdateAgentSettings.mockResolvedValueOnce({})

          // Launch both changes concurrently.
          const p1 = ops.handleAgentSettingChange('a-concurrent', { sets: { sandbox_policy: 'danger-full-access' } })
          const p2 = ops.handleAgentSettingChange('a-concurrent', { sets: { network_access: 'enabled' } })

          // Both optimistic updates should be applied.
          const mid = view.getAgentTab('a-concurrent')
          expect(mid?.optionValues?.sandbox_policy).toBe('danger-full-access')
          expect(mid?.optionValues?.network_access).toBe('enabled')

          // Fail the first RPC — its rollback should only revert sandbox_policy,
          // leaving network_access intact.
          rejectFirst(new Error('sandbox fail'))
          await p1
          await p2

          const final = view.getAgentTab('a-concurrent')
          expect(final?.optionValues?.sandbox_policy).toBe('workspace-write')
          expect(final?.optionValues?.network_access).toBe('enabled')
        }
        finally {
          dispose()
        }
      })
    })

    it('rolls back to unset when the group had no prior current value', async () => {
      await createRoot(async (dispose) => {
        try {
          const { view, ops, add } = setup()
          // No `currentValue` on the group, so the tab carries no prior value for
          // the axis; a failed change reverts by DELETING the key (not writing ''),
          // so agentTabOptionGroups falls through to the catalog's confirmed value
          // instead of blanking the group with a spurious empty override.
          const agent = create(AgentInfoSchema, {
            id: 'a-2',
            workerId: 'w-1',
            optionGroups: [{
              id: 'opencode_mode',
              label: 'Execution Mode',
              options: [
                { id: 'safe', name: 'Safe' },
                { id: 'fast', name: 'Fast' },
              ],
            }],
          })
          add({ id: agent.id, ...protoToAgentTabFields(fixtureStore, agent.workerId, agent) })
          mockUpdateAgentSettings.mockRejectedValueOnce(new Error('boom'))

          await ops.handleAgentSettingChange('a-2', { sets: { opencode_mode: 'fast' } })

          const values = view.getAgentTab('a-2')?.optionValues
          expect(values && 'opencode_mode' in values).toBe(false)
        }
        finally {
          dispose()
        }
      })
    })
  })

  describe('handleAgentClose', () => {
    it('removes agent/tab synchronously BEFORE the close RPC resolves', async () => {
      await createRoot(async (dispose) => {
        try {
          const { view, chatStore, agentInputQueueStore, ops, add } = setup()
          const agent = create(AgentInfoSchema, { id: 'a-1', workerId: 'w-1' })
          add({ id: agent.id, ...protoToAgentTabFields(fixtureStore, agent.workerId, agent) })
          add({ id: 'a-1', title: 'Agent Olivia', workerId: 'w-1', workingDir: '/tmp' })

          // Never-resolving RPC to prove the UI mutation is synchronous.
          mockCloseAgent.mockReturnValueOnce(new Promise(() => {}))

          ops.handleAgentClose('a-1')

          // Store mutations happened synchronously.
          expect(view.getAgentTab('a-1')).toBeUndefined()
          // Per-agent state is reclaimed synchronously too.
          expect(chatStore.forgetAgent).toHaveBeenCalledWith('a-1')
          expect(agentInputQueueStore.clearAgent).toHaveBeenCalledWith('a-1')
          // RPC was dispatched with KEEP as the default worktree action.
          expect(mockCloseAgent).toHaveBeenCalledWith('w-1', { agentId: 'a-1', worktreeAction: WorktreeAction.KEEP })
        }
        finally {
          dispose()
        }
      })
    })

    it('passes through the worktreeAction argument', async () => {
      await createRoot(async (dispose) => {
        try {
          const { ops, add } = setup()
          const agent = create(AgentInfoSchema, { id: 'a-remove', workerId: 'w-1' })
          add({ id: agent.id, ...protoToAgentTabFields(fixtureStore, agent.workerId, agent) })
          add({ id: 'a-remove', title: 'Agent Remove', workerId: 'w-1', workingDir: '/tmp' })

          mockCloseAgent.mockResolvedValueOnce({
            result: {
              worktreePath: '',
              worktreeId: '',
              failureMessage: '',
              failureDetail: '',
            },
          } as CloseAgentResponse)

          ops.handleAgentClose('a-remove', WorktreeAction.REMOVE)
          await flush()

          expect(mockCloseAgent).toHaveBeenCalledWith('w-1', { agentId: 'a-remove', worktreeAction: WorktreeAction.REMOVE })
        }
        finally {
          dispose()
        }
      })
    })

    it('surfaces failure_message + failure_detail via toast when the RPC reports a partial failure', async () => {
      await createRoot(async (dispose) => {
        try {
          const { view, ops, add } = setup()
          const agent = create(AgentInfoSchema, { id: 'a-fail', workerId: 'w-1' })
          add({ id: agent.id, ...protoToAgentTabFields(fixtureStore, agent.workerId, agent) })
          add({ id: 'a-fail', title: 'Agent Fail', workerId: 'w-1', workingDir: '/tmp' })

          mockCloseAgent.mockResolvedValueOnce({
            result: {
              worktreeId: 'wt-1',
              worktreePath: '/some/wt',
              failureMessage: 'Failed to remove worktree',
              failureDetail: 'git worktree remove /some/wt: exit 128',
            },
          } as CloseAgentResponse)

          ops.handleAgentClose('a-fail', WorktreeAction.REMOVE)
          await flush()

          expect(mockShowWarnToast).toHaveBeenCalledWith('Failed to remove worktree: git worktree remove /some/wt: exit 128')
          // Tab was removed synchronously — failure doesn't roll back UI.
          expect(view.getAgentTab('a-fail')).toBeUndefined()
        }
        finally {
          dispose()
        }
      })
    })

    it('surfaces a generic toast when the RPC rejects', async () => {
      await createRoot(async (dispose) => {
        try {
          const { view, ops, add } = setup()
          const agent = create(AgentInfoSchema, { id: 'a-reject', workerId: 'w-1' })
          add({ id: agent.id, ...protoToAgentTabFields(fixtureStore, agent.workerId, agent) })
          add({ id: 'a-reject', title: 'Agent Reject', workerId: 'w-1', workingDir: '/tmp' })

          const err = new Error('network down')
          mockCloseAgent.mockRejectedValueOnce(err)

          ops.handleAgentClose('a-reject')
          await flush()

          expect(mockShowWarnToast).toHaveBeenCalledWith('Failed to close agent', err)
          expect(view.getAgentTab('a-reject')).toBeUndefined()
        }
        finally {
          dispose()
        }
      })
    })

    it('skips RPC and still removes tab when workerId is missing', async () => {
      await createRoot(async (dispose) => {
        try {
          const { view, ops, add } = setup()
          const agent = create(AgentInfoSchema, { id: 'a-2', workerId: '' })
          add({ id: agent.id, ...protoToAgentTabFields(fixtureStore, agent.workerId, agent) })
          add({ id: 'a-2', title: 'Agent Liam', workerId: '', workingDir: '' })

          mockCloseAgent.mockClear()

          ops.handleAgentClose('a-2')

          expect(mockCloseAgent).not.toHaveBeenCalled()
          expect(view.getAgentTab('a-2')).toBeUndefined()
          expect(view.getAgentTab('a-2')).toBeUndefined()
        }
        finally {
          dispose()
        }
      })
    })

    it('skips RPC when agent is not found in store', async () => {
      await createRoot(async (dispose) => {
        try {
          const { ops } = setup()

          mockCloseAgent.mockClear()

          ops.handleAgentClose('nonexistent')

          expect(mockCloseAgent).not.toHaveBeenCalled()
        }
        finally {
          dispose()
        }
      })
    })

    /**
     * The worker holds the parent chain, so its `descendant_agent_ids` is the
     * authoritative list of subagent tabs that go with this one.
     *
     * `handleTabClose` sweeps them optimistically from this client's own tabs,
     * which is what updates the strip on the click. It cannot see a subagent
     * tab opened moments ago, though: `openSubagentTab` leaves `parentAgentId`
     * to `listAgents` hydration, so until that lands the tab looks parentless
     * and would outlive the agent that fed it.
     */
    it('retires the subagent tabs the worker reports under the closed agent', async () => {
      await createRoot(async (dispose) => {
        try {
          const { view, ops, add } = setup()
          for (const id of ['a-root', 'a-child', 'a-grandchild']) {
            const agent = create(AgentInfoSchema, { id, workerId: 'w-1' })
            add({ id, ...protoToAgentTabFields(fixtureStore, agent.workerId, agent) })
            add({ id, title: id, workerId: 'w-1', workingDir: '/tmp' })
          }
          // No parentAgentId on either child: the hydration that would set one
          // has not landed, which is exactly when the local sweep is blind.
          expect(view.getAgentTab('a-child')).toBeDefined()

          mockCloseAgent.mockResolvedValueOnce({
            result: { worktreePath: '', worktreeId: '', failureMessage: '', failureDetail: '' },
            descendantAgentIds: ['a-grandchild', 'a-child'],
          } as CloseAgentResponse)

          ops.handleAgentClose('a-root')
          // The parent goes synchronously; the children wait on the worker.
          expect(view.getAgentTab('a-root')).toBeUndefined()
          expect(view.getAgentTab('a-child')).toBeDefined()

          await flush()
          expect(view.getAgentTab('a-child')).toBeUndefined()
          expect(view.getAgentTab('a-grandchild')).toBeUndefined()
        }
        finally {
          dispose()
        }
      })
    })

    /**
     * The worker answers from the `agents` table, where a row exists for every
     * subagent the provider ever spawned -- `EnsureChildAgent` creates one on
     * first sight of a spawn, whether or not anyone opened that transcript.
     *
     * The hub resolves a tombstone's workspace THROUGH the tab record, so an id
     * it has no record for is rejected as UNKNOWN_WORKSPACE, and that rejection
     * is fatal for the whole batch. Passing the worker's list through unfiltered
     * therefore does not merely waste an op: it takes the real tombstones with
     * it, and every genuine subagent tab stays open.
     */
    it('retires only the reported ids it holds a live tab for', async () => {
      await createRoot(async (dispose) => {
        try {
          const { view, ops, add, harness } = setup()
          for (const id of ['a-root3', 'a-real']) {
            const agent = create(AgentInfoSchema, { id, workerId: 'w-1' })
            add({ id, ...protoToAgentTabFields(fixtureStore, agent.workerId, agent) })
            add({ id, title: id, workerId: 'w-1', workingDir: '/tmp' })
          }

          mockCloseAgent.mockResolvedValueOnce({
            result: { worktreePath: '', worktreeId: '', failureMessage: '', failureDetail: '' },
            // `a-never-opened` ran as a subagent but nobody opened its tab.
            descendantAgentIds: ['a-never-opened', 'a-real'],
          } as CloseAgentResponse)

          ops.handleAgentClose('a-root3')
          await flush()

          expect(view.getAgentTab('a-real'), 'the tab that exists is retired').toBeUndefined()
          // A tombstone for an unknown id does not merely no-op: applying one
          // MATERIALIZES a record for it. Its absence is the proof no op went.
          expect(
            harness.pending.state.speculativeState.tabs['a-never-opened'],
            'no op is emitted for an id with no tab record',
          ).toBeUndefined()
        }
        finally {
          dispose()
        }
      })
    })

    /**
     * A descendant the optimistic sweep could not see never goes through
     * `handleTabClose`, so this pass is the ONLY thing that retires it -- and a
     * bare tombstone would leave its chat-store entry (loaded window, live tail,
     * command streams, span index, and to-dos), its input queue snapshot, its control-store
     * entry and its attachments allocated for the life of the page. Nothing else
     * reclaims them: `forgetAgent` has no other caller.
     */
    it('reclaims per-agent store state for each subagent it retires', async () => {
      await createRoot(async (dispose) => {
        try {
          const { ops, add, chatStore, agentInputQueueStore, controlStore } = setup()
          for (const id of ['a-root4', 'a-kid']) {
            const agent = create(AgentInfoSchema, { id, workerId: 'w-1' })
            add({ id, ...protoToAgentTabFields(fixtureStore, agent.workerId, agent) })
            add({ id, title: id, workerId: 'w-1', workingDir: '/tmp' })
          }
          const clearAgent = vi.spyOn(controlStore, 'clearAgent')

          mockCloseAgent.mockResolvedValueOnce({
            result: { worktreePath: '', worktreeId: '', failureMessage: '', failureDetail: '' },
            descendantAgentIds: ['a-kid'],
          } as CloseAgentResponse)

          ops.handleAgentClose('a-root4')
          await flush()

          expect(chatStore.forgetAgent).toHaveBeenCalledWith('a-kid')
          expect(agentInputQueueStore.clearAgent).toHaveBeenCalledWith('a-kid')
          expect(clearAgent).toHaveBeenCalledWith('a-kid')
        }
        finally {
          dispose()
        }
      })
    })

    // A rejected close reports nothing, and the tab sweep must not throw on the
    // way past -- `awaitCloseResult` owns the user-facing error.
    it('retires no subagent tab when the close RPC rejects', async () => {
      await createRoot(async (dispose) => {
        try {
          const { view, ops, add } = setup()
          for (const id of ['a-root2', 'a-child2']) {
            const agent = create(AgentInfoSchema, { id, workerId: 'w-1' })
            add({ id, ...protoToAgentTabFields(fixtureStore, agent.workerId, agent) })
            add({ id, title: id, workerId: 'w-1', workingDir: '/tmp' })
          }
          mockCloseAgent.mockRejectedValueOnce(new Error('worker unreachable'))

          ops.handleAgentClose('a-root2')
          await flush()

          expect(view.getAgentTab('a-child2'), 'nothing said this tab should go').toBeDefined()
        }
        finally {
          dispose()
        }
      })
    })
  })

  describe('handleControlResponse claim-token echo', () => {
    const answer = (requestId: string) =>
      new TextEncoder().encode(JSON.stringify({ response: { request_id: requestId, response: { behavior: 'allow' } } }))

    const request = (requestId: string, claimToken?: string): ControlRequest =>
      ({ requestId, agentId: 'a1', payload: { request: { tool_name: 'Bash' } }, claimToken })

    it('echoes the answered instance\'s per-instance claimToken so the worker dedups per instance', async () => {
      await createRoot(async (dispose) => {
        try {
          const { ops } = setup()
          vi.mocked(workerRpc.sendControlResponse).mockClear()

          await ops.handleControlResponse(request('r1', 'instance-token-1'), answer('r1'))

          expect(workerRpc.sendControlResponse).toHaveBeenCalledWith(
            expect.any(String),
            expect.objectContaining({ agentId: 'a1', claimToken: 'instance-token-1' }),
          )
        }
        finally {
          dispose()
        }
      })
    })

    it('echoes an empty claimToken for a request that carries none (degrades to id-only dedup)', async () => {
      await createRoot(async (dispose) => {
        try {
          const { ops } = setup()
          vi.mocked(workerRpc.sendControlResponse).mockClear()

          await ops.handleControlResponse(request('r-missing'), answer('r-missing'))

          expect(workerRpc.sendControlResponse).toHaveBeenCalledWith(
            expect.any(String),
            expect.objectContaining({ agentId: 'a1', claimToken: '' }),
          )
        }
        finally {
          dispose()
        }
      })
    })

    it('answers as the captured instance after the request left the store', async () => {
      await createRoot(async (dispose) => {
        try {
          const { ops } = setup()
          vi.mocked(workerRpc.sendControlResponse).mockClear()

          // A prior answer or cancel already removed this request. The caller holds the
          // instance, so the answer still claims on it rather than on an empty-token key,
          // which could double-win or drop the answer.
          await ops.handleControlResponse(request('r-gone', 'captured-token-9'), answer('r-gone'))

          expect(workerRpc.sendControlResponse).toHaveBeenCalledWith(
            expect.any(String),
            expect.objectContaining({ agentId: 'a1', claimToken: 'captured-token-9' }),
          )
        }
        finally {
          dispose()
        }
      })
    })

    // A reused request_id can hold a SECOND instance in the store. The answer must
    // never read a token back from there: that instance's token would claim the
    // wrong answer, and the worker would dedup the two against each other.
    it('ignores a same-id sibling in the store and answers with the captured token', async () => {
      await createRoot(async (dispose) => {
        try {
          const { ops, controlStore } = setup()
          vi.mocked(workerRpc.sendControlResponse).mockClear()
          controlStore.addRequest('a1', { requestId: 'r1', agentId: 'a1', payload: { request: { tool_name: 'Bash' } }, claimToken: 'other-instance-token' })

          await ops.handleControlResponse(request('r1'), answer('r1'))

          expect(workerRpc.sendControlResponse).toHaveBeenCalledWith(
            expect.any(String),
            expect.objectContaining({ agentId: 'a1', claimToken: '' }),
          )
        }
        finally {
          dispose()
        }
      })
    })

    it('rejects after reporting a transport failure', async () => {
      await createRoot(async (dispose) => {
        try {
          const { ops } = setup()
          const failure = new Error('send failed')
          vi.mocked(workerRpc.sendControlResponse).mockRejectedValueOnce(failure)

          await expect(ops.handleControlResponse(request('r1'), answer('r1'))).rejects.toBe(failure)
          expect(mockShowWarnToast).toHaveBeenCalledWith('Failed to send response', failure)
        }
        finally {
          dispose()
        }
      })
    })
  })

  describe('loadAvailableProviders', () => {
    // The RETRY moved to callWorker, which every worker RPC goes through —
    // see workerRpc.test.ts. What stays this hook's business is the
    // CANCELLATION: a reply for the worker the user just left must not
    // overwrite the list for the one they moved to.
    it('applies the list it receives', async () => {
      await createRoot(async (dispose) => {
        try {
          mockShowWarnToast.mockClear()
          mockListAvailableProviders.mockResolvedValue({ providers: [AgentProvider.CLAUDE_CODE] })
          const { ops } = setup()
          await flush()
          expect(ops.availableProviders()).toEqual([AgentProvider.CLAUDE_CODE])
          expect(mockShowWarnToast).not.toHaveBeenCalled()
        }
        finally {
          dispose()
        }
      })
    })

    // The effect registers its cancel closure with onCleanup. Returning it
    // from the effect body did nothing — SolidJS hands an effect's return
    // value to the next run as `prev` and never calls it — so a reply
    // outlived the scope that asked for it and landed on whatever worker
    // the user moved to.
    it('ignores a reply that arrives after its scope is disposed', async () => {
      mockShowWarnToast.mockClear()
      let resolveList: (v: unknown) => void = () => {}
      mockListAvailableProviders.mockImplementation(() => new Promise((r) => {
        resolveList = r
      }))

      let ops!: ReturnType<typeof setup>['ops']
      let dispose!: () => void
      await createRoot(async (d) => {
        dispose = d
        ops = setup().ops
        await flush()
      })
      expect(mockListAvailableProviders).toHaveBeenCalledTimes(1)

      dispose()
      resolveList({ providers: [AgentProvider.CLAUDE_CODE] })
      await flush()
      expect(ops.availableProviders()).toBeUndefined()
      expect(mockShowWarnToast).not.toHaveBeenCalled()
    })

    // The guard sits on the WRITE, not on the call. Every caller outside
    // this hook -- the refresh button in the agent dialogs -- reaches
    // `loadAvailableProviders` through hops that discard its return value,
    // so a cancel closure handed back to the caller protected nobody.
    it('drops a reply for the worker the tab has since left', async () => {
      mockShowWarnToast.mockClear()
      const pending = deferred<{ providers: AgentProvider[] }>()
      let workerId = 'w-1'
      await createRoot(async (dispose) => {
        try {
          // The mount effect settles first, so the manual refresh below is
          // the only call left in flight.
          mockListAvailableProviders.mockResolvedValueOnce({ providers: [AgentProvider.CODEX] })
          const { ops } = setup('ws-1', () => workerId)
          await flush()
          expect(ops.availableProviders()).toEqual([AgentProvider.CODEX])

          mockListAvailableProviders.mockReturnValueOnce(pending.promise)
          ops.loadAvailableProviders()
          // The user moves to a tab on another worker while the scan runs.
          workerId = 'w-2'
          pending.resolve({ providers: [AgentProvider.CLAUDE_CODE] })
          await flush()

          expect(ops.availableProviders()).toEqual([AgentProvider.CODEX])
        }
        finally {
          dispose()
        }
      })
    })

    // A newer scan supersedes the older one, so the list cannot be written
    // twice for one worker with the stale answer landing last.
    it('aborts the previous scan when a newer one starts', async () => {
      await createRoot(async (dispose) => {
        try {
          const first = deferred<{ providers: AgentProvider[] }>()
          mockListAvailableProviders.mockReturnValueOnce(first.promise)
          const { ops } = setup()
          await flush()

          mockListAvailableProviders.mockResolvedValueOnce({ providers: [AgentProvider.CODEX] })
          ops.loadAvailableProviders()
          await flush()
          expect(ops.availableProviders()).toEqual([AgentProvider.CODEX])

          const signal = mockListAvailableProviders.mock.calls[0][1].signal as AbortSignal
          expect(signal.aborted).toBe(true)

          first.resolve({ providers: [AgentProvider.CLAUDE_CODE] })
          await flush()
          expect(ops.availableProviders()).toEqual([AgentProvider.CODEX])
        }
        finally {
          dispose()
        }
      })
    })

    // Leaving the request to retry for a worker no tab points at spends a
    // full backoff budget on an answer nobody can use.
    it('aborts the scan when the tab leaves every worker', async () => {
      await createRoot(async (dispose) => {
        try {
          const pending = deferred<{ providers: AgentProvider[] }>()
          mockListAvailableProviders.mockReturnValueOnce(pending.promise)
          let workerId = 'w-1'
          const { ops } = setup('ws-1', () => workerId)
          await flush()

          const signal = mockListAvailableProviders.mock.calls[0][1].signal as AbortSignal
          expect(signal.aborted).toBe(false)

          // The tab moves to one with no worker. The effect re-runs on that
          // context change; the exported entry point is the same code path
          // without the reactive plumbing.
          workerId = ''
          ops.loadAvailableProviders()
          expect(signal.aborted).toBe(true)
          expect(mockListAvailableProviders).toHaveBeenCalledTimes(1)
        }
        finally {
          dispose()
        }
      })
    })

    // A settled failure still reaches the user. The retry budget is spent
    // inside callWorker, so what arrives here is the final answer.
    it('toasts a failure and keeps the previous list', async () => {
      await createRoot(async (dispose) => {
        try {
          mockShowWarnToast.mockClear()
          const unavailable = new ChannelError('rpc', 'provider scan did not finish; retry', { code: Code.Unavailable })
          mockListAvailableProviders.mockRejectedValue(unavailable)
          const { ops } = setup()
          await flush()
          expect(ops.availableProviders()).toBeUndefined()
          expect(mockShowWarnToast).toHaveBeenCalledWith('Failed to load available agent providers', unavailable)
        }
        finally {
          dispose()
        }
      })
    })

    // The scan runs from an effect, so a dropped link fails it alongside every
    // other background load. Each one that spoke turned a single outage into a
    // row of toasts, all of them reading as our own plumbing.
    it('stays silent when a dropped link is what failed the scan', async () => {
      await createRoot(async (dispose) => {
        try {
          mockShowWarnToast.mockClear()
          mockListAvailableProviders.mockRejectedValue(channelNotOpenError())
          const { ops } = setup()
          await flush()
          expect(ops.availableProviders()).toBeUndefined()
          expect(mockShowWarnToast).not.toHaveBeenCalled()
        }
        finally {
          dispose()
        }
      })
    })
  })
})
