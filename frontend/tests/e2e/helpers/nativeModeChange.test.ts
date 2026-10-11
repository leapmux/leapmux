import type { Locator } from '@playwright/test'
import type { AgentChatMessage, AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { create } from '@bufbuild/protobuf'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentChatMessageSchema, AgentInfoSchema, AgentProvider, AgentStatus, ContentCompression, MessageSource } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { captureStoredNativeModeNotice, chooseNativeModeWithNotice } from './nativeModeChange'

const state = vi.hoisted(() => ({
  events: [] as string[],
  noticeCount: 1,
  requests: 0,
  sendsRequest: false,
  showsThinking: false,
  chooseError: undefined as Error | undefined,
  agent: undefined as AgentInfo | undefined,
  messages: [] as AgentChatMessage[],
  reads: [] as string[],
  onRead: undefined as (() => void) | undefined,
  renderedText: 'Workflow (Default → Plan Mode)',
  renderedQuery: undefined as string | RegExp | undefined,
  thinkingChecks: 0,
  modelStatusReads: 0,
}))

vi.mock('./nativeScenario', () => ({ currentNativeAgent: async () => state.agent }))
vi.mock('./nativeMessages', async (original) => {
  const actual = await original<typeof import('./nativeMessages')>()
  return {
    ...actual,
    readAllAgentMessages: async (_context: unknown, agentId: string) => {
      state.reads.push(agentId)
      state.onRead?.()
      return state.messages
    },
  }
})

vi.mock('@playwright/test', async () => {
  const { expect } = await import('vitest')
  return { expect: (value: unknown) => {
    if (value && typeof value === 'object' && 'count' in value && typeof value.count === 'function') {
      const readCount = value.count
      return { toHaveCount: async (count: number) => expect(await readCount()).toBe(count) }
    }
    return expect(value)
  } }
})

vi.mock('./thinkingIndicatorWatch', () => ({
  thinkingIndicatorShownDuring: async (_page: unknown, action: () => Promise<void>) => {
    state.thinkingChecks++
    await action()
    return state.showsThinking
  },
}))

vi.mock('./ui', () => ({
  chooseSettingsOption: async (_page: unknown, option: string) => {
    state.events.push(`choose ${option}`)
    if (state.chooseError)
      throw state.chooseError
    if (state.sendsRequest)
      state.requests++
  },
  waitForSettingsIdle: async () => { state.events.push('idle') },
  expectSettingsChip: async (_page: unknown, label: string) => { state.events.push(`chip ${label}`) },
  visibleOnly: (locator: Locator) => locator,
}))

const notice = { count: async () => {
  const query = state.renderedQuery
  const matches = typeof query === 'string' ? state.renderedText === query : query?.test(state.renderedText)
  return matches ? state.noticeCount : 0
} }
const context = {
  page: {
    getByText: (text: string | RegExp) => {
      state.renderedQuery = text
      state.events.push('notice query')
      return notice
    },
  },
  modelScript: { status: async () => {
    state.modelStatusReads++
    return { requests: Array.from({ length: state.requests }) }
  } },
} as unknown as ManagedNativeScenarioContext
const change = { groupId: 'collaboration_mode', value: 'plan', label: 'Plan Mode', previousLabel: 'Default', noticeLabel: 'Workflow' }

function settingsChange(overrides: Record<string, unknown> = {}) {
  return {
    type: 'settings_changed',
    changes: {
      collaboration_mode: { old: 'default', new: 'plan', old_label: 'Default', new_label: 'Plan Mode', label: 'Workflow', ...overrides },
    },
  }
}

function storedNotice(
  body: unknown = settingsChange(),
  overrides: Partial<Pick<AgentChatMessage, 'id' | 'seq' | 'agentSessionId' | 'agentProvider' | 'source'>> = {},
): AgentChatMessage {
  return create(AgentChatMessageSchema, {
    id: 'mode-notice',
    seq: 1n,
    agentSessionId: 'session',
    agentProvider: AgentProvider.CODEX,
    source: MessageSource.LEAPMUX,
    contentCompression: ContentCompression.NONE,
    content: new TextEncoder().encode(JSON.stringify(body)),
    ...overrides,
  })
}

async function selectedNotice() {
  return chooseNativeModeWithNotice(context, change)
}

beforeEach(() => {
  state.events = []
  state.noticeCount = 1
  state.requests = 0
  state.sendsRequest = false
  state.showsThinking = false
  state.chooseError = undefined
  state.agent = create(AgentInfoSchema, {
    id: 'agent',
    workerId: 'worker',
    workingDir: '/native/work',
    agentSessionId: 'session',
    agentProvider: AgentProvider.CODEX,
    status: AgentStatus.ACTIVE,
    optionGroups: [{ id: 'collaboration_mode', currentValue: 'default' }],
  })
  state.messages = [storedNotice()]
  state.reads = []
  state.onRead = undefined
  state.renderedText = 'Workflow (Default → Plan Mode)'
  state.renderedQuery = undefined
  state.thinkingChecks = 0
  state.modelStatusReads = 0
})

