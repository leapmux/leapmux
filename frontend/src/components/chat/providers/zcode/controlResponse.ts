import type { ControlResponseSummary } from '../../model/controlResponse'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { ZCODE_ACTION, ZCODE_ANSWER_FIELD, ZCODE_DECISION, ZCODE_METHOD, ZCODE_PLAN_CONTROL, ZCODE_REPLY_FIELD } from '~/generated/contracts/zcode-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { CONTROL_DECISION_WORDS, feedback, label } from '../../persistedControlResponse'
import { zcodeQuestionRecords, zcodeQuestionText } from './askUserQuestion'
import { zcodeExtractControl } from './extractControl'

/** Match the installed provider's string and string-array answer normalization. */
function normalizedAnswer(value: unknown): string | undefined {
  if (typeof value === 'string')
    return value.trim() || undefined
  if (Array.isArray(value))
    return value.filter(item => typeof item === 'string').map(item => item.trim()).filter(Boolean).join(', ')
  return undefined
}

/**
 * The question texts of the request, in order.
 *
 * `zcodeQuestionRecords` is the provider's ONE question list, so the reader and this
 * display agree about which question each answer belongs to. The order is what lines the
 * positional `answer_<index>` fallback up with the list the worker keyed the reply by.
 * A record with neither question text nor header contributes an empty string: it keeps
 * the INDEX of the later questions correct, and the caller skips it because nothing can
 * answer it.
 */
function requestQuestions(payload: Record<string, unknown> | null | undefined): string[] {
  if (!payload)
    return []
  return zcodeQuestionRecords(payload).map(zcodeQuestionText)
}

function questionAnswerDisplay(payload: Record<string, unknown> | null | undefined, content: Record<string, unknown> | null | undefined): ControlResponseSummary | null {
  if (!content)
    return null
  const answers = pickObject(content, ZCODE_ANSWER_FIELD.Map)
  // The native mapper treats an explicit empty map as an empty answer, even if positional fields exist.
  if (answers && Object.keys(answers).length === 0)
    return null
  const questions = requestQuestions(payload)
  const resolved = new Map<string, string>()
  questions.forEach((question, index) => {
    if (!question)
      return
    // `hasOwn`, never a bare lookup: the key is the question text the AGENT wrote,
    // and one that reads `toString` or `constructor` answers from `Object.prototype`
    // for every plain object. That answer is a function, which normalizes to
    // undefined -- and it short-circuits the positional fallback, so the reader's
    // real answer disappeared from the saved summary.
    const mapped = answers && Object.hasOwn(answers, question) ? answers[question] : undefined
    const value = mapped ?? content[`${ZCODE_ANSWER_FIELD.IndexedPrefix}${index}`]
      ?? (questions.length === 1 ? content[ZCODE_ANSWER_FIELD.Single] : undefined)
    const answer = normalizedAnswer(value)
    if (answer !== undefined)
      resolved.set(question, answer)
  })
  const lines = [...resolved].map(([question, answer]) => `${question}: ${answer}`)
  return lines.length ? label(lines.join('\n')) : null
}

/** Read the actual native reply. LeapMux stores the complete matching request separately. */
export function zcodeControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  const result = pickObject(cr.response, 'result')
  if (!result)
    return null
  const method = pickString(cr.request, 'method')
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
  // The SAME reader the banner drew the request with. ZCode multiplexes the plan and
  // the question over this one RPC, and the two surfaces read different fields of the
  // stored request to tell them apart -- the tool name here, `schema.interaction`
  // there. One request that carries only one of them made the two disagree.
  if (zcodeExtractControl({ payload: cr.request ?? {} })?.kind === 'plan') {
    const answers = pickObject(content, ZCODE_ANSWER_FIELD.Map)
    const answer = normalizedAnswer(answers?.[ZCODE_PLAN_CONTROL.Question]
      ?? content?.[`${ZCODE_ANSWER_FIELD.IndexedPrefix}0`] ?? content?.[ZCODE_ANSWER_FIELD.Single])
    if (answer === ZCODE_PLAN_CONTROL.Approve)
      return label(CONTROL_DECISION_WORDS.plan.allow)
    return answer ? feedback(answer) : label(CONTROL_DECISION_WORDS.plan.deny)
  }
  return questionAnswerDisplay(cr.request, content)
}
