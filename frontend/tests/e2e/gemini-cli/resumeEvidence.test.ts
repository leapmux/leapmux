import type { Locator, Page, TestInfo } from '@playwright/test'
import type { AgentInfo, ListAgentMessagesResponse } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { ModelScript } from '../helpers/modelScriptFixture'
import type { NativeResumeEvidence, NativeResumeResult } from '../helpers/nativeLifecycle'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { NativeToolOutputFileIO, NativeToolOutputFileStat } from '../helpers/nativeToolOutputFile'
import { Buffer } from 'node:buffer'
import { createHash } from 'node:crypto'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { create, fromJsonString } from '@bufbuild/protobuf'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { MESSAGE_PAGE_LIMIT } from '../../../src/generated/contracts/chat-history'
import { AgentChatMessageSchema, AgentInfoSchema, AgentProvider, AgentStatus, ContentCompression, ListAgentMessagesResponseSchema, MessagePageAnchor } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { captureGeminiResumeEvidence, exerciseGeminiResumeWithEvidence } from './resumeEvidence'

const calls = vi.hoisted(() => ({
  runDir: '',
  selectedId: 'original-worker-agent' as string | null,
  io: undefined as NativeToolOutputFileIO | undefined,
  agent: vi.fn<(context: unknown, id: string) => Promise<AgentInfo | null>>(),
  worker: vi.fn<(id: string, method: string, requestSchema: unknown, responseSchema: unknown, request: unknown) => Promise<ListAgentMessagesResponse>>(),
  lifecycle: vi.fn<typeof import('../helpers/nativeLifecycle')['exerciseSessionResume']>(),
}))

vi.mock('../helpers/server', async importOriginal => ({
  ...await importOriginal<typeof import('../helpers/server')>(),
  getGlobalState: () => ({ tmpDir: calls.runDir }),
}))
vi.mock('../helpers/nativeScenario', async importOriginal => ({
  ...await importOriginal<typeof import('../helpers/nativeScenario')>(),
  nativeAgentById: calls.agent,
}))
vi.mock('../helpers/api', () => ({ getTestChannel: async () => ({ callWorker: calls.worker }) }))
vi.mock('../helpers/nativeLifecycle', () => ({ exerciseSessionResume: calls.lifecycle }))
vi.mock('../helpers/nativeToolOutputFile', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../helpers/nativeToolOutputFile')>()
  return { ...actual, readNativeToolOutputFile: (path: string, maxBytes: number) => actual.readNativeToolOutputFile(path, maxBytes, calls.io) }
})

const scratchRoot = resolve(import.meta.dirname, '../../../../.tmp')
let directory: string

beforeEach(() => {
  vi.resetAllMocks()
  mkdirSync(scratchRoot, { recursive: true })
  directory = mkdtempSync(join(scratchRoot, 'gemini-resume-evidence-unit-'))
  calls.runDir = directory
  calls.selectedId = 'original-worker-agent'
  calls.io = undefined
})

afterEach(() => rmSync(directory, { recursive: true, force: true }))

function object(value: unknown): Record<string, unknown> {
  if (!isObject(value))
    throw new Error('The resume evidence fixture expected an object.')
  return value
}

function objects(value: unknown): Record<string, unknown>[] {
  if (!Array.isArray(value) || value.some(item => !isObject(item)))
    throw new Error('The resume evidence fixture expected an object array.')
  return value.map(object)
}

