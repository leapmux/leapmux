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

// ---------------------------------------------------------------------------
// Message preparation -- the one route from an AgentChatMessage to its row
//
// Reading a row takes three steps, in this order: parse the stored bytes, merge the
// provider's supplemental content into them, then classify the MERGED payload. Every
// reader of a row needs all three, and each of the four used to assemble its own
// subset:
//
//   - the transcript resolved the payload and classified the RAW bytes;
//   - the scroll rail did neither, so a row whose body LeapMux recovered previewed
//     as nothing;
//   - the image tab resolved the payload and classified the raw bytes, so a wrapped
//     ACP result extracted as a row its own category contradicted;
//   - the toolbar read the transcript's row, and so inherited its mismatch.
//
// A merged payload can classify DIFFERENTLY from the raw one -- that is the whole
// reason the merge runs before the classifier -- so the order is not a preference. A
// reader that classifies the raw bytes and extracts the merged ones asks two
// questions about two different messages and gets an answer that fits neither.
// ---------------------------------------------------------------------------

/**
 * One chat message, read into everything a row needs and nothing more.
 *
 * `original` and `resolved` are BOTH kept, and the difference is load-bearing: the
 * Raw JSON view must show the bytes the worker stored, and every display must read
 * the merge. A single field served one of the two and silently broke the other.
 */
export interface PreparedMessage {
  message: AgentChatMessage
  /** The stored bytes, exactly as the Raw JSON view shows them. */
  original: ParsedMessageContent
  /** `original` with the provider's supplemental content merged in. Every display reads this. */
  resolved: ResolvedMessageContent
  /** The category, decided from {@link PreparedMessage.resolved}. */
  category: MessageCategory
}

/** What a caller already holds, so preparation repeats none of it. */
export interface PrepareMessageOptions extends ClassificationContext {
  /**
   * The parse of the stored bytes, when the caller has one. `parseMessageContent` is
   * itself cached on the message reference, so passing it saves a hash lookup rather
   * than a parse -- but a caller that holds the SHARED resolver's parse must pass it,
   * so every reader of the row holds one object.
   */
  original?: ParsedMessageContent
  /**
   * The merged payload, when the caller has one. The shared resolver holds it per
   * message and per supplemental revision, and passing it is what keeps the
   * transcript, the toolbar and the image tab on one object.
   */
  resolved?: ResolvedMessageContent
}

/** What reading a prepared message into a row needs beyond the message itself. */
export interface PreparedRowOptions {
  /**
   * The three sides of this row's tool span, already resolved.
   *
   * Absent for a reader that resolved no siblings, and {@link soleSide} then states
   * this message as the span's only side. Supply it to state a sibling, or to state a
   * role the frame itself does not claim -- the image tab reads every span as
   * FINISHED, because a provider that states no picture before completion resolves
   * every image tab to nothing otherwise. The `role` reaches the provider, which
   * decides what to do with it; it does not overwrite the drawn row's own role.
   */
  span?: ToolSpanContext
}

/**
 * The span sides for a reader that resolved no sibling rows.
 *
 * `current` is the RESOLVED payload and never absent, which is the correction this
 * carries: a plugin reads `sides.current` for the supplemental half -- the body
 * LeapMux recovered when the provider's own frame carried none -- and for the row's
 * own role. The scroll rail left the sides empty, so every provider that recovers a
 * body into supplemental content previewed its frame without it, and a Codex row
 * previewed under the wrong role.
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
 * Cheap to call repeatedly: `parseMessageContent` is cached on the message
 * reference, and a caller that holds the resolver's parse passes it in. The
 * classification is NOT cached on the message reference -- it depends on the merged
 * payload, which moves when supplemental content arrives.
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
 * The span type and the completion come from the PREPARED MESSAGE and cannot be
 * overridden. They are columns of the row the caller asked about, so a caller that
 * supplied its own could describe a different row than the one it prepared.
 */
export function extractPreparedRow(prepared: PreparedMessage, options: PreparedRowOptions = {}): ChatRowExtraction {
  return extractChatRow(prepared.message.agentProvider, prepared.resolved, prepared.category, {
    span: options.span ?? soleSpan(prepared),
    spanType: prepared.message.spanType,
    completion: prepared.message.completion,
  })
}

/**
 * Prepare a message and read its row in one call.
 *
 * For the readers OUTSIDE the transcript -- the scroll-rail preview and the image
 * tab -- which hold a message and want its row. The transcript prepares once per row
 * and extracts under its own cache key, so it calls the two halves separately.
 */
export function prepareChatRow(
  message: AgentChatMessage,
  options: PrepareMessageOptions & PreparedRowOptions = {},
): { prepared: PreparedMessage, extraction: ChatRowExtraction } {
  const prepared = prepareMessage(message, options)
  return { prepared, extraction: extractPreparedRow(prepared, options) }
}
