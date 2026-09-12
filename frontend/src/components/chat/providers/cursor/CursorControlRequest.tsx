import type { Component } from 'solid-js'
import type { ActionsProps, ContentProps, ControlAnswerState, Question } from '../../controls/types'

import { Match, Show, Switch } from 'solid-js'
import { ButtonGroup } from '~/components/common/ButtonGroup'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { buildAllowResponse, buildDenyResponse } from '~/utils/controlResponse'
import * as styles from '../../ControlRequestBanner.css'
import { actionButtonClass, ControlActionRow } from '../../controls/ControlActionRow'
import { sendResponse, toRpcId } from '../../controls/types'

function getCursorParams(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  return pickObject(payload, 'params', undefined)
}

export function isCursorAskQuestionPayload(payload: Record<string, unknown>): boolean {
  return payload.method === 'cursor/ask_question'
}

export function isCursorCreatePlanPayload(payload: Record<string, unknown>): boolean {
  return payload.method === 'cursor/create_plan'
}

export function isCursorControlPayload(payload: Record<string, unknown>): boolean {
  return isCursorAskQuestionPayload(payload) || isCursorCreatePlanPayload(payload)
}

export function getCursorQuestions(payload: Record<string, unknown>): Question[] {
  const params = getCursorParams(payload)
  if (!Array.isArray(params?.questions))
    return []
  return params.questions.filter(isObject).map(question => ({
    id: pickString(question, 'id', undefined),
    question: pickString(question, 'prompt'),
    header: pickString(question, 'prompt') || pickString(question, 'id', undefined),
    multiSelect: question.allowMultiple === true,
    options: (Array.isArray(question.options) ? question.options : []).filter(isObject).flatMap((option) => {
      const label = pickString(option, 'label') || pickString(option, 'id')
      return label ? [{ value: pickString(option, 'id') || label, label }] : []
    }),
  }))
}

export function sendCursorQuestionResponse(
  onRespond: (content: Uint8Array) => Promise<void>,
  requestId: string,
  questions: Question[],
  answerState: ControlAnswerState,
): Promise<void> {
  const answers = questions.map((question, index) => {
    const selected = answerState.selections()[index] ?? []
    return {
      questionId: question.id || `q${index}`,
      selectedOptionIds: selected,
    }
  }).filter(answer => answer.selectedOptionIds.length > 0)

  return sendResponse(onRespond, {
    jsonrpc: '2.0',
    id: toRpcId(requestId),
    result: {
      outcome: {
        outcome: 'answered',
        answers,
      },
    },
  })
}

export function sendCursorQuestionRejectResponse(
  onRespond: (content: Uint8Array) => Promise<void>,
  requestId: string,
  reason?: string,
): Promise<void> {
  return sendResponse(onRespond, {
    jsonrpc: '2.0',
    id: toRpcId(requestId),
    result: {
      outcome: {
        outcome: 'cancelled',
        ...(reason ? { reason } : {}),
      },
    },
  })
}

export const CursorControlContent: Component<ContentProps> = (props) => {
  const params = () => getCursorParams(props.request.payload)
  const planName = () => params()?.name as string | undefined
  const overview = () => params()?.overview as string | undefined

  return (
    <Switch
      fallback={<div class={styles.controlBannerTitle}>Cursor Request</div>}
    >
      <Match when={isCursorCreatePlanPayload(props.request.payload)}>
        <div class={styles.controlBannerTitle}>
          {planName() ? `Create Plan: ${planName()}` : 'Create Plan'}
        </div>
        <Show when={overview()}>
          <div class={styles.bannerReason}>{overview()}</div>
        </Show>
      </Match>
    </Switch>
  )
}

export const CursorControlActions: Component<ActionsProps> = (props) => {
  const createPlanAllow = () => sendResponse(props.onRespond, buildAllowResponse(props.request.requestId, {}))

  const createPlanReject = () => sendResponse(props.onRespond,
    // Bare deny (no typed reason): buildDenyResponse fills the shared
    // CONTROL_REJECTED_BY_USER_MESSAGE placeholder, so don't re-spell the literal here.
    buildDenyResponse(props.request.requestId))

  return (
    <ControlActionRow
      primary={(
        <ButtonGroup>
          <button class={actionButtonClass(true)} onClick={createPlanReject} data-testid="control-deny-btn">Reject</button>
          <button class={actionButtonClass()} onClick={createPlanAllow} data-testid="control-allow-btn">Allow</button>
        </ButtonGroup>
      )}
    />
  )
}
