import type { RepoCheckout } from './repoCheckouts'
import type { BranchGroup } from './WorkspaceTabTree'
import { fireEvent, render, screen, within } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import * as workerRpc from '~/api/workerRpc'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { stubBranchMenuActions } from '~/test-support/branchMenu'
import { withPreferences } from '~/test-support/preferencesProvider'
import { RepoContextMenu } from './RepoContextMenu'

vi.mock('~/api/workerRpc', () => ({
  listAvailableProviders: vi.fn(async () => ({ providers: [AgentProvider.CLAUDE_CODE] })),
  listAvailableShells: vi.fn(async () => ({ shells: ['/bin/zsh'], defaultShell: '/bin/zsh' })),
}))

const listProviders = workerRpc.listAvailableProviders as unknown as ReturnType<typeof vi.fn>
const listShells = workerRpc.listAvailableShells as unknown as ReturnType<typeof vi.fn>

beforeEach(() => {
  vi.clearAllMocks()
})

function branch(overrides: Partial<BranchGroup> = {}): BranchGroup {
  return {
    branchName: 'main',
    workerId: 'w1',
    gitToplevel: '/home/me/leapmux',
    isWorktree: false,
    displayLabel: 'main',
    homeDir: '/home/me',
    flavor: undefined,
    workerLabel: '',
    tabs: [],
    diffAdded: 0,
    diffDeleted: 0,
    diffUntracked: 0,
    ...overrides,
  }
}

function checkout(overrides: Partial<RepoCheckout> = {}): RepoCheckout {
  return {
    workerId: 'w1',
    gitToplevel: '/home/me/leapmux',
    originUrl: 'https://example.com/o/r.git',
    isLocal: false,
    label: 'main',
    branch: branch(),
    ...overrides,
  }
}

function renderMenu(overrides: Partial<Parameters<typeof RepoContextMenu>[0]> = {}) {
  const props = {
    checkouts: () => [checkout()],
    actionsFor: () => stubBranchMenuActions(),
    disabledReasonFor: () => undefined,
    onCollapseAllBranches: vi.fn(),
    nothingToCollapse: () => false,
    ...overrides,
  }
  render(withPreferences(() => <RepoContextMenu {...props} />))
  return props
}

/** Open the menu, as a user does. Every item below depends on it. */
function openMenu() {
  fireEvent.click(screen.getByTestId('repo-row-menu-trigger'))
}

function itemsOf(testId: string): string[] {
  return within(screen.getByTestId(testId))
    .queryAllByRole('menuitem', { hidden: true })
    .map(el => el.textContent?.trim() ?? '')
}

