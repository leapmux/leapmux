import type { JsonValue } from '@bufbuild/protobuf'
import type { TestInfo } from '@playwright/test'
import type { AgentChatMessage, AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { NativeResumeEvidence } from '../helpers/nativeLifecycle'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { Buffer } from 'node:buffer'
import { createHash } from 'node:crypto'
import { readdirSync, writeFileSync } from 'node:fs'
import { extname, join, normalize } from 'node:path'
import { toJson } from '@bufbuild/protobuf'
import { MESSAGE_PAGE_LIMIT } from '../../../src/generated/contracts/chat-history'
import { AgentChatMessageSchema, AgentInfoSchema, AgentStatus, ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema, MessagePageAnchor } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { getTestChannel } from '../helpers/api'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { nativeAgentById } from '../helpers/nativeScenario'
import { readNativeToolOutputFile } from '../helpers/nativeToolOutputFile'
import { geminiNativeProject } from './nativeStore'

interface EvidenceErrorComponent {
  id: number
  parent: number | null
  relation: 'root' | 'aggregate' | 'cause' | 'property'
  childIndex: number | null
  key: string | null
  name: string
  message: string
  valueKind: 'aggregate-error' | 'error' | 'null' | 'string' | 'number' | 'boolean' | 'undefined' | 'bigint' | 'symbol' | 'function' | 'array' | 'object'
  value: JsonValue
  reference: number | null
}

interface EvidenceError {
  name: string
  message: string
  components?: EvidenceErrorComponent[]
}

interface ArchiveObservation {
  path: string
  state: 'owned-observation' | 'unstable' | 'refused' | 'failed'
  sessionId: string | null
  projectHash: string | null
  kind: string | null
  bytes: number | null
  sha256: string | null
  attachment: string | null
  incompleteTrailingLine: boolean | null
  error: EvidenceError | null
}

interface GeminiResumeReceipt {
  phase: 'stored' | 'opened' | 'continued' | 'failed'
  priorAgent: JsonValue | null
  selectedTabId: string | null
  workerBefore: JsonValue | null
  workerAfter: JsonValue | null
  messages: JsonValue[]
  workerRead: {
    state: 'complete-observation' | 'unstable' | 'failed' | 'not-read'
    error: EvidenceError | null
  }
  archives: ArchiveObservation[]
  request: MockModelRequestRecord | null
  failure: EvidenceError | null
}

type GeminiResumeObservation = NativeResumeEvidence | {
  phase: 'failed'
  prior?: Readonly<AgentInfo>
  cause: unknown
}

class ArchiveRefusal extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'GeminiResumeArchiveRefusal'
  }
}

function componentValue(value: unknown): Pick<EvidenceErrorComponent, 'valueKind' | 'value'> {
  if (value === null)
    return { valueKind: 'null', value: null }
  switch (typeof value) {
    case 'string':
      return { valueKind: 'string', value }
    case 'boolean':
      return { valueKind: 'boolean', value }
    case 'number':
      return { valueKind: 'number', value: Object.is(value, -0) ? '-0' : Number.isFinite(value) ? value : String(value) }
    case 'undefined':
      return { valueKind: 'undefined', value: null }
    case 'bigint':
      return { valueKind: 'bigint', value: String(value) }
    case 'symbol':
      return { valueKind: 'symbol', value: String(value) }
    case 'function':
      return { valueKind: 'function', value: null }
    case 'object':
      if (value instanceof AggregateError)
        return { valueKind: 'aggregate-error', value: null }
      if (value instanceof Error)
        return { valueKind: 'error', value: null }
      if (Array.isArray(value))
        return { valueKind: 'array', value: value.length }
      return { valueKind: 'object', value: null }
  }
  throw new Error('The error component has an unsupported JavaScript value type.')
}

function componentHeader(value: unknown): Pick<EvidenceErrorComponent, 'name' | 'message'> {
  if (value instanceof Error)
    return { name: value.name, message: value.message }
  if (typeof value === 'object' && value !== null)
    return { name: 'NonError', message: Array.isArray(value) ? 'The thrown value is an array.' : 'The thrown value is an object.' }
  if (typeof value === 'function')
    return { name: 'NonError', message: 'The thrown value is a function.' }
  return { name: 'NonError', message: String(value) }
}

