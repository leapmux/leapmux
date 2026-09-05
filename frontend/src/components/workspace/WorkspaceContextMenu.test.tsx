import type { Tab } from '~/stores/tab.types'
import { create } from '@bufbuild/protobuf'
import { cleanup, fireEvent, render, screen, within } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { WorkspaceContextMenu } from '~/components/workspace/WorkspaceContextMenu'
import { SectionSchema, SectionType, Sidebar } from '~/generated/proto/leapmux/v1/section_pb'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { repoKey } from '~/stores/repoGit'
import { createRepoGitStore } from '~/stores/repoGit.store'
import { stubWorkspaceStartActions } from '~/test-support/branchMenu'
import { withPreferences } from '~/test-support/preferencesProvider'

const copyTextMock = vi.hoisted(() => vi.fn())
vi.mock('~/lib/clipboard', async importOriginal => ({
  ...await importOriginal<typeof import('~/lib/clipboard')>(),
  copyTextToClipboard: (...args: unknown[]) => copyTextMock(...args),
}))

/**
 * The REAL `DropdownMenu`, not a mock.
 *
 * `vitest.setup.ts` stubs `showPopover` / `hidePopover` / `:popover-open`, and
 * `DropdownMenu.test.tsx` drives 40+ open/close tests against those stubs -- so
 * the "jsdom lacks the popover API" mock this file used to carry was obsolete.
 * It also actively hid three things: it dropped `onToggle` on the floor, so a
 * missing `onToggle={setMenuOpen}` (the likeliest bug in a menu whose whole
 * content depends on it) passed green; it FLATTENED every submenu into the
 * parent's item list, so an order assertion could not fail on a nesting
 * mistake; and it nulled out the item-content components this menu needs.
 */

function makeSection(id: string, name: string, sectionType: SectionType) {
  return create(SectionSchema, { id, name, position: '', sectionType, sidebar: Sidebar.LEFT })
}

const IN_PROGRESS = makeSection('sec-ip', 'In Progress', SectionType.WORKSPACES_IN_PROGRESS)
const CUSTOM = makeSection('sec-custom', 'My Section', SectionType.WORKSPACES_CUSTOM)
const ARCHIVED = makeSection('sec-archived', 'Archived', SectionType.WORKSPACES_ARCHIVED)

interface RepoSeed {
  workerId?: string
  toplevel: string
  originUrl?: string
}

function tabsAndStore(seeds: readonly RepoSeed[]) {
  const store = createRepoGitStore()
  const tabs: Tab[] = []
  seeds.forEach((seed, i) => {
    const workerId = seed.workerId ?? 'w1'
    store.upsert(repoKey(workerId, seed.toplevel), {
      workerId,
      toplevel: seed.toplevel,
      branch: 'main',
      originUrl: seed.originUrl ?? '',
    })
    tabs.push({
      id: `t${i}`,
      workspaceId: 'ws-1',
      type: TabType.AGENT,
      workerId,
      gitToplevel: seed.toplevel,
    } as Tab)
  })
  return { store, tabs }
}

function noop() {}

function renderMenu(overrides: Partial<Parameters<typeof WorkspaceContextMenu>[0]> = {}) {
  const { store, tabs } = tabsAndStore([])
  const props = {
    workspaceId: 'ws-1',
    workspaceTitle: 'gentle-amber-fox',
    sectionName: 'In Progress',
    isArchived: false,
    sections: [IN_PROGRESS, CUSTOM],
    currentSectionId: 'sec-ip',
    getTabs: () => tabs,
    repoGitStore: store,
    onRename: noop,
    onMoveTo: noop as (sectionId: string) => void,
    onArchive: noop,
    onUnarchive: noop,
    onDelete: noop,
    ...overrides,
  }
  render(withPreferences(() => <WorkspaceContextMenu {...props} />))
  return props
}

/** Open the menu, as a user does. Every item below depends on it. */
function openMenu() {
  fireEvent.click(screen.getByTestId('workspace-row-menu-trigger'))
}

/**
 * The item labels of ONE popover, in order. Submenus are not flattened in.
 *
 * `hidden: true` because a closed popover is outside the accessibility tree,
 * and the stubs in `vitest.setup.ts` toggle a data attribute rather than the
 * real popover state -- so jsdom keeps applying the UA `display: none`. The
 * info block is dropped: it is a `menuitem` (deliberately -- see
 * `MenuInfoButton`), but it is a block of rows and not a command.
 */
