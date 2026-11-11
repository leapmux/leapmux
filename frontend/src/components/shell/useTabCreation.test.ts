import type { TabCreationOrigin } from './useTabCreation'
import type { OpenAgentRequest } from '~/generated/proto/leapmux/v1/agent_pb'
import type { TabCreation } from '~/generated/proto/leapmux/v1/tab_creation_pb'
import { create } from '@bufbuild/protobuf'
import { Code } from '@connectrpc/connect'
import { createRoot, createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentInfoSchema, AgentProvider, AgentStatus, AgentStatusChangeSchema, AvailableOptionGroupSchema, OpenAgentResponseSchema } from '~/generated/proto/leapmux/v1/agent_pb'
import { GitRepoStatusSchema, TabType } from '~/generated/proto/leapmux/v1/common_pb'
import { TabInsertion } from '~/generated/proto/leapmux/v1/tab_creation_pb'
import { handleAgentStatusChange } from '~/hooks/agentEvents'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { setStorageAccountForTests } from '~/lib/browserStorage'
import { ChannelError } from '~/lib/channelError'
import { getCRDTBridge, tombstoneTab } from '~/lib/crdt'
import { getCachedSettingsLabel } from '~/lib/settingsLabelCache'
import { createAgentSessionStore } from '~/stores/agentSession.store'
import { createChatStore } from '~/stores/chat.store'
import { createControlStore } from '~/stores/control.store'
import { repoKey } from '~/stores/repoGit'
import { createRepoGitStore } from '~/stores/repoGit.store'
import { deferred, settle } from '~/test-support/async'
import { installTestBridge, seedWorkspace } from '~/test-support/crdtBridge'
import { createCanonicalTabPublisher } from '~/test-support/tabPublication'
import { createTestTabStores } from '~/test-support/tabStores'
import { useTabCreation } from './useTabCreation'

const mockOpenAgent = vi.fn()
const mockOpenTerminal = vi.fn()
const mockRegisterPayload = vi.fn()
const mockCreateWorkspace = vi.fn()
vi.mock('~/api/workerRpc', () => ({
  workerAgent: {
    openAgent: (...args: unknown[]) => mockOpenAgent(...args),
  },
  workerTerminal: {
    openTerminal: (...args: unknown[]) => mockOpenTerminal(...args),
  },
  workerPrivate: {
    registerTabPayload: (...args: unknown[]) => mockRegisterPayload(...args),
  },
}))
vi.mock('~/api/clients', () => ({
  workspaceClient: {
    createWorkspace: (...args: unknown[]) => mockCreateWorkspace(...args),
  },
}))

const disposers: Array<() => void> = []
beforeEach(() => {
  mockOpenAgent.mockReset()
  mockOpenTerminal.mockReset()
  mockRegisterPayload.mockReset()
  mockCreateWorkspace.mockReset()
})
afterEach(() => {
  for (const dispose of disposers.splice(0))
    dispose()
  vi.restoreAllMocks()
})

function setup(pendingAxes: () => ReadonlySet<string> = () => new Set()) {
  return createRoot((dispose) => {
    disposers.push(dispose)
    const harness = installTestBridge({ workspaceId: 'ws' })
    const publisher = createCanonicalTabPublisher(harness)
    const stores = createTestTabStores('ws')
    const repoGitStore = createRepoGitStore()
    const [userId, setUserId] = createSignal<string | undefined>(harness.userId)
    const [activeWorkspaceId, setActiveWorkspaceId] = createSignal<string | undefined>('ws')
    const focusEditor = vi.fn()
    const coordinator = useTabCreation({
      ...stores,
      userId,
      state: () => getCRDTBridge()?.speculativeState() ?? null,
      activeWorkspaceId,
      repoGitStore,
      settingsPendingAxes: pendingAxes,
      selectWorkspace: setActiveWorkspaceId,
      focusEditor,
    })
    const onError = vi.fn()
    const origin = {
      workerId: 'worker',
      workspaceId: 'ws',
      retryKey: 'test-action',
      onError,
    }
    const request = { workingDir: '/repo', agentProvider: AgentProvider.CLAUDE_CODE }
    function agentReply(req: OpenAgentRequest, publish = false) {
      const id = req.creation!.tabId
      const placement = req.creation!.placement!
      const at = {
        workerId: 'worker',
        workspaceId: placement.workspaceId,
        tileId: placement.destination.case === 'tileId'
          ? placement.destination.value
          : 'new-root',
      }
      return create(OpenAgentResponseSchema, {
        agent: create(AgentInfoSchema, {
          id,
          workerId: 'worker',
          workingDir: req.workingDir,
          agentProvider: req.agentProvider,
        }),
        publication: publish
          ? publisher.publish(TabType.AGENT, id, at)
          : publisher.receipt(TabType.AGENT, id, at),
      })
    }
    return {
      ...stores,
      dispose,
      coordinator,
      harness,
      publisher,
      repoGitStore,
      origin,
      request,
      agentReply,
      setUserId,
      setActiveWorkspaceId,
      focusEditor,
      onError,
    }
  })
}

