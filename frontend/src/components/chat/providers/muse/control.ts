import type { ControlAnswerState } from '../../controls/types'
import type { PermissionOption, PermissionPrompt, PermissionScope } from '../../model/controlPrompt'
import type { ControlQuestion } from '../../model/question'
import type { ProviderAskUserQuestion, ProviderControlCapability } from '../capabilities'
import { MUSE_APPROVAL_DECISION, MUSE_CHOICE_SCOPE, MUSE_METHOD, MUSE_QUESTION_SELECTION_MODE } from '~/generated/contracts/muse-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { buildJsonRpcResult, sendJsonRpcResult } from '../../controls/types'
import { KIND_ALLOW_ALWAYS, KIND_ALLOW_ONCE, KIND_REJECT_ALWAYS, KIND_REJECT_ONCE } from '../../model/controlPrompt'
import { museParams } from './protocol'

export function museQuestions(payload: Record<string, unknown>): ControlQuestion[] {
  const params = museParams(payload)
  if (!Array.isArray(params?.questions))
    return []
  const questions: ControlQuestion[] = []
  const ids = new Set<string>()
  for (const question of params.questions) {
    if (!isObject(question))
      return []
    const id = pickString(question, 'id')
    const text = pickString(question, 'question')
    const selection = pickObject(question, 'selection')
    if (!id.trim() || ids.has(id) || !text.trim() || typeof question.header !== 'string' || !Array.isArray(question.options)
      || (selection?.mode !== MUSE_QUESTION_SELECTION_MODE.Single && selection?.mode !== MUSE_QUESTION_SELECTION_MODE.Multiple)) {
      return []
    }
    ids.add(id)
    const options: ControlQuestion['options'] = []
    const labels = new Set<string>()
    for (const option of question.options) {
      if (!isObject(option))
        return []
      const label = pickString(option, 'label')
      if (!label.trim() || labels.has(label))
        return []
      labels.add(label)
      const description = pickString(option, 'description')
      const preview = pickString(pickObject(option, 'preview'), 'content')
      options.push({ label, value: label, ...(description ? { description } : {}), ...(preview ? { preview } : {}) })
    }
    const minimum = selection.minSelections === undefined ? 0 : selection.minSelections
    const maximum = selection.maxSelections === undefined ? options.length : selection.maxSelections
    if (typeof minimum !== 'number' || !Number.isSafeInteger(minimum) || minimum < 0
      || typeof maximum !== 'number' || !Number.isSafeInteger(maximum) || maximum < minimum || maximum > options.length) {
      return []
    }
    questions.push({ id, header: question.header, question: text, options, multiSelect: selection.mode === MUSE_QUESTION_SELECTION_MODE.Multiple })
  }
  return questions
}

export function museAnswers(questions: ControlQuestion[], state: ControlAnswerState): Record<string, unknown>[] {
  return questions.map((question, index) => {
    const selected = state.selections()[index] ?? []
    const typed = state.customTexts()[index] ?? ''
    const result: Record<string, unknown> = { questionId: question.id }
    if (selected.length) {
      if (question.multiSelect)
        result.selectedLabels = selected
      else
        result.selectedLabel = selected[0]
      if (typed)
        result.note = typed
    }
    else {
      result.freeText = typed
    }
    return result
  })
}

export const museAskUserQuestion: ProviderAskUserQuestion = {
  isRequest: payload => (payload.method === MUSE_METHOD.UserInputRequest || payload.method === MUSE_METHOD.UserInputRequested) && museQuestions(payload).length > 0,
  extractQuestions: museQuestions,
  sendAnswer: (request, sender, questions, state) => sendJsonRpcResult(sender, request.requestId, { answers: museAnswers(questions, state) }),
  sendReject: (request, sender, reason) => sendJsonRpcResult(sender, request.requestId, { cancelled: true, reason }),
}

