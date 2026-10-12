import type { ControlResponseSummary } from '../../model/controlResponse'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { label } from '../../persistedControlResponse'
import { museQuestions } from './control'

/**
 * The saved answer of one native Muse control request.
 *
 * The browser answers a user-input prompt with a JSON-RPC result whose `answers`
 * array carries one entry per question -- `questionId` plus `selectedLabel`
 * (single), `selectedLabels` (multiple), or `freeText`, each with an optional
 * `note` -- exactly the shape the Worker forwards as `userInput/answer`. The
 * saved approval decision travels as `approval/decide` instead, so a bare
 * permission answer reaches the neutral envelope and needs no reader here.
 *
 * A question shows one `question: answer` line per answered question, in the
 * order the prompt asked them; a typed answer shows its text, and a note rides
 * the same line after the values it qualifies.
 */
export function museControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  // The Worker stores the reply it would post to the native host: the
  // `userInput/answer` method envelope whose params carry the answers.
  const params = pickObject(cr.response, 'params', undefined)
  const answers = params?.answers
  if (!Array.isArray(answers))
    return null
  const questions = museQuestions(cr.request ?? {})
  const byId = new Map(answers.filter(isObject).map(answer => [pickString(answer, 'questionId'), answer]))
  const lines: string[] = []
  for (const question of questions) {
    const answer = byId.get(question.id ?? '')
    if (!answer)
      continue
    const values: string[] = []
    const single = pickString(answer, 'selectedLabel', '')
    if (single)
      values.push(single)
    const multiple = answer.selectedLabels
    if (Array.isArray(multiple)) {
      for (const value of multiple) {
        if (typeof value === 'string' && value)
          values.push(value)
      }
    }
    const typed = pickString(answer, 'freeText')
    if (typed)
      values.push(typed)
    const note = pickString(answer, 'note')
    if (note)
      values.push(note)
    if (values.length === 0)
      return null
    lines.push(`${question.question}: ${values.join(', ')}`)
  }
  // Every answer must address a question the prompt asked, and every asked
  // question needs its answer: a stray or missing entry is not this request's
  // saved response.
  if (lines.length !== answers.length || lines.length === 0)
    return null
  return label(lines.join('\n'))
}