function sentAgent(index = 0): OpenAgentRequest {
  return mockOpenAgent.mock.calls[index]![1] as OpenAgentRequest
}

describe('useTabCreation', () => {
  it('allocates a new identity when canonical state ends an uncertain attempt', async () => {
    const s = setup()
    mockOpenAgent.mockRejectedValueOnce(new ChannelError('transport', 'lost response'))
    expect(await s.coordinator.openAgent({ ...s.origin, request: s.request })).toBe(false)
    const first = sentAgent()
    s.publisher.publish(TabType.AGENT, first.creation!.tabId, { workerId: 'worker' })
    s.publisher.deliver([
      tombstoneTab(s.publisher.context, TabType.AGENT, first.creation!.tabId),
    ])
    await settle()
    const held = deferred<unknown>()
    mockOpenAgent.mockReturnValueOnce(held.promise)
    const second = s.coordinator.openAgent({ ...s.origin, request: s.request })
    await settle()
    const next = sentAgent(1)
    expect(next.creation!.tabId).not.toBe(first.creation!.tabId)
    held.resolve(s.agentReply(next, true))
    expect(await second).toBe(true)
  })

  it('joins a committed workspace attempt while canonical delivery is pending', async () => {
    const s = setup()
    seedWorkspace(s.harness, 'ws-new', 'new-root')
    mockCreateWorkspace.mockResolvedValue({ workspaceId: 'ws-new' })
    mockOpenAgent.mockImplementation((_worker, request: OpenAgentRequest) => Promise.resolve(s.agentReply(request)))
    const input = { ...s.origin, title: 'New workspace', request: s.request }
    const first = s.coordinator.openWorkspace(input)
    await settle()
    const second = s.coordinator.openWorkspace(input)
    await settle()
    expect(mockCreateWorkspace).toHaveBeenCalledOnce()
    expect(mockOpenAgent).toHaveBeenCalledOnce()
    const request = sentAgent()
    expect(request.creation?.placement?.destination.case).toBe('firstLeaf')
    s.publisher.publish(TabType.AGENT, request.creation!.tabId, { workerId: 'worker', workspaceId: 'ws-new', tileId: 'new-root' })
    expect(await first).toBe(true)
    expect(await second).toBe(true)
  })

  it('captures the source tab before awaiting workspace creation', async () => {
    const s = setup()
    for (const [id, workerId, branch] of [
      ['source-a', 'worker-a', 'original'],
      ['source-b', 'worker-b', 'changed'],
    ]) {
      s.publisher.publish(TabType.AGENT, id!, { workerId: workerId! })
      s.metadata.patch(id!, { workingDir: '/repo', gitToplevel: '/repo' })
      s.repoGitStore.upsert(repoKey(workerId!, '/repo'), { workerId: workerId!, toplevel: '/repo', branch: branch! })
    }
    s.selection.setActive(s.view.getAgentTab('source-a')!)
    const held = deferred<{ workspaceId: string }>()
    mockCreateWorkspace.mockReturnValueOnce(held.promise)
    mockOpenAgent.mockReturnValue(new Promise(() => {}))
    void s.coordinator.openWorkspace({ ...s.origin, title: 'New workspace', request: s.request })
    await settle()
    s.selection.setActive(s.view.getAgentTab('source-b')!)
    seedWorkspace(s.harness, 'ws-new', 'new-root')
    held.resolve({ workspaceId: 'ws-new' })
    await settle()
    expect(mockOpenAgent).toHaveBeenCalledOnce()
    expect(s.repoGitStore.get(repoKey('worker', '/repo'))?.branch).toBe('original')
  })
})

describe('useTabCreation Git snapshot freshness', () => {
  it('keeps Git-only live data over an older creation reply', async () => {
    const s = setup()
    const held = deferred<unknown>()
    mockOpenAgent.mockReturnValueOnce(held.promise)
    const result = s.coordinator.openAgent({ ...s.origin, request: s.request })
    await settle()
    const request = sentAgent()
    const id = request.creation!.tabId
    s.publisher.publish(TabType.AGENT, id, { workerId: 'worker' })
    handleAgentStatusChange(id, create(AgentStatusChangeSchema, {
      gitStatus: create(GitRepoStatusSchema, { toplevel: '/current', branch: 'current' }),
    }), 'live', {
      view: s.view,
      metadata: s.metadata,
      selection: s.selection,
      repoGitStore: s.repoGitStore,
      agentSessionStore: createAgentSessionStore(),
      chatStore: createChatStore(),
      controlStore: createControlStore(),
      getActiveWorkspaceId: () => 'ws',
    }, createLoadingSignal(), () => {}, 'worker')
    const reply = s.agentReply(request)
    reply.agent!.gitStatus = create(GitRepoStatusSchema, { toplevel: '/obsolete', branch: 'obsolete' })
    held.resolve(reply)
    expect(await result).toBe(true)
    expect.soft(s.metadata.get(id)?.gitToplevel).toBe('/current')
    expect.soft(s.repoGitStore.get(repoKey('worker', '/obsolete'))).toBeUndefined()
  })
})