const APPROVAL_METHODS: ReadonlySet<string> = new Set([MUSE_METHOD.ApprovalRequest, MUSE_METHOD.ApprovalRequested, MUSE_METHOD.ApprovalUpdated])
const ALLOW_DECISIONS: ReadonlySet<string> = new Set([MUSE_APPROVAL_DECISION.Approved, MUSE_APPROVAL_DECISION.ApprovedForSession, MUSE_APPROVAL_DECISION.ApprovedPolicyAmendment])
const REJECT_DECISIONS: ReadonlySet<string> = new Set([MUSE_APPROVAL_DECISION.Denied, MUSE_APPROVAL_DECISION.DeniedPolicyAmendment, MUSE_APPROVAL_DECISION.TimedOut, MUSE_APPROVAL_DECISION.Abort])
const CHOICE_SCOPES: ReadonlySet<string> = new Set(Object.values(MUSE_CHOICE_SCOPE))

interface MuseApprovalChoice {
  choiceId: string
  label: string
  decision: string
  scope: string
  acceptsFeedback: boolean
  known: boolean
}

/** Validate the complete native choice list before display or reply selection. */
function museApprovalChoices(value: unknown): MuseApprovalChoice[] | null {
  if (!Array.isArray(value) || value.length === 0)
    return null
  const choices: MuseApprovalChoice[] = []
  const ids = new Set<string>()
  for (const choice of value) {
    if (!isObject(choice))
      return null
    const optionId = pickString(choice, 'choiceId')
    const name = pickString(choice, 'label')
    const decision = pickString(choice, 'decision')
    if (!optionId.trim() || ids.has(optionId) || !name.trim() || !decision.trim() || typeof choice.scope !== 'string' || !choice.scope.trim())
      return null
    ids.add(optionId)
    if (choice.acceptsFeedback !== undefined && typeof choice.acceptsFeedback !== 'boolean')
      return null
    const known = (ALLOW_DECISIONS.has(decision) || REJECT_DECISIONS.has(decision))
      && CHOICE_SCOPES.has(choice.scope)
    choices.push({ choiceId: optionId, label: name, decision, scope: choice.scope, acceptsFeedback: choice.acceptsFeedback === true, known })
  }
  return choices
}

export const museControl: ProviderControlCapability = {
  askUserQuestion: museAskUserQuestion,
  preservesSelectionNotes: true,
  controlToolSpanId: payload => pickString(museParams(payload), 'itemId'),
  sendPermissionOption: (sender, id, choiceId) => sendJsonRpcResult(sender, id, { choiceId }),
  extractControl: ({ payload }) => {
    if (typeof payload.method !== 'string' || !APPROVAL_METHODS.has(payload.method))
      return null
    const params = museParams(payload)
    if (!params)
      return null
    const choices = museApprovalChoices(params.availableChoices)
    if (!choices || choices.some(choice => !choice.known))
      return null
    const options: PermissionOption[] = choices.map((choice) => {
      let scope: PermissionScope | undefined
      if (choice.scope === MUSE_CHOICE_SCOPE.Session)
        scope = 'session'
      else if (choice.scope === MUSE_CHOICE_SCOPE.LocalPersistent)
        scope = 'workspace'
      const persistent = scope !== undefined
      const allow = ALLOW_DECISIONS.has(choice.decision)
      const kind = allow ? persistent ? KIND_ALLOW_ALWAYS : KIND_ALLOW_ONCE : persistent ? KIND_REJECT_ALWAYS : KIND_REJECT_ONCE
      return { optionId: choice.choiceId, name: choice.label, kind, ...(scope ? { scope } : {}) }
    })
    const subject = pickObject(params, 'subject')
    return { kind: 'permission', permission: { title: pickString(params, 'toolName') || pickString(subject, 'toolName'), command: pickString(subject, 'command'), options } satisfies PermissionPrompt }
  },
  buildControlResponse: (payload, feedback, requestId) => {
    const choices = museApprovalChoices(museParams(payload)?.availableChoices)
    if (!choices)
      throw new Error('The native Muse approval choices are invalid.')
    const choice = choices.find(choice => choice.scope === MUSE_CHOICE_SCOPE.Once
      && (choice.decision === MUSE_APPROVAL_DECISION.Denied || choice.decision === MUSE_APPROVAL_DECISION.Abort)
      && (feedback === '' || choice.acceptsFeedback))
    if (!choice)
      throw new Error('Muse supplies no denial choice that accepts this feedback.')
    return buildJsonRpcResult(requestId, { choiceId: choice.choiceId, ...(feedback ? { feedback } : {}) })
  },
}
