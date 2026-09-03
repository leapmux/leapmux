import type { Tab } from '~/stores/tab.types'
import { create } from '@bufbuild/protobuf'
import { fireEvent, render, screen, within } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { isSectionFilterShown, resetWorkspaceListStateForTests, workspaceSortOrder } from '~/components/workspace/workspaceListState'
import { gitModeStickyKey, rememberStickyGitMode } from '~/components/workspace/workspaceStartPoint'
import { SectionSchema, SectionType, Sidebar } from '~/generated/proto/leapmux/v1/section_pb'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { GitMode } from '~/hooks/useGitModeState'
import { localStorageClearForTests, setStorageAccount } from '~/lib/browserStorage'
import { repoKey } from '~/stores/repoGit'
import { createRepoGitStore } from '~/stores/repoGit.store'
import { WorkspaceSectionMenu } from './WorkspaceSectionMenu'

/**
 * The REAL `DropdownMenu`. See `WorkspaceContextMenu.test.tsx` for why a mock
 * is worse than useless here: it drops `onToggle`, which is what gates this
 * menu's entire repository list.
 */

function makeSection(name: string, sectionType: SectionType, sidebar = Sidebar.LEFT) {
  return create(SectionSchema, { id: `sec-${sectionType}`, name, position: 'n', sectionType, sidebar })
}

const IN_PROGRESS = makeSection('In progress', SectionType.WORKSPACES_IN_PROGRESS)
const ARCHIVED = makeSection('Archived', SectionType.WORKSPACES_ARCHIVED)
const CUSTOM = makeSection('My Section', SectionType.WORKSPACES_CUSTOM)

function tabsAndStore(toplevels: readonly string[], workerId = 'w1') {
  const store = createRepoGitStore()
  const tabs: Tab[] = toplevels.map((toplevel, i) => {
    store.upsert(repoKey(workerId, toplevel), { workerId, toplevel, branch: 'main' })
    return { id: `t${i}`, workspaceId: 'ws-1', type: TabType.AGENT, workerId, gitToplevel: toplevel } as Tab
  })
  return { store, tabs }
}

function noop() {}

function renderMenu(overrides: Partial<Parameters<typeof WorkspaceSectionMenu>[0]> = {}) {
  const { store, tabs } = tabsAndStore([])
  const props = {
    section: IN_PROGRESS,
    canCreate: true,
    getTabs: () => tabs,
    getWorkspaceIds: () => ['ws-1'],
    repoGitStore: store,
    hasActiveWorkspace: () => false,
    onRevealActiveWorkspace: noop,
    onCollapseAll: noop,
    onExpandAll: noop,
    onNewWorkspace: noop as Parameters<typeof WorkspaceSectionMenu>[0]['onNewWorkspace'],
    onUnarchiveAll: noop,
    onEmptyArchive: noop,
    onNewSection: noop,
    onRenameSection: noop,
    onDeleteSection: noop,
    ...overrides,
  }
  render(() => <WorkspaceSectionMenu {...props} />)
  return props
}

function triggerId(section: typeof IN_PROGRESS): string {
  const slug = section === ARCHIVED
    ? 'workspaces_archived'
    : section === CUSTOM ? 'workspaces_custom' : 'workspaces_in_progress'
  return `sidebar-section-menu-${slug}`
}

function openMenu(section = IN_PROGRESS) {
  fireEvent.click(screen.getByTestId(triggerId(section)))
}

/** The item labels of one popover, in order. `hidden: true` — see the row menu's test. */
function itemsOf(testId: string): string[] {
  return within(screen.getByTestId(testId))
    .queryAllByRole('menuitem', { hidden: true })
    .map(el => el.textContent?.trim() ?? '')
}

function inProgressItems(): string[] {
  return itemsOf('sidebar-section-menu-workspaces_in_progress-popover')
}

