import type { Component } from 'solid-js'
import type { WirePermissionOption } from '../../controls/permissionOptions'
import type { ActionsProps, ContentProps, ControlAnswerState, Question } from '../../controls/types'

import { Show } from 'solid-js'
import * as styles from '../../ControlRequestBanner.css'
import { PermissionDecisionActions } from '../../controls/PermissionDecisionActions'
import { sendResponse, sendSelectedOptionResponse, toRpcId } from '../../controls/types'

/** Extract OpenCode requestPermission params from the control request payload. */
function getOpenCodeParams(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  return payload.params as Record<string, unknown> | undefined
}

/** Extract the tool call info from a requestPermission payload. */
function getToolCall(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  const params = getOpenCodeParams(payload)
  return params?.toolCall as Record<string, unknown> | undefined
}

/**
 * The pair OpenCode itself answers with, for a payload that carries no options.
 * These are the daemon's real option ids — it maps an unknown id to reject, so a
 * synthesized pair with invented ids would turn every Allow into a reject.
 */
const DEFAULT_OPTIONS: readonly WirePermissionOption[] = [
  { optionId: 'once', kind: 'allow_once', name: 'Allow' },
  { optionId: 'reject', kind: 'reject_once', name: 'Deny' },
]

/** Extract permission options from a requestPermission payload. */
function getOptions(payload: Record<string, unknown>): WirePermissionOption[] {
  const params = getOpenCodeParams(payload)
  const options = params?.options as WirePermissionOption[] | undefined
  return options && options.length > 0 ? options : [...DEFAULT_OPTIONS]
}

function getQuestionProperties(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  return payload.properties as Record<string, unknown> | undefined
}

export function isOpenCodeQuestionPayload(payload: Record<string, unknown>): boolean {
  return payload.type === 'question.asked' && Array.isArray(getQuestionProperties(payload)?.questions)
}

/**
 * Read the `properties.questions` array off an OpenCode-style `question.asked`
 * payload and normalize the legacy `multiple` field to `multiSelect`. Used by
 * both the OpenCode and Kilo plugins (Kilo is an OpenCode fork that shares
 * the same wire format).
 */
export function extractOpenCodeQuestions(payload: Record<string, unknown>): Question[] {
  const properties = getQuestionProperties(payload)
  const rawQuestions = (properties?.questions as Array<Record<string, unknown>> | undefined) ?? []
  return rawQuestions.map(question => ({
    ...question,
    multiSelect: (question.multiSelect as boolean | undefined) ?? (question.multiple as boolean | undefined),
  })) as Question[]
}

/**
 * Sends an OpenCode permission response as a JSON-RPC response.
 */
export function sendOpenCodePermissionResponse(
  onRespond: (content: Uint8Array) => Promise<void>,
  requestId: string,
  optionId: string,
): Promise<void> {
  return sendSelectedOptionResponse(onRespond, requestId, optionId)
}

export function sendOpenCodeQuestionResponse(
  onRespond: (content: Uint8Array) => Promise<void>,
  requestId: string,
  questions: Question[],
  answerState: ControlAnswerState,
): Promise<void> {
  const answers: string[][] = questions.map((_, index) => {
    const selected = answerState.selections()[index] ?? []
    const customText = answerState.customTexts()[index]?.trim()
    if (selected.length > 0)
      return selected
    if (customText)
      return [customText]
    return []
  })
  return sendResponse(onRespond, {
    jsonrpc: '2.0',
    id: toRpcId(requestId),
    result: { answers },
  })
}

export function sendOpenCodeQuestionRejectResponse(
  onRespond: (content: Uint8Array) => Promise<void>,
  requestId: string,
): Promise<void> {
  return sendResponse(onRespond, {
    jsonrpc: '2.0',
    id: toRpcId(requestId),
    result: { rejected: true },
  })
}

/** OpenCode-specific control request content. */
export const OpenCodeControlContent: Component<ContentProps> = (props) => {
  const toolCall = () => getToolCall(props.request.payload)
  const title = () => (toolCall()?.title as string) || 'Permission Request'
  const kind = () => toolCall()?.kind as string | undefined

  return (
    <>
      <div class={styles.controlBannerTitle}>{title()}</div>
      <Show when={kind()}>
        <div class={styles.bannerHint}>{kind()}</div>
      </Show>
    </>
  )
}

/** OpenCode-specific control request action buttons. */
export const OpenCodeControlActions: Component<ActionsProps> = props => (
  <PermissionDecisionActions {...props} options={getOptions} send={sendOpenCodePermissionResponse} />
)
