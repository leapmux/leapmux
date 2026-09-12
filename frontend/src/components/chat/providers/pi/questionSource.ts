import type { ParsedMessageContent } from '~/lib/messageParser'
import { PI_DIALOG_METHOD, PI_EVENT, PI_TOOL } from '~/generated/contracts/pi-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

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
    const options: SourceOption[] = value.options.map((option) => {
      const row = option as Record<string, unknown>
      return { label: text(pickString(row, 'label')), description: text(pickString(row, 'description')), preview: pickString(row, 'preview', undefined) }
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
