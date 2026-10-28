import type { Page } from '@playwright/test'
import type { AgentInputQueueSnapshot } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { MockModelScenarioStatus } from '../helpers/mockModelScript'
import type { ModelScript } from '../helpers/modelScriptFixture'
import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { Buffer } from 'node:buffer'
import { join, resolve, sep } from 'node:path'
import { create } from '@bufbuild/protobuf'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentChatMessageSchema, AgentInputQueueSnapshotSchema, AgentProvider, ContentCompression, MessageCompletion, MessageSource } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { RUN_ROOT_ENV } from '../helpers/runRoot'
import { attachMuseChildReadinessFailure, MUSE_CHILD_GATE, openMuseChild, readMuseNativeControlRecords, readMuseNativeSessionLogs } from './subagent-common.muse'

const probe = vi.hoisted(() => {
  const row = { marker: 'row', filter: vi.fn(), first: vi.fn() }
  row.filter.mockReturnValue(row)
  row.first.mockReturnValue(row)
  return {
    row,
    readFile: vi.fn<(path: string, encoding?: string) => Promise<Buffer | string>>(),
    privatePath: vi.fn<(path: string, root: string) => void>(),
    queue: vi.fn<typeof import('../helpers/nativeInputQueueIdle').readNativeInputQueue>(),
    snapshot: vi.fn<typeof import('../helpers/nativeMessages').readNativeMessageSnapshot>(),
    attach: vi.fn<(name: string, options: { body?: string | Buffer, contentType?: string }) => Promise<void>>(),
    status: vi.fn<ModelScript['status']>(),
    release: vi.fn<ModelScript['releaseGateIfHeld']>(),
    openChild: vi.fn<() => Promise<string>>(),
    readiness: undefined as AgentInputQueueSnapshot | undefined,
    fresh: undefined as AgentInputQueueSnapshot | undefined,
    gateHeld: false,
    waitFailure: new Error('the original readiness wait failed'),
    reportFailure: undefined as Error | undefined,
    assertionFailure: undefined as unknown,
    sent: [] as string[],
  }
})

vi.mock('node:fs/promises', async (importOriginal) => {
  const actual = await importOriginal<typeof import('node:fs/promises')>()
  return {
    ...actual,
    readFile: probe.readFile,
    default: { ...actual, readFile: probe.readFile },
  }
})
vi.mock('../helpers/nativePrivatePath', () => ({ assertPrivateNativePath: probe.privatePath }))
vi.mock('../helpers/nativeInputQueueIdle', () => ({ readNativeInputQueue: probe.queue }))
vi.mock('../helpers/nativeMessages', async importOriginal => ({
  ...await importOriginal<typeof import('../helpers/nativeMessages')>(),
  readNativeMessageSnapshot: probe.snapshot,
}))
vi.mock('../helpers/nativeScenario', () => ({ selectedAgentTabId: async () => 'parent-agent' }))
vi.mock('../helpers/providerToolCalls', () => ({
  bashToolCall: (_provider: unknown, id: string) => ({ id }),
  codeExecutionToolCall: (_provider: unknown, id: string) => ({ id }),
  museSubmitResultToolCall: (id: string) => ({ id }),
}))
vi.mock('../helpers/subagentRegistry', () => ({
  backgroundTaskRows: () => probe.row,
  expandBackgroundTasksSection: async () => {},
  openChildTabFromRow: probe.openChild,
}))
vi.mock('../helpers/ui', () => ({
  assistantBubbles: () => probe.row,
  sendMessage: async (_page: Page, text: string) => { probe.sent.push(text) },
  tabById: () => ({ click: async () => {} }),
  waitForAgentIdle: async () => {},
}))
vi.mock('../helpers/retryUntilPass', () => ({
  retryUntilPass: async (attempt: () => Promise<unknown>) => {
    try {
      return await attempt()
    }
    catch (error) {
      probe.assertionFailure = error
      throw probe.waitFailure
    }
  },
}))
vi.mock('@playwright/test', async () => {
  const { expect } = await import('vitest')
  return {
    expect: (value: unknown) => value === probe.row ? { toHaveAttribute: async () => {} } : expect(value),
    test: {
      info: () => {
        if (probe.reportFailure)
          throw probe.reportFailure
        return { attach: probe.attach }
      },
    },
  }
})

