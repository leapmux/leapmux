import type { PersistedControlResponse } from './persistedControlResponse'
import type { ClassificationContext, ClassificationInput } from './providers/registry'
import type { ResolvedMessageContent } from './rowExtractionTypes'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import { AssembledMessageKind, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { parseMessageContent } from '~/lib/messageParser'
import { isWorkerWrittenNotification } from '~/lib/notificationTypes'
import { parseAssembledMessage } from './assembledMessage'
import { parsePersistedControlResponse } from './persistedControlResponse'
import { pluginFor, resolveMessageForRendering } from './providers/registry'
import './providers'

export type MessageCategory
  = | { kind: 'hidden' }
    | { kind: 'notification', messages: unknown[] }
    | { kind: 'tool_use' }
    | { kind: 'tool_result' }
    | { kind: 'agent_prompt' }
    | { kind: 'assistant_text' }
    | { kind: 'assistant_thinking' }
    | { kind: 'assistant_plan' }
    | { kind: 'user_text' }
    | { kind: 'user_content' }
    | { kind: 'plan_execution' }
    | { kind: 'result_divider' }
    | { kind: 'control_response', response: PersistedControlResponse }
    | { kind: 'compact_summary' }
    | { kind: 'unknown' }
    | { kind: 'unsupported_provider' }

export function toClassificationInput(parsed: ResolvedMessageContent, message: AgentChatMessage): ClassificationInput {
  return {
    ...parsed,
    agentProvider: message.agentProvider,
    source: message.source,
    assembledKind: message.assembledKind,
    completion: message.completion,
    spanId: message.spanId,
    spanType: message.spanType,
    parentSpanId: message.parentSpanId,
    seq: message.seq,
    createdAt: message.createdAt,
  }
}

function isLeapMuxUserPayload(input: ClassificationInput): boolean {
  return input.source === MessageSource.USER
    && input.wrapper === null
    && input.parentObject !== undefined
    && typeof input.parentObject.content === 'string'
    && !('type' in input.parentObject)
}

export function classifyMessage(input: ClassificationInput, context?: ClassificationContext): MessageCategory {
  switch (input.assembledKind) {
    case AssembledMessageKind.REASONING:
      return { kind: 'assistant_thinking' }
    case AssembledMessageKind.PLAN:
      return { kind: 'assistant_plan' }
    case AssembledMessageKind.TEXT:
      return { kind: 'assistant_text' }
  }
  const assembled = parseAssembledMessage(input.parentObject)
  if (assembled) {
    switch (assembled.kind) {
      case 'reasoning':
        return { kind: 'assistant_thinking' }
      case 'plan':
        return { kind: 'assistant_plan' }
      case 'text':
        return { kind: 'assistant_text' }
    }
  }
  const response = parsePersistedControlResponse(input)
  if (response)
    return { kind: 'control_response', response }

  const plugin = pluginFor(input.agentProvider)
  if (!plugin)
    return isLeapMuxUserPayload(input) ? { kind: 'user_content' } : { kind: 'unsupported_provider' }
  if (!input.wrapper && isWorkerWrittenNotification(input.parentObject))
    return { kind: 'notification', messages: [input.parentObject] }
  return plugin.transcript.classify(input, context)
}

const classifyCache = new WeakMap<AgentChatMessage, MessageCategory>()

export function classifyAgentMessage(message: AgentChatMessage): MessageCategory {
  const cached = classifyCache.get(message)
  if (cached)
    return cached
  const result = classifyMessage(toClassificationInput(resolveMessageForRendering(parseMessageContent(message), message.agentProvider), message))
  classifyCache.set(message, result)
  return result
}

export function invalidateMessageClassificationCache(message: AgentChatMessage): void {
  classifyCache.delete(message)
}
