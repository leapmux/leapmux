import type { QuestionOption, QuestionPrompt } from '../../../model/question'
import type { QuestionAnswer } from '../../../model/tools/question'
import { OH_MY_PI_ASK_OPTION, OH_MY_PI_ASK_QUESTION } from '~/generated/contracts/ohmypi-protocol'
import { isObject, pickNumber, pickString, stringArray } from '~/lib/jsonPick'

/** The word omp adds to the recommended option of a question. */
const RECOMMENDED = 'Recommended'

/**
 * The questions of one `ask` call or one question-bridge request, in omp's own record
 * shape: `{id, question, header?, options:[{label, description?, preview?}], multi?,
 * recommended?}`.
 *
 * The recommended option -- an index into `options` -- states the word omp shows
 * beside it, so the reader sees the same hint the terminal shows.
 */
export function ohMyPiQuestionPrompts(records: unknown): Array<QuestionPrompt & { id: string, multiSelect: boolean }> {
  if (!Array.isArray(records))
    return []
  return records.filter(isObject).flatMap((record) => {
    const question = pickString(record, OH_MY_PI_ASK_QUESTION.Question)
    if (!question)
      return []
    const recommended = pickNumber(record, OH_MY_PI_ASK_QUESTION.Recommended, undefined)
    const options: QuestionOption[] = Array.isArray(record[OH_MY_PI_ASK_QUESTION.Options])
      ? (record[OH_MY_PI_ASK_QUESTION.Options] as unknown[]).filter(isObject).flatMap((option, index) => {
          const label = pickString(option, OH_MY_PI_ASK_OPTION.Label)
          if (!label)
            return []
          const description = [pickString(option, OH_MY_PI_ASK_OPTION.Description), index === recommended ? RECOMMENDED : '']
            .filter(Boolean)
            .join(' · ')
          const preview = pickString(option, OH_MY_PI_ASK_OPTION.Preview)
          return [{ label, ...(description ? { description } : {}), ...(preview.trim() ? { preview } : {}) }]
        })
      : []
    const header = pickString(record, OH_MY_PI_ASK_QUESTION.Header)
    return [{
      id: pickString(record, OH_MY_PI_ASK_QUESTION.ID),
      question,
      ...(header ? { header } : {}),
      options,
      multiSelect: record[OH_MY_PI_ASK_QUESTION.Multi] === true,
    }]
  })
}

/**
 * The answer to the one question of a call, as omp states it to the model
 * (`formatSingleQuestionResponse`): the chosen labels, then the typed text.
 */
function singleAnswerText(record: Record<string, unknown>): string | null {
  const selected = stringArray(record.selectedOptions)
  const custom = pickString(record, 'customInput')
  const parts = [selected.join(', '), custom].filter(part => part.trim() !== '')
  return parts.length > 0 ? parts.join('; ') : null
}

/**
 * The answer to one question of several, as omp states it to the model
 * (`formatQuestionResult`): the typed text alone when the answer has one, else the
 * chosen labels.
 *
 * The question bridge finishes a multi-select question of such a call through
 * "Other", with the chosen labels as the typed text. The labels are then the chosen
 * options AND the typed text, and the answer states them once.
 */
function answerTextOfSeveral(record: Record<string, unknown>): string | null {
  if (typeof record.customInput === 'string')
    return record.customInput.trim() ? record.customInput : null
  const selected = stringArray(record.selectedOptions).join(', ')
  return selected.trim() ? selected : null
}

/**
 * The answers one finished `ask` call states, one per question, under each question's
 * header or text.
 *
 * omp states a single question's answer in the details themselves, and the answers of
 * several questions in `details.results`. Returns null for a result that states
 * neither, and the caller draws the result text.
 */
export function ohMyPiQuestionAnswers(details: Record<string, unknown>): QuestionAnswer[] | null {
  const header = (record: Record<string, unknown>) => pickString(record, 'question') || pickString(record, 'id') || 'Answer'
  if (Array.isArray(details.results)) {
    const records = details.results.filter(isObject)
    return records.length > 0 ? records.map(record => ({ header: header(record), answer: answerTextOfSeveral(record) })) : null
  }
  return pickString(details, 'question') ? [{ header: header(details), answer: singleAnswerText(details) }] : null
}
