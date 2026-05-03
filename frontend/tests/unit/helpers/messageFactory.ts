import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { AgentProvider, ContentCompression, MessageSource } from '~/generated/leapmux/v1/agent_pb'
import { NOTIFICATION_THREAD_TYPE } from '~/lib/messageParser'

/** Encode a JSON object as raw message content bytes (no wrapper). */
export function rawContent(obj: unknown): Uint8Array {
  return new TextEncoder().encode(JSON.stringify(obj))
}

/** Encode messages into a notification-thread wrapper envelope. */
export function wrapContent(messages: unknown[], oldSeqs: number[] = []): Uint8Array {
  return rawContent({ type: NOTIFICATION_THREAD_TYPE, old_seqs: oldSeqs, messages })
}

/** Build a minimal AgentChatMessage for testing. */
export function makeMessage(overrides: Partial<{
  id: string
  source: MessageSource
  seq: bigint
  createdAt: string
  deliveryError: string
  content: Uint8Array
  contentCompression: ContentCompression
  agentProvider: number
  depth: number
  spanId: string
  parentSpanId: string
  spanType: string
  spanLines: string
  spanColor: number
}>): AgentChatMessage {
  return {
    $typeName: 'leapmux.v1.AgentChatMessage' as const,
    id: overrides.id ?? 'msg-1',
    source: overrides.source ?? MessageSource.AGENT,
    seq: overrides.seq ?? 1n,
    createdAt: overrides.createdAt ?? '',
    deliveryError: overrides.deliveryError ?? '',
    content: overrides.content ?? new Uint8Array(),
    contentCompression: overrides.contentCompression ?? ContentCompression.NONE,
    depth: overrides.depth ?? 0,
    spanId: overrides.spanId ?? '',
    parentSpanId: overrides.parentSpanId ?? '',
    spanType: overrides.spanType ?? '',
    spanLines: overrides.spanLines ?? '[]',
    agentProvider: overrides.agentProvider ?? AgentProvider.CLAUDE_CODE,
    spanColor: overrides.spanColor ?? -1,
  } as AgentChatMessage
}