function fixture(status = AgentStatus.ACTIVE) {
  const home = join(directory, 'isolated-home')
  const workingDir = join(directory, 'native-project')
  const nativeRoot = join(home, '.gemini')
  const project = join(nativeRoot, 'tmp', 'controlled-project')
  const chats = join(project, 'chats')
  const output = join(directory, 'attachments')
  mkdirSync(chats, { recursive: true })
  mkdirSync(workingDir)
  mkdirSync(output)
  writeFileSync(join(nativeRoot, 'projects.json'), JSON.stringify({ projects: { [workingDir]: 'controlled-project' } }))
  writeFileSync(join(project, '.project_root'), workingDir)
  const agent = create(AgentInfoSchema, {
    id: 'original-worker-agent',
    workerId: 'resume-worker',
    agentProvider: AgentProvider.GEMINI_CLI,
    status,
    workingDir,
    agentSessionId: '1df17a24-50da-4090-a7e9-87b38feec450',
    ...(status === AgentStatus.STARTUP_FAILED ? { startupError: 'Native load failed: -32603 No previous sessions found for this project.' } : {}),
  })
  const context: ManagedNativeScenarioContext = {
    provider: agent.agentProvider,
    workspaceId: 'resume-workspace',
    leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'controlled-token', workerId: agent.workerId, agentEnv: { GEMINI_CLI_HOME: home } },
    page: Object.assign({} as Page, {
      locator: (selector: string) => {
        expect(selector).toContain(':visible')
        const tab = Object.assign({} as Locator, {
          first: () => tab,
          count: async () => calls.selectedId === null ? 0 : 1,
          getAttribute: async (name: string) => name === 'data-tab-id' ? calls.selectedId : null,
        })
        return tab
      },
    }),
    get modelScript(): ModelScript { throw new Error('The evidence unit fixture must not access a model.') },
  }
  const attachments: { name: string, path: string }[] = []
  const testInfo: Pick<TestInfo, 'attach' | 'outputPath'> = {
    outputPath: (...names) => join(output, ...names),
    attach: vi.fn<TestInfo['attach']>(async (name, options) => {
      if (!options?.path)
        throw new Error('The evidence fixture expected a complete file attachment.')
      attachments.push({ name, path: options.path })
    }),
  }
  const owner = { sessionId: agent.agentSessionId, projectHash: createHash('sha256').update(workingDir).digest('hex'), kind: 'main' }
  const archive = join(chats, 'session-2026-10-04T01-02-1df17a24.jsonl')
  const text = `${[
    owner,
    { id: 'native-original-user', type: 'user', content: 'Original 漢字 prompt', zero: 0, enabled: false, absent: null, empty: '' },
    { $set: { messages: [] }, nativeMetadata: { zero: 0, enabled: false, absent: null } },
  ].map(record => JSON.stringify(record)).join('\n')}\n`
  writeFileSync(archive, text)
  const row = create(AgentChatMessageSchema, {
    id: 'worker-native-row',
    seq: 1n,
    agentSessionId: agent.agentSessionId,
    spanId: 'native-span',
    parentSpanId: 'native-parent',
    spanType: 'execute',
    content: Uint8Array.from(Buffer.from(JSON.stringify({ sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: 'Native 漢字', zero: 0, enabled: false, absent: null } }))),
    contentCompression: ContentCompression.NONE,
    supplementalContent: Uint8Array.from(Buffer.from(JSON.stringify({ provider: { zero: 0, enabled: false, empty: '', absent: null }, metadata: { elapsed_ms: 0 } }))),
    supplementalContentCompression: ContentCompression.NONE,
  })
  calls.agent.mockResolvedValue(agent)
  calls.worker.mockResolvedValue(create(ListAgentMessagesResponseSchema, { messages: [row] }))
  const receipt = (phase = 'stored') => object(JSON.parse(readFileSync(join(output, `gemini-resume-${phase}-receipt.json`), 'utf8')))
  return { context, testInfo, agent, owner, archive, text, row, attachments, receipt, project, chats, nativeRoot, workingDir }
}

function filePorts(text: string, path: string) {
  const bytes = Buffer.from(text)
  const stat: NativeToolOutputFileStat = { dev: 1, ino: 2, size: bytes.length, mtimeMs: 3, ctimeMs: 4, isFile: () => true, isSymbolicLink: () => false }
  const io: NativeToolOutputFileIO = {
    realpath: vi.fn(() => dirname(path)),
    lstat: vi.fn(() => stat),
    open: vi.fn(() => 0),
    stat: vi.fn(() => stat),
    read: vi.fn(() => bytes),
    close: vi.fn(),
  }
  calls.io = io
  return { io, stat, bytes }
}

