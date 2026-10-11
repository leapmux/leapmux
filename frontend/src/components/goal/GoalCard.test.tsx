import type { GoalAction, SessionGoal } from '~/stores/chatGoal'
import { create } from '@bufbuild/protobuf'
import { fireEvent, render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { AgentGoalSchema, AgentGoalStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { protoGoalToStore } from '~/stores/chatGoal'
import * as statusDotStyles from '~/styles/statusDot.css'
import { GoalCard } from './GoalCard'

function goal(over: Partial<SessionGoal> = {}): SessionGoal {
  return { objective: 'every test passes', status: 'active', ...over }
}

const ALL: GoalAction[] = ['set', 'clear', 'pause', 'resume']

describe('GoalCard', () => {
  it('shows the objective and its status', () => {
    const { getByTestId } = render(() => (
      <GoalCard goal={{ current: goal(), progress: {}, actions: ALL }} />
    ))
    expect(getByTestId('goal-objective').textContent).toBe('every test passes')
    expect(getByTestId('goal-status-dot').getAttribute('data-status')).toBe('active')
  })

  // Retain the provider's native detail beside the neutral status.
  // The neutral status alone cannot identify each native limit.
  it('shows the provider status detail beside the neutral status', () => {
    const { getByTestId } = render(() => (
      <GoalCard goal={{ current: goal({ status: 'blocked', statusDetail: 'usageLimited' }), progress: {}, actions: ALL }} />
    ))
    expect(getByTestId('goal-status-detail').textContent).toContain('usageLimited')
  })

  /**
   * Keep an absent counter distinct from a reported zero.
   * A missing field must not display a number that the provider did not report.
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
   * Render the provider's time counter without a local timer.
   * A timer could increase that counter during an approval wait.
   * The next provider report could then reduce the displayed value.
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
   * A current goal with an action handler displays one menu trigger.
   * GoalActionsMenu decides which supported actions can run.
   */
  it('offers the actions menu for a goal it can act on', () => {
    const { getByTestId } = render(() => (
      <GoalCard goal={{ current: goal(), progress: {}, actions: ALL, onAction: vi.fn() }} />
    ))
    expect(getByTestId('goal-actions-trigger')).not.toBeNull()
  })

  /**
   * Forward the complete surface so the selected action reaches its handler.
   * The menu's own tests cannot detect a card that omits or replaces that handler.
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

  // The read-only surface displays the goal and offers no change controls.
  it('offers no actions menu when the surface has no handler', () => {
    const { queryByTestId, getByTestId } = render(() => (
      <GoalCard goal={{ current: goal(), progress: {}, actions: ALL }} />
    ))
    expect(queryByTestId('goal-actions-trigger')).toBeNull()
    // Keep the goal visible when its actions are absent.
    expect(getByTestId('goal-objective').textContent).toContain('every test passes')
  })

  // A provider with no supported actions displays the goal without controls.
  it('renders no controls when the agent supports no action', () => {
    const { queryByTestId } = render(() => (
      <GoalCard goal={{ current: goal(), progress: {}, actions: [], onAction: vi.fn() }} />
    ))
    expect(queryByTestId('goal-actions-trigger')).toBeNull()
    for (const action of ALL)
      expect(queryByTestId(`goal-action-${action}`)).toBeNull()
  })

  /**
   * The empty card offers Set directly.
   * It displays no menu because other actions require a current goal.
   */
  it('offers no actions menu in the empty state', () => {
    const { queryByTestId } = render(() => (
      <GoalCard goal={{ progress: {}, actions: ['set'], onAction: vi.fn() }} />
    ))
    expect(queryByTestId('goal-actions-trigger')).toBeNull()
  })

  // The separate capability list lets the empty card offer the first Set action.
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
   * GoalsAndTodos supplies a separator when a to-do list follows the card.
   * The card cannot determine whether another row follows it.
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

  // Keep one stable live-region node while its text changes.
  // Replacing that node could cause an additional screen-reader announcement.
  it('keeps one polite live region that states the current goal', () => {
    const { container } = render(() => (
      <GoalCard goal={{ current: goal({ status: 'blocked', statusDetail: 'notSatisfied' }), progress: {}, actions: [] }} announce />
    ))
    const live = container.querySelectorAll('[role="status"][aria-live="polite"]')
    expect(live.length).toBe(1)
    expect(live[0]?.textContent).toContain('every test passes')
    expect(live[0]?.textContent).toContain('notSatisfied')
  })

  /**
   * The card renders Markdown but its live region announces plain text.
   * Reading Markdown source would announce syntax that the visible card does not show.
   * GoalObjective uses the same plain text rule for its tooltip.
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
   * The sidebar and an open ThinkingIndicator popover can display the same goal.
   * Only the card with announce set supplies a live region.
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
   * The worker derives Dormant when no live process serves an agent with a stored goal.
   * That projection changes no stored goal status or timestamp.
   * The card reports no running process without displaying a fault.
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

describe('unknown goal presentation', () => {
  it('shows a neutral unknown status and retains the native detail and counters', () => {
    const current = protoGoalToStore(create(AgentGoalSchema, { nativeId: 'unknown-goal', objective: 'Keep the **objective**', status: AgentGoalStatus.UNKNOWN, statusDetail: 'provider-future-state' }))
    const { getByTestId, container } = render(() => <GoalCard goal={{ current, progress: { tokensUsed: 0, timeUsedSeconds: 30, iterations: 2 }, actions: ALL }} announce />)
    const dot = getByTestId('goal-status-dot')
    expect(getByTestId('goal-objective').textContent).toContain('Keep the objective')
    expect(getByTestId('goal-status-detail')).toHaveTextContent('provider-future-state')
    expect(getByTestId('goal-progress')).toHaveTextContent('0 tokens')
    expect(getByTestId('goal-progress')).toHaveTextContent('30s')
    expect(getByTestId('goal-progress')).toHaveTextContent('2 turns')
    expect(dot).toHaveAttribute('data-status', 'unknown')
    expect(dot).toHaveAttribute('aria-label', 'Unknown')
    expect(dot.classList.contains(statusDotStyles.statusDotMuted)).toBe(true)
    expect(dot.classList.contains(statusDotStyles.statusDotDanger)).toBe(false)
    expect(dot.classList.contains(statusDotStyles.statusDotActive)).toBe(false)
    expect(container.querySelector('[role="status"][aria-live="polite"]')).toHaveTextContent('Session goal unknown, provider-future-state: Keep the objective')
    expect(getByTestId('goal-card')).not.toHaveTextContent('Needs attention')
  })

  it('keeps an unknown read-only goal visible without inventing progress or controls', () => {
    const current = protoGoalToStore(create(AgentGoalSchema, { objective: 'Keep the goal', status: AgentGoalStatus.UNKNOWN }))
    const { getByTestId, queryByTestId } = render(() => <GoalCard goal={{ current, progress: {}, actions: [] }} />)
    expect(getByTestId('goal-card')).toHaveTextContent('Keep the goal')
    expect(queryByTestId('goal-progress')).toBeNull()
    expect(queryByTestId('goal-status-detail')).toBeNull()
    expect(queryByTestId('goal-actions-trigger')).toBeNull()
    expect(getByTestId('goal-status-dot')).toHaveAttribute('aria-label', 'Unknown')
  })
})
