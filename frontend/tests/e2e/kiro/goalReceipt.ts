import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { existsSync, readdirSync, readFileSync, realpathSync } from 'node:fs'
import { basename, isAbsolute, join } from 'node:path'
import { isObject } from '../../../src/lib/jsonPick'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'

export interface KiroGoalMessage {
  id: string
  payload: Record<string, unknown>
}

export interface KiroGoalSessionIdentity {
  home: string
  runDir: string
  workingDir: string
  sessionId: string
  parentSessionId: string
}

export interface KiroGoalCancellation {
  executionId: string
  startMessageId: string
  usageMessageId: string
  endMessageId: string
}

/** Read the actual workflow step session from its native model request. */
export function kiroGoalSessionId(request: MockModelRequestRecord): string {
  const state = isObject(request.body) && isObject(request.body.conversationState) ? request.body.conversationState : undefined
  if (request.protocol !== 'aws-event-stream' || typeof state?.conversationId !== 'string' || state.conversationId.trim() === '')
    throw new Error('The Kiro goal request requires its actual native session ID.')
  return state.conversationId
}

/** Decode complete native append records. A partial last line waits for its newline. */
export function decodeKiroGoalMessages(text: string): KiroGoalMessage[] {
  const complete = text.split('\n').slice(0, -1)
  return complete.filter(line => line.trim() !== '').map((line) => {
    let value: unknown
    try {
      value = JSON.parse(line)
    }
    catch (cause) {
      throw new Error('The native Kiro goal file contains a malformed complete JSON line.', { cause })
    }
    if (!isObject(value) || typeof value.id !== 'string' || value.id.trim() === '' || !isObject(value.payload))
      throw new Error('The native Kiro goal file contains an invalid message record.')
    return { id: value.id, payload: value.payload }
  })
}

/** Capture the actual latest execution before Pause or Clear. */
export function kiroGoalExecutionId(messages: readonly KiroGoalMessage[]): string | undefined {
  const started = messages.findLast(message => message.payload.type === 'turn_start')
  if (!started)
    return undefined
  const id = started.payload.executionId
  if (typeof id !== 'string' || id.trim() === '' || started.id !== `${id}-turn-start`)
    throw new Error('The native Kiro goal start requires its exact execution ID.')
  return id
}

/** Require the native start, aborted usage, and cancelled end of one exact execution. */
export function kiroGoalCancellation(messages: readonly KiroGoalMessage[], executionId: string): KiroGoalCancellation | undefined {
  if (executionId.trim() === '')
    throw new Error('The Kiro cancellation proof requires a captured execution ID.')
  const matching = (type: string) => messages.map((message, index) => ({ message, index })).filter(({ message }) => message.payload.type === type && message.payload.executionId === executionId)
  const starts = matching('turn_start')
  const usages = matching('usage_summary')
  const ends = matching('turn_end')
  if (starts.length > 1 || usages.length > 1 || ends.length > 1)
    throw new Error('The native Kiro goal file repeats one execution receipt.')
  const start = starts[0]
  const usage = usages[0]
  const end = ends[0]
  if (!start || !usage || !end)
    return undefined
  if (start.index >= usage.index || usage.index >= end.index)
    throw new Error('The native Kiro cancellation receipts are out of order.')
  if (start.message.id !== `${executionId}-turn-start` || usage.message.id !== `${executionId}-usage` || end.message.id !== `${executionId}-turn-end` || usage.message.payload.status !== 'aborted' || end.message.payload.stopReason !== 'cancelled')
    return undefined
  return { executionId, startMessageId: start.message.id, usageMessageId: usage.message.id, endMessageId: end.message.id }
}

/** Read only the exact native child file under the private run and HOME. */
export function readKiroGoalMessages(identity: KiroGoalSessionIdentity): KiroGoalMessage[] | null {
  if (!isAbsolute(identity.home) || !isAbsolute(identity.workingDir) || !identity.parentSessionId.trim())
    throw new Error('The native Kiro goal read requires its private HOME, working directory, and parent session.')
  const sessionId = identity.sessionId
  if (!sessionId || sessionId === '.' || sessionId === '..' || sessionId.trim() !== sessionId || basename(sessionId) !== sessionId || sessionId.includes('\\') || sessionId.includes('\0'))
    throw new Error('The native Kiro goal session ID must be one filename component.')
  assertPrivateNativePath(identity.home, identity.runDir)
  assertPrivateNativePath(identity.workingDir, identity.runDir)
  const sessions = join(identity.home, '.kiro', 'sessions')
  if (!existsSync(sessions))
    return null
  assertPrivateNativePath(sessions, identity.home)
  const matches = readdirSync(sessions, { withFileTypes: true }).filter(entry => entry.isDirectory()).map(entry => join(sessions, entry.name, sessionId)).filter(path => existsSync(join(path, 'session.json')) && existsSync(join(path, 'messages.jsonl')))
  if (matches.length === 0)
    return null
  if (matches.length !== 1)
    throw new Error('The private Kiro store contains multiple files for one native session.')
  const path = matches[0]
  if (!path)
    throw new Error('The private Kiro store contains no selected native session file.')
  const metadataFile = join(path, 'session.json')
  const messageFile = join(path, 'messages.jsonl')
  assertPrivateNativePath(metadataFile, identity.home)
  assertPrivateNativePath(messageFile, identity.home)
  const metadata: unknown = JSON.parse(readFileSync(metadataFile, 'utf8'))
  if (!isObject(metadata) || metadata.id !== sessionId || metadata.rootConversationId !== identity.parentSessionId || !Array.isArray(metadata.workspacePaths) || !metadata.workspacePaths.every((value: unknown) => typeof value === 'string') || !metadata.workspacePaths.some((value: string) => realpathSync(value) === realpathSync(identity.workingDir)))
    throw new Error('The native Kiro goal file does not identify the exact child workspace and parent session.')
  return decodeKiroGoalMessages(readFileSync(messageFile, 'utf8'))
}