describe('captureGeminiResumeEvidence', () => {
  it('retains exact native archive bytes and complete Worker bytes without projecting history', async () => {
    const f = fixture()
    await captureGeminiResumeEvidence(f.context, f.testInfo, { phase: 'stored', prior: f.agent })
    const result = f.receipt()
    expect(object(result.workerRead).state).toBe('complete-observation')
    const decoded = fromJsonString(AgentChatMessageSchema, JSON.stringify(object(objects(result.messages)[0])))
    expect(decoded.content).toEqual(f.row.content)
    expect(decoded.supplementalContent).toEqual(f.row.supplementalContent)
    expect(decoded.seq).toBe(1n)
    expect(decoded.spanId).toBe('native-span')
    expect(decoded.parentSpanId).toBe('native-parent')
    expect(decoded.agentSessionId).toBe(f.agent.agentSessionId)
    const archive = objects(result.archives)[0]
    expect(archive).toMatchObject({ state: 'owned-observation', sessionId: f.agent.agentSessionId, projectHash: f.owner.projectHash, kind: 'main', bytes: Buffer.byteLength(f.text), incompleteTrailingLine: false })
    const path = f.attachments.find(item => item.name === archive?.attachment)?.path
    if (!path)
      throw new Error('The owned native archive attachment is absent.')
    expect(readFileSync(path)).toEqual(readFileSync(f.archive))
    expect(archive?.sha256).toBe(createHash('sha256').update(readFileSync(f.archive)).digest('hex'))
    expect(f.text).toContain('"messages":[]')
    expect(result.request).toBeNull()
    expect(result.failure).toBeNull()
  })

  it.each([AgentStatus.STARTING, AgentStatus.STARTUP_FAILED])('reads pending Worker pages without an ACTIVE wait for status %s', async (status) => {
    const f = fixture(status)
    const later = create(AgentChatMessageSchema, { ...f.row, id: 'worker-later', seq: 9007199254740993n })
    calls.worker.mockReset().mockResolvedValueOnce(create(ListAgentMessagesResponseSchema, { messages: [f.row], hasMore: true })).mockResolvedValueOnce(create(ListAgentMessagesResponseSchema, { messages: [later] }))
    await captureGeminiResumeEvidence(f.context, f.testInfo, { phase: 'opened', prior: f.agent })
    const result = f.receipt('opened')
    expect(object(result.workerRead).state).toBe('complete-observation')
    expect(objects(result.messages).map(item => item.seq)).toEqual(['1', '9007199254740993'])
    expect(calls.agent).toHaveBeenCalledTimes(2)
    expect(calls.worker.mock.calls[0]?.[4]).toEqual({ agentId: f.agent.id, anchor: MessagePageAnchor.OLDEST, limit: MESSAGE_PAGE_LIMIT })
    expect(calls.worker.mock.calls[1]?.[4]).toEqual({ agentId: f.agent.id, anchor: MessagePageAnchor.AFTER, cursorSeq: 1n, limit: MESSAGE_PAGE_LIMIT })
    if (status === AgentStatus.STARTUP_FAILED)
      expect(object(result.workerBefore).startupError).toBe(f.agent.startupError)
  })

  it('refuses a physically impossible pending row with sequence zero', async () => {
    const f = fixture(AgentStatus.STARTUP_FAILED)
    calls.worker.mockResolvedValue(create(ListAgentMessagesResponseSchema, { messages: [create(AgentChatMessageSchema, { ...f.row, seq: 0n })] }))
    await captureGeminiResumeEvidence(f.context, f.testInfo, { phase: 'opened', prior: f.agent })
    const result = f.receipt('opened')
    expect(object(result.workerRead).state).toBe('failed')
    expect(objects(result.messages)[0]?.seq).toBe('0')
  })

  it.each(['absent tab', 'absent agent', 'keeper', 'foreign session', 'foreign child', 'foreign worker'])('records %s without certifying its Worker transcript', async (condition) => {
    const f = fixture(AgentStatus.STARTUP_FAILED)
    if (condition === 'absent tab')
      calls.selectedId = null
    else if (condition === 'absent agent')
      calls.agent.mockResolvedValue(null)
    else if (condition === 'keeper')
      calls.agent.mockResolvedValue(create(AgentInfoSchema, { ...f.agent, workingDir: join(directory, 'keeper') }))
    else if (condition === 'foreign session')
      calls.agent.mockResolvedValue(create(AgentInfoSchema, { ...f.agent, agentSessionId: '91a4ca8d-77a3-4a0f-ab4f-ae770ed92187' }))
    else if (condition === 'foreign child')
      calls.agent.mockResolvedValue(create(AgentInfoSchema, { ...f.agent, parentAgentId: 'another-parent', rootAgentId: 'another-root' }))
    else
      calls.agent.mockResolvedValue(create(AgentInfoSchema, { ...f.agent, workerId: 'another-worker' }))
    await captureGeminiResumeEvidence(f.context, f.testInfo, { phase: 'opened', prior: f.agent })
    expect(object(f.receipt('opened').workerRead).state).not.toBe('complete-observation')
    expect(calls.worker).not.toHaveBeenCalled()
  })

  it.each(['absent ID', 'duplicate ID', 'negative sequence', 'same sequence', 'descending sequence', 'empty more page'])('retains returned bytes and rejects a pending page with %s', async (condition) => {
    const f = fixture(AgentStatus.STARTUP_FAILED)
    const bad = create(AgentChatMessageSchema, { ...f.row, id: 'later-row', seq: 2n })
    if (condition === 'absent ID')
      bad.id = ''
    else if (condition === 'duplicate ID')
      bad.id = f.row.id
    else if (condition === 'negative sequence')
      bad.seq = -1n
    else if (condition === 'same sequence')
      bad.seq = 1n
    else if (condition === 'descending sequence')
      bad.seq = 0n
    calls.worker.mockReset().mockResolvedValueOnce(create(ListAgentMessagesResponseSchema, { messages: [f.row], hasMore: true })).mockResolvedValueOnce(create(ListAgentMessagesResponseSchema, { messages: condition === 'empty more page' ? [] : [bad], hasMore: condition === 'empty more page' }))
    await captureGeminiResumeEvidence(f.context, f.testInfo, { phase: 'opened', prior: f.agent })
    const result = f.receipt('opened')
    expect(object(result.workerRead).state).toBe('failed')
    expect(objects(result.messages)[0]?.id).toBe(f.row.id)
    expect(objects(result.messages)).toHaveLength(condition === 'empty more page' ? 1 : 2)
    expect(calls.worker).toHaveBeenCalledTimes(2)
  })

  it.each(['session', 'parent', 'root', 'spawn', 'provider child', 'directory'])('records unstable ownership after a %s change', async (condition) => {
    const f = fixture(AgentStatus.STARTUP_FAILED)
    const changed = create(AgentInfoSchema, { ...f.agent })
    if (condition === 'session')
      changed.agentSessionId = '91a4ca8d-77a3-4a0f-ab4f-ae770ed92187'
    else if (condition === 'parent')
      changed.parentAgentId = 'changed-parent'
    else if (condition === 'root')
      changed.rootAgentId = 'changed-root'
    else if (condition === 'spawn')
      changed.spawnSpanId = 'changed-span'
    else if (condition === 'provider child')
      changed.providerChildKey = 'changed-provider-child'
    else
      changed.workingDir = join(directory, 'changed-directory')
    calls.agent.mockReset().mockResolvedValueOnce(f.agent).mockResolvedValueOnce(changed)
    await captureGeminiResumeEvidence(f.context, f.testInfo, { phase: 'opened', prior: f.agent })
    expect(object(f.receipt('opened').workerRead).state).toBe('unstable')
    expect(objects(f.receipt('opened').messages)).toHaveLength(1)
  })

  it('preserves multiple exact owned files without selecting a replay boundary', async () => {
    const f = fixture()
    writeFileSync(join(f.chats, 'session-2026-10-04T01-03-1df17a24.json'), JSON.stringify({ ...f.owner, messages: [] }))
    await captureGeminiResumeEvidence(f.context, f.testInfo, { phase: 'stored', prior: f.agent })
    const archives = objects(f.receipt().archives)
    expect(archives).toHaveLength(2)
    expect(archives.map(item => item.state)).toEqual(['owned-observation', 'owned-observation'])
    expect(f.attachments.filter(item => item.name.includes('-archive-'))).toHaveLength(2)
  })

  it.each(['UUID', 'project hash', 'child kind', 'changed header', 'changed set', 'invalid set', 'record before header', 'empty file', 'invalid full line'])('refuses an archive with %s', async (condition) => {
    const f = fixture()
    let text = f.text
    if (condition === 'UUID')
      text = JSON.stringify({ ...f.owner, sessionId: '1df17a24-FOREIGN-native-session' })
    else if (condition === 'project hash')
      text = JSON.stringify({ ...f.owner, projectHash: 'f'.repeat(64) })
    else if (condition === 'child kind')
      text = JSON.stringify({ ...f.owner, kind: 'subagent' })
    else if (condition === 'changed header')
      text += `${JSON.stringify({ ...f.owner, projectHash: 'f'.repeat(64) })}\n`
    else if (condition === 'changed set')
      text += `${JSON.stringify({ $set: { sessionId: '1df17a24-FOREIGN-native-session' } })}\n`
    else if (condition === 'invalid set')
      text += `${JSON.stringify({ $set: null })}\n`
    else if (condition === 'record before header')
      text = `${JSON.stringify({ id: 'early', type: 'user', content: '' })}\n${text}`
    else if (condition === 'empty file')
      text = ''
    else
      text += '{broken}\n'
    writeFileSync(f.archive, text)
    await captureGeminiResumeEvidence(f.context, f.testInfo, { phase: 'stored', prior: f.agent })
    expect(objects(f.receipt().archives)[0]?.state).toBe('refused')
    expect(f.attachments.filter(item => item.name.includes('-archive-'))).toEqual([])
  })

  it('retains a partial final line as bytes without counting it as a complete native record', async () => {
    const f = fixture()
    const text = `${f.text}{"id":"native-incomplete"`
    writeFileSync(f.archive, text)
    await captureGeminiResumeEvidence(f.context, f.testInfo, { phase: 'stored', prior: f.agent })
    const archive = objects(f.receipt().archives)[0]
    expect(archive?.incompleteTrailingLine).toBe(true)
    const path = f.attachments.find(item => item.name === archive?.attachment)?.path
    if (!path)
      throw new Error('The partial native archive attachment is absent.')
    expect(readFileSync(path, 'utf8')).toBe(text)
  })

  it.each(['registry', 'project marker'])('refuses a foreign %s before attaching archive contents', async (condition) => {
    const f = fixture()
    if (condition === 'registry')
      writeFileSync(join(f.nativeRoot, 'projects.json'), JSON.stringify({ projects: { [f.workingDir]: '../foreign' } }))
    else
      writeFileSync(join(f.project, '.project_root'), join(directory, 'foreign-project'))
    await captureGeminiResumeEvidence(f.context, f.testInfo, { phase: 'stored', prior: f.agent })
    expect(objects(f.receipt().archives)[0]?.state).toBe('refused')
    expect(f.attachments.filter(item => item.name.includes('-archive-'))).toEqual([])
  })

  it('preserves a legacy root archive without kind and an empty native message', async () => {
    const f = fixture()
    const owner = { sessionId: f.owner.sessionId, projectHash: f.owner.projectHash, messages: [{ id: 'empty-native', type: 'gemini', content: '' }] }
    writeFileSync(f.archive, JSON.stringify(owner))
    await captureGeminiResumeEvidence(f.context, f.testInfo, { phase: 'stored', prior: f.agent })
    expect(objects(f.receipt().archives)[0]).toMatchObject({ state: 'owned-observation', kind: null })
  })

  it('rejects a symlinked archive through the existing stable reader', async () => {
    const f = fixture()
    const ports = filePorts(f.text, f.archive)
    ports.io.lstat = () => ({ ...ports.stat, isSymbolicLink: () => true })
    await captureGeminiResumeEvidence(f.context, f.testInfo, { phase: 'stored', prior: f.agent })
    expect(objects(f.receipt().archives)[0]).toMatchObject({ state: 'failed', attachment: null, sha256: null, sessionId: null })
    expect(ports.io.open).not.toHaveBeenCalled()
  })

  it.each(['before open', 'inode', 'mtime', 'ctime', 'append', 'partial read', 'directory', 'path replacement'])('refuses certification after %s changes without a file retry', async (condition) => {
    const f = fixture()
    const ports = filePorts(f.text, f.archive)
    if (condition === 'before open') {
      ports.io.stat = vi.fn(() => ({ ...ports.stat, ino: 99 }))
    }
    else if (condition === 'partial read') {
      ports.io.read = vi.fn(() => ports.bytes.subarray(1))
    }
    else if (condition === 'directory') {
      let reads = 0
      ports.io.realpath = vi.fn(() => ++reads === 1 ? dirname(f.archive) : join(directory, 'foreign-directory'))
    }
    else if (condition === 'path replacement') {
      let reads = 0
      ports.io.lstat = vi.fn(() => ++reads === 1 ? ports.stat : { ...ports.stat, ino: 99 })
    }
    else {
      let reads = 0
      const changed = { ...ports.stat }
      if (condition === 'inode')
        changed.ino = 99
      else if (condition === 'mtime')
        changed.mtimeMs = 99
      else if (condition === 'ctime')
        changed.ctimeMs = 99
      else
        changed.size++
      ports.io.stat = vi.fn(() => ++reads === 1 ? ports.stat : changed)
    }
    await captureGeminiResumeEvidence(f.context, f.testInfo, { phase: 'stored', prior: f.agent })
    expect(objects(f.receipt().archives)[0]).toMatchObject({ state: 'failed', attachment: null, sha256: null, sessionId: null })
    expect(ports.io.open).toHaveBeenCalledTimes(1)
    expect(ports.io.close).toHaveBeenCalledTimes(1)
    expect(ports.io.read).toHaveBeenCalledTimes(condition === 'before open' ? 0 : 1)
  })

  it('accepts the exact sixteen MiB limit and preserves the full captured digest', async () => {
    const f = fixture()
    const limit = 16 * 1024 * 1024
    const base = JSON.stringify({ ...f.owner, padding: '' })
    const text = JSON.stringify({ ...f.owner, padding: 'x'.repeat(limit - Buffer.byteLength(base)) })
    const ports = filePorts(text, f.archive)
    await captureGeminiResumeEvidence(f.context, f.testInfo, { phase: 'stored', prior: f.agent })
    expect(objects(f.receipt().archives)[0]).toMatchObject({ state: 'owned-observation', bytes: limit, sha256: createHash('sha256').update(ports.bytes).digest('hex') })
    expect(ports.io.read).toHaveBeenCalledTimes(1)
    expect(ports.io.close).toHaveBeenCalledTimes(1)
  })

  it('refuses one byte above sixteen MiB before opening the file', async () => {
    const f = fixture()
    const ports = filePorts(f.text, f.archive)
    ports.io.lstat = () => ({ ...ports.stat, size: 16 * 1024 * 1024 + 1 })
    await captureGeminiResumeEvidence(f.context, f.testInfo, { phase: 'stored', prior: f.agent })
    expect(objects(f.receipt().archives)[0]).toMatchObject({ state: 'failed', attachment: null, sha256: null })
    expect(ports.io.open).not.toHaveBeenCalled()
    expect(ports.io.read).not.toHaveBeenCalled()
  })

  it('records both stable-reader failure components without certifying owned bytes', async () => {
    const f = fixture()
    const ports = filePorts(f.text, f.archive)
    ports.io.read = () => {
      throw new Error('Exact native read failure')
    }
    ports.io.close = () => {
      throw new Error('Exact native close failure')
    }
    await captureGeminiResumeEvidence(f.context, f.testInfo, { phase: 'stored', prior: f.agent })
    const archive = objects(f.receipt().archives)[0]
    expect(object(archive?.error)).toHaveProperty('components')
    const components = objects(object(archive?.error).components)
    expect(components.filter(item => item.relation === 'aggregate').map(item => item.message)).toEqual(['Exact native read failure', 'Exact native close failure'])
    expect(archive).toMatchObject({ state: 'failed', attachment: null, sha256: null })
  })

  it('retains cyclic and shared error causes with zero false null and empty components', async () => {
    const f = fixture()
    const ports = filePorts(f.text, f.archive)
    const shared = new Error('Exact shared native cause')
    const failure = new AggregateError([0, false, null, '', shared, shared], 'Native aggregate details', { cause: null })
    failure.cause = failure
    ports.io.read = () => {
      throw failure
    }
    await captureGeminiResumeEvidence(f.context, f.testInfo, { phase: 'stored', prior: f.agent })
    expect(object(objects(f.receipt().archives)[0]?.error)).toHaveProperty('components')
    const components = objects(object(objects(f.receipt().archives)[0]?.error).components)
    expect(components).toEqual(expect.arrayContaining([
      expect.objectContaining({ relation: 'aggregate', valueKind: 'number', value: 0 }),
      expect.objectContaining({ relation: 'aggregate', valueKind: 'boolean', value: false }),
      expect.objectContaining({ relation: 'aggregate', valueKind: 'null', value: null }),
      expect.objectContaining({ relation: 'aggregate', valueKind: 'string', value: '' }),
      expect.objectContaining({ relation: 'cause', reference: 0 }),
    ]))
    const sharedComponents = components.filter(item => item.message === shared.message)
    expect(sharedComponents).toHaveLength(2)
    expect(sharedComponents[1]?.reference).toBe(sharedComponents[0]?.id)
  })

  it('retains very deep cause details without recursive serialization', async () => {
    const f = fixture()
    const ports = filePorts(f.text, f.archive)
    let failure: Error = new Error('Exact deepest native cause')
    for (let index = 0; index < 12_000; index++)
      failure = new Error(`Native cause ${index}`, { cause: failure })
    ports.io.read = () => {
      throw failure
    }
    await captureGeminiResumeEvidence(f.context, f.testInfo, { phase: 'stored', prior: f.agent })
    expect(object(objects(f.receipt().archives)[0]?.error)).toHaveProperty('components')
    const components = objects(object(objects(f.receipt().archives)[0]?.error).components)
    expect(components).toHaveLength(12_001)
    expect(components.at(-1)?.message).toBe('Exact deepest native cause')
  })

  it.each([
    { label: 'negative number', value: -7, valueKind: 'number', encoded: -7 },
    { label: 'negative zero', value: -0, valueKind: 'number', encoded: '-0' },
    { label: 'infinity', value: Number.POSITIVE_INFINITY, valueKind: 'number', encoded: 'Infinity' },
    { label: 'not a number', value: Number.NaN, valueKind: 'number', encoded: 'NaN' },
    { label: 'undefined', value: undefined, valueKind: 'undefined', encoded: null },
    { label: 'large bigint', value: 9007199254740993n, valueKind: 'bigint', encoded: '9007199254740993' },
    { label: 'symbol', value: Symbol('native-cause'), valueKind: 'symbol', encoded: 'Symbol(native-cause)' },
  ])('retains a present $label cause through JSON serialization', async ({ value, valueKind, encoded }) => {
    const f = fixture()
    const ports = filePorts(f.text, f.archive)
    ports.io.read = () => {
      throw new Error('Native cause value', { cause: value })
    }
    await captureGeminiResumeEvidence(f.context, f.testInfo, { phase: 'stored', prior: f.agent })
    const components = objects(object(objects(f.receipt().archives)[0]?.error).components)
    expect(components.find(item => item.relation === 'cause')).toMatchObject({ valueKind, value: encoded })
  })

  it('retains enumerable native error fields and a shared null-prototype cause', async () => {
    const f = fixture()
    const ports = filePorts(f.text, f.archive)
    const cause: Record<string, unknown> = Object.assign(Object.create(null), { zero: 0, enabled: false, empty: '', absent: null })
    cause.self = cause
    const failure = Object.assign(new Error('Native structured cause', { cause }), { code: 0, retry: false, details: cause })
    ports.io.read = () => {
      throw failure
    }
    await captureGeminiResumeEvidence(f.context, f.testInfo, { phase: 'stored', prior: f.agent })
    const components = objects(object(objects(f.receipt().archives)[0]?.error).components)
    expect(components).toEqual(expect.arrayContaining([
      expect.objectContaining({ relation: 'property', key: 'code', valueKind: 'number', value: 0 }),
      expect.objectContaining({ relation: 'property', key: 'retry', valueKind: 'boolean', value: false }),
      expect.objectContaining({ relation: 'property', key: 'zero', valueKind: 'number', value: 0 }),
      expect.objectContaining({ relation: 'property', key: 'enabled', valueKind: 'boolean', value: false }),
      expect.objectContaining({ relation: 'property', key: 'empty', valueKind: 'string', value: '' }),
      expect.objectContaining({ relation: 'property', key: 'absent', valueKind: 'null', value: null }),
    ]))
    const causeNode = components.find(item => item.relation === 'cause')
    expect(components.find(item => item.key === 'self')?.reference).toBe(causeNode?.id)
    expect(components.find(item => item.key === 'details')?.reference).toBe(causeNode?.id)
  })

  it('retains array positions and a function cause without recursive data', async () => {
    const f = fixture()
    const ports = filePorts(f.text, f.archive)
    const cause = [0, false, null, () => undefined]
    ports.io.read = () => {
      throw new Error('Native array cause', { cause })
    }
    await captureGeminiResumeEvidence(f.context, f.testInfo, { phase: 'stored', prior: f.agent })
    const components = objects(object(objects(f.receipt().archives)[0]?.error).components)
    expect(components.find(item => item.relation === 'cause')).toMatchObject({ valueKind: 'array', value: 4 })
    expect(components.filter(item => item.relation === 'property').map(item => [item.key, item.valueKind, item.value])).toEqual([
      ['0', 'number', 0],
      ['1', 'boolean', false],
      ['2', 'null', null],
      ['3', 'function', null],
    ])
  })

  it('does not repeat a failed Worker identity read', async () => {
    const f = fixture(AgentStatus.STARTUP_FAILED)
    calls.agent.mockReset().mockResolvedValueOnce(f.agent).mockRejectedValueOnce(new Error('Exact Worker identity read failure'))
    await captureGeminiResumeEvidence(f.context, f.testInfo, { phase: 'opened', prior: f.agent })
    expect(calls.agent).toHaveBeenCalledTimes(2)
    expect(object(f.receipt('opened').workerRead).state).toBe('failed')
    expect(object(object(f.receipt('opened').workerRead).error).message).toBe('Exact Worker identity read failure')
  })

  it('records both Worker read failures as separate components', async () => {
    const f = fixture(AgentStatus.STARTUP_FAILED)
    calls.worker.mockRejectedValue(new Error('Exact Worker page failure'))
    calls.agent.mockReset().mockResolvedValueOnce(f.agent).mockRejectedValueOnce(new Error('Exact Worker identity failure'))
    await captureGeminiResumeEvidence(f.context, f.testInfo, { phase: 'opened', prior: f.agent })
    expect(object(object(f.receipt('opened').workerRead).error)).toHaveProperty('components')
    const components = objects(object(object(f.receipt('opened').workerRead).error).components)
    expect(components.filter(item => item.relation === 'aggregate').map(item => item.message)).toEqual(['Exact Worker page failure', 'Exact Worker identity failure'])
    expect(calls.agent).toHaveBeenCalledTimes(2)
    expect(calls.worker).toHaveBeenCalledTimes(1)
  })

  it('retains an actual empty Worker observation without an invented row', async () => {
    const f = fixture(AgentStatus.STARTUP_FAILED)
    calls.worker.mockResolvedValue(create(ListAgentMessagesResponseSchema))
    await captureGeminiResumeEvidence(f.context, f.testInfo, { phase: 'opened', prior: f.agent })
    expect(f.receipt('opened')).toMatchObject({ messages: [], workerRead: { state: 'complete-observation' } })
    expect(calls.worker).toHaveBeenCalledTimes(1)
  })

  it.each(['open', 'close', 'invalid UTF-8'])('records a %s read failure without attaching an owned archive', async (condition) => {
    const f = fixture()
    const ports = filePorts(f.text, f.archive)
    if (condition === 'open')
      ports.io.open = () => { throw new Error('Exact native open failure') }
    else if (condition === 'close')
      ports.io.close = () => { throw new Error('Exact native close failure') }
    else
      ports.io.read = () => new Uint8Array(ports.bytes.length).fill(0xFF)
    await captureGeminiResumeEvidence(f.context, f.testInfo, { phase: 'stored', prior: f.agent })
    expect(objects(f.receipt().archives)[0]).toMatchObject({ state: 'failed', attachment: null, sha256: null })
    if (condition === 'open')
      expect(ports.io.close).not.toHaveBeenCalled()
  })

  it('preserves the continued model request without a version or replay-completion claim', async () => {
    const f = fixture()
    const request: MockModelRequestRecord = { protocol: 'google-generative-language', path: '/v1beta/models/mock:streamGenerateContent', body: { contents: [], zero: 0, enabled: false, empty: '', absent: null } }
    await captureGeminiResumeEvidence(f.context, f.testInfo, { phase: 'continued', prior: f.agent, request })
    const result = f.receipt('continued')
    expect(result.request).toEqual(request)
    expect(result).not.toHaveProperty('version')
    expect(result).not.toHaveProperty('replayComplete')
    expect(result).not.toHaveProperty('replayBoundary')
  })
})