describe('captureStoredNativeModeNotice', () => {
  function ownerBefore(): AgentInfo {
    if (!state.agent)
      throw new Error('The test requires its Worker agent.')
    return create(AgentInfoSchema, state.agent)
  }

  const storedChange = { ...change, oldValue: 'default' }
  const locator = notice as unknown as Locator

  it('captures a caller-proved notice without a setting choice or activity assertion', async () => {
    state.noticeCount = 0
    state.showsThinking = true
    state.requests = 4
    const result = await captureStoredNativeModeNotice(context, ownerBefore(), storedChange, locator)
    expect(result).toBe(notice)
    await result.assertStored()
    expect(state.events).toEqual([])
    expect(state.thinkingChecks).toBe(0)
    expect(state.modelStatusReads).toBe(0)
    expect(state.reads).toEqual(['agent', 'agent'])
  })

  it.each([
    ['agent', { id: 'other-agent' }],
    ['Worker', { workerId: 'other-worker' }],
    ['provider', { agentProvider: AgentProvider.CLAUDE_CODE }],
    ['working directory', { workingDir: '/other/work' }],
  ])('rejects the wrong %s owner supplied before capture', async (_field, overrides) => {
    const owner = create(AgentInfoSchema, { ...ownerBefore(), ...overrides })
    await expect(captureStoredNativeModeNotice(context, owner, storedChange, locator)).rejects.toThrow('native mode notice owner')
    expect(state.reads).toEqual([])
  })

  it('rejects a Worker owner change during capture', async () => {
    const owner = ownerBefore()
    state.onRead = () => {
      if (!state.agent)
        throw new Error('The test requires its Worker agent.')
      state.agent = create(AgentInfoSchema, { ...state.agent, workerId: 'changed-during-read' })
    }
    await expect(captureStoredNativeModeNotice(context, owner, storedChange, locator)).rejects.toThrow('native mode notice owner changed during the Worker message read')
  })

  it('rejects a changed selected owner after capture', async () => {
    const result = await captureStoredNativeModeNotice(context, ownerBefore(), storedChange, locator)
    if (!state.agent)
      throw new Error('The test requires its Worker agent.')
    state.agent = create(AgentInfoSchema, { ...state.agent, rootAgentId: 'other-root' })
    await expect(result.assertStored()).rejects.toThrow('native mode notice owner')
    expect(state.reads).toEqual(['agent'])
  })

  it('rejects duplicate matching entries during capture', async () => {
    state.messages = [storedNotice({ type: 'notification_thread', messages: [settingsChange(), settingsChange()] })]
    await expect(captureStoredNativeModeNotice(context, ownerBefore(), storedChange, locator)).rejects.toThrow('exactly one stored native mode notice')
  })

  it('rejects a stored entry from another native session during capture', async () => {
    state.messages = [storedNotice(undefined, { agentSessionId: 'other-session' })]
    await expect(captureStoredNativeModeNotice(context, ownerBefore(), storedChange, locator)).rejects.toThrow('exactly one stored native mode notice')
  })
})

