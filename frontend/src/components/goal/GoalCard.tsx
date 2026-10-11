import type { Component } from 'solid-js'
import type { GoalSurface, SessionGoal } from '~/stores/chatGoal'
import { createMemo, Show } from 'solid-js'
import { formatSecondsParts } from '~/components/chat/rendererUtils'
import { StatusDot } from '~/components/common/StatusDot'
import { markdownToPlainText } from '~/lib/markdownPlainText'
import { goalActionState, goalStatusLabel } from '~/stores/chatGoal'
import { srOnly } from '~/styles/shared.css'
import * as statusDotStyles from '~/styles/statusDot.css'
import { GoalActionsMenu } from './GoalActionsMenu'
import * as styles from './GoalCard.css'
import { GoalObjective } from './GoalObjective'

export interface GoalCardProps {
  /** The complete goal surface. */
  goal: GoalSurface
  /**
   * Whether this card owns the live region for goal changes.
   * The sidebar and an open ThinkingIndicator popover can display the same goal.
   * The sidebar supplies one announcement. The popover displays the card silently.
   */
  announce?: boolean
}

function statusDotClass(goal: SessionGoal): string {
  switch (goal.status) {
    // The active palette supplies the pulse animation.
    case 'active':
      return statusDotStyles.statusDotActive
    case 'paused':
      return statusDotStyles.statusDotPending
    case 'done':
      return statusDotStyles.statusDotSuccess
    case 'blocked':
      return statusDotStyles.statusDotDanger
    // A dormant goal has no live process.
    // An unknown goal has no recognized active or blocked status.
    // Both use the existing muted palette without a pulse or danger color.
    case 'dormant':
    case 'unknown':
      return statusDotStyles.statusDotMuted
  }
}

/**
 * Display one agent's session goal and its reported counters.
 *
 * Render the reported time without a timer.
 * A local timer could increase the counter while the provider waits for approval.
 * The next provider report could then reduce that displayed value.
 *
 * GoalObjective owns the objective clamp and its expansion controls.
 * GoalActionsMenu owns the state of each action.
 * The card forwards the complete surface to that menu.
 */
export const GoalCard: Component<GoalCardProps> = (props) => {
  // Keep one stable text node in the live region.
  // Replace its text when a reported field changes.
  // Swapping nodes could cause an additional screen-reader announcement.
  const announcement = createMemo(() => {
    const goal = props.goal.current
    if (!goal)
      return 'No session goal'
    const detail = goal.statusDetail ? `, ${goal.statusDetail}` : ''
    // GoalObjective renders the Markdown source.
    // The live region needs the visible words without Markdown syntax.
    // GoalObjective uses the same plain text rule for its tooltip.
    return `Session goal ${goalStatusLabel(goal.status).toLowerCase()}${detail}: ${markdownToPlainText(goal.objective)}`
  })

  // Display only the counters that the provider reports.
  // Keep an absent counter distinct from a reported zero.
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

  /** Report whether the empty card can offer Set. */
  const canSetFirstGoal = () =>
    props.goal.onAction !== undefined
    && goalActionState(props.goal, 'set').kind === 'enabled'

  return (
    <div class={styles.card} data-testid="goal-card">
      <div class={styles.headerRow}>
        {/* The user can rename the section header through section.name.
            The card therefore supplies its own fixed heading. */}
        <div class={styles.heading}>Session goal</div>
        {/* The empty state offers Set directly and displays no menu.
            GoalActionsMenu owns the decision about each existing goal's actions. */}
        <Show when={props.goal.current}>
          <GoalActionsMenu goal={props.goal} />
        </Show>
      </div>
      {/* srOnly places the live region off screen but retains it in the accessibility tree.
          display: none or visibility: hidden would suppress its announcement.

          Keep its text node mounted while the reported goal changes.
          Render the region only when announce is set.
          The sidebar owns the announcement. The popover displays the same card silently. */}
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
