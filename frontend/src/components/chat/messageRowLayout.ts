import type { MessageBandKind } from './chatRowGeometry'
import type { MessageCategory } from './messageClassifier'
import { MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { messageBandKind } from './chatRowGeometry'
import * as chatStyles from './messageStyles.css'

function sourceStyle(source: MessageSource): string {
  switch (source) {
    case MessageSource.USER: return chatStyles.userMessage
    case MessageSource.AGENT: return chatStyles.agentFallbackMessage
    default: return chatStyles.systemMessage
  }
}

const META_KINDS = new Set<MessageCategory['kind']>([
  'hidden',
  'result_divider',
  'tool_use',
  'tool_result',
  'agent_prompt',
  'assistant_plan',
  'control_response',
  'compact_summary',
  'notification',
  'unsupported_provider',
])

export function isMirroredMessageRow(kind: MessageCategory['kind'], source: MessageSource): boolean {
  return !META_KINDS.has(kind) && source === MessageSource.USER
}

export function messageRowClass(kind: MessageCategory['kind'], source: MessageSource): string {
  if (kind === 'notification')
    return chatStyles.messageRowCenter
  return isMirroredMessageRow(kind, source) ? chatStyles.messageRowEnd : chatStyles.messageRow
}

export function messageBubbleClass(kind: MessageCategory['kind'], source: MessageSource): string {
  if (messageBandKind(kind))
    return chatStyles.bandMessage
  if (kind === 'notification')
    return chatStyles.systemMessage
  if (kind === 'plan_execution')
    return chatStyles.planExecutionMessage
  if (META_KINDS.has(kind))
    return chatStyles.metaMessage
  return sourceStyle(source)
}

export function rowIsWidened(kind: MessageCategory['kind'], source: MessageSource): boolean {
  return !messageBandKind(kind) && (kind === 'result_divider' || isMirroredMessageRow(kind, source))
}

export function messageRowChromeClass(kind: MessageCategory['kind'], source: MessageSource): string {
  const band = messageBandKind(kind)
  if (band)
    return band === 'thought' ? `${chatStyles.bandRow} ${chatStyles.bandRowThought}` : chatStyles.bandRow
  return rowIsWidened(kind, source) ? chatStyles.bleedRow : ''
}

export function bubbleRunsToRightEdge(kind: MessageCategory['kind'], source: MessageSource): boolean {
  return isMirroredMessageRow(kind, source) && rowIsWidened(kind, source)
}

export interface MessageRowChrome {
  class: string
  band: MessageBandKind | undefined
}

export function messageRowChrome(baseClass: string, kind: MessageCategory['kind'], source: MessageSource): MessageRowChrome {
  return {
    class: [baseClass, messageRowChromeClass(kind, source)].filter(Boolean).join(' '),
    band: messageBandKind(kind),
  }
}
