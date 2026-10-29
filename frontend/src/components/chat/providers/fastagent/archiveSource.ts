import type { ParsedMessageContent } from '~/lib/messageParser'
import { FASTAGENT_ARCHIVE } from '~/generated/contracts/fastagent-protocol'
import { isObject, pickObject } from '~/lib/jsonPick'

type ArchiveRole = typeof FASTAGENT_ARCHIVE.AssistantRole | typeof FASTAGENT_ARCHIVE.UserRole
type ArchiveInput = Pick<ParsedMessageContent, 'parentObject' | 'supplementalContent' | 'agentSessionId'>
interface ArchiveIdentity {
  captureId: string
  sessionId: string
  actor: string
  historyFile: string
  messageIndex: number
  role: ArchiveRole
}
export interface FastAgentNativeRequest {
  raw: Record<string, unknown>
  name: string
  args: Record<string, unknown>
}
interface ArchiveToolIdentity extends ArchiveIdentity {
  callId: string
  spanId: string
  request: FastAgentNativeRequest
}
export type FastAgentArchiveSource
  = | (ArchiveIdentity & { kind: 'content' | 'reasoning' | 'error', index: number, text: string })
    | (ArchiveIdentity & { kind: 'metadata', index: number, channel: string, raw: Record<string, unknown> })
    | (ArchiveToolIdentity & { kind: 'request' })
    | (ArchiveToolIdentity & { kind: 'result', result: Record<string, unknown>, failed: boolean })

