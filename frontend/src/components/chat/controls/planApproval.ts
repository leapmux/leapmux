import type { Accessor } from 'solid-js'
import type { ControlRequestSwitch } from './ControlRequestSwitches'
import type { ActionsProps } from './types'
import type { PermissionMode } from '~/utils/controlResponse'

import { createMemo, createSignal } from 'solid-js'
import { computePercentage } from '~/components/chat/widgets/ContextUsageGrid'

export interface PlanApprovalState {
  clearContext: Accessor<boolean>
  setClearContext: (v: boolean) => void
  bypassPermissions: Accessor<boolean>
  setBypassPermissions: (v: boolean) => void
  permissionMode: Accessor<PermissionMode | undefined>
  contextPct: Accessor<number | null>
}

/** Creates shared plan approval state (clear context + bypass permissions). */
export function createPlanApprovalState(props: Pick<ActionsProps, 'contextUsage' | 'modelContextWindow' | 'agentProvider' | 'bypassPermissionMode'>): PlanApprovalState {
  const [clearContext, setClearContext] = createSignal(false)
  const [bypassPermissions, setBypassPermissions] = createSignal(false)
  const contextPct = createMemo(() => {
    const pct = computePercentage(props.contextUsage, props.modelContextWindow, props.agentProvider)
    return pct !== null ? Math.round(pct) : null
  })
  const permissionMode = () => bypassPermissions() ? props.bypassPermissionMode : undefined

  return { clearContext, setClearContext, bypassPermissions, setBypassPermissions, contextPct, permissionMode }
}

/** Builds the shared option list for a plan approval. */
export function planApprovalSwitches(state: PlanApprovalState, bypassPermissionMode?: PermissionMode): ControlRequestSwitch[] {
  return [
    {
      id: 'plan-clear-context-checkbox',
      label: 'Clear Context',
      checked: state.clearContext(),
      onChange: state.setClearContext,
      suffix: state.contextPct() !== null ? ` (${state.contextPct()}%)` : undefined,
    },
    ...(bypassPermissionMode
      ? [{
          id: 'plan-bypass-permissions-checkbox',
          label: 'Bypass Permissions',
          checked: state.bypassPermissions(),
          onChange: state.setBypassPermissions,
        }]
      : []),
  ]
}
