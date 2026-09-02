import type { WorkingTreeInfo } from '~/components/common/WorkingTree'
import type { DiffStats } from '~/stores/repoGit'
import { render, screen } from '@solidjs/testing-library'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { hoverForTooltip } from '~/test-support/clipStub'
import { WorkingTreeChip } from './WorkingTreeChip'

const WORKTREE_DIR = '/home/dev/repos/leapmux-worktrees/feature'

beforeAll(() => {
  // The tooltip enters the top layer, and the menu behind the chip is a popover.
  HTMLElement.prototype.showPopover = vi.fn()
  HTMLElement.prototype.hidePopover = vi.fn()
  HTMLElement.prototype.togglePopover = vi.fn()
})

beforeEach(() => {
  vi.useFakeTimers()
})

afterEach(() => {
  vi.useRealTimers()
})

/** The flat option shape the cases read better in, assembled into the prop. */
function renderChip(overrides: Partial<WorkingTreeInfo> & { disabledReason?: string } = {}) {
  const workingTree: WorkingTreeInfo = {
    isWorktree: overrides.isWorktree ?? true,
    name: 'name' in overrides ? overrides.name! : 'feature',
    directory: overrides.directory ?? WORKTREE_DIR,
    homeDir: 'homeDir' in overrides ? overrides.homeDir : '/home/dev',
    flavor: overrides.flavor,
    stats: overrides.stats,
  }
  render(() => (
    <WorkingTreeChip
      workingTree={workingTree}
      disabledReason={overrides.disabledReason}
      onChangeBranch={() => {}}
      onDeleteBranch={() => {}}
    />
  ))
  return screen.queryByTestId('composer-branch-trigger')
}

describe('workingTreeChip', () => {
  it('marks a worktree with the worktree glyph', () => {
    const chip = renderChip({ isWorktree: true })

    expect(chip!.querySelector('[data-testid="worktree-icon"]')).not.toBeNull()
    expect(chip!.querySelector('[data-testid="branch-icon"]')).toBeNull()
  })

  it('marks a main-repo checkout with the branch glyph', () => {
    const chip = renderChip({ isWorktree: false, directory: '/home/dev/repos/leapmux' })

    expect(chip!.querySelector('[data-testid="branch-icon"]')).not.toBeNull()
    expect(chip!.querySelector('[data-testid="worktree-icon"]')).toBeNull()
  })

  // The chip has room for a name and nothing else, so the kind and the
  // directory live in the tooltip -- the same body the sidebar row shows.
  it('states the kind, the directory and the diff stats on hover', () => {
    const stats: DiffStats = { added: 38, deleted: 12, untracked: 0 }
    const chip = renderChip({ stats })

    const tooltip = hoverForTooltip(chip!)
    expect(tooltip).not.toBeNull()
    expect(tooltip!.textContent).toContain('Worktree')
    expect(tooltip!.textContent).toContain('~/repos/leapmux-worktrees/feature')
    expect(tooltip!.textContent).toContain('+38')
    expect(tooltip!.textContent).toContain('-12')
  })

  it('names the main working tree a branch on hover', () => {
    const chip = renderChip({ isWorktree: false, directory: '/home/dev/repos/leapmux' })

    const tooltip = hoverForTooltip(chip!)
    expect(tooltip!.textContent).toContain('Branch')
    expect(tooltip!.textContent).toContain('~/repos/leapmux')
  })

  it('drops the badge when there is nothing to report', () => {
    const chip = renderChip({ stats: { added: 0, deleted: 0, untracked: 0 } })

    const tooltip = hoverForTooltip(chip!)
    expect(tooltip!.querySelector('[data-testid="git-diff-stats"]')).toBeNull()
    // The two rows still render — a clean tree is still a worktree.
    expect(tooltip!.textContent).toContain('Worktree')
    expect(tooltip!.textContent).toContain('~/repos/leapmux-worktrees/feature')
  })

  // The status bar already carries four chips on a narrow tile, so the badge
  // stays in the tooltip. A visible badge here would push the name out.
  it('carries no badge on the chip itself', () => {
    const chip = renderChip({ stats: { added: 38, deleted: 12, untracked: 0 } })

    expect(chip!.querySelector('[data-testid="git-diff-stats"]')).toBeNull()
  })

  it('leaves the directory absolute when the worker home dir is unknown', () => {
    const chip = renderChip({ homeDir: undefined })

    expect(hoverForTooltip(chip!)!.textContent).toContain(WORKTREE_DIR)
  })

  // A reason the actions are unusable outranks the rows: it is the only route
  // to that reason for a user who never opens the menu.
  it('replaces the rows with the reason when the actions are unusable', () => {
    const chip = renderChip({ disabledReason: 'This Worker is offline.', stats: { added: 1, deleted: 0, untracked: 0 } })

    const tooltip = hoverForTooltip(chip!)
    expect(tooltip!.textContent).toBe('This Worker is offline.')
    expect(tooltip!.querySelector('[data-testid="working-tree-rows"]')).toBeNull()
  })

  it('renders nothing for an empty branch name', () => {
    expect(renderChip({ name: '' })).toBeNull()
  })

  // The worker's own path rule, threaded from the panel. Without it the chip
  // sniffs the flavor from the string, and a Windows worker whose git reports
  // a forward-slash UNC path falls through to posix and never compresses.
  it('shortens a UNC directory with the caller flavor', () => {
    const chip = renderChip({
      directory: '//srv/share/dev/repo',
      homeDir: '\\\\srv\\share\\dev',
      flavor: 'win32',
    })

    expect(hoverForTooltip(chip!)!.querySelector('[data-testid="working-tree-directory"]')!.textContent)
      .toBe('~\\repo')
  })
})
