import type { BackgroundTaskItem } from '~/stores/chatBackgroundTasks'
import { fireEvent, render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { BackgroundTaskPanel } from './BackgroundTaskPanel'

function row(over: Partial<BackgroundTaskItem> & { rowKey: string }): BackgroundTaskItem {
  return {
    kind: over.kind ?? 'subagent',
    title: over.title ?? 'T',
    activity: over.activity ?? '',
    status: over.status ?? 'running',
    ...over,
  }
}

function renderPanel(tasks: BackgroundTaskItem[] = []) {
  return render(() => <BackgroundTaskPanel variant="sidebar" tasks={tasks} />)
}

function tab(container: HTMLElement, key: string): Element {
  return container.querySelector(`[data-testid="bg-task-filter-${key}"]`)!
}

describe('backgroundTaskPanel', () => {
  it('keeps the registry test ids', () => {
    const { container } = renderPanel()
    expect(container.querySelector('[data-testid="bg-task-list"]')).not.toBeNull()
    expect(container.querySelector('[data-testid="bg-task-filter-tab-bar"]')).not.toBeNull()
    for (const key of ['all', 'subagent', 'shell'])
      expect(tab(container, key)).not.toBeNull()
  })

  it('renders the three background-task tabs in order', () => {
    const { container } = renderPanel()
    const labels = [...container.querySelectorAll('[role="tab"]')].map(value => value.textContent)
    expect(labels).toEqual(['All', 'Subagents', 'Shell'])
  })

  it('filters the task list with the selected tab', () => {
    const { container } = renderPanel([
      row({ rowKey: 'agent', kind: 'subagent', title: 'Review' }),
      row({ rowKey: 'shell', kind: 'shell', title: 'Run tests' }),
    ])
    fireEvent.click(tab(container, 'shell'))
    expect(container.textContent).toContain('Run tests')
    expect(container.textContent).not.toContain('Review')
  })

  // FilterTabBar reconciles by reference. Replacing the task list must not
  // replace a focused tab button.
  it('keeps the same tab elements when the task list changes', () => {
    const [tasks, setTasks] = createSignal<BackgroundTaskItem[]>([
      row({ rowKey: 'a' }),
    ])
    const { container } = render(() => (
      <BackgroundTaskPanel variant="sidebar" tasks={tasks()} />
    ))
    const before = tab(container, 'all')

    setTasks([row({ rowKey: 'b', title: 'New task' })])

    expect(tab(container, 'all')).toBe(before)
    expect(container.textContent).toContain('New task')
  })

  it('gives each mount its own panel id and points its tabs at it', () => {
    const first = renderPanel()
    const second = renderPanel()
    const idOf = (container: HTMLElement) => container.querySelector('[role="tabpanel"]')!.id
    expect(idOf(first.container)).not.toBe(idOf(second.container))
    expect(tab(first.container, 'all').getAttribute('aria-controls')).toBe(idOf(first.container))
  })
})
