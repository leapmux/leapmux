import type { DiffStats } from '~/stores/repoGit'
import { render, screen } from '@solidjs/testing-library'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { hoverForTooltip } from '~/test-support/clipStub'
import { workingTreeDeleteLabel, WorkingTreeIcon, workingTreeKindLabel, WorkingTreeRows, WorkingTreeTooltip } from './WorkingTree'

const HOME = '/Users/trustin'
const WORKTREE_DIR = '/Users/trustin/Workspaces/leapmux-worktrees/blushing-slow-wolf'

function renderRows(overrides: Partial<Parameters<typeof WorkingTreeRows>[0]> = {}) {
  return render(() => (
    <WorkingTreeRows
      isWorktree={overrides.isWorktree ?? true}
      name={overrides.name ?? 'blushing-slow-wolf'}
      directory={overrides.directory ?? WORKTREE_DIR}
      homeDir={overrides.homeDir}
      flavor={overrides.flavor}
      worker={overrides.worker}
      stats={overrides.stats}
    />
  ))
}

function directoryText(): string {
  return screen.getByTestId('working-tree-directory').textContent ?? ''
}

describe('workingTreeKindLabel', () => {
  it('names a linked worktree and the main working tree apart', () => {
    expect(workingTreeKindLabel(true)).toBe('Worktree')
    expect(workingTreeKindLabel(false)).toBe('Branch')
  })
})

// The menu item, the dialog title and the red submit button all read this one
// function, so the three cannot drift apart -- and no caller lowercases a
// Title-case noun to build a label of its own.
describe('workingTreeDeleteLabel', () => {
  it('names the delete action after what it removes', () => {
    expect(workingTreeDeleteLabel(true)).toBe('Delete worktree')
    expect(workingTreeDeleteLabel(false)).toBe('Delete branch')
  })
})

describe('workingTreeIcon (WorkingTreeIcon)', () => {
  // The whole point of the component: two glyphs a user can tell apart at a
  // glance. A single shared icon is the defect it exists to remove, so the two
  // renders must not resolve to the same element.
  it('renders a different glyph for each kind', () => {
    const worktree = render(() => <WorkingTreeIcon isWorktree size="xs" />)
    expect(worktree.container.querySelector('[data-testid="worktree-icon"]')).not.toBeNull()
    expect(worktree.container.querySelector('[data-testid="branch-icon"]')).toBeNull()

    const branch = render(() => <WorkingTreeIcon isWorktree={false} size="xs" />)
    expect(branch.container.querySelector('[data-testid="branch-icon"]')).not.toBeNull()
    expect(branch.container.querySelector('[data-testid="worktree-icon"]')).toBeNull()
  })

  it('passes the caller class through to the svg', () => {
    const { container } = render(() => <WorkingTreeIcon isWorktree size="sm" class="group-icon" />)

    expect(container.querySelector('[data-testid="worktree-icon"]')!.getAttribute('class'))
      .toContain('group-icon')
  })

  // The sidebar branch row is a plain div with no tabindex, so its tooltip
  // opens under a pointer alone. Without a label the kind reaches a screen
  // reader nowhere, and the row that deletes a directory reads exactly like
  // the row that does not.
  it('names the kind when a call site asks for the label', () => {
    render(() => <WorkingTreeIcon isWorktree size="xs" label={workingTreeKindLabel(true)} />)

    expect(screen.getByRole('img', { name: 'Worktree' })).toBeInTheDocument()
  })

  it('names a branch glyph too', () => {
    render(() => <WorkingTreeIcon isWorktree={false} size="xs" label={workingTreeKindLabel(false)} />)

    expect(screen.getByRole('img', { name: 'Branch' })).toBeInTheDocument()
  })

  // The default, and the reason the label is opt-in: every other call site
  // prints the noun in text beside the glyph, where a second announcement is
  // noise. lucide decides `aria-hidden` by whether a role/aria key is PRESENT,
  // so this also pins that the component passes no `role: undefined` key.
  it('stays out of the accessibility tree with no label', () => {
    const { container } = render(() => <WorkingTreeIcon isWorktree={false} size="xs" />)

    expect(container.querySelector('[data-testid="branch-icon"]')!.getAttribute('aria-hidden')).toBe('true')
  })
})

