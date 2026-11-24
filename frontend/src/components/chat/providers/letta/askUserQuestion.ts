/**
 * Letta Code's `AskUserQuestion` tool call, as the transcript draws it: the
 * questions of the call, and the sentence of the receipt that it returned.
 *
 * Letta asks through `AskUserQuestion`, which posts its questions and returns a
 * receipt at once. The Worker publishes that receipt as a question request, reads
 * it into the prompt that the banner draws, and turns the reader's decision into
 * the response that Letta Code reads.
 */
import type { QuestionPrompt } from '../../model/question'
import { LETTA_QUESTION } from '~/generated/contracts/letta-protocol'
import { pickString } from '~/lib/jsonPick'
import { questionsFromRecords } from '../questionRecords'

/**
 * The questions of an `AskUserQuestion` TOOL CALL, for the transcript row that
 * draws it.
 *
 * The arguments carry the same records as the question request, so the text
 * and the option labels read the same here. The row also keeps the header and
 * the description of each option.
 */
export function lettaQuestionsFromToolInput(input: Record<string, unknown>): QuestionPrompt[] {
  return questionsFromRecords(
    input.questions,
    (question) => {
      const header = pickString(question, 'header')
      return {
        ...(header
          ? { header }
          : {}),
        question: pickString(question, 'question'),
      }
    },
    (option) => {
      const label = pickString(option, 'label')
      if (!label)
        return null
      const description = pickString(option, 'description')
      return {
        label,
        ...(description
          ? { description }
          : {}),
      }
    },
  )
}

/**
 * The sentence that a question receipt states for the reader, or undefined when
 * `returned` is no receipt.
 *
 * The receipt of an accepted `AskUserQuestion` call is a JSON object. It repeats
 * the questions, which the request row already draws, and it adds one `message`
 * that tells the reader that the answer arrives later. That message is the result
 * of the call. A return that is no receipt, such as the text of a refused call,
 * has no message here and stays as Letta Code wrote it, and so does a receipt
 * with no usable message.
 */
export function lettaQuestionReceiptMessage(returned: Record<string, unknown> | null): string | undefined {
  if (pickString(returned, LETTA_QUESTION.FieldType) !== LETTA_QUESTION.ReceiptType)
    return undefined
  const message = pickString(returned, LETTA_QUESTION.FieldMessage)
  return message.trim() === ''
    ? undefined
    : message
}
