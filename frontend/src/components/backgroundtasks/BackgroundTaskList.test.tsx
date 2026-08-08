import type { BackgroundTaskItem } from '~/stores/chatBackgroundTasks'
import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { BackgroundTaskList } from '~/components/backgroundtasks/BackgroundTaskList'

function row(over: Partial<BackgroundTaskItem> & { rowKey: string }): BackgroundTaskItem {
  return {
    kind: over.kind ?? 'subagent',
    title: over.title ?? 'T',
    activity: over.activity ?? '',
    status: over.status ?? 'running',
    ...over,
  }
}

describe('backgroundTaskList', () => {
  it('renders a status glyph + title + activity for a running subagent', () => {
    const { container } = render(() => (
      <BackgroundTaskList tasks={[row({ rowKey: 't1', title: 'Spawned agent', status: 'running', activity: 'running Bash', childAgentId: 'c1' })]} />
    ))
    const el = container.querySelector('[data-testid="bg-task-row"]') as HTMLElement
    expect(el).toBeTruthy()
    expect(el.dataset.status).toBe('running')
    expect(el.dataset.kind).toBe('subagent')
    expect(el.dataset.childAgentId).toBe('c1')
    expect(container.textContent).toContain('Spawned agent')
    expect(container.textContent).toContain('running Bash')
  })

  it('renders the end label for a terminal row instead of activity', () => {
    const { container } = render(() => (
      <BackgroundTaskList tasks={[row({ rowKey: 't1', status: 'failed' })]} />
    ))
    expect(container.textContent).toContain('Failed')
  })

  it('renders a group header when rows carry a groupKey', () => {
    const { container } = render(() => (
      <BackgroundTaskList tasks={[
        row({ rowKey: 'free', status: 'running' }),
        row({ rowKey: 'g1', status: 'running', groupKey: 'wf:x', groupLabel: 'my workflow' }),
      ]}
      />
    ))
    expect(container.textContent).toContain('my workflow')
  })

  it('fires onOpenSubagent only for subagent rows with a childAgentId', () => {
    const onOpen = vi.fn()
    const { container } = render(() => (
      <BackgroundTaskList
        tasks={[
          row({ rowKey: 'agent', status: 'running', childAgentId: 'c1' }),
          row({ rowKey: 'shell', status: 'running', kind: 'shell' }),
        ]}
        onOpenSubagent={onOpen}
      />
    ))
    const rows = container.querySelectorAll('[data-testid="bg-task-row"]')
    expect(rows).toHaveLength(2)
    // The subagent row is a button; the shell row is a div.
    const agentRow = rows[0] as HTMLButtonElement
    expect(agentRow.tagName).toBe('BUTTON')
    agentRow.click()
    expect(onOpen).toHaveBeenCalledOnce()
    expect(onOpen.mock.calls[0][0].rowKey).toBe('agent')

    const shellRow = rows[1] as HTMLElement
    expect(shellRow.tagName).toBe('DIV')
  })

  it('does not render a button when onOpenSubagent is absent', () => {
    const { container } = render(() => (
      <BackgroundTaskList tasks={[row({ rowKey: 'agent', status: 'running', childAgentId: 'c1' })]} />
    ))
    const el = container.querySelector('[data-testid="bg-task-row"]') as HTMLElement
    expect(el.tagName).toBe('DIV')
  })
})
