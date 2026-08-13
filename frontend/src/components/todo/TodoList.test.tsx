import type { TodoItem } from '~/stores/chatTodos'
import { render } from '@solidjs/testing-library'
import { createStore } from 'solid-js/store'
import { describe, expect, it, vi } from 'vitest'
import { TodoList } from '~/components/todo/TodoList'
import * as styles from '~/components/todo/TodoList.css'
import { clippedText } from '~/styles/shared.css'
import { hoverForTooltip, stubClipped, stubFitting, unhoverTooltip } from '~/test-support/clipStub'
import { classSelector } from '~/test-support/composedClass'

describe('todoList', () => {
  it('renders the deleted checkbox + strike-through for a deleted row', () => {
    const { container } = render(() => (
      <TodoList todos={[{ id: '1', content: 'gone task', status: 'deleted', activeForm: '' }]} />
    ))
    // The strike-through is applied to the row that wraps the checkbox.
    // vanilla-extract hashes class names but always retains the source
    // identifier as a substring, so we match on "todoStruck".
    const row = container.querySelector('[data-task-checkbox="deleted"]')?.closest('div')?.parentElement
    expect(row?.className).toMatch(/todoStruck/)
    expect(container.querySelector('[data-task-checkbox="deleted"]')).toBeTruthy()
    expect(container.textContent).toContain('gone task')
  })

  it('renders activeForm (not content) for in_progress rows', () => {
    const { container } = render(() => (
      <TodoList todos={[{ id: '2', content: 'Run tests', status: 'in_progress', activeForm: 'Running tests' }]} />
    ))
    expect(container.textContent).toContain('Running tests')
    expect(container.textContent).not.toContain('Run tests')
    expect(container.querySelector('[data-task-checkbox="in_progress"]')).toBeTruthy()
  })

  it('mixes statuses in a single list and emits the right checkbox glyph per row', () => {
    const { container } = render(() => (
      <TodoList
        todos={[
          { id: 'a', content: 'still pending', status: 'pending', activeForm: '' },
          { id: 'b', content: 'doing now', status: 'in_progress', activeForm: 'Working on it' },
          { id: 'c', content: 'all done', status: 'completed', activeForm: '' },
          { id: 'd', content: 'removed', status: 'deleted', activeForm: '' },
        ]}
      />
    ))
    expect(container.querySelectorAll('[data-task-checkbox="pending"]')).toHaveLength(1)
    expect(container.querySelectorAll('[data-task-checkbox="in_progress"]')).toHaveLength(1)
    expect(container.querySelectorAll('[data-task-checkbox="completed"]')).toHaveLength(1)
    expect(container.querySelectorAll('[data-task-checkbox="deleted"]')).toHaveLength(1)
  })

  /**
   * A to-do is a Solid store proxy, so every read of it belongs in a tracked
   * scope. The row's `classList` reads `status` that way, and so does the label
   * through `todoDisplayLabel`.
   *
   * The label was ALREADY tracked before the clipping change: `{todoDisplayLabel(todo)}`
   * in JSX compiles to a lazy accessor, so hoisting the surrounding element into
   * a `const` never froze it. This case therefore passes either way and only
   * guards against a future regression -- see the description case below for the
   * read that genuinely was not tracked.
   */
  it('follows an in-place status change into the label and the row class', () => {
    const [todos, setTodos] = createStore<TodoItem[]>([
      { id: '1', content: 'Run tests', status: 'pending', activeForm: 'Running tests' },
    ])
    const { container } = render(() => <TodoList todos={todos} />)
    expect(container.textContent).toContain('Run tests')

    setTodos(0, 'status', 'in_progress')

    const row = container.querySelector('[data-task-checkbox="in_progress"]')?.closest('div')?.parentElement
    expect(row?.className).toMatch(/todoInProgress/)
    expect(container.textContent).toContain('Running tests')
    expect(container.textContent).not.toContain('Run tests')
  })

  it('renders the empty state as an empty wrapper for an empty list', () => {
    const { container } = render(() => <TodoList todos={[]} />)
    expect(container.querySelectorAll('[data-task-checkbox]')).toHaveLength(0)
  })

  // The label is one clipped line on every surface, so the tooltip is the only
  // route to a description and to a label the row had to clip.
  it('clips the label to one line', () => {
    const { container } = render(() => (
      <TodoList todos={[{ id: '1', content: 'A label wider than the sidebar', status: 'pending', activeForm: '' }]} />
    ))
    const text = container.querySelector(classSelector(styles.todoText))!
    expect(text.textContent).toBe('A label wider than the sidebar')
    // Token membership, not a substring: a future class whose own name merely
    // CONTAINS "clippedText" would satisfy a regex and prove nothing.
    expect(text.className.trim().split(/\s+/)).toContain(clippedText)
  })

  /**
   * The wrapping variant, for the chat transcript's tool card.
   *
   * The card stretches to the tile, so it has the room to show a whole to-do.
   * The sidebar section (~208px) does not, which is why `compact` is the
   * default. jsdom loads no stylesheet, so the modifier class on the root is
   * what a unit test can see; the rules it selects are asserted in the browser.
   */
  it('marks the list for wrapping only in the full variant', () => {
    const todos: TodoItem[] = [{ id: '1', content: 'Run tests', status: 'pending', activeForm: '' }]

    const compact = render(() => <TodoList todos={todos} />)
    expect(compact.container.firstElementChild!.className).not.toMatch(/todoListWrapping/)

    const full = render(() => <TodoList todos={todos} variant="full" />)
    expect(full.container.firstElementChild!.className).toMatch(/todoListWrapping/)
  })

  // Sorting lives in TodoList, so EVERY surface that renders a to-do list --
  // the sidebar section, the ThinkingIndicator popover, the chat cards -- reads
  // in the same order without each call site remembering to sort.
  it('renders in-progress first, then pending, then completed, then deleted', () => {
    const { container } = render(() => (
      <TodoList
        todos={[
          { id: '1', content: 'done', status: 'completed', activeForm: '' },
          { id: '2', content: 'gone', status: 'deleted', activeForm: '' },
          { id: '3', content: 'todo', status: 'pending', activeForm: '' },
          { id: '4', content: 'now', status: 'in_progress', activeForm: '' },
        ]}
      />
    ))
    const labels = [...container.querySelectorAll('[data-task-checkbox]')]
      .map(el => el.closest('div')?.parentElement?.textContent ?? '')
    expect(labels).toEqual(['now', 'todo', 'done', 'gone'])
  })

  it('keeps the oldest first inside one status group', () => {
    const { container } = render(() => (
      <TodoList
        todos={[
          { id: '1', content: 'first', status: 'pending', activeForm: '' },
          { id: '2', content: 'second', status: 'pending', activeForm: '' },
          { id: '3', content: 'third', status: 'pending', activeForm: '' },
        ]}
      />
    ))
    const labels = [...container.querySelectorAll('[data-task-checkbox]')]
      .map(el => el.closest('div')?.parentElement?.textContent ?? '')
    expect(labels).toEqual(['first', 'second', 'third'])
  })
})

