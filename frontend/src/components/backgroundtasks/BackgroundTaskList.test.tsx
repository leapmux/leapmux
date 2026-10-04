import type { BackgroundTaskItem as ProtoBackgroundTaskItem } from '~/generated/proto/leapmux/v1/agent_pb'
import type { BackgroundTaskItem } from '~/stores/chatBackgroundTasks'
import { fireEvent, render } from '@solidjs/testing-library'
import { createStore } from 'solid-js/store'
import { describe, expect, it, vi } from 'vitest'
import * as styles from '~/components/backgroundtasks/BackgroundTaskList.css'
import { BackgroundTaskPanel } from '~/components/backgroundtasks/BackgroundTaskPanel'
import { BackgroundTaskKind, BackgroundTaskStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { createBackgroundTaskStore } from '~/stores/chatBackgroundTaskStore'
import { clippedText } from '~/styles/shared.css'
import * as statusDotStyles from '~/styles/statusDot.css'
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

/** One wire row, for the cases that drive the real store rather than a literal list. */
function protoTask(id: string, title: string, activeForm: string): ProtoBackgroundTaskItem {
  return {
    id,
    kind: BackgroundTaskKind.SUBAGENT,
    status: BackgroundTaskStatus.RUNNING,
    title,
    activeForm,
    childAgentId: '',
    parentAgentId: '',
    groupKey: '',
    groupLabel: '',
    description: '',
    createdAt: '',
    updatedAt: '',
    endedAt: '',
  } as ProtoBackgroundTaskItem
}

/**
 * Render rows through their actual panel host.
 * The panel owns the root, tabs, and tabpanel region that these tests inspect.
 * Preserve that DOM structure and its test IDs.
 * The sidebar and popover variants differ only in sizing classes that jsdom cannot measure.
 */
function renderList(props: {
  tasks: BackgroundTaskItem[]
  onOpenSubagent?: (item: BackgroundTaskItem) => void
  loadFailed?: boolean
}) {
  return render(() => (
    <BackgroundTaskPanel
      variant="sidebar"
      tasks={props.tasks}
      {...(props.loadFailed !== undefined ? { loadFailed: props.loadFailed } : {})}
      {...(props.onOpenSubagent !== undefined ? { onOpenSubagent: props.onOpenSubagent } : {})}
    />
  ))
}

/**
 * The task region alone, without the tab labels.
 */
function rowsText(container: HTMLElement): string {
  return container.querySelector('[role="tabpanel"]')?.textContent ?? ''
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

describe('BackgroundTaskList', () => {
  it.each([
    ['completed', 'Completed'],
    ['failed', 'Failed'],
    ['stopped', 'Stopped'],
    ['interrupted', 'Interrupted'],
  ] as const)('shows the final %s outcome without a transcript divider', (status, label) => {
    const { container } = renderList({
      tasks: [row({ rowKey: 'native-child', title: 'Native child', status, childAgentId: 'child-1' })],
      onOpenSubagent: vi.fn(),
    })
    const child = container.querySelector('[data-testid="bg-task-row"]')
    expect(child?.getAttribute('data-status')).toBe(status)
    expect(child?.getAttribute('data-child-agent-id')).toBe('child-1')
    expect(child?.querySelector(`[aria-label="${label}"]`)).not.toBeNull()
    expect(child?.textContent).toContain('Native child')
  })

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

  // The status dot sits beside the title.
  // A dot inside the title would affect its overflow and therefore its clipping tooltip.
  it('puts the status dot beside the title, on the title line', () => {
    const { container } = renderList({
      tasks: [row({ rowKey: 't1', title: 'Spawned agent', activity: 'running Bash' })],
    })
    const dot = container.querySelector('[data-testid="bg-task-status-dot"]')!
    const title = titles(container)[0]!
    expect(title.contains(dot)).toBe(false)
    // Both elements share the title line.
    const titleRow = container.querySelector(classSelector(styles.titleRow))!
    expect(titleRow.contains(title)).toBe(true)
    expect(titleRow.contains(dot)).toBe(true)
    // The dot follows the title at the row's right edge.
    expect(title.compareDocumentPosition(dot) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    // The secondary line is a separate block and never holds the dot.
    expect(secondaries(container)[0]?.contains(dot)).toBe(false)
  })

  // Provider metadata alone selects monospace titles.
  // Claude can supply a prose description for a shell row, so shell kind cannot decide.
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

  // The Worker preserves usable native row keys exactly.
  // The browser cleans their display labels without merging distinct provider identities.
  describe('the label falls back to a CLEANED row key', () => {
    const labelOf = (container: HTMLElement) => titles(container)[0]?.textContent

    // Cursor's native toolCallId can contain a newline. The display label must remove it.
    it('folds a newline the provider put in the key', () => {
      const { container } = renderList({
        tasks: [row({ rowKey: 'call-abc\nfc-def', title: '' })],
      })
      expect(labelOf(container)).toBe('call-abc fc-def')
    })

    it('strips a bidirectional override, which reorders what the reader sees', () => {
      const { container } = renderList({
        tasks: [row({ rowKey: 'call-‮abc', title: '' })],
      })
      expect(labelOf(container)).toBe('call-abc')
    })

    it('leaves an ordinary key alone', () => {
      const { container } = renderList({ tasks: [row({ rowKey: 'toolu_01A2b3', title: '' })] })
      expect(labelOf(container)).toBe('toolu_01A2b3')
    })

    // Clean each candidate before the fallback reads it.
    // An invisible description must select the key instead of creating a blank label.
    it('falls through a candidate that cleans to nothing', () => {
      const { container } = renderList({
        tasks: [row({ rowKey: 'call-abc', title: '', description: '​​' })],
      })
      expect(labelOf(container)).toBe('call-abc')
    })

    // A valid identity can contain only invisible bidirectional characters.
    // When cleanName empties every candidate, the fallback must still supply a visible title.
    it('names the row Untitled when every candidate cleans to nothing', () => {
      const { container } = renderList({
        tasks: [row({ rowKey: '\u202E\u202E', title: '', description: '\u200B' })],
      })
      expect(labelOf(container)).toBe('Untitled')
    })

    // The Worker already cleans the title. The browser cleaner must preserve that value.
    it('passes a worker-cleaned title through unchanged', () => {
      const { container } = renderList({
        tasks: [row({ rowKey: 'k', title: 'npm test --grep "$FOO"' })],
      })
      expect(labelOf(container)).toBe('npm test --grep "$FOO"')
    })

    // Compare cleaned text on both lines before suppressing a repeated command.
    // Claude can supply the same command as title and description, including repeated spaces.
    it('suppresses a description that differs from the title only by a whitespace run', () => {
      const { container } = renderList({
        tasks: [row({
          rowKey: 'k',
          title: 'npm test  --grep "$FOO"',
          description: 'npm test  --grep "$FOO"',
        })],
      })
      expect(labelOf(container)).toBe('npm test --grep "$FOO"')
      expect(secondaries(container)).toHaveLength(0)
    })

    // Providers can also include a newline in the repeated description.
    it('suppresses a description that differs from the title only by a newline', () => {
      const { container } = renderList({
        tasks: [row({ rowKey: 'k', title: 'build\nand test', description: 'build\nand test' })],
      })
      expect(secondaries(container)).toHaveLength(0)
    })

    // Keep a second line that differs from the title.
    it('still shows a description that differs from the title', () => {
      const { container } = renderList({
        tasks: [row({ rowKey: 'k', title: 'Run the suite', description: 'npm test' })],
      })
      expect(secondaries(container).map(el => el.textContent)).toEqual(['npm test'])
    })

    // Raw activity and description text can contain bidirectional controls.
    // Clean them before display and comparison.
    it('cleans the second line it does show', () => {
      const { container } = renderList({
        tasks: [row({ rowKey: 'k', title: 'Run the suite', activity: 'step\u202Eone' })],
      })
      expect(secondaries(container).map(el => el.textContent)).toEqual(['stepone'])
    })
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
    expect(onOpen.mock.calls[0]?.[0]?.rowKey).toBe('agent')

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

  // Static and clickable rows share the same registry attributes for browser selection.
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
    // Both rows carry this attribute. A row without a child uses an empty value.
    expect(rows.map(el => el.getAttribute('data-child-agent-id'))).toEqual(['c1', ''])
  })

  // Oat gives buttons medium font weight. taskRow restores the normal weight for clickable and static rows.
  // jsdom cannot measure stylesheets, so this test checks the shared class.
  // The browser component test checks the resolved font weight.
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
    const clickable = classesOf(rows[0]!)
    const staticRow = classesOf(rows[1]!)
    expect(clickable.size).toBeGreaterThan(0)
    // The static row preserves every shared class.
    expect([...clickable].filter(c => !staticRow.has(c))).toEqual([])
    // Its one additional class overrides the cursor.
    expect(staticRow.size).toBe(clickable.size + 1)
  })

  // The registry already belongs to one root agent.
  // A retained parentAgentId must not add a redundant parent label to each row.
  it('never renders a "via <parent>" chip', () => {
    const { container } = renderList({
      tasks: [row({ rowKey: 'agent', status: 'running', childAgentId: 'c1', parentAgentId: 'root-1' })],
      onOpenSubagent: vi.fn(),
    })
    expect(container.textContent).not.toContain('via')
  })

  // One retained dot exposes each status through its color, shape, and accessible label.
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
    // Each status remains distinct. An interrupted task uses the failure color.
    // Reduced motion removes the active pulse, so the pending ring must still differ from the active dot.
    const cls = (i: number) => dots[i]?.className ?? ''
    expect(cls(0)).not.toBe(cls(1)) // pending differs from running
    expect(cls(2)).not.toBe(cls(0)) // completed differs from in-progress
    expect(cls(3)).not.toBe(cls(0)) // failed differs from in-progress
    expect(cls(3)).not.toBe(cls(2)) // failed differs from completed
    expect(cls(5)).toBe(cls(3)) // interrupted is colored like a failure
    expect(cls(4)).not.toBe(cls(3)) // an explicit stop is not a failure
    expect(cls(0)).not.toBe(cls(2)) // queued differs from completed
  })

  // The accessible label distinguishes failed and interrupted states that share a color.
  it('states the status on the dot for anyone who cannot use the color', () => {
    const { container } = renderList({ tasks: [row({ rowKey: 'a', status: 'interrupted' })] })
    expect(container.querySelector('[aria-label="Interrupted"]')).not.toBeNull()
  })

  // Claude can repeat a background shell command in its description. Show that text once.
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
 * Kind tabs separate a mixed registry into its native task categories.
 * Subagents open transcripts. Shell rows report commands. Workflow rows report workflow runs.
 */
describe('BackgroundTaskList kind tabs', () => {
  const mixed = [
    row({ rowKey: 'agent', kind: 'subagent', title: 'Review the diff', childAgentId: 'c1' }),
    row({ rowKey: 'shell', kind: 'shell', title: 'npm test' }),
    row({ rowKey: 'workflow', kind: 'workflow', title: 'Review pipeline' }),
    row({ rowKey: 'future', kind: 'unknown', rawKind: 99, title: 'Future task', childAgentId: 'c2' }),
  ]

  it('shows every kind on the All tab', () => {
    const { container, getByTestId } = renderList({ tasks: mixed })
    expect(getByTestId('bg-task-filter-all')).toHaveAttribute('aria-selected', 'true')
    expect(container.querySelectorAll('[data-testid="bg-task-row"]')).toHaveLength(4)
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

  it('shows only workflow rows on the Workflows tab', () => {
    const { container, getByTestId } = renderList({ tasks: mixed })
    fireEvent.click(getByTestId('bg-task-filter-workflow'))
    const rows = [...container.querySelectorAll('[data-testid="bg-task-row"]')]
    expect(rows.map(el => el.getAttribute('data-kind'))).toEqual(['workflow'])
    expect(rowsText(container)).toContain('Review pipeline')
    expect(rows[0]?.tagName).toBe('DIV')
    expect(rows[0]?.querySelector('svg.lucide-workflow')).not.toBeNull()
  })

  it('keeps an unknown kind neutral and static on the All tab', () => {
    const { container, queryByTestId } = renderList({ tasks: mixed })
    const unknown = container.querySelector('[data-kind="unknown"]')
    expect(unknown?.tagName).toBe('DIV')
    expect(unknown?.querySelector('svg')).not.toBeNull()
    expect(unknown?.querySelector('svg.lucide-bot')).toBeNull()
    expect(queryByTestId('bg-task-filter-unknown')).toBeNull()
  })

  // An empty tab must state that it contains no rows. A blank region would look like a rendering failure.
  it('states that a tab with no rows is empty, per kind', () => {
    const { container, getByTestId } = renderList({ tasks: [row({ rowKey: 'agent', kind: 'subagent' })] })
    fireEvent.click(getByTestId('bg-task-filter-shell'))
    expect(container.querySelectorAll('[data-testid="bg-task-row"]')).toHaveLength(0)
    expect(rowsText(container)).toBe('No shell commands')

    fireEvent.click(getByTestId('bg-task-filter-subagent'))
    expect(container.querySelectorAll('[data-testid="bg-task-row"]')).toHaveLength(1)

    fireEvent.click(getByTestId('bg-task-filter-workflow'))
    expect(container.querySelectorAll('[data-testid="bg-task-row"]')).toHaveLength(0)
    expect(rowsText(container)).toBe('No workflows')
  })

  it('states that an empty registry is empty on the All tab', () => {
    const { container } = renderList({ tasks: [] })
    expect(rowsText(container)).toBe('No background tasks')
  })

  // Each tab must identify its own tabpanel. Two simultaneous mounts must not share that ID.
  it('gives each mount its own panel id, and points its tabs at it', () => {
    const { getByTestId, container } = renderList({ tasks: mixed })
    const panelId = container.querySelector('[role="tabpanel"]')!.id
    expect(panelId).toBeTruthy()
    expect(getByTestId('bg-task-filter-all')).toHaveAttribute('aria-controls', panelId)

    const second = renderList({ tasks: mixed })
    expect(second.container.querySelector('[role="tabpanel"]')!.id).not.toBe(panelId)
  })

  // A kind filter must remove group headers whose rows it removes.
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
 * Clip each row line and expose its complete text on hover.
 * An unbroken wrapped label can exceed the sidebar width and create horizontal scrolling.
 */
describe('BackgroundTaskList clipping', () => {
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
    // Require the exact class token. A longer class containing that text cannot satisfy the assertion.
    expect(classes(titles(container)[0]!)).toContain(clippedText)
    expect(classes(secondaries(container)[0]!)).toContain(clippedText)
  })

  it('clips a group header without wrapping its label', () => {
    const { container } = renderList({
      tasks: [row({ rowKey: 'g1', groupKey: 'wf:x', groupLabel: 'find-flaky-tests-and-fix-them' })],
    })
    const header = container.querySelector(classSelector(styles.groupHeader))!
    expect(header.textContent).toBe('find-flaky-tests-and-fix-them')
    expect(classes(header)).toContain(clippedText)
  })

  // A clipped header needs a hover tooltip that exposes its complete label.
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
    const title = titles(container)[0]!
    stubClipped(title)
    expect(hover(title)).toBe(long)
  })

  it('shows no title tooltip while the title fits', () => {
    const { container } = renderList({ tasks: [row({ rowKey: 't1', title: 'Short' })] })
    const title = titles(container)[0]!
    stubFitting(title)
    expect(hover(title)).toBeNull()
  })

  it('gives the full activity on hover once the secondary line is clipped', () => {
    const activity = 'Running Bash(git log --oneline --format=%H%x09%s -n 200)'
    const { container } = renderList({
      tasks: [row({ rowKey: 't1', title: 'Spawned agent', status: 'running', activity })],
    })
    const secondary = secondaries(container)[0]!
    stubClipped(secondary)
    expect(hover(secondary)).toBe(activity)
  })

  // An Interrupted label omits the process restart cause.
  // Add that explanation below the label without replacing the complete clipped text.
  // Keep the explanation available when the label fits too.
  it('adds an explanation to a finished status without losing its label', () => {
    const { container } = renderList({ tasks: [row({ rowKey: 't1', status: 'interrupted' })] })
    const secondary = secondaries(container)[0]!
    expect(secondary.textContent).toBe('Interrupted')
    const tip = hover(secondary)
    expect(tip).toContain('Interrupted')
    expect(tip).toContain('stopped by a worker restart')
  })

  // Preserve the complete clipped label when its tooltip also carries an explanation.
  it('keeps a clipped label reachable beside its explanation', () => {
    const { container } = renderList({ tasks: [row({ rowKey: 't1', status: 'interrupted' })] })
    const secondary = secondaries(container)[0]!
    stubClipped(secondary)
    const tip = hover(secondary)
    expect(tip).toContain('Interrupted')
    expect(tip).toContain('stopped by a worker restart')
  })

  // A final status without an explanation still exposes its complete clipped label.
  it('falls back to the label for a finished status with no explanation', () => {
    const { container } = renderList({ tasks: [row({ rowKey: 't1', status: 'failed' })] })
    const secondary = secondaries(container)[0]!
    expect(secondary.textContent).toBe('Failed')
    stubFitting(secondary)
    expect(hover(secondary)).toBeNull()
    stubClipped(secondary)
    expect(hover(secondary)).toBe('Failed')
  })
})

/**
 * A failed registry read must not appear as an authoritative empty list.
 * Empty registries hide the section, so that mistake would also hide the failure.
 * A missing Worker database column can produce this state.
 */
describe('BackgroundTaskList load failure', () => {
  function renderFailed(tasks: BackgroundTaskItem[]) {
    return render(() => (
      <BackgroundTaskPanel variant="sidebar" tasks={tasks} loadFailed />
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

  // A failed refresh preserves retained rows. The failure message replaces missing content only.
  it('still renders the rows it has', () => {
    const { container, queryByTestId } = renderFailed([
      row({ rowKey: 'a', title: 'Review the diff' }),
    ])
    expect(container.querySelectorAll('[data-testid="bg-task-row"]')).toHaveLength(1)
    expect(queryByTestId('bg-task-load-failed')).toBeNull()
  })

  // When no rows remain, every kind must report the read failure instead of claiming absence.
  it('overrides the per-kind empty message on every tab when it has no rows', () => {
    const { container, getByTestId } = renderFailed([])
    fireEvent.click(getByTestId('bg-task-filter-shell'))
    expect(rowsText(container)).toContain('Could not load background tasks')
  })

  /**
   * An empty selected kind must not call a retained registry unreadable.
   * applyLatestPage records refresh failure without removing earlier rows.
   * The All tab can retain subagents while the Shell tab contains none.
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

/**
 * Field updates must preserve the row element and its dependent elements.
 * Each broadcast supplies the whole registry, even when one activity field changes.
 * Replacing the row would close its tooltip and restart its status animation under a stationary pointer.
 * The store's setReconciled preserves item identity. These tests require individual component bindings to update in place.
 */
describe('BackgroundTaskList in-place updates', () => {
  /** A store-backed list, which is the shape the sidebar actually renders. */
  function renderLiveList(initial: BackgroundTaskItem[]) {
    const [tasks, setTasks] = createStore<BackgroundTaskItem[]>(initial)
    const result = render(() => <BackgroundTaskPanel variant="sidebar" tasks={tasks} />)
    return { ...result, setTasks }
  }

  it('leaves the title and the status dot alone when the activity changes', () => {
    const { container, setTasks } = renderLiveList([
      row({ rowKey: 't1', title: 'Review the diff', status: 'running', activity: 'reading' }),
    ])
    const titleBefore = titles(container)[0]!
    const dotBefore = container.querySelector('[data-testid="bg-task-status-dot"]')!
    const rowBefore = container.querySelector('[data-testid="bg-task-row"]')!

    setTasks(0, 'activity', 'writing')

    expect(secondaries(container)[0]!.textContent).toBe('writing')
    expect(titles(container)[0]).toBe(titleBefore)
    expect(container.querySelector('[data-testid="bg-task-status-dot"]')).toBe(dotBefore)
    expect(container.querySelector('[data-testid="bg-task-row"]')).toBe(rowBefore)
  })

  it('updates the title in place when the title changes', () => {
    const { container, setTasks } = renderLiveList([
      row({ rowKey: 't1', title: 'Untitled work', status: 'running', activity: 'reading' }),
    ])
    const titleBefore = titles(container)[0]!

    setTasks(0, 'title', 'Review the diff')

    expect(titles(container)[0]).toBe(titleBefore)
    expect(titleBefore.textContent).toBe('Review the diff')
  })

  it('keeps exact task identity available when titles match and the row identity changes', () => {
    const { container, setTasks } = renderLiveList([
      row({ rowKey: 'workflow-first', title: 'Compute one local value.', kind: 'workflow' }),
      row({ rowKey: 'workflow-second', title: 'Compute one local value.', kind: 'workflow' }),
    ])
    const rows = [...container.querySelectorAll<HTMLElement>('[data-testid="bg-task-row"]')]
    expect(rows.map(element => element.dataset.taskId)).toEqual(['workflow-first', 'workflow-second'])
    setTasks(0, 'rowKey', 'workflow-replacement')
    expect([...container.querySelectorAll<HTMLElement>('[data-testid="bg-task-row"]')].map(element => element.dataset.taskId)).toEqual(['workflow-replacement', 'workflow-second'])
    expect(container.querySelector('[data-task-id="workflow-first"]')).toBeNull()
  })

  // A retained row must update data-status and its strike-through class reactively.
  it('follows a status change on the row and its dot without rebuilding either', () => {
    const { container, setTasks } = renderLiveList([
      row({ rowKey: 't1', title: 'Review the diff', status: 'running', activity: 'reading' }),
    ])
    const rowBefore = container.querySelector<HTMLElement>('[data-testid="bg-task-row"]')!
    const dotBefore = container.querySelector<HTMLElement>('[data-testid="bg-task-status-dot"]')!

    setTasks(0, 'status', 'completed')

    expect(container.querySelector('[data-testid="bg-task-row"]')).toBe(rowBefore)
    expect(rowBefore.dataset.status).toBe('completed')
    expect(classes(rowBefore)).toContain(styles.taskStruck)
    expect(container.querySelector('[data-testid="bg-task-status-dot"]')).toBe(dotBefore)
    expect(classes(dotBefore)).toContain(statusDotStyles.statusDotSuccess)
  })

  it('keeps a paused child open and updates its activity without rebuilding', () => {
    const { container, setTasks } = renderLiveList([
      row({ rowKey: 'child', title: 'Review the diff', status: 'running', activity: 'reading' }),
    ])
    const taskRow = container.querySelector<HTMLElement>('[data-testid="bg-task-row"]')!
    const dot = container.querySelector<HTMLElement>('[data-testid="bg-task-status-dot"]')!

    setTasks(0, 'status', 'paused')
    setTasks(0, 'activity', 'paused')

    expect(taskRow.dataset.status).toBe('paused')
    expect(secondaries(container)[0]?.textContent).toBe('paused')
    expect(classes(taskRow)).not.toContain(styles.taskStruck)
    expect(classes(dot)).toContain(statusDotStyles.statusDotMuted)

    setTasks(0, 'status', 'running')
    setTasks(0, 'activity', 'reading again')

    expect(container.querySelector('[data-testid="bg-task-row"]')).toBe(taskRow)
    expect(container.querySelector('[data-testid="bg-task-status-dot"]')).toBe(dot)
    expect(secondaries(container)[0]?.textContent).toBe('reading again')
    expect(classes(taskRow)).not.toContain(styles.taskStruck)
  })

  // A child ID can arrive after its row. That update must change clickability without changing the element tag.
  // Replacing a div with a button would close its tooltip and restart the status pulse.
  it('becomes clickable without rebuilding the row when the child agent id arrives', () => {
    const [tasks, setTasks] = createStore<BackgroundTaskItem[]>([
      row({ rowKey: 't1', title: 'Review the diff', status: 'running' }),
    ])
    const onOpenSubagent = vi.fn()
    const { container } = render(() => (
      <BackgroundTaskPanel variant="sidebar" tasks={tasks} onOpenSubagent={onOpenSubagent} />
    ))
    const rowBefore = container.querySelector<HTMLElement>('[data-testid="bg-task-row"]')!
    const dotBefore = container.querySelector('[data-testid="bg-task-status-dot"]')!
    // The row remains a button before and after the child ID arrives.
    expect(rowBefore.tagName).toBe('BUTTON')
    // It cannot activate before a transcript exists.
    expect(rowBefore.getAttribute('aria-disabled')).toBe('true')
    expect(classes(rowBefore)).toContain(styles.taskRowStatic)
    fireEvent.click(rowBefore)
    expect(onOpenSubagent).not.toHaveBeenCalled()

    setTasks(0, 'childAgentId', 'c1')

    expect(container.querySelector('[data-testid="bg-task-row"]')).toBe(rowBefore)
    expect(container.querySelector('[data-testid="bg-task-status-dot"]')).toBe(dotBefore)
    expect(rowBefore.dataset.childAgentId).toBe('c1')
    expect(rowBefore.getAttribute('aria-disabled')).toBeNull()
    expect(classes(rowBefore)).not.toContain(styles.taskRowStatic)
    fireEvent.click(rowBefore)
    expect(onOpenSubagent).toHaveBeenCalledTimes(1)
  })

  // aria-disabled preserves pointer events while the subagent starts.
  // A native disabled attribute would block the clipped title's hover tooltip.
  it('leaves a not-yet-openable row able to show its own title tooltip', () => {
    vi.useFakeTimers()
    try {
      const long = 'A title far wider than the row that holds it'
      const [tasks] = createStore<BackgroundTaskItem[]>([
        row({ rowKey: 't1', title: long, status: 'running' }),
      ])
      const { container } = render(() => (
        <BackgroundTaskPanel variant="sidebar" tasks={tasks} onOpenSubagent={() => {}} />
      ))
      const el = container.querySelector<HTMLButtonElement>('[data-testid="bg-task-row"]')!
      expect(el.getAttribute('aria-disabled')).toBe('true')
      expect(el.disabled).toBe(false)

      const title = titles(container)[0]!
      stubClipped(title)
      expect(hoverForTooltip(title)?.textContent).toBe(long)
    }
    finally {
      vi.useRealTimers()
    }
  })

  // A shell row cannot open a transcript. Its stable kind therefore selects a static element.
  it('draws a shell row as a plain element', () => {
    const [tasks] = createStore<BackgroundTaskItem[]>([
      row({ rowKey: 't1', title: 'npm test', kind: 'shell', status: 'running' }),
    ])
    const { container } = render(() => (
      <BackgroundTaskPanel variant="sidebar" tasks={tasks} onOpenSubagent={() => {}} />
    ))
    expect(container.querySelector('[data-testid="bg-task-row"]')!.tagName).toBe('DIV')
  })

  /**
   * Grouped rows require the same stable-element behavior as ungrouped rows.
   * groupBackgroundTasks creates new group objects after status changes.
   * For compares object references, so iterating those objects would rebuild each grouped row.
   * Primitive group keys must preserve the rows even when another row changes status.
   */
  it('keeps a grouped row and its dot across a status change elsewhere in the group', () => {
    const { container, setTasks } = renderLiveList([
      row({ rowKey: 't1', title: 'Review the diff', status: 'running', groupKey: 'wf:x', groupLabel: 'Workflow' }),
      row({ rowKey: 't2', title: 'Write the tests', status: 'pending', groupKey: 'wf:x', groupLabel: 'Workflow' }),
    ])
    const rowsBefore = [...container.querySelectorAll('[data-testid="bg-task-row"]')]
    const dotsBefore = [...container.querySelectorAll('[data-testid="bg-task-status-dot"]')]
    expect(rowsBefore).toHaveLength(2)

    // Finish the second row. The first row remains unchanged.
    setTasks(1, 'status', 'completed')

    const rowsAfter = [...container.querySelectorAll('[data-testid="bg-task-row"]')]
    expect(rowsAfter[0]).toBe(rowsBefore[0])
    expect([...container.querySelectorAll('[data-testid="bg-task-status-dot"]')][0]).toBe(dotsBefore[0])
    expect(rowsAfter.map(el => (el as HTMLElement).dataset.status)).toEqual(['running', 'completed'])
  })

  it('keeps a hovered tooltip on a grouped row across a rebroadcast', () => {
    vi.useFakeTimers()
    try {
      const long = 'A grouped title far wider than the row that holds it'
      const { container, setTasks } = renderLiveList([
        row({ rowKey: 't1', title: long, status: 'running', groupKey: 'wf:x', groupLabel: 'Workflow' }),
        row({ rowKey: 't2', title: 'Write the tests', status: 'pending', groupKey: 'wf:x', groupLabel: 'Workflow' }),
      ])
      const title = titles(container)[0]!
      stubClipped(title)
      expect(hoverForTooltip(title)?.textContent).toBe(long)

      setTasks(1, 'status', 'running')

      expect(titles(container)[0]).toBe(title)
      expect(document.querySelector('[role="tooltip"]')?.textContent).toBe(long)
    }
    finally {
      vi.useRealTimers()
    }
  })

  /**
   * Exercise the actual store and component together.
   * The Worker rebroadcasts the whole registry after one row changes.
   * Preserve a tooltip under a stationary pointer instead of closing it with a replaced element.
   */
  it('keeps a hovered title tooltip open across a whole-registry rebroadcast', () => {
    vi.useFakeTimers()
    try {
      const store = createBackgroundTaskStore()
      const long = 'A title far wider than the row that holds it'
      store.replace('a1', [protoTask('t1', long, 'reading')])
      const { container } = render(() => (
        <BackgroundTaskPanel variant="sidebar" tasks={store.get('a1')} />
      ))

      const title = titles(container)[0]!
      stubClipped(title)
      expect(hoverForTooltip(title)?.textContent).toBe(long)

      store.replace('a1', [protoTask('t1', long, 'writing')])

      expect(secondaries(container)[0]!.textContent).toBe('writing')
      expect(titles(container)[0]).toBe(title)
      expect(document.querySelector('[role="tooltip"]')?.textContent).toBe(long)
    }
    finally {
      vi.useRealTimers()
    }
  })

  // The same registry update must preserve the status dot and its pulse after an activity change.
  it('keeps the status dot across a whole-registry rebroadcast', () => {
    const store = createBackgroundTaskStore()
    store.replace('a1', [protoTask('t1', 'Review the diff', 'reading')])
    const { container } = render(() => (
      <BackgroundTaskPanel variant="sidebar" tasks={store.get('a1')} />
    ))
    const dot = container.querySelector('[data-testid="bg-task-status-dot"]')!

    store.replace('a1', [protoTask('t1', 'Review the diff', 'writing')])

    expect(container.querySelector('[data-testid="bg-task-status-dot"]')).toBe(dot)
  })
})