const runRoot = resolve('/isolated/leapmux-e2e-readiness')
const dataHome = join(runRoot, 'agent-home', '.local', 'share')
const rootTime = Date.UTC(2026, 9, 28, 12)

function uuidAt(timestamp: number): string {
  const prefix = timestamp.toString(16).padStart(12, '0')
  return `${prefix.slice(0, 8)}-${prefix.slice(8)}-7000-8000-000000000001`
}

const parentSession = uuidAt(rootTime)
const childSession = uuidAt(rootTime + 86_400_000)
const created = new Date(rootTime)
const rootPath = join(dataHome, 'muse', 'sessions', String(created.getFullYear()), String(created.getMonth() + 1).padStart(2, '0'), String(created.getDate()).padStart(2, '0'), parentSession)
const parentBytes = Buffer.from('{"unknown":9007199254740993}\n{"partial":')
const childBytes = Buffer.from([0x7B, 0x22, 0x78, 0x22, 0x3A, 0xFF])

function queue(activeTurn: boolean, activeTurnSteerable: boolean, revision = 41n): AgentInputQueueSnapshot {
  return create(AgentInputQueueSnapshotSchema, { agentId: 'child-agent', activeTurn, activeTurnSteerable, revision })
}

function transcript(agentId: string): NativeMessageSnapshot {
  const session = agentId === 'parent-agent' ? parentSession : childSession
  return {
    agentId,
    agentSessionId: session,
    messages: [create(AgentChatMessageSchema, {
      id: `${agentId}-message`,
      seq: 9007199254740993n,
      agentSessionId: session,
      agentProvider: AgentProvider.MUSE_CODE,
      source: MessageSource.AGENT,
      spanId: 'native-span',
      completion: MessageCompletion.COMPLETE,
      content: Buffer.from('{"native":"original"}'),
      contentCompression: ContentCompression.NONE,
      supplementalContent: Buffer.from('{"provider":{"nativeRecords":[]}}'),
      supplementalContentCompression: ContentCompression.NONE,
    })],
  }
}

function modelStatus(): MockModelScenarioStatus {
  return { complete: false, nextStep: 1, stepCount: 2, ruleMatches: { held: 1 }, pendingGates: [MUSE_CHILD_GATE], requests: [], unexpectedRequests: [] }
}

function native(): ManagedNativeScenarioContext {
  return {
    page: {} as Page,
    provider: AgentProvider.MUSE_CODE,
    workspaceId: 'workspace',
    providerAgent: { provider: AgentProvider.MUSE_CODE, prefix: 'muse' },
    leapmuxServer: { hubUrl: 'http://127.0.0.1:1', adminToken: 'mock', workerId: 'worker', agentEnv: { XDG_DATA_HOME: dataHome } },
    modelScript: {
      id: 'muse-readiness-unit',
      testDeadline: () => undefined,
      prompt: text => text,
      rule: async () => {},
      queue: async () => 0,
      requestAt: async () => { throw new Error('The readiness helper must not request a model receipt.') },
      fallback: async () => { throw new Error('The readiness helper must not install a model fallback.') },
      waitForGate: async () => {
        probe.gateHeld = true
        return modelStatus()
      },
      waitForSteps: async () => modelStatus(),
      status: probe.status,
      releaseGate: async () => { throw new Error('The readiness helper must use its existing conditional gate release.') },
      releaseGateIfHeld: probe.release,
      allowUnconsumed: () => { throw new Error('The readiness helper must not waive model consumption.') },
    } satisfies ModelScript,
  }
}

interface CapturedReadinessEvidence {
  lastQueue: Record<string, unknown>
  queues: Record<'parent' | 'child', { status: string, value: Record<string, unknown> }>
  transcripts: Record<'parent' | 'child', {
    status: string
    value: {
      agentId: string
      agentSessionId: string
      messages: { original: { status: string }, supplement: { status: string }, originalBytesBase64: string }[]
    }
  }>
  model: { status: string, value: MockModelScenarioStatus }
}

