import type { MessageCategory } from './messageClassifier'
import type { ClassificationContext } from './providers/registry'
import type { ChatRowExtraction } from './rowExtraction'
import type { ResolvedMessageContent } from './rowExtractionTypes'
import type { ToolSpanContext } from '~/components/chat/rowExtractionTypes'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { parseMessageContent } from '~/lib/messageParser'
import { classifyMessage, toClassificationInput } from './messageClassifier'
import { resolvedSpanRole, resolveMessageForRendering } from './providers/registry'
import { extractChatRow } from './rowExtraction'

// Message preparation supplies one route from received rendering bytes to the row model.
// Every reader follows the same order:
// 1. Parse the received bytes.
// 2. Resolve the provider supplement.
// 3. Classify the resolved payload.
// Classification can change after resolution. Classifying raw bytes and extracting
// resolved bytes can give the transcript and its toolbar different row kinds.
// The scroll rail and image tab require the same resolved payload also.

/**
 * Keep both the original parse and the resolved payload.
 * The Raw JSON view requires the received bytes. Every display reads the resolved payload.
 * One field cannot satisfy both requirements.
 */
export interface PreparedMessage {
  message: AgentChatMessage
  /** The unmerged parse of received bytes that the Raw JSON view shows. */
  original: ParsedMessageContent
  /** Resolve original with the provider's supplemental content. Every display reads this. */
  resolved: ResolvedMessageContent
  /** The category, decided from {@link PreparedMessage.resolved}. */
  category: MessageCategory
}

/** Reuse values that the caller already holds. */
export interface PrepareMessageOptions extends ClassificationContext {
  /**
   * Reuse the caller's original parse.
   * The parser caches by message reference, so this avoids a cache lookup.
   * A caller that holds the shared resolver's parse must pass that same object.
   */
  original?: ParsedMessageContent
  /**
   * Reuse the caller's resolved payload for this message and supplemental revision.
   * The transcript and its toolbar must read the same object. The image tab uses that object also.
   */
  resolved?: ResolvedMessageContent
}

/** Supply additional data that row extraction requires. */
export interface PreparedRowOptions {
  /**
   * The request and result of this row's tool span, already resolved.
   *
   * This value is absent when a reader resolved no sibling. In that case,
   * {@link soleSpan} derives the role and visible rows from this message.
   */
  span?: ToolSpanContext
}

/**
 * Build the span context for a reader that resolved no sibling rows.
 *
 * The extraction input already carries this message's resolved content. This
 * context therefore states only its role and its presence in the loaded window.
 */
function soleSpan(prepared: PreparedMessage): ToolSpanContext {
  const role = resolvedSpanRole(prepared.resolved, prepared.message.agentProvider)
  return {
    request: undefined,
    result: undefined,
    role,
    visibleRows: { request: role === 'request', result: role === 'result' },
  }
}

/**
 * Prepare one message for every reader of its row.
 *
 * The parser caches the original parse by message reference.
 * A caller can supply its existing resolved payload also.
 * Classification runs again because a new supplement can change the resolved payload.
 */
export function prepareMessage(message: AgentChatMessage, options: PrepareMessageOptions = {}): PreparedMessage {
  const original = options.original ?? parseMessageContent(message)
  const resolved = options.resolved ?? resolveMessageForRendering(original, message.agentProvider)
  const category = classifyMessage(
    toClassificationInput(resolved, message),
    ...(options.isChildTranscript === undefined ? [] : [{ isChildTranscript: options.isChildTranscript }]),
  )
  return { message, original, resolved, category }
}

/**
 * Read a prepared message into the row model.
 *
 * The prepared message supplies the span ID and span type. It supplies completion also.
 * Callers cannot override these received values with metadata from another row.
 */
export function extractPreparedRow(prepared: PreparedMessage, options: PreparedRowOptions = {}): ChatRowExtraction {
  return extractChatRow(prepared.message.agentProvider, prepared.resolved, prepared.category, {
    span: options.span ?? soleSpan(prepared),
    spanId: prepared.message.spanId,
    spanType: prepared.message.spanType,
    completion: prepared.message.completion,
  })
}

/**
 * Prepare a message and read its row in one call. The scroll rail uses this combined
 * operation. The transcript and image tab call preparation and extraction separately.
 */
export function prepareChatRow(
  message: AgentChatMessage,
  options: PrepareMessageOptions & PreparedRowOptions = {},
): { prepared: PreparedMessage, extraction: ChatRowExtraction } {
  const prepared = prepareMessage(message, options)
  return { prepared, extraction: extractPreparedRow(prepared, options) }
}
