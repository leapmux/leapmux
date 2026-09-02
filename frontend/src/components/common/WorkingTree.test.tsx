import type { DiffStats } from '~/stores/repoGit'
import { render, screen } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { WorkingTreeIcon, workingTreeKindLabel, WorkingTreeRows } from './WorkingTree'

const HOME = '/Users/trustin'
const WORKTREE_DIR = '/Users/trustin/Workspaces/leapmux-worktrees/blushing-slow-wolf'

function renderRows(overrides: Partial<Parameters<typeof WorkingTreeRows>[0]> = {}) {
  return render(() => (
    <WorkingTreeRows
      isWorktree={overrides.isWorktree ?? true}
      name={overrides.name ?? 'blushing-slow-wolf'}
      directory={overrides.directory ?? WORKTREE_DIR}
      homeDir={overrides.homeDir}
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
})
