import type { GoalAction, GoalSurface, SessionGoal } from '~/stores/chatGoal'
import { create } from '@bufbuild/protobuf'
import { fireEvent, render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { AgentGoalSchema, AgentGoalStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { protoGoalToStore } from '~/stores/chatGoal'
import { dangerMenuItem } from '~/styles/shared.css'
import { GoalActionsMenu } from './GoalActionsMenu'

function goal(over: Partial<SessionGoal> = {}): SessionGoal {
  return { objective: 'every test passes', status: 'active', ...over }
}

const ALL: GoalAction[] = ['set', 'clear', 'pause', 'resume']

/**
 * Use an untyped record for the explicit onAction: undefined fixture.
 * exactOptionalPropertyTypes excludes that present key from Partial<GoalSurface>.
 */
function surface(over: Record<string, unknown> = {}): GoalSurface {
  return Object.assign({ current: goal(), progress: {}, actions: ALL, onAction: vi.fn() }, over)
}

describe('GoalActionsMenu', () => {
  it('offers the verbs in the order they read', () => {
    const { getAllByRole } = render(() => (
      <GoalActionsMenu goal={surface()} />
    ))
    expect(getAllByRole('menuitem', { hidden: true }).map(i => i.textContent))
      .toEqual(['Pause', 'Resume', 'Replace goal…', 'Clear goal'])
  })

  it('runs the action its menu item states', () => {
    const onAction = vi.fn()
    const { getByTestId } = render(() => (
      <GoalActionsMenu goal={surface({ onAction })} />
    ))
    fireEvent.click(getByTestId('goal-action-clear'))
    expect(onAction).toHaveBeenCalledWith('clear')
  })

  /** An unsupported Pause or Resume action stays absent from the menu. */
  it('omits an action the provider does not support at all', () => {
    const { queryByTestId, getByTestId } = render(() => (
      <GoalActionsMenu goal={surface({ actions: ['set', 'clear'] })} />
    ))
    expect(queryByTestId('goal-action-pause')).toBeNull()
    expect(queryByTestId('goal-action-resume')).toBeNull()
    expect(getByTestId('goal-action-clear')).not.toBeNull()
  })

  /**
   * A supported action that the current goal refuses stays visible.
   * Disable it and retain its reason so it can return when the state permits it.
   */
  it('keeps a supported action the goal state refuses, disabled with its reason', () => {
    const { getByTestId } = render(() => (
      <GoalActionsMenu goal={surface({ current: goal({ status: 'paused' }) })} />
    ))
    const pause = getByTestId('goal-action-pause') as HTMLButtonElement
    expect(pause.disabled).toBe(true)
    // Keep the action as the accessible name.
    // The reason belongs in the tooltip and must not replace that name.
    expect(pause.textContent).toBe('Pause')
    expect((getByTestId('goal-action-resume') as HTMLButtonElement).disabled).toBe(false)
  })

  // Dormant permits neither Pause nor Resume.
  // Each refused action states which goal status it requires.
  it('disables pause and resume for a dormant goal, and keeps clear', () => {
    const { getByTestId } = render(() => (
      <GoalActionsMenu goal={surface({ current: goal({ status: 'dormant' }) })} />
    ))
    expect((getByTestId('goal-action-pause') as HTMLButtonElement).disabled).toBe(true)
    expect((getByTestId('goal-action-resume') as HTMLButtonElement).disabled).toBe(true)
    expect((getByTestId('goal-action-clear') as HTMLButtonElement).disabled).toBe(false)
  })

  // Clear removes the goal.
  // Separate that destructive action from the preceding actions.
  it('marks Clear goal as destructive and separates it', () => {
    const { getByTestId, container } = render(() => (
      <GoalActionsMenu goal={surface()} />
    ))
    expect(getByTestId('goal-action-clear').classList.contains(dangerMenuItem)).toBe(true)
    expect(container.querySelectorAll('hr')).toHaveLength(1)
    expect(getByTestId('goal-action-set').classList.contains(dangerMenuItem)).toBe(false)
  })

  // Nothing to separate when Clear is the only verb the provider offers.
  it('draws no rule when the destructive verb stands alone', () => {
    const { container } = render(() => (
      <GoalActionsMenu goal={surface({ actions: ['clear'] })} />
    ))
    expect(container.querySelectorAll('hr')).toHaveLength(0)
  })

  /** A provider with no supported actions displays no menu trigger. */
  it('renders nothing when the agent supports no action', () => {
    const { queryByTestId } = render(() => (
      <GoalActionsMenu goal={surface({ actions: [] })} />
    ))
    expect(queryByTestId('goal-actions-trigger')).toBeNull()
    for (const action of ALL)
      expect(queryByTestId(`goal-action-${action}`)).toBeNull()
  })

  /**
   * The menu owns the complete decision about whether a control can act.
   * A host with no handler displays no trigger.
   * The card must not duplicate that decision from the same surface.
   */
  it('renders nothing when the surface carries no handler', () => {
    const { queryByTestId } = render(() => (
      <GoalActionsMenu goal={surface({ onAction: undefined })} />
    ))
    expect(queryByTestId('goal-actions-trigger')).toBeNull()
  })

  // The icon trigger has no visible text.
  // Its tooltip supplies its accessible name.
  it('gives its trigger an accessible name', () => {
    const { getByRole } = render(() => (
      <GoalActionsMenu goal={surface()} />
    ))
    expect(getByRole('button', { name: 'Goal actions' })).not.toBeNull()
  })
})

describe('unknown goal controls', () => {
  it('keeps supported controls and refuses state-dependent actions for an unknown goal', () => {
    const current = protoGoalToStore(create(AgentGoalSchema, { objective: 'Keep the goal', status: AgentGoalStatus.UNKNOWN, statusDetail: 'future-state' }))
    const onAction = vi.fn()
    const { getByTestId } = render(() => <GoalActionsMenu goal={surface({ current, onAction })} />)
    expect(getByTestId('goal-action-pause')).toBeDisabled()
    expect(getByTestId('goal-action-resume')).toBeDisabled()
    expect(getByTestId('goal-action-set')).not.toBeDisabled()
    expect(getByTestId('goal-action-clear')).not.toBeDisabled()
    fireEvent.click(getByTestId('goal-actions-trigger'))
    fireEvent.click(getByTestId('goal-action-clear'))
    expect(onAction).toHaveBeenCalledExactlyOnceWith('clear')
    expect(current.status).toBe('unknown')
  })
})