type CreationKind = 'agent' | 'terminal' | 'file' | 'image'
function mockFor(kind: CreationKind) {
  return kind === 'agent'
    ? mockOpenAgent
    : kind === 'terminal'
      ? mockOpenTerminal
      : mockRegisterPayload
}
function typeFor(kind: CreationKind) {
  return kind === 'agent'
    ? TabType.AGENT
    : kind === 'terminal'
      ? TabType.TERMINAL
      : kind === 'file'
        ? TabType.FILE
        : TabType.IMAGE
}
function sentCreation(kind: CreationKind, index = 0): TabCreation {
  const request = mockFor(kind).mock.calls[index]![1]
  return kind === 'terminal'
    ? request.target.value
    : request.creation
}
function openKind(s: ReturnType<typeof setup>, kind: CreationKind, callbacks: Partial<TabCreationOrigin> = {}) {
  const origin = {
    ...s.origin,
    ...callbacks,
  }
  switch (kind) {
    case 'agent': return s.coordinator.openAgent({ ...origin, request: s.request })
    case 'terminal': return s.coordinator.openTerminal({
      ...origin,
      request: { workingDir: '/repo', cols: 80, rows: 24 },
    })
    case 'file': return s.coordinator.registerPayload({
      ...origin,
      request: {
        payload: {
          workingDir: '/repo',
          kind: {
            case: 'file',
            value: { filePath: '/repo/a.md' },
          },
        },
      },
    })
    case 'image': return s.coordinator.registerPayload({
      ...origin,
      request: {
        payload: {
          workingDir: '/repo',
          kind: {
            case: 'image',
            value: {
              agentId: 'source',
              seq: 1n,
              imageIndex: 0,
              title: 'Image',
            },
          },
        },
      },
    })
  }
}
function replyFor(s: ReturnType<typeof setup>, kind: CreationKind) {
  const creation = sentCreation(kind)
  const publication = s.publisher.receipt(typeFor(kind), creation.tabId)
  return kind === 'agent'
    ? s.agentReply(sentAgent())
    : kind === 'terminal'
      ? { terminalId: creation.tabId, title: 'Shell', publication }
      : { publication }
}

describe('useTabCreation canonical ordering', () => {
  for (const kind of ['agent', 'terminal', 'file', 'image'] as const) {
    it.each(['event-first', 'reply-first'] as const)(`waits for both ${kind} outcomes when %s`, async (order) => {
      const s = setup()
      const held = deferred<unknown>()
      mockFor(kind).mockReturnValueOnce(held.promise)
      const observed = vi.fn()
      const enqueue = vi.spyOn(getCRDTBridge()!, 'enqueue')
      let finished = false
      const result = openKind(s, kind, { onObserved: observed }).then((value) => {
        finished = true
        return value
      })
      await settle()
      const creation = sentCreation(kind)
      const publish = () => s.publisher.publish(typeFor(kind), creation.tabId, { workerId: 'worker' })
      if (order === 'event-first')
        publish()
      else
        held.resolve(replyFor(s, kind))
      await settle()
      expect(finished).toBe(false)
      expect(observed).not.toHaveBeenCalled()
      expect(s.selection.state.activeByWorkspace.ws).toBeUndefined()
      if (order === 'event-first')
        held.resolve(replyFor(s, kind))
      else
        publish()
      expect(await result).toBe(true)
      expect(observed).toHaveBeenCalledOnce()
      expect(s.selection.activeTabForWorkspace('ws')?.id).toBe(creation.tabId)
      expect(enqueue).not.toHaveBeenCalled()
    })

    it(`ignores a late ${kind} reply after account replacement`, async () => {
      const s = setup()
      const held = deferred<unknown>()
      mockFor(kind).mockReturnValueOnce(held.promise)
      const observed = vi.fn()
      const committed = vi.fn()
      const result = openKind(s, kind, { onObserved: observed, onCommitted: committed })
      await settle()
      const creation = sentCreation(kind)
      const reply = replyFor(s, kind)
      setStorageAccountForTests('different-account')
      s.setUserId('different-account')
      held.resolve(reply)
      expect(await result).toBe(false)
      await settle()
      expect(s.metadata.get(creation.tabId)).toBeUndefined()
      expect(observed).not.toHaveBeenCalled()
      expect(committed).not.toHaveBeenCalled()
      expect(s.onError).not.toHaveBeenCalled()
      expect(s.focusEditor).not.toHaveBeenCalled()
    })
  }
})