function attachedEvidence(): CapturedReadinessEvidence {
  const call = probe.attach.mock.calls.find(([name]) => name === 'muse-held-child-readiness')
  expect(call).toBeDefined()
  return JSON.parse(String(call![1].body))
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.stubEnv(RUN_ROOT_ENV, runRoot)
  probe.readiness = queue(true, true)
  probe.fresh = queue(false, false, 42n)
  probe.waitFailure = new Error('the original readiness wait failed')
  probe.reportFailure = undefined
  probe.assertionFailure = undefined
  probe.gateHeld = false
  probe.sent = []
  probe.privatePath.mockImplementation(() => {})
  probe.openChild.mockResolvedValue('child-agent')
  probe.readFile.mockImplementation(async path => path.includes(`${sep}subagent${sep}`) ? childBytes : parentBytes)
  probe.status.mockResolvedValue(modelStatus())
  probe.release.mockImplementation(async () => {
    probe.gateHeld = false
    return true
  })
  probe.snapshot.mockImplementation(async (_context, id) => transcript(id))
  let childReads = 0
  probe.queue.mockImplementation(async (_server, id) => id === 'parent-agent'
    ? create(AgentInputQueueSnapshotSchema, { agentId: id, activeTurn: true, revision: 40n })
    : childReads++ === 0 ? probe.readiness! : probe.fresh!)
  probe.attach.mockResolvedValue(undefined)
})

afterEach(() => {
  vi.unstubAllEnvs()
})

describe('readMuseNativeSessionLogs', () => {
  it('reads the exact same-root paths and preserves every raw byte and partial final line', async () => {
    const logs = await readMuseNativeSessionLogs(parentSession, childSession, dataHome)
    expect(probe.readFile.mock.calls).toEqual([
      [join(rootPath, 'session.jsonl')],
      [join(rootPath, 'subagent', childSession, 'session.jsonl')],
    ])
    expect(logs).toEqual({
      parent: { status: 'fulfilled', value: { path: join(rootPath, 'session.jsonl'), bytes: parentBytes } },
      child: { status: 'fulfilled', value: { path: join(rootPath, 'subagent', childSession, 'session.jsonl'), bytes: childBytes } },
    })
    expect(probe.privatePath.mock.calls).toEqual(probe.readFile.mock.calls.map(([path]) => [path, runRoot]))
  })

  it.each([
    { parent: 'not-a-session', want: 'UUIDv7 root session' },
    { parent: parentSession.replace('-7000-', '-4000-'), want: 'UUIDv7 root session' },
    { parent: parentSession.replace('-8000-', '-0000-'), want: 'UUIDv7 root session' },
    { parent: 'ffffffff-ffff-7000-8000-000000000001', want: 'four-digit storage date' },
    { child: '../another-session', want: 'distinct child UUID' },
    { child: parentSession, want: 'distinct child UUID' },
    { data: '', want: 'absolute normalized isolated data directory' },
    { data: 'relative/data', want: 'absolute normalized isolated data directory' },
    { data: `${dataHome}${sep}..${sep}share`, want: 'absolute normalized isolated data directory' },
    { data: `${dataHome}\0`, want: 'absolute normalized isolated data directory' },
    { data: resolve('/isolated/leapmux-e2e-another/agent-home/.local/share'), want: 'outside the current isolated run' },
    { data: runRoot, want: 'outside the current isolated run' },
    { root: '', want: 'current isolated run root' },
    { root: 'relative/leapmux-e2e-run', want: 'current isolated run root' },
    { root: resolve('/isolated/ordinary-directory'), want: 'current isolated run root' },
  ])('refuses invalid identity, storage date or isolation before filesystem access: %j', async (input) => {
    if ('root' in input)
      vi.stubEnv(RUN_ROOT_ENV, input.root)
    await expect(readMuseNativeSessionLogs(input.parent ?? parentSession, input.child ?? childSession, input.data ?? dataHome)).rejects.toThrow(input.want)
    expect(probe.privatePath).not.toHaveBeenCalled()
    expect(probe.readFile).not.toHaveBeenCalled()
  })

  it.each(['parent', 'child'])('retains the other log when the %s file read fails', async (failed) => {
    const failure = new Error('the exact file cannot be read')
    probe.readFile.mockImplementation(async (path) => {
      const child = path.includes(`${sep}subagent${sep}`)
      if (child === (failed === 'child'))
        throw failure
      return child ? childBytes : parentBytes
    })
    const logs = await readMuseNativeSessionLogs(parentSession, childSession, dataHome)
    expect(logs[failed as 'parent' | 'child']).toEqual({ status: 'rejected', reason: failure })
    expect(logs[failed === 'parent' ? 'child' : 'parent'].status).toBe('fulfilled')
    expect(probe.readFile).toHaveBeenCalledTimes(2)
  })

  it('reads only the known parent when the child identity is unresolved', async () => {
    const logs = await readMuseNativeSessionLogs(parentSession, undefined, dataHome)
    expect(logs.parent.status).toBe('fulfilled')
    expect(logs.child.status).toBe('rejected')
    expect(probe.readFile.mock.calls).toEqual([[join(rootPath, 'session.jsonl')]])
  })

  it('retains a containment refusal without reading or searching the refused file', async () => {
    probe.privatePath.mockImplementation((path) => {
      if (path.includes(`${sep}subagent${sep}`))
        throw new Error('the child symlink leaves this run')
    })
    const logs = await readMuseNativeSessionLogs(parentSession, childSession, dataHome)
    expect(logs.parent.status).toBe('fulfilled')
    expect(logs.child.status).toBe('rejected')
    expect(probe.readFile.mock.calls).toEqual([[join(rootPath, 'session.jsonl')]])
  })
})

