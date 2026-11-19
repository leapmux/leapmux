import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationInput } from '../registry'
import { OPENCODE_NATIVE_EVENT, OPENCODE_NATIVE_MESSAGE_TYPE, OPENCODE_NATIVE_METADATA_EVENT, OPENCODE_NATIVE_METADATA_MESSAGE_TYPE } from '~/generated/contracts/opencode-native-protocol'
import { pickString } from '~/lib/jsonPick'
import { isPlainNotificationType } from '~/lib/notificationTypes'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { notificationClassifierFor } from '../../notificationClassification'
import { openCodeNotificationEntry } from './extractors/notification'
import { openCodeToolRecord, openCodeToolSpanRole } from './extractors/toolCommon'

const NOTIFICATION_TYPES = new Set<string>([
  OPENCODE_NATIVE_EVENT.RetryScheduled,
  OPENCODE_NATIVE_EVENT.CompactionStarted,
  OPENCODE_NATIVE_EVENT.CompactionDelta,
  OPENCODE_NATIVE_EVENT.CompactionEnded,
  OPENCODE_NATIVE_EVENT.CompactionFailed,
  OPENCODE_NATIVE_MESSAGE_TYPE.Compaction,
])

/** Classify OpenCode v2 records after native supplemental resolution. */
export function classifyOpenCodeMessage(input: ClassificationInput): MessageCategory {
  const { parentObject: parent, wrapper } = input
  const notification = notificationClassifierFor(input.agentProvider, openCodeNotificationEntry)
  if (wrapper && wrapper.messages.length === 0)
    return { kind: 'hidden' }
  if (isNotificationThreadWrapper(wrapper, NOTIFICATION_TYPES))
    return notification(wrapper.messages, 'hidden')
  if (!parent)
    return { kind: 'unknown' }
  const type = pickString(parent, 'type')
  if (!type && typeof parent.content === 'string') {
    return {
      kind: parent.planExecution === true
        ? 'plan_execution'
        : 'user_content',
    }
  }
  if (type === OPENCODE_NATIVE_MESSAGE_TYPE.User)
    return { kind: 'user_content' }
  const tool = openCodeToolRecord(parent)
  if (tool) {
    return {
      kind: openCodeToolSpanRole(tool, input.completion) === 'result'
        ? 'tool_result'
        : 'tool_use',
    }
  }
  switch (type) {
    case OPENCODE_NATIVE_MESSAGE_TYPE.Idle:
    case OPENCODE_NATIVE_EVENT.ExecutionSucceeded:
    case OPENCODE_NATIVE_EVENT.ExecutionFailed:
    case OPENCODE_NATIVE_EVENT.ExecutionInterrupted:
      return { kind: 'result_divider' }
    case OPENCODE_NATIVE_EVENT.PermissionAsked:
    case OPENCODE_NATIVE_EVENT.FormCreated:
    case OPENCODE_NATIVE_EVENT.ToolInputStarted:
      return { kind: 'hidden' }
  }
  if (NOTIFICATION_TYPES.has(type))
    return notification([parent], 'hidden')
  if (OPENCODE_NATIVE_METADATA_EVENT.has(type) || OPENCODE_NATIVE_METADATA_MESSAGE_TYPE.has(type))
    return { kind: 'hidden' }
  if (isPlainNotificationType(type))
    return notification([parent])
  return { kind: 'unknown' }
}
