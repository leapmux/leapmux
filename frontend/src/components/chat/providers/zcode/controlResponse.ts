import type { ControlResponseDisplay, PersistedControlResponse } from '../../persistedControlResponse'
import { ZCODE_ACTION, ZCODE_ANSWER_FIELD, ZCODE_DECISION, ZCODE_INTERACTION, ZCODE_METHOD, ZCODE_PLAN_CONTROL, ZCODE_REPLY_FIELD } from '~/generated/contracts/zcode-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { CONTROL_DECISION_WORDS, feedback, label } from '../../persistedControlResponse'

/** Match the installed provider's string and string-array answer normalization. */
function normalizedAnswer(value: unknown): string | undefined {
  if (typeof value === 'string')
    return value.trim() || undefined
  if (Array.isArray(value))
    return value.filter(item => typeof item === 'string').map(item => item.trim()).filter(Boolean).join(', ')
  return undefined
}

/** The native tool input supplies question order. The request's display fields permit recovery when it is absent. */
function requestQuestions(params: Record<string, unknown> | null | undefined): string[] {
  const input = pickObject(params, 'input')
  const schema = pickObject(params, 'schema')
  const questions = input?.questions ?? params?.questions ?? schema?.questions
  if (!Array.isArray(questions))
    return []
  return questions.filter(isObject).flatMap(question => typeof question.question === 'string' ? [question.question] : [])
}

function questionAnswerDisplay(params: Record<string, unknown> | null | undefined, content: Record<string, unknown> | null | undefined): ControlResponseDisplay | null {
  if (!content)
    return null
  const answers = pickObject(content, ZCODE_ANSWER_FIELD.Map)
  // The native mapper treats an explicit empty map as an empty answer, even if positional fields exist.
  if (answers && Object.keys(answers).length === 0)
    return null
  const questions = requestQuestions(params)
  const resolved = new Map<string, string>()
  questions.forEach((question, index) => {
    const value = answers?.[question] ?? content[`${ZCODE_ANSWER_FIELD.IndexedPrefix}${index}`]
      ?? (questions.length === 1 ? content[ZCODE_ANSWER_FIELD.Single] : undefined)
    const answer = normalizedAnswer(value)
    if (answer !== undefined)
      resolved.set(question, answer)
  })
  const lines = [...resolved].map(([question, answer]) => question ? `${question}: ${answer}` : answer)
  return lines.length ? label(lines.join('\n')) : null
}

/** Read the actual native reply. LeapMux stores the complete matching request separately. */
export function zcodeControlResponseDisplay(cr: PersistedControlResponse): ControlResponseDisplay | null {
  const result = pickObject(cr.response, 'result')
  if (!result)
    return null
  const method = pickString(cr.request, 'method')
  const params = pickObject(cr.request, 'params')
  // A permission answer reads the words its own buttons carried (`GenericToolActions`
  // draws Allow and Deny), and a plan approval reads the plan control's own pair.
  if (method === ZCODE_METHOD.RequestPermission) {
    switch (result.decision) {
      case ZCODE_DECISION.Allow:
        return label(CONTROL_DECISION_WORDS.permission.allow)
      case ZCODE_DECISION.Deny: {
        const reason = pickString(result, 'reason')
        const deny = CONTROL_DECISION_WORDS.permission.deny
        return label(reason.trim() ? `${deny}\n${reason}` : deny)
      }
      case ZCODE_DECISION.Escalate:
        return label('Escalated')
      case ZCODE_DECISION.Modify:
        return label('Modified')
      default:
        return null
    }
  }
  if (method !== ZCODE_METHOD.RequestUserInput)
    return null
  const action = result[ZCODE_REPLY_FIELD.Action]
  if (action === ZCODE_ACTION.Cancel)
    return label('Cancel')
  if (action === ZCODE_ACTION.Decline)
    return label(CONTROL_DECISION_WORDS.plan.deny)
  if (action !== ZCODE_ACTION.Accept)
    return null
  const content = pickObject(result, ZCODE_REPLY_FIELD.Content)
  if (pickObject(params, 'schema')?.interaction === ZCODE_INTERACTION.PlanApproval) {
    const answers = pickObject(content, ZCODE_ANSWER_FIELD.Map)
    const answer = normalizedAnswer(answers?.[ZCODE_PLAN_CONTROL.Question]
      ?? content?.[`${ZCODE_ANSWER_FIELD.IndexedPrefix}0`] ?? content?.[ZCODE_ANSWER_FIELD.Single])
    if (answer === ZCODE_PLAN_CONTROL.Approve)
      return label(CONTROL_DECISION_WORDS.plan.allow)
    return answer ? feedback(answer) : label(CONTROL_DECISION_WORDS.plan.deny)
  }
  return questionAnswerDisplay(params, content)
}