describe('readMuseNativeControlRecords', () => {
  it('keeps the existing native control selection and malformed-line failure', async () => {
    const records = [
      { payload: { kind: 'subagent_control' } },
      { payload: { kind: 'command_intake' } },
      { payload_type: 'runtime.command_intake.received', payload: { kind: 'future' } },
      { payload: { kind: 'run', event: { kind: 'inbox_item_drained' } } },
      { payload: { kind: 'run', event: { kind: 'other' } } },
    ]
    probe.readFile.mockResolvedValue(`${records.map(record => JSON.stringify(record)).join('\n')}\n`)
    expect(await readMuseNativeControlRecords(parentSession, dataHome)).toEqual(records.slice(0, 4))
    probe.readFile.mockResolvedValue('{"unfinished":')
    await expect(readMuseNativeControlRecords(parentSession, dataHome)).rejects.toThrow(SyntaxError)
  })
})

describe('openMuseChild', () => {
  it.each([
    { activeTurn: false, activeTurnSteerable: true },
    { activeTurn: true, activeTurnSteerable: false },
  ])('captures the separate readiness flags before releasing the model gate: %j', async (flags) => {
    probe.readiness = queue(flags.activeTurn, flags.activeTurnSteerable)
    probe.attach.mockImplementation(async () => {
      expect(probe.gateHeld).toBe(true)
    })
    const context = native()
    await expect(openMuseChild(context, true)).rejects.toBe(probe.waitFailure)
    expect(probe.assertionFailure).toBeInstanceOf(Error)
    const evidence = attachedEvidence()
    expect(evidence.lastQueue).toMatchObject({ ...flags, revision: '41' })
    expect(evidence.queues.child.value).toMatchObject({ activeTurn: false, activeTurnSteerable: false, revision: '42' })
    expect(evidence.model.value).toMatchObject({ pendingGates: [MUSE_CHILD_GATE], ruleMatches: { held: 1 }, nextStep: 1, stepCount: 2 })
    expect(evidence.transcripts.child.value).toMatchObject({ agentId: 'child-agent', agentSessionId: childSession })
    expect(evidence.transcripts.child.value.messages[0]).toMatchObject({
      seq: '9007199254740993',
      sessionId: childSession,
      spanId: 'native-span',
      completion: MessageCompletion.COMPLETE,
      original: { status: 'fulfilled', value: { native: 'original' } },
      supplement: { status: 'fulfilled', value: { provider: { nativeRecords: [] } } },
      originalBytesBase64: Buffer.from('{"native":"original"}').toString('base64'),
    })
    expect(probe.attach.mock.calls.find(([name]) => name === 'muse-held-parent-session.jsonl')?.[1].body).toEqual(parentBytes)
    expect(probe.attach.mock.calls.find(([name]) => name === 'muse-held-child-session.jsonl')?.[1].body).toEqual(childBytes)
    expect(probe.release).not.toHaveBeenCalled()
    expect(probe.gateHeld).toBe(true)
    expect(probe.sent).toEqual(['Run the native child workflow.'])
  })

  it('keeps independent evidence when a fresh queue and model status read fail', async () => {
    probe.readiness = queue(false, true)
    probe.queue.mockResolvedValueOnce(probe.readiness).mockRejectedValue(new Error('fresh queue unavailable'))
    probe.status.mockRejectedValue(new Error('model status unavailable'))
    const context = native()
    await expect(openMuseChild(context, true)).rejects.toBe(probe.waitFailure)
    const evidence = attachedEvidence()
    expect(evidence.queues.parent.status).toBe('rejected')
    expect(evidence.queues.child.status).toBe('rejected')
    expect(evidence.model.status).toBe('rejected')
    expect(evidence.transcripts.parent.status).toBe('fulfilled')
    expect(evidence.transcripts.child.status).toBe('fulfilled')
    expect(probe.readFile).toHaveBeenCalledTimes(2)
  })

  it('captures the known parent without an empty child read when opening the child tab fails', async () => {
    const failure = new Error('the child tab has no resolved ID')
    probe.openChild.mockRejectedValue(failure)
    await expect(openMuseChild(native(), true)).rejects.toBe(failure)
    expect(probe.snapshot.mock.calls.map(([, id]) => id)).toEqual(['parent-agent'])
    expect(probe.queue.mock.calls.map(([, id]) => id)).toEqual(['parent-agent'])
    expect(attachedEvidence().transcripts.child.status).toBe('rejected')
    expect(probe.readFile.mock.calls).toEqual([[join(rootPath, 'session.jsonl')]])
    expect(probe.release).not.toHaveBeenCalled()
  })

  it.each(['attachments', 'reporter'])('rethrows the original readiness error when %s fail', async (failure) => {
    probe.readiness = queue(false, false)
    if (failure === 'attachments')
      probe.attach.mockRejectedValue(new Error('attachment storage refused'))
    else
      probe.reportFailure = new Error('the report is unavailable')
    await expect(openMuseChild(native(), true)).rejects.toBe(probe.waitFailure)
    expect(probe.release).not.toHaveBeenCalled()
    if (failure === 'attachments')
      expect(probe.attach).toHaveBeenCalledTimes(3)
  })

  it.each([false, true])('performs no diagnostic reads when held=%s succeeds', async (held) => {
    const context = native()
    const child = await openMuseChild(context, held)
    expect(child.childId).toBe('child-agent')
    expect(child.parentId).toBe('parent-agent')
    expect(child.finish).toBeTypeOf('function')
    expect(probe.snapshot).not.toHaveBeenCalled()
    expect(probe.status).not.toHaveBeenCalled()
    expect(probe.readFile).not.toHaveBeenCalled()
    expect(probe.attach).not.toHaveBeenCalled()
    expect(probe.release).not.toHaveBeenCalled()
    expect(probe.queue).toHaveBeenCalledTimes(held ? 1 : 0)
    await child.finish()
    expect(probe.release).toHaveBeenCalledExactlyOnceWith(MUSE_CHILD_GATE)
    expect(probe.snapshot).not.toHaveBeenCalled()
    expect(probe.readFile).not.toHaveBeenCalled()
  })
})