describe('useTabCreation retry identity', () => {
  it.each(['transport', 'definitive'] as const)('retains only uncertain attempts after %s failure', async (failure) => {
    const s = setup()
    mockOpenAgent.mockRejectedValueOnce(failure === 'transport'
      ? new ChannelError('transport', 'lost response')
      : new ChannelError('rpc', 'rejected creation', { code: Code.Aborted }))
    const input = {
      ...s.origin,
      request: {
        ...s.request,
        options: { z: 'last', a: 'first' },
      },
    }
    expect(await s.coordinator.openAgent(input)).toBe(false)
    const first = sentAgent()
    mockOpenAgent.mockImplementationOnce((_worker, request: OpenAgentRequest) => Promise.resolve(s.agentReply(request, true)))
    expect(await s.coordinator.openAgent({
      ...input,
      request: {
        ...input.request,
        options: { a: 'first', z: 'last' },
      },
    })).toBe(true)
    const second = sentAgent(1)
    if (failure === 'transport')
      expect(second).toEqual(first)
    else
      expect(second.creation!.tabId).not.toBe(first.creation!.tabId)
  })

  it('captures request values and allocates another identity for different configuration', async () => {
    const s = setup()
    const held = deferred<unknown>()
    mockOpenAgent.mockReturnValueOnce(held.promise)
    const input = {
      ...s.origin,
      request: {
        ...s.request,
        options: { model: 'original' },
      },
    }
    const result = s.coordinator.openAgent(input)
    input.request.options.model = 'changed'
    await settle()
    expect(sentAgent().options.model).toBe('original')
    held.reject(new ChannelError('transport', 'unknown outcome'))
    expect(await result).toBe(false)
    mockOpenAgent.mockImplementationOnce((_worker, request: OpenAgentRequest) => Promise.resolve(s.agentReply(request, true)))
    expect(await s.coordinator.openAgent(input)).toBe(true)
    expect(sentAgent(1).creation!.tabId).not.toBe(sentAgent().creation!.tabId)
  })

  it('reuses the workspace and tab identities after an unknown agent result', async () => {
    const s = setup()
    seedWorkspace(s.harness, 'ws-new', 'new-root')
    mockCreateWorkspace.mockResolvedValue({ workspaceId: 'ws-new' })
    mockOpenAgent.mockRejectedValueOnce(new ChannelError('transport', 'unknown outcome'))
    const input = { ...s.origin, title: 'Workspace', request: s.request }
    expect(await s.coordinator.openWorkspace(input)).toBe(false)
    const original = sentAgent()
    mockOpenAgent.mockImplementationOnce((_worker, request: OpenAgentRequest) => Promise.resolve(s.agentReply(request, true)))
    expect(await s.coordinator.openWorkspace(input)).toBe(true)
    expect(mockCreateWorkspace).toHaveBeenCalledOnce()
    expect(sentAgent(1)).toEqual(original)
  })
})