/** Preserve error details in a flat graph. Shared causes and cycles point at their first node. */
function evidenceError(error: unknown): EvidenceError {
  const queue: {
    value: unknown
    parent: number | null
    relation: EvidenceErrorComponent['relation']
    childIndex: number | null
    key: string | null
  }[] = [{ value: error, parent: null, relation: 'root', childIndex: null, key: null }]
  const components: EvidenceErrorComponent[] = []
  const seen = new WeakMap<object, number>()
  for (const item of queue) {
    const header = componentHeader(item.value)
    const value = componentValue(item.value)
    const node: EvidenceErrorComponent = { id: components.length, parent: item.parent, relation: item.relation, childIndex: item.childIndex, key: item.key, ...header, ...value, reference: null }
    components.push(node)
    if ((typeof item.value !== 'object' || item.value === null) && typeof item.value !== 'function')
      continue
    const previous = seen.get(item.value)
    if (previous !== undefined) {
      node.reference = previous
      continue
    }
    seen.set(item.value, node.id)
    if (item.value instanceof AggregateError) {
      for (const [index, child] of item.value.errors.entries())
        queue.push({ value: child, parent: node.id, relation: 'aggregate', childIndex: index, key: null })
    }
    if (item.value instanceof Error && Object.hasOwn(item.value, 'cause'))
      queue.push({ value: item.value.cause, parent: node.id, relation: 'cause', childIndex: null, key: 'cause' })
    for (const key of Object.keys(item.value)) {
      if (item.value instanceof Error && (key === 'name' || key === 'message' || key === 'cause' || (item.value instanceof AggregateError && key === 'errors')))
        continue
      const descriptor = Object.getOwnPropertyDescriptor(item.value, key)
      if (!descriptor)
        continue
      const child = Object.hasOwn(descriptor, 'value') ? descriptor.value : { get: descriptor.get, set: descriptor.set }
      queue.push({ value: child, parent: node.id, relation: 'property', childIndex: null, key })
    }
  }
  return { ...componentHeader(error), components }
}

function sameOwner(first: AgentInfo, second: AgentInfo): boolean {
  return first.id === second.id && first.workerId === second.workerId && first.agentProvider === second.agentProvider
    && first.agentSessionId === second.agentSessionId && first.workingDir === second.workingDir
    && first.parentAgentId === second.parentAgentId && first.rootAgentId === second.rootAgentId
    && first.spawnSpanId === second.spawnSpanId && first.providerChildKey === second.providerChildKey
}

function expectedOwner(agent: AgentInfo, prior: Readonly<AgentInfo>, workerId: string): boolean {
  return agent.workerId === workerId && agent.agentProvider === prior.agentProvider && agent.workingDir === prior.workingDir
    && agent.parentAgentId === '' && agent.spawnSpanId === '' && agent.providerChildKey === ''
    && (agent.rootAgentId === '' || agent.rootAgentId === agent.id)
    && (agent.agentSessionId === prior.agentSessionId || (agent.agentSessionId === ''
      && (agent.status === AgentStatus.STARTING || agent.status === AgentStatus.STARTUP_FAILED)))
}

/** Read pending or failed Worker rows as observations, without requiring completed native startup. */
async function pendingMessages(context: ManagedNativeScenarioContext, agentId: string, messages: AgentChatMessage[]): Promise<void> {
  const server = context.leapmuxServer
  const channel = await getTestChannel(server.hubUrl, server.adminToken)
  const seen = new Set<string>()
  let cursor: bigint | undefined
  for (;;) {
    const response = await channel.callWorker(server.workerId, 'ListAgentMessages', ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema, {
      agentId,
      anchor: cursor === undefined ? MessagePageAnchor.OLDEST : MessagePageAnchor.AFTER,
      ...(cursor === undefined ? {} : { cursorSeq: cursor }),
      limit: MESSAGE_PAGE_LIMIT,
    })
    // Retain the returned bytes even when this diagnostic page fails validation.
    messages.push(...response.messages)
    if (response.messages.length === 0 && response.hasMore)
      throw new Error('The resume evidence page is empty but claims another page.')
    for (const message of response.messages) {
      if (message.id.trim() === '' || seen.has(message.id))
        throw new Error('The resume evidence page contains an absent or duplicate message ID.')
      if (message.seq <= 0n)
        throw new Error('The resume evidence page contains a zero or negative message sequence.')
      if (cursor !== undefined && message.seq <= cursor)
        throw new Error('The resume evidence cursor did not advance.')
      seen.add(message.id)
      cursor = message.seq
    }
    if (!response.hasMore)
      return
  }
}

