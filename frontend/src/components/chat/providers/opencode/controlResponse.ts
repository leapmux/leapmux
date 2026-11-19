import type { ControlPrompt } from '../../model/controlPrompt'
import type { ControlResponseSummary } from '../../model/controlResponse'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { OPENCODE_NATIVE_ANSWER_FIELD, OPENCODE_NATIVE_PERMISSION_DECISION } from '~/generated/contracts/opencode-native-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { joinAnswerLines, label, labeledAnswerLine } from '../../persistedControlResponse'

/**
 * The title of one form field, as the prompt that the Worker recorded with the answer
 * states it: the header of the question whose id is the field's key, or the title of
 * the field's property in the form's schema. Empty when the prompt states neither.
 */
function fieldTitle(prompt: ControlPrompt | undefined, key: string): string {
  if (prompt?.kind === 'question')
    return prompt.questions.find(question => question.id === key)?.header ?? ''
  if (prompt?.kind === 'elicitation') {
    const schema = prompt.elicitation.schema
    const properties = isObject(schema)
      ? pickObject(schema, 'properties')
      : null
    return pickString(pickObject(properties, key), 'title')
  }
  return ''
}

/**
 * Describe the resolved control answer that the Worker stores for OpenCode: a native
 * permission decision, a cancelled form, or the answer of each form field under the
 * field's title, which the recorded prompt states.
 */
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
  const lines = Object.entries(answers).flatMap(([key, answer]) => {
    const values = Array.isArray(answer) && answer.length > 0
      ? answer
      : [answer]
    const textValues = values.map(value => typeof value === 'string' && value.trim() !== ''
      ? value
      : JSON.stringify(value))
    const line = labeledAnswerLine(fieldTitle(record.prompt, key) || key, textValues)
    return line === null
      ? []
      : [line]
  })
  const text = joinAnswerLines(lines)
  return text
    ? label(text)
    : label('Answered')
}
