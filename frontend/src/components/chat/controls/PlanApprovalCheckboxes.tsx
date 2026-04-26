import type { Accessor, Component } from 'solid-js'
import type { ActionsProps } from './types'
import type { PermissionMode } from '~/utils/controlResponse'

import { createMemo, createSignal, Show } from 'solid-js'
import { computePercentage } from '~/components/chat/widgets/ContextUsageGrid'
import { CompactSwitch } from '~/components/common/CompactSwitch'
import * as styles from '../ControlRequestBanner.css'

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

/** Shared checkboxes for plan approval (Clear Context + Bypass Permissions). */
export const PlanApprovalCheckboxes: Component<{
  state: PlanApprovalState
  bypassPermissionMode?: PermissionMode
}> = (props) => {
  return (
    <div class={styles.planApprovalCheckboxes}>
      <CompactSwitch
        checked={props.state.clearContext()}
        onChange={props.state.setClearContext}
        data-testid="plan-clear-context-checkbox"
        fontSize="var(--text-8)"
      >
        Clear Context
        <Show when={props.state.contextPct() !== null}>
          {' '}
          (
          {props.state.contextPct()}
          %)
        </Show>
      </CompactSwitch>
      <Show when={props.bypassPermissionMode}>
        <CompactSwitch
          checked={props.state.bypassPermissions()}
          onChange={props.state.setBypassPermissions}
          data-testid="plan-bypass-permissions-checkbox"
          fontSize="var(--text-8)"
        >
          Bypass Permissions
        </CompactSwitch>
      </Show>
    </div>
  )
}