const METADATA_CHANNELS: ReadonlySet<string> = new Set([
  FASTAGENT_ARCHIVE.TimingChannel,
  FASTAGENT_ARCHIVE.ToolTimingChannel,
  FASTAGENT_ARCHIVE.ToolMetadataChannel,
  FASTAGENT_ARCHIVE.UsageChannel,
  FASTAGENT_ARCHIVE.ShellProcessMetadataChannel,
])
function nonempty(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0
}
function nativeIndex(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
}
function nativeHistoryFile(value: unknown): value is string {
  return typeof value === 'string' && /^history_[^/\\\0\r\n]+\.json$/u.test(value)
}
export function hasFastAgentArchiveSource(input: ArchiveInput): boolean {
  return isObject(input.supplementalContent) && Object.hasOwn(input.supplementalContent, FASTAGENT_ARCHIVE.Namespace)
}
export function fastAgentNativeRequest(raw: unknown): FastAgentNativeRequest | undefined {
  if (!isObject(raw) || raw.method !== 'tools/call')
    return undefined
  const params = pickObject(raw, 'params')
  if (!params || !nonempty(params.name))
    return undefined
  let args: Record<string, unknown> = {}
  if (params.arguments !== undefined && params.arguments !== null) {
    if (!isObject(params.arguments))
      return undefined
    args = params.arguments
  }
  return { raw, name: params.name, args }
}
function validNativeResult(raw: unknown): raw is Record<string, unknown> {
  return isObject(raw) && Array.isArray(raw.content)
    && raw.content.every(block => isObject(block) && nonempty(block.type))
    && (raw.isError === undefined || typeof raw.isError === 'boolean')
}
export function fastAgentArchiveSource(input: ArchiveInput): FastAgentArchiveSource | undefined {
  if (!isObject(input.parentObject) || !isObject(input.supplementalContent))
    return undefined
  const meta = input.supplementalContent[FASTAGENT_ARCHIVE.Namespace]
  if (!isObject(meta) || meta[FASTAGENT_ARCHIVE.FormatKey] !== FASTAGENT_ARCHIVE.Format)
    return undefined
  const captureId = meta[FASTAGENT_ARCHIVE.CaptureID]
  const sessionId = meta[FASTAGENT_ARCHIVE.SessionID]
  const actor = meta[FASTAGENT_ARCHIVE.Actor]
  const historyFile = meta[FASTAGENT_ARCHIVE.HistoryFile]
  const messageIndex = meta[FASTAGENT_ARCHIVE.MessageIndex]
  const role = meta[FASTAGENT_ARCHIVE.Role]
  if (!nonempty(captureId) || !nonempty(sessionId) || sessionId !== input.agentSessionId
    || !nonempty(actor) || !nativeHistoryFile(historyFile) || !nativeIndex(messageIndex)
    || (role !== FASTAGENT_ARCHIVE.AssistantRole && role !== FASTAGENT_ARCHIVE.UserRole)) {
    return undefined
  }
  if (meta[FASTAGENT_ARCHIVE.Timestamp] !== undefined && typeof meta[FASTAGENT_ARCHIVE.Timestamp] !== 'string')
    return undefined
  const identity: ArchiveIdentity = {
    captureId,
    sessionId,
    actor,
    historyFile,
    messageIndex,
    role,
  }
  const raw = input.parentObject
  const field = meta[FASTAGENT_ARCHIVE.Field]
  if (field === FASTAGENT_ARCHIVE.Content || field === FASTAGENT_ARCHIVE.Channels) {
    const index = meta[FASTAGENT_ARCHIVE.Index]
    if (!nativeIndex(index))
      return undefined
    if (field === FASTAGENT_ARCHIVE.Content) {
      if (raw.type !== 'text' || typeof raw.text !== 'string')
        return undefined
      return {
        ...identity,
        kind: 'content',
        index,
        text: raw.text,
      }
    }
    const channel = meta[FASTAGENT_ARCHIVE.Channel]
    if (!nonempty(channel))
      return undefined
    if (METADATA_CHANNELS.has(channel)) {
      return {
        ...identity,
        kind: 'metadata',
        index,
        channel,
        raw,
      }
    }
    if (raw.type !== 'text' || typeof raw.text !== 'string')
      return undefined
    if (channel === FASTAGENT_ARCHIVE.Reasoning && role === FASTAGENT_ARCHIVE.AssistantRole) {
      return {
        ...identity,
        kind: 'reasoning',
        index,
        text: raw.text,
      }
    }
    if (channel === FASTAGENT_ARCHIVE.ErrorChannel) {
      return {
        ...identity,
        kind: 'error',
        index,
        text: raw.text,
      }
    }
    return undefined
  }
  if (field !== FASTAGENT_ARCHIVE.ToolCalls && field !== FASTAGENT_ARCHIVE.ToolResults)
    return undefined
  const callId = meta[FASTAGENT_ARCHIVE.CallID]
  const spanId = meta[FASTAGENT_ARCHIVE.SpanID]
  const toolName = meta[FASTAGENT_ARCHIVE.ToolName]
  if (!nonempty(callId) || !nonempty(spanId) || !nonempty(toolName))
    return undefined
  const request = fastAgentNativeRequest(field === FASTAGENT_ARCHIVE.ToolCalls
    ? raw
    : meta[FASTAGENT_ARCHIVE.Request])
  if (!request || request.name !== toolName)
    return undefined
  const tool: ArchiveToolIdentity = { ...identity, callId, spanId, request }
  if (field === FASTAGENT_ARCHIVE.ToolCalls) {
    return role === FASTAGENT_ARCHIVE.AssistantRole
      ? { ...tool, kind: 'request' }
      : undefined
  }
  if (role !== FASTAGENT_ARCHIVE.UserRole || !validNativeResult(raw))
    return undefined
  return {
    ...tool,
    kind: 'result',
    result: raw,
    failed: raw.isError === true,
  }
}
export function sameFastAgentArchiveCall(left: FastAgentArchiveSource, right: FastAgentArchiveSource): boolean {
  if ((left.kind !== 'request' && left.kind !== 'result') || (right.kind !== 'request' && right.kind !== 'result'))
    return false
  return left.captureId === right.captureId && left.sessionId === right.sessionId
    && left.actor === right.actor && left.historyFile === right.historyFile
    && left.callId === right.callId && left.spanId === right.spanId
    && JSON.stringify(left.request.raw) === JSON.stringify(right.request.raw)
}
export function fastAgentNativeResultText(result: Record<string, unknown>): string {
  if (!Array.isArray(result.content))
    return ''
  return result.content.filter(isObject)
    .filter(block => block.type === 'text' && typeof block.text === 'string')
    .map(block => typeof block.text === 'string'
      ? block.text
      : '')
    .join('\n')
}
