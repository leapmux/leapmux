import type { ControlResponseSummary } from '../../model/controlResponse'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { OPENCODE_NATIVE_ANSWER_FIELD, OPENCODE_NATIVE_PERMISSION_DECISION } from '~/generated/contracts/opencode-native-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { joinAnswerLines, label, labeledAnswerLine } from '../../persistedControlResponse'
import { openCodeForm } from './form'

/** Describe the resolved control answer that the Worker stores for OpenCode. */
export function openCodeControlResponseSummary(record: PersistedControlResponse): ControlResponseSummary | null {
  const response = record.response
  if (!response)
    return null
  const decision = pickString(response, OPENCODE_NATIVE_ANSWER_FIELD.Decision)
  switch (decision) {
    case OPENCODE_NATIVE_PERMISSION_DECISION.Once:
      return label('Allowed')
    case OPENCODE_NATIVE_PERMISSION_DECISION.Always:
      return label('Always allowed')
    case OPENCODE_NATIVE_PERMISSION_DECISION.Reject:
      return label('Denied')
  }
  if (response[OPENCODE_NATIVE_ANSWER_FIELD.Cancel] === true)
    return label('Cancelled')
  const answers = response[OPENCODE_NATIVE_ANSWER_FIELD.Answer]
  if (!isObject(answers))
    return null
  const fields = record.request
    ? openCodeForm(record.request)?.fields ?? []
    : []
  const lines = Object.entries(answers).flatMap(([key, answer]) => {
    const field = fields.find(field => field.key === key)
    const values = Array.isArray(answer) && answer.length > 0
      ? answer
      : [answer]
    const textValues = values.map(value => typeof value === 'string' && value.trim() !== ''
      ? value
      : JSON.stringify(value))
    const line = labeledAnswerLine(pickString(field, 'title') || key, textValues)
    return line === null
      ? []
      : [line]
  })
  const text = joinAnswerLines(lines)
  return text
    ? label(text)
    : label('Answered')
}
