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
      <GoalCard goal={{ current: goal(), progress: {}, actions: ALL }} />
    ))
    expect(getByTestId('goal-objective').textContent).toBe('every test passes')
    expect(getByTestId('goal-status-dot').getAttribute('data-status')).toBe('active')
  })

  // The provider's own word survives beside the neutral status, because mapping
  // five vocabularies onto four values loses which limit was hit.
  it('shows the provider status detail beside the neutral status', () => {
    const { getByTestId } = render(() => (
      <GoalCard goal={{ current: goal({ status: 'blocked', statusDetail: 'usageLimited' }), progress: {}, actions: ALL }} />
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
      <GoalCard goal={{ current: goal(), progress: { tokensUsed: 1200, timeUsedSeconds: 90 }, actions: ALL }} />
    ))
    const text = getByTestId('goal-progress').textContent ?? ''
    expect(text).toContain('1,200 tokens')
    expect(text).toContain('1m 30s')
    expect(text).not.toContain('turn')
  })

  it('shows a token budget beside the usage when one is reported', () => {
    const { getByTestId } = render(() => (
      <GoalCard goal={{ current: goal(), progress: { tokensUsed: 500, tokenBudget: 2000 }, actions: ALL }} />
    ))
    expect(getByTestId('goal-progress').textContent).toContain('500 / 2,000 tokens')
  })

  it('omits the progress row entirely when nothing was reported', () => {
    const { queryByTestId } = render(() => (
      <GoalCard goal={{ current: goal(), progress: {}, actions: ALL }} />
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
        <GoalCard goal={{ current: goal(), progress: { timeUsedSeconds: 30 }, actions: ALL }} />
      ))
      const before = getByTestId('goal-progress').textContent
      vi.advanceTimersByTime(5000)
      expect(getByTestId('goal-progress').textContent).toBe(before)
    }
    finally {
      vi.useRealTimers()
    }
  })

  /**
   * The card offers the verbs behind one `...` trigger, so a menu is what a
   * goal with a handler shows. Which verbs it holds, and which of them are
   * refused, is `GoalActionsMenu`'s decision and is tested there.
   */
  it('offers the actions menu for a goal it can act on', () => {
    const { getByTestId } = render(() => (
      <GoalCard goal={{ current: goal(), progress: {}, actions: ALL, onAction: vi.fn() }} />
    ))
    expect(getByTestId('goal-actions-trigger')).not.toBeNull()
  })

  /**
   * The card hands the menu the WHOLE surface, so the verb a reader picks
   * reaches the handler that surface carries.
   *
   * Tested here rather than only in `./GoalActionsMenu.test.tsx`, because that
   * suite renders the menu alone: it cannot see the card dropping the handler
   * or forwarding the wrong action, and the only other coverage of this path
   * sits in another component's test file.
   */
  it('runs a menu verb against the handler its surface carries', () => {
    const onAction = vi.fn()
    const { getByTestId } = render(() => (
      <GoalCard goal={{ current: goal(), progress: {}, actions: ALL, onAction }} />
    ))

    fireEvent.click(getByTestId('goal-actions-trigger'))
    fireEvent.click(getByTestId('goal-action-pause'))

    expect(onAction).toHaveBeenCalledWith('pause')
  })

  // A read-only surface: the panel renders the goal, and nothing can change it.
  it('offers no actions menu when the surface has no handler', () => {
    const { queryByTestId, getByTestId } = render(() => (
      <GoalCard goal={{ current: goal(), progress: {}, actions: ALL }} />
    ))
    expect(queryByTestId('goal-actions-trigger')).toBeNull()
    // The goal itself is still on screen; only the verbs are absent.
    expect(getByTestId('goal-objective').textContent).toContain('every test passes')
  })

  // A read-only provider (Reasonix reports a goal but can change none) shows the
  // goal and no controls at all, rather than a row of dead buttons.
  it('renders no controls when the agent supports no action', () => {
    const { queryByTestId } = render(() => (
      <GoalCard goal={{ current: goal(), progress: {}, actions: [], onAction: vi.fn() }} />
    ))
    expect(queryByTestId('goal-actions-trigger')).toBeNull()
    for (const action of ALL)
      expect(queryByTestId(`goal-action-${action}`)).toBeNull()
  })

  /**
   * No menu in the empty state. `set` is the only verb that applies with no
   * goal, and the empty state offers it as its own call to action -- a first
   * goal must not be one click deeper than the concept it introduces.
   */
  it('offers no actions menu in the empty state', () => {
    const { queryByTestId } = render(() => (
      <GoalCard goal={{ progress: {}, actions: ['set'], onAction: vi.fn() }} />
    ))
    expect(queryByTestId('goal-actions-trigger')).toBeNull()
  })

  // Setting is how the FIRST goal arrives, so the empty state has to offer it --
  // which is why the capability list is separate from the goal.
  it('offers Set a goal in the empty state when the agent supports it', () => {
    const onAction = vi.fn()
    const { getByTestId } = render(() => (
      <GoalCard goal={{ progress: {}, actions: ['set'], onAction }} />
    ))
    const button = getByTestId('goal-action-set') as HTMLButtonElement
    expect(button.disabled).toBe(false)
    fireEvent.click(button)
    expect(onAction).toHaveBeenCalledWith('set')
  })

  /**
   * A separator states that something FOLLOWS, and the card cannot see what is
   * below it. GoalsAndTodos renders the rule when a to-do list follows.
   */
  it('draws no separator of its own', () => {
    const { container } = render(() => (
      <GoalCard goal={{ current: goal(), progress: {}, actions: ALL }} />
    ))
    expect(container.querySelectorAll('hr')).toHaveLength(0)
  })

  it('omits Set a goal for an agent that cannot set one', () => {
    const { queryByTestId, getByTestId } = render(() => (
      <GoalCard goal={{ progress: {}, actions: [], onAction: vi.fn() }} />
    ))
    expect(queryByTestId('goal-action-set')).toBeNull()
    // The card still says what it knows, which is that there is no goal.
    expect(getByTestId('goal-card-empty')).not.toBeNull()
  })

  // One stable node with changing text. A `<Show>` that swapped nodes would
  // make a screen reader re-announce on every rebuild.
  it('keeps one polite live region that states the current goal', () => {
    const { container } = render(() => (
      <GoalCard goal={{ current: goal({ status: 'blocked', statusDetail: 'notSatisfied' }), progress: {}, actions: [] }} announce />
    ))
    const live = container.querySelectorAll('[role="status"][aria-live="polite"]')
    expect(live.length).toBe(1)
    expect(live[0].textContent).toContain('every test passes')
    expect(live[0].textContent).toContain('notSatisfied')
  })

  /**
   * The objective is markdown SOURCE, and the card renders it. A screen reader
   * handed the source reads the syntax -- "ship the asterisk asterisk auth
   * refactor asterisk asterisk" -- so the live region announces the words the
   * card actually shows. `GoalObjective` refuses to hand the source to
   * `Tooltip`'s `text` for the same reason.
   */
  it('announces the objective as words, not as markdown syntax', () => {
    const { container } = render(() => (
      <GoalCard
        goal={{
          current: goal({ objective: 'ship the **auth refactor**, see `task test`' }),
          progress: {},
          actions: [],
        }}
        announce
      />
    ))
    const live = container.querySelector('[role="status"][aria-live="polite"]')!
    expect(live.textContent).toContain('ship the auth refactor, see task test')
    expect(live.textContent).not.toContain('**')
    expect(live.textContent).not.toContain('`')
  })

  /**
   * Two cards can be on screen at once: the sidebar section and an open
   * ThinkingIndicator popover render the same content. A live region in each
   * announces one goal change twice, so only the instance that sets `announce`
   * holds one.
   */
  it('holds no live region unless it owns the announcement', () => {
    const { container } = render(() => (
      <GoalCard goal={{ current: goal(), progress: {}, actions: [] }} />
    ))
    expect(container.querySelectorAll('[role="status"][aria-live="polite"]')).toHaveLength(0)
    // The objective is still on screen; only the announcement is elsewhere.
    expect(container.textContent).toContain('every test passes')
  })

  /**
   * A dormant goal is WAITING, not failing: no live process pursues it. The
   * worker writes that state at boot and when an agent exits, so it reaches the
   * card on every restart -- and reporting it as a fault would cry wolf each
   * time.
   */
  it('renders a dormant goal as waiting rather than as a fault', () => {
    const { getByTestId } = render(() => (
      <GoalCard goal={{ current: goal({ status: 'dormant' }), progress: {}, actions: ALL }} />
    ))
    expect(getByTestId('goal-status-dot').getAttribute('data-status')).toBe('dormant')
    expect(getByTestId('goal-card').textContent).toContain('Not running')
    expect(getByTestId('goal-card').textContent).not.toContain('Needs attention')
  })
})
