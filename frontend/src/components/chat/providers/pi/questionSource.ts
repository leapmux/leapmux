import type { QuestionIR } from '../../ir/questionBody'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { PI_DIALOG_METHOD, PI_EVENT, PI_TOOL } from '~/generated/contracts/pi-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { questionsFromRecords } from '../questionRecords'

interface SourceOption {
  label: string
  description: string
  preview?: string
}

export interface PiSourceQuestion {
  index: number
  prompt: string
  header: string
  title: string
  multiSelect: boolean
  options: SourceOption[]
}

const text = (value: string) => value.replaceAll('\r\n', '\n')

export function piQuestionOptionLine(option: SourceOption, index: number): string {
  return `${index + 1}. ${option.label} — ${option.description}`
}

/**
 * The question records one of Pi's four question tools sends.
 *
 * Each of them states a LIST under `questions`. A tool that asks exactly one states
 * the record at the root instead, so the root is read as a list of one -- which
 * yields nothing when it carries no `question` field either.
 */
function piQuestionRecords(args: Record<string, unknown>): Record<string, unknown>[] {
  const questions = args.questions
  return Array.isArray(questions) ? questions.filter(isObject) : [args]
}

/**
 * The questions a Pi call asked, in the shape the shared body builder reads.
 *
 * Pi's four question tools -- `ask_user_question`, `plan_mode_question`,
 * `goal_question` and `goal_questionnaire` -- spell one question the way
 * {@link piQuestionFromSource} reads it below, so the field names stay in this one
 * module. An option carries a sentence and a worked example beside its label, and the
 * row draws BOTH: the control surface above it does, so a reader who comes back to
 * the row has to be able to tell what the alternatives actually were.
 */
export function piQuestionsFromArgs(args: Record<string, unknown>): QuestionIR[] {
  return questionsFromRecords(
    piQuestionRecords(args),
    (record) => {
      const header = text(pickString(record, 'header'))
      return { ...(header ? { header } : {}), question: text(pickString(record, 'question')) }
    },
    (option) => {
      const label = text(pickString(option, 'label'))
      if (!label)
        return null
      const description = text(pickString(option, 'description'))
      const preview = pickString(option, 'preview', undefined)
      return {
        label,
        ...(description ? { description } : {}),
        ...(preview !== undefined ? { preview } : {}),
      }
    },
  )
}

/**
 * The header words a question row states, or none when the row composes its own.
 *
 * One question IS the header, because the row has room for it and nothing else states
 * it. Several cannot share one line, so the shared renderer states their count, and
 * this gives it nothing to override.
 */
export function piQuestionTitle(questions: QuestionIR[]): string | undefined {
  // One question is the list; the `length === 1` test pins the indexed read.
  return questions.length === 1 ? questions[0]?.question : undefined
}

/** Validate the source against the dialog before adding omitted option previews. */
export function piQuestionFromSource(dialog: Record<string, unknown>, source?: ParsedMessageContent): PiSourceQuestion | undefined {
  const original = source?.parentObject
  if (original?.type !== PI_EVENT.ToolExecutionStart || original.toolName !== PI_TOOL.AskUserQuestion)
    return undefined
  const questions = pickObject(original, 'args')?.questions
  if (!Array.isArray(questions))
    return undefined
  const method = pickString(dialog, 'method')
  const title = pickString(dialog, 'title')
  const placeholder = pickString(dialog, 'placeholder')
  let match: PiSourceQuestion | undefined
  for (const [index, value] of questions.entries()) {
    if (!isObject(value) || typeof value.question !== 'string' || !value.question || !Array.isArray(value.options) || !value.options.length)
      continue
    if (!value.options.every(option => isObject(option) && typeof option.label === 'string' && typeof option.description === 'string'))
      continue
    // `filter(isObject)` drops nothing: the test above already refused a list that
    // holds a non-object. It is what states the element type, in place of an assertion.
    const options: SourceOption[] = value.options.filter(isObject).map((row) => {
      const preview = pickString(row, 'preview', undefined)
      return {
        label: text(pickString(row, 'label')),
        description: text(pickString(row, 'description')),
        ...(preview !== undefined ? { preview } : {}),
      }
    })
    const header = text(pickString(value, 'header'))
    const prompt = text(value.question)
    const question: PiSourceQuestion = { index, prompt, header, title: `${header ? `[${header}] ` : ''}${prompt}`, multiSelect: value.multiSelect === true, options }
    const lines = options.map(piQuestionOptionLine)
    let valid = false
    if (method === PI_DIALOG_METHOD.Select && !question.multiSelect && Array.isArray(dialog.options)) {
      const offered = dialog.options
      valid = offered.length === lines.length + 1
        && lines.every((line, index) => offered[index] === line)
        && typeof offered[lines.length] === 'string'
        && offered[lines.length].startsWith(`${lines.length + 1}. `)
        && (title === question.title || title.startsWith(`${question.title}\n\n--- `))
    }
    else if (method === PI_DIALOG_METHOD.Input) {
      valid = question.multiSelect
        ? placeholder === '1,3' && title.startsWith(`${question.title}\n\n${lines.join('\n')}\n\n`)
        : placeholder === '' && title.startsWith(`${question.title}\n\n`) && !title.startsWith(`${question.title}\n\n--- `)
    }
    if (!valid)
      continue
    if (match)
      return undefined
    match = question
  }
  return match
}
