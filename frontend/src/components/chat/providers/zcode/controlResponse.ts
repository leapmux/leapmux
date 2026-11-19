import type { ControlResponseSummary } from '../../model/controlResponse'
import type { ControlQuestion } from '../../model/question'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { ZCODE_ACTION, ZCODE_ANSWER_FIELD, ZCODE_DECISION, ZCODE_PLAN_CONTROL, ZCODE_REPLY_FIELD } from '~/generated/contracts/zcode-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { CONTROL_DECISION_WORDS, feedback, label } from '../../persistedControlResponse'

/** Match the installed provider's string and string-array answer normalization. */
function normalizedAnswer(value: unknown): string | undefined {
  if (typeof value === 'string')
    return value.trim() || undefined
  if (Array.isArray(value))
    return value.filter(item => typeof item === 'string').map(item => item.trim()).filter(Boolean).join(', ')
  return undefined
}

/**
 * One line for each answered question of the recorded prompt, in its order.
 *
 * The app-server reads `answers[<question text>]` first, then `answer_<index>`, then
 * `answer` for a single question, and this reads the saved reply the same way.
 *
 * The positional index counts the prompt's questions. The prompt leaves out a record
 * with neither question text nor header, which nothing can answer, so a positional
 * answer after such a record sits one index later than this reads it. The Worker also
 * keys each answer by its question text (`zcodeAnswerContent` in Go), and the keyed
 * answer comes first.
 */
function questionAnswerDisplay(questions: readonly ControlQuestion[], content: Record<string, unknown> | null): ControlResponseSummary | null {
  if (!content)
    return null
  const answers = pickObject(content, ZCODE_ANSWER_FIELD.Map)
  // The native mapper treats an explicit empty map as an empty answer, even if positional fields exist.
  if (answers && Object.keys(answers).length === 0)
    return null
  const resolved = new Map<string, string>()
  questions.forEach(({ question }, index) => {
    if (!question)
      return
    // `hasOwn`, never a bare lookup: the key is the question text the AGENT wrote,
    // and one that reads `toString` or `constructor` answers from `Object.prototype`
    // for every plain object. That answer is a function, which normalizes to
    // undefined -- and it short-circuits the positional fallback, so the reader's
    // real answer disappeared from the saved summary.
    const mapped = answers && Object.hasOwn(answers, question)
      ? answers[question]
      : undefined
    const value = mapped ?? content[`${ZCODE_ANSWER_FIELD.IndexedPrefix}${index}`]
      ?? (questions.length === 1
        ? content[ZCODE_ANSWER_FIELD.Single]
        : undefined)
    const answer = normalizedAnswer(value)
    if (answer !== undefined)
      resolved.set(question, answer)
  })
  const lines = [...resolved].map(([question, answer]) => `${question}: ${answer}`)
  return lines.length
    ? label(lines.join('\n'))
    : null
}

/** The display of the plan approval's accepted reply: the approval sentinel, or the reviewer's feedback. */
function planAnswerDisplay(content: Record<string, unknown> | null): ControlResponseSummary {
  const answers = pickObject(content, ZCODE_ANSWER_FIELD.Map)
  const answer = normalizedAnswer(answers?.[ZCODE_PLAN_CONTROL.Question]
    ?? content?.[`${ZCODE_ANSWER_FIELD.IndexedPrefix}0`] ?? content?.[ZCODE_ANSWER_FIELD.Single])
  if (answer === ZCODE_PLAN_CONTROL.Approve)
    return label(CONTROL_DECISION_WORDS.plan.allow)
  return answer
    ? feedback(answer)
    : label(CONTROL_DECISION_WORDS.plan.deny)
}

/**
 * The display of one reply to `interaction/requestUserInput`: a cancel, a decline, or
 * an accepted reply, whose content `accepted` reads.
 */
function userInputDisplay(
  result: Record<string, unknown>,
  accepted: (content: Record<string, unknown> | null) => ControlResponseSummary | null,
): ControlResponseSummary | null {
  const action = result[ZCODE_REPLY_FIELD.Action]
  if (action === ZCODE_ACTION.Cancel)
    return label('Cancel')
  if (action === ZCODE_ACTION.Decline)
    return label(CONTROL_DECISION_WORDS.plan.deny)
  if (action !== ZCODE_ACTION.Accept)
    return null
  return accepted(pickObject(result, ZCODE_REPLY_FIELD.Content))
}

/**
 * The display of one reply to `interaction/requestPermission`, in the words its own
 * buttons carried (Allow and Deny). The reply is the chosen option's own response,
 * which states a decision and not the option, so the words are the decision's.
 */
function permissionDisplay(result: Record<string, unknown>): ControlResponseSummary | null {
  switch (result.decision) {
    case ZCODE_DECISION.Allow:
      return label(CONTROL_DECISION_WORDS.permission.allow)
    case ZCODE_DECISION.Deny: {
      const reason = pickString(result, 'reason')
      const deny = CONTROL_DECISION_WORDS.permission.deny
      return label(reason.trim()
        ? `${deny}\n${reason}`
        : deny)
    }
    case ZCODE_DECISION.Escalate:
      return label('Escalated')
    case ZCODE_DECISION.Modify:
      return label('Modified')
    default:
      return null
  }
}

/**
 * Read the actual native reply.
 *
 * ZCode multiplexes the plan and the question over one RPC. The prompt that the Worker
 * recorded with the answer tells them apart, and states the questions, because the
 * banner drew that prompt: a second reading of the stored request could disagree with
 * it. A row that records no prompt reads as nothing, and the caller falls back to the
 * shared label.
 */
export function zcodeControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  const result = pickObject(cr.response, 'result')
  if (!result)
    return null
  const prompt = cr.prompt
  switch (prompt?.kind) {
    case 'permission':
      return permissionDisplay(result)
    case 'plan':
      return userInputDisplay(result, planAnswerDisplay)
    case 'question':
      return userInputDisplay(result, content => questionAnswerDisplay(prompt.questions, content))
    default:
      return null
  }
}
