import type { GoalAction, GoalSurface, SessionGoal } from '~/stores/chatGoal'
import { fireEvent, render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { dangerMenuItem } from '~/styles/shared.css'
import { GoalActionsMenu } from './GoalActionsMenu'

function goal(over: Partial<SessionGoal> = {}): SessionGoal {
  return { objective: 'every test passes', status: 'active', ...over }
}

const ALL: GoalAction[] = ['set', 'clear', 'pause', 'resume']

function surface(over: Partial<GoalSurface> = {}): GoalSurface {
  return { current: goal(), progress: {}, actions: ALL, ...over }
}

describe('goalActionsMenu', () => {
  it('offers the verbs in the order they read', () => {
    const { getAllByRole } = render(() => (
      <GoalActionsMenu goal={surface()} onAction={vi.fn()} />
    ))
    expect(getAllByRole('menuitem', { hidden: true }).map(i => i.textContent))
      .toEqual(['Pause', 'Resume', 'Replace goal…', 'Clear goal'])
  })

  it('runs the action a menu item names', () => {
    const onAction = vi.fn()
    const { getByTestId } = render(() => (
      <GoalActionsMenu goal={surface()} onAction={onAction} />
    ))
    fireEvent.click(getByTestId('goal-action-clear'))
    expect(onAction).toHaveBeenCalledWith('clear')
  })

  /**
   * Claude Code's gap, and the one a user meets most: it has no pause and no
   * resume at all. That gap is PERMANENT, so the items are absent -- an item
   * that can never light up says less than no item.
   */
  it('omits an action the provider does not support at all', () => {
    const { queryByTestId, getByTestId } = render(() => (
      <GoalActionsMenu goal={surface({ actions: ['set', 'clear'] })} onAction={vi.fn()} />
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
      <GoalActionsMenu goal={surface({ current: goal({ status: 'paused' }) })} onAction={vi.fn()} />
    ))
    const pause = getByTestId('goal-action-pause') as HTMLButtonElement
    expect(pause.disabled).toBe(true)
    // The accessible name stays the verb. An ariaLabel carrying the reason
    // would announce a sentence where "Pause" belongs and break every
    // by-role lookup.
    expect(pause.textContent).toBe('Pause')
    expect((getByTestId('goal-action-resume') as HTMLButtonElement).disabled).toBe(false)
  })

  // Neither verb applies to a dormant goal, and each says which state it needs
  // rather than going silent.
  it('disables pause and resume for a dormant goal, and keeps clear', () => {
    const { getByTestId } = render(() => (
      <GoalActionsMenu goal={surface({ current: goal({ status: 'dormant' }) })} onAction={vi.fn()} />
    ))
    expect((getByTestId('goal-action-pause') as HTMLButtonElement).disabled).toBe(true)
    expect((getByTestId('goal-action-resume') as HTMLButtonElement).disabled).toBe(true)
    expect((getByTestId('goal-action-clear') as HTMLButtonElement).disabled).toBe(false)
  })

  // Clearing destroys the goal. It is the one item that reads as destructive,
  // and a rule keeps it away from the verb above it.
  it('marks Clear goal as destructive and separates it', () => {
    const { getByTestId, container } = render(() => (
      <GoalActionsMenu goal={surface()} onAction={vi.fn()} />
    ))
    expect(getByTestId('goal-action-clear').classList.contains(dangerMenuItem)).toBe(true)
    expect(container.querySelectorAll('hr')).toHaveLength(1)
    expect(getByTestId('goal-action-set').classList.contains(dangerMenuItem)).toBe(false)
  })

  // Nothing to separate when Clear is the only verb the provider offers.
  it('draws no rule when the destructive verb stands alone', () => {
    const { container } = render(() => (
      <GoalActionsMenu goal={surface({ actions: ['clear'] })} onAction={vi.fn()} />
    ))
    expect(container.querySelectorAll('hr')).toHaveLength(0)
  })

  /**
   * A read-only provider -- Reasonix reports a goal but can change none --
   * gets no trigger at all, rather than a `...` that opens an empty card.
   */
  it('renders nothing when the agent supports no action', () => {
    const { queryByTestId } = render(() => (
      <GoalActionsMenu goal={surface({ actions: [] })} onAction={vi.fn()} />
    ))
    expect(queryByTestId('goal-actions-trigger')).toBeNull()
    for (const action of ALL)
      expect(queryByTestId(`goal-action-${action}`)).toBeNull()
  })

  // The trigger is an icon with no visible text, so its tooltip is also its
  // accessible name.
  it('names its trigger', () => {
    const { getByRole } = render(() => (
      <GoalActionsMenu goal={surface()} onAction={vi.fn()} />
    ))
    expect(getByRole('button', { name: 'Goal actions' })).not.toBeNull()
  })
})
