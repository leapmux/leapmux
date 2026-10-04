import type { ControlResponseSummary } from '../../model/controlResponse'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { DEEPSEEK_HARNESS_CONTROL_EVENT } from '~/generated/contracts/deepseek-harness-protocol'
import { isObject, pickObject, pickString, stringArray } from '~/lib/jsonPick'
import { decodeControlBehaviorEnvelope } from '~/utils/controlResponse'
import { CONTROL_DECISION_WORDS, feedbackOrLabel, joinAnswerLines, label } from '../../persistedControlResponse'
import { deepseekHarnessQuestionRecords } from './askUserQuestion'

/** Read the exact saved native choices through their original question IDs. */
export function deepseekHarnessControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  const decision = decodeControlBehaviorEnvelope(cr.response)
  if (!decision || !cr.request || decision.requestId !== cr.requestId)
    return null
  if (cr.request.event === DEEPSEEK_HARNESS_CONTROL_EVENT.Approval)
    return decision.behavior === 'allow' ? label('Allow once') : feedbackOrLabel(decision.message, CONTROL_DECISION_WORDS.permission.deny)
  if (cr.request.event !== DEEPSEEK_HARNESS_CONTROL_EVENT.UserQuestions)
    return null
  const questions = deepseekHarnessQuestionRecords(cr.request)
  const plan = questions.length === 1 ? questions[0] : undefined
  if (pickObject(plan, 'intent')?.kind === 'plan-review')
    return decision.behavior === 'allow' ? label(CONTROL_DECISION_WORDS.plan.allow) : feedbackOrLabel(decision.message, 'Keep planning')
  if (decision.behavior === 'deny')
    return feedbackOrLabel(decision.message, 'Dismissed')
  const native = pickObject(pickObject(cr.response, 'response'), 'response')
  if (!Array.isArray(native?.answers))
    return null
  const answers = native.answers.filter(isObject)
  if (answers.length !== native.answers.length)
    return null
  const ids = answers.map(answer => pickString(answer, 'id'))
  if (ids.some(id => !id) || new Set(ids).size !== ids.length)
    return null
  const lines: string[] = []
  for (const question of questions) {
    const answer = answers.find(answer => answer.id === question.id)
    if (!answer)
      return null
    const offered = Array.isArray(question.options) ? question.options.filter(isObject).map(option => pickString(option, 'label')) : []
    const selected = stringArray(answer.selected)
    if (selected.some(value => !offered.includes(value)))
      return null
    const custom = pickString(answer, 'custom')
    const text = [...selected, ...(custom ? [custom] : [])].join(', ')
    if (text)
      lines.push(`${pickString(question, 'question') || pickString(question, 'header') || pickString(question, 'id')}: ${text}`)
  }
  const text = joinAnswerLines(lines)
  return text ? label(text) : label('No answer')
}
