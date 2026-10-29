import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationInput } from '../registry'
import { COPILOT_EVENT, COPILOT_EVENT_PREFIX, COPILOT_LEAPMUX_ROW_TYPE } from '~/generated/contracts/copilot-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { isPlainNotificationType } from '~/lib/notificationTypes'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { notificationClassifierFor } from '../../notificationClassification'
import { turnEndLabel } from '../../turnEndLabel'
import { retainedRowIsFinal } from '../registry'
import { copilotNotificationEntry } from './extractors/notification'
import { copilotEvent } from './protocol'

/**
 * The events that thread into chat as notifications.
 *
 * These events report changes that need a transcript notice.
 */
export const COPILOT_NOTIFICATION_TYPES = new Set<string>([
  COPILOT_EVENT.SessionError,
  COPILOT_EVENT.SessionWarning,
  COPILOT_EVENT.SessionInfo,
  COPILOT_EVENT.SessionCompactionComplete,
  COPILOT_EVENT.SessionContextCleared,
  COPILOT_EVENT.SessionTruncation,
  COPILOT_EVENT.SubagentCompleted,
  COPILOT_EVENT.SubagentFailed,
  COPILOT_EVENT.SystemNotification,
  // An MCP server can block its tools until the reader signs in or refreshes its headers.
  // State that reason in the transcript.
  COPILOT_EVENT.McpOauthRequired,
  COPILOT_EVENT.McpOauthCompleted,
  COPILOT_EVENT.McpHeadersRefreshRequired,
  COPILOT_EVENT.McpHeadersRefreshCompleted,
  // State whether the runtime changed the Auto tier.
  COPILOT_EVENT.SessionAutoTierRecommendation,
  COPILOT_EVENT.SessionAutoTierSwitchFailed,
  // Record a prompt schedule from /every or /after. No other surface displays it.
  COPILOT_EVENT.SessionScheduleCreated,
  COPILOT_EVENT.SessionScheduleCancelled,
  COPILOT_EVENT.SessionScheduleRearmed,
  // The runtime supplies no meaning for an extension notice. Show its name and source.
  COPILOT_EVENT.SessionCustomNotification,
])

/** Runtime trace families from the same drop table that the Worker reads. */
const COPILOT_RUNTIME_PREFIXES: readonly string[] = Object.values(COPILOT_EVENT_PREFIX)

/** Test the shared trace-prefix membership. */
function copilotDescribesRuntime(type: string): boolean {
  return COPILOT_RUNTIME_PREFIXES.some(prefix => type.startsWith(prefix))
}

/**
 * Control requests use the control surface for answers. A transcript copy states the request without action buttons.
 */
const COPILOT_CONTROL_REQUEST_TYPES = new Set<string>([
  COPILOT_EVENT.PermissionRequested,
  COPILOT_EVENT.UserInputRequested,
  COPILOT_EVENT.ExitPlanModeRequested,
  COPILOT_EVENT.ElicitationRequested,
  COPILOT_EVENT.SamplingRequested,
  COPILOT_EVENT.ExternalToolRequested,
  COPILOT_EVENT.AutoModeSwitchRequested,
  COPILOT_EVENT.ToolUserRequested,
])

/** True for a Copilot event that belongs in a notification thread. */
function copilotNotifies(entry: unknown): boolean {
  const event = copilotEvent(entry)
  return !!event && (COPILOT_NOTIFICATION_TYPES.has(event.type) || COPILOT_CONTROL_REQUEST_TYPES.has(event.type)
    || event.type === COPILOT_EVENT.SkillInvoked || event.type === COPILOT_EVENT.SkillInvokedRef
    // A native refusal can reach no control surface. State it here, and omit decisions already shown through a reader response.
    || event.type === COPILOT_EVENT.PermissionCompleted
    // A reused approval has no new control request. State which approval admitted this call.
    || event.type === COPILOT_EVENT.PermissionCarriedForward)
}

export function classifyCopilotMessage(input: ClassificationInput): MessageCategory {
  const parent = input.parentObject
  const wrapper = input.wrapper
  const notification = notificationClassifierFor(input.agentProvider, copilotNotificationEntry)

  if (wrapper) {
    if (wrapper.messages.length === 0)
      return { kind: 'hidden' }
    // A Copilot JSON-RPC frame has no top-level type. Read its event to recognize the thread.
    // Hide a thread whose entries supply no display text.
    if (wrapper.messages.some(entry => copilotNotifies(entry))) {
      return notification(wrapper.messages, 'hidden')
    }
    if (isNotificationThreadWrapper(wrapper))
      return notification(wrapper.messages)
  }

  if (!parent)
    return { kind: 'unknown' }

  if (pickString(parent, 'type') === COPILOT_LEAPMUX_ROW_TYPE.SqlTodos)
    return { kind: 'hidden' }

  // The Worker writes user input as the neutral {content} envelope. Read it before native event dispatch.
  const event = copilotEvent(parent)
  if (!event) {
    if (typeof parent.content === 'string') {
      if (parent.planExecution === true)
        return { kind: 'plan_execution' }
      return { kind: 'user_content' }
    }
    if (isPlainNotificationType(pickString(parent, 'type')))
      return notification([parent])
    return { kind: 'unknown' }
  }

  switch (event.type) {
    case COPILOT_EVENT.AssistantMessage:
      return pickString(event.data, 'content').trim()
        ? { kind: 'assistant_text' }
        : { kind: 'hidden' }
    case COPILOT_EVENT.AssistantReasoning:
      return pickString(event.data, 'content').trim()
        ? { kind: 'assistant_thinking' }
        : { kind: 'hidden' }
    case COPILOT_EVENT.ToolStarted:
      // An interrupted call can have no native completion. The Worker closes it with a copy of its start frame.
      // Read that retained copy as the result. See copilotSpanRole.
      if (retainedRowIsFinal(input.completion))
        return { kind: 'tool_result' }
      return { kind: 'tool_use' }
    case COPILOT_EVENT.ToolCompleted:
      return { kind: 'tool_result' }
    case COPILOT_EVENT.SessionIdle:
      return { kind: 'result_divider' }
  }
  if (copilotNotifies(parent)) {
    return notification([parent], 'hidden')
  }
  if (copilotDescribesRuntime(event.type))
    return { kind: 'hidden' }
  return { kind: 'unknown' }
}

/**
 * The turn-end label for one `session.idle` row.
 *
 * The runtime reports no duration for a turn, so the label carries none.
 */
export function copilotResultDivider(parsed: unknown): { label: string, isError?: boolean } | null {
  if (!isObject(parsed))
    return null
  const data = copilotEvent(parsed)?.data
  if (!data)
    return null
  return {
    label: turnEndLabel(data.aborted === true
      ? 'interrupted'
      : 'ended'),
  }
}
