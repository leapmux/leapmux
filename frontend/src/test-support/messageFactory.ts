import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import { create } from '@bufbuild/protobuf'
import { NOTIFICATION_THREAD_TYPE } from '~/generated/contracts/worker-vocab'
import { AgentChatMessageSchema, AgentProvider, ContentCompression, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'

/** Encode a JSON object as raw message content bytes (no wrapper). */
export function rawContent(obj: unknown): Uint8Array {
  return new TextEncoder().encode(JSON.stringify(obj))
}

/** Encode messages into a notification-thread wrapper envelope. */
export function wrapContent(messages: unknown[], oldSeqs: number[] = []): Uint8Array {
  return rawContent({ type: NOTIFICATION_THREAD_TYPE, old_seqs: oldSeqs, messages })
}

/** Build a minimal AgentChatMessage for testing. */
export function makeMessage(overrides: Partial<Omit<AgentChatMessage, '$typeName' | '$unknown'>>): AgentChatMessage {
  return create(AgentChatMessageSchema, {
    ...overrides,
    id: overrides.id ?? 'msg-1',
    source: overrides.source ?? MessageSource.AGENT,
    seq: overrides.seq ?? 1n,
    contentCompression: overrides.contentCompression ?? ContentCompression.NONE,
    supplementalContentCompression: overrides.supplementalContentCompression ?? ContentCompression.NONE,
    spanLines: overrides.spanLines ?? '[]',
    agentProvider: overrides.agentProvider ?? AgentProvider.CLAUDE_CODE,
    spanColor: overrides.spanColor ?? -1,
  })
}