describe('useTabCreation local completion', () => {
  it('selects the current canonical location after a move', async () => {
    const s = setup()
    seedWorkspace(s.harness, 'other', 'other-root')
    const held = deferred<unknown>()
    mockOpenAgent.mockReturnValueOnce(held.promise)
    const result = openKind(s, 'agent')
    await settle()
    const creation = sentCreation('agent')
    s.publisher.publish(TabType.AGENT, creation.tabId, { workerId: 'worker', workspaceId: 'other', tileId: 'other-root' })
    held.resolve(replyFor(s, 'agent'))
    expect(await result).toBe(true)
    expect(s.selection.activeTabForWorkspace('other')?.id).toBe(creation.tabId)
    expect(s.layoutStore.focusedTileIdFor('other')).toBe('other-root')
  })

  it('preserves a newer user selection when creation completes', async () => {
    const s = setup()
    const held = deferred<unknown>()
    mockOpenAgent.mockReturnValueOnce(held.promise)
    const result = openKind(s, 'agent')
    await settle()
    s.publisher.publish(TabType.AGENT, 'peer', { workerId: 'worker' })
    s.selection.setActive(s.view.getAgentTab('peer')!)
    s.publisher.publish(TabType.AGENT, sentCreation('agent').tabId, { workerId: 'worker' })
    held.resolve(replyFor(s, 'agent'))
    expect(await result).toBe(true)
    expect(s.selection.activeTabForWorkspace('ws')?.id).toBe('peer')
    await settle()
    expect(s.focusEditor).not.toHaveBeenCalled()
  })

  it('does not recreate metadata or call completion after canonical close', async () => {
    const s = setup()
    const held = deferred<unknown>()
    mockOpenAgent.mockReturnValueOnce(held.promise)
    const observed = vi.fn()
    const committed = vi.fn()
    const result = openKind(s, 'agent', { onObserved: observed, onCommitted: committed })
    await settle()
    const id = sentCreation('agent').tabId
    s.publisher.publish(TabType.AGENT, id, { workerId: 'worker' })
    s.publisher.deliver([
      tombstoneTab(s.publisher.context, TabType.AGENT, id),
    ])
    s.metadata.remove(id)
    held.resolve(replyFor(s, 'agent'))
    expect(await result).toBe(false)
    await settle()
    expect(s.metadata.get(id)).toBeUndefined()
    expect(observed).not.toHaveBeenCalled()
    expect(committed).not.toHaveBeenCalled()
  })

  it('does not close a replacement dialog when an old reply arrives', async () => {
    const s = setup()
    const [viewIdentity, setViewIdentity] = createSignal<unknown>({ instance: 'old' })
    const held = deferred<unknown>()
    mockOpenAgent.mockReturnValueOnce(held.promise)
    const observed = vi.fn()
    const result = openKind(s, 'agent', { viewIdentity, onObserved: observed })
    await settle()
    setViewIdentity({ instance: 'replacement' })
    s.publisher.publish(TabType.AGENT, sentCreation('agent').tabId, { workerId: 'worker' })
    held.resolve(replyFor(s, 'agent'))
    expect(await result).toBe(false)
    expect(observed).not.toHaveBeenCalled()
    expect(s.selection.state.activeByWorkspace.ws).toBeUndefined()
  })
})

describe('useTabCreation reply freshness', () => {
  it('preserves fully hydrated metadata and skips stale mapper side effects', async () => {
    const s = setup()
    const held = deferred<unknown>()
    mockOpenAgent.mockReturnValueOnce(held.promise)
    const result = openKind(s, 'agent')
    await settle()
    const id = sentCreation('agent').tabId
    s.publisher.publish(TabType.AGENT, id, { workerId: 'worker' })
    s.metadata.patch(id, {
      hydrated: true,
      title: 'Current title',
      workingDir: '/current',
      gitToplevel: '/current',
    })
    const reply = s.agentReply(sentAgent())
    reply.agent!.gitStatus = create(GitRepoStatusSchema, { toplevel: '/obsolete', branch: 'obsolete' })
    held.resolve(reply)
    expect(await result).toBe(true)
    expect(s.metadata.get(id)).toMatchObject({
      hydrated: true,
      title: 'Current title',
      workingDir: '/current',
      gitToplevel: '/current',
    })
    expect(s.repoGitStore.get(repoKey('worker', '/obsolete'))).toBeUndefined()
  })

  it('keeps live lifecycle, private title, and catalog labels over an older reply', async () => {
    const s = setup()
    const held = deferred<unknown>()
    mockOpenAgent.mockReturnValueOnce(held.promise)
    const result = openKind(s, 'agent')
    await settle()
    const id = sentCreation('agent').tabId
    s.publisher.publish(TabType.AGENT, id, { workerId: 'worker' })
    const group = create(AvailableOptionGroupSchema, {
      id: 'model',
      currentValue: 'current',
      options: [
        { id: 'current', name: 'Current label' },
      ],
    })
    s.metadata.patchLive(id, {
      agentStatus: AgentStatus.ACTIVE,
      title: 'Private title',
      optionGroups: [group],
    }, ['status', 'catalog'])
    const reply = s.agentReply(sentAgent())
    reply.agent!.status = AgentStatus.STARTING
    reply.agent!.title = 'Old title'
    reply.agent!.optionGroups = [
      create(AvailableOptionGroupSchema, {
        id: 'model',
        options: [
          { id: 'old', name: 'Old label' },
        ],
      }),
    ]
    held.resolve(reply)
    expect(await result).toBe(true)
    expect(s.metadata.get(id)).toMatchObject({
      agentStatus: AgentStatus.ACTIVE,
      title: 'Private title',
      optionGroups: [group],
    })
    expect(getCachedSettingsLabel(AgentProvider.CLAUDE_CODE, 'model', 'current')).toBe('Current label')
  })

  it('keeps an optimistic settings axis while applying the full creation snapshot', async () => {
    const s = setup(() => new Set(['model']))
    const held = deferred<unknown>()
    mockOpenAgent.mockReturnValueOnce(held.promise)
    const result = openKind(s, 'agent')
    await settle()
    const id = sentCreation('agent').tabId
    s.publisher.publish(TabType.AGENT, id, { workerId: 'worker' })
    s.metadata.patch(id, {
      optionValues: { model: 'local-choice' },
    })
    const reply = s.agentReply(sentAgent())
    reply.agent!.optionGroups = [
      create(AvailableOptionGroupSchema, { id: 'model', currentValue: 'old-choice' }),
    ]
    held.resolve(reply)
    expect(await result).toBe(true)
    expect(s.metadata.get(id)?.optionValues?.model).toBe('local-choice')
    expect(s.metadata.get(id)?.hydrated).toBe(true)
  })

  it('keeps terminal hydration incomplete and preserves a newer title', async () => {
    const s = setup()
    const held = deferred<unknown>()
    mockOpenTerminal.mockReturnValueOnce(held.promise)
    const result = openKind(s, 'terminal')
    await settle()
    const id = sentCreation('terminal').tabId
    s.metadata.patch(id, { title: 'Renamed shell' })
    s.publisher.publish(TabType.TERMINAL, id, { workerId: 'worker' })
    held.resolve(replyFor(s, 'terminal'))
    expect(await result).toBe(true)
    expect(s.metadata.get(id)?.title).toBe('Renamed shell')
    expect(s.metadata.get(id)?.hydrated).toBeUndefined()
    expect(s.metadata.get(id)?.terminalStatus).toBeUndefined()
    expect(s.metadata.get(id)?.shellStartDir).toBeUndefined()
  })
})