/**
 * The tooltip on the label, which carries two things the row cannot show: a
 * description, and the rest of a label the row had to clip.
 *
 * The hover target is the LABEL, not the whole row. `showWhen` measures its
 * target, so the tooltip has to hang off the element that does the clipping.
 */
describe('todoList label tooltip', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  function labelOf(container: HTMLElement): HTMLElement {
    return container.querySelector<HTMLElement>(classSelector(styles.todoText))!
  }

  const hover = hoverForTooltip

  it('shows the label and its description together when a task has one', () => {
    const { container } = render(() => (
      <TodoList
        todos={[{ id: '1', content: 'Task with details', status: 'pending', activeForm: '', description: 'Long-form explanation' }]}
      />
    ))
    // Shown although the label fits: the description is the part the row
    // cannot show at all, so clipping is not the trigger for it.
    const tip = hover(labelOf(container))!
    expect(tip.textContent).toContain('Task with details')
    expect(tip.textContent).toContain('Long-form explanation')
  })

  it('uses the in-progress label, not the content, in that tooltip', () => {
    const { container } = render(() => (
      <TodoList
        todos={[{ id: '2', content: 'Run tests', status: 'in_progress', activeForm: 'Running tests', description: 'The whole suite' }]}
      />
    ))
    const tip = hover(labelOf(container))!
    expect(tip.textContent).toContain('Running tests')
    expect(tip.textContent).not.toContain('Run tests')
  })

  // An EMPTY description is an absent one. It must not force the tooltip open
  // on a label that fits, and it must not render a blank second line.
  it('treats an empty description as absent', () => {
    const { container } = render(() => (
      <TodoList todos={[{ id: '1', content: 'Short', status: 'pending', activeForm: '', description: '' }]} />
    ))
    const label = labelOf(container)
    stubFitting(label)
    expect(hover(label)).toBeNull()

    stubClipped(label)
    expect(hover(label)!.textContent).toBe('Short')
  })

  // Before the labels were clipped, a task with no description had no tooltip
  // at all. Now it gets one, but only once its label is actually clipped.
  it('gives a task without a description the full label once it is clipped', () => {
    const { container } = render(() => (
      <TodoList todos={[{ id: '1', content: 'A label far wider than this row', status: 'pending', activeForm: '' }]} />
    ))
    const label = labelOf(container)
    stubFitting(label)
    expect(hover(label)).toBeNull()

    stubClipped(label)
    expect(hover(label)!.textContent).toBe('A label far wider than this row')
  })

  /**
   * The read that genuinely was NOT tracked before this change.
   *
   * The old row chose between a wrapped and an unwrapped element with a plain
   * `todo.description ? … : …` in the `<For>` body, which runs once per row. An
   * in-place write to `description` therefore never added or removed the
   * tooltip. The description now reaches the label as a prop, so the read sits
   * inside a tracked scope and the tooltip follows it.
   */
  it('follows an in-place description change', () => {
    const [todos, setTodos] = createStore<TodoItem[]>([
      { id: '1', content: 'Run tests', status: 'pending', activeForm: '' },
    ])
    const { container } = render(() => <TodoList todos={todos} />)
    const label = labelOf(container)
    stubFitting(label)
    expect(hover(label)).toBeNull()

    setTodos(0, 'description', 'the whole suite')
    expect(hover(label)?.textContent).toContain('the whole suite')
    unhoverTooltip(label)

    setTodos(0, 'description', undefined)
    expect(hover(label)).toBeNull()
  })
})
