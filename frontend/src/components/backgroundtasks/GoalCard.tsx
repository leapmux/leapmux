import type { Component } from 'solid-js'
import type { GoalAction, GoalSurface, SessionGoal } from '~/stores/chatGoal'
import { createMemo, For, Show } from 'solid-js'
import { formatSecondsParts } from '~/components/chat/rendererUtils'
import { StatusDot } from '~/components/common/StatusDot'
import { Tooltip } from '~/components/common/Tooltip'
import { goalActionState, goalStatusLabel } from '~/stores/chatGoal'
import { srOnly } from '~/styles/shared.css'
import * as taskStyles from './BackgroundTaskList.css'
import * as styles from './GoalCard.css'

export interface GoalCardProps {
  /** The goal, its counters, the live actions and their handler. */
  goal: GoalSurface
  /**
   * Whether THIS card owns the live region that announces a status change.
   *
   * Two cards can be on screen at once: the sidebar section and an open
   * ThinkingIndicator popover render the same panel. A live region in each
   * announces one goal change twice, so exactly one instance sets this.
   */
  announce?: boolean
}

/** The verb buttons, in the order they read. */
const ACTIONS: { action: GoalAction, label: string }[] = [
  { action: 'pause', label: 'Pause' },
  { action: 'resume', label: 'Resume' },
  { action: 'set', label: 'Replace' },
  { action: 'clear', label: 'Clear' },
]

function statusDotClass(goal: SessionGoal): string {
  switch (goal.status) {
    // statusDotActive carries the pulse keyframe, which is what marks a goal
    // still being worked on.
    case 'active':
      return taskStyles.statusDotActive
    case 'paused':
      return taskStyles.statusDotPending
    case 'done':
      return taskStyles.statusDotSuccess
    case 'blocked':
      return taskStyles.statusDotDanger
    // A dormant goal is WAITING, not failing: no live process pursues it, so
    // the muted dot says "nothing is happening here" without the alarm a
    // danger dot raises.
    case 'dormant':
      return taskStyles.statusDotMuted
  }
}

/**
 * GoalCard shows the session goal -- the standing objective the agent keeps
 * working toward until a per-turn check says the condition holds.
 *
 * There is at most one per agent, so this is a card and not a list.
 *
 * It renders the reported elapsed time and NEVER runs a timer. Two reasons, and
 * the second is the decisive one: `ToolRunningBadge` already made this call for
 * the same hazard ("there is deliberately no timer here"), and Codex's
 * `timeUsedSeconds` is BUDGET CONSUMED rather than wall clock -- so ticking it
 * would assert the agent is spending while it waits on an approval, and the
 * number would jump backwards when the real value lands.
 */
