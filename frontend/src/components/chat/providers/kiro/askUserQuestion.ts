import type { ControlQuestion, QuestionOption } from '../../model/question'
import { KIRO_USER_INPUT_ACTION } from '~/generated/contracts/kiro-protocol'
import { isObject, pickString } from '~/lib/jsonPick'

/**
 * Kiro's question dialog, `_kiro/userInput`.
 *
 * Kiro asks ONE question, with a list of options or none. The reader chooses one
 * option or types an answer of their own, and Kiro reads the answer as text. The
 * Worker reads the request into the prompt that the browser draws, and writes the
 * answer. Here the transcript row of the question call reads the question and its
 * options, and the display of the saved answer reads the text.
 */

/** The words a recommended option carries beside its title, as Kiro's own dialog shows it. */
const KIRO_RECOMMENDED_SUFFIX = ' (recommended)'

/** One option as Kiro states it. */
interface KiroUserInputOption {
  title: string
  description: string
  recommended: boolean
}

/**
 * The options of one question, read from an untyped wire list. The model may state
 * an option as its title alone, a bare string.
 */
function kiroOptions(raw: unknown): KiroUserInputOption[] {
  if (!Array.isArray(raw))
    return []
  return raw.map(option => typeof option === 'string'
    ? { title: option }
    : option).filter(isObject).flatMap((option) => {
    const title = pickString(option, 'title')
    if (!title)
      return []
    return [
      {
        title,
        description: pickString(option, 'description'),
        recommended: option.recommended === true,
      },
    ]
  })
}

/** One option in the shared model. The value is Kiro's own title, which the answer states. */
function questionOption(option: KiroUserInputOption): QuestionOption {
  return {
    value: option.title,
    label: option.recommended
      ? `${option.title}${KIRO_RECOMMENDED_SUFFIX}`
      : option.title,
    ...(option.description
      ? { description: option.description }
      : {}),
  }
}

/**
 * One Kiro question in the shared model: the question and its options. A question
 * with no option is a free-text question.
 *
 * The sub-options of an option are not part of it: the answer states the ones the
 * reader kept, and the transcript row states the question alone.
 */
export function kiroUserInputQuestion(question: string, rawOptions: unknown): ControlQuestion {
  return {
    question,
    options: kiroOptions(rawOptions).map(questionOption),
    multiSelect: false,
  }
}

/** The answer one saved reply states, or null for a dismissal or an unreadable reply. */
export function kiroUserInputSavedAnswer(result: Record<string, unknown>): string | null {
  if (pickString(result, 'action') !== KIRO_USER_INPUT_ACTION.Answered)
    return null
  const answer = pickString(result, 'answer').trim()
  return answer || null
}
