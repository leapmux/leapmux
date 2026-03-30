import type { Component } from 'solid-js'
import type { ActionsProps, AskQuestionState, ContentProps, Question } from './types'

import { Match, Show, Switch } from 'solid-js'
import { ButtonGroup } from '~/components/common/ButtonGroup'
import { buildAllowResponse, buildDenyResponse } from '~/utils/controlResponse'
import * as styles from '../ControlRequestBanner.css'
import { AskUserQuestionActions, AskUserQuestionContent } from './AskUserQuestionControl'
import { toRpcId } from './CodexControlRequest'
import { sendResponse } from './types'

function getCursorParams(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  return payload.params as Record<string, unknown> | undefined
}

function isCursorAskQuestionPayload(payload: Record<string, unknown>): boolean {
  return payload.method === 'cursor/ask_question'
}

function isCursorCreatePlanPayload(payload: Record<string, unknown>): boolean {
  return payload.method === 'cursor/create_plan'
}

export function getCursorQuestions(payload: Record<string, unknown>): Question[] {
  const params = getCursorParams(payload)
  const rawQuestions = (params?.questions as Array<Record<string, unknown>> | undefined) ?? []
  return rawQuestions.map(question => ({
    id: question.id as string | undefined,
    question: (question.prompt as string | undefined) ?? '',
    header: (question.prompt as string | undefined) ?? (question.id as string | undefined),
    multiSelect: (question.allowMultiple as boolean | undefined) ?? false,
    options: ((question.options as Array<Record<string, unknown>> | undefined) ?? []).map(option => ({
      id: option.id as string | undefined,
      label: (option.label as string | undefined) ?? (option.id as string | undefined) ?? '',
    })),
  }))
}

function wrapAsAskUserQuestion(payload: Record<string, unknown>): Record<string, unknown> {
  return {
    ...payload,
    request: {
      tool_name: 'AskUserQuestion',
      input: { questions: getCursorQuestions(payload) },
    },
  }
}

export function sendCursorQuestionResponse(
  agentId: string,
  onRespond: (agentId: string, content: Uint8Array) => Promise<void>,
  requestId: string,
  questions: Question[],
  askState: AskQuestionState,
): Promise<void> {
  const answers = questions.map((question, index) => {
    const selected = askState.selections()[index] ?? []
    const selectedOptionIds = selected.map((selectedLabel) => {
      const match = question.options.find(option => option.label === selectedLabel)
      return match?.id || selectedLabel
    })
    return {
      questionId: question.id || `q${index}`,
      selectedOptionIds,
    }
  }).filter(answer => answer.selectedOptionIds.length > 0)

  return sendResponse(agentId, onRespond, {
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
  agentId: string,
  onRespond: (agentId: string, content: Uint8Array) => Promise<void>,
  requestId: string,
  reason?: string,
): Promise<void> {
  return sendResponse(agentId, onRespond, {
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
      <Match when={isCursorAskQuestionPayload(props.request.payload)}>
        <AskUserQuestionContent
          request={{ ...props.request, payload: wrapAsAskUserQuestion(props.request.payload) }}
          askState={props.askState}
          optionsDisabled={props.optionsDisabled}
        />
      </Match>
      <Match when={isCursorCreatePlanPayload(props.request.payload)}>
        <div class={styles.controlBannerTitle}>
          {planName() ? `Create Plan: ${planName()}` : 'Create Plan'}
        </div>
        <Show when={overview()}>
          <div class={styles.codexReason}>{overview()}</div>
        </Show>
      </Match>
    </Switch>
  )
}

export const CursorControlActions: Component<ActionsProps> = (props) => {
  const questions = () => getCursorQuestions(props.request.payload)

  const createPlanAllow = () => sendResponse(
    props.request.agentId,
    props.onRespond,
    buildAllowResponse(props.request.requestId, {}),
  )

  const createPlanReject = () => sendResponse(
    props.request.agentId,
    props.onRespond,
    buildDenyResponse(props.request.requestId, 'Rejected by user.'),
  )

  const userInputOnRespond = async (_agentId: string, content: Uint8Array) => {
    const parsed = JSON.parse(new TextDecoder().decode(content))
    if (parsed?.response?.response?.behavior === 'deny') {
      await sendCursorQuestionRejectResponse(props.request.agentId, props.onRespond, props.request.requestId, parsed?.response?.response?.message as string | undefined)
      return
    }
    await sendCursorQuestionResponse(props.request.agentId, props.onRespond, props.request.requestId, questions(), props.askState)
  }

  return (
    <Switch
      fallback={(
        <div class={styles.controlFooter}>
          <div class={styles.controlFooterLeft}>
            {props.infoTrigger}
          </div>
          <div class={styles.controlFooterRight}>
            <ButtonGroup>
              <button class="outline" onClick={createPlanReject} data-testid="control-deny-btn">Reject</button>
              <button onClick={createPlanAllow} data-testid="control-allow-btn">Allow</button>
            </ButtonGroup>
          </div>
        </div>
      )}
    >
      <Match when={isCursorAskQuestionPayload(props.request.payload)}>
        <AskUserQuestionActions
          {...props}
          request={{ ...props.request, payload: wrapAsAskUserQuestion(props.request.payload) }}
          onRespond={userInputOnRespond}
        />
      </Match>
    </Switch>
  )
}
