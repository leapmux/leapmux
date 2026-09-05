import type { BackgroundTaskItem } from '~/stores/chatBackgroundTasks'
/// <reference types="vitest/globals" />
import type { GoalAction, SessionGoal } from '~/stores/chatGoal'
import type { TodoItem } from '~/stores/chatTodos'
import { fireEvent, render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { motion } from '~/styles/tokens'
import { ThinkingIndicator } from './ThinkingIndicator'

// The token-count <Show> gates on BOTH `visible` and a positive estimate, so the
// count's presence is asserted against the visibility it is meant to track.
//
// Rendering visible=true normally drives the expand-tick rAF loop, which the
// synchronous test rAF stub would recurse into forever; `renderVisible` stubs
// rAF to a no-op for the render and passes paused=true so the compass sim and
// verb-rotation interval stay idle. Hidden cases render visible=false directly.
function renderVisible(thinkingTokens?: number) {
  const realRaf = globalThis.requestAnimationFrame
  globalThis.requestAnimationFrame = (() => 0) as typeof globalThis.requestAnimationFrame
  try {
    return render(() => (
      <ThinkingIndicator visible={true} paused={true} thinkingTokens={thinkingTokens} />
    ))
  }
  finally {
    globalThis.requestAnimationFrame = realRaf
  }
}

describe('thinking indicator token count', () => {
  it('renders the running thinking-token count when visible and positive', () => {
    const { getByText } = renderVisible(1234)
    expect(getByText('1.23k tokens')).toBeInTheDocument()
  })

  it('renders a sub-1k estimate verbatim, without a k suffix', () => {
    // 230 is the literal value from the original thinking_tokens payload.
    const { getByText } = renderVisible(230)
    expect(getByText('230 tokens')).toBeInTheDocument()
  })

  it('does not render the count while hidden, even with a positive estimate', () => {
    // The estimate's clear is event-driven, so a stale value can briefly outlive
    // the indicator; gating the count on `visible` keeps it from rendering (and
    // running its roll effects) inside a collapsed, invisible row.
    const { getByTestId, queryByText } = render(() => (
      <ThinkingIndicator visible={false} thinkingTokens={1234} />
    ))
    expect((getByTestId('thinking-indicator') as HTMLElement).style.display).toBe('none')
    expect(queryByText(/tokens/)).toBeNull()
  })

  it('renders nothing when the estimate is absent', () => {
    const { queryByText } = renderVisible(undefined)
    expect(queryByText(/tokens/)).toBeNull()
  })

  it('renders nothing when the estimate is zero', () => {
    const { queryByText } = renderVisible(0)
    expect(queryByText(/tokens/)).toBeNull()
  })

  it('keeps the count mounted through the row fade after hiding, then unmounts it', () => {
    vi.useFakeTimers()
    const realRaf = globalThis.requestAnimationFrame
    globalThis.requestAnimationFrame = (() => 0) as typeof globalThis.requestAnimationFrame
    try {
      const [visible, setVisible] = createSignal(true)
      const { queryByText } = render(() => (
        <ThinkingIndicator visible={visible()} paused={true} thinkingTokens={500} />
      ))
      expect(queryByText('500 tokens')).toBeInTheDocument()

      // The indicator hides (turn end). The count must NOT pop — it stays
      // mounted (frozen on its last value) to fade out with the collapsing row.
      setVisible(false)
      expect(queryByText('500 tokens')).toBeInTheDocument()

      // Once the wrapper's opacity fade (ROW_FADE_MS) elapses, it unmounts.
      vi.advanceTimersByTime(motion.medium)
      expect(queryByText('500 tokens')).toBeNull()
    }
    finally {
      globalThis.requestAnimationFrame = realRaf
      vi.useRealTimers()
    }
  })

  it('removes the collapsed wrapper from flex layout after the hide transition', () => {
    vi.useFakeTimers()
    const realRaf = globalThis.requestAnimationFrame
    globalThis.requestAnimationFrame = (() => 0) as typeof globalThis.requestAnimationFrame
    try {
      const [visible, setVisible] = createSignal(true)
      const { getByTestId } = render(() => (
        <ThinkingIndicator visible={visible()} paused={true} />
      ))
      const indicator = getByTestId('thinking-indicator') as HTMLElement
      expect(indicator.style.display).toBe('grid')

      setVisible(false)
      expect(indicator.style.display).toBe('grid')

      vi.advanceTimersByTime(motion.medium * 2)
      expect(indicator.style.display).toBe('none')
    }
    finally {
      globalThis.requestAnimationFrame = realRaf
      vi.useRealTimers()
    }
  })

  it('keeps the wrapper in layout when shown again before hide cleanup fires', () => {
    vi.useFakeTimers()
    const realRaf = globalThis.requestAnimationFrame
    globalThis.requestAnimationFrame = (() => 0) as typeof globalThis.requestAnimationFrame
    try {
      const [visible, setVisible] = createSignal(true)
      const { getByTestId } = render(() => (
        <ThinkingIndicator visible={visible()} paused={true} />
      ))
      const indicator = getByTestId('thinking-indicator') as HTMLElement

      setVisible(false)
      vi.advanceTimersByTime(motion.medium)
      setVisible(true)
      vi.advanceTimersByTime(motion.medium * 2)

      expect(indicator.style.display).toBe('grid')
    }
    finally {
      globalThis.requestAnimationFrame = realRaf
      vi.useRealTimers()
    }
  })
})

describe('thinking indicator chips', () => {
  const realRaf = globalThis.requestAnimationFrame

  function bgTask(over: Partial<BackgroundTaskItem> & { rowKey: string }): BackgroundTaskItem {
    return {
      kind: over.kind ?? 'subagent',
      title: over.title ?? 'T',
      activity: over.activity ?? '',
      status: over.status ?? 'running',
      ...over,
    }
  }

  /** N running rows, the shape that makes the chip read "N background tasks". */
  function running(n: number): BackgroundTaskItem[] {
    return Array.from({ length: n }, (_, i) => bgTask({ rowKey: `r${i}`, status: 'running' }))
  }

  function renderChips(props: {
    backgroundTasks?: BackgroundTaskItem[]
    onOpenSubagent?: (item: BackgroundTaskItem) => void
    todos?: TodoItem[]
    thinkingTokens?: number
    goal?: SessionGoal
    goalActions?: GoalAction[]
  }) {
    globalThis.requestAnimationFrame = (() => 0) as typeof globalThis.requestAnimationFrame
    try {
      return render(() => (
        <ThinkingIndicator visible={true} paused={true} {...props} />
      ))
    }
    finally {
      globalThis.requestAnimationFrame = realRaf
    }
  }

  it('labels the bg-tasks counter rather than showing a bare number', () => {
    const { getByTestId, queryByTestId } = renderChips({ backgroundTasks: running(2) })
    expect(getByTestId('thinking-bg-tasks-chip')).toHaveTextContent('2 background tasks')
    expect(queryByTestId('bg-tasks-popover')).toBeInTheDocument()
  })

  it('uses the singular noun for a single background task', () => {
    const { getByTestId } = renderChips({ backgroundTasks: running(1) })
    expect(getByTestId('thinking-bg-tasks-chip')).toHaveTextContent('1 background task')
  })

  it('hides the bg-tasks chip when the registry is empty', () => {
    const { queryByTestId } = renderChips({ backgroundTasks: [] })
    expect(queryByTestId('thinking-bg-tasks-chip')).toBeNull()
  })

  // A caller that has no registry to report at all (a tab whose root has none)
  // omits the prop entirely; the count must read 0, not throw.
  it('hides the bg-tasks chip when no registry is supplied', () => {
    const { queryByTestId } = renderChips({})
    expect(queryByTestId('thinking-bg-tasks-chip')).toBeNull()
  })

  // The count is the work IN PROGRESS, not the size of the registry. Finished
  // rows stay in the list -- the popover is where a user reads what a subagent
  // did -- so counting them left the chip saying "3 background tasks" beside a
  // registry where nothing ran.
  it('counts only pending and running rows', () => {
    const { getByTestId } = renderChips({
      backgroundTasks: [
        bgTask({ rowKey: 'a', status: 'running' }),
        bgTask({ rowKey: 'b', status: 'pending' }),
        bgTask({ rowKey: 'c', status: 'completed' }),
        bgTask({ rowKey: 'd', status: 'failed' }),
        bgTask({ rowKey: 'e', status: 'stopped' }),
        bgTask({ rowKey: 'f', status: 'interrupted' }),
      ],
    })
    expect(getByTestId('thinking-bg-tasks-chip')).toHaveTextContent('2 background tasks')
  })

  it('hides the bg-tasks chip once every row has finished', () => {
    const { queryByTestId } = renderChips({
      backgroundTasks: [
        bgTask({ rowKey: 'a', status: 'completed' }),
        bgTask({ rowKey: 'b', status: 'interrupted' }),
      ],
    })
    expect(queryByTestId('thinking-bg-tasks-chip')).toBeNull()
  })

  // The chip and the popover read ONE list, so a positive count can never open
  // an empty popover -- the failure a separate count prop invited.
  it('opens a popover holding every row the registry carries, finished included', () => {
    const { getByTestId } = renderChips({
      backgroundTasks: [
        bgTask({ rowKey: 'a', status: 'running', title: 'Still going' }),
        bgTask({ rowKey: 'b', status: 'completed', title: 'Already done' }),
      ],
    })
    const popover = getByTestId('bg-tasks-popover')
    expect(getByTestId('thinking-bg-tasks-chip')).toHaveTextContent('1 background task')
    expect(popover.querySelectorAll('[data-testid="bg-task-row"]')).toHaveLength(2)
  })

  // A menu closes on any click inside it, because every click in a menu is an
  // activation. This popover holds a kind tab bar, where a click is part of
  // READING the list -- so the popover would dismiss itself on the very click
  // that filters it, and the filter would be unusable.
  it('stays open when the user picks a kind tab', async () => {
    const { getByTestId } = renderChips({ backgroundTasks: running(2) })
    const popover = getByTestId('bg-tasks-popover')
    const hide = vi.spyOn(popover, 'hidePopover')

    await fireEvent.click(getByTestId('bg-task-filter-shell'))

    expect(hide).not.toHaveBeenCalled()
    expect(getByTestId('bg-task-filter-shell')).toHaveAttribute('aria-selected', 'true')
  })

  // Opening a subagent IS an activation: its tab takes over, so a popover left
  // open would cover the transcript the click just opened.
  it('closes when the user opens a subagent from a row', async () => {
    const onOpenSubagent = vi.fn()
    const { getByTestId, container } = renderChips({
      backgroundTasks: [bgTask({ rowKey: 'a', status: 'running', childAgentId: 'c1' })],
      onOpenSubagent,
    })
    const popover = getByTestId('bg-tasks-popover')
    const hide = vi.spyOn(popover, 'hidePopover')

    await fireEvent.click(container.querySelector('[data-testid="bg-task-row"]')!)

    expect(hide).toHaveBeenCalled()
    expect(onOpenSubagent).toHaveBeenCalledOnce()
    expect(onOpenSubagent.mock.calls[0][0].rowKey).toBe('a')
  })

  /**
   * The list renders a subagent row as a BUTTON on the strength of the handler
   * being present, so the popover must pass one only when its own host did.
   * Wrapping unconditionally gave a host that supplies nothing a row that looks
   * clickable, dismisses the popover, and does nothing.
   */
  it('renders a static row when the host supplies no way to open a subagent', () => {
    const { container } = renderChips({
      backgroundTasks: [bgTask({ rowKey: 'a', status: 'running', childAgentId: 'c1' })],
    })
    const row = container.querySelector('[data-testid="bg-task-row"]')!
    expect(row.tagName).toBe('DIV')
  })

  it('renders the todos counter as done/total plus a noun', () => {
    const todos: TodoItem[] = [
      { rowKey: 'a', content: 'a', status: 'completed', activeForm: '' },
      { rowKey: 'b', content: 'b', status: 'in_progress', activeForm: 'doing b' },
      { rowKey: 'c', content: 'c', status: 'pending', activeForm: '' },
    ]
    const { getByTestId } = renderChips({ todos })
    expect(getByTestId('thinking-todos-chip')).toHaveTextContent('1/3 to-dos')
    expect(getByTestId('todo-list-popover')).toBeInTheDocument()
  })

  it('uses the singular noun for a one-item to-do list', () => {
    const todos: TodoItem[] = [{ rowKey: 'a', content: 'a', status: 'pending', activeForm: '' }]
    const { getByTestId } = renderChips({ todos })
    expect(getByTestId('thinking-todos-chip')).toHaveTextContent('0/1 to-do')
  })

  it('hides the todos chip when all todos are deleted', () => {
    const todos: TodoItem[] = [
      { rowKey: 'a', content: 'a', status: 'deleted', activeForm: '' },
    ]
    const { queryByTestId } = renderChips({ todos })
    expect(queryByTestId('thinking-todos-chip')).toBeNull()
  })

  it('hides the todos chip when the list is empty', () => {
    const { queryByTestId } = renderChips({ todos: [] })
    expect(queryByTestId('thinking-todos-chip')).toBeNull()
  })

  // The row reads "<verb>... <background tasks> · <to-dos> · <tokens>": the
  // rotating verb leads, and the counters trail it, middot-separated. The verb
  // is outside the separator chain, so leading it adds no middot of its own.
  /**
   * The goal chip, which leads the counters.
   *
   * It is a CONVENIENCE, not the way a goal is reached: this whole indicator is
   * hidden unless the agent is ACTIVE and no permission prompt is pending, which
   * is exactly the paused / blocked / achieved states a goal needs acting on.
   * The sidebar section is the reachable surface -- see
   * shouldShowBackgroundTasksSection.
   */
  it('shows a goal chip naming the status, and hides it without a goal', () => {
    const goal: SessionGoal = { objective: 'every test passes', status: 'blocked' }
    const { getByTestId } = renderChips({ goal })
    expect(getByTestId('thinking-goal-chip').textContent).toBe('Goal: needs attention')
    expect(renderChips({}).queryByTestId('thinking-goal-chip')).toBeNull()
  })

  // A goal is not a background task, so it must not resurrect the chip that
  // counts them -- the two answer different questions.
  it('does not show the background-tasks chip for a goal alone', () => {
    const goal: SessionGoal = { objective: 'x', status: 'active' }
    const { queryByTestId } = renderChips({ goal })
    expect(queryByTestId('thinking-bg-tasks-chip')).toBeNull()
  })

  it('draws three separators when all four counters show', () => {
    const todos: TodoItem[] = [{ rowKey: 'a', content: 'a', status: 'pending', activeForm: '' }]
    const goal: SessionGoal = { objective: 'x', status: 'active' }
    const { getByTestId } = renderChips({ thinkingTokens: 500, backgroundTasks: running(2), todos, goal })
    const dots = (getByTestId('thinking-indicator').textContent ?? '').split('\u00B7').length - 1
    expect(dots).toBe(3)
  })

  it('orders the verb before the counters, separated by middots', () => {
    const todos: TodoItem[] = [{ rowKey: 'a', content: 'a', status: 'pending', activeForm: '' }]
    const { getByTestId, getByText } = renderChips({ thinkingTokens: 500, backgroundTasks: running(2), todos })
    // The odometer is aria-hidden; getByText finds the screen-reader copy.
    const tokens = getByText('500 tokens')
    const bg = getByTestId('thinking-bg-tasks-chip')
    const todo = getByTestId('thinking-todos-chip')
    const verb = getByTestId('thinking-verb')
    const before = (a: Element, b: Element) =>
      !!(a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING)
    expect(before(verb, bg)).toBe(true)
    expect(before(bg, todo)).toBe(true)
    expect(before(todo, tokens)).toBe(true)
    // Exactly two separators for three counters.
    const dots = (getByTestId('thinking-indicator').textContent ?? '').split('\u00B7').length - 1
    expect(dots).toBe(2)
  })

  // Tokens sits LAST in the chain now, so it owns the separator that its
  // predecessor would otherwise dangle when the middle counter is absent.
  it('draws one separator between the two counters that remain', () => {
    const { getByTestId } = renderChips({ thinkingTokens: 500, backgroundTasks: running(2) })
    const dots = (getByTestId('thinking-indicator').textContent ?? '').split('·').length - 1
    expect(dots).toBe(1)
  })

  // The last counter in the chain must still draw nothing when it is alone --
  // the case a "separator before every counter but the first" rule gets wrong.
  it('draws no separator when only the token count is present', () => {
    const { getByTestId } = renderChips({ thinkingTokens: 500 })
    expect(getByTestId('thinking-indicator').textContent).not.toContain('·')
  })

  // A missing neighbour must not leave a dangling separator.
  it('draws no separator when only one counter is present', () => {
    const { getByTestId } = renderChips({ backgroundTasks: running(2) })
    expect(getByTestId('thinking-indicator').textContent).not.toContain('\u00B7')
  })
})
