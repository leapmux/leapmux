import { AgentProvider, MessageRole } from '~/generated/leapmux/v1/agent_pb'
import * as chatStyles from './messageStyles.css'
import { getProviderPlugin } from './providers'

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
    | { kind: 'assistant_text' }
    | { kind: 'assistant_thinking' }
    | { kind: 'user_text' }
    | { kind: 'user_content' }
    | { kind: 'result_divider' }
    | { kind: 'control_response' }
    | { kind: 'compact_summary' }
    | { kind: 'unknown' }

/**
 * Classify a parsed message into exactly one category.
 *
 * Always dispatches through the provider plugin registry. Each provider
 * (Claude Code, Codex, etc.) registers its own classify implementation.
 */
export function classifyMessage(
  parentObject: Record<string, unknown> | undefined,
  wrapper: { old_seqs: number[], messages: unknown[] } | null,
  agentProvider?: AgentProvider,
): MessageCategory {
  const provider = agentProvider ?? AgentProvider.CLAUDE_CODE
  const plugin = getProviderPlugin(provider)
    ?? getProviderPlugin(AgentProvider.CLAUDE_CODE)
  if (plugin)
    return plugin.classify(parentObject, wrapper)
  return { kind: 'unknown' }
}

// ---------------------------------------------------------------------------
// CSS helpers — derive layout classes from category
// ---------------------------------------------------------------------------

function roleStyle(role: MessageRole): string {
  switch (role) {
    case MessageRole.USER: return chatStyles.userMessage
    case MessageRole.ASSISTANT: return chatStyles.assistantMessage
    default: return chatStyles.systemMessage
  }
}

const META_KINDS = new Set<MessageCategory['kind']>([
  'hidden',
  'result_divider',
  'tool_use',
  'tool_result',
  'control_response',
  'compact_summary',
  'notification',
  'task_notification',
])

/** Row class: determines horizontal alignment. */
export function messageRowClass(kind: MessageCategory['kind'], role: MessageRole): string {
  if (kind === 'notification' || kind === 'notification_thread')
    return chatStyles.messageRowCenter
  if (!META_KINDS.has(kind) && role === MessageRole.USER)
    return chatStyles.messageRowEnd
  return chatStyles.messageRow
}

/** Bubble class: determines visual style of the message container. */
export function messageBubbleClass(kind: MessageCategory['kind'], role: MessageRole): string {
  if (kind === 'notification' || kind === 'notification_thread')
    return chatStyles.systemMessage
  if (kind === 'assistant_thinking')
    return chatStyles.thinkingMessage
  if (META_KINDS.has(kind))
    return chatStyles.metaMessage
  return roleStyle(role)
}
