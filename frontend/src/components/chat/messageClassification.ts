import type { MessageBandKind } from './chatRowGeometry'
import type { ClassificationContext, ClassificationInput } from './providers/registry'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { parseMessageContent } from '~/lib/messageParser'
import { isWorkerAuthoredNotification } from '~/lib/notificationTypes'
import { messageBandKind } from './chatRowGeometry'
import * as chatStyles from './messageStyles.css'
import { isPersistedControlResponse } from './persistedControlResponse'
import { pluginFor } from './providers/registry'
import './providers'

// ---------------------------------------------------------------------------
// MessageCategory — discriminated union for single-pass message classification
// ---------------------------------------------------------------------------

export type MessageCategory
  = | { kind: 'hidden' }
    // A notification carries its message list: a consolidated thread holds the
    // wrapper's messages, and a standalone notification is just a one-element
    // thread (`messages: [parentObject]`). renderNotificationThread is the sole
    // renderer for both, so there is one notification category, not two.
    | { kind: 'notification', messages: unknown[] }
    | { kind: 'tool_use', toolName: string, toolUse: Record<string, unknown>, content: Array<Record<string, unknown>> }
    | { kind: 'tool_result' }
    | { kind: 'agent_prompt' }
    | { kind: 'assistant_text' }
    | { kind: 'assistant_thinking' }
    | { kind: 'user_text' }
    | { kind: 'user_content' }
    | { kind: 'plan_execution' }
    | { kind: 'result_divider' }
    | { kind: 'control_response' }
    | { kind: 'compact_summary' }
    | { kind: 'unknown' }
    // The message's `agentProvider` is UNSPECIFIED or has no registered plugin,
    // so we cannot interpret its wire format. Distinct from `unknown` (a known
    // provider's unrecognized shape): this is a misconfiguration -- every live
    // agent sets a real provider -- surfaced as a loud error by MessageBubble
    // rather than silently guessing one provider's renderer for another's bytes.
    | { kind: 'unsupported_provider' }

/**
 * Build the input for `classifyMessage` from a parsed envelope and an
 * `AgentChatMessage`. Keeps the common case
 * (`classifyMessage(toClassificationInput(parsed, msg))`) terse.
 */
export function toClassificationInput(
  parsed: ParsedMessageContent,
  message: AgentChatMessage,
): ClassificationInput {
  return {
    rawText: parsed.rawText,
    topLevel: parsed.topLevel,
    parentObject: parsed.parentObject,
    wrapper: parsed.wrapper,
    agentProvider: message.agentProvider,
    source: message.source,
    spanId: message.spanId,
    spanType: message.spanType,
    parentSpanId: message.parentSpanId,
    seq: message.seq,
    createdAt: message.createdAt,
  }
}

/**
 * True for LeapMux's OWN user-row payload: the flat `{content, attachments?}`
 * object every user message is persisted as.
 *
 * Tests exactly what the shared `UserContentMessage` renderer reads, and the same
 * `type`-free shape Claude's `userContentRenderer` requires, so a row this accepts
 * is one the neutral renderer can draw. The absence of `type` is what separates it
 * from a provider envelope, every one of which is tagged.
 *
 * Restricted to a USER source and to an unwrapped row: a notification thread
 * carries its own wrapper and classifies as a notification, and an AGENT row is
 * provider bytes whatever it happens to look like.
 */
function isLeapMuxUserPayload(input: ClassificationInput): boolean {
  return input.source === MessageSource.USER
    && input.wrapper === null
    && input.parentObject !== undefined
    && typeof input.parentObject.content === 'string'
    && !('type' in input.parentObject)
}

/**
 * Classify a parsed message into exactly one category.
 *
 * Dispatches strictly through the message's own provider plugin. An UNSPECIFIED
 * or unregistered provider has no plugin: we refuse to guess one (e.g. default
 * to Claude), because misreading another provider's envelope is worse than a
 * visible failure -- the result is `unsupported_provider`, which MessageBubble
 * surfaces as a loud error. A row LeapMux wrote ITSELF is the exception; see
 * {@link isLeapMuxUserPayload}.
 */
