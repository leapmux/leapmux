import type { Accessor } from 'solid-js'
import type { ControlRequestSwitch } from './ControlDecisionFooter'
import type { ActionsProps } from './types'
import type { PermissionMode } from '~/utils/controlResponse'

import { createMemo, createSignal } from 'solid-js'
import { OPTION_ID_PERMISSION_MODE } from '~/components/chat/settingsGroups'
import { computePercentage } from '~/components/chat/widgets/ContextUsageGrid'

export interface PlanApprovalState {
  clearContext: Accessor<boolean>
  setClearContext: (v: boolean) => void
  bypassPermissions: Accessor<boolean>
  setBypassPermissions: (v: boolean) => void
  permissionMode: Accessor<PermissionMode | undefined>
  /**
   * The permission mode this provider's bypass preset selects, or undefined when the
   * preset carries no permission mode at all.
   *
   * The approval travels as ONE control response, so the only part of a preset this
   * banner can apply is the mode it puts in that response. A preset that switches some
   * other axis (Copilot's bypass sets `allow_all`) cannot be applied here, and the
   * switch is not drawn — rather than drawn and silently doing nothing, which is what
   * a bare `sets.permissionMode` read produced once the preset type stopped
   * guaranteeing that key.
   */
  bypassMode: Accessor<PermissionMode | undefined>
  contextPct: Accessor<number | null>
}

/** Creates shared plan approval state (clear context + bypass permissions). */
export function createPlanApprovalState(props: Pick<ActionsProps, 'contextUsage' | 'modelContextWindow' | 'agentProvider' | 'bypass'>): PlanApprovalState {
  const [clearContext, setClearContext] = createSignal(false)
  const [bypassPermissions, setBypassPermissions] = createSignal(false)
  const contextPct = createMemo(() => {
    const pct = computePercentage(props.contextUsage, props.modelContextWindow, props.agentProvider)
    return pct !== null ? Math.round(pct) : null
  })
  const bypassMode = () => props.bypass?.settings.sets[OPTION_ID_PERMISSION_MODE]
  const permissionMode = () => bypassPermissions() ? bypassMode() : undefined

  return { clearContext, setClearContext, bypassPermissions, setBypassPermissions, contextPct, permissionMode, bypassMode }
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
    ...(state.bypassMode()
      ? [{
          id: 'plan-bypass-permissions-checkbox',
          label: 'Bypass Permissions',
          checked: state.bypassPermissions(),
          onChange: state.setBypassPermissions,
        }]
      : []),
  ]
}
