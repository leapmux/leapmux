import type { PermissionOption } from '../../model/controlPrompt'
import type { ControlResponseSummary } from '../../model/controlResponse'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { permissionOptionLabel } from '../../controls/permissionOptionLabels'
import { feedbackOrLabel, label } from '../../persistedControlResponse'

/**
 * The saved answer of one native Muse control request.
 *
 * The Worker answers a user-input prompt with the `userInput/answer` method
 * envelope. Its `answers` array carries one entry per question -- `questionId`
 * plus `selectedLabel` (single), `selectedLabels` (multiple), or `freeText`,
 * each with an optional `note`. The Worker answers an approval with
 * `approval/decide` instead, which states the chosen `choiceId`, the typed
 * `feedback` of a refusal, and no `answers`.
 *
 * An approval shows the label of the option that the reader chose, which the
 * recorded permission prompt states for the `choiceId`, or the typed feedback.
 *
 * A question shows one `question: answer` line per answered question, in the
 * order that the recorded prompt asked them; a typed answer shows its text, and
 * a note rides the same line after the values it qualifies.
 */
export function museControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  // The Worker stores the reply that it posts to the native host: a method
  // envelope whose params carry the answer.
  const params = pickObject(cr.response, 'params', undefined)
  if (cr.prompt?.kind === 'permission')
    return museApprovalSummary(params, cr.prompt.permission.options)
  const answers = params?.answers
  if (!Array.isArray(answers))
    return null
  const questions = cr.prompt?.kind === 'question'
    ? cr.prompt.questions
    : []
  const byId = new Map(answers.filter(isObject).map(answer => [
    pickString(answer, 'questionId'),
    answer,
  ]))
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
  // Every answer entry must be an object that addresses its own question of
  // the prompt: a stray, unreadable or repeated entry is not this request's
  // saved response. A question with no answer shows no line.
  if (lines.length !== answers.length || lines.length === 0)
    return null
  return label(lines.join('\n'))
}

/**
 * The display of one saved approval: the typed feedback of a refusal, or else the
 * label of the chosen option. A choice that the prompt does not offer reads as
 * nothing, so the row falls back to the generic label.
 */
function museApprovalSummary(params: Record<string, unknown> | undefined, options: readonly PermissionOption[]): ControlResponseSummary | null {
  const choiceId = pickString(params, 'choiceId')
  const option = choiceId
    ? options.find(candidate => candidate.optionId === choiceId)
    : undefined
  if (!option)
    return null
  return feedbackOrLabel(pickString(params, 'feedback').trim(), permissionOptionLabel(option))
}
