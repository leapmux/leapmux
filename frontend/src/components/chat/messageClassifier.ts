import type { NotificationEntry } from './model/notification'
import type { PersistedControlResponse } from './persistedControlResponse'
import type { ClassificationContext, ClassificationInput } from './providers/registry'
import type { ResolvedMessageContent } from './rowExtractionTypes'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { SettingsLabelRead } from '~/lib/settingsLabelCache'
import { AssembledMessageKind, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { parseMessageContent } from '~/lib/messageParser'
import { isWorkerWrittenNotification } from '~/lib/notificationTypes'
import { refreshSettingsLabelRead } from '~/lib/settingsLabelCache'
import { parseAssembledMessage } from './assembledMessage'
import { classifyNotifications } from './notificationClassification'
import { parsePersistedControlResponse } from './persistedControlResponse'
import { pluginFor, resolveMessageForRendering } from './providers/registry'
import './providers'

export type MessageCategory
  = | { kind: 'hidden' }
    | { kind: 'notification', entries: NotificationEntry[] }
    | { kind: 'tool_use' }
    | { kind: 'tool_result' }
    | { kind: 'assistant_text' }
    | { kind: 'assistant_thinking' }
    | { kind: 'assistant_plan' }
    | { kind: 'user_text' }
    | { kind: 'user_content' }
    | { kind: 'plan_execution' }
    | { kind: 'result_divider' }
    | { kind: 'control_response', response: PersistedControlResponse }
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
    && input.parentObject !== null
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

  // LeapMux writes this envelope for every provider. Classify it before the
  // provider plugin so a new plugin cannot hide the user's own message.
  if (isLeapMuxUserPayload(input)) {
    if (input.parentObject?.planExecution === true)
      return { kind: 'plan_execution' }
    return { kind: 'user_content' }
  }

  const plugin = pluginFor(input.agentProvider)
  if (!plugin)
    return { kind: 'unsupported_provider' }
  if (!input.wrapper && isWorkerWrittenNotification(input.parentObject))
    return classifyNotifications([input.parentObject], input.agentProvider, plugin.transcript.notificationEntry)
  return plugin.transcript.classify(input, context)
}

const classifyCache = new WeakMap<AgentChatMessage, SettingsLabelRead<MessageCategory>>()

export function classifyAgentMessage(message: AgentChatMessage): MessageCategory {
  const cached = classifyCache.get(message)
  const classified = refreshSettingsLabelRead(cached, () =>
    classifyMessage(toClassificationInput(resolveMessageForRendering(parseMessageContent(message), message.agentProvider), message)))
  if (classified !== cached)
    classifyCache.set(message, classified)
  return classified.value
}

export function invalidateMessageClassificationCache(message: AgentChatMessage): void {
  classifyCache.delete(message)
}
