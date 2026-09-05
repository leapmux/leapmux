import type { BackgroundTaskItem as ProtoBackgroundTaskItem } from '~/generated/proto/leapmux/v1/agent_pb'
import type { BackgroundTaskItem } from '~/stores/chatBackgroundTasks'
import { fireEvent, render } from '@solidjs/testing-library'
import { createStore } from 'solid-js/store'
import { describe, expect, it, vi } from 'vitest'
import { AgentWorkPanel } from '~/components/backgroundtasks/AgentWorkPanel'
import * as styles from '~/components/backgroundtasks/BackgroundTaskList.css'
import { BackgroundTaskKind, BackgroundTaskStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { createBackgroundTaskStore } from '~/stores/chatBackgroundTaskStore'
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
 * Render the rows through the PANEL that hosts them.
 *
 * The panel owns the root, the tab bar and the `role=tabpanel` region these
 * cases select on, so going through it keeps the DOM contract identical to what
 * the component used to render alone -- which is what lets the E2E specs and
 * every test id survive the split.
 *
 * The sidebar variant, because the popover one differs only in sizing classes
 * that jsdom cannot see.
 */
function renderList(props: {
  tasks: BackgroundTaskItem[]
  onOpenSubagent?: (item: BackgroundTaskItem) => void
  loadFailed?: boolean
}) {
  return render(() => (
    <AgentWorkPanel
      variant="sidebar"
      tasks={props.tasks}
      goalProgress={{}}
      goalActions={[]}
      loadFailed={props.loadFailed}
      onOpenSubagent={props.onOpenSubagent}
    />
  ))
}

/**
 * The TASK region alone: no tab-bar labels, and no goal card.
 *
 * The goal card shares the tabpanel on the All and Goal tabs, so reading the
 * whole region would fold the session goal into every assertion about task
 * rows. Removing it by test id keeps these cases about the registry, which is
 * what they are for -- the card has its own file.
 */
function rowsText(container: HTMLElement): string {
  const panel = container.querySelector('[role="tabpanel"]')!.cloneNode(true) as HTMLElement
  panel.querySelector('[data-testid="goal-card"]')?.remove()
  return panel.textContent ?? ''
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

  // The worker keeps a row key VERBATIM, because the key is the row's identity
  // and a rewrite merges two providers' rows into one. So the reader is where
  // an unreadable key is cleaned, and this block is the guard on that split.
  describe('the label falls back to a CLEANED row key', () => {
    const labelOf = (container: HTMLElement) => titles(container)[0].textContent

    // Cursor's observed toolCallId shape. It reaches the browser with the
    // newline in it, and the label must not carry one.
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

    // Each arm is cleaned and the FALLBACK reads the cleaned arm, so a
    // description of nothing but invisible characters falls through to the key
    // instead of rendering the row as a blank line.
    it('falls through an arm that cleans to nothing', () => {
      const { container } = renderList({
        tasks: [row({ rowKey: 'call-abc', title: '', description: '​​' })],
      })
      expect(labelOf(container)).toBe('call-abc')
    })

    // The LAST arm can clean to nothing as readily as the first two. The worker
    // refuses an unusable row key rather than rewriting it, and a key of
    // nothing but bidirectional overrides is usable as an identity, so it
    // reaches the reader as a non-empty string that `cleanName` empties. Every
    // arm then falls through and the row drew a blank first line with a status
    // dot beside it.
    it('names the row Untitled when every arm cleans to nothing', () => {
      const { container } = renderList({
        tasks: [row({ rowKey: '\u202E\u202E', title: '', description: '\u200B' })],
      })
      expect(labelOf(container)).toBe('Untitled')
    })

    // The worker already cleaned the title, and the rule is idempotent, so the
    // reader's clean must be a no-op on it.
    it('passes a worker-cleaned title through unchanged', () => {
      const { container } = renderList({
        tasks: [row({ rowKey: 'k', title: 'npm test --grep "$FOO"' })],
      })
      expect(labelOf(container)).toBe('npm test --grep "$FOO"')
    })

    // The echo guard compares the second line against the first, so BOTH sides
    // have to be cleaned. The title arm is cleaned and the description arm was
    // not, so the comparison stopped matching for every string the fold
    // rewrites -- and the row printed the same command twice, once folded and
    // once raw. Claude's local_bash sends the command as both title and
    // description, and a command carrying a double space is ordinary, so this
    // is the exact case the guard exists for.
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

    // A newline is the other shape the fold rewrites, and it reaches the row
    // from a provider that wraps its copy.
    it('suppresses a description that differs from the title only by a newline', () => {
      const { container } = renderList({
        tasks: [row({ rowKey: 'k', title: 'build\nand test', description: 'build\nand test' })],
      })
      expect(secondaries(container)).toHaveLength(0)
    })

    // The guard must not swallow a genuinely different second line.
    it('still shows a description that differs from the title', () => {
      const { container } = renderList({
        tasks: [row({ rowKey: 'k', title: 'Run the suite', description: 'npm test' })],
      })
      expect(secondaries(container).map(el => el.textContent)).toEqual(['npm test'])
    })

    // The second line is cleaned as well as compared: `activity` and
    // `description` arrive from the provider UNCLEANED (the worker cleans
    // `title` alone), so a bidirectional override in one would reorder the line.
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
      <AgentWorkPanel variant="sidebar" goalProgress={{}} goalActions={[]} tasks={tasks} loadFailed />
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

/**
 * What a row must NOT rebuild when one of its fields changes.
 *
 * The registry arrives whole on every broadcast, so a subagent that reports
 * progress every few seconds redraws the section that often. A row rebuilt from
 * scratch takes its tooltip and its animations with it: the full-title tooltip
 * under the pointer closed and reopened, and the status dot restarted its pulse
 * -- both under a cursor that never moved.
 *
 * The store half is `setReconciled` in `~/stores/chatPerAgentStore`, which is
 * what keeps a row's identity across the broadcast. These cases hold the
 * component half: one field changing must update ONE binding.
 */
describe('backgroundTaskList in-place updates', () => {
  /** A store-backed list, which is the shape the sidebar actually renders. */
  function renderLiveList(initial: BackgroundTaskItem[]) {
    const [tasks, setTasks] = createStore<BackgroundTaskItem[]>(initial)
    const result = render(() => <AgentWorkPanel variant="sidebar" goalProgress={{}} goalActions={[]} tasks={tasks} />)
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

  // `data-status` is what the E2E suite selects a finished row by, and the
  // strike-through class is how a finished row reads. Both were correct only
  // because the row used to be rebuilt; a row that survives has to carry them
  // reactively.
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
    expect(classes(dotBefore)).toContain(styles.statusDotSuccess)
  })

  // The row becomes clickable only once the worker reports the child agent id,
  // which arrives in a later broadcast than the row itself -- so the TAG cannot
  // depend on it. A `Show` keyed on the id swapped a <div> for a <button> and
  // rebuilt the whole row body at exactly the moment the user is watching that
  // row spawn: the title tooltip closed and the status dot's pulse restarted,
  // which is the flicker every other case here exists to prevent.
  it('becomes clickable without rebuilding the row when the child agent id arrives', () => {
    const [tasks, setTasks] = createStore<BackgroundTaskItem[]>([
      row({ rowKey: 't1', title: 'Review the diff', status: 'running' }),
    ])
    const onOpenSubagent = vi.fn()
    const { container } = render(() => (
      <AgentWorkPanel variant="sidebar" goalProgress={{}} goalActions={[]} tasks={tasks} onOpenSubagent={onOpenSubagent} />
    ))
    const rowBefore = container.querySelector<HTMLElement>('[data-testid="bg-task-row"]')!
    const dotBefore = container.querySelector('[data-testid="bg-task-status-dot"]')!
    // Always a button, so nothing about the id can change the element.
    expect(rowBefore.tagName).toBe('BUTTON')
    // ...but not yet an ENABLED one: there is no transcript to open.
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

  // `aria-disabled`, never the `disabled` attribute. A disabled control
  // dispatches no pointer event of its own OR to its descendants, so the row's
  // own title tooltip would be unreachable for as long as the subagent spawns --
  // and that tooltip is the only route to a clipped title.
  it('leaves a not-yet-openable row able to show its own title tooltip', () => {
    vi.useFakeTimers()
    try {
      const long = 'A title far wider than the row that holds it'
      const [tasks] = createStore<BackgroundTaskItem[]>([
        row({ rowKey: 't1', title: long, status: 'running' }),
      ])
      const { container } = render(() => (
        <AgentWorkPanel variant="sidebar" goalProgress={{}} goalActions={[]} tasks={tasks} onOpenSubagent={() => {}} />
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

  // A shell row can never open a transcript, so it is not a button at all --
  // the tag still follows the one field that cannot change over a row's life.
  it('draws a shell row as a plain element', () => {
    const [tasks] = createStore<BackgroundTaskItem[]>([
      row({ rowKey: 't1', title: 'npm test', kind: 'shell', status: 'running' }),
    ])
    const { container } = render(() => (
      <AgentWorkPanel variant="sidebar" goalProgress={{}} goalActions={[]} tasks={tasks} onOpenSubagent={() => {}} />
    ))
    expect(container.querySelector('[data-testid="bg-task-row"]')!.tagName).toBe('DIV')
  })

  /**
   * The same guarantee for a GROUPED row, which is where a real Claude workflow
   * puts every subagent it spawns.
   *
   * `groupBackgroundTasks` builds fresh group objects on every run, and the
   * memo it feeds re-runs on any status change, because the sort reads
   * `status`. Iterating those objects with `For` -- which reconciles by
   * reference -- therefore tore down and rebuilt every row of the section
   * whenever ANY row in the list changed status, however well the store
   * reconciled the items underneath. Every other case in this describe uses an
   * ungrouped fixture and passes either way.
   */
  it('keeps a grouped row and its dot across a status change elsewhere in the group', () => {
    const { container, setTasks } = renderLiveList([
      row({ rowKey: 't1', title: 'Review the diff', status: 'running', groupKey: 'wf:x', groupLabel: 'Workflow' }),
      row({ rowKey: 't2', title: 'Write the tests', status: 'pending', groupKey: 'wf:x', groupLabel: 'Workflow' }),
    ])
    const rowsBefore = [...container.querySelectorAll('[data-testid="bg-task-row"]')]
    const dotsBefore = [...container.querySelectorAll('[data-testid="bg-task-status-dot"]')]
    expect(rowsBefore).toHaveLength(2)

    // The SECOND row finishes. The first one did not change at all.
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
   * The reported bug, end to end, through the real store.
   *
   * The worker rebroadcasts the WHOLE registry whenever one row changes, so this
   * is the path the sidebar actually takes -- and the one the two halves of the
   * fix have to survive together. The tooltip is what made it visible: a
   * tooltip closes with the element it is attached to, so a row rebuilt under a
   * stationary pointer blinks.
   */
  it('keeps a hovered title tooltip open across a whole-registry rebroadcast', () => {
    vi.useFakeTimers()
    try {
      const store = createBackgroundTaskStore()
      const long = 'A title far wider than the row that holds it'
      store.replace('a1', [protoTask('t1', long, 'reading')])
      const { container } = render(() => (
        <AgentWorkPanel variant="sidebar" goalProgress={{}} goalActions={[]} tasks={store.get('a1')} />
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

  // The other half of the same rebroadcast: a dot that is rebuilt restarts its
  // pulse from the top, which reads as a blink on a row that only reported new
  // activity.
  it('keeps the status dot across a whole-registry rebroadcast', () => {
    const store = createBackgroundTaskStore()
    store.replace('a1', [protoTask('t1', 'Review the diff', 'reading')])
    const { container } = render(() => (
      <AgentWorkPanel variant="sidebar" goalProgress={{}} goalActions={[]} tasks={store.get('a1')} />
    ))
    const dot = container.querySelector('[data-testid="bg-task-status-dot"]')!

    store.replace('a1', [protoTask('t1', 'Review the diff', 'writing')])

    expect(container.querySelector('[data-testid="bg-task-status-dot"]')).toBe(dot)
  })
})