export const GoalCard: Component<GoalCardProps> = (props) => {
  // One string, rebuilt only when a field it reads changes, so the live region
  // holds ONE stable text node. A `<Show>` that swapped nodes would make a
  // screen reader re-announce on every rebuild.
  const announcement = createMemo(() => {
    const goal = props.goal.current
    if (!goal)
      return 'No session goal'
    const detail = goal.statusDetail ? `, ${goal.statusDetail}` : ''
    return `Session goal ${goalStatusLabel(goal.status).toLowerCase()}${detail}: ${goal.objective}`
  })

  // Only the counters the provider actually reported. An absent counter is left
  // out rather than shown as zero: no two providers report the same set, and a
  // "0 tokens" row would state a number nobody gave.
  const metaParts = createMemo(() => {
    const p = props.goal.progress
    const parts: string[] = []
    if (p.tokensUsed !== undefined) {
      parts.push(p.tokenBudget !== undefined && p.tokenBudget > 0
        ? `${p.tokensUsed.toLocaleString()} / ${p.tokenBudget.toLocaleString()} tokens`
        : `${p.tokensUsed.toLocaleString()} tokens`)
    }
    if (p.timeUsedSeconds !== undefined)
      parts.push(formatSecondsParts(p.timeUsedSeconds))
    if (p.iterations !== undefined)
      parts.push(`${p.iterations} ${p.iterations === 1 ? 'turn' : 'turns'}`)
    return parts
  })

  // Only the actions this agent offers at all. A gap in the PROVIDER is
  // permanent -- Claude Code has no pause or resume, Reasonix can report a goal
  // but never change one -- so its button would never light up, and a row of
  // dead controls says less than no row. A supported action that the current
  // goal state refuses still renders, disabled with its reason, because that one
  // comes back.
  // FILTER, never map. `<For>` reconciles rows by REFERENCE, so returning fresh
  // objects would tear down and rebuild every button whenever the goal's status
  // moved -- losing the tooltip under the pointer and the focus a screen-reader
  // user had on a disabled control, at the exact moment the goal changed. The
  // module-level ACTIONS entries are stable, so a row survives while it stays
  // offered.
  const offeredActions = createMemo(() =>
    ACTIONS.filter(({ action }) =>
      goalActionState(props.goal.current, props.goal.actions, action).kind !== 'hidden'),
  )

  return (
    <div class={styles.card} data-testid="goal-card">
      <div class={styles.heading}>Session goal</div>
      {/* Offscreen rather than hidden: `display: none` and `visibility: hidden`
          both take a live region out of the accessibility tree, so nothing is
          announced. `srOnly` is the shared spelling of that.

          Always mounted with changing text -- see `announcement`.

          ONLY when `announce` is set. Up to two cards can be on screen at once
          (the sidebar section and an open ThinkingIndicator popover render the
          same panel), and a live region in each announces one goal change
          twice. The sidebar owns the announcement; the popover renders the same
          card silently. */}
      <Show when={props.announce}>
        <div class={srOnly} role="status" aria-live="polite">{announcement()}</div>
      </Show>
      <Show
        when={props.goal.current}
        fallback={(
          <div class={styles.empty} data-testid="goal-card-empty">
            <span>No session goal</span>
            <Show when={props.goal.onAction && goalActionState(props.goal.current, props.goal.actions, 'set').kind === 'enabled'}>
              <button
                type="button"
                class={styles.action}
                data-testid="goal-action-set"
                onClick={() => props.goal.onAction?.('set')}
              >
                Set a goal
              </button>
            </Show>
          </div>
        )}
      >
        {goal => (
          <>
            <div class={styles.objective} data-testid="goal-objective">{goal().objective}</div>
            <div class={styles.statusRow}>
              <StatusDot
                class={statusDotClass(goal())}
                label={goalStatusLabel(goal().status)}
                tooltip
                status={goal().status}
                testId="goal-status-dot"
              />
              <span>{goalStatusLabel(goal().status)}</span>
              <Show when={goal().statusDetail}>
                {detail => (
                  <span data-testid="goal-status-detail">
                    (
                    {detail()}
                    )
                  </span>
                )}
              </Show>
            </div>
            <Show when={metaParts().length > 0}>
              <div class={styles.meta} data-testid="goal-progress">{metaParts().join(' · ')}</div>
            </Show>
            <Show when={props.goal.onAction && offeredActions().length > 0}>
              <div class={styles.actions}>
                <For each={offeredActions()}>
                  {({ action, label }) => {
                    // Re-asked per render, so a capability that changes when the
                    // process restarts updates the control and its tooltip
                    // together -- one question, so the two can never disagree.
                    const reason = () => {
                      const state = goalActionState(props.goal.current, props.goal.actions, action)
                      return state.kind === 'disabled' ? state.reason : undefined
                    }
                    return (
                      <Show
                        when={reason()}
                        fallback={(
                          <button
                            type="button"
                            class={styles.action}
                            data-testid={`goal-action-${action}`}
                            onClick={() => props.goal.onAction?.(action)}
                          >
                            {label}
                          </button>
                        )}
                      >
                        {why => (
                          // A real `disabled`, wrapped in Tooltip so the reason
                          // is reachable: a disabled control takes no focus, so
                          // the offscreen description Tooltip leaves behind is
                          // the only route to it for a screen-reader user.
                          //
                          // No `ariaLabel`: the button has visible text, and
                          // ariaLabel would REPLACE "Pause" with the reason
                          // sentence -- which breaks every by-role lookup and
                          // announces a sentence where a verb belongs.
                          <Tooltip text={why()}>
                            <button
                              type="button"
                              class={styles.action}
                              data-testid={`goal-action-${action}`}
                              disabled
                            >
                              {label}
                            </button>
                          </Tooltip>
                        )}
                      </Show>
                    )
                  }}
                </For>
              </div>
            </Show>
          </>
        )}
      </Show>
    </div>
  )
}
