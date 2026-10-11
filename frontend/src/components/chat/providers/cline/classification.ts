import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationInput } from '../registry'
import { CLINE_EVENT } from '~/generated/contracts/cline-protocol'
import { pickString } from '~/lib/jsonPick'
import { isPlainNotificationType } from '~/lib/notificationTypes'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { notificationClassifierFor } from '../../notificationClassification'
import { retainedRowIsFinal } from '../registry'
import { clineIsNotice, clineNotificationEntry } from './extractors/notification'
import { clineEndsRun } from './extractors/resultDivider'
import { clineToolFinish, clineToolStart } from './extractors/toolCommon'
import { clineEnvelope } from './protocol'

/** Cline message classification. */
export function classifyClineMessage(input: ClassificationInput): MessageCategory {
  const parent = input.parentObject
  const wrapper = input.wrapper
  const notification = notificationClassifierFor(input.agentProvider, clineNotificationEntry)

  if (wrapper) {
    if (wrapper.messages.length === 0)
      return { kind: 'hidden' }
    // A thread of Cline notices collapses to hidden when none of them words anything.
    if (wrapper.messages.some(clineIsNotice))
      return notification(wrapper.messages, 'hidden')
    if (isNotificationThreadWrapper(wrapper))
      return notification(wrapper.messages)
  }

  if (!parent)
    return { kind: 'unknown' }

  const envelope = clineEnvelope(parent)
  if (!envelope) {
    // A row the service layer wrote: the LeapMux-neutral `{content}` user row, or one
    // of LeapMux's own notices.
    if (typeof parent.content === 'string') {
      if (parent.hidden === true)
        return { kind: 'hidden' }
      if (parent.planExecution === true)
        return { kind: 'plan_execution' }
      return { kind: 'user_content' }
    }
    if (isPlainNotificationType(pickString(parent, 'type')))
      return notification([parent])
    return { kind: 'unknown' }
  }

  switch (envelope.event) {
    case CLINE_EVENT.AssistantFinished:
    case CLINE_EVENT.AssistantMedia:
      return { kind: 'assistant_text' }
    case CLINE_EVENT.ReasoningFinished:
      return { kind: 'assistant_thinking' }
    case CLINE_EVENT.ToolStarted:
      if (!clineToolStart(parent))
        return { kind: 'unknown' }
      // A retained copy of this row is the call's RESULT: the turn ended while the call
      // ran, so Cline sent no result and the worker stored the start again.
      return retainedRowIsFinal(input.completion) ? { kind: 'tool_result' } : { kind: 'tool_use' }
    case CLINE_EVENT.ToolFinished:
      return clineToolFinish(parent) ? { kind: 'tool_result' } : { kind: 'unknown' }
    case CLINE_EVENT.SessionNotice:
    case CLINE_EVENT.TeamProgress:
      return notification([parent], 'hidden')
    default:
      break
  }
  if (clineEndsRun(envelope.event))
    return { kind: 'result_divider' }
  // An event of a later Cline: the worker keeps it so that a reader can inspect it.
  return { kind: 'unknown' }
}