/** Verify native ownership without projecting history or assigning a replay boundary. */
function archiveOwner(text: string, prior: Readonly<AgentInfo>) {
  const expectedHash = createHash('sha256').update(normalize(prior.workingDir)).digest('hex')
  let foundHeader = false
  let kind: string | null = null
  const identity = (record: Record<string, unknown>, header: boolean) => {
    for (const [key, expected] of [
      ['sessionId', prior.agentSessionId],
      ['projectHash', expectedHash],
    ] as const) {
      if ((header || Object.hasOwn(record, key)) && record[key] !== expected)
        throw new ArchiveRefusal('The native resume archive has another or invalid session owner.')
    }
    if (Object.hasOwn(record, 'kind')) {
      if (record.kind !== 'main')
        throw new ArchiveRefusal('The native resume archive is not a root session.')
      kind = record.kind
    }
  }
  let whole: unknown
  let completeJSON = false
  try {
    whole = JSON.parse(text.trim())
    completeJSON = true
  }
  catch {
    // JSONL requires separate complete records. A partial last line is not a record.
  }
  if (completeJSON) {
    if (!isObject(whole))
      throw new ArchiveRefusal('The native resume archive is not an object.')
    identity(whole, true)
    return { sessionId: prior.agentSessionId, projectHash: expectedHash, kind, incompleteTrailingLine: false }
  }
  const lines = text.split('\n')
  let incompleteTrailingLine = false
  for (const [index, line] of lines.entries()) {
    if (line.trim() === '')
      continue
    let record: unknown
    try {
      record = JSON.parse(line)
    }
    catch {
      if (index === lines.length - 1 && !text.endsWith('\n')) {
        incompleteTrailingLine = true
        break
      }
      throw new ArchiveRefusal('The native resume archive contains an invalid complete JSONL record.')
    }
    if (!isObject(record))
      throw new ArchiveRefusal('The native resume archive record is not an object.')
    if (Object.hasOwn(record, 'sessionId')) {
      identity(record, true)
      foundHeader = true
    }
    else {
      if (!foundHeader)
        throw new ArchiveRefusal('The native resume archive record precedes its session owner.')
      if (Object.hasOwn(record, '$set')) {
        if (!isObject(record.$set))
          throw new ArchiveRefusal('The native resume archive metadata update is not an object.')
        identity(record.$set, false)
      }
    }
  }
  if (!foundHeader)
    throw new ArchiveRefusal('The native resume archive has no complete session owner.')
  return { sessionId: prior.agentSessionId, projectHash: expectedHash, kind, incompleteTrailingLine }
}

function emptyArchive(path: string): ArchiveObservation {
  return { path, state: 'failed', sessionId: null, projectHash: null, kind: null, bytes: null, sha256: null, attachment: null, incompleteTrailingLine: null, error: null }
}