function itemsOf(testId: string): string[] {
  return within(screen.getByTestId(testId))
    .queryAllByRole('menuitem', { hidden: true })
    .filter(el => el.dataset.testid !== 'workspace-info-button')
    .map(el => el.textContent?.trim() ?? '')
}

describe('workspaceContextMenu', () => {
  it('reads no tab of any workspace until it is OPENED', () => {
    // `DropdownMenu` renders children eagerly, so the items are in the DOM
    // either way -- what `onToggle` controls is the WORK. One of these mounts per
    // workspace row, and it serves the right-click menu too, so an ungated
    // build walks every tab of every workspace on every reactive tick.
    //
    // The repository-derived label is the observable half of that gate, and a
    // dropped `onToggle` leaves it stuck on the no-repository shape forever.
    const { store, tabs } = tabsAndStore([{ toplevel: '/home/me/leapmux' }])
    renderMenu({ getTabs: () => tabs, repoGitStore: store })

    // No repository resolved yet, so the menu shows the no-target shape.
    expect(screen.getByTestId('workspace-new-agent').textContent).toBe('New agent...')
    expect(screen.queryByTestId('workspace-info-button')).not.toBeInTheDocument()

    openMenu()

    // Now it has one, so the no-target items are gone and the repository's
    // own block replaces them.
    expect(screen.queryByTestId('workspace-new-agent')).not.toBeInTheDocument()
    expect(itemsOf('workspace-context-menu')).toContain('Copy repository path')
    expect(screen.getByTestId('workspace-info-button')).toBeInTheDocument()
  })

  it('offers rename, move, archive and delete on a live workspace, in that order', () => {
    renderMenu()
    openMenu()

    expect(itemsOf('workspace-context-menu')).toEqual([
      'New agent...',
      'New terminal...',
      'Rename',
      'Move to',
      'Archive',
      'Delete',
    ])
  })

  // Repository FIRST, then the actions on it. The previous shape asked for the
  // action first, which split one repository's actions across three submenus.
  it('puts one repository\'s every action in one flat block, in order', () => {
    const { store, tabs } = tabsAndStore([
      { toplevel: '/home/me/leapmux', originUrl: 'https://example.com/o/r.git' },
    ])
    renderMenu({ getTabs: () => tabs, repoGitStore: store, isLocalWorkerFn: () => false })
    openMenu()

    // The flat shape names the repository in the item, because nothing else on
    // screen does -- and that is the label this menu already carried.
    expect(itemsOf('workspace-context-menu')).toEqual([
      'New agent in example.com/o/r...',
      'New terminal in example.com/o/r...',
      'Copy repository URL',
      'Copy repository path',
      'Rename',
      'Move to',
      'Archive',
      'Delete',
    ])
  })

  it('hides rename on an archived workspace', () => {
    // `isWorkspaceMutatable` says archival is the one thing that blocks
    // mutation, and the tab bar already routes the sibling operation through
    // `canRenameTab(archived, tab)`. This row was the one surface ignoring it.
    renderMenu({ isArchived: true, sections: [IN_PROGRESS, ARCHIVED], currentSectionId: 'sec-archived' })
    openMenu()

    const items = itemsOf('workspace-context-menu')
    expect(items).not.toContain('Rename')
    expect(items).toContain('Delete')
  })

  it('hides the tab-creation items on an archived workspace', () => {
    renderMenu({ isArchived: true, sections: [IN_PROGRESS, ARCHIVED], currentSectionId: 'sec-archived' })
    openMenu()

    const items = itemsOf('workspace-context-menu')
    expect(items.some(i => i.startsWith('New agent'))).toBe(false)
    expect(items.some(i => i.startsWith('New terminal'))).toBe(false)
  })

  it('keeps Move to on an archived workspace', () => {
    // Moving writes `workspace_section_items.section_id`, which is not a
    // mutation OF the workspace -- and it is the only way to unarchive into a
    // SPECIFIC custom section, because Unarchive always targets In progress.
    renderMenu({ isArchived: true, sections: [IN_PROGRESS, CUSTOM, ARCHIVED], currentSectionId: 'sec-archived' })
    openMenu()

    expect(itemsOf('workspace-context-menu')).toContain('Move to')
  })

  it('swaps Archive for Unarchive once archived', () => {
    renderMenu({ isArchived: true, sections: [IN_PROGRESS, ARCHIVED], currentSectionId: 'sec-archived' })
    openMenu()

    const items = itemsOf('workspace-context-menu')
    expect(items).toContain('Unarchive')
    expect(items).not.toContain('Archive')
  })

  describe('move to', () => {
    it('lists every other workspace section, and no other kind', () => {
      renderMenu({
        sections: [
          IN_PROGRESS,
          makeSection('sec-a', 'Alpha', SectionType.WORKSPACES_CUSTOM),
          ARCHIVED,
          makeSection('sec-files', 'Files', SectionType.FILES),
        ],
        currentSectionId: 'sec-ip',
      })
      openMenu()
      fireEvent.click(screen.getByTestId('workspace-move-to'))

      expect(itemsOf('workspace-move-to-popover')).toEqual(['Alpha'])
    })

    it('is absent when nothing else can hold the workspace', () => {
      renderMenu({ sections: [IN_PROGRESS, ARCHIVED], currentSectionId: 'sec-ip' })
      openMenu()

      expect(itemsOf('workspace-context-menu')).not.toContain('Move to')
      expect(screen.queryByTestId('workspace-move-to')).not.toBeInTheDocument()
    })

    it('reports the chosen section', () => {
      const onMoveTo = vi.fn()
      renderMenu({ onMoveTo })
      openMenu()
      fireEvent.click(screen.getByTestId('workspace-move-to'))
      fireEvent.click(within(screen.getByTestId('workspace-move-to-popover')).getByText('My Section'))

      expect(onMoveTo).toHaveBeenCalledWith('sec-custom')
    })
  })

  describe('tab creation, by repository count', () => {
    it('offers a bare item, with no target, for a workspace with no repository', () => {
      // The row that most needs a way in: a freshly created workspace has no
      // tabs at all.
      const startActions = stubWorkspaceStartActions()
      renderMenu({ startActions })
      openMenu()
      fireEvent.click(screen.getByTestId('workspace-new-agent'))

      expect(screen.getByTestId('workspace-new-agent').textContent).toBe('New agent...')
      expect(startActions.onNewAgentAt).toHaveBeenCalledWith({ workspaceId: 'ws-1', workerId: '', workingDir: '' })
    })

    it('renders FLAT for one repository, with no submenu to click through', () => {
      const { store, tabs } = tabsAndStore([{ toplevel: '/home/me/leapmux' }])
      const startActions = stubWorkspaceStartActions()
      renderMenu({ getTabs: () => tabs, repoGitStore: store, startActions })
      openMenu()

      expect(screen.queryByTestId('workspace-repository-leapmux')).not.toBeInTheDocument()

      // No origin on this fixture, so the label falls back to the directory
      // name -- which is what `repoKeyAndLabel` does for a local-only clone.
      fireEvent.click(screen.getByRole('menuitem', { name: 'New agent in leapmux...', hidden: true }))
      expect(startActions.onNewAgentAt).toHaveBeenCalledWith({
        workspaceId: 'ws-1',
        workerId: 'w1',
        workingDir: '/home/me/leapmux',
      })
    })

    it('renders one submenu per repository for two repositories', () => {
      const { store, tabs } = tabsAndStore([
        { toplevel: '/home/me/alpha' },
        { toplevel: '/home/me/beta' },
      ])
      const startActions = stubWorkspaceStartActions()
      renderMenu({ getTabs: () => tabs, repoGitStore: store, startActions })
      openMenu()

      expect(itemsOf('workspace-context-menu').toSorted())
        .toEqual(['Archive', 'Delete', 'Move to', 'Rename', 'alpha', 'beta'])

      fireEvent.click(screen.getByTestId('workspace-repository-beta'))
      fireEvent.click(
        within(screen.getByTestId('workspace-repository-beta-popover'))
          .getByRole('menuitem', { name: 'New terminal...', hidden: true }),
      )
      expect(startActions.onNewTerminalAt).toHaveBeenCalledWith({
        workspaceId: 'ws-1',
        workerId: 'w1',
        workingDir: '/home/me/beta',
      })
    })

    // Every repository's actions now live together, so the two blocks a
    // submenu holds are the tab-creation pair and the shared repository block.
    it('gives each repository submenu the same block the flat shape has', () => {
      const { store, tabs } = tabsAndStore([
        { toplevel: '/home/me/alpha', originUrl: 'https://example.com/o/a.git' },
        { toplevel: '/home/me/beta' },
      ])
      renderMenu({ getTabs: () => tabs, repoGitStore: store, isLocalWorkerFn: () => false })
      openMenu()
      // Labelled by the formatted origin, which is what `repoKeyAndLabel`
      // prefers over the directory name once a repository has a remote.
      fireEvent.click(screen.getByTestId('workspace-repository-example-com-o-a'))

      expect(itemsOf('workspace-repository-example-com-o-a-popover')).toEqual([
        'New agent...',
        'New terminal...',
        'Copy repository URL',
        'Copy repository path',
      ])
    })

    // "No checkout" and "no REACHABLE checkout" are different states, and the
    // no-target item is only right for the first. It means "follow the current
    // tab context", so offering it for the second started an agent somewhere
    // else entirely -- a machine the user never picked.
    // An offline repository is DISABLED, not hidden. It used to vanish from
    // the tab-creation submenu while still appearing under Repository, so one
    // repository was in one menu and not the other.
    it('disables only the tab-creation items when the repository\'s worker is offline', () => {
      const { store, tabs } = tabsAndStore([
        { toplevel: '/home/me/leapmux', originUrl: 'https://example.com/o/r.git' },
      ])
      const startActions = stubWorkspaceStartActions()
      renderMenu({
        getTabs: () => tabs,
        repoGitStore: store,
        isWorkerOnline: () => false,
        isLocalWorkerFn: () => false,
        startActions,
      })
      openMenu()

      const newAgent = screen.getByRole('menuitem', { name: 'New agent in example.com/o/r...', hidden: true })
      expect(newAgent).toBeDisabled()
      // The repository block stays usable: it copies text the browser already
      // holds, which needs no Worker at all. Read before the click below,
      // because a click on any item dismisses the whole menu.
      expect(screen.getByRole('menuitem', { name: 'Copy repository path', hidden: true })).not.toBeDisabled()
      expect(screen.getByRole('menuitem', { name: 'Copy repository URL', hidden: true })).not.toBeDisabled()

      fireEvent.click(newAgent)
      expect(startActions.onNewAgentAt).not.toHaveBeenCalled()
    })

    // The no-target item means "follow the current tab context", so a
    // workspace that HAS a repository must never fall back to it: it would
    // start an agent on a machine the user never picked.
    it('never offers the no-target item once a repository exists', () => {
      const { store, tabs } = tabsAndStore([{ toplevel: '/home/me/leapmux' }])
      renderMenu({ getTabs: () => tabs, repoGitStore: store, isWorkerOnline: () => false })
      openMenu()

      expect(screen.queryByTestId('workspace-new-agent')).not.toBeInTheDocument()
      expect(screen.queryByTestId('workspace-new-terminal')).not.toBeInTheDocument()
    })

    // The other half: a workspace with NO checkout at all keeps the no-target
    // item, because a freshly created workspace has no tabs and that row most
    // needs a way in.
    it('keeps the no-target item for a workspace with no checkout', () => {
      const startActions = stubWorkspaceStartActions()
      renderMenu({ isWorkerOnline: () => false, startActions })
      openMenu()

      const item = screen.getByTestId('workspace-new-agent') as HTMLButtonElement
      expect(item.textContent).toBe('New agent...')
      expect(item).not.toBeDisabled()
      fireEvent.click(item)
      expect(startActions.onNewAgentAt).toHaveBeenCalledWith({
        workspaceId: 'ws-1',
        workerId: '',
        workingDir: '',
      })
    })
  })

  describe('the Repository block', () => {
    it('offers Copy repository URL only for a repository that has an origin', () => {
      const { store, tabs } = tabsAndStore([{ toplevel: '/home/me/leapmux' }])
      renderMenu({ getTabs: () => tabs, repoGitStore: store, isLocalWorkerFn: () => false })
      openMenu()

      const items = itemsOf('workspace-context-menu')
      expect(items).not.toContain('Copy repository URL')
      expect(items).toContain('Copy repository path')
    })

    // Reveal opens the LOCAL file manager, so a remote worker's absolute path
    // either does not exist here or -- worse -- exists and is a different
    // directory. The PATH is still worth copying: pasting it into an ssh
    // session on the machine that has it is exactly the use.
    it('offers Reveal in file manager only for a LOCAL worker', () => {
      const { store, tabs } = tabsAndStore([{ toplevel: '/home/me/leapmux' }])
      renderMenu({ getTabs: () => tabs, repoGitStore: store, isLocalWorkerFn: () => false })
      openMenu()
      expect(itemsOf('workspace-context-menu')).not.toContain('Reveal in file manager')

      cleanup()
      renderMenu({ getTabs: () => tabs, repoGitStore: store, isLocalWorkerFn: () => true })
      openMenu()
      expect(itemsOf('workspace-context-menu')).toContain('Reveal in file manager')
    })

    it('copies the checkout path, not the origin URL, from Copy repository path', async () => {
      const { store, tabs } = tabsAndStore([
        { toplevel: '/home/me/leapmux', originUrl: 'https://example.com/o/r.git' },
      ])
      renderMenu({ getTabs: () => tabs, repoGitStore: store, isLocalWorkerFn: () => false })
      openMenu()
      fireEvent.click(screen.getByRole('menuitem', { name: 'Copy repository path', hidden: true }))

      await vi.waitFor(() => expect(copyTextMock).toHaveBeenCalledWith('/home/me/leapmux'))
    })

    it('stays available on an ARCHIVED workspace, because it mutates nothing', () => {
      const { store, tabs } = tabsAndStore([
        { toplevel: '/home/me/leapmux', originUrl: 'https://example.com/o/r.git' },
      ])
      renderMenu({
        getTabs: () => tabs,
        repoGitStore: store,
        isArchived: true,
        isLocalWorkerFn: () => false,
        sections: [IN_PROGRESS, ARCHIVED],
        currentSectionId: 'sec-archived',
      })
      openMenu()

      // The read-only rows survive; the two that open a tab do not.
      const items = itemsOf('workspace-context-menu')
      expect(items).toContain('Copy repository URL')
      expect(items).toContain('Copy repository path')
      expect(items).not.toContain('New agent...')
      expect(items).not.toContain('New terminal...')
    })
  })

  describe('the info block', () => {
    it('reports the workspace, its section and its tab count', () => {
      const { store, tabs } = tabsAndStore([{ toplevel: '/home/me/leapmux' }])
      renderMenu({ getTabs: () => tabs, repoGitStore: store })
      openMenu()

      const info = screen.getByTestId('workspace-info-button')
      expect(info.textContent).toContain('gentle-amber-fox')
      expect(info.textContent).toContain('In Progress')
      expect(info.textContent).toContain('leapmux')
    })

    it('omits the repository row when the workspace spans more than one', () => {
      const { store, tabs } = tabsAndStore([
        { toplevel: '/home/me/alpha' },
        { toplevel: '/home/me/beta' },
      ])
      renderMenu({ getTabs: () => tabs, repoGitStore: store })
      openMenu()

      expect(screen.getByTestId('workspace-info-button').textContent).not.toContain('Repository')
    })
  })

  // One representative for the six row menus. The other five wrappers forward
  // the same prop through the same two lines, and `DropdownMenu` owns
  // everything after -- covered in ~/components/common/DropdownMenu.test.tsx.
  it('opens from a right-click on the row it is given', () => {
    const row = document.createElement('div')
    document.body.append(row)
    renderMenu({ contextMenuFor: () => row })

    // Fake timers because the gesture defers the open by a tick -- see
    // `attachContextMenuGesture`, which opens after the platform's own
    // `contextmenu` so light-dismiss cannot eat the menu it just opened.
    vi.useFakeTimers()
    try {
      // The info block is the content that depends on the open state, so its presence is what says
      // the menu really opened rather than merely being mounted.
      expect(screen.queryByTestId('workspace-info-button')).not.toBeInTheDocument()
      row.dispatchEvent(new MouseEvent('contextmenu', { clientX: 10, clientY: 10, bubbles: true, cancelable: true }))
      vi.runAllTimers()
      expect(screen.getByTestId('workspace-info-button')).toBeInTheDocument()
    }
    finally {
      vi.useRealTimers()
      row.remove()
    }
  })
})
