import type { ControlQuestion } from '../../model/question'
import type { ProviderAskUserQuestion } from '../capabilities'
import { OPENCODE_NATIVE_ANSWER_FIELD, OPENCODE_NATIVE_FORM_FIELD_TYPE } from '~/generated/contracts/opencode-native-protocol'
import { isObject, pickNumber, pickString } from '~/lib/jsonPick'
import { sendJsonRpcResult } from '../../controls/types'
import { openCodeForm } from './form'

/** Native question-tool forms contain only visible string or multiple-choice fields. */
export function openCodeIsQuestionRequest(payload: Record<string, unknown>): boolean {
  const form = openCodeForm(payload)
  return form?.metadata.kind === 'question' && form.fields.every(field =>
    (field.type === OPENCODE_NATIVE_FORM_FIELD_TYPE.String || field.type === OPENCODE_NATIVE_FORM_FIELD_TYPE.Multiselect)
    && field.hidden !== true && field.when === undefined)
}

export function openCodeQuestions(payload: Record<string, unknown>): ControlQuestion[] {
  if (!openCodeIsQuestionRequest(payload))
    return []
  const form = openCodeForm(payload)
  if (!form)
    return []
  return form.fields.map((field) => {
    const header = pickString(field, 'title')
    const minimum = pickNumber(field, 'minItems', undefined)
    const maximum = pickNumber(field, 'maxItems', undefined)
    const options = Array.isArray(field.options)
      ? field.options.flatMap((option) => {
          if (!isObject(option) || typeof option.value !== 'string' || typeof option.label !== 'string')
            return []
          const description = pickString(option, 'description')
          return [
            {
              value: option.value,
              label: option.label,
              ...(description
                ? { description }
                : {}),
            },
          ]
        })
      : []
    return {
      id: pickString(field, 'key'),
      question: pickString(field, 'description') || header,
      ...(header
        ? { header }
        : {}),
      options,
      multiSelect: field.type === OPENCODE_NATIVE_FORM_FIELD_TYPE.Multiselect,
      allowEmpty: field.required !== true,
      allowCustomText: field.custom === true,
      ...(minimum !== undefined
        ? { minimumSelections: minimum }
        : {}),
      ...(maximum !== undefined
        ? { maximumSelections: maximum }
        : {}),
    }
  })
}

export const openCodeQuestionHandling: ProviderAskUserQuestion = {
  isRequest: openCodeIsQuestionRequest,
  extractQuestions: openCodeQuestions,
  sendAnswer: (request, send, questions, state) => {
    const answer = new Map<string, string | string[]>()
    questions.forEach((question, index) => {
      if (question.id === undefined)
        return
      const selected = state.selections()[index] ?? []
      const custom = state.customTexts()[index]
      if (question.multiSelect) {
        answer.set(question.id, selected.length > 0
          ? selected
          : custom
            ? [custom]
            : [])
      }
      else if (selected.length > 0) {
        answer.set(question.id, selected[0] ?? '')
      }
      else if (custom !== undefined && custom !== '') {
        answer.set(question.id, custom)
      }
    })
    return sendJsonRpcResult(send, request.requestId, {
      [OPENCODE_NATIVE_ANSWER_FIELD.Answer]: Object.fromEntries(answer),
    })
  },
  sendReject: (request, send) => sendJsonRpcResult(send, request.requestId, { [OPENCODE_NATIVE_ANSWER_FIELD.Cancel]: true }),
}