describe('repoContextMenu', () => {
  // The same three sections a branch row's menu carries, in the same order: a
  // repository with one checkout and the branch inside it offer the same
  // things, and two shapes for that would be two shapes to learn.
  it('renders one checkout flat, in the branch menu\'s section order', () => {
    renderMenu()
    openMenu()

    const items = itemsOf('repo-context-menu')
    expect(items).toContain('New agent...')
    expect(items).toContain('New terminal...')
    expect(items).toContain('Copy repository URL')
    expect(items).toContain('Copy repository path')
    expect(items.at(-1)).toBe('Collapse all branches')
    expect(screen.getByText('Agents')).toBeInTheDocument()
    expect(screen.getByText('Terminals')).toBeInTheDocument()
    expect(screen.getByText('Repository')).toBeInTheDocument()
  })

  it('gives each of several checkouts its own submenu', () => {
    renderMenu({
      checkouts: () => [
        checkout(),
        checkout({
          gitToplevel: '/home/me/wt',
          label: 'feature (worktree)',
          branch: branch({ branchName: 'feature', gitToplevel: '/home/me/wt', isWorktree: true }),
        }),
      ],
    })
    openMenu()

    expect(itemsOf('repo-context-menu')).toEqual([
      'main',
      'feature (worktree)',
      'Collapse all branches',
    ])
    expect(screen.getByText('Checkouts')).toBeInTheDocument()
  })

  it('holds the same block inside a checkout submenu as the flat shape has', () => {
    renderMenu({
      checkouts: () => [
        checkout(),
        checkout({ gitToplevel: '/home/me/wt', label: 'feature (worktree)' }),
      ],
    })
    openMenu()
    fireEvent.click(screen.getByTestId('repo-checkout-main'))

    const items = itemsOf('repo-checkout-main-popover')
    expect(items).toContain('New agent...')
    expect(items).toContain('Copy repository path')
  })

  // The ROW defers its checkout projection on this, because one of these
  // menus mounts per repository row of every workspace and the projection
  // walks every branch under it.
  it('tells its caller when it opens and closes', () => {
    const onToggle = vi.fn()
    renderMenu({ onToggle })

    openMenu()
    expect(onToggle).toHaveBeenLastCalledWith(true)
  })

  // One of these mounts per repository row of every workspace, so a fetch on
  // MOUNT would scan every Worker in the fleet to fill menus nobody opened.
  it('asks a checkout\'s Worker for nothing until the menu opens', () => {
    renderMenu()

    expect(listProviders).not.toHaveBeenCalled()
    expect(listShells).not.toHaveBeenCalled()

    openMenu()
    expect(listProviders).toHaveBeenCalledWith('w1', expect.anything())
    expect(listShells).toHaveBeenCalledWith('w1', { workerId: 'w1' })
  })

  // A repository can span two machines, so each checkout asks its OWN Worker
  // rather than borrowing the first one's answer.
  it('asks each checkout\'s own Worker', () => {
    renderMenu({
      checkouts: () => [
        checkout(),
        checkout({ workerId: 'w2', gitToplevel: '/srv/leapmux', label: 'main (worker-b)' }),
      ],
    })
    openMenu()
    fireEvent.click(screen.getByTestId('repo-checkout-main-worker-b'))

    expect(listShells).toHaveBeenCalledWith('w2', { workerId: 'w2' })
  })

  it('collapses every branch of the repository from its own item', () => {
    const props = renderMenu()
    openMenu()
    fireEvent.click(screen.getByTestId('repo-collapse-branches'))

    expect(props.onCollapseAllBranches).toHaveBeenCalledTimes(1)
  })

  it('dims Collapse all branches once there is nothing left to collapse', () => {
    const props = renderMenu({ nothingToCollapse: () => true })
    openMenu()

    const item = screen.getByTestId('repo-collapse-branches') as HTMLButtonElement
    expect(item).toBeDisabled()
    fireEvent.click(item)
    expect(props.onCollapseAllBranches).not.toHaveBeenCalled()
  })

  // An offline Worker takes the tab-creation items with it, and nothing else:
  // the repository rows copy text the browser already holds.
  it('disables only the tab-creation items when the checkout\'s worker is offline', () => {
    renderMenu({ disabledReasonFor: () => 'This Worker is offline.' })
    openMenu()

    expect(screen.getByRole('menuitem', { name: 'New agent...', hidden: true })).toBeDisabled()
    expect(screen.getByRole('menuitem', { name: 'Copy repository path', hidden: true })).not.toBeDisabled()
  })

  // A branch row with no branch name has no ref to bind, so the tab-creation
  // items have no target -- but the path is still real and still copyable.
  it('keeps the repository block when a checkout has no bindable actions', () => {
    renderMenu({ actionsFor: () => undefined })
    openMenu()

    const items = itemsOf('repo-context-menu')
    expect(items).not.toContain('New agent...')
    expect(items).toContain('Copy repository path')
  })

  it('renders only the collapse item when the repository has no checkout', () => {
    renderMenu({ checkouts: () => [] })
    openMenu()

    expect(itemsOf('repo-context-menu')).toEqual(['Collapse all branches'])
  })
})