describe('chooseNativeModeWithNotice', () => {
  it('confirms the selected chip and its exact single notice after the worker settles', async () => {
    expect(await chooseNativeModeWithNotice(context, change)).toBe(notice)
    expect(state.events).toEqual([
      'choose collaboration_mode-plan',
      'idle',
      'chip Plan Mode',
      'notice query',
    ])
  })

  it.each([0, 2])('rejects a notice count of %i', async (count) => {
    state.noticeCount = count
    await expect(chooseNativeModeWithNotice(context, change)).rejects.toThrow('to be 1')
  })

  it('rejects a model request caused by the settings change', async () => {
    state.sendsRequest = true
    await expect(chooseNativeModeWithNotice(context, change)).rejects.toThrow('to have a length of +0')
    expect(state.events.some(event => event.startsWith('notice '))).toBe(false)
  })

  it('rejects a thinking indicator during the settings change', async () => {
    state.showsThinking = true
    await expect(chooseNativeModeWithNotice(context, change)).rejects.toThrow('to be false')
    expect(state.events.some(event => event.startsWith('notice '))).toBe(false)
  })

  it('preserves a settings failure and checks no later state', async () => {
    const failure = new Error('The mode choice failed.')
    state.chooseError = failure
    await expect(chooseNativeModeWithNotice(context, change)).rejects.toBe(failure)
    expect(state.events).toEqual(['choose collaboration_mode-plan'])
  })

  it.each(['absent', 'empty'])('rejects an %s current mode before it changes settings', async (value) => {
    if (!state.agent)
      throw new Error('The test requires its Worker agent.')
    if (value === 'absent') {
      state.agent.optionGroups = []
    }
    else {
      const group = state.agent.optionGroups[0]
      if (!group)
        throw new Error('The test requires its native option group.')
      group.currentValue = ''
    }
    await expect(chooseNativeModeWithNotice(context, change)).rejects.toThrow('requires a current value')
    expect(state.events).toEqual([])
    expect(state.reads).toEqual([])
  })

  it('keeps the exact stored notice after its rendered row leaves the visible window', async () => {
    const result = await selectedNotice()
    state.noticeCount = 0
    await result.assertStored()
    expect(state.reads).toEqual(['agent', 'agent'])
  })

  it('requires the rendered notice token inside a combined notification paragraph', async () => {
    state.renderedText = 'Effort (Low → High), Workflow (Default → Plan Mode), Interrupted'
    state.messages = [storedNotice({ type: 'notification_thread', messages: [settingsChange(), { type: 'interrupted' }] })]
    await selectedNotice()
  })

  it.each([
    'Other Workflow (Default → Plan Mode)',
    'Workflow (Default → Plan Mode Extra)',
    'Workflow (Default → Plan Mode) extra',
  ])('rejects a rendered token that only contains the required notice: %s', async (text) => {
    state.renderedText = text
    await expect(chooseNativeModeWithNotice(context, change)).rejects.toThrow('to be 1')
  })

  it('finds the notice inside a combined notification thread after unrelated notifications', async () => {
    const result = await selectedNotice()
    state.messages = [storedNotice({
      type: 'notification_thread',
      old_seqs: [1, 4],
      messages: [{ type: 'interrupted' }, settingsChange(), { type: 'agent_status', text: 'Idle' }],
    })]
    state.noticeCount = 0
    await result.assertStored()
  })

  it('accepts a settings notice that also carries an unrelated option change', async () => {
    const result = await selectedNotice()
    const body = settingsChange()
    state.messages = [storedNotice({
      ...body,
      changes: { ...body.changes, effort: { old: 'low', new: 'high', old_label: 'Low', new_label: 'High', label: 'Effort' } },
    })]
    await result.assertStored()
  })

  it.each(['separate rows', 'one thread'])('rejects duplicate matching notices in %s', async (location) => {
    const result = await selectedNotice()
    state.messages = location === 'separate rows'
      ? [storedNotice(), storedNotice(undefined, { id: 'duplicate', seq: 2n })]
      : [storedNotice({ type: 'notification_thread', messages: [settingsChange(), settingsChange()] })]
    await expect(result.assertStored()).rejects.toThrow('exactly one stored native mode notice')
  })

  it.each([
    ['old value', { old: 'ask' }],
    ['new value', { new: 'default' }],
    ['old label', { old_label: 'Ask' }],
    ['new label', { new_label: 'Other Plan' }],
    ['group label', { label: 'Other Workflow' }],
  ])('rejects a stored notice with the wrong %s', async (_field, overrides) => {
    const result = await selectedNotice()
    state.messages = [storedNotice(settingsChange(overrides))]
    await expect(result.assertStored()).rejects.toThrow('exactly one stored native mode notice')
  })

  it('rejects matching values under the wrong option group', async () => {
    const result = await selectedNotice()
    state.messages = [storedNotice({ type: 'settings_changed', changes: { permissionMode: settingsChange().changes.collaboration_mode } })]
    await expect(result.assertStored()).rejects.toThrow('exactly one stored native mode notice')
  })

  it.each([
    ['native session', { agentSessionId: 'other-session' }],
    ['provider', { agentProvider: AgentProvider.CLAUDE_CODE }],
    ['source', { source: MessageSource.AGENT }],
    ['message ID', { id: 'replacement-notice' }],
  ])('rejects a stored notice with the wrong %s owner', async (_field, overrides) => {
    const result = await selectedNotice()
    state.messages = [storedNotice(undefined, overrides)]
    await expect(result.assertStored()).rejects.toThrow('stored native mode notice')
  })

  it.each([
    ['agent', { id: 'other-agent' }],
    ['Worker', { workerId: 'other-worker' }],
    ['provider', { agentProvider: AgentProvider.CLAUDE_CODE }],
    ['working directory', { workingDir: '/other/work' }],
  ])('rejects a different selected %s owner before it reads that transcript', async (_field, overrides) => {
    const result = await selectedNotice()
    if (!state.agent)
      throw new Error('The test requires its Worker agent.')
    state.agent = create(AgentInfoSchema, { ...state.agent, ...overrides })
    await expect(result.assertStored()).rejects.toThrow('native mode notice owner')
    expect(state.reads).toEqual(['agent'])
  })

  it('keeps the initial notice session after the provider starts its first native conversation', async () => {
    if (!state.agent)
      throw new Error('The test requires its Worker agent.')
    state.agent.agentSessionId = ''
    state.messages = [storedNotice(undefined, { agentSessionId: '' })]
    const result = await selectedNotice()
    state.agent.agentSessionId = 'first-native-session'
    await result.assertStored()
  })

  it('rejects an owner change during the Worker message read', async () => {
    const result = await selectedNotice()
    state.onRead = () => {
      if (!state.agent)
        throw new Error('The test requires its Worker agent.')
      state.agent = create(AgentInfoSchema, { ...state.agent, agentSessionId: 'changed-during-read' })
    }
    await expect(result.assertStored()).rejects.toThrow('native mode notice owner changed during the Worker message read')
  })
})
