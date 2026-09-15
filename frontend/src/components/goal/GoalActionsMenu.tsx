import type { Component } from 'solid-js'
import type { GoalAction, GoalSurface } from '~/stores/chatGoal'
import { createMemo, For, Show } from 'solid-js'
import { DisabledReasonMenuItem } from '~/components/common/DisabledReasonMenuItem'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { moreHorizontalTrigger } from '~/components/common/moreHorizontalTrigger'
import { goalActionState } from '~/stores/chatGoal'
import { dangerMenuItem } from '~/styles/shared.css'

export interface GoalActionsMenuProps {
  /** Keep the goal, its capabilities, and its handler together to prevent mismatched actions. */
  goal: GoalSurface
}

/** Display actions in this order. Separate the destructive Clear action and use its warning color. */
const ACTIONS: { action: GoalAction, label: string, danger?: boolean }[] = [
  { action: 'pause', label: 'Pause' },
  { action: 'resume', label: 'Resume' },
  { action: 'set', label: 'Replace goal…' },
  { action: 'clear', label: 'Clear goal', danger: true },
]

/**
 * Keep goal actions in a menu that fits the 360px Goals & To-dos popover.
 * Hide unsupported actions. Explain why a supported action is temporarily unavailable.
 * Clear removes the goal without a confirmation. It does not delete provider artifacts.
 */
export const GoalActionsMenu: Component<GoalActionsMenuProps> = (props) => {
  // Filter the stable action objects so For preserves each retained row and its tooltip as state changes.
  const offered = createMemo(() =>
    ACTIONS.filter(({ action }) =>
      goalActionState(props.goal, action).kind !== 'hidden'),
  )

  // Show a trigger only when a handler and at least one supported action exist.
  return (
    <Show when={props.goal.onAction && offered().length > 0}>
      <DropdownMenu
        trigger={moreHorizontalTrigger({
          'title': 'Goal actions',
          'data-testid': 'goal-actions-trigger',
        })}
        aria-label="Goal actions"
        data-testid="goal-actions-menu"
      >
        <For each={offered()}>
          {(item, index) => {
            // Read capabilities reactively so the disabled state and its reason stay consistent after a process restart.
            const reason = () => {
              const state = goalActionState(props.goal, item.action)
              return state.kind === 'disabled' ? state.reason : undefined
            }
            return (
              <>
                {/* Separate the destructive action when another action precedes it. */}
                <Show when={item.danger && index() > 0}>
                  <hr />
                </Show>
                <DisabledReasonMenuItem
                  reason={reason()}
                  class={item.danger ? dangerMenuItem : undefined}
                  data-testid={`goal-action-${item.action}`}
                  onClick={() => props.goal.onAction?.(item.action)}
                >
                  {item.label}
                </DisabledReasonMenuItem>
              </>
            )
          }}
        </For>
      </DropdownMenu>
    </Show>
  )
}