describe('useTabCreation disposed observers', () => {
  for (const kind of ['agent', 'terminal', 'file', 'image'] as const) {
    it.each(['resolve', 'reject'] as const)(`ignores a late ${kind} request that will %s after disposal`, async (outcome) => {
      const s = setup()
      const held = deferred<unknown>()
      mockFor(kind).mockReturnValueOnce(held.promise)
      const observed = vi.fn()
      const committed = vi.fn()
      const result = openKind(s, kind, { onObserved: observed, onCommitted: committed })
      await settle()
      const id = sentCreation(kind).tabId
      const reply = replyFor(s, kind)
      s.dispose()
      s.metadata.remove(id)
      if (outcome === 'resolve')
        held.resolve(reply)
      else
        held.reject(new Error('late failure'))
      expect(await result).toBe(false)
      await settle()
      expect(mockFor(kind).mock.calls[0]?.[2].signal.aborted).toBe(true)
      expect(s.metadata.get(id)).toBeUndefined()
      expect(observed).not.toHaveBeenCalled()
      expect(committed).not.toHaveBeenCalled()
      expect(s.onError).not.toHaveBeenCalled()
      expect(s.focusEditor).not.toHaveBeenCalled()
    })

    it(`ignores an already fulfilled ${kind} reply across account replacement`, async () => {
      const s = setup()
      const held = deferred<unknown>()
      mockFor(kind).mockReturnValueOnce(held.promise)
      const committed = vi.fn()
      const result = openKind(s, kind, { onCommitted: committed })
      await settle()
      const id = sentCreation(kind).tabId
      held.resolve(replyFor(s, kind))
      setStorageAccountForTests('replacement-account')
      s.setUserId('replacement-account')
      expect(await result).toBe(false)
      await settle()
      expect(s.metadata.get(id)).toBeUndefined()
      expect(committed).not.toHaveBeenCalled()
    })
  }
})

describe('useTabCreation scheduled focus', () => {
  it.each([
    'current',
    'selection',
    'tile',
    'workspace',
    'dialog',
    'account',
    'dispose',
  ] as const)('rechecks the initiating context when %s changes before the frame', async (change) => {
    const s = setup()
    let otherTile: string | null = null
    if (change === 'tile') {
      otherTile = s.layoutStore.splitTile(s.harness.rootTileId, 'horizontal')
      s.layoutStore.setFocusedTile(s.layoutStore.firstLeafIdFor('ws'), 'ws')
    }
    const frames: FrameRequestCallback[] = []
    vi.spyOn(window, 'requestAnimationFrame').mockImplementation((callback) => {
      frames.push(callback)
      return frames.length
    })
    const [viewIdentity, setViewIdentity] = createSignal<unknown>({ instance: 'original' })
    mockOpenAgent.mockImplementationOnce((_worker, request: OpenAgentRequest) => Promise.resolve(s.agentReply(request, true)))
    expect(await openKind(s, 'agent', {
      viewIdentity,
      onObserved: () => setViewIdentity(null),
    })).toBe(true)
    await settle()
    expect(frames.length).toBeGreaterThan(0)
    switch (change) {
      case 'selection':
        s.publisher.publish(TabType.AGENT, 'other-selection', { workerId: 'worker' })
        s.selection.setActive(s.view.getAgentTab('other-selection')!)
        break
      case 'tile': {
        expect(otherTile).toBeTruthy()
        const selected = s.selection.state.activeByWorkspace.ws
        s.layoutStore.setFocusedTile(otherTile, 'ws')
        expect(s.selection.state.activeByWorkspace.ws).toBe(selected)
        break
      }
      case 'workspace':
        s.setActiveWorkspaceId('other')
        break
      case 'dialog':
        setViewIdentity({ instance: 'replacement' })
        break
      case 'account':
        setStorageAccountForTests('replacement-account')
        s.setUserId('replacement-account')
        break
      case 'dispose':
        s.dispose()
        break
      case 'current': break
    }
    for (const frame of frames)
      frame(0)
    expect(s.focusEditor).toHaveBeenCalledTimes(change === 'current'
      ? 1
      : 0)
  })
})

