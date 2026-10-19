import type { MessageInitShape } from '@bufbuild/protobuf'
import type { Page } from '@playwright/test'
import type { AgentInfo, BackgroundTaskItem } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { create } from '@bufbuild/protobuf'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentInfoSchema, AgentProvider, AgentStatus, BackgroundTaskItemSchema, BackgroundTaskKind, BackgroundTaskStatus, ListAgentMessagesResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { matchesRequest } from '../helpers/mockModelScript'
import { startWaitLimitForTests } from '../helpers/testDeadline'
import { copilotChildTaskId, copilotChildTaskMatcher, readCopilotChildTaskId } from './childIdentity'

const native = vi.hoisted(() => ({
  parent: undefined as AgentInfo | undefined,
  parentReads: [] as (AgentInfo | null)[],
  children: [] as AgentInfo[],
  tasks: [] as BackgroundTaskItem[],
  sidebarReads: 0,
  onSidebar: undefined as (() => void) | undefined,
}))

vi.mock('../helpers/nativeMessages', async importOriginal => ({
  ...await importOriginal<typeof import('../helpers/nativeMessages')>(),
  readNativeMessageSnapshot: async () => { throw new Error('The Worker stores no native child-start frames.') },
}))

vi.mock('../helpers/nativeScenario', () => ({
  nativeAgentById: async () => native.parentReads.length ? native.parentReads.shift() : native.parent,
  nativeAgentsByIds: async () => native.children,
}))

vi.mock('../helpers/nativeSidebarSnapshot', () => ({
  readNativeSidebarSnapshot: async () => {
    native.sidebarReads++
    native.onSidebar?.()
    return create(ListAgentMessagesResponseSchema, { todosLoaded: true, backgroundTasksLoaded: true, backgroundTasks: native.tasks })
  },
}))

function parent(fields: MessageInitShape<typeof AgentInfoSchema> = {}): AgentInfo {
  return create(AgentInfoSchema, {
    id: 'parent',
    workerId: 'worker',
    rootAgentId: 'parent',
    agentSessionId: 'native-session',
    agentProvider: AgentProvider.GITHUB_COPILOT,
    status: AgentStatus.ACTIVE,
    ...fields,
  })
}

function child(fields: MessageInitShape<typeof AgentInfoSchema> = {}): AgentInfo {
  return create(AgentInfoSchema, {
    id: 'child',
    workerId: 'worker',
    parentAgentId: 'parent',
    rootAgentId: 'parent',
    agentProvider: AgentProvider.GITHUB_COPILOT,
    status: AgentStatus.ACTIVE,
    spawnSpanId: 'spawn',
    providerChildKey: 'native-child',
    ...fields,
  })
}

function task(owner: AgentInfo): BackgroundTaskItem {
  return create(BackgroundTaskItemSchema, {
    id: owner.providerChildKey,
    childAgentId: owner.id,
    parentAgentId: owner.parentAgentId,
    kind: BackgroundTaskKind.SUBAGENT,
    status: BackgroundTaskStatus.RUNNING,
  })
}

let endWaitLimit: (() => void) | undefined
const context = { page: {} as Page, leapmuxServer: { workerId: 'worker', hubUrl: 'http://unused.invalid', adminToken: 'unused' } }

beforeEach(() => {
  native.parent = parent()
  native.parentReads = []
  native.children = [child({ id: 'other-child', spawnSpanId: 'other-call', providerChildKey: 'other-native' }), child()]
  native.tasks = native.children.map(task)
  native.sidebarReads = 0
  native.onSidebar = undefined
})

afterEach(() => {
  endWaitLimit?.()
  endWaitLimit = undefined
})

describe('copilotChildTaskMatcher', () => {
  const task = 'NATIVECHILDTASK1 report one word.'
  const selects = (userText: string, matched = task) => matchesRequest(copilotChildTaskMatcher(matched), { protocol: 'openai-chat-completions', userText, systemText: '', body: {} })

  it('selects the child turn after the datetime block that Copilot puts first', () => {
    expect(selects(`<current_datetime>2026-10-06T00:00:00Z</current_datetime>\n${task}\n\nSCENARIO_MARK`)).toBe(true)
    expect(selects(task)).toBe(true)
  })

  it('refuses a turn that holds the task inside a line', () => {
    expect(selects(`Delegate this: ${task}`)).toBe(false)
  })

  it('reads regular expression syntax in the task as plain text', () => {
    expect(selects('Read a.b once', 'Read a.b once')).toBe(true)
    expect(selects('Read aXb once', 'Read a.b once')).toBe(false)
  })

  it('refuses an empty task', () => {
    expect(() => copilotChildTaskMatcher('')).toThrow('nonempty task')
  })
})

