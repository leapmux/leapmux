/**
 * Letta Code's question requests: recognize one, read its questions, and read
 * the questions of the tool call that posted it.
 *
 * Letta asks through `AskUserQuestion`, which posts its questions and returns a
 * receipt at once. The Worker publishes that receipt as a question request: its
 * `tool_input` holds the `questions` of the call, verbatim. The answer returns
 * through the shared question control, which folds the answers into that input.
 * The Worker turns the answer into the response that Letta Code reads.
 */
import type { ControlQuestion, QuestionPrompt } from '../../model/question'
import { LETTA_DELTA_FIELD, LETTA_QUESTION } from '~/generated/contracts/letta-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { questionsFromRecords } from '../questionRecords'

/** Whether a stored control payload is a question request. */
export function lettaIsQuestionRequest(payload: Record<string, unknown>): boolean {
  return pickString(payload, 'type') === 'ask_user'
}

/** The input of the question call that a stored question request carries. */
export function lettaQuestionToolInput(payload: Record<string, unknown>): Record<string, unknown> {
  return pickObject(payload, LETTA_DELTA_FIELD.ToolInput) ?? {}
}

/** The questions of a stored question request, for the shared control. */
export function lettaQuestionsFromPayload(payload: Record<string, unknown>): ControlQuestion[] {
  const toolInput = lettaQuestionToolInput(payload)
  const questions = Array.isArray(toolInput.questions) ? toolInput.questions : []
  return questions.filter(isObject).map((question) => {
    const options = Array.isArray(question.options) ? question.options : []
    return {
      question: pickString(question, 'question'),
      // Letta's options are `{label, description}` OBJECTS, not bare strings.
      // Reading them as strings dropped every one and the banner drew a
      // question with no choices.
      options: options.flatMap((option) => {
        const label = typeof option === 'string' ? option : pickString(option, 'label')
        return label ? [{ value: label, label }] : []
      }),
      multiSelect: question.multiSelect === true,
    }
  })
}

/**
 * The questions of an `AskUserQuestion` TOOL CALL, for the transcript row that
 * draws it.
 *
 * The arguments carry the same records as the question request, so the text
 * and the option labels read the same here. The row also keeps the header and
 * the description of each option, which the banner has no place for.
 */
export function lettaQuestionsFromToolInput(input: Record<string, unknown>): QuestionPrompt[] {
  return questionsFromRecords(
    input.questions,
    (question) => {
      const header = pickString(question, 'header')
      return { ...(header ? { header } : {}), question: pickString(question, 'question') }
    },
    (option) => {
      const label = pickString(option, 'label')
      if (!label)
        return null
      const description = pickString(option, 'description')
      return { label, ...(description ? { description } : {}) }
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
  return message.trim() === '' ? undefined : message
}
