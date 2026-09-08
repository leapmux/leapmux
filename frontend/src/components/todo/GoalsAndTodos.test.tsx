import type { GoalSurface } from '~/stores/chatGoal'
import type { TodoItem } from '~/stores/chatTodos'
import { fireEvent, render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { classSelector } from '~/test-support/composedClass'
import { GoalsAndTodos } from './GoalsAndTodos'
import * as todoStyles from './TodoList.css'

const todo: TodoItem = {
  id: '1',
  rowKey: '1',
  content: 'Run the tests',
  status: 'pending',
  activeForm: '',
}

function surface(over: Partial<GoalSurface> = {}): GoalSurface {
  return {
    current: { objective: 'Ship the release', status: 'active' },
    progress: {},
    actions: ['set', 'clear'],
    ...over,
  }
}

describe('goalsAndTodos', () => {
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
