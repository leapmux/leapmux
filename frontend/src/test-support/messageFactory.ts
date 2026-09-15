import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import { create } from '@bufbuild/protobuf'
import { MESSAGE_METADATA_FIELD, NOTIFICATION_THREAD_TYPE } from '~/generated/contracts/worker-vocab'
import { AgentChatMessageSchema, AgentProvider, ContentCompression, MarkType, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'

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

/** Keep native response and request fixtures separate from the worker's control identity. */
export function makeControlResponseMessage(
  provider: AgentProvider,
  response: unknown,
  request?: unknown,
): AgentChatMessage {
  return makeMessage({
    agentProvider: provider,
    source: MessageSource.USER,
    markType: MarkType.CONTROL_RESPONSE,
    content: rawContent(response),
    supplementalContent: rawContent({
      provider: request,
      metadata: {
        [MESSAGE_METADATA_FIELD.ControlRequestID]: 'request-1',
        [MESSAGE_METADATA_FIELD.ControlRequestClaimToken]: 'claim-1',
      },
    }),
  })
}
