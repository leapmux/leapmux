import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationInput } from '../registry'
import { ANTIGRAVITY_FRAME, ANTIGRAVITY_STEP_TYPE } from '~/generated/contracts/antigravity-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { notificationClassifierFor } from '../../notificationClassification'
import { antigravityNotificationEntry } from './extractors/notification'
import { antigravityMessageIsChildPrompt, antigravityPlannerContent, antigravityRowOpen, antigravityStepDisposition, antigravityStepHeader } from './extractors/toolCommon'

export function classifyAntigravityMessage(input: ClassificationInput): MessageCategory {
  const parent = input.parentObject
  const notification = notificationClassifierFor(input.agentProvider, antigravityNotificationEntry)
  if (input.wrapper) {
    const category = notification(input.wrapper.messages, 'hidden')
    // The Worker can wrap the native system message that starts a child.
    if (category.kind !== 'hidden' || parent?.type !== ANTIGRAVITY_STEP_TYPE.SystemMessage)
      return category
  }
  if (!parent)
    return { kind: 'unknown' }
  if (pickString(parent, 'name') === ANTIGRAVITY_FRAME.TurnEnd)
    return { kind: 'result_divider' }
  if (pickString(parent, 'name') === ANTIGRAVITY_FRAME.Interaction)
    return notification([parent], 'hidden')
  const step = antigravityStepHeader(parent)
  if (step) {
    switch (antigravityStepDisposition(step.type)) {
      case 'planner': {
        const { text, thinking } = antigravityPlannerContent(parent)
        return !text && thinking
          ? { kind: 'assistant_thinking' }
          : { kind: 'assistant_text' }
      }
      case 'user':
        return { kind: 'user_content' }
      case 'system': {
        const message = pickObject(pickObject(parent, 'systemMessage'), 'agentMessage')
        if (!message || message.hideFromUser === true || !pickString(message, 'content'))
          return { kind: 'hidden' }
        return antigravityMessageIsChildPrompt(message)
          ? { kind: 'user_content' }
          : notification([parent], 'hidden')
      }
      case 'notification':
        return notification([parent], 'hidden')
      case 'tool':
        return antigravityRowOpen(step, input.completion)
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
