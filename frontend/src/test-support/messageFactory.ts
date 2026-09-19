import type { AgentChatMessage, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
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

/**
 * One transcript frame: the fields a provider's stored row carries, spelled the
 * way a fixture states them and the way a scenario mutates them.
 *
 * `content` and `rawContent` are the two halves of one slot: the JSON-encodable
 * payload, or bytes this test must not let the encoder touch (a malformed row,
 * a captured wrapper). `supplemental` and `metadata` share the stored envelope
 * the worker writes, exactly as `makeControlResponseMessage` spells it.
 */
export interface TranscriptFrame {
  id: string
  provider: AgentProvider
  seq?: bigint
  source?: MessageSource
  spanId?: string
  spanType?: string
  agentSessionId?: string
  content?: unknown
  rawContent?: Uint8Array
  supplemental?: unknown
  metadata?: unknown
  supplementalRevision?: bigint
  completion?: MessageCompletion
}

/**
 * One transcript frame as its stored `AgentChatMessage`.
 *
 * The message id, the provider and the sequence are the identity a scenario
 * addresses a row by, so the default sequence keeps them unique per call site:
 * `makeTranscriptMessage(frame, nextSeq())` walks an archive in order.
 */
export function makeTranscriptMessage(frame: TranscriptFrame, defaultSeq: bigint): AgentChatMessage {
  if ('content' in frame && 'rawContent' in frame)
    throw new Error('A transcript frame states `content` or `rawContent`, never both.')
  const supplemental = frame.supplemental !== undefined || frame.metadata !== undefined
  return makeMessage({
    id: frame.id,
    agentProvider: frame.provider,
    seq: frame.seq ?? defaultSeq,
    ...(frame.source !== undefined ? { source: frame.source } : {}),
    ...(frame.spanId !== undefined ? { spanId: frame.spanId } : {}),
    ...(frame.spanType !== undefined ? { spanType: frame.spanType } : {}),
    ...(frame.agentSessionId !== undefined ? { agentSessionId: frame.agentSessionId } : {}),
    content: frame.rawContent ?? rawContent(frame.content ?? null),
    ...(supplemental
      ? {
          supplementalContent: rawContent({ provider: frame.supplemental, metadata: frame.metadata }),
          supplementalRevision: frame.supplementalRevision ?? 1n,
        }
      : {}),
    ...(frame.completion !== undefined ? { completion: frame.completion } : {}),
  })
}
