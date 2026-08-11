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

  // The clickable and static rows are built from one shared attribute bag, so
  // the registry attributes the E2E specs select on cannot drift between them.
  it('puts the same registry attributes on the clickable and the static row', () => {
    const { container } = render(() => (
      <BackgroundTaskList
        tasks={[
          row({ rowKey: 'agent', status: 'running', childAgentId: 'c1' }),
          row({ rowKey: 'shell', status: 'running', kind: 'shell' }),
        ]}
        onOpenSubagent={vi.fn()}
      />
    ))
    const rows = [...container.querySelectorAll('[data-testid="bg-task-row"]')]
    expect(rows.map(el => el.tagName)).toEqual(['BUTTON', 'DIV'])
    expect(rows.map(el => el.getAttribute('data-status'))).toEqual(['running', 'running'])
    expect(rows.map(el => el.getAttribute('data-kind'))).toEqual(['subagent', 'shell'])
    // Present on BOTH, empty when there is no child to open.
    expect(rows.map(el => el.getAttribute('data-child-agent-id'))).toEqual(['c1', ''])
  })

  // Oat's base button rule renders a <button> at var(--font-medium), and the
  // clickable row IS a button while the static row is a div. Both must carry
  // taskRow, which declares the normal weight, or an open subagent reads as
  // emphasized against the shell rows. jsdom loads no stylesheet, so the shared
  // class is the only part a unit test can see; E2E 170 asserts the weight that
  // the browser resolves.
  it('gives the clickable and the static row the same style classes', () => {
    const { container } = render(() => (
      <BackgroundTaskList
        tasks={[
          row({ rowKey: 'agent', status: 'running', childAgentId: 'c1' }),
          row({ rowKey: 'shell', status: 'running', kind: 'shell' }),
        ]}
        onOpenSubagent={vi.fn()}
      />
    ))
    const rows = [...container.querySelectorAll('[data-testid="bg-task-row"]')]
    const classesOf = (el: Element) => new Set(el.className.split(/\s+/).filter(Boolean))
    const clickable = classesOf(rows[0])
    const staticRow = classesOf(rows[1])
    expect(clickable.size).toBeGreaterThan(0)
    // The static row carries every class the clickable one does...
    expect([...clickable].filter(c => !staticRow.has(c))).toEqual([])
    // ...plus exactly one more, the cursor override.
    expect(staticRow.size).toBe(clickable.size + 1)
  })

  // The registry is already scoped to one root agent, so naming the parent on
  // every row was noise. Removed -- and a row that still carries a
  // parentAgentId must not resurrect it.
  it('never renders a "via <parent>" chip', () => {
    const { container } = render(() => (
      <BackgroundTaskList
        tasks={[row({ rowKey: 'agent', status: 'running', childAgentId: 'c1', parentAgentId: 'root-1' })]}
        onOpenSubagent={vi.fn()}
      />
    ))
    expect(container.textContent).not.toContain('via')
  })

  // Status is a COLOR on one constant dot, not a different glyph per state, so
  // the column reads as a status light instead of a set of shapes to learn.
  it('colors one status dot per state rather than swapping the glyph', () => {
    const statuses: BackgroundTaskItem['status'][] = [
      'pending',
      'running',
      'completed',
      'failed',
      'stopped',
      'interrupted',
    ]
    const { container } = render(() => (
      <BackgroundTaskList tasks={statuses.map(status => row({ rowKey: status, status }))} />
    ))
    const dots = [...container.querySelectorAll('[data-testid="bg-task-status-dot"]')]
    expect(dots).toHaveLength(statuses.length)
    // In progress, succeeded, and failed are three DISTINCT colors; the two
    // in-progress states share one, and a crash is colored like a failure.
    const cls = (i: number) => dots[i].className
    expect(cls(0)).toBe(cls(1)) // pending === running
    expect(cls(2)).not.toBe(cls(0)) // completed differs from in-progress
    expect(cls(3)).not.toBe(cls(0)) // failed differs from in-progress
    expect(cls(3)).not.toBe(cls(2)) // failed differs from completed
    expect(cls(5)).toBe(cls(3)) // interrupted is colored like a failure
    expect(cls(4)).not.toBe(cls(3)) // an explicit stop is not a failure
  })

  // Color alone cannot tell failed from interrupted, so the dot names its state.
  it('names the status on the dot for anyone who cannot use the color', () => {
    const { container } = render(() => (
      <BackgroundTaskList tasks={[row({ rowKey: 'a', status: 'interrupted' })]} />
    ))
    expect(container.querySelector('[aria-label="Interrupted"]')).not.toBeNull()
  })

  // Claude names a background shell's command as its `description`, which is
  // already the row's title, so echoing it below said the same thing twice.
  it('drops a secondary line that just repeats the title', () => {
    const { container } = render(() => (
      <BackgroundTaskList
        tasks={[row({ rowKey: 'sh', kind: 'shell', status: 'running', title: 'npm test', description: 'npm test' })]}
      />
    ))
    expect(container.textContent).toBe('npm test')
  })

  it('keeps a secondary line that adds something the title does not say', () => {
    const { container } = render(() => (
      <BackgroundTaskList
        tasks={[row({ rowKey: 'sh', kind: 'shell', status: 'running', title: 'npm test', activity: 'installing deps' })]}
      />
    ))
    expect(container.textContent).toContain('npm test')
    expect(container.textContent).toContain('installing deps')
  })
})
