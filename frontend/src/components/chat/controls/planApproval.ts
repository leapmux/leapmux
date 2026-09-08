import type { Accessor } from 'solid-js'
import type { ControlRequestSwitch } from './ControlDecisionFooter'
import type { ControlPermissionPill } from './permissionPresets'
import type { ActionsProps } from './types'
import type { PermissionMode } from '~/utils/controlResponse'

import { createMemo } from 'solid-js'
import { computePercentage } from '~/components/chat/widgets/ContextUsageGrid'
import { buildPermissionPill, createPermissionPresetChoice, PLAN_APPROVAL_PERMISSION_CHOICE, planApprovalPresets, presetPermissionMode } from './permissionPresets'
import { createControlSwitch } from './types'

export interface PlanApprovalState {
  clearContext: Accessor<boolean>
  setClearContext: (v: boolean) => void
  /** The permission pill group this banner draws, or undefined when no applicable preset exists. */
  permissionPill: Accessor<ControlPermissionPill | undefined>
  permissionMode: Accessor<PermissionMode | undefined>
  contextPct: Accessor<number | null>
}

/** Creates shared plan approval state (clear context + permission preset choice). */
export function createPlanApprovalState(props: Pick<ActionsProps, 'contextUsage' | 'modelContextWindow' | 'agentProvider' | 'presets' | 'answerState'>): PlanApprovalState {
  // The switch lives in the composer's shared answer record, keyed by the id
  // that `planApprovalSwitches` below renders it under; the permission pill
  // choice is stored the same way under its own group id. See
  // `createControlSwitch` / `createPermissionPresetChoice`.
  const clear = createControlSwitch(() => props.answerState, 'plan-clear-context-checkbox')
  const permissionChoice = createPermissionPresetChoice(props, () => PLAN_APPROVAL_PERMISSION_CHOICE)
  const { checked: clearContext, set: setClearContext } = clear
  const contextPct = createMemo(() => {
    const pct = computePercentage(props.contextUsage, props.modelContextWindow, props.agentProvider)
    return pct !== null ? Math.round(pct) : null
  })
  // A plan approval carries only the preset's permission MODE (see
  // `planApprovalPresets`), so both the pill it draws and the mode it embeds
  // read from that filtered view of the controller.
  const presets = () => planApprovalPresets(props.presets)
  const permissionMode = () => presetPermissionMode(presets(), permissionChoice.choice())
  const permissionPill = () => buildPermissionPill(presets(), permissionChoice)

  return { clearContext, setClearContext, permissionPill, contextPct, permissionMode }
}

/** Builds the shared option list for a plan approval. */
export function planApprovalSwitches(state: PlanApprovalState): ControlRequestSwitch[] {
  return [
    {
      id: 'plan-clear-context-checkbox',
      label: 'Clear Context',
      checked: state.clearContext(),
      onChange: state.setClearContext,
      suffix: state.contextPct() !== null ? ` (${state.contextPct()}%)` : undefined,
    },
  ]
}
