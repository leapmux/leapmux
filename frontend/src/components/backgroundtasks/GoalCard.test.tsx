import type { GoalAction, SessionGoal } from '~/stores/chatGoal'
import { fireEvent, render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { GoalCard } from './GoalCard'

function goal(over: Partial<SessionGoal> = {}): SessionGoal {
  return { objective: 'every test passes', status: 'active', ...over }
}

const ALL: GoalAction[] = ['set', 'clear', 'pause', 'resume']

describe('goalCard', () => {
  it('shows the objective and its status', () => {
    const { getByTestId } = render(() => (
      <GoalCard goal={goal()} progress={{}} supportedActions={ALL} />
    ))
    expect(getByTestId('goal-objective').textContent).toBe('every test passes')
    expect(getByTestId('goal-status-dot').getAttribute('data-status')).toBe('active')
  })

  // The provider's own word survives beside the neutral status, because mapping
  // five vocabularies onto four values loses which limit was hit.
  it('shows the provider status detail beside the neutral status', () => {
    const { getByTestId } = render(() => (
      <GoalCard goal={goal({ status: 'blocked', statusDetail: 'usageLimited' })} progress={{}} supportedActions={ALL} />
    ))
    expect(getByTestId('goal-status-detail').textContent).toContain('usageLimited')
  })

  /**
   * Absent and zero are different answers. No two providers report the same
   * counters, so a field the provider never sent must not render as a zero --
   * "0 tokens" states a number nobody gave.
   */
  it('renders only the counters the provider reported', () => {
    const { getByTestId } = render(() => (
      <GoalCard goal={goal()} progress={{ tokensUsed: 1200, timeUsedSeconds: 90 }} supportedActions={ALL} />
    ))
    const text = getByTestId('goal-progress').textContent ?? ''
    expect(text).toContain('1,200 tokens')
    expect(text).toContain('1m 30s')
    expect(text).not.toContain('turn')
  })

  it('shows a token budget beside the usage when one is reported', () => {
    const { getByTestId } = render(() => (
      <GoalCard goal={goal()} progress={{ tokensUsed: 500, tokenBudget: 2000 }} supportedActions={ALL} />
    ))
    expect(getByTestId('goal-progress').textContent).toContain('500 / 2,000 tokens')
  })

  it('omits the progress row entirely when nothing was reported', () => {
    const { queryByTestId } = render(() => (
      <GoalCard goal={goal()} progress={{}} supportedActions={ALL} />
    ))
    expect(queryByTestId('goal-progress')).toBeNull()
  })

  /**
   * There is deliberately no timer. `ToolRunningBadge` made the same call for
   * the same render cost, and Codex's `timeUsedSeconds` is BUDGET CONSUMED
   * rather than wall clock -- ticking it would assert spending while the agent
   * waits on an approval, and the number would jump backwards when the real one
   * lands.
   */
  it('does not tick the elapsed time', () => {
    vi.useFakeTimers()
    try {
      const { getByTestId } = render(() => (
        <GoalCard goal={goal()} progress={{ timeUsedSeconds: 30 }} supportedActions={ALL} />
      ))
      const before = getByTestId('goal-progress').textContent
      vi.advanceTimersByTime(5000)
      expect(getByTestId('goal-progress').textContent).toBe(before)
    }
    finally {
      vi.useRealTimers()
    }
  })

  it('runs the action a verb button names', () => {
    const onAction = vi.fn()
    const { getByTestId } = render(() => (
      <GoalCard goal={goal()} progress={{}} supportedActions={ALL} onAction={onAction} />
    ))
    fireEvent.click(getByTestId('goal-action-clear'))
    expect(onAction).toHaveBeenCalledWith('clear')
  })

  /**
   * Claude Code's gap, and the one a user meets most: it has no pause and no
   * resume at all. That gap is PERMANENT, so the buttons are absent -- a control
   * that can never light up says less than no control.
   */
  it('omits an action the provider does not support at all', () => {
    const { queryByTestId, getByTestId } = render(() => (
      <GoalCard goal={goal()} progress={{}} supportedActions={['set', 'clear']} onAction={vi.fn()} />
    ))
    expect(queryByTestId('goal-action-pause')).toBeNull()
    expect(queryByTestId('goal-action-resume')).toBeNull()
    expect(getByTestId('goal-action-clear')).not.toBeNull()
  })

  /**
   * The other half of the same rule. A SUPPORTED action that the current goal
   * state refuses keeps its place, disabled with the reason, because it comes
   * back the moment the state changes.
   */
  it('keeps a supported action the goal state refuses, disabled with its reason', () => {
    const { getByTestId } = render(() => (
      <GoalCard goal={goal({ status: 'paused' })} progress={{}} supportedActions={ALL} onAction={vi.fn()} />
    ))
    const pause = getByTestId('goal-action-pause') as HTMLButtonElement
    expect(pause.disabled).toBe(true)
    // The accessible name stays the verb. An ariaLabel carrying the reason
    // would announce a sentence where "Pause" belongs and break every
    // by-role lookup.
    expect(pause.textContent).toBe('Pause')
    expect((getByTestId('goal-action-resume') as HTMLButtonElement).disabled).toBe(false)
  })

  // A read-only provider (Reasonix reports a goal but can change none) shows the
  // goal and no controls at all, rather than a row of dead buttons.
  it('renders no controls when the agent supports no action', () => {
    const { queryByTestId } = render(() => (
      <GoalCard goal={goal()} progress={{}} supportedActions={[]} onAction={vi.fn()} />
    ))
    for (const action of ALL)
      expect(queryByTestId(`goal-action-${action}`)).toBeNull()
  })

  // Setting is how the FIRST goal arrives, so the empty state has to offer it --
  // which is why the capability list is separate from the goal.
  it('offers Set a goal in the empty state when the agent supports it', () => {
    const onAction = vi.fn()
    const { getByTestId } = render(() => (
      <GoalCard progress={{}} supportedActions={['set']} onAction={onAction} />
    ))
    const button = getByTestId('goal-action-set') as HTMLButtonElement
    expect(button.disabled).toBe(false)
    fireEvent.click(button)
    expect(onAction).toHaveBeenCalledWith('set')
  })

  it('omits Set a goal for an agent that cannot set one', () => {
    const { queryByTestId, getByTestId } = render(() => (
      <GoalCard progress={{}} supportedActions={[]} onAction={vi.fn()} />
    ))
    expect(queryByTestId('goal-action-set')).toBeNull()
    // The card still says what it knows, which is that there is no goal.
    expect(getByTestId('goal-card-empty')).not.toBeNull()
  })

  // One stable node with changing text. A `<Show>` that swapped nodes would
  // make a screen reader re-announce on every rebuild.
  it('keeps one polite live region that states the current goal', () => {
    const { container } = render(() => (
      <GoalCard goal={goal({ status: 'blocked', statusDetail: 'notSatisfied' })} progress={{}} supportedActions={[]} />
    ))
    const live = container.querySelectorAll('[role="status"][aria-live="polite"]')
    expect(live.length).toBe(1)
    expect(live[0].textContent).toContain('every test passes')
    expect(live[0].textContent).toContain('notSatisfied')
  })
})
