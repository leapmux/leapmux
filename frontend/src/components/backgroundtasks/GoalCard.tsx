import type { Component } from 'solid-js'
import type { GoalSurface, SessionGoal } from '~/stores/chatGoal'
import { createMemo, Show } from 'solid-js'
import { formatSecondsParts } from '~/components/chat/rendererUtils'
import { StatusDot } from '~/components/common/StatusDot'
import { goalActionState, goalStatusLabel } from '~/stores/chatGoal'
import { srOnly } from '~/styles/shared.css'
import * as taskStyles from './BackgroundTaskList.css'
import { GoalActionsMenu } from './GoalActionsMenu'
import * as styles from './GoalCard.css'
import { GoalObjective } from './GoalObjective'

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
 *
 * The objective and the verbs each live in their own component --
 * `./GoalObjective` and `./GoalActionsMenu` -- because each owns a rule this
 * card must not restate: the clamp and its two routes back, and the three-way
 * hidden / enabled / refused state of every verb.
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

  /** Whether the empty state may offer its call to action. */
  const canSetFirstGoal = () =>
    props.goal.onAction !== undefined
    && goalActionState(props.goal.current, props.goal.actions, 'set').kind === 'enabled'

  return (
    <div class={styles.card} data-testid="goal-card">
      <div class={styles.headerRow}>
        {/* The section header above this card is the user-renameable
            `section.name`, so it may say anything at all -- the card cannot
            borrow it to say what it is. */}
        <div class={styles.heading}>Session goal</div>
        {/* No menu in the empty state. `set` is the only verb that applies with
            no goal, and the empty state offers it as its own call to action --
            a first goal must not be one click deeper than the concept it
            introduces. */}
        <Show when={props.goal.current && props.goal.onAction}>
          <GoalActionsMenu
            goal={props.goal}
            onAction={action => props.goal.onAction?.(action)}
          />
        </Show>
      </div>
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
            <span>No session goal.</span>
            <Show when={canSetFirstGoal()}>
              <button
                type="button"
                class="small outline"
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
            <GoalObjective objective={goal().objective} />
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
          </>
        )}
      </Show>
    </div>
  )
}