export function classifyMessage(
  input: ClassificationInput,
  context?: ClassificationContext,
): MessageCategory {
  const plugin = pluginFor(input.agentProvider)
  if (!plugin) {
    // A USER row is the one message no plugin is needed to read: LeapMux writes
    // every one of them itself, in its own flat `{content, attachments?}` shape
    // (see the SendAgentMessage handler and chatReconcile's payload extractor).
    // There are no provider bytes here to misread, so the loud error below is
    // the wrong answer for it -- and it is not hypothetical. An agent tab
    // arrives from the CRDT projection with NO worker-side metadata, because the
    // hub strips the agent payload (it is E2EE on the worker) and useTabHydrators
    // fetches it separately. Until that answers, the tab carries no
    // agentProvider, so the optimistic bubble a send builds from it claims
    // UNSPECIFIED -- and the user's own message rendered as "Unsupported agent
    // provider: Unknown (0)" for as long as the echo took to arrive, in a META
    // row that also lost the full-bleed geometry a user row is laid out with.
    if (isLeapMuxUserPayload(input))
      return { kind: 'user_content' }
    return { kind: 'unsupported_provider' }
  }

  // A persisted control-response row ({isSynthetic, controlResponse}) is a LeapMux-NEUTRAL synthetic
  // shape, not a provider wire format, so its classification lives here once instead of being
  // re-hardcoded in every plugin's classify (where a new provider plugin could forget it and render
  // the row as raw JSON). It is persisted as a standalone row, never inside a notification thread, so
  // guard on !input.wrapper to preserve the plugins' wrapper-first precedence: a notification thread
  // whose first message somehow looks synthetic still classifies as a notification, not this.
  if (!input.wrapper && isPersistedControlResponse(input.parentObject))
    return { kind: 'control_response' }

  // A worker-authored notification is provider-neutral BY CONSTRUCTION: the worker writes it, the
  // agent never does, so no plugin can recognize it from its own wire format. Classifying it here
  // makes registering one a single edit; the per-provider tables would each have to add it, and the
  // one that forgot would render the row as raw JSON.
  //
  // Scoped to WORKER_AUTHORED_NOTIFICATION_TYPES, never to NOTIFICATION_TYPE as a whole: most of that
  // vocabulary is agent-emitted and a plugin may legitimately suppress a member (Codex hides
  // `agent_error` for a turn it already reported), which a blanket test placed ahead of
  // plugin.classify would resurrect.
  if (!input.wrapper && isWorkerAuthoredNotification(input.parentObject))
    return { kind: 'notification', messages: [input.parentObject] }

  return plugin.classify(input, context)
}

/** Classify a message, returning both the parsed content and category. */
export function classifyParsedMessage(
  message: AgentChatMessage,
  classificationContext?: ClassificationContext,
) {
  const parsed = parseMessageContent(message)
  const category = classifyMessage(toClassificationInput(parsed, message), classificationContext)
  return { parsed, category }
}

// AgentChatMessage is immutable once persisted, so caching the
// context-free classification by message reference avoids redispatching
// through the provider plugin on every isAgentWorking scan. Skip when a
// ClassificationContext is supplied (MessageBubble's per-render path)
// because the classifier may consult context-dependent fields like the
// command-stream length.
//
// Solid's createStore wraps stored objects in proxies, so the wire-side
// ref passed at broadcast time and the proxy ref read by per-render
// scans have different identities. The cache therefore primarily serves
// the dominant cost — repeated isAgentWorking scans across visible
// chats — and broadcast-time hits act as one-shot warm-ups whose
// entries are GC'd once the wire ref goes out of scope.
//
// Cache safety caveat: today's consumers (isAgentWorking,
// shouldClearStreamingText) treat 'hidden' and 'assistant_thinking'
// equivalently, which is why the Codex reasoning classifier's
// context-dependent split between those two kinds is currently
// invisible to cache readers. A future caller that distinguishes them
// MUST either pass through `classifyMessage` directly or extend this
// cache key to include the relevant context bits.
const classifyCache = new WeakMap<AgentChatMessage, MessageCategory>()

/**
 * Classify a persisted AgentChatMessage. Cached by message reference.
 * `parseMessageContent` is itself WeakMap-cached on the same message
 * ref, so the inner call costs a hash lookup when the caller has
 * already parsed.
 */
