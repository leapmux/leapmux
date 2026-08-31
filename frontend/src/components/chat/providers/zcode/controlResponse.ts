/**
 * Display derivation for a persisted ZCode control-response row.
 *
 * ZCode's answers travel as LeapMux's NEUTRAL allow/deny envelope, not as a native
 * ZCode frame: the worker translates the envelope into the app-server's reply
 * (`{decision}` for a permission, `{action, content}` for a question) at the moment
 * it forwards it, so what gets stored is the neutral shape. The neutral derivation
 * therefore already reads every field of it.
 *
 * What this module adds is the ANSWER TEXT of a question. The neutral derivation
 * reports a bare "Approved" for an allow, which for an AskUserQuestion loses the
 * thing the user actually decided -- so the picked labels are read out of
 * `updatedInput.answers` and rendered as `question: answer` lines instead.
 */

import type { ControlResponseDisplay, PersistedControlResponse } from '../../persistedControlResponse'
import { ZCODE_TOOL } from '~/generated/contracts/zcode-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { decodeControlBehaviorEnvelope } from '~/utils/controlResponse'
import {
  controlBehaviorDisplay,
  joinAnswerLines,
  labelOrNull,
} from '../../persistedControlResponse'
import { ZCODE_METHOD } from './protocol'

/**
 * The `answers` map the shared AskUserQuestion control attaches to its allow
 * envelope: question text -> the option labels the user picked, comma-joined.
 *
 * A blank answer is dropped: the app-server discards one too, so showing it would
 * claim an answer that was never delivered.
 */
function zcodeAnswers(response: Record<string, unknown> | undefined): Record<string, string> {
  const inner = pickObject(pickObject(response, 'response'), 'response')
  const answers = pickObject(pickObject(inner, 'updatedInput'), 'answers')
  if (!answers)
    return {}
  const out: Record<string, string> = {}
  for (const [question, answer] of Object.entries(answers)) {
    if (typeof answer === 'string' && answer.trim() !== '')
      out[question] = answer.trim()
  }
  return out
}

/** The question texts the pruned request context lists, in the order it declared them. */
function zcodeRequestQuestions(request: Record<string, unknown> | undefined): string[] {
  const questions = request?.questions
  if (!Array.isArray(questions))
    return []
  return questions.flatMap(q => (isObject(q) ? [pickString(q, 'question')] : []))
}

/**
 * Render the answers as `question: answer` lines, ordered by the pruned request's
 * own question list.
 *
 * The request's order is used rather than the map's, so two rows of the same
 * multi-question prompt read identically -- object key order is a detail of whoever
 * serialized the map. An answer whose question the request does not name is appended
 * rather than dropped, which covers a stale or absent request context.
 */
function zcodeAnswerLines(cr: PersistedControlResponse): string | null {
  const answers = zcodeAnswers(cr.response)
  if (Object.keys(answers).length === 0)
    return null

  const lines: string[] = []
  const rendered = new Set<string>()
  const line = (question: string, answer: string): string =>
    question ? `${question}: ${answer}` : answer

  for (const question of zcodeRequestQuestions(cr.request)) {
    const answer = answers[question]
    if (answer === undefined || rendered.has(question))
      continue
    rendered.add(question)
    lines.push(line(question, answer))
  }
  for (const [question, answer] of Object.entries(answers)) {
    if (rendered.has(question))
      continue
    lines.push(line(question, answer))
  }
  return joinAnswerLines(lines)
}

/**
 * Whether the answered request was an AskUserQuestion rather than a permission or a
 * plan approval.
 *
 * Both fields of the pruned request are checked: the method rules out a permission,
 * and the tool name rules out the plan approval, which travels over the SAME method.
 */
function zcodeIsQuestion(cr: PersistedControlResponse): boolean {
  const request = cr.request
  if (!request || pickString(request, 'method') !== ZCODE_METHOD.RequestUserInput)
    return false
  return pickString(pickObject(request, 'request'), 'tool_name') !== ZCODE_TOOL.ExitPlanMode
}

/**
 * Derive the display for a persisted ZCode answer.
 *
 * An ALLOWED question renders its answers. Everything else -- a permission decision,
 * a plan approval, and every denial with or without feedback -- is exactly what the
 * neutral envelope already describes, so it delegates. Returns null when the payload
 * is not that envelope at all, which lets the caller degrade.
 */
export function zcodeControlResponseDisplay(cr: PersistedControlResponse): ControlResponseDisplay | null {
  if (decodeControlBehaviorEnvelope(cr.response)?.behavior === 'allow' && zcodeIsQuestion(cr)) {
    const answers = labelOrNull(zcodeAnswerLines(cr))
    if (answers)
      return answers
  }
  return controlBehaviorDisplay(cr.response)
}
