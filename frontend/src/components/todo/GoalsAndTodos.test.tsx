import type { TodoItem } from '~/models/todo'
import type { GoalSurface } from '~/stores/chatGoal'
import { create } from '@bufbuild/protobuf'
import { fireEvent, render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { AgentGoalSchema, AgentGoalStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { protoGoalToStore } from '~/stores/chatGoal'
import { classSelector } from '~/test-support/composedClass'
import { GoalsAndTodos } from './GoalsAndTodos'
import * as styles from './GoalsAndTodos.css'
import * as todoStyles from './TodoList.css'

const todo: TodoItem = {
  id: '1',
  rowKey: '1',
  content: 'Run the tests',
  status: 'pending',
  activeForm: '',
}

// Use an untyped record for the explicit current: undefined fixture.
// exactOptionalPropertyTypes excludes that present key from Partial<GoalSurface>.
function surface(over: Record<string, unknown> = {}): GoalSurface {
  const base: GoalSurface = { current: { objective: 'Ship the release', status: 'active' }, progress: {}, actions: ['set', 'clear'] }
  return Object.assign(base, over)
}

describe('GoalsAndTodos', () => {
  it('renders the card, separator, and list in that order', () => {
    const { getByTestId } = render(() => (
      <GoalsAndTodos variant="sidebar" goal={surface()} todos={[todo]} />
    ))
    const root = getByTestId('goals-and-todos')
    const order = [...root.children].map((element) => {
      if (element.matches(classSelector(todoStyles.todoList)))
        return 'todo-list'
      return element.getAttribute('data-testid')
    })
    expect(order.slice(0, 3)).toEqual(['goal-card', 'goal-card-separator', 'todo-list'])
  })

  it('draws the separator only when both sections render', () => {
    const withTodos = render(() => (
      <GoalsAndTodos variant="sidebar" goal={surface()} todos={[todo]} />
    ))
    expect(withTodos.queryByTestId('goal-card-separator')).not.toBeNull()

    const withoutTodos = render(() => (
      <GoalsAndTodos variant="sidebar" goal={surface()} todos={[]} />
    ))
    expect(withoutTodos.queryByTestId('goal-card-separator')).toBeNull()
  })

  it('renders the list alone when the provider has no goal feature', () => {
    const { container, queryByTestId } = render(() => (
      <GoalsAndTodos variant="sidebar" todos={[todo]} />
    ))
    expect(queryByTestId('goal-card')).toBeNull()
    expect(queryByTestId('goal-card-separator')).toBeNull()
    expect(container.querySelector(classSelector(todoStyles.todoList))).not.toBeNull()
  })

  it('renders the empty goal card and its Set route', () => {
    const onAction = vi.fn()
    const { getByTestId } = render(() => (
      <GoalsAndTodos
        variant="sidebar"
        goal={surface({ current: undefined, actions: ['set'], onAction })}
        todos={[]}
      />
    ))
    expect(getByTestId('goal-card-empty')).toHaveTextContent('No session goal.')
    fireEvent.click(getByTestId('goal-action-set'))
    expect(onAction).toHaveBeenCalledWith('set')
  })

  it('does not mount TodoList for an empty to-do list', () => {
    const { container } = render(() => (
      <GoalsAndTodos variant="sidebar" goal={surface()} todos={[]} />
    ))
    expect(container.querySelector(classSelector(todoStyles.todoList))).toBeNull()
  })

  /**
   * The separator requires a goal surface and a nonempty to-do list.
   * The surface's current goal can be absent because the empty card still occupies that position.
   * Requiring goal.current would remove the gap before the first to-do row.
   */
  it('draws the separator under an empty card too', () => {
    const { getByTestId } = render(() => (
      <GoalsAndTodos variant="sidebar" goal={surface({ current: undefined })} todos={[todo]} />
    ))
    expect(getByTestId('goal-card-empty')).not.toBeNull()
    expect(getByTestId('goal-card-separator')).not.toBeNull()
  })

  /**
   * The popover variant restricts the objective width.
   * The sidebar variant applies no such maximum.
   * This case detects an inverted variant condition.
   */
  it('caps the popover variant, and only that variant', () => {
    const popover = render(() => (
      <GoalsAndTodos variant="popover" goal={surface()} todos={[todo]} />
    ))
    expect(popover.getByTestId('goals-and-todos').matches(classSelector(styles.popoverRoot))).toBe(true)

    const sidebar = render(() => (
      <GoalsAndTodos variant="sidebar" goal={surface()} todos={[todo]} />
    ))
    expect(sidebar.getByTestId('goals-and-todos').matches(classSelector(styles.popoverRoot))).toBe(false)
  })

  it('mounts one live region only when this host owns announcements', () => {
    const announcing = render(() => (
      <GoalsAndTodos variant="sidebar" goal={surface()} todos={[]} announceGoal />
    ))
    expect(announcing.container.querySelectorAll('[role="status"][aria-live="polite"]')).toHaveLength(1)

    const silent = render(() => (
      <GoalsAndTodos variant="popover" goal={surface()} todos={[]} />
    ))
    expect(silent.container.querySelectorAll('[role="status"][aria-live="polite"]')).toHaveLength(0)
  })
})

describe('unknown goals on both hosts', () => {
  it('retains the same unknown goal and only the sidebar announcement', () => {
    const current = protoGoalToStore(create(AgentGoalSchema, { objective: 'Keep the native objective', status: AgentGoalStatus.UNKNOWN, statusDetail: 'native-future-state' }))
    const shared = surface({ current })
    const { container, getAllByTestId } = render(() => (
      <>
        <GoalsAndTodos variant="sidebar" goal={shared} todos={[todo]} announceGoal />
        <GoalsAndTodos variant="popover" goal={shared} todos={[]} />
      </>
    ))
    expect(getAllByTestId('goal-objective')).toHaveLength(2)
    expect(getAllByTestId('goal-status-detail')).toHaveLength(2)
    expect(getAllByTestId('goal-status-detail')[0]).toHaveTextContent('native-future-state')
    expect(container.querySelectorAll('[role="status"][aria-live="polite"]')).toHaveLength(1)
    expect(container.querySelectorAll('[data-testid="goal-card-separator"]')).toHaveLength(1)
    for (const dot of getAllByTestId('goal-status-dot'))
      expect(dot).toHaveAttribute('data-status', 'unknown')
  })
})
