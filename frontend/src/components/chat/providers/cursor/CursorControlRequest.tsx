import type { Component } from 'solid-js'
import type { ActionsProps, ContentProps, ControlAnswerState, ControlResponseSender, Question } from '../../controls/types'

import { Match, Show, Switch } from 'solid-js'
import { CURSOR_METHOD } from '~/generated/contracts/cursor-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { buildAllowResponse, buildDenyResponse } from '~/utils/controlResponse'
import * as styles from '../../ControlRequestBanner.css'
import { ControlDecisionFooter } from '../../controls/ControlDecisionFooter'
import { sendResponse } from '../../controls/types'

function getCursorParams(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  return pickObject(payload, 'params', undefined)
}

export function isCursorAskQuestionPayload(payload: Record<string, unknown>): boolean {
  return payload.method === CURSOR_METHOD.AskQuestion
}

export function isCursorCreatePlanPayload(payload: Record<string, unknown>): boolean {
  return payload.method === CURSOR_METHOD.CreatePlan
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
  onRespond: ControlResponseSender,
  requestId: string,
  questions: Question[],
  answerState: ControlAnswerState,
): Promise<void> {
  // Cursor's own answer carries `freeformText` beside `selectedOptionIds`, and its own
  // interface reads both, so a typed answer travels rather than being dropped. A question
  // answered with typed text ALONE is still an answer, which is why the survival test
  // below reads either half. See RL-006.
  const answers = questions.map((question, index) => {
    const selected = answerState.selections()[index] ?? []
    const typed = answerState.customTexts()[index]?.trim() ?? ''
    return {
      questionId: question.id || `q${index}`,
      selectedOptionIds: selected,
      ...(typed ? { freeformText: typed } : {}),
    }
  }).filter(answer => answer.selectedOptionIds.length > 0 || answer.freeformText !== undefined)

  return sendResponse(onRespond, {
    jsonrpc: '2.0',
    id: requestId,
    result: {
      outcome: {
        outcome: 'answered',
        answers,
      },
    },
  })
}

export function sendCursorQuestionRejectResponse(
  onRespond: ControlResponseSender,
  requestId: string,
  reason?: string,
): Promise<void> {
  return sendResponse(onRespond, {
    jsonrpc: '2.0',
    id: requestId,
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
  const planName = () => pickString(params(), 'name')
  const overview = () => pickString(params(), 'overview')

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
    <ControlDecisionFooter
      hasEditorContent={props.hasEditorContent}
      onSendFeedback={props.onTriggerSend}
      negativeAction={{ label: 'Reject', testId: 'control-deny-btn', onSelect: createPlanReject }}
      positiveAction={{ label: 'Approve', testId: 'control-allow-btn', onSelect: createPlanAllow }}
    />
  )
}