describe('useTabCreation receipt validation', () => {
  it.each([
    'tab',
    'kind',
    'workspace',
    'tile',
    'position',
    'agent-id',
    'agent-worker',
  ] as const)('retains an uncertain attempt after an invalid %s receipt field', async (field) => {
    const s = setup()
    mockOpenAgent.mockImplementationOnce((_worker, request: OpenAgentRequest) => {
      const reply = s.agentReply(request)
      switch (field) {
        case 'tab':
          reply.publication!.tab!.tabId = 'different'
          break
        case 'kind':
          reply.publication!.tab!.tabType = TabType.FILE
          break
        case 'workspace':
          reply.publication!.workspaceId = 'different'
          break
        case 'tile':
          reply.publication!.tileId = ''
          break
        case 'position':
          reply.publication!.position = ''
          break
        case 'agent-id':
          reply.agent!.id = 'different'
          break
        case 'agent-worker':
          reply.agent!.workerId = 'different'
          break
      }
      return Promise.resolve(reply)
    })
    expect(await openKind(s, 'agent')).toBe(false)
    const id = sentCreation('agent').tabId
    expect(s.metadata.get(id)?.hydrated).toBeUndefined()
    expect(s.onError).toHaveBeenCalledOnce()
    mockOpenAgent.mockImplementationOnce((_worker, request: OpenAgentRequest) => Promise.resolve(s.agentReply(request, true)))
    expect(await openKind(s, 'agent')).toBe(true)
    expect(sentCreation('agent', 1).tabId).toBe(id)
  })
})

describe('useTabCreation terminal identity', () => {
  it('retains the exact attempt after a mismatched terminal identity', async () => {
    const s = setup()
    mockOpenTerminal.mockImplementationOnce(() => ({
      ...replyFor(s, 'terminal'),
      terminalId: 'different',
    }))
    const observed = vi.fn()
    expect(await openKind(s, 'terminal', { onObserved: observed })).toBe(false)
    const first = mockOpenTerminal.mock.calls[0]![1]
    const id = sentCreation('terminal').tabId
    expect(s.metadata.get(id)?.title).toBeUndefined()
    expect(s.metadata.get(id)?.hydrated).toBeUndefined()
    expect(observed).not.toHaveBeenCalled()
    mockOpenTerminal.mockImplementationOnce(() => {
      s.publisher.publish(TabType.TERMINAL, id, { workerId: 'worker' })
      return replyFor(s, 'terminal')
    })
    expect(await openKind(s, 'terminal', { onObserved: observed })).toBe(true)
    expect(mockOpenTerminal.mock.calls[1]![1]).toEqual(first)
    expect(observed).toHaveBeenCalledOnce()
  })
})

describe('useTabCreation workspace admission', () => {
  it('refuses a missing Worker before creating a workspace', async () => {
    const s = setup()
    mockCreateWorkspace.mockResolvedValue({ workspaceId: 'should-not-exist' })
    expect(await s.coordinator.openWorkspace({
      ...s.origin,
      workerId: '',
      title: 'Workspace',
      request: s.request,
    })).toBe(false)
    expect(mockCreateWorkspace).not.toHaveBeenCalled()
    expect(mockOpenAgent).not.toHaveBeenCalled()
    expect(s.onError).toHaveBeenCalledOnce()
  })
})