describe('workspaceSectionMenu', () => {
  beforeEach(() => {
    localStorageClearForTests()
    setStorageAccount('u-1')
    resetWorkspaceListStateForTests()
  })

  it('names the trigger after its section, so it is not an unnamed button', () => {
    renderMenu()
    expect(screen.getByTestId(triggerId(IN_PROGRESS))).toHaveAccessibleName('In progress actions')
  })

  it('offers the create item, the view items and the section CRUD, in that order', () => {
    renderMenu()
    openMenu()

    // `Filter workspaces` is a `menuitemcheckbox` and not a `menuitem`, so it
    // is asserted separately below; this is the command order.
    expect(inProgressItems()).toEqual([
      'New workspace...',
      'Sort by',
      'Collapse all',
      'Expand all',
      'New section...',
    ])
  })

  it('offers the filter toggle as a checkbox, reflecting whether the box is open', () => {
    renderMenu()
    openMenu()

    const toggle = screen.getByTestId('sidebar-filter-workspaces')
    expect(toggle).toHaveAttribute('aria-checked', 'false')
    fireEvent.click(toggle)
    expect(isSectionFilterShown(IN_PROGRESS.id)).toBe(true)
  })

  describe('the Sort by submenu', () => {
    it('mounts its items only once it is opened', () => {
      // `DropdownMenu` renders children eagerly and the parent's keyboard
      // navigation queries the whole subtree, so a closed submenu's items would
      // be phantom stops for ArrowDown and type-ahead.
      renderMenu()
      openMenu()
      expect(screen.queryByTestId('workspace-sort-key-name')).not.toBeInTheDocument()

      fireEvent.click(screen.getByTestId('sidebar-sort-by'))
      expect(screen.getByTestId('workspace-sort-key-name')).toBeInTheDocument()
    })

    it('carries the criterion and the order as two independent radio groups', () => {
      renderMenu()
      openMenu()
      fireEvent.click(screen.getByTestId('sidebar-sort-by'))

      const popover = screen.getByTestId('sidebar-sort-by-popover')
      expect(within(popover).getAllByRole('group', { hidden: true })
        .map(g => g.getAttribute('aria-label'))).toEqual(['Sort by', 'Order'])
      expect(screen.getByTestId('workspace-sort-key-manual')).toHaveAttribute('aria-checked', 'true')
    })

    it('picks a criterion WITHOUT closing, so the order is still reachable', () => {
      // The whole reason the submenu is `as="div"`: a `menu` popover dismisses
      // on any click inside it.
      renderMenu()
      openMenu()
      fireEvent.click(screen.getByTestId('sidebar-sort-by'))

      fireEvent.click(screen.getByTestId('workspace-sort-key-name'))
      expect(workspaceSortOrder().key).toBe('name')
      expect(screen.getByTestId('workspace-sort-direction-desc')).toBeInTheDocument()

      fireEvent.click(screen.getByTestId('workspace-sort-direction-desc'))
      expect(workspaceSortOrder()).toEqual({ key: 'name', direction: 'desc' })
    })

    it('words the order radios for the criterion in force', () => {
      renderMenu()
      openMenu()
      fireEvent.click(screen.getByTestId('sidebar-sort-by'))
      fireEvent.click(screen.getByTestId('workspace-sort-key-created'))

      expect(screen.getByTestId('workspace-sort-direction-desc').textContent).toContain('Newest first')
    })
  })

  it('offers Reveal active workspace only while this section holds it', () => {
    renderMenu({ hasActiveWorkspace: () => true })
    openMenu()
    expect(inProgressItems()).toContain('Reveal active workspace')
  })

  it('disables Collapse all and Expand all for an empty section', () => {
    renderMenu({ getWorkspaceIds: () => [] })
    openMenu()

    const popover = screen.getByTestId('sidebar-section-menu-workspaces_in_progress-popover')
    // `queryAllByRole` skips disabled items, so read the buttons directly.
    const labels = [...popover.querySelectorAll('button')].map(b => `${b.textContent?.trim()}:${b.disabled}`)
    expect(labels).toContain('Collapse all:true')
    expect(labels).toContain('Expand all:true')
  })

  describe('the repository group', () => {
    it('lists nothing until the menu is OPENED', () => {
      // The gate is a memo, not a `<Show>`: a `<Show>` hides the DOM and keeps
      // the subscriptions, so every section's menu would re-scan every tab on
      // every reactive tick while closed.
      const { store, tabs } = tabsAndStore(['/home/me/leapmux'])
      renderMenu({ getTabs: () => tabs, repoGitStore: store })

      expect(screen.queryByText('New workspace in')).not.toBeInTheDocument()

      openMenu()
      expect(screen.getByText('New workspace in')).toBeInTheDocument()
    })

    it('renders neither the group nor its header when there is no repository', () => {
      renderMenu()
      openMenu()
      expect(screen.queryByText('New workspace in')).not.toBeInTheDocument()
    })

    it('offers one row per repository, and reports its start point', () => {
      const { store, tabs } = tabsAndStore(['/home/me/leapmux', '/home/me/api'])
      const onNewWorkspace = vi.fn()
      renderMenu({ getTabs: () => tabs, repoGitStore: store, onNewWorkspace })
      openMenu()

      expect(inProgressItems()).toEqual(expect.arrayContaining(['leapmux', 'api']))

      fireEvent.click(screen.getByText('leapmux'))
      expect(onNewWorkspace).toHaveBeenCalledWith({
        kind: 'repo',
        workerId: 'w1',
        gitToplevel: '/home/me/leapmux',
        isWorktree: false,
        currentBranch: 'main',
      })
    })

    it('reports a bare directory start point for the plain New workspace item', () => {
      const onNewWorkspace = vi.fn()
      renderMenu({ onNewWorkspace })
      openMenu()
      fireEvent.click(screen.getByTestId('sidebar-new-workspace'))
      expect(onNewWorkspace).toHaveBeenCalledWith({ kind: 'directory' })
    })

    it('shows the remembered git mode as the row detail, in the dialog vocabulary', () => {
      const { store, tabs } = tabsAndStore(['/home/me/leapmux'])
      rememberStickyGitMode(gitModeStickyKey('w1', '/home/me/leapmux'), GitMode.CreateWorktree)
      renderMenu({ getTabs: () => tabs, repoGitStore: store })
      openMenu()

      expect(screen.getByRole('menuitem', { name: /leapmux/, hidden: true }).textContent)
        .toContain('Create new worktree')
    })

    it('shows NO detail for a repository nothing is remembered for', () => {
      // A row that says "Use current state" for a repository nobody has started
      // a workspace in states a default as though it were a memory.
      const { store, tabs } = tabsAndStore(['/home/me/leapmux'])
      renderMenu({ getTabs: () => tabs, repoGitStore: store })
      openMenu()

      expect(screen.getByRole('menuitem', { name: /leapmux/, hidden: true }).textContent?.trim())
        .toBe('leapmux')
    })

    it('omits a repository whose worker is offline', () => {
      const { store, tabs } = tabsAndStore(['/home/me/leapmux'])
      renderMenu({ getTabs: () => tabs, repoGitStore: store, isWorkerOnline: () => false })
      openMenu()

      expect(screen.queryByText('New workspace in')).not.toBeInTheDocument()
    })
  })

  describe('the archived section', () => {
    it('offers no create item at all', () => {
      // A workspace created into Archived is born read-only, which is why
      // `canAddToSection` refuses it.
      const { store, tabs } = tabsAndStore(['/home/me/leapmux'])
      renderMenu({ section: ARCHIVED, canCreate: false, getTabs: () => tabs, repoGitStore: store })
      openMenu(ARCHIVED)

      const items = itemsOf('sidebar-section-menu-workspaces_archived-popover')
      expect(items).not.toContain('New workspace...')
      expect(screen.queryByText('New workspace in')).not.toBeInTheDocument()
      // And the In-progress-only test id stays off it, or every Playwright
      // lookup for it fails strict mode.
      expect(screen.queryByTestId('sidebar-new-workspace')).not.toBeInTheDocument()
    })

    it('offers the bulk operations', () => {
      renderMenu({ section: ARCHIVED, canCreate: false })
      openMenu(ARCHIVED)

      expect(itemsOf('sidebar-section-menu-workspaces_archived-popover')).toEqual([
        'Sort by',
        'Collapse all',
        'Expand all',
        'Unarchive all',
        'Empty archive...',
        'New section...',
      ])
    })

    it('hides the bulk operations while the archive is empty', () => {
      renderMenu({ section: ARCHIVED, canCreate: false, getWorkspaceIds: () => [] })
      openMenu(ARCHIVED)

      const items = itemsOf('sidebar-section-menu-workspaces_archived-popover')
      expect(items).not.toContain('Unarchive all')
      expect(items).not.toContain('Empty archive...')
    })

    it('offers no rename or delete of the section itself', () => {
      // The hub refuses both for a built-in section, so showing them would be
      // an item that always fails.
      renderMenu({ section: ARCHIVED, canCreate: false })
      openMenu(ARCHIVED)

      const items = itemsOf('sidebar-section-menu-workspaces_archived-popover')
      expect(items).not.toContain('Rename section...')
      expect(items).not.toContain('Delete section...')
    })
  })

  describe('a custom section', () => {
    it('offers rename and delete of the section itself', () => {
      renderMenu({ section: CUSTOM })
      openMenu(CUSTOM)

      const items = itemsOf('sidebar-section-menu-workspaces_custom-popover')
      expect(items).toContain('Rename section...')
      expect(items).toContain('Delete section...')
    })

    it('reports each section action', () => {
      const onNewSection = vi.fn()
      const onRenameSection = vi.fn()
      const onDeleteSection = vi.fn()
      renderMenu({ section: CUSTOM, onNewSection, onRenameSection, onDeleteSection })
      openMenu(CUSTOM)

      fireEvent.click(screen.getByText('New section...'))
      fireEvent.click(screen.getByText('Rename section...'))
      fireEvent.click(screen.getByText('Delete section...'))
      expect(onNewSection).toHaveBeenCalledOnce()
      expect(onRenameSection).toHaveBeenCalledOnce()
      expect(onDeleteSection).toHaveBeenCalledOnce()
    })
  })

  it('reports the two bulk view actions', () => {
    const onCollapseAll = vi.fn()
    const onExpandAll = vi.fn()
    renderMenu({ onCollapseAll, onExpandAll })
    openMenu()

    fireEvent.click(screen.getByText('Collapse all'))
    fireEvent.click(screen.getByText('Expand all'))
    expect(onCollapseAll).toHaveBeenCalledOnce()
    expect(onExpandAll).toHaveBeenCalledOnce()
  })
})
