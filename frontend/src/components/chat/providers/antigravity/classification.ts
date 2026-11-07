import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationInput } from '../registry'
import { ANTIGRAVITY_FRAME, ANTIGRAVITY_STEP_TYPE } from '~/generated/contracts/antigravity-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { notificationClassifierFor } from '../../notificationClassification'
import { antigravityNotificationEntry } from './extractors/notification'
import { antigravityMessageIsChildPrompt, antigravityStepHeader, antigravityStepOpen } from './extractors/toolCommon'

export function classifyAntigravityMessage(input: ClassificationInput): MessageCategory {
  const parent = input.parentObject
  const notification = notificationClassifierFor(input.agentProvider, antigravityNotificationEntry)
  if (input.wrapper && input.wrapper.messages.length === 0)
    return { kind: 'hidden' }
  if (!parent)
    return { kind: 'unknown' }
  if (input.wrapper) {
    const category = notification([parent], 'hidden')
    if (category.kind === 'notification')
      return category
  }
  if (pickString(parent, 'name') === ANTIGRAVITY_FRAME.TurnEnd)
    return { kind: 'result_divider' }
  if (pickString(parent, 'name') === ANTIGRAVITY_FRAME.Interaction)
    return notification([parent], 'hidden')
  const step = antigravityStepHeader(parent)
  if (step) {
    switch (step.type) {
      case ANTIGRAVITY_STEP_TYPE.PlannerResponse: {
        const response = pickObject(parent, 'plannerResponse')
        const text = pickString(response, 'modifiedResponse') || pickString(response, 'response')
        if (!text && pickString(response, 'thinking'))
          return { kind: 'assistant_thinking' }
        return { kind: 'assistant_text' }
      }
      case ANTIGRAVITY_STEP_TYPE.UserInput:
        return { kind: 'user_content' }
      case ANTIGRAVITY_STEP_TYPE.SystemMessage: {
        const message = pickObject(pickObject(parent, 'systemMessage'), 'agentMessage')
        if (!message || message.hideFromUser === true || !pickString(message, 'content'))
          return { kind: 'hidden' }
        return antigravityMessageIsChildPrompt(message)
          ? { kind: 'user_content' }
          : notification([parent], 'hidden')
      }
      case ANTIGRAVITY_STEP_TYPE.ErrorMessage:
      case ANTIGRAVITY_STEP_TYPE.Checkpoint:
        return notification([parent], 'hidden')
      default:
        return antigravityStepOpen(step)
          ? { kind: 'tool_use' }
          : { kind: 'tool_result' }
    }
  }
  if (typeof parent.content === 'string') {
    if (parent.planExecution === true)
      return { kind: 'plan_execution' }
    return { kind: 'user_content' }
  }
  return { kind: 'unknown' }
}
