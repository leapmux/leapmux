import type { Accessor, Component } from 'solid-js'
import type { ActionsProps } from './types'
import type { PermissionOption } from '~/components/chat/model/controlPrompt'

import { createMemo } from 'solid-js'
import { isAllowPermissionKind, isRejectPermissionKind } from '~/components/chat/model/controlPrompt'
import { permissionDecision } from './controlDecision'
import { ControlDecisionFooter } from './ControlDecisionFooter'
import { permissionOptionLabel } from './permissionOptionLabels'
import {
  allowScopePillOptions,
  decisionLabel,
  layoutPermissionOptions,
  resolvePermissionOption,
} from './permissionOptions'
import { buildSessionPermissionPill, createSessionPermissionPresetChoice, respondThenApplyPermissionPreset } from './permissionPresets'
import { CONTROL_ALLOW_CHOICE_ID, createControlChoice } from './types'

/**
 * The decision row of a permission prompt: scope pills (Once / Always / Session /
 * Project), the permission pill group (Unchanged / Smart / Bypass), Deny / Allow, and
 * one extra button per option no slot or group consumed.
 *
 * The options arrive in the prompt that the Worker read, and every permission prompt
 * states them: a runtime's own answers, or LeapMux's allow and deny. A choice is sent
 * as a decision that names the option, and the Worker's provider builds the runtime's
 * own answer from it.
 */
export const PermissionDecisionActions: Component<ActionsProps & {
  options: Accessor<PermissionOption[]>
  independentOptions?: boolean
}> = (props) => {
  const layout = createMemo(() => layoutPermissionOptions(props.options()))
  const permissionChoice = createSessionPermissionPresetChoice(props)
  const scopeChoice = createControlChoice(() => props.answerState, CONTROL_ALLOW_CHOICE_ID)

  // The scope pills a payload with always options draws (Once / Always, or
  // Once / Session / Project). The stored selection is clamped to the offered
  // options: a payload swap must not leave the group reporting an option the
  // agent no longer offers.
  const scopeOptions = createMemo(() => {
    const scope = layout().allowScope
    return scope
      ? allowScopePillOptions(scope)
      : undefined
  })
  const selectedScope = () => {
    const scope = layout().allowScope
    if (!scope)
      return undefined
    return scope.some(option => option.optionId === scopeChoice.choice())
      ? scopeChoice.choice()
      // `allowScope` is built as `[once, ...always]`, so a first pill always
      // exists; `?.` is the type-level guard alone.
      : scope[0]?.optionId
  }
  const permissionPill = createMemo(() => buildSessionPermissionPill(props.presets, permissionChoice))

  // The option's response is AWAITED before a permission preset is applied: the
  // worker dispatches the two concurrently, and a mode change the provider cannot
  // take live relaunches the agent, which kills the session before an un-awaited
  // answer reaches it. Only an ALLOW-kind option — the request's positive action
  // family — applies a preset: an extra answer option decides nothing about
  // future permissions.
  const handleOption = async (option: PermissionOption | undefined) => {
    if (!option)
      return
    if (!props.independentOptions && isAllowPermissionKind(option.kind)) {
      await respondThenApplyPermissionPreset(
        props.onDecide(permissionDecision(option.optionId)),
        props.presets,
        permissionChoice.choice(),
      )
      return
    }
    await props.onDecide(permissionDecision(option.optionId))
  }

  const handleDecision = (polarity: 'allow' | 'reject') =>
    handleOption(resolvePermissionOption(layout(), polarity, selectedScope()))

  return (
    <ControlDecisionFooter
      hasEditorContent={props.hasEditorContent}
      onSendFeedback={props.onTriggerSend}
      independentActions={() => props.independentOptions
        ? props.options().map(option => ({
            label: permissionOptionLabel(option),
            testId: `control-decision-${option.optionId}`,
            destructive: isRejectPermissionKind(option.kind),
            onSelect: () => handleOption(option),
          }))
        : []}
      allowChoicePill={() => {
        const options = scopeOptions()
        return !props.independentOptions && layout().positive && options
          ? {
              label: 'Allow scope',
              options,
              selected: selectedScope() ?? options[0].key,
              onSelect: scopeChoice.setChoice,
            }
          : undefined
      }}
      permissionPill={() => !props.independentOptions && layout().positive
        ? permissionPill()
        : undefined}
      // The decision slots are passed only when the layout filled them: an
      // explicit `undefined` is not assignable to an optional prop under
      // exactOptionalPropertyTypes, and the footer reads absent the same.
      {...(!props.independentOptions && layout().negative
        ? {
            negativeAction: {
              // The label follows the selected pill, because the pill decides
              // which refusal Deny sends.
              label: decisionLabel(layout(), 'reject', selectedScope()),
              testId: 'control-deny-btn',
              onSelect: () => handleDecision('reject'),
            },
          }
        : {})}
      {...(!props.independentOptions && layout().positive
        ? {
            positiveAction: {
              label: decisionLabel(layout(), 'allow'),
              testId: 'control-allow-btn',
              onSelect: () => handleDecision('allow'),
            },
          }
        : {})}
      additionalActions={() => props.independentOptions
        ? []
        : layout().additional.map(option => ({
            label: permissionOptionLabel(option),
            testId: `control-decision-${option.optionId}`,
            // Mark a refusal clearly in the menu, apart from a broader approval.
            destructive: isRejectPermissionKind(option.kind),
            onSelect: () => handleOption(option),
          }))}
    />
  )
}
