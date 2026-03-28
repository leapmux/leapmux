import type { ClassificationContext, ClassificationInput } from './providers/registry'
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
    | { kind: 'agent_prompt' }
    | { kind: 'assistant_text' }
    | { kind: 'assistant_thinking' }
    | { kind: 'user_text' }
    | { kind: 'user_content' }
    | { kind: 'result_divider' }
    | { kind: 'control_response' }
    | { kind: 'compact_summary' }
    | { kind: 'unknown' }

export function buildClassificationInput(
  parsed: Pick<ClassificationInput, 'rawText' | 'topLevel' | 'parentObject' | 'wrapper'>,
  message: {
    role: MessageRole
    agentProvider?: AgentProvider
    spanId?: string
    spanType?: string
    parentSpanId?: string
    seq?: bigint
    createdAt?: string
  },
): ClassificationInput {
  return {
    ...parsed,
    messageRole: message.role,
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
  inputOrParent: ClassificationInput | Record<string, unknown> | undefined,
  wrapperOrContext?: { old_seqs: number[], messages: unknown[] } | ClassificationContext | null,
  agentProviderOrContext?: AgentProvider | ClassificationContext,
  maybeContext?: ClassificationContext,
): MessageCategory {
  const input = (() => {
    if (inputOrParent && typeof inputOrParent === 'object' && 'rawText' in inputOrParent && 'messageRole' in inputOrParent)
      return inputOrParent as ClassificationInput
    return buildClassificationInput(
      {
        rawText: '',
        topLevel: inputOrParent ?? null,
        parentObject: inputOrParent,
        wrapper: wrapperOrContext && typeof wrapperOrContext === 'object' && 'messages' in wrapperOrContext
          ? wrapperOrContext as { old_seqs: number[], messages: unknown[] }
          : null,
      },
      { role: MessageRole.SYSTEM, agentProvider: typeof agentProviderOrContext === 'number' ? agentProviderOrContext : undefined },
    )
  })()
  const context = (wrapperOrContext && typeof wrapperOrContext === 'object' && !('messages' in wrapperOrContext))
    ? wrapperOrContext as ClassificationContext
    : (typeof agentProviderOrContext === 'object' ? agentProviderOrContext as ClassificationContext : maybeContext)

  const provider = input.agentProvider ?? (typeof agentProviderOrContext === 'number' ? agentProviderOrContext : undefined) ?? AgentProvider.CLAUDE_CODE
  const plugin = getProviderPlugin(provider)
    ?? getProviderPlugin(AgentProvider.CLAUDE_CODE)
  if (plugin)
    return plugin.classify(input, context)
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
  'agent_prompt',
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