describe('useTabCreation captured origin', () => {
  it('keeps the initiating callback and destination when the caller changes its object', async () => {
    const s = setup()
    seedWorkspace(s.harness, 'different', 'different-root')
    s.layoutStore.setFocusedTile('different-root', 'different')
    const held = deferred<unknown>()
    mockOpenAgent.mockReturnValueOnce(held.promise)
    const original = vi.fn()
    const replacement = vi.fn()
    const input = { ...s.origin, request: s.request, onObserved: original }
    const result = s.coordinator.openAgent(input)
    await settle()
    input.workspaceId = 'different'
    input.onObserved = replacement
    held.resolve(s.agentReply(sentAgent(), true))
    expect(await result).toBe(true)
    expect(original).toHaveBeenCalledOnce()
    expect(replacement).not.toHaveBeenCalled()
    expect(s.selection.state.activeByWorkspace.ws).toBeDefined()
  })

  it('keeps the original Worker while workspace creation waits', async () => {
    const s = setup()
    const held = deferred<{ workspaceId: string }>()
    mockCreateWorkspace.mockReturnValueOnce(held.promise)
    mockOpenAgent.mockReturnValue(new Promise(() => {}))
    const input = {
      ...s.origin,
      title: 'Captured workspace',
      request: s.request,
      seedGitFromActiveTab: false,
    }
    const result = s.coordinator.openWorkspace(input)
    await settle()
    input.workerId = 'different-worker'
    input.seedGitFromActiveTab = true
    seedWorkspace(s.harness, 'ws-new', 'new-root')
    held.resolve({ workspaceId: 'ws-new' })
    await settle()
    expect(mockOpenAgent.mock.calls[0]?.[0]).toBe('worker')
    s.dispose()
    expect(await result).toBe(false)
  })
})

describe('useTabCreation captured placement', () => {
  it('captures the selected reference and preserves it across an uncertain retry', async () => {
    const s = setup()
    s.publisher.publish(TabType.AGENT, 'first', { workerId: 'worker', position: 'a' })
    s.publisher.publish(TabType.AGENT, 'last', { workerId: 'worker', position: 'z' })
    s.selection.setActive(s.view.getAgentTab('first')!)
    const enqueue = vi.spyOn(getCRDTBridge()!, 'enqueue')
    const held = deferred<unknown>()
    mockOpenAgent.mockReturnValueOnce(held.promise)
    const initial = openKind(s, 'agent')
    await settle()
    const request = sentAgent()
    expect(request.creation?.placement).toMatchObject({
      workspaceId: 'ws',
      destination: { case: 'tileId', value: s.harness.rootTileId },
      insertion: TabInsertion.AFTER,
      relativeTabId: 'first',
    })
    s.selection.setActive(s.view.getAgentTab('last')!)
    held.reject(new ChannelError('transport', 'unknown outcome'))
    expect(await initial).toBe(false)
    mockOpenAgent.mockImplementationOnce((_worker, retry: OpenAgentRequest) => Promise.resolve(s.agentReply(retry, true)))
    expect(await openKind(s, 'agent')).toBe(true)
    expect(sentAgent(1).creation).toEqual(request.creation)
    expect(s.view.forTile(s.harness.rootTileId).map(tab => tab.id)).toEqual(['first', request.creation!.tabId, 'last'])
    expect(enqueue).not.toHaveBeenCalled()
  })

  it('requests last insertion without a reference in an empty tile', async () => {
    const s = setup()
    mockOpenAgent.mockImplementationOnce((_worker, request: OpenAgentRequest) => Promise.resolve(s.agentReply(request, true)))
    expect(await openKind(s, 'agent')).toBe(true)
    expect(sentCreation('agent').placement).toMatchObject({ insertion: TabInsertion.LAST, relativeTabId: '' })
  })
})

describe('useTabCreation acceptsEarlyMetadata', () => {
  it('accepts only the current matching attempt through an uncertain result and retires it with canonical removal', async () => {
    const s = setup()
    const response = deferred<ReturnType<typeof s.agentReply>>()
    mockOpenAgent.mockReturnValue(response.promise)
    const observed = s.coordinator.openAgent({ ...s.origin, request: s.request })
    await settle()
    const id = sentAgent().creation!.tabId
    expect(s.coordinator.acceptsEarlyMetadata(id, TabType.AGENT, 'worker')).toBe(true)
    expect(s.coordinator.acceptsEarlyMetadata('unknown', TabType.AGENT, 'worker')).toBe(false)
    expect(s.coordinator.acceptsEarlyMetadata(id, TabType.FILE, 'worker')).toBe(false)
    expect(s.coordinator.acceptsEarlyMetadata(id, TabType.AGENT, 'other')).toBe(false)
    response.reject(new Error('Acknowledgement was lost.'))
    expect(await observed).toBe(false)
    expect(s.coordinator.acceptsEarlyMetadata(id, TabType.AGENT, 'worker')).toBe(true)
    s.publisher.publish(TabType.AGENT, id, { workerId: 'worker' })
    s.publisher.deliver([
      tombstoneTab(s.publisher.context, TabType.AGENT, id),
    ])
    expect(s.coordinator.acceptsEarlyMetadata(id, TabType.AGENT, 'worker')).toBe(false)
    s.dispose()
    expect(s.coordinator.acceptsEarlyMetadata(id, TabType.AGENT, 'worker')).toBe(false)
  })
})
