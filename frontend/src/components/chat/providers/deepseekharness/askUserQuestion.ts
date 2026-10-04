import type { ControlAnswerState } from '../../controls/types'
import type { ControlQuestion } from '../../model/question'
import { DEEPSEEK_HARNESS_CONTROL_EVENT } from '~/generated/contracts/deepseek-harness-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { buildControlResponseEnvelope } from '~/utils/controlResponse'

export function deepseekHarnessQuestionRecords(payload: Record<string, unknown>): Record<string, unknown>[] {
  const request = pickObject(payload, 'request') ?? payload
  return Array.isArray(request.questions) ? request.questions.filter(isObject) : []
}

export function deepseekHarnessIsQuestionRequest(payload: Record<string, unknown>): boolean {
  return payload.event === DEEPSEEK_HARNESS_CONTROL_EVENT.UserQuestions
    && !deepseekHarnessQuestionRecords(payload).some(record => pickObject(record, 'intent')?.kind === 'plan-review')
}

export function deepseekHarnessQuestions(payload: Record<string, unknown>): ControlQuestion[] {
  return deepseekHarnessQuestionRecords(payload).flatMap((record) => {
    const id = pickString(record, 'id')
    const question = pickString(record, 'question')
    if (!id || !question)
      return []
    const header = pickString(record, 'header')
    const options = Array.isArray(record.options) ? record.options.filter(isObject) : []
    return [{
      id,
      question,
      options: options.flatMap((option) => {
        const label = pickString(option, 'label')
        const description = pickString(option, 'description')
        return label ? [{ label, value: label, ...(description ? { description } : {}) }] : []
      }),
      ...(header ? { header } : {}),
      ...(record.multiSelect === true ? { multiSelect: true } : {}),
    }]
  })
}

export function buildDeepseekHarnessAnswers(requestId: string, questions: ControlQuestion[], state: ControlAnswerState): Record<string, unknown> {
  const answers = questions.flatMap((question, index) => {
    if (!question.id)
      return []
    const offered = new Set(question.options.map(option => option.value ?? option.label))
    const selected = (state.selections()[index] ?? []).filter(value => offered.has(value))
    const custom = (state.customTexts()[index] ?? '').trim()
    return [{ id: question.id, selected: question.multiSelect ? selected : selected.slice(0, 1), ...(custom ? { custom } : {}) }]
  })
  return buildControlResponseEnvelope(requestId, { behavior: 'allow', answers })
}
