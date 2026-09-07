import type { Component } from 'solid-js'
import type { WirePermissionOption } from '../../controls/permissionOptions'
import type { ActionsProps, ContentProps, ControlAnswerState, Question } from '../../controls/types'

import { createMemo, For, Show } from 'solid-js'
import { ButtonGroup } from '~/components/common/ButtonGroup'
import * as styles from '../../ControlRequestBanner.css'
import { ControlActionRow } from '../../controls/ControlActionRow'
import { ControlAllowScopePillGroup, ControlPermissionPillGroup } from '../../controls/ControlDecisionFooter'
import {
  ALLOW_SCOPE_CHOICE_ID,
  allowScopePillOptions,
  isRejectPermissionKind,
  layoutPermissionOptions,
  permissionOptionLabel,
  resolvePermissionOption,
  scopeRemembers,
} from '../../controls/permissionOptions'
import { applyPermissionPreset, buildPermissionPill, createPermissionPresetChoice } from '../../controls/permissionPresets'
import { createControlChoice, sendResponse, toRpcId } from '../../controls/types'

/** Extract OpenCode requestPermission params from the control request payload. */
function getOpenCodeParams(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  return payload.params as Record<string, unknown> | undefined
}

/** Extract the tool call info from a requestPermission payload. */
function getToolCall(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  const params = getOpenCodeParams(payload)
  return params?.toolCall as Record<string, unknown> | undefined
}

/** Extract permission options from a requestPermission payload. */
function getOptions(payload: Record<string, unknown>): WirePermissionOption[] {
  const params = getOpenCodeParams(payload)
  return (params?.options as WirePermissionOption[] | undefined) ?? []
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
  return sendResponse(onRespond, {
    jsonrpc: '2.0',
    id: toRpcId(requestId),
    result: { outcome: { outcome: 'selected', optionId } },
  })
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
export const OpenCodeControlActions: Component<ActionsProps> = (props) => {
  const layout = createMemo(() => layoutPermissionOptions(getOptions(props.request.payload)))
  const permissionChoice = createPermissionPresetChoice(props)
  const scopeChoice = createControlChoice(() => props.answerState, ALLOW_SCOPE_CHOICE_ID, '')

  // The scope pills a payload with always options draws (Once / Always, or
  // Once / Session / Project). The stored selection is clamped to the offered
  // options: a payload swap must not leave the group reporting an option the
  // agent no longer offers.
  const scopeOptions = createMemo(() => {
    const scope = layout().allowScope
    return scope ? allowScopePillOptions(scope) : undefined
  })
  const selectedScope = () => {
    const scope = layout().allowScope
    if (!scope)
      return undefined
    return scope.some(option => option.optionId === scopeChoice.choice())
      ? scopeChoice.choice()
      : scope[0].optionId
  }

  // The option's response is AWAITED before a permission preset is applied: the
  // worker dispatches the two concurrently, and a mode change the provider cannot
  // take live relaunches the agent, killing the session before an un-awaited
  // answer reaches it. A reject-kind option decides nothing about future
  // permissions, so it applies no preset.
  const handleOption = async (option: WirePermissionOption | undefined) => {
    if (!option)
      return
    await sendOpenCodePermissionResponse(props.onRespond, props.request.requestId, option.optionId)
    if (!isRejectPermissionKind(option.kind))
      await applyPermissionPreset(props.presets, permissionChoice.choice())
  }

  const handleDecision = (polarity: 'allow' | 'reject') =>
    handleOption(resolvePermissionOption(layout(), polarity, scopeRemembers(layout(), selectedScope()), selectedScope()))

  return (
    <ControlActionRow
      primary={(
        <>
          <Show when={scopeOptions() || buildPermissionPill(props.presets, permissionChoice)}>
            <div class={styles.controlRequestSwitches}>
              <Show when={scopeOptions()}>
                {options => (
                  <ControlAllowScopePillGroup
                    options={options()}
                    selected={selectedScope()!}
                    onSelect={scopeChoice.setChoice}
                  />
                )}
              </Show>
              <Show when={buildPermissionPill(props.presets, permissionChoice)}>
                {pill => <ControlPermissionPillGroup pill={pill()} />}
              </Show>
            </div>
          </Show>
          <Show
            when={layout().positive || layout().negative}
            fallback={(
              <ButtonGroup>
                <button class="outline" onClick={() => handleOption({ optionId: 'reject', kind: 'reject_once', name: 'Deny' })} data-testid="control-deny-btn">Deny</button>
                <button onClick={() => handleOption({ optionId: 'once', kind: 'allow_once', name: 'Allow' })} data-testid="control-allow-btn">Allow</button>
              </ButtonGroup>
            )}
          >
            <ButtonGroup>
              <Show when={layout().negative}>
                <button
                  class="outline"
                  onClick={() => handleDecision('reject')}
                  data-testid="control-deny-btn"
                >
                  Deny
                </button>
              </Show>
              <Show when={layout().positive || scopeOptions()}>
                <button
                  onClick={() => handleDecision('allow')}
                  data-testid="control-allow-btn"
                >
                  Allow
                </button>
              </Show>
              <For each={layout().additional}>
                {option => (
                  <button
                    class={isRejectPermissionKind(option.kind) ? 'outline' : undefined}
                    onClick={() => handleOption(option)}
                    data-testid={`control-decision-${option.optionId}`}
                  >
                    {permissionOptionLabel(option)}
                  </button>
                )}
              </For>
            </ButtonGroup>
          </Show>
        </>
      )}
    />
  )
}
