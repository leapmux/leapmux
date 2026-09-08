import type { Component } from 'solid-js'
import type { WirePermissionOption } from './permissionOptions'
import type { ActionsProps } from './types'

import { createMemo, For, Show } from 'solid-js'
import { ButtonGroup } from '~/components/common/ButtonGroup'
import * as styles from '../ControlRequestBanner.css'
import { actionButtonClass, ControlActionRow } from './ControlActionRow'
import { ControlAllowScopePillGroup, ControlPermissionPillGroup } from './ControlPillGroups'
import {
  ALLOW_SCOPE_CHOICE_ID,
  allowScopePillOptions,
  decisionLabel,
  isAllowPermissionKind,
  isRejectPermissionKind,
  layoutPermissionOptions,
  permissionOptionLabel,
  resolvePermissionOption,
} from './permissionOptions'
import { buildSessionPermissionPill, createSessionPermissionPresetChoice, respondThenApplyPermissionPreset } from './permissionPresets'
import { createControlChoice } from './types'

/** Sends one selected option as the provider's permission reply (ACP- and OpenCode-family envelopes are the same). */
export type SendPermissionOption = (
  onRespond: (content: Uint8Array) => Promise<void>,
  requestId: string,
  optionId: string,
) => Promise<void>

/** Reads a request's wire options; each provider extracts its own payload shape. */
export type PermissionOptionsGetter = (payload: Record<string, unknown>) => WirePermissionOption[]

/**
 * The shared decision row for a wire-options permission request: scope pills
 * (Once / Always / Session / Project), the permission pill group (Unchanged /
 * Smart / Bypass), Deny / Allow, and one extra button per option no slot or
 * group consumed. The ACP and OpenCode families differ only in their payload
 * extraction and sender, so each provider passes its `options` getter and `send`
 * and keeps its wire specifics in its own file.
 */
export const PermissionDecisionActions: Component<ActionsProps & {
  options: PermissionOptionsGetter
  send: SendPermissionOption
}> = (props) => {
  const layout = createMemo(() => layoutPermissionOptions(props.options(props.request.payload)))
  const permissionChoice = createSessionPermissionPresetChoice(props)
  const scopeChoice = createControlChoice(() => props.answerState, ALLOW_SCOPE_CHOICE_ID)

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
  const permissionPill = createMemo(() => buildSessionPermissionPill(props.presets, permissionChoice))

  // The option's response is AWAITED before a permission preset is applied: the
  // worker dispatches the two concurrently, and a mode change the provider cannot
  // take live relaunches the agent, which kills the session before an un-awaited
  // answer reaches it. Only an ALLOW-kind option — the request's positive action
  // family — applies a preset: an extra answer option decides nothing about
  // future permissions.
  const handleOption = async (option: WirePermissionOption | undefined) => {
    if (!option)
      return
    if (isAllowPermissionKind(option.kind)) {
      await respondThenApplyPermissionPreset(
        props.send(props.onRespond, props.request.requestId, option.optionId),
        props.presets,
        permissionChoice.choice(),
      )
      return
    }
    await props.send(props.onRespond, props.request.requestId, option.optionId)
  }

  const handleDecision = (polarity: 'allow' | 'reject') =>
    handleOption(resolvePermissionOption(layout(), polarity, selectedScope()))

  // The pill cluster is drawn only when a positive action exists to apply it:
  // with no allow-kind option, no selection the group offers can ever act, and
  // a drawn control that silently does nothing is the trap this row avoids.
  const pillCluster = () => layout().positive && (scopeOptions() || permissionPill())

  return (
    <ControlActionRow
      leading={(
        <Show when={pillCluster()}>
          <div class={styles.controlRequestSwitches}>
            <Show when={scopeOptions()}>
              {options => (
                <ControlAllowScopePillGroup
                  options={options()}
                  selected={selectedScope() ?? options()[0].key}
                  onSelect={scopeChoice.setChoice}
                />
              )}
            </Show>
            <Show when={permissionPill()}>
              {pill => <ControlPermissionPillGroup pill={pill()} />}
            </Show>
          </div>
        </Show>
      )}
      primary={(
        <>
          <ButtonGroup>
            <Show when={layout().negative}>
              <button
                class={actionButtonClass(true)}
                onClick={() => handleDecision('reject')}
                data-testid="control-deny-btn"
              >
                {decisionLabel(layout(), 'reject')}
              </button>
            </Show>
            <Show when={layout().positive}>
              <button
                class={actionButtonClass()}
                onClick={() => handleDecision('allow')}
                data-testid="control-allow-btn"
              >
                {decisionLabel(layout(), 'allow')}
              </button>
            </Show>
            <For each={layout().additional}>
              {option => (
                <button
                  class={actionButtonClass(isRejectPermissionKind(option.kind))}
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