/** Attach native bytes and observed Worker state. This phase does not certify replay completion. */
export async function captureGeminiResumeEvidence(
  context: ManagedNativeScenarioContext,
  testInfo: Pick<TestInfo, 'attach' | 'outputPath'>,
  evidence: GeminiResumeObservation,
): Promise<void> {
  const prior = evidence.prior
  const receipt: GeminiResumeReceipt = {
    phase: evidence.phase,
    priorAgent: prior ? toJson(AgentInfoSchema, prior, { alwaysEmitImplicit: true }) : null,
    selectedTabId: null,
    workerBefore: null,
    workerAfter: null,
    messages: [],
    workerRead: { state: 'not-read', error: null },
    archives: [],
    request: evidence.phase === 'continued' ? evidence.request : null,
    failure: evidence.phase === 'failed' ? evidenceError(evidence.cause) : null,
  }
  let before: AgentInfo | null = null
  let readFailure: { value: unknown } | undefined
  const messages: AgentChatMessage[] = []
  try {
    const tab = context.page.locator('[data-testid="tab"][data-tab-type="agent"][aria-selected="true"]:visible').first()
    receipt.selectedTabId = await tab.count() > 0 ? await tab.getAttribute('data-tab-id') : null
    if (receipt.selectedTabId) {
      before = await nativeAgentById(context, receipt.selectedTabId)
      receipt.workerBefore = before ? toJson(AgentInfoSchema, before, { alwaysEmitImplicit: true }) : null
    }
    if (before && prior) {
      if (before.id !== receipt.selectedTabId || !expectedOwner(before, prior, context.leapmuxServer.workerId))
        throw new Error('The selected Worker agent does not belong to the expected resumed session.')
      if (before.status === AgentStatus.ACTIVE)
        messages.push(...(await readNativeMessageSnapshot(context, before.id)).messages)
      else
        await pendingMessages(context, before.id, messages)
      receipt.workerRead.state = 'complete-observation'
    }
  }
  catch (error) {
    readFailure = { value: error }
    receipt.workerRead = { state: 'failed', error: evidenceError(error) }
  }
  if (before) {
    try {
      const after = await nativeAgentById(context, before.id)
      receipt.workerAfter = after ? toJson(AgentInfoSchema, after, { alwaysEmitImplicit: true }) : null
      if (!after || !sameOwner(before, after)) {
        const ownerFailure = new Error('The Worker session or owner changed during the resume evidence read.')
        receipt.workerRead = {
          state: 'unstable',
          error: evidenceError(readFailure
            ? new AggregateError([readFailure.value, ownerFailure], 'The Worker message read failed and its owner changed.')
            : ownerFailure),
        }
      }
    }
    catch (afterError) {
      receipt.workerRead = {
        state: 'failed',
        error: evidenceError(readFailure
          ? new AggregateError([readFailure.value, afterError], 'The Worker message and identity reads failed.')
          : afterError),
      }
    }
  }
  receipt.messages = messages.map(message => toJson(AgentChatMessageSchema, message, { alwaysEmitImplicit: true }))
  if (prior) {
    let directory: string | undefined
    let candidates: string[] = []
    try {
      directory = join(geminiNativeProject(context, prior), 'chats')
      const short = prior.agentSessionId.slice(0, 8)
      candidates = readdirSync(directory).filter(name => name.startsWith('session-')
        && (name.endsWith(`-${short}.jsonl`) || name.endsWith(`-${short}.json`)))
    }
    catch (error) {
      const observation = emptyArchive(directory ?? '')
      observation.state = 'refused'
      observation.error = evidenceError(error)
      receipt.archives.push(observation)
    }
    if (directory) {
      for (const [index, name] of candidates.entries()) {
        const observation = emptyArchive(join(directory, name))
        receipt.archives.push(observation)
        let text: string
        try {
          text = readNativeToolOutputFile(observation.path, 16 * 1024 * 1024)
          Object.assign(observation, archiveOwner(text, prior))
        }
        catch (error) {
          observation.state = error instanceof ArchiveRefusal ? 'refused' : 'failed'
          observation.error = evidenceError(error)
          continue
        }
        const attachment = `gemini-resume-${evidence.phase}-archive-${index}${extname(name)}`
        const path = testInfo.outputPath(attachment)
        writeFileSync(path, text, 'utf8')
        await testInfo.attach(attachment, { path, contentType: name.endsWith('.jsonl') ? 'application/x-ndjson' : 'application/json' })
        observation.state = 'owned-observation'
        observation.bytes = Buffer.byteLength(text)
        // This digest identifies captured bytes. Gemini supplies no exclusive replay token.
        observation.sha256 = createHash('sha256').update(text).digest('hex')
        observation.attachment = attachment
      }
    }
  }
  const attachment = `gemini-resume-${evidence.phase}-receipt.json`
  const path = testInfo.outputPath(attachment)
  writeFileSync(path, JSON.stringify(receipt, null, 2), 'utf8')
  await testInfo.attach(attachment, { path, contentType: 'application/json' })
}

/** Preserve resume failures while capturing the final observed state once. */
export async function exerciseGeminiResumeWithEvidence(
  context: ManagedNativeScenarioContext,
  testInfo: Pick<TestInfo, 'attach' | 'outputPath'>,
): Promise<MockModelRequestRecord> {
  let prior: Readonly<AgentInfo> | undefined
  try {
    return await exerciseSessionResume(context, { resumeEvidence: async (evidence) => {
      prior = evidence.prior
      await captureGeminiResumeEvidence(context, testInfo, evidence)
    } })
  }
  catch (failure) {
    try {
      await captureGeminiResumeEvidence(context, testInfo, { phase: 'failed', ...(prior ? { prior } : {}), cause: failure })
    }
    catch (captureFailure) {
      throw new AggregateError([failure, captureFailure], 'The native resume scenario and failure evidence capture failed.')
    }
    throw failure
  }
}
