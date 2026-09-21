import type { MessageCategory } from './messageClassifier'
import type { NotificationEntry } from './model/notification'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '~/lib/jsonPick'
import { notificationEntriesForReader } from './notificationEntries'

export type ProviderNotificationReader = (message: Record<string, unknown>) => NotificationEntry[]

/** Classify notification messages from the entries that the row will draw. */
export function classifyNotifications(
  messages: readonly unknown[],
  agentProvider: AgentProvider | undefined,
  providerReader?: ProviderNotificationReader,
  empty: 'notification' | 'hidden' = 'notification',
): MessageCategory {
  const entries = messages.flatMap(message =>
    isObject(message) ? notificationEntriesForReader(message, agentProvider, providerReader) : [])
  return entries.length > 0 || empty === 'notification'
    ? { kind: 'notification', entries }
    : { kind: 'hidden' }
}
