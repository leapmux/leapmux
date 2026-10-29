/** Native workflow-child scenarios with independently marked model requests. */
import type { TestInfo } from '@playwright/test'
import type { AgentInputQueueSnapshot } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { Buffer } from 'node:buffer'
import { readFile } from 'node:fs/promises'
import { basename, isAbsolute, join, normalize, relative, sep } from 'node:path'
import process from 'node:process'
import { expect, test } from '@playwright/test'
import { MUSE_PARENT_INPUT } from '../../../src/generated/contracts/muse-protocol'
import { isObject, pickObject, pickString } from '../../../src/lib/jsonPick'
import { readNativeInputQueue } from '../helpers/nativeInputQueueIdle'
import { nativeMessageBody, nativeMessageSupplement, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { assertPrivateNativePath } from '../helpers/nativePrivatePath'
import { selectedAgentTabId } from '../helpers/nativeScenario'
import { bashToolCall, codeExecutionToolCall, museSubmitResultToolCall } from '../helpers/providerToolCalls'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { RUN_ROOT_ENV, RUN_ROOT_PREFIX } from '../helpers/runRoot'
import { backgroundTaskRows, expandBackgroundTasksSection, openChildTabFromRow } from '../helpers/subagentRegistry'
import { assistantBubbles, sendMessage, tabById, waitForAgentIdle } from '../helpers/ui'

export const MUSE_CHILD_TASK = 'Inspect the native child transcript.'
export const MUSE_CHILD_PROGRESS = 'The native child started its own command.'
export const MUSE_CHILD_OUTPUT = 'MUSE_CHILD_OUTPUT_42'
export const MUSE_CHILD_LATER_OUTPUT = 'MUSE_CHILD_LATER_77'
export const MUSE_CHILD_RESULT = 'The native child completed its own work.'
export const MUSE_CHILD_GATE = 'muse-child-next-boundary'

export async function readMuseNativeControlRecords(id: string, dataHome: string | undefined): Promise<Record<string, unknown>[]> {
  if (!/^[a-f0-9]{8}-[a-f0-9]{4}-7[a-f0-9]{3}-[a-f0-9]{4}-[a-f0-9]{12}$/i.test(id))
    throw new Error('The native diagnostic requires a UUIDv7 root session.')
  if (!dataHome)
    throw new Error('The native diagnostic requires the isolated data directory.')
  const created = new Date(Number.parseInt(id.replaceAll('-', '').slice(0, 12), 16))
  const path = join(dataHome, 'muse', 'sessions', String(created.getFullYear()), String(created.getMonth() + 1).padStart(2, '0'), String(created.getDate()).padStart(2, '0'), id, 'session.jsonl')
  const source = await readFile(path, 'utf8')
  return source.split(String.fromCharCode(10)).filter(line => line.trim()).flatMap((line) => {
    const record: unknown = JSON.parse(line)
    return isObject(record) && (pickObject(record, 'payload')?.kind === MUSE_PARENT_INPUT.SubagentControl
      || pickObject(record, 'payload')?.kind === MUSE_PARENT_INPUT.CommandIntake
      || pickString(record, 'payload_type').startsWith('runtime.command_intake.')
      || (pickObject(record, 'payload')?.kind === 'run' && pickObject(pickObject(record, 'payload'), 'event')?.kind === 'inbox_item_drained'))
      ? [record]
      : []
  })
}

export interface MuseNativeSessionLog {
  path: string
  bytes: Buffer
}

export interface MuseNativeSessionLogs {
  parent: PromiseSettledResult<MuseNativeSessionLog>
  child: PromiseSettledResult<MuseNativeSessionLog>
}

/** Read only the owned session files, preserving even an incomplete final line. */
export async function readMuseNativeSessionLogs(parentSessionID: string, childSessionID: string | undefined, dataHome: string | undefined): Promise<MuseNativeSessionLogs> {
  const uuid = /^[a-f0-9]{8}-[a-f0-9]{4}-[1-8][a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$/i
  if (!uuid.test(parentSessionID) || parentSessionID[14] !== '7')
    throw new Error('The native session log requires a UUIDv7 root session.')
  if (childSessionID !== undefined && (!uuid.test(childSessionID) || childSessionID === parentSessionID))
    throw new Error('The native session log requires a distinct child UUID.')
  const created = new Date(Number.parseInt(parentSessionID.replaceAll('-', '').slice(0, 12), 16))
  const year = created.getFullYear()
  if (!Number.isFinite(created.getTime()) || year < 1970 || year > 9999)
    throw new Error('The native root session has no valid four-digit storage date.')
  const runRoot = process.env[RUN_ROOT_ENV]
  if (!runRoot || !isAbsolute(runRoot) || normalize(runRoot) !== runRoot || runRoot.includes('\0') || !basename(runRoot).startsWith(RUN_ROOT_PREFIX))
    throw new Error('The native session log requires the current isolated run root.')
  if (!dataHome || !isAbsolute(dataHome) || normalize(dataHome) !== dataHome || dataHome.includes('\0'))
    throw new Error('The native session log requires an absolute normalized isolated data directory.')
  const location = relative(runRoot, dataHome)
  if (location === '' || location === '..' || location.startsWith(`..${sep}`) || isAbsolute(location))
    throw new Error('The native session log data directory is outside the current isolated run.')
  const root = join(dataHome, 'muse', 'sessions', String(year), String(created.getMonth() + 1).padStart(2, '0'), String(created.getDate()).padStart(2, '0'), parentSessionID)
  const read = async (path: string): Promise<MuseNativeSessionLog> => {
    assertPrivateNativePath(path, runRoot)
    return { path, bytes: await readFile(path) }
  }
  const [parent, child] = await Promise.allSettled([
    read(join(root, 'session.jsonl')),
    childSessionID === undefined
      ? Promise.reject(new Error('The native child session is unresolved.'))
      : read(join(root, 'subagent', childSessionID, 'session.jsonl')),
  ])
  return { parent, child }
}

export interface MuseChildReadinessContext {
  parentId: string
  childId?: string
  lastQueue?: AgentInputQueueSnapshot
}

function diagnosticResult<T>(result: PromiseSettledResult<T>, project: (value: T) => unknown = value => value): unknown {
  if (result.status === 'fulfilled') {
    return {
      status: result.status,
      value: project(result.value),
    }
  }
  const reason: unknown = result.reason
  return {
    status: result.status,
    reason: reason instanceof Error
      ? { name: reason.name, message: reason.message, stack: reason.stack }
      : String(reason),
  }
}

function diagnosticValue(read: () => unknown): unknown {
  try {
    return diagnosticResult({
      status: 'fulfilled',
      value: read(),
    })
  }
  catch (reason) {
    return diagnosticResult({ status: 'rejected', reason })
  }
}

function diagnosticTranscript(snapshot: NativeMessageSnapshot) {
  return {
    agentId: snapshot.agentId,
    agentSessionId: snapshot.agentSessionId,
    messages: snapshot.messages.map(message => ({
      id: message.id,
      seq: String(message.seq),
      sessionId: message.agentSessionId,
      provider: message.agentProvider,
      source: message.source,
      spanId: message.spanId,
      completion: message.completion,
      original: diagnosticValue(() => nativeMessageBody(message)),
      supplement: diagnosticValue(() => nativeMessageSupplement(message)),
      originalBytesBase64: Buffer.from(message.content).toString('base64'),
      originalCompression: message.contentCompression,
      supplementBytesBase64: Buffer.from(message.supplementalContent).toString('base64'),
      supplementCompression: message.supplementalContentCompression,
    })),
  }
}

/** Capture readiness evidence without changing the held child or its queue. */
export async function attachMuseChildReadinessFailure(native: ManagedNativeScenarioContext, context: MuseChildReadinessContext, report: Pick<TestInfo, 'attach'>): Promise<void> {
  const { parentId, childId, lastQueue } = context
  const [parentQueue, childQueue, parentSnapshot, childSnapshot, model] = await Promise.allSettled([
    Promise.resolve().then(() => readNativeInputQueue(native.leapmuxServer, parentId)),
    Promise.resolve().then(() => childId
      ? readNativeInputQueue(native.leapmuxServer, childId)
      : Promise.reject(new Error('The Worker child ID is unresolved.'))),
    Promise.resolve().then(() => readNativeMessageSnapshot(native, parentId)),
    Promise.resolve().then(() => childId
      ? readNativeMessageSnapshot(native, childId)
      : Promise.reject(new Error('The Worker child ID is unresolved.'))),
    Promise.resolve().then(() => native.modelScript.status()),
  ])
  let logs: PromiseSettledResult<MuseNativeSessionLogs>
  try {
    if (parentSnapshot.status !== 'fulfilled')
      throw new Error('The owning native parent session is unavailable.')
    logs = { status: 'fulfilled', value: await readMuseNativeSessionLogs(parentSnapshot.value.agentSessionId, childSnapshot.status === 'fulfilled'
      ? childSnapshot.value.agentSessionId
      : undefined, native.leapmuxServer.agentEnv?.XDG_DATA_HOME) }
  }
  catch (reason) {
    logs = { status: 'rejected', reason }
  }
  const evidence = {
    parentId,
    childId,
    lastQueue,
    queues: {
      parent: diagnosticResult(parentQueue),
      child: diagnosticResult(childQueue),
    },
    transcripts: {
      parent: diagnosticResult(parentSnapshot, diagnosticTranscript),
      child: diagnosticResult(childSnapshot, diagnosticTranscript),
    },
    model: diagnosticResult(model),
    nativeLogs: diagnosticResult(logs, value => ({
      parent: diagnosticResult(value.parent, log => ({ path: log.path, bytes: log.bytes.length })),
      child: diagnosticResult(value.child, log => ({ path: log.path, bytes: log.bytes.length })),
    })),
  }
  const attachments: { name: string, body: string | Buffer, contentType: string }[] = [
    {
      name: 'muse-held-child-readiness',
      body: JSON.stringify(evidence, (_key, value) => typeof value === 'bigint'
        ? String(value)
        : value),
      contentType: 'application/json',
    },
  ]
  if (logs.status === 'fulfilled') {
    for (const [owner, result] of Object.entries(logs.value)) {
      if (result.status === 'fulfilled')
        attachments.push({ name: `muse-held-${owner}-session.jsonl`, body: result.value.bytes, contentType: 'application/x-ndjson' })
    }
  }
  await Promise.allSettled(attachments.map(({ name, ...attachment }) => Promise.resolve().then(() => report.attach(name, attachment))))
}

/** Start one real child and return its actual registry link and native tab. */
export async function openMuseChild(native: ManagedNativeScenarioContext, held: boolean) {
  const { page, modelScript } = native
  const parentId = await selectedAgentTabId(page)
  const childInput = modelScript.prompt(MUSE_CHILD_TASK)
  await modelScript.rule(
    {
      name: 'the Muse child runs its own command',
      when: {
        body: ['Workflow child completion protocol', MUSE_CHILD_TASK],
      },
      once: true,
      respond: {
        text: MUSE_CHILD_PROGRESS,
        toolCalls: [
          bashToolCall(native.provider, 'muse-child-first', 'printf \'MUSE_CHILD_OUTPUT_%s\\n\' 42'),
        ],
      },
    },
    {
      name: 'the Muse child reaches its next command boundary',
      when: {
        body: ['Workflow child completion protocol', MUSE_CHILD_OUTPUT],
      },
      once: true,
      respond: {
        ...(held
          ? { gate: MUSE_CHILD_GATE }
          : {}),
        toolCalls: [
          bashToolCall(native.provider, 'muse-child-later', 'printf \'MUSE_CHILD_LATER_%s\\n\' 77'),
        ],
      },
    },
    {
      name: 'the Muse child submits its own result',
      when: {
        body: ['Workflow child completion protocol', MUSE_CHILD_LATER_OUTPUT],
      },
      once: true,
      respond: {
        text: MUSE_CHILD_RESULT,
        toolCalls: [
          museSubmitResultToolCall('muse-child-submit', 'The child result contract is complete.'),
        ],
      },
    },
  )
  const start = await modelScript.queue(
    {
      toolCalls: [
        codeExecutionToolCall(native.provider, 'muse-child-workflow', `export default async function workflow(host) { return await host.agent({ input: ${JSON.stringify(childInput)} }); }`),
      ],
    },
    { text: 'The parent received the native child outcome.' },
  )
  let childId: string | undefined
  let lastQueue: AgentInputQueueSnapshot | undefined
  try {
    await sendMessage(page, modelScript.prompt('Run the native child workflow.'))
    if (held) {
      await modelScript.waitForGate(MUSE_CHILD_GATE)
    }
    else {
      await modelScript.waitForSteps(start + 2)
      await waitForAgentIdle(page)
    }
    await expandBackgroundTasksSection(page)
    const row = backgroundTaskRows(page, { kind: 'subagent' }).filter({ hasText: MUSE_CHILD_TASK }).first()
    await expect(row).toHaveAttribute('data-status', held ? 'running' : 'succeeded')
    const openedChildId = await openChildTabFromRow(page, row)
    childId = openedChildId
    if (held) {
      // The native model gate precedes the worker's asynchronous child-history read.
      await retryUntilPass(async () => {
        lastQueue = await readNativeInputQueue(native.leapmuxServer, openedChildId)
        expect(lastQueue.activeTurn).toBe(true)
        expect(lastQueue.activeTurnSteerable).toBe(true)
      })
    }
    const finish = async () => {
      await modelScript.releaseGateIfHeld(MUSE_CHILD_GATE)
      await modelScript.waitForSteps(start + 2)
      await tabById(page, parentId).click()
      await waitForAgentIdle(page)
      await tabById(page, openedChildId).click()
      await waitForAgentIdle(page)
    }
    return {
      row,
      childId: openedChildId,
      parentId,
      finish,
    }
  }
  catch (error) {
    if (held) {
      try {
        await attachMuseChildReadinessFailure(native, {
          parentId,
          ...(childId === undefined
            ? {}
            : { childId }),
          ...(lastQueue === undefined
            ? {}
            : { lastQueue }),
        }, test.info())
      }
      catch {
        // Diagnostic/reporting failures must preserve the original readiness failure.
      }
    }
    throw error
  }
}

export async function expectMuseChildResult(native: ManagedNativeScenarioContext, result = MUSE_CHILD_RESULT) {
  await expect(assistantBubbles(native.page).filter({ hasText: result })).toHaveCount(1)
}
