import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { ContentCompression, MessageRole } from '~/generated/leapmux/v1/agent_pb'

/** Encode a JSON object as raw message content bytes (no wrapper). */
export function rawContent(obj: unknown): Uint8Array {
  return new TextEncoder().encode(JSON.stringify(obj))
}

/** Encode messages into a LEAPMUX notification wrapper envelope. */
export function wrapContent(messages: unknown[], oldSeqs: number[] = []): Uint8Array {
  return rawContent({ old_seqs: oldSeqs, messages })
}

/** Build a minimal AgentChatMessage for testing. */
export function makeMessage(overrides: Partial<{
  id: string
  role: MessageRole
  seq: bigint
  createdAt: string
  deliveryError: string
  content: Uint8Array
  contentCompression: ContentCompression
  agentProvider: number
  depth: number
  scopeId: string
  threadLines: string
}>): AgentChatMessage {
  return {
    $typeName: 'leapmux.v1.AgentChatMessage' as const,
    id: overrides.id ?? 'msg-1',
    role: overrides.role ?? MessageRole.ASSISTANT,
    seq: overrides.seq ?? 1n,
    createdAt: overrides.createdAt ?? '',
    deliveryError: overrides.deliveryError ?? '',
    content: overrides.content ?? new Uint8Array(),
    contentCompression: overrides.contentCompression ?? ContentCompression.NONE,
    depth: overrides.depth ?? 0,
    scopeId: overrides.scopeId ?? '',
    threadLines: overrides.threadLines ?? '[]',
  } as AgentChatMessage
}