export function classifyAgentMessage(message: AgentChatMessage): MessageCategory {
  const cached = classifyCache.get(message)
  if (cached)
    return cached
  const result = classifyMessage(toClassificationInput(parseMessageContent(message), message))
  classifyCache.set(message, result)
  return result
}

/**
 * Drop the memoized classification for a message whose content was replaced in
 * place under a stable reference (the store's same-seq update). Pairs with
 * `invalidateMessageParseCache`: both caches key on the immutability assumption an
 * in-place merge violates.
 */
export function invalidateMessageClassificationCache(message: AgentChatMessage): void {
  classifyCache.delete(message)
}

// ---------------------------------------------------------------------------
// CSS helpers — derive layout classes from category
// ---------------------------------------------------------------------------

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
  'control_response',
  'compact_summary',
  'notification',
  'unsupported_provider',
])

/**
 * Categories that must NOT clear the in-flight streaming text buffer
 * when a persisted AGENT message arrives. Notification rows are handled
 * separately via `parsed.wrapper`.
 */
const NON_STREAM_CLEAR_KINDS = new Set<MessageCategory['kind']>([
  'notification',
  'hidden',
  'control_response',
  'compact_summary',
  'agent_prompt',
  'plan_execution',
])

/**
 * True when a persisted AGENT message should drop the in-flight
 * streaming text buffer. Notification wrappers and meta categories
 * leave the buffer alone — only assistant-side outputs (text,
 * thinking, tool_use, tool_result) and turn-end dividers close it.
 * `kind === 'unknown'` and `kind === 'unsupported_provider'` both
 * deliberately fall through to true (neither is in NON_STREAM_CLEAR_KINDS)
 * so any unclassified or unattributable AGENT shape conservatively closes
 * the buffer rather than leaving stale streaming text glued to the next
 * message.
 */
export function shouldClearStreamingText(
  msg: { source: MessageSource },
  parsed: ParsedMessageContent,
  category: MessageCategory,
): boolean {
  if (msg.source !== MessageSource.AGENT)
    return false
  if (parsed.wrapper !== null)
    return false
  return !NON_STREAM_CLEAR_KINDS.has(category.kind)
}

/**
 * True when the row lays its bubble out at the END of the line and MIRRORS its
 * toolbar beside it (`messageRowEnd`: a right-aligned user message, whose
 * actions sit to the LEFT of the bubble in a right-to-left grid).
 *
 * Exported because the toolbar's button order depends on it: a mirrored row puts
 * Quote first so it lands nearest the bubble, where every other row puts the
 * timestamp first. Both callers read this one predicate, so the class and the
 * order can't disagree about which rows are mirrored.
 */
export function isMirroredMessageRow(kind: MessageCategory['kind'], source: MessageSource): boolean {
  // `notification` needs no test of its own: META_KINDS holds it.
  return !META_KINDS.has(kind) && source === MessageSource.USER
}

/** Row class: determines horizontal alignment. */
export function messageRowClass(kind: MessageCategory['kind'], source: MessageSource): string {
  if (kind === 'notification')
    return chatStyles.messageRowCenter
  if (isMirroredMessageRow(kind, source))
    return chatStyles.messageRowEnd
  return chatStyles.messageRow
}

