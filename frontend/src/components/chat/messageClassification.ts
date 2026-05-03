import type { ClassificationContext, ClassificationInput } from './providers/registry'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { AgentProvider, MessageSource } from '~/generated/leapmux/v1/agent_pb'
import { parseMessageContent } from '~/lib/messageParser'
import * as chatStyles from './messageStyles.css'
import { providerFor } from './providers/registry'
import './providers'

// ---------------------------------------------------------------------------
// MessageCategory — discriminated union for single-pass message classification
// ---------------------------------------------------------------------------

export type MessageCategory
  = | { kind: 'hidden' }
    | { kind: 'notification_thread', messages: unknown[] }
    | { kind: 'notification' }
    | { kind: 'task_notification' }
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
    spanId: message.spanId,
    spanType: message.spanType,
    parentSpanId: message.parentSpanId,
    seq: message.seq,
    createdAt: message.createdAt,
  }
}

/**
 * Classify a parsed message into exactly one category.
 *
 * Always dispatches through the provider plugin registry. Each provider
 * (Claude Code, Codex, etc.) registers its own classify implementation.
 */
export function classifyMessage(
  input: ClassificationInput,
  context?: ClassificationContext,
): MessageCategory {
  const provider = input.agentProvider ?? AgentProvider.CLAUDE_CODE
  const plugin = providerFor(provider)
    ?? providerFor(AgentProvider.CLAUDE_CODE)
  if (plugin)
    return plugin.classify(input, context)
  return { kind: 'unknown' }
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

// ---------------------------------------------------------------------------
// CSS helpers — derive layout classes from category
// ---------------------------------------------------------------------------

function sourceStyle(source: MessageSource): string {
  switch (source) {
    case MessageSource.USER: return chatStyles.userMessage
    case MessageSource.AGENT: return chatStyles.assistantMessage
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
  'task_notification',
])

/**
 * Categories that must NOT clear the in-flight streaming text buffer
 * when a persisted AGENT message arrives. Notification-thread rows are
 * handled separately via `parsed.wrapper`.
 */
const NON_STREAM_CLEAR_KINDS = new Set<MessageCategory['kind']>([
  'notification',
  'notification_thread',
  'task_notification',
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
 * `kind === 'unknown'` deliberately falls through to true so any
 * unclassified AGENT shape conservatively closes the buffer rather
 * than leaving stale streaming text glued to the next message.
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

/** Row class: determines horizontal alignment. */
export function messageRowClass(kind: MessageCategory['kind'], source: MessageSource): string {
  if (kind === 'notification' || kind === 'notification_thread')
    return chatStyles.messageRowCenter
  if (!META_KINDS.has(kind) && source === MessageSource.USER)
    return chatStyles.messageRowEnd
  return chatStyles.messageRow
}

/** Bubble class: determines visual style of the message container. */
export function messageBubbleClass(kind: MessageCategory['kind'], source: MessageSource): string {
  if (kind === 'notification' || kind === 'notification_thread')
    return chatStyles.systemMessage
  if (kind === 'assistant_thinking')
    return chatStyles.thinkingMessage
  if (kind === 'plan_execution')
    return chatStyles.planExecutionMessage
  if (META_KINDS.has(kind))
    return chatStyles.metaMessage
  return sourceStyle(source)
}
