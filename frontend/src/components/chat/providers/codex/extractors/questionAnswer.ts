import type { ChatRow } from '../../../model/row'
import { CODEX_ASYNC_QUESTION_INPUT, CODEX_CONTROL_TOOL } from '~/generated/contracts/codex-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { getToolInput, getToolName } from '~/utils/controlResponse'
import { leapmuxUserRow } from '../../../leapmuxRows'

/** Format a question answer from its original text and stored question source. */
export function codexQuestionAnswerRow(payload: unknown, source: unknown): ChatRow | null {
  const row = leapmuxUserRow(payload)
  if (row?.kind !== 'user' || !isObject(source) || getToolName(source) !== CODEX_CONTROL_TOOL.AsyncQuestion)
    return row
  const title = pickString(getToolInput(source), CODEX_ASYNC_QUESTION_INPUT.Title, '')
  if (!title.trim())
    return row
  const question = title.split('\n').map(line => `> ${line.replace(/\r$/, '')}`).join('\n')
  return { ...row, text: `${question}\n\n${row.text}` }
}