/** Bubble class: determines visual style of the message container. */
export function messageBubbleClass(kind: MessageCategory['kind'], source: MessageSource): string {
  // A band's content class is the same for a message and a thought -- the solid
  // vs dashed line lives on the ROW (see messageRowChromeClass), so this must not
  // branch on the band kind again.
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

/**
 * Row class for the decoration a row paints across the FULL panel width, or ''
 * for a row that stays inside the list gutter. Three rows bleed: a message or a
 * thought paints a band behind itself, a turn-end divider runs its rule to both
 * edges, and a mirrored row's end-of-line card runs its right side to the right
 * edge.
 *
 * Belongs on the ROW element, so that the measured row and the visible row carry
 * identical chrome and cannot wrap their text at different widths. Reach it
 * through `messageRowChrome` below, which is what every row-mount site uses.
 */
export function messageRowChromeClass(kind: MessageCategory['kind'], source: MessageSource): string {
  const band = messageBandKind(kind)
  if (band)
    return band === 'thought' ? `${chatStyles.bandRow} ${chatStyles.bandRowThought}` : chatStyles.bandRow
  return rowIsWidened(kind, source) ? chatStyles.bleedRow : ''
}

/**
 * True when the row is WIDENED to the panel edge.
 *
 * A turn-end rule reaches both edges; an end-of-line card's right side reaches
 * the right one. Neither row paints anything itself, so both just widen.
 *
 * Stated as a boolean, and read by both `messageRowChromeClass` (which turns it
 * into a class) and `bubbleRunsToRightEdge` (which needs the fact). The
 * predicate used to recover it by splitting the class list and testing for one
 * token, which held only while `bleedRow` stayed a SINGLE class name --
 * vanilla-extract's `style([a, b])` returns several, and this module already
 * builds five of its classes that way. The first composition would have turned
 * the predicate false for every row, retreating every user message and plan card
 * from the panel edge with nothing failing.
 */
export function rowIsWidened(kind: MessageCategory['kind'], source: MessageSource): boolean {
  return !messageBandKind(kind) && (kind === 'result_divider' || isMirroredMessageRow(kind, source))
}

/**
 * True when the row's bubble runs its RIGHT side off the panel edge.
 *
 * Asks the two questions the stylesheet's own rule asks. The declarations that
 * move the bubble live only in the `bubbleFlushRight` rule, which is scoped to a
 * `bleedRow` ancestor (~/components/chat/messageStyles.css.ts), so a bubble
 * reaches the edge exactly when the row LAYS IT OUT at the end of the line and
 * the row is WIDENED to that edge. Reading both facts from the ROW is what keeps
 * the marker and the rule in step, and it makes the invariant the layout rests on
 * -- a bleeding bubble always sits in a widened row -- true by construction.
 *
 * The identity test this replaced asked instead whether the bubble class was
 * `userMessage`. It answered for one card only: a plan execution renders its own
 * accent card, so it kept a whole gutter of space inside a row that
 * `messageRowChromeClass` had already widened for it.
 *
 * Two row families drop out on their own, with no condition of their own here. A
 * band kind gets `bandRow` rather than `bleedRow`, and its content stretches
 * instead of hugging its text, so it is not a card that can meet an edge. A
 * turn-end divider IS widened, but its row does not mirror -- its rule reaches
 * both edges by a descendant's own margins.
 *
 * A delivery error is the one case that opts out. It stacks "Failed to deliver /
 * Retry / Delete" under the bubble, laid out against the row's CONTENT edge, so a
 * bleeding bubble above it would leave the two right edges a whole gutter apart.
 * Bleeding that line to match would be worse: it would put Retry and Delete under
 * the scroll rail's column, which takes every pointer event in its own box. An
 * undelivered message is also not part of the settled transcript that runs off
 * the panel edge, and a closed bubble with its controls squared under it says so.
 */
export function bubbleRunsToRightEdge(
  kind: MessageCategory['kind'],
  source: MessageSource,
  hasDeliveryError: boolean,
): boolean {
  return !hasDeliveryError
    && isMirroredMessageRow(kind, source)
    && rowIsWidened(kind, source)
}

/** Everything a mounted chat row's own element carries, for one `(kind, source)`. */
export interface MessageRowChrome {
  /** `baseClass` and the row's bleed decoration, joined and ready for `class=`. */
  class: string
  /** The band this row paints, or undefined. `data-band` is the e2e hook for it. */
  band: MessageBandKind | undefined
}

/**
 * The chrome for ONE mounted chat row. Every place that mounts a row calls this
 * and spreads the result, which is what keeps them identical.
 *
 * There are four such places, and they agree on nothing else: the virtual row and
 * the streaming tail (ChatView), the hidden premeasure row (chatHiddenPremeasure)
 * and, for its class alone, the positioned skeleton that stands in for an
 * unmeasured row. They differ in positioning and in role -- absolute and measured,
 * in flow, hidden and measured, an overlay -- so they are deliberately NOT one
 * component. Their CHROME is the one thing that must not differ: a row measured
 * without its band commits a height two borders and two paddings short of the
 * height it renders at, and the offset map then shifts under the reader.
 */
export function messageRowChrome(
  baseClass: string,
  kind: MessageCategory['kind'],
  source: MessageSource,
): MessageRowChrome {
  return {
    class: [baseClass, messageRowChromeClass(kind, source)].filter(Boolean).join(' '),
    band: messageBandKind(kind),
  }
}
