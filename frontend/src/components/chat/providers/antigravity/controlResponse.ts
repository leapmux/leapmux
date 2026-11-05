import type { ControlAnswerState, ControlResponseSender } from '../../controls/types'
import type { ControlResponseSummary } from '../../model/controlResponse'
import type { ControlQuestion } from '../../model/question'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import type { ControlRequest } from '~/stores/control.store'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { CONTROL_DECISION_WORDS, controlBehaviorDisplay, joinAnswerLines, label, labeledAnswerLine, labelOrNull } from '../../persistedControlResponse'
import { antigravityExtractQuestions } from './askUserQuestion'

interface InteractionAddress {
  cascadeId: string
  trajectoryId: string
  stepIndex: number
}

export function antigravityInteractionAddress(requestId: string): InteractionAddress {
  const parts = requestId.split(':')
  if (parts.length !== 4 || parts[0] !== 'antigravity')
    throw new Error('The Antigravity request ID is malformed.')
  return {
    cascadeId: parts[1]!,
    trajectoryId: parts[2]!,
    stepIndex: Number(parts[3]),
  }
}

export function antigravityPermissionResponse(requestId: string, optionId: string, feedback = ''): Record<string, unknown> {
  const address = antigravityInteractionAddress(requestId)
  return {
    cascadeId: address.cascadeId,
    interaction: {
      trajectoryId: address.trajectoryId,
      stepIndex: address.stepIndex,
      permission: {
        allow: optionId === 'allow',
        scope: 'PERMISSION_SCOPE_ONCE',
        ...(feedback
          ? { userDenyInstruction: feedback }
          : {}),
      },
    },
  }
}

export async function sendAntigravityPermissionResponse(onRespond: ControlResponseSender, requestId: string, optionId: string, feedback = ''): Promise<void> {
  await onRespond(new TextEncoder().encode(JSON.stringify(antigravityPermissionResponse(requestId, optionId, feedback))))
}

export async function sendAntigravityQuestionResponse(request: ControlRequest, onRespond: ControlResponseSender, questions: ControlQuestion[], answerState: ControlAnswerState): Promise<void> {
  const payload = request.payload as Record<string, unknown>
  const address = antigravityInteractionAddress(request.requestId)
  const source = pickObject(pickObject(payload, 'requestedInteraction'), 'askQuestion')?.questions
  const wireQuestions = Array.isArray(source)
    ? source
    : []
  const responses = questions.map((question, index) => {
    const wire = isObject(wireQuestions[index])
      ? wireQuestions[index]
      : {}
    const selectedOptionIds = answerState.selections()[index] ?? []
    const writeInResponse = answerState.customTexts()[index] ?? ''
    return {
      ...wire,
      ...(selectedOptionIds.length > 0
        ? { selectedOptionIds }
        : {}),
      ...(writeInResponse
        ? { writeInResponse }
        : {}),
      ...(selectedOptionIds.length === 0 && !writeInResponse
        ? { skipped: true }
        : {}),
    }
  })
  await onRespond(new TextEncoder().encode(JSON.stringify({
    cascadeId: address.cascadeId,
    interaction: {
      trajectoryId: address.trajectoryId,
      stepIndex: address.stepIndex,
      askQuestion: { responses },
    },
  })))
}

export async function sendAntigravityQuestionReject(request: ControlRequest, onRespond: ControlResponseSender): Promise<void> {
  const address = antigravityInteractionAddress(request.requestId)
  await onRespond(new TextEncoder().encode(JSON.stringify({
    cascadeId: address.cascadeId,
    interaction: {
      trajectoryId: address.trajectoryId,
      stepIndex: address.stepIndex,
      askQuestion: { cancelled: true },
    },
  })))
}

export function antigravityControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  const behavior = controlBehaviorDisplay(cr.response)
  if (behavior)
    return behavior
  const interaction = pickObject(cr.response, 'interaction')
  const permission = pickObject(interaction, 'permission')
  if (permission) {
    return label(permission.allow === true
      ? CONTROL_DECISION_WORDS.permission.allow
      : CONTROL_DECISION_WORDS.permission.deny)
  }
  const ask = pickObject(interaction, 'askQuestion')
  if (!ask)
    return null
  if (ask.cancelled === true)
    return label('Dismissed')
  const responses = Array.isArray(ask.responses)
    ? ask.responses
    : []
  const questions = antigravityExtractQuestions(cr.request ?? {})
  const lines: string[] = []
  responses.forEach((answer, index) => {
    if (!isObject(answer))
      return
    const selected = Array.isArray(answer.selectedOptionIds)
      ? answer.selectedOptionIds.filter((id): id is string => typeof id === 'string')
      : []
    const labels = selected.map(id => questions[index]?.options.find(option => option.value === id)?.label ?? id)
    const text = pickString(answer, 'writeInResponse') || labels.join(', ') || (answer.skipped === true
      ? 'Skipped'
      : '')
    const line = labeledAnswerLine(questions[index]?.question ?? `Question ${index + 1}`, text)
    if (line !== null)
      lines.push(line)
  })
  return labelOrNull(joinAnswerLines(lines))
}