describe('copilotChildTaskId', () => {
  it('selects the actual native child through the exact spawning call and owner', () => {
    expect(copilotChildTaskId(parent(), native.children, 'spawn')).toBe('native-child')
  })

  it.each(['', ' '])('refuses an invalid native parent session: "%s"', (agentSessionId) => {
    expect(() => copilotChildTaskId(parent({ agentSessionId }), [child()], 'spawn')).toThrow('native session and owner IDs')
  })

  it('accepts identical links but refuses conflicting Worker or native identities', () => {
    expect(copilotChildTaskId(parent(), [child(), child()], 'spawn')).toBe('native-child')
    expect(() => copilotChildTaskId(parent(), [child(), child({ id: 'another-child' })], 'spawn')).toThrow('one native child')
    expect(() => copilotChildTaskId(parent(), [child(), child({ providerChildKey: 'conflict' })], 'spawn')).toThrow('conflicting native identities')
  })

  it.each([undefined, null, '', ' ', 0, [], {}])('refuses an invalid native child identity: %j', (providerChildKey) => {
    expect(() => copilotChildTaskId(parent(), [Object.assign(child(), { providerChildKey })], 'spawn')).toThrow('no native child ID')
  })

  it.each(['', ' '])('refuses an empty Worker child identity: "%s"', (id) => {
    expect(() => copilotChildTaskId(parent(), [child({ id })], 'spawn')).toThrow('Worker identity')
  })

  it.each([
    { parentAgentId: 'another-parent' },
    { rootAgentId: 'another-root' },
    { workerId: 'another-worker' },
    { spawnSpanId: 'another-call' },
    { agentProvider: AgentProvider.CLAUDE_CODE },
    { status: AgentStatus.INACTIVE },
  ])('refuses a child outside the exact active owner: %j', (fields) => {
    expect(() => copilotChildTaskId(parent(), [child(fields)], 'spawn')).toThrow('one native child')
  })

  it('refuses absent links and an empty spawn call', () => {
    expect(() => copilotChildTaskId(parent(), [], 'spawn')).toThrow('one native child')
    expect(() => copilotChildTaskId(parent(), [child()], '')).toThrow('spawn call ID')
    expect(() => copilotChildTaskId(parent(), [child()], ' ')).toThrow('spawn call ID')
  })

  it('ignores unrelated links even when their native identity is absent', () => {
    expect(copilotChildTaskId(parent(), [child({ spawnSpanId: 'other', providerChildKey: '' }), child()], 'spawn')).toBe('native-child')
  })
})

describe('readCopilotChildTaskId', () => {
  it('uses exact child links when the Worker stores no subagent-start frame', async () => {
    await expect(readCopilotChildTaskId(context, 'parent', 'spawn')).resolves.toBe('native-child')
  })

  it('waits for the native registry entry instead of choosing a title', async () => {
    const expected = task(child())
    native.tasks = []
    native.onSidebar = () => {
      if (native.sidebarReads === 2)
        native.tasks = [expected]
    }
    await expect(readCopilotChildTaskId(context, 'parent', 'spawn')).resolves.toBe('native-child')
    expect(native.sidebarReads).toBe(2)
  })

  it('refuses a parent session that changes during the read', async () => {
    endWaitLimit = startWaitLimitForTests(500)
    native.parentReads = [parent()]
    native.parent = parent({ agentSessionId: 'replacement-session' })
    await expect(readCopilotChildTaskId(context, 'parent', 'spawn')).rejects.toThrow('parent ownership changed')
  })

  it.each([
    { kind: BackgroundTaskKind.SHELL },
    { status: BackgroundTaskStatus.SUCCEEDED },
    { parentAgentId: 'another-parent' },
  ])('ignores registry entries outside the running child scope: %j', async (fields) => {
    endWaitLimit = startWaitLimitForTests(500)
    native.tasks = [create(BackgroundTaskItemSchema, { ...task(child()), ...fields })]
    await expect(readCopilotChildTaskId(context, 'parent', 'spawn')).rejects.toThrow('no running Copilot child')
  })

  it('ignores an unrequested child that an invalid Worker reply includes', async () => {
    native.tasks = [task(child())]
    native.children.push(child({ id: 'unrequested', providerChildKey: 'unrequested-native' }))
    await expect(readCopilotChildTaskId(context, 'parent', 'spawn')).resolves.toBe('native-child')
  })

  it('refuses a different initial parent or Worker', async () => {
    native.parent = parent({ id: 'other' })
    await expect(readCopilotChildTaskId(context, 'parent', 'spawn')).rejects.toThrow('another parent or Worker')
    native.parent = parent({ workerId: 'other' })
    await expect(readCopilotChildTaskId(context, 'parent', 'spawn')).rejects.toThrow('another parent or Worker')
  })

  it('refuses an empty parent or spawn identity before reading the registry', async () => {
    await expect(readCopilotChildTaskId(context, '', 'spawn')).rejects.toThrow('parent and spawn call IDs')
    await expect(readCopilotChildTaskId(context, 'parent', '')).rejects.toThrow('parent and spawn call IDs')
    expect(native.sidebarReads).toBe(0)
  })
})
