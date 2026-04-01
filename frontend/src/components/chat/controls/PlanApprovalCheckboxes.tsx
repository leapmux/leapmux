import type { Accessor, Component } from 'solid-js'
import type { ActionsProps } from './types'
import type { PermissionMode } from '~/utils/controlResponse'

import { createSignal, Show } from 'solid-js'
import { computePercentage } from '~/components/chat/ContextUsageGrid'
import * as styles from '../ControlRequestBanner.css'

export interface PlanApprovalState {
  clearContext: Accessor<boolean>
  permissionMode: Accessor<PermissionMode | undefined>
}

/** Creates shared plan approval state (clear context + bypass permissions). */
export function createPlanApprovalState(props: Pick<ActionsProps, 'contextUsage' | 'modelContextWindow' | 'agentProvider' | 'bypassPermissionMode'>): PlanApprovalState & { contextPct: Accessor<number | null>, bypassPermissions: Accessor<boolean>, setClearContext: (v: boolean) => void, setBypassPermissions: (v: boolean) => void } {
  const [clearContext, setClearContext] = createSignal(false)
  const [bypassPermissions, setBypassPermissions] = createSignal(false)
  const contextPct = () => {
    const pct = computePercentage(props.contextUsage, props.modelContextWindow, props.agentProvider)
    return pct !== null ? Math.round(pct) : null
  }
  const permissionMode = () => bypassPermissions() ? props.bypassPermissionMode : undefined

  return { clearContext, setClearContext, bypassPermissions, setBypassPermissions, contextPct, permissionMode }
}

/** Shared checkboxes for plan approval (Clear Context + Bypass Permissions). */
export const PlanApprovalCheckboxes: Component<{
  state: ReturnType<typeof createPlanApprovalState>
  bypassPermissionMode?: PermissionMode
}> = (props) => {
  return (
    <div class={styles.planApprovalCheckboxes}>
      <label data-testid="plan-clear-context-checkbox">
        <input
          type="checkbox"
          role="switch"
          checked={props.state.clearContext()}
          onChange={e => props.state.setClearContext(e.currentTarget.checked)}
        />
        Clear Context
        <Show when={props.state.contextPct() !== null}>
          {' '}
          (
          {props.state.contextPct()}
          %)
        </Show>
      </label>
      <Show when={props.bypassPermissionMode}>
        <label data-testid="plan-bypass-permissions-checkbox">
          <input
            type="checkbox"
            role="switch"
            checked={props.state.bypassPermissions()}
            onChange={e => props.state.setBypassPermissions(e.currentTarget.checked)}
          />
          Bypass Permissions
        </label>
      </Show>
    </div>
  )
}