describe('workingTreeRows (WorkingTreeRows)', () => {
  it('labels a linked worktree and shows its name', () => {
    renderRows({ isWorktree: true })

    expect(screen.getByText('Worktree')).toBeTruthy()
    expect(screen.getByTestId('working-tree-name').textContent).toBe('blushing-slow-wolf')
    expect(screen.getByTestId('worktree-icon')).toBeTruthy()
  })

  it('labels the main working tree as a branch', () => {
    renderRows({ isWorktree: false, name: 'main', directory: '/Users/trustin/Workspaces/leapmux' })

    expect(screen.getByText('Branch')).toBeTruthy()
    expect(screen.getByTestId('branch-icon')).toBeTruthy()
  })

  it('tilde-compresses a directory under the home dir', () => {
    renderRows({ homeDir: HOME })

    expect(directoryText()).toBe('~/Workspaces/leapmux-worktrees/blushing-slow-wolf')
  })

  // A worker whose system info has not landed yet reports no home dir. A long
  // absolute path is correct; a guessed short one would not be.
  it('leaves the path absolute when no home dir is known', () => {
    renderRows({ homeDir: undefined })

    expect(directoryText()).toBe(WORKTREE_DIR)
  })

  it('leaves a path outside the home dir absolute', () => {
    renderRows({ directory: '/srv/repos/leapmux', homeDir: HOME })

    expect(directoryText()).toBe('/srv/repos/leapmux')
  })

  it('renders the diff badge when there is work to report', () => {
    const stats: DiffStats = { added: 38, deleted: 12, untracked: 2 }
    renderRows({ homeDir: HOME, stats })

    const badge = screen.getByTestId('git-diff-stats')
    expect(badge.textContent).toContain('+38')
    expect(badge.textContent).toContain('-12')
    expect(badge.textContent).toContain('*2')
  })

  it('renders no badge for an all-zero snapshot', () => {
    renderRows({ stats: { added: 0, deleted: 0, untracked: 0 } })

    expect(screen.queryByTestId('git-diff-stats')).toBeNull()
  })

  it('renders no badge when the caller has no stats', () => {
    renderRows({ stats: undefined })

    expect(screen.queryByTestId('git-diff-stats')).toBeNull()
  })

  // The sidebar's "(no branch)" bucket and a row whose git probe has not landed
  // reach this component with empty strings. The kind label is still worth
  // stating, so neither row is dropped.
  it('still states the kind for an unnamed checkout with no directory', () => {
    renderRows({ name: '', directory: '' })

    expect(screen.getByText('Worktree')).toBeTruthy()
    expect(screen.getByTestId('working-tree-name').textContent).toBe('')
    expect(directoryText()).toBe('')
  })

  // Most callers pass this as a Tooltip's `content`, and a tooltip portal is
  // `pointer-events: none` — anything interactive inside it is unreachable.
  it('renders nothing interactive, so it can be a tooltip body', () => {
    const { container } = renderRows({ homeDir: HOME, stats: { added: 1, deleted: 0, untracked: 0 } })

    expect(container.querySelector('button, a, input, [role="tooltip"]')).toBeNull()
  })

  // The worker's own path rule, not a guess from the string. `detectFlavor`
  // recognizes win32 only by a leading backslash or a drive letter, so a
  // Windows worker whose git reports a forward-slash UNC path sniffs as posix
  // and the directory never compresses.
  it('uses the caller flavor for a UNC path git reports with forward slashes', () => {
    renderRows({ directory: '//srv/share/u/repo', homeDir: '\\\\srv\\share\\u', flavor: 'win32' })

    expect(directoryText()).toBe('~\\repo')
  })

  it('sniffs the flavor from the path when the caller states none', () => {
    renderRows({ directory: 'C:\\Users\\u\\repo', homeDir: 'C:\\Users\\u' })

    expect(directoryText()).toBe('~\\repo')
  })

  // Two workers with the same branch at the same path under each home dir
  // produce two identical rows, and Delete on one removes the other machine's
  // directory. The caller sets `worker` exactly when that can happen.
  it('states the worker when the caller supplies one', () => {
    renderRows({ homeDir: HOME, worker: 'build-box' })

    expect(screen.getByText('Worker')).toBeTruthy()
    expect(screen.getByTestId('working-tree-worker').textContent).toBe('build-box')
  })

  it('drops the worker row where one worker owns everything on screen', () => {
    renderRows({ homeDir: HOME })

    expect(screen.queryByTestId('working-tree-worker')).toBeNull()
    expect(screen.queryByText('Worker')).toBeNull()
  })
})

// One owner of the precedence rule `Tooltip` imposes: `content` replaces
// `text`, so a reason and the rows can never both show. Two call sites that
// each spelled it out could start answering differently.
describe('workingTreeTooltip (WorkingTreeTooltip)', () => {
  const INFO = { isWorktree: true, name: 'feature', directory: WORKTREE_DIR, homeDir: HOME }

  beforeAll(() => {
    HTMLElement.prototype.showPopover = vi.fn()
    HTMLElement.prototype.hidePopover = vi.fn()
  })

  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('shows the rows when the actions are usable', () => {
    render(() => (
      <WorkingTreeTooltip info={INFO}>
        <button type="button">chip</button>
      </WorkingTreeTooltip>
    ))

    const tooltip = hoverForTooltip(screen.getByRole('button'))
    expect(tooltip!.textContent).toContain('Worktree')
    expect(tooltip!.textContent).toContain('~/Workspaces/leapmux-worktrees/blushing-slow-wolf')
  })

  it('replaces the rows with the reason when they are not', () => {
    render(() => (
      <WorkingTreeTooltip info={INFO} disabledReason="This Worker is offline.">
        <button type="button">chip</button>
      </WorkingTreeTooltip>
    ))

    const tooltip = hoverForTooltip(screen.getByRole('button'))
    expect(tooltip!.textContent).toBe('This Worker is offline.')
    expect(tooltip!.querySelector('[data-testid="working-tree-rows"]')).toBeNull()
  })
})
