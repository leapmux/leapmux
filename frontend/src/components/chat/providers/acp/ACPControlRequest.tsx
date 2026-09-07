import type { Component } from 'solid-js'
import type { WirePermissionOption } from '../../controls/permissionOptions'
import type { ActionsProps, ContentProps } from '../../controls/types'

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

function getACPParams(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  return payload.params as Record<string, unknown> | undefined
}

function getToolCall(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  return getACPParams(payload)?.toolCall as Record<string, unknown> | undefined
}

function getOptions(payload: Record<string, unknown>): WirePermissionOption[] {
  return (getACPParams(payload)?.options as WirePermissionOption[] | undefined) ?? []
}

export function sendACPPermissionResponse(
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

export const ACPControlContent: Component<ContentProps> = (props) => {
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

export const ACPControlActions: Component<ActionsProps> = (props) => {
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
  // take live relaunches the agent, which kills the session before an un-awaited
  // answer reaches it. A reject-kind option decides nothing about future
  // permissions, so it applies no preset.
  const handleOption = async (option: WirePermissionOption | undefined) => {
    if (!option)
      return
    await sendACPPermissionResponse(props.onRespond, props.request.requestId, option.optionId)
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
        </>
      )}
    />
  )
}
