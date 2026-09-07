import type { Component } from 'solid-js'
import type { GoalAction, GoalSurface } from '~/stores/chatGoal'
import { createMemo, For, Show } from 'solid-js'
import { DisabledReasonMenuItem } from '~/components/common/DisabledReasonMenuItem'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { moreHorizontalTrigger } from '~/components/common/moreHorizontalTrigger'
import { goalActionState } from '~/stores/chatGoal'
import { dangerMenuItem } from '~/styles/shared.css'

export interface GoalActionsMenuProps {
  /** The goal and what the running agent can do with it. */
  goal: GoalSurface
  /** Perform one action. */
  onAction: (action: GoalAction) => void
}

/**
 * The verbs, in the order they read.
 *
 * `danger` marks the one that destroys the goal, which the menu draws in the
 * danger colour and separates from the rest.
 */
const ACTIONS: { action: GoalAction, label: string, danger?: boolean }[] = [
  { action: 'pause', label: 'Pause' },
  { action: 'resume', label: 'Resume' },
  { action: 'set', label: 'Replace goal…' },
  { action: 'clear', label: 'Clear goal', danger: true },
]

/**
 * The session goal's verbs, behind the card's `...` trigger.
 *
 * A menu rather than a row of buttons. Pause and Resume are opposites, so at
 * most one of them applies at any moment, and neither applies to a goal that is
 * achieved, blocked or dormant -- a row therefore showed one live control
 * beside two or three dead ones, and it wrapped inside the 360px popover the
 * same panel renders in. A menu costs no width, holds each refused verb with
 * its reason, and lets Clear read as the destructive action it is.
 *
 * Clearing asks for no confirmation. It destroys no artifact, and reopening
 * Replace prefilled with the objective is a better undo than a confirm step.
 */
export const GoalActionsMenu: Component<GoalActionsMenuProps> = (props) => {
  // FILTER, never map. `<For>` reconciles rows by REFERENCE, so returning fresh
  // objects would tear down and rebuild every item whenever the goal's status
  // moved -- losing the tooltip under the pointer and the focus a screen-reader
  // user had on a disabled item, at the exact moment the goal changed. The
  // module-level ACTIONS entries are stable, so an item survives while it stays
  // offered.
  //
  // A gap in the PROVIDER is permanent -- Claude Code has no pause or resume,
  // Reasonix can report a goal but never change one -- so its item is absent
  // rather than dead. A verb the current goal state refuses comes back, so it
  // holds its place and says why.
  const offered = createMemo(() =>
    ACTIONS.filter(({ action }) =>
      goalActionState(props.goal.current, props.goal.actions, action).kind !== 'hidden'),
  )

  return (
    <Show when={offered().length > 0}>
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
            // Re-asked per render, so a capability that changes when the
            // process restarts updates the item and its reason together -- one
            // question, so the two can never disagree.
            const reason = () => {
              const state = goalActionState(props.goal.current, props.goal.actions, item.action)
              return state.kind === 'disabled' ? state.reason : undefined
            }
            return (
              <>
                {/* A rule above the destructive verb, and only when something
                    stands above it. */}
                <Show when={item.danger && index() > 0}>
                  <hr />
                </Show>
                <DisabledReasonMenuItem
                  reason={reason()}
                  class={item.danger ? dangerMenuItem : undefined}
                  data-testid={`goal-action-${item.action}`}
                  onClick={() => props.onAction(item.action)}
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
