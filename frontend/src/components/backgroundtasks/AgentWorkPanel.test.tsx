import type { BackgroundTaskItem } from '~/stores/chatBackgroundTasks'
import type { SessionGoal } from '~/stores/chatGoal'
import { fireEvent, render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentWorkPanel } from './AgentWorkPanel'

function row(over: Partial<BackgroundTaskItem> & { rowKey: string }): BackgroundTaskItem {
  return {
    kind: over.kind ?? 'subagent',
    title: over.title ?? 'T',
    activity: over.activity ?? '',
    status: over.status ?? 'running',
    ...over,
  }
}

function goal(over: Partial<SessionGoal> = {}): SessionGoal {
  return { objective: 'every test passes', status: 'active', ...over }
}

function renderPanel(props: { tasks?: BackgroundTaskItem[], goal?: SessionGoal } = {}) {
  return render(() => (
    <AgentWorkPanel
      variant="sidebar"
      tasks={props.tasks ?? []}
      goal={props.goal}
      goalProgress={{}}
      goalActions={[]}
    />
  ))
}

function tab(container: HTMLElement, key: string): Element {
  return container.querySelector(`[data-testid="bg-task-filter-${key}"]`)!
}

describe('agentWorkPanel', () => {
  /**
   * The DOM contract the split had to preserve. The E2E specs and the sidebar
   * both select on these ids, so a decomposition that renamed them would break
   * suites that have nothing to do with the goal.
   */
  it('keeps the list root and the tab test ids the registry surfaces select on', () => {
    const { container } = renderPanel()
    expect(container.querySelector('[data-testid="bg-task-list"]')).not.toBeNull()
    expect(container.querySelector('[data-testid="bg-task-filter-tab-bar"]')).not.toBeNull()
    for (const key of ['all', 'subagent', 'shell', 'goal'])
      expect(tab(container, key)).not.toBeNull()
  })

  it('renders the tabs in order, with Goal last', () => {
    const { container } = renderPanel()
    const labels = [...container.querySelectorAll('[role="tab"]')].map(t => t.textContent)
    expect(labels).toEqual(['All', 'Subagents', 'Shell', 'Goal'])
  })

  // "All" means everything the agent is working on, which includes the
  // objective it is working toward.
  it('shows the goal card above the rows on the All tab', () => {
    const { container, getByTestId } = renderPanel({
      tasks: [row({ rowKey: 'a', title: 'Explore' })],
      goal: goal(),
    })
    expect(getByTestId('goal-card')).not.toBeNull()
    const panel = container.querySelector('[role="tabpanel"]')!
    expect(panel.textContent).toContain('every test passes')
    expect(panel.textContent).toContain('Explore')
  })

  it('shows the goal alone on the Goal tab, with no task rows', () => {
    const { container, getByTestId, queryByTestId } = renderPanel({
      tasks: [row({ rowKey: 'a', title: 'Explore' })],
      goal: goal(),
    })
    fireEvent.click(tab(container, 'goal'))
    expect(getByTestId('goal-card')).not.toBeNull()
    expect(queryByTestId('bg-task-row')).toBeNull()
  })

  it('hides the goal card on the kind tabs', () => {
    const { container, queryByTestId } = renderPanel({
      tasks: [row({ rowKey: 'a' })],
      goal: goal(),
    })
    fireEvent.click(tab(container, 'subagent'))
    expect(queryByTestId('goal-card')).toBeNull()
  })

  /**
   * The Goals tab is always present because it is where a goal gets SET -- its
   * empty state is a control, not dead weight, which is why it does not appear
   * only when a goal already exists.
   */
  it('offers the empty state on the Goal tab when there is no goal', () => {
    const { container, getByTestId } = renderPanel()
    fireEvent.click(tab(container, 'goal'))
    expect(getByTestId('goal-card-empty')).not.toBeNull()
  })

  // The goal must never enter the registry's kind union: doing so would enrol it
  // in countActiveBackgroundTasks, which feeds rootWorkState and would keep the
  // thinking indicator spinning for the goal's whole life.
  it('never renders the goal as a task row', () => {
    const { container, queryByTestId } = renderPanel({ goal: goal() })
    expect(queryByTestId('bg-task-row')).toBeNull()
    fireEvent.click(tab(container, 'all'))
    expect(queryByTestId('bg-task-row')).toBeNull()
  })

  it('gives each mount its own panel id and points its tabs at it', () => {
    const first = renderPanel()
    const second = renderPanel()
    const idOf = (c: HTMLElement) => c.querySelector('[role="tabpanel"]')!.id
    expect(idOf(first.container)).not.toBe(idOf(second.container))
    expect(tab(first.container, 'all').getAttribute('aria-controls')).toBe(idOf(first.container))
  })
})
