import type { BackgroundTaskItem } from '~/stores/chatBackgroundTasks'
import { fireEvent, render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { BackgroundTaskList } from '~/components/backgroundtasks/BackgroundTaskList'
import * as styles from '~/components/backgroundtasks/BackgroundTaskList.css'
import { clippedText } from '~/styles/shared.css'
import { hoverForTooltip, stubClipped, stubFitting } from '~/test-support/clipStub'
import { classSelector } from '~/test-support/composedClass'

function row(over: Partial<BackgroundTaskItem> & { rowKey: string }): BackgroundTaskItem {
  return {
    kind: over.kind ?? 'subagent',
    title: over.title ?? 'T',
    activity: over.activity ?? '',
    status: over.status ?? 'running',
    ...over,
  }
}

/**
 * Render the sidebar variant. `variant` is required, so every case states which
 * surface it is; the popover variant differs only in its own sizing classes,
 * which jsdom cannot see, so the behaviour cases all use the sidebar one.
 */
function renderList(props: {
  tasks: BackgroundTaskItem[]
  onOpenSubagent?: (item: BackgroundTaskItem) => void
}) {
  return render(() => (
    <BackgroundTaskList variant="sidebar" tasks={props.tasks} onOpenSubagent={props.onOpenSubagent} />
  ))
}

/** The rows region alone, without the kind tab bar's own labels. */
function rowsText(container: HTMLElement): string {
  return container.querySelector('[role="tabpanel"]')!.textContent ?? ''
}

/** The class tokens on the element, so a test asserts membership, not a substring. */
function classes(el: Element): string[] {
  return el.className.trim().split(/\s+/)
}

function titles(container: HTMLElement): HTMLElement[] {
  return [...container.querySelectorAll<HTMLElement>(classSelector(styles.taskTitle))]
}

function secondaries(container: HTMLElement): HTMLElement[] {
  return [...container.querySelectorAll<HTMLElement>(classSelector(styles.taskSecondary))]
}

describe('backgroundTaskList', () => {
  it('renders a status glyph + title + activity for a running subagent', () => {
    const { container } = renderList({
      tasks: [row({ rowKey: 't1', title: 'Spawned agent', status: 'running', activity: 'running Bash', childAgentId: 'c1' })],
    })
    const el = container.querySelector('[data-testid="bg-task-row"]') as HTMLElement
    expect(el).toBeTruthy()
    expect(el.dataset.status).toBe('running')
    expect(el.dataset.kind).toBe('subagent')
    expect(el.dataset.childAgentId).toBe('c1')
    expect(container.textContent).toContain('Spawned agent')
    expect(container.textContent).toContain('running Bash')
  })

  // The dot is a SIBLING of the title on the title line, not a child of it. A
  // child would count toward the title's own overflow, and the title is the
  // element whose clipping decides whether its tooltip appears at all.
  it('puts the status dot beside the title, on the title line', () => {
    const { container } = renderList({
      tasks: [row({ rowKey: 't1', title: 'Spawned agent', activity: 'running Bash' })],
    })
    const dot = container.querySelector('[data-testid="bg-task-status-dot"]')!
    const title = titles(container)[0]
    expect(title.contains(dot)).toBe(false)
    // Both sit on the title line...
    const titleRow = container.querySelector(classSelector(styles.titleRow))!
    expect(titleRow.contains(title)).toBe(true)
    expect(titleRow.contains(dot)).toBe(true)
    // ...and the dot follows the title, so it lands at the row's right end.
    expect(title.compareDocumentPosition(dot) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    // The secondary line is a separate block and never holds the dot.
    expect(secondaries(container)[0].contains(dot)).toBe(false)
  })

  // Code type follows the PROVIDER's claim, not the row's kind. A shell row
  // whose title is the model's prose -- Claude sends `description || command`,
  // so most of them are -- must not be set in the monospace face.
  it('sets a title in the monospace face only when it is a real command', () => {
    const { container } = renderList({
      tasks: [
        row({ rowKey: 'cmd', kind: 'shell', title: 'go test ./internal/worker/service/...', titleIsCommand: true }),
        row({ rowKey: 'prose', kind: 'shell', title: 'Run the worker service tests' }),
        row({ rowKey: 'ag', kind: 'subagent', title: 'Review the diff' }),
      ],
    })
    const titleOf = (text: string) =>
      titles(container).find(t => t.textContent?.includes(text))!
    expect(titleOf('go test').className).toContain(styles.taskTitleCommand)
    expect(titleOf('Run the worker').className).not.toContain(styles.taskTitleCommand)
    expect(titleOf('Review').className).not.toContain(styles.taskTitleCommand)
  })

  it('renders the end label for a finished row instead of activity', () => {
    const { container } = renderList({ tasks: [row({ rowKey: 't1', status: 'failed' })] })
    expect(container.textContent).toContain('Failed')
  })

  it('renders a group header when rows carry a groupKey', () => {
    const { container } = renderList({
      tasks: [
        row({ rowKey: 'free', status: 'running' }),
        row({ rowKey: 'g1', status: 'running', groupKey: 'wf:x', groupLabel: 'my workflow' }),
      ],
    })
    expect(container.textContent).toContain('my workflow')
  })

  it('fires onOpenSubagent only for subagent rows with a childAgentId', () => {
    const onOpen = vi.fn()
    const { container } = renderList({
      tasks: [
        row({ rowKey: 'agent', status: 'running', childAgentId: 'c1' }),
        row({ rowKey: 'shell', status: 'running', kind: 'shell' }),
      ],
      onOpenSubagent: onOpen,
    })
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
    const { container } = renderList({
      tasks: [row({ rowKey: 'agent', status: 'running', childAgentId: 'c1' })],
    })
    const el = container.querySelector('[data-testid="bg-task-row"]') as HTMLElement
    expect(el.tagName).toBe('DIV')
  })

  // The clickable and static rows are built from one shared attribute bag, so
  // the registry attributes the E2E specs select on cannot drift between them.
  it('puts the same registry attributes on the clickable and the static row', () => {
    const { container } = renderList({
      tasks: [
        row({ rowKey: 'agent', status: 'running', childAgentId: 'c1' }),
        row({ rowKey: 'shell', status: 'running', kind: 'shell' }),
      ],
      onOpenSubagent: vi.fn(),
    })
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
    const { container } = renderList({
      tasks: [
        row({ rowKey: 'agent', status: 'running', childAgentId: 'c1' }),
        row({ rowKey: 'shell', status: 'running', kind: 'shell' }),
      ],
      onOpenSubagent: vi.fn(),
    })
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

  // The registry is already scoped to one root agent, so a parent label on
  // every row was noise. Removed -- and a row that still carries a
  // parentAgentId must not resurrect it.
  it('never renders a "via <parent>" chip', () => {
    const { container } = renderList({
      tasks: [row({ rowKey: 'agent', status: 'running', childAgentId: 'c1', parentAgentId: 'root-1' })],
      onOpenSubagent: vi.fn(),
    })
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
    const { container } = renderList({ tasks: statuses.map(status => row({ rowKey: status, status })) })
    const dots = [...container.querySelectorAll('[data-testid="bg-task-status-dot"]')]
    expect(dots).toHaveLength(statuses.length)
    // Queued, running, succeeded, and failed are DISTINCT; a crash is colored
    // like a failure. Queued differs from running because running's only extra
    // signal is the pulse, which is suppressed under reduced motion -- so
    // sharing one dot made the two identical for the readers who cannot see it.
    const cls = (i: number) => dots[i].className
    expect(cls(0)).not.toBe(cls(1)) // pending differs from running
    expect(cls(2)).not.toBe(cls(0)) // completed differs from in-progress
    expect(cls(3)).not.toBe(cls(0)) // failed differs from in-progress
    expect(cls(3)).not.toBe(cls(2)) // failed differs from completed
    expect(cls(5)).toBe(cls(3)) // interrupted is colored like a failure
    expect(cls(4)).not.toBe(cls(3)) // an explicit stop is not a failure
    expect(cls(0)).not.toBe(cls(2)) // queued differs from completed
  })

  // Color alone cannot tell failed from interrupted, so the dot states its status.
  it('states the status on the dot for anyone who cannot use the color', () => {
    const { container } = renderList({ tasks: [row({ rowKey: 'a', status: 'interrupted' })] })
    expect(container.querySelector('[aria-label="Interrupted"]')).not.toBeNull()
  })

  // Claude sends a background shell's command as its `description`, which is
  // already the row's title, so echoing it below said the same thing twice.
  it('drops a secondary line that just repeats the title', () => {
    const { container } = renderList({
      tasks: [row({ rowKey: 'sh', kind: 'shell', status: 'running', title: 'npm test', description: 'npm test' })],
    })
    expect(rowsText(container)).toBe('npm test')
  })

  it('keeps a secondary line that adds something the title does not say', () => {
    const { container } = renderList({
      tasks: [row({ rowKey: 'sh', kind: 'shell', status: 'running', title: 'npm test', activity: 'installing deps' })],
    })
    expect(rowsText(container)).toContain('npm test')
    expect(rowsText(container)).toContain('installing deps')
  })
})

/**
 * The kind tabs. A registry that mixes subagents with background shells reads
 * as one undifferentiated list, and the two are looked for separately: a
 * subagent row is a transcript to open, a shell row is a command to check on.
 */
describe('backgroundTaskList kind tabs', () => {
  const mixed = [
    row({ rowKey: 'agent', kind: 'subagent', title: 'Review the diff', childAgentId: 'c1' }),
    row({ rowKey: 'shell', kind: 'shell', title: 'npm test' }),
  ]

  it('shows every kind on the All tab', () => {
    const { container, getByTestId } = renderList({ tasks: mixed })
    expect(getByTestId('bg-task-filter-all')).toHaveAttribute('aria-selected', 'true')
    expect(container.querySelectorAll('[data-testid="bg-task-row"]')).toHaveLength(2)
  })

  it('shows only subagent rows on the Subagents tab', () => {
    const { container, getByTestId } = renderList({ tasks: mixed })
    fireEvent.click(getByTestId('bg-task-filter-subagent'))
    const rows = [...container.querySelectorAll('[data-testid="bg-task-row"]')]
    expect(rows.map(el => el.getAttribute('data-kind'))).toEqual(['subagent'])
    expect(rowsText(container)).toContain('Review the diff')
    expect(rowsText(container)).not.toContain('npm test')
  })

  it('shows only shell rows on the Shell tab', () => {
    const { container, getByTestId } = renderList({ tasks: mixed })
    fireEvent.click(getByTestId('bg-task-filter-shell'))
    const rows = [...container.querySelectorAll('[data-testid="bg-task-row"]')]
    expect(rows.map(el => el.getAttribute('data-kind'))).toEqual(['shell'])
  })

  // An empty tab must say so. Rendering nothing leaves a blank box that reads
  // as a rendering fault rather than as "there are none of these".
  it('states that a tab with no rows is empty, per kind', () => {
    const { container, getByTestId } = renderList({ tasks: [row({ rowKey: 'agent', kind: 'subagent' })] })
    fireEvent.click(getByTestId('bg-task-filter-shell'))
    expect(container.querySelectorAll('[data-testid="bg-task-row"]')).toHaveLength(0)
    expect(rowsText(container)).toBe('No shell commands')

    fireEvent.click(getByTestId('bg-task-filter-subagent'))
    expect(container.querySelectorAll('[data-testid="bg-task-row"]')).toHaveLength(1)
  })

  it('states that an empty registry is empty on the All tab', () => {
    const { container } = renderList({ tasks: [] })
    expect(rowsText(container)).toBe('No background tasks')
  })

  // The tabs swap the region below them, so each one must point at the region
  // it actually swaps -- and two mounts on screen at once may not share an id.
  it('gives each mount its own panel id, and points its tabs at it', () => {
    const { getByTestId, container } = renderList({ tasks: mixed })
    const panelId = container.querySelector('[role="tabpanel"]')!.id
    expect(panelId).toBeTruthy()
    expect(getByTestId('bg-task-filter-all')).toHaveAttribute('aria-controls', panelId)

    const second = renderList({ tasks: mixed })
    expect(second.container.querySelector('[role="tabpanel"]')!.id).not.toBe(panelId)
  })

  // A group header belongs to the rows under it. Filtering to a kind whose rows
  // are all in one group must not leave the OTHER group's header behind.
  it('drops a group whose rows the filter removed', () => {
    const { container, getByTestId } = renderList({
      tasks: [
        row({ rowKey: 'a', kind: 'subagent', groupKey: 'wf:x', groupLabel: 'my workflow' }),
        row({ rowKey: 's', kind: 'shell', title: 'npm test' }),
      ],
    })
    expect(rowsText(container)).toContain('my workflow')
    fireEvent.click(getByTestId('bg-task-filter-shell'))
    expect(rowsText(container)).not.toContain('my workflow')
  })
})

/**
 * Every line of a row is held to ONE line and clipped, and gives its full text
 * back on hover. Wrapping made the sidebar section scroll sideways, because a
 * label with no break opportunity escaped the box and `rows` computes its
 * horizontal overflow to `auto`.
 */
describe('backgroundTaskList clipping', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  const hover = (el: Element): string | null => hoverForTooltip(el)?.textContent ?? null

  it('clips the title and the secondary line to one line', () => {
    const { container } = renderList({
      tasks: [row({ rowKey: 't1', title: 'Spawned agent', activity: 'running Bash' })],
    })
    // Token membership, not a substring: a future class whose own name merely
    // CONTAINS "clippedText" would satisfy a regex and prove nothing.
    expect(classes(titles(container)[0])).toContain(clippedText)
    expect(classes(secondaries(container)[0])).toContain(clippedText)
  })

  it('clips a group header, which had no wrapping rule at all', () => {
    const { container } = renderList({
      tasks: [row({ rowKey: 'g1', groupKey: 'wf:x', groupLabel: 'find-flaky-tests-and-fix-them' })],
    })
    const header = container.querySelector(classSelector(styles.groupHeader))!
    expect(header.textContent).toBe('find-flaky-tests-and-fix-them')
    expect(classes(header)).toContain(clippedText)
  })

  // The header had no tooltip at all before it was clipped, so the hover route
  // is the whole of what replaced a label the reader could simply see.
  it('gives the full group label on hover once the header is clipped', () => {
    const label = 'find-flaky-tests-and-fix-them-across-every-package'
    const { container } = renderList({
      tasks: [row({ rowKey: 'g1', groupKey: 'wf:x', groupLabel: label })],
    })
    const header = container.querySelector<HTMLElement>(classSelector(styles.groupHeader))!
    stubClipped(header)
    expect(hover(header)).toBe(label)
  })

  it('gives the full title on hover once the title is clipped', () => {
    const long = 'go test ./internal/worker/service/... -run TestEverything -count=1'
    const { container } = renderList({
      tasks: [row({ rowKey: 'cmd', kind: 'shell', title: long, titleIsCommand: true })],
    })
    const title = titles(container)[0]
    stubClipped(title)
    expect(hover(title)).toBe(long)
  })

  it('shows no title tooltip while the title fits', () => {
    const { container } = renderList({ tasks: [row({ rowKey: 't1', title: 'Short' })] })
    const title = titles(container)[0]
    stubFitting(title)
    expect(hover(title)).toBeNull()
  })

  it('gives the full activity on hover once the secondary line is clipped', () => {
    const activity = 'Running Bash(git log --oneline --format=%H%x09%s -n 200)'
    const { container } = renderList({
      tasks: [row({ rowKey: 't1', title: 'Spawned agent', status: 'running', activity })],
    })
    const secondary = secondaries(container)[0]
    stubClipped(secondary)
    expect(hover(secondary)).toBe(activity)
  })

  // A finished status carries an explanation the label cannot: "Interrupted"
  // does not say that a worker restart cut the task off. The explanation is
  // ADDED to the label, never put in its place, so a label that clips keeps its
  // own route back. It shows while the label fits too, because it carries what
  // the label cannot.
  it('adds an explanation to a finished status without losing its label', () => {
    const { container } = renderList({ tasks: [row({ rowKey: 't1', status: 'interrupted' })] })
    const secondary = secondaries(container)[0]
    expect(secondary.textContent).toBe('Interrupted')
    const tip = hover(secondary)
    expect(tip).toContain('Interrupted')
    expect(tip).toContain('stopped by a worker restart')
  })

  // The label stays reachable even when the row carries an explanation AND the
  // label is too long for its box -- the case the previous shape lost.
  it('keeps a clipped label reachable beside its explanation', () => {
    const { container } = renderList({ tasks: [row({ rowKey: 't1', status: 'interrupted' })] })
    const secondary = secondaries(container)[0]
    stubClipped(secondary)
    const tip = hover(secondary)
    expect(tip).toContain('Interrupted')
    expect(tip).toContain('stopped by a worker restart')
  })

  // ...and a finished status with no explanation falls back to the clipped
  // behaviour, rather than losing its tooltip entirely.
  it('falls back to the label for a finished status with no explanation', () => {
    const { container } = renderList({ tasks: [row({ rowKey: 't1', status: 'failed' })] })
    const secondary = secondaries(container)[0]
    expect(secondary.textContent).toBe('Failed')
    stubFitting(secondary)
    expect(hover(secondary)).toBeNull()
    stubClipped(secondary)
    expect(hover(secondary)).toBe('Failed')
  })
})

/**
 * A registry that could not be LOADED must not read as an empty one.
 *
 * The sidebar section is hidden when the registry is empty, so "no answer"
 * rendering as "no tasks" removed the whole section from the screen. A worker
 * database missing a column did exactly that: the only trace was a warn log.
 */
describe('backgroundTaskList load failure', () => {
  function renderFailed(tasks: BackgroundTaskItem[]) {
    return render(() => (
      <BackgroundTaskList variant="sidebar" tasks={tasks} loadFailed />
    ))
  }

  it('says the load failed instead of saying there are none', () => {
    const { container, queryByTestId } = renderFailed([])
    expect(rowsText(container)).toContain('Could not load background tasks')
    expect(rowsText(container)).not.toContain('No background tasks')
    expect(queryByTestId('bg-task-load-failed')).not.toBeNull()
    expect(queryByTestId('bg-task-empty')).toBeNull()
  })

  it('marks the empty box as an emptiness when nothing failed', () => {
    const { queryByTestId } = renderList({ tasks: [] })
    expect(queryByTestId('bg-task-empty')).not.toBeNull()
    expect(queryByTestId('bg-task-load-failed')).toBeNull()
  })

  // A failure that still has rows to show (a stale registry the client kept)
  // renders the rows: the message stands in for MISSING content, never over it.
  it('still renders the rows it has', () => {
    const { container, queryByTestId } = renderFailed([
      row({ rowKey: 'a', title: 'Review the diff' }),
    ])
    expect(container.querySelectorAll('[data-testid="bg-task-row"]')).toHaveLength(1)
    expect(queryByTestId('bg-task-load-failed')).toBeNull()
  })

  // With NOTHING to show, the failure is the right answer on every tab: the
  // registry could not be read, so no kind of row can be claimed to be absent.
  it('overrides the per-kind empty message on every tab when it has no rows', () => {
    const { container, getByTestId } = renderFailed([])
    fireEvent.click(getByTestId('bg-task-filter-shell'))
    expect(rowsText(container)).toContain('Could not load background tasks')
  })

  /**
   * ...but a failure that still HAS rows must not claim the registry is
   * unreadable on a tab that is merely empty of its own kind.
   *
   * A failed load leaves the rows it already had (`applyLatestPage` records the
   * failure without wiping them), so this state is reachable: the user sees two
   * subagents on All, clicks Shell, and would be told the worker could not be
   * read -- about a registry the same mount is showing two clicks away.
   */
  it('keeps the per-kind empty message on a tab whose kind has no rows', () => {
    const { container, queryByTestId, getByTestId } = renderFailed([
      row({ rowKey: 'a', title: 'Review the diff', kind: 'subagent' }),
    ])
    fireEvent.click(getByTestId('bg-task-filter-shell'))

    expect(rowsText(container)).toContain('No shell commands')
    expect(rowsText(container)).not.toContain('Could not load background tasks')
    expect(queryByTestId('bg-task-empty')).not.toBeNull()
    expect(queryByTestId('bg-task-load-failed')).toBeNull()
  })
})
