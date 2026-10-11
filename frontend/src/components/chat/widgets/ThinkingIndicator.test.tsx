import type { TodoItem } from '~/models/todo'
import type { BackgroundTaskItem } from '~/stores/chatBackgroundTasks'
/// <reference types="vitest/globals" />
import type { GoalAction, GoalProgress, SessionGoal } from '~/stores/chatGoal'
import { create } from '@bufbuild/protobuf'
import { fireEvent, render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { AgentGoalSchema, AgentGoalStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { protoGoalToStore } from '~/stores/chatGoal'
import { motion } from '~/styles/tokens'
import { ThinkingIndicator } from './ThinkingIndicator'

// The token count requires a positive estimate and a visible indicator.
//
// A visible render starts the expansion requestAnimationFrame loop.
// The synchronous test stub would call that loop recursively.
// renderVisible supplies a no-op animation handler and pauses the simulation and verb interval.
// Hidden cases render visible=false directly.
function renderVisible(thinkingTokens?: number, outputBytes?: number, outputBytesMinimum?: boolean) {
  const realRaf = globalThis.requestAnimationFrame
  globalThis.requestAnimationFrame = (() => 0) as typeof globalThis.requestAnimationFrame
  try {
    return render(() => (
      <ThinkingIndicator
        visible={true}
        paused={true}
        {...(thinkingTokens === undefined ? {} : { thinkingTokens })}
        {...(outputBytes === undefined ? {} : { outputBytes })}
        {...(outputBytesMinimum === undefined ? {} : { outputBytesMinimum })}
      />
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
    // An event clears the estimate, so its old value can briefly remain after the indicator hides.
    // The visible condition prevents the hidden row from displaying or updating that count.
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

  it('renders no output count when the byte count is absent or zero', () => {
    const { queryByTestId: queryAbsent } = renderVisible(undefined, undefined)
    expect(queryAbsent('thinking-output-count')).toBeNull()
    const { queryByTestId: queryZero } = renderVisible(undefined, 0)
    expect(queryZero('thinking-output-count')).toBeNull()
  })

  it('renders token and output counters together', () => {
    const { getByText } = renderVisible(230, 1536)
    expect(getByText('230 tokens')).toBeInTheDocument()
    expect(getByText('1.5 KB')).toBeInTheDocument()
  })

  it('marks provider-limited output as a minimum', () => {
    const { getByText } = renderVisible(undefined, 1536, true)
    expect(getByText('≥1.5 KB')).toBeInTheDocument()
  })

  it('keeps the count mounted through the row fade after hiding, then unmounts it', () => {
    vi.useFakeTimers()
    const realRaf = globalThis.requestAnimationFrame
    globalThis.requestAnimationFrame = (() => 0) as typeof globalThis.requestAnimationFrame
    try {
      const [visible, setVisible] = createSignal(true)
      const { queryByText } = render(() => (
        <ThinkingIndicator visible={visible()} paused={true} thinkingTokens={500} outputBytes={1536} />
      ))
      expect(queryByText('500 tokens')).toBeInTheDocument()
      expect(queryByText('1.5 KB')).toBeInTheDocument()

      // At turn end, retain the last count while the row fades.
      // Removing the count immediately would change the row before its fade ends.
      setVisible(false)
      expect(queryByText('500 tokens')).toBeInTheDocument()
      expect(queryByText('1.5 KB')).toBeInTheDocument()

      // Remove the count after the wrapper's ROW_FADE_MS opacity transition ends.
      vi.advanceTimersByTime(motion.medium)
      expect(queryByText('500 tokens')).toBeNull()
      expect(queryByText('1.5 KB')).toBeNull()
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

  /** Create N running rows for the background-task count. */
  function running(n: number): BackgroundTaskItem[] {
    return Array.from({ length: n }, (_, i) => bgTask({ rowKey: `r${i}`, status: 'running' }))
  }

  function renderChips(props: {
    backgroundTasks?: BackgroundTaskItem[]
    onOpenSubagent?: (item: BackgroundTaskItem) => void
    todos?: TodoItem[]
    thinkingTokens?: number
    goal?: SessionGoal
    goalProgress?: GoalProgress
    goalActions?: GoalAction[]
    onGoalAction?: (action: GoalAction) => void
    goalSupported?: boolean
  }) {
    // Separate the goal fields to construct GoalSurface below.
    // Read the plain fixture once rather than forwarding those fields as component props.
    // eslint-disable-next-line solid/reactivity -- The fixture is a plain object. Read it once.
    const { goal, goalProgress, goalActions, onGoalAction, goalSupported, ...rest } = props
    globalThis.requestAnimationFrame = (() => 0) as typeof globalThis.requestAnimationFrame
    try {
      return render(() => (
        <ThinkingIndicator
          visible={true}
          paused={true}
          {...rest}
          {...(goalSupported === true || goal !== undefined
            ? {
                goal: {
                  ...(goal === undefined ? {} : { current: goal }),
                  progress: goalProgress ?? {},
                  actions: goalActions ?? [],
                  ...(onGoalAction === undefined ? {} : { onAction: onGoalAction }),
                },
              }
            : {})}
        />
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

  // A caller with no registry can omit the prop.
  // The absent registry must report a zero count without throwing.
  it('hides the bg-tasks chip when no registry is supplied', () => {
    const { queryByTestId } = renderChips({})
    expect(queryByTestId('thinking-bg-tasks-chip')).toBeNull()
  })

  // Count pending and running work rather than every retained registry row.
  // Completed rows remain available for inspection in the popover.
  it('counts only pending and running rows', () => {
    const { getByTestId } = renderChips({
      backgroundTasks: [
        bgTask({ rowKey: 'a', status: 'running' }),
        bgTask({ rowKey: 'b', status: 'pending' }),
        bgTask({ rowKey: 'c', status: 'succeeded' }),
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
        bgTask({ rowKey: 'a', status: 'succeeded' }),
        bgTask({ rowKey: 'b', status: 'interrupted' }),
      ],
    })
    expect(queryByTestId('thinking-bg-tasks-chip')).toBeNull()
  })

  // The chip and popover read the same list.
  // A positive count must therefore open a populated popover.
  it('opens a popover holding every row the registry carries, finished included', () => {
    const { getByTestId } = renderChips({
      backgroundTasks: [
        bgTask({ rowKey: 'a', status: 'running', title: 'Still going' }),
        bgTask({ rowKey: 'b', status: 'succeeded', title: 'Already done' }),
      ],
    })
    const popover = getByTestId('bg-tasks-popover')
    expect(getByTestId('thinking-bg-tasks-chip')).toHaveTextContent('1 background task')
    expect(popover.querySelectorAll('[data-testid="bg-task-row"]')).toHaveLength(2)
  })

  // A menu treats an inside click as an activation and closes.
  // The kind filter only changes the displayed list.
  // The card popover must remain open so the user can read that list.
  it('stays open when the user picks a kind tab', async () => {
    const { getByTestId } = renderChips({ backgroundTasks: running(2) })
    const popover = getByTestId('bg-tasks-popover')
    const hide = vi.spyOn(popover, 'hidePopover')

    await fireEvent.click(getByTestId('bg-task-filter-shell'))

    expect(hide).not.toHaveBeenCalled()
    expect(getByTestId('bg-task-filter-shell')).toHaveAttribute('aria-selected', 'true')
  })

  // Opening a subagent selects its tab.
  // Close the popover so it does not cover the selected transcript.
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
    expect(onOpenSubagent.mock.calls[0]?.[0].rowKey).toBe('a')
  })

  /**
   * The list creates a button only when the host supplies an open handler.
   * An unconditional wrapper would create a clickable row that closes the popover but opens nothing.
   */
  it('renders a static row when the host supplies no way to open a subagent', () => {
    const { container } = renderChips({
      backgroundTasks: [bgTask({ rowKey: 'a', status: 'running', childAgentId: 'c1' })],
    })
    const row = container.querySelector('[data-testid="bg-task-row"]')!
    expect(row.tagName).toBe('DIV')
  })

  // Set opens a modal above this popover.
  // The card popover stays open on inside clicks, so the Set handler must close it explicitly.
  it('closes the to-dos popover when the user starts to set a goal', async () => {
    const onGoalAction = vi.fn()
    const { getByTestId } = renderChips({
      todos: [{ rowKey: 'a', content: 'Run tests', status: 'pending', activeForm: '' }],
      goal: { objective: 'Ship it', status: 'active' },
      goalActions: ['set', 'clear', 'pause'],
      onGoalAction,
    })
    const popover = getByTestId('todo-list-popover')
    const hide = vi.spyOn(popover, 'hidePopover')

    // Open the actual menu trigger before selecting an existing goal's action.
    // jsdom permits a click on hidden menu content.
    // That direct click would not prove that a browser user can open the menu.
    await fireEvent.click(getByTestId('goal-actions-trigger'))
    await fireEvent.click(getByTestId('goal-action-set'))

    expect(hide).toHaveBeenCalled()
    expect(onGoalAction).toHaveBeenCalledWith('set')
  })

  // The following actions change the goal within this panel:
  // - Clear.
  // - Pause.
  // - Resume.
  //
  // Keep the panel open so the user can see that result.
  it('keeps the to-dos popover open for an action that acts in place', async () => {
    const onGoalAction = vi.fn()
    const { getByTestId } = renderChips({
      todos: [{ rowKey: 'a', content: 'Run tests', status: 'pending', activeForm: '' }],
      goal: { objective: 'Ship it', status: 'active' },
      goalActions: ['set', 'clear', 'pause'],
      onGoalAction,
    })
    const popover = getByTestId('todo-list-popover')
    const hide = vi.spyOn(popover, 'hidePopover')

    await fireEvent.click(getByTestId('goal-actions-trigger'))
    await fireEvent.click(getByTestId('goal-action-pause'))

    expect(hide).not.toHaveBeenCalled()
    expect(onGoalAction).toHaveBeenCalledWith('pause')
  })

  // The popover wraps the action handler and preserves the rest of GoalSurface:
  // - The current goal.
  // - The reported counters.
  // - The supported actions.
  //
  // Omitting one field would remove content or disable controls.
  it('passes the rest of the goal surface through to the card', () => {
    const { getByTestId } = renderChips({
      todos: [{ rowKey: 'a', content: 'Run tests', status: 'pending', activeForm: '' }],
      goal: { objective: 'Keep the suite green', status: 'active' },
      goalProgress: { tokensUsed: 1200 },
      goalActions: ['set', 'clear', 'pause'],
      onGoalAction: vi.fn(),
    })
    const card = getByTestId('goal-card')
    expect(card).toHaveTextContent('Keep the suite green')
    expect(card).toHaveTextContent('1,200 tokens')
    fireEvent.click(getByTestId('goal-actions-trigger'))
    expect(getByTestId('goal-action-pause')).toBeInTheDocument()
  })

  // Preserve an absent host handler.
  // An unconditional wrapper would offer controls that have no action handler.
  it('offers no goal controls when the host supplies no handler', () => {
    const { queryByTestId } = renderChips({
      todos: [{ rowKey: 'a', content: 'Run tests', status: 'pending', activeForm: '' }],
      goal: { objective: 'Ship it', status: 'active' },
      goalActions: ['set', 'clear', 'pause'],
    })
    expect(queryByTestId('goal-action-pause')).toBeNull()
    expect(queryByTestId('goal-action-set')).toBeNull()
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

  it('reports an unknown goal through the actual chip and silent popover', () => {
    const current = protoGoalToStore(create(AgentGoalSchema, { objective: 'Keep the objective', status: AgentGoalStatus.UNKNOWN, statusDetail: 'future-state' }))
    const { getByTestId, container } = renderChips({ todos: [], goal: current, goalProgress: { iterations: 3 } })
    expect(getByTestId('goal-objective')).toHaveTextContent('Keep the objective')
    expect(getByTestId('goal-status-detail')).toHaveTextContent('future-state')
    expect(getByTestId('goal-progress')).toHaveTextContent('3 turns')
    expect(container.querySelectorAll('[role="status"][aria-live="polite"]')).toHaveLength(0)
    expect(getByTestId('thinking-todos-chip').textContent).toBe('Goal: unknown')
    expect(getByTestId('goal-status-dot')).toHaveAttribute('data-status', 'unknown')
  })

  /**
   * A stored goal or a to-do list can open the shared popover.
   * A goal-only agent still needs a transcript surface when its sidebar section is closed or elsewhere.
   */
  it('shows the chip for a goal with no to-do list, naming its status', () => {
    const { getByTestId } = renderChips({
      todos: [],
      goal: { objective: 'every test passes', status: 'blocked' },
    })
    // Display the goal status when the popover has no to-do list to count.
    expect(getByTestId('thinking-todos-chip').textContent).toBe('Goal: needs attention')
  })

  // An undeleted to-do row makes the chip display its to-do count.
  // The popover still displays the goal card.
  it('shows the to-do count when a list exists beside a goal', () => {
    const todos: TodoItem[] = [
      { rowKey: 'a', content: 'a', status: 'completed', activeForm: '' },
      { rowKey: 'b', content: 'b', status: 'pending', activeForm: '' },
    ]
    const { getByTestId } = renderChips({
      todos,
      goal: { objective: 'every test passes', status: 'active' },
    })
    expect(getByTestId('thinking-todos-chip').textContent).toBe('1/2 to-dos')
  })

  /**
   * The stored goal controls the chip's presence.
   * An empty capable surface supports Set but supplies no goal status to display.
   */
  it('hides the chip for a goal-capable agent that has no goal yet', () => {
    const { queryByTestId } = renderChips({ todos: [], goalSupported: true, goalActions: ['set'] })
    expect(queryByTestId('thinking-todos-chip')).toBeNull()
  })

  it('renders the goal card above the list in the to-dos popover', () => {
    const todos: TodoItem[] = [{ rowKey: 'a', content: 'Run tests', status: 'pending', activeForm: '' }]
    const { getByTestId, getByText } = renderChips({
      todos,
      goal: { objective: 'Ship it', status: 'active' },
    })
    const card = getByTestId('goal-card')
    const listItem = getByText('Run tests')
    expect(card.compareDocumentPosition(listItem) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('renders no goal card when the host passes no surface', () => {
    const todos: TodoItem[] = [{ rowKey: 'a', content: 'Run tests', status: 'pending', activeForm: '' }]
    const { queryByTestId } = renderChips({ todos })
    expect(queryByTestId('goal-card')).toBeNull()
  })

  /**
   * A capable agent can have no current goal.
   * The popover must retain its route to the first Set action.
   */
  it('offers the empty card and its Set route for a goal-capable agent with no goal', () => {
    const todos: TodoItem[] = [{ rowKey: 'a', content: 'Run tests', status: 'pending', activeForm: '' }]
    const { getByTestId } = renderChips({
      todos,
      goalSupported: true,
      goalActions: ['set'],
      onGoalAction: vi.fn(),
    })
    expect(getByTestId('goal-card-empty')).not.toBeNull()
    expect(getByTestId('goal-action-set')).not.toBeNull()
  })

  // The rotating verb precedes the counter row.
  // Separate adjacent counters with a middle dot, without adding one after the verb.
  it('draws two separators when all three counters show', () => {
    const todos: TodoItem[] = [{ rowKey: 'a', content: 'a', status: 'pending', activeForm: '' }]
    const { getByTestId } = renderChips({ thinkingTokens: 500, backgroundTasks: running(2), todos })
    const dots = (getByTestId('thinking-indicator').textContent ?? '').split('\u00B7').length - 1
    expect(dots).toBe(2)
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

  // Tokens is the last counter.
  // Its separator requires a visible preceding counter.
  it('draws one separator between the two counters that remain', () => {
    const { getByTestId } = renderChips({ thinkingTokens: 500, backgroundTasks: running(2) })
    const dots = (getByTestId('thinking-indicator').textContent ?? '').split('·').length - 1
    expect(dots).toBe(1)
  })

  // An isolated last counter displays no separator.
  it('draws no separator when only the token count is present', () => {
    const { getByTestId } = renderChips({ thinkingTokens: 500 })
    expect(getByTestId('thinking-indicator').textContent).not.toContain('·')
  })

  // An absent adjacent counter must not leave a separator.
  it('draws no separator when only one counter is present', () => {
    const { getByTestId } = renderChips({ backgroundTasks: running(2) })
    expect(getByTestId('thinking-indicator').textContent).not.toContain('\u00B7')
  })
})