describe('attachMuseChildReadinessFailure', () => {
  it('keeps child and queue evidence when the owning parent transcript cannot be read', async () => {
    probe.snapshot.mockImplementation(async (_context, id) => {
      if (id === 'parent-agent')
        throw new Error('the parent transcript is unavailable')
      return transcript(id)
    })
    await attachMuseChildReadinessFailure(native(), { parentId: 'parent-agent', childId: 'child-agent' }, { attach: probe.attach })
    const evidence = attachedEvidence()
    expect(evidence.transcripts.parent.status).toBe('rejected')
    expect(evidence.transcripts.child.status).toBe('fulfilled')
    expect(evidence.queues.parent.status).toBe('fulfilled')
    expect(evidence.queues.child.status).toBe('fulfilled')
    expect(evidence.model.status).toBe('fulfilled')
    expect(probe.readFile).not.toHaveBeenCalled()
  })

  it('retains raw message bytes when a provider payload cannot decode', async () => {
    const snapshot = transcript('child-agent')
    snapshot.messages[0]!.content = Buffer.from('{"partial":')
    probe.snapshot.mockImplementation(async (_context, id) => id === 'child-agent' ? snapshot : transcript(id))
    await attachMuseChildReadinessFailure(native(), { parentId: 'parent-agent', childId: 'child-agent' }, { attach: probe.attach })
    const row = attachedEvidence().transcripts.child.value.messages[0]!
    expect(row.original.status).toBe('rejected')
    expect(Buffer.from(row.originalBytesBase64, 'base64')).toEqual(Buffer.from('{"partial":'))
    expect(row.supplement.status).toBe('fulfilled')
  })
})