describe('exerciseGeminiResumeWithEvidence', () => {
  it('captures all three real callback phases and returns the unchanged resume result', async () => {
    const f = fixture()
    const request: MockModelRequestRecord = { protocol: 'google-generative-language', path: '/v1beta/models/mock:streamGenerateContent', body: { contents: [] } }
    const result: NativeResumeResult = {
      marker: '0123456789abcdef0123456789abcdef',
      originalPrompt: 'Keep RESUMEPROMPT0123456789abcdef0123456789abcdef for the stored session.',
      originalAnswer: 'RESUMEANSWER0123456789abcdef0123456789abcdef',
      resumedPrompt: 'Reply to RESUMEDPROMPT0123456789abcdef0123456789abcdef in the reopened session.',
      resumedAnswer: 'RESUMEDNEWANSWER0123456789abcdef0123456789abcdef',
      request,
    }
    calls.lifecycle.mockImplementation(async (_context, options) => {
      if (!options?.resumeEvidence)
        throw new Error('The actual resume wrapper did not supply its evidence callback.')
      const evidence: NativeResumeEvidence[] = [
        { phase: 'stored', prior: f.agent },
        { phase: 'opened', prior: f.agent },
        { phase: 'continued', prior: f.agent, request },
      ]
      for (const item of evidence)
        await options.resumeEvidence(item)
      return result
    })
    expect(await exerciseGeminiResumeWithEvidence(f.context, f.testInfo)).toBe(result)
    expect(f.receipt('stored').phase).toBe('stored')
    expect(f.receipt('opened').phase).toBe('opened')
    expect(f.receipt('continued').request).toEqual(request)
    expect(f.attachments.some(item => item.name.includes('-failed-'))).toBe(false)
  })

  it('reopens the session only after the minute of its newest archive ends', async () => {
    const f = fixture()
    // A later archive of the same session, and an archive of another session in an even later minute.
    writeFileSync(join(f.chats, 'session-2026-10-04T01-05-1df17a24.jsonl'), f.text)
    writeFileSync(join(f.chats, 'session-2026-10-04T01-09-0badf00d.jsonl'), f.text)
    const log: string[] = []
    let now = Date.parse('2026-10-04T01:05:10.000Z')
    const clock = {
      now: () => now,
      sleep: async (milliseconds: number) => {
        log.push(`sleep ${milliseconds}`)
        now += milliseconds
      },
    }
    calls.lifecycle.mockImplementation(async (_context, options) => {
      for (const phase of ['stored', 'opened'] as const) {
        await options?.resumeEvidence?.({ phase, prior: f.agent })
        log.push(`${phase} done`)
      }
      return { marker: '', originalPrompt: '', originalAnswer: '', resumedPrompt: '', resumedAnswer: '', request: { protocol: 'google-generative-language', path: '', body: {} } }
    })
    await exerciseGeminiResumeWithEvidence(f.context, f.testInfo, clock)
    expect(log).toEqual(['sleep 50000', 'stored done', 'opened done'])
    expect(new Date(now).toISOString()).toBe('2026-10-04T01:06:00.000Z')
  })

  it('reopens at once when the minute of the archive already ended', async () => {
    const f = fixture()
    const sleep = vi.fn<(milliseconds: number) => Promise<void>>()
    calls.lifecycle.mockImplementation(async (_context, options) => {
      await options?.resumeEvidence?.({ phase: 'stored', prior: f.agent })
      return { marker: '', originalPrompt: '', originalAnswer: '', resumedPrompt: '', resumedAnswer: '', request: { protocol: 'google-generative-language', path: '', body: {} } }
    })
    await exerciseGeminiResumeWithEvidence(f.context, f.testInfo, { now: () => Date.parse('2026-10-04T01:03:00.000Z'), sleep })
    expect(sleep).not.toHaveBeenCalled()
  })

  it('refuses to reopen a stored session that has no archive, and captures the failure', async () => {
    const f = fixture()
    rmSync(f.archive)
    calls.lifecycle.mockImplementation(async (_context, options) => {
      await options?.resumeEvidence?.({ phase: 'stored', prior: f.agent })
      throw new Error('The scenario continued without a native archive.')
    })
    await expect(exerciseGeminiResumeWithEvidence(f.context, f.testInfo)).rejects.toThrow('The native Gemini session has no archive to reopen.')
    expect(f.receipt('failed').failure).toMatchObject({ message: 'The native Gemini session has no archive to reopen.' })
  })

  it('captures a failed native scenario once and rethrows the exact original failure', async () => {
    const f = fixture()
    const failure = new Error('Exact native continuation failure')
    calls.lifecycle.mockImplementation(async (_context, options) => {
      await options?.resumeEvidence?.({ phase: 'stored', prior: f.agent })
      throw failure
    })
    await expect(exerciseGeminiResumeWithEvidence(f.context, f.testInfo)).rejects.toBe(failure)
    expect(f.receipt('failed').failure).toMatchObject({ name: failure.name, message: failure.message })
    expect(f.attachments.filter(item => item.name === 'gemini-resume-failed-receipt.json')).toHaveLength(1)
  })

  it('captures a failure before the original identity without fabricating a prior agent', async () => {
    const f = fixture()
    const failure = new Error('Exact original native answer failure')
    calls.lifecycle.mockRejectedValue(failure)
    await expect(exerciseGeminiResumeWithEvidence(f.context, f.testInfo)).rejects.toBe(failure)
    expect(f.receipt('failed')).toMatchObject({ priorAgent: null, archives: [], workerRead: { state: 'not-read' } })
    expect(calls.worker).not.toHaveBeenCalled()
  })

  it('retains the original failure first when the failure attachment also fails', async () => {
    const f = fixture()
    const failure = new Error('Exact native scenario failure')
    const captureFailure = new Error('Exact native attachment failure')
    calls.lifecycle.mockRejectedValue(failure)
    f.testInfo.attach = vi.fn<TestInfo['attach']>().mockRejectedValue(captureFailure)
    const result = await exerciseGeminiResumeWithEvidence(f.context, f.testInfo).then(() => undefined, error => error)
    expect(result).toBeInstanceOf(AggregateError)
    if (!(result instanceof AggregateError))
      throw new Error('The resume wrapper did not retain both failures.')
    expect(result.errors).toEqual([failure, captureFailure])
  })
})
