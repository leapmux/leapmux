import type { Accessor, Component } from 'solid-js'
import type { WirePermissionOption } from './permissionOptionLabels'
import type { ActionsProps, ControlResponseSender } from './types'

import { createMemo } from 'solid-js'
import { ControlDecisionFooter } from './ControlDecisionFooter'
import { isAllowPermissionKind, isRejectPermissionKind, permissionOptionLabel } from './permissionOptionLabels'
import {
  allowScopePillOptions,
  decisionLabel,
  layoutPermissionOptions,
  resolvePermissionOption,
} from './permissionOptions'
import { buildSessionPermissionPill, createSessionPermissionPresetChoice, respondThenApplyPermissionPreset } from './permissionPresets'
import { CONTROL_ALLOW_CHOICE_ID, createControlChoice } from './types'

/** Sends ONE chosen option as this provider's permission reply, in its own envelope. */
export type SendPermissionOption = (
  onRespond: ControlResponseSender,
  requestId: string,
  optionId: string,
) => Promise<void>

/**
 * The shared decision row for a permission request whose runtime states its own
 * answers: scope pills (Once / Always / Session / Project), the permission pill
 * group (Unchanged / Smart / Bypass), Deny / Allow, and one extra button per option
 * no slot or group consumed.
 *
 * The options arrive from the control IR, which each provider's `extractControl`
 * filled -- so no wire shape is read here. The provider states `send` beside them,
 * because the envelope that carries one id back is its own.
 */
export const PermissionDecisionActions: Component<ActionsProps & {
  options: Accessor<WirePermissionOption[]>
  send: SendPermissionOption
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
    return scope ? allowScopePillOptions(scope) : undefined
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

  return (
    <ControlDecisionFooter
      hasEditorContent={props.hasEditorContent}
      onSendFeedback={props.onTriggerSend}
      allowChoicePill={() => {
        const options = scopeOptions()
        return layout().positive && options
          ? { label: 'Allow scope', options, selected: selectedScope() ?? options[0].key, onSelect: scopeChoice.setChoice }
          : undefined
      }}
      permissionPill={() => layout().positive ? permissionPill() : undefined}
      // The decision slots are passed only when the layout filled them: an
      // explicit `undefined` is not assignable to an optional prop under
      // exactOptionalPropertyTypes, and the footer reads absent the same.
      {...(layout().negative
        ? { negativeAction: { label: decisionLabel(layout(), 'reject'), testId: 'control-deny-btn', onSelect: () => handleDecision('reject') } }
        : {})}
      {...(layout().positive
        ? { positiveAction: { label: decisionLabel(layout(), 'allow'), testId: 'control-allow-btn', onSelect: () => handleDecision('allow') } }
        : {})}
      additionalActions={() => layout().additional.map(option => ({
        label: permissionOptionLabel(option),
        testId: `control-decision-${option.optionId}`,
        // A refusal reads as a refusal wherever it lands. "Reject always" and
        // "Allow for this workspace" are both extras, and an undifferentiated
        // menu made them look like the same kind of answer.
        destructive: isRejectPermissionKind(option.kind),
        onSelect: () => handleOption(option),
      }))}
    />
  )
}
